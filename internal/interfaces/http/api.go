package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/access"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
)

type Transactions interface {
	FindByProviderID(context.Context, string, string) (financial.Record, error)
	FindFinancial(context.Context, string, string) (financial.Record, error)
}
type Ready interface{ Check(context.Context) error }
type API struct {
	processor    *financial.Processor
	wallets      *financial.WalletService
	transactions Transactions
	verifier     access.Verifier
	ready        Ready
}

func NewAPI(p *financial.Processor, w *financial.WalletService, t Transactions, v access.Verifier, r Ready) *API {
	return &API{p, w, t, v, r}
}

type MoneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func moneyDTO(m money.Money) MoneyDTO {
	a, _ := m.Amount()
	c, _ := m.Currency()
	return MoneyDTO{a, c}
}
func optionalMoney(m *money.Money) *MoneyDTO {
	if m == nil {
		return nil
	}
	v := moneyDTO(*m)
	return &v
}
func respond(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func fail(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]string{"error": code})
}
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(json.RawMessage)); err != io.EOF {
		return financial.ErrInvalidInput
	}
	return nil
}
func (a *API) protected(internal bool, next func(http.ResponseWriter, *http.Request, access.Principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, 401, "UNAUTHENTICATED")
			return
		}
		fields := strings.Fields(values[0])
		if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
			fail(w, 401, "UNAUTHENTICATED")
			return
		}
		principal, err := a.verifier.Verify(r.Context(), fields[1])
		if err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, 401, "UNAUTHENTICATED")
			return
		}
		if (internal && !principal.Internal) || (!internal && principal.ProviderID == "") {
			fail(w, 403, "FORBIDDEN")
			return
		}
		next(w, r, principal)
	}
}
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if a.ready.Check(ctx) != nil {
			fail(w, 503, "NOT_READY")
			return
		}
		respond(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /wagering/transactions", a.protected(false, a.process))
	mux.HandleFunc("GET /wagering/transactions/{id}", a.protected(false, a.transaction))
	mux.HandleFunc("GET /providers/{provider}/wagering/transactions/{external}", a.protected(false, a.transaction))
	mux.HandleFunc("POST /wallets", a.protected(true, a.createWallet))
	mux.HandleFunc("GET /wallets/{id}", a.protected(true, a.getWallet))
	mux.HandleFunc("GET /wallets/{id}/ledger", a.protected(true, a.ledger))
	mux.HandleFunc("POST /wallets/{id}/reconciliation", a.protected(true, a.reconcile))
	mux.HandleFunc("GET /metrics", a.protected(true, func(w http.ResponseWriter, r *http.Request, p access.Principal) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "wallet_reconciliation_divergences_total %d\n", a.wallets.Divergences())
	}))
	return mux
}
func (a *API) process(w http.ResponseWriter, r *http.Request, p access.Principal) {
	var body struct {
		ProviderID            string   `json:"providerId"`
		ExternalTransactionID string   `json:"externalTransactionId"`
		PlayerID              string   `json:"playerId"`
		WalletID              string   `json:"walletId"`
		RoundID               string   `json:"roundId"`
		GameID                string   `json:"gameId"`
		Kind                  wt.Kind  `json:"kind"`
		Money                 MoneyDTO `json:"money"`
		Reference             *string  `json:"referenceExternalTransactionId"`
	}
	if decode(w, r, &body) != nil {
		fail(w, 400, "INVALID_JSON")
		return
	}
	if body.ProviderID != p.ProviderID {
		fail(w, 403, "PROVIDER_MISMATCH")
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || keys[0] == "" {
		fail(w, 400, "INVALID_IDEMPOTENCY_KEY")
		return
	}
	m, err := money.Parse(body.Money.Amount, body.Money.Currency)
	if err != nil {
		fail(w, 400, "INVALID_MONEY")
		return
	}
	req := financial.ProcessRequest{ProviderID: p.ProviderID, ExternalTransactionID: body.ExternalTransactionID, IdempotencyKey: keys[0], PlayerID: body.PlayerID, WalletID: body.WalletID, RoundID: body.RoundID, GameID: body.GameID, Kind: body.Kind, Money: m}
	if body.Reference != nil {
		req.ReferenceExternalTransactionID = *body.Reference
	}
	result, err := a.processor.Process(r.Context(), req)
	if err != nil {
		mapError(w, err)
		return
	}
	status := 200
	if result.Status == wt.REJECTED {
		status = 422
	}
	if result.Status == wt.PENDING || result.Status == wt.PENDING_REFERENCE {
		status = 202
	}
	respond(w, status, struct {
		TransactionID string    `json:"transactionId"`
		Status        wt.Status `json:"status"`
		Balance       *MoneyDTO `json:"balance"`
		Replay        bool      `json:"idempotentReplay"`
		Failure       *string   `json:"failureCode"`
	}{result.TransactionID, result.Status, optionalMoney(result.Balance), result.IdempotentReplay, optionalString(string(result.FailureCode))})
}
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func (a *API) transaction(w http.ResponseWriter, r *http.Request, p access.Principal) {
	var record financial.Record
	var err error
	if provider := r.PathValue("provider"); provider != "" {
		if provider != p.ProviderID {
			fail(w, 404, "NOT_FOUND")
			return
		}
		record, err = a.transactions.FindFinancial(r.Context(), p.ProviderID, r.PathValue("external"))
	} else {
		record, err = a.transactions.FindByProviderID(r.Context(), p.ProviderID, r.PathValue("id"))
	}
	if err != nil {
		mapError(w, err)
		return
	}
	t := record.Transaction
	respond(w, 200, map[string]any{"transactionId": t.ID(), "providerId": t.ProviderID(), "externalTransactionId": t.ExternalTransactionID(), "playerId": t.PlayerID(), "walletId": t.WalletID(), "roundId": t.RoundID(), "gameId": t.GameID(), "kind": t.Kind(), "money": moneyDTO(t.Money()), "referenceExternalTransactionId": optionalString(t.ReferenceExternalTransactionID()), "status": t.Status(), "failureCode": optionalString(string(t.FailureCode())), "balance": optionalMoney(record.Balance), "createdAt": t.CreatedAt(), "updatedAt": t.UpdatedAt()})
}
func walletDTO(v wallet.Wallet) any {
	return map[string]any{"id": v.ID(), "playerId": v.PlayerID(), "balance": moneyDTO(v.Balance()), "version": v.Version()}
}
func (a *API) createWallet(w http.ResponseWriter, r *http.Request, _ access.Principal) {
	var body struct {
		PlayerID string   `json:"playerId"`
		Balance  MoneyDTO `json:"initialBalance"`
	}
	if decode(w, r, &body) != nil {
		fail(w, 400, "INVALID_JSON")
		return
	}
	m, err := money.Parse(body.Balance.Amount, body.Balance.Currency)
	if err != nil {
		fail(w, 400, "INVALID_MONEY")
		return
	}
	v, err := a.wallets.Create(r.Context(), body.PlayerID, m)
	if err != nil {
		mapError(w, err)
		return
	}
	respond(w, 201, walletDTO(v))
}
func (a *API) getWallet(w http.ResponseWriter, r *http.Request, _ access.Principal) {
	v, err := a.wallets.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	respond(w, 200, walletDTO(v))
}
func (a *API) ledger(w http.ResponseWriter, r *http.Request, _ access.Principal) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			fail(w, 400, "INVALID_INPUT")
			return
		}
	}
	page, err := a.wallets.Ledger(r.Context(), r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		mapError(w, err)
		return
	}
	items := []any{}
	for _, e := range page.Entries {
		items = append(items, map[string]any{"id": e.ID(), "walletId": e.WalletID(), "transactionId": e.TransactionID(), "direction": e.Direction(), "money": moneyDTO(e.Money()), "balanceBefore": moneyDTO(e.BalanceBefore()), "balanceAfter": moneyDTO(e.BalanceAfter()), "createdAt": e.CreatedAt()})
	}
	respond(w, 200, map[string]any{"items": items, "nextCursor": optionalString(page.NextCursor)})
}
func (a *API) reconcile(w http.ResponseWriter, r *http.Request, _ access.Principal) {
	v, err := a.wallets.Reconcile(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	respond(w, 200, map[string]any{"walletId": v.WalletID, "storedBalance": moneyDTO(v.Stored), "calculatedBalance": moneyDTO(v.Calculated), "difference": moneyDTO(v.Difference), "consistent": v.Consistent, "checkedEntries": v.CheckedEntries})
}
func mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, financial.ErrNotFound):
		fail(w, 404, "NOT_FOUND")
	case errors.Is(err, financial.ErrConflict), errors.Is(err, financial.ErrWalletExists):
		fail(w, 409, "CONFLICT")
	case errors.Is(err, financial.ErrInvalidInput), errors.Is(err, financial.ErrInvalidContext), errors.Is(err, financial.ErrUnsupportedOperation), errors.Is(err, wallet.ErrInvalidPlayerID), errors.Is(err, wallet.ErrInvalidInitialBalance):
		fail(w, 400, "INVALID_INPUT")
	case errors.Is(err, financial.ErrInvalidPersistedData):
		fail(w, 500, "INTERNAL_ERROR")
	default:
		fail(w, 503, "SERVICE_UNAVAILABLE")
	}
}
