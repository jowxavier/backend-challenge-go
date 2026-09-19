//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/access"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	httpapi "github.com/jowxavier/backend-challenge-go/internal/interfaces/http"
)

type httpVerifier struct{}

func (httpVerifier) Verify(_ context.Context, token string) (access.Principal, error) {
	switch token {
	case "internal":
		return access.Principal{Internal: true}, nil
	case "provider":
		return access.Principal{ProviderID: "provider"}, nil
	case "other":
		return access.Principal{ProviderID: "other"}, nil
	}
	return access.Principal{}, access.ErrUnauthenticated
}

type healthy struct{}

func (healthy) Check(context.Context) error { return nil }
func httpCall(t *testing.T, h http.Handler, method, path, token, key, body string, want int) map[string]json.RawMessage {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	var data map[string]json.RawMessage
	must(t, json.Unmarshal(w.Body.Bytes(), &data))
	return data
}
func value(t *testing.T, m map[string]json.RawMessage, k string) string {
	t.Helper()
	var s string
	must(t, json.Unmarshal(m[k], &s))
	return s
}
func TestHTTPFinancialOperations(t *testing.T) {
	pool := testPool(t)
	svc := financial.NewWalletService(NewWalletAPIStore(pool), time.Now)
	api := httpapi.NewAPI(newProcessor(t, NewRunner(pool)), svc, NewWagerTransactionRepository(pool), httpVerifier{}, healthy{}).Handler()
	created := httpCall(t, api, "POST", "/wallets", "internal", "", `{"playerId":"p","initialBalance":{"amount":"100.00","currency":"BRL"}}`, 201)
	id := value(t, created, "id")
	body := func(ext, kind, amount, reference string) string {
		return fmt.Sprintf(`{"providerId":"provider","externalTransactionId":%q,"playerId":"p","walletId":%q,"roundId":"r","gameId":"g","kind":%q,"money":{"amount":%q,"currency":"BRL"},"referenceExternalTransactionId":%s}`, ext, id, kind, amount, reference)
	}
	for _, tc := range []struct {
		id, kind, amount, ref string
		want                  int
	}{{"bet", "BET", "10", "null", 200}, {"win", "WIN", "3", "null", 200}, {"loss", "LOSS", "0", "null", 200}, {"refund", "REFUND", "10", `"bet"`, 200}, {"rollback", "ROLLBACK", "3", `"win"`, 200}, {"referenced-win", "WIN", "2", `"bet"`, 200}, {"pending", "REFUND", "1", `"missing"`, 202}, {"rejected", "BET", "1000", "null", 422}} {
		got := httpCall(t, api, "POST", "/wagering/transactions", "provider", tc.id, body(tc.id, tc.kind, tc.amount, tc.ref), tc.want)
		txid := value(t, got, "transactionId")
		httpCall(t, api, "GET", "/wagering/transactions/"+txid, "provider", "", "", 200)
		httpCall(t, api, "GET", "/wagering/transactions/"+txid, "other", "", "", 404)
		httpCall(t, api, "GET", "/providers/provider/wagering/transactions/"+tc.id, "provider", "", "", 200)
		httpCall(t, api, "GET", "/providers/provider/wagering/transactions/"+tc.id, "other", "", "", 404)
	}
	replay := httpCall(t, api, "POST", "/wagering/transactions", "provider", "alias", body("bet", "BET", "10.00", "null"), 200)
	var balance httpapi.MoneyDTO
	must(t, json.Unmarshal(replay["balance"], &balance))
	if balance.Amount != "90.00" || string(replay["idempotentReplay"]) != "true" {
		t.Fatal(replay)
	}
	httpCall(t, api, "POST", "/wagering/transactions", "provider", "bet", body("different", "BET", "10", "null"), 409)
	httpCall(t, api, "POST", "/wagering/transactions", "provider", "x", body("invalid", "BET", "1e3", "null"), 400)
	httpCall(t, api, "POST", "/wagering/transactions", "provider", "x", "{", 400)
	httpCall(t, api, "POST", "/wagering/transactions", "provider", "", body("missing-key", "BET", "1", "null"), 400)
	httpCall(t, api, "POST", "/wagering/transactions", "other", "x", body("attack", "BET", "1", "null"), 403)
	httpCall(t, api, "POST", "/wagering/transactions", "provider", "opening", body("external-opening", "OPENING", "1", "null"), 400)
	httpCall(t, api, "GET", "/wallets/"+id, "provider", "", "", 403)
	httpCall(t, api, "GET", "/wallets/"+id, "internal", "", "", 200)
	reconciliation := httpCall(t, api, "POST", "/wallets/"+id+"/reconciliation", "internal", "", "", 200)
	if string(reconciliation["consistent"]) != "true" {
		t.Fatal(reconciliation)
	}
	first := httpCall(t, api, "GET", "/wallets/"+id+"/ledger?limit=1", "internal", "", "", 200)
	cursor := value(t, first, "nextCursor")
	second := httpCall(t, api, "GET", "/wallets/"+id+"/ledger?limit=1&cursor="+cursor, "internal", "", "", 200)
	if string(first["items"]) == string(second["items"]) {
		t.Fatal("repeated cursor page")
	}
	httpCall(t, api, "GET", "/wallets/"+id+"/ledger?cursor=bad", "internal", "", "", 400)
	httpCall(t, api, "POST", "/wallets", "internal", "", `{"playerId":"p","initialBalance":{"amount":"0","currency":"BRL"}}`, 409)
}
func TestOpeningAtomicityAndMigration(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	up, err := os.ReadFile("../../../migrations/000007_opening.up.sql")
	must(t, err)
	down, err := os.ReadFile("../../../migrations/000007_opening.down.sql")
	must(t, err)
	_, err = pool.Exec(ctx, string(down))
	must(t, err)
	_, err = pool.Exec(ctx, string(up))
	must(t, err)
	svc := financial.NewWalletService(NewWalletAPIStore(pool), time.Now)
	zero, _ := money.Zero("BRL")
	w, err := svc.Create(ctx, "zero", zero)
	must(t, err)
	if w.Version() != 1 || count(t, pool, "wager_transactions") != 0 {
		t.Fatal("zero opening")
	}
	w, err = svc.Create(ctx, "positive", testMoney(t, 100))
	must(t, err)
	if w.Version() != 1 || count(t, pool, "wager_transactions") != 1 || count(t, pool, "outbox_events") != 2 || count(t, pool, "wallet_ledger_entries") != 1 {
		t.Fatal("positive opening")
	}
	var id string
	must(t, pool.QueryRow(ctx, `SELECT id FROM wager_transactions WHERE kind='OPENING'`).Scan(&id))
	opening, err := NewWagerTransactionRepository(pool).GetByID(ctx, id)
	must(t, err)
	if opening.ProviderID() != "" || opening.WalletID() != w.ID() {
		t.Fatal(opening)
	}
	_, err = pool.Exec(ctx, `INSERT INTO wager_transactions(id,player_id,wallet_id,kind,amount_minor,currency,status,created_at,updated_at,result_balance_minor,result_currency) SELECT id||'-duplicate',player_id,wallet_id,kind,amount_minor,currency,status,created_at,updated_at,result_balance_minor,result_currency FROM wager_transactions WHERE id=$1`, id)
	if err == nil {
		t.Fatal("duplicate opening accepted")
	}
	rec, err := svc.Reconcile(ctx, w.ID())
	must(t, err)
	if !rec.Consistent || rec.CheckedEntries != 1 {
		t.Fatal(rec)
	}
	conn, err := pool.Acquire(ctx)
	must(t, err)
	defer conn.Release()
	_, err = conn.Exec(ctx, string(down))
	if err == nil {
		t.Fatal("lost opening history")
	}
	_, err = conn.Exec(ctx, "ROLLBACK")
	must(t, err)
	// Database failure after ledger creation must undo the entire wallet opening.
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_opening_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER opening_fail BEFORE INSERT ON outbox_events FOR EACH STATEMENT EXECUTE FUNCTION fail_opening_event()`)
	must(t, err)
	before := count(t, pool, "wallets")
	if _, err = svc.Create(ctx, "failed", testMoney(t, 100)); err == nil {
		t.Fatal("accepted failure")
	}
	if count(t, pool, "wallets") != before || count(t, pool, "wallet_ledger_entries") != 1 {
		t.Fatal("partial opening")
	}
}
func TestHTTPConcurrentInstances(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	svc := financial.NewWalletService(NewWalletAPIStore(pool), time.Now)
	w, err := svc.Create(ctx, "p", testMoney(t, 10000))
	must(t, err)
	servers := []*httptest.Server{}
	for i := 0; i < 2; i++ {
		servers = append(servers, httptest.NewServer(httpapi.NewAPI(newProcessor(t, NewRunner(pool)), svc, NewWagerTransactionRepository(pool), httpVerifier{}, healthy{}).Handler()))
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	done := make(chan struct{}, 2)
	results := make(chan int, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			ready <- struct{}{}
			<-start
			body := fmt.Sprintf(`{"providerId":"provider","externalTransactionId":"bet-%d","playerId":"p","walletId":%q,"roundId":"r","gameId":"g","kind":"BET","money":{"amount":"80.00","currency":"BRL"}}`, i, w.ID())
			req, err := http.NewRequestWithContext(ctx, "POST", servers[i].URL+"/wagering/transactions", strings.NewReader(body))
			if err != nil {
				errs <- err
				return
			}
			req.Header.Set("Authorization", "Bearer provider")
			req.Header.Set("Idempotency-Key", fmt.Sprint(i))
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err
				return
			}
			res.Body.Close()
			results <- res.StatusCode
			errs <- nil
		}(i)
	}
	<-ready
	<-ready
	close(start)
	defer func() { <-done; <-done }()
	for i := 0; i < 2; i++ {
		must(t, <-errs)
	}
	a, b := <-results, <-results
	if !((a == 200 && b == 422) || (a == 422 && b == 200)) {
		t.Fatal(a, b)
	}
	after, err := svc.Get(ctx, w.ID())
	must(t, err)
	n, _ := after.Balance().MinorUnits()
	if n != 2000 || count(t, pool, "wallet_ledger_entries") != 2 {
		t.Fatal("concurrent HTTP effects", n)
	}
}
