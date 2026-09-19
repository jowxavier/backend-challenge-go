package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/domain/ledger"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

type Type string

const (
	Processed        Type = "WagerTransactionProcessed"
	Rejected         Type = "WagerTransactionRejected"
	BalanceChanged   Type = "WalletBalanceChanged"
	PendingReference Type = "WagerTransactionPendingReference"
)

var ErrInvalidEvent = errors.New("invalid event")

// Event retains serialized snapshots, so callers cannot mutate nested payloads.
type Event struct {
	id, transactionID, aggregateID string
	kind                           Type
	at                             time.Time
	version                        int
	payload                        string
}

func (e Event) ID() string            { return e.id }
func (e Event) TransactionID() string { return e.transactionID }
func (e Event) AggregateID() string   { return e.aggregateID }
func (e Event) Type() Type            { return e.kind }
func (e Event) OccurredAt() time.Time { return e.at }
func (e Event) Version() int          { return e.version }
func (e Event) JSON() []byte          { return []byte(e.payload) }
func (e Event) Valid() bool           { return e.id != "" && e.payload != "" }

type decimalMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func decimal(m money.Money) (decimalMoney, error) {
	a, err := m.Amount()
	if err != nil {
		return decimalMoney{}, err
	}
	c, err := m.Currency()
	return decimalMoney{a, c}, err
}

type transactionData struct {
	ProviderID            string        `json:"providerId"`
	ExternalTransactionID string        `json:"externalTransactionId"`
	TransactionID         string        `json:"transactionId"`
	PlayerID              string        `json:"playerId"`
	WalletID              string        `json:"walletId"`
	RoundID               string        `json:"roundId"`
	GameID                string        `json:"gameId"`
	Kind                  wt.Kind       `json:"kind"`
	Status                wt.Status     `json:"status"`
	Money                 decimalMoney  `json:"money"`
	Reference             *string       `json:"referenceExternalTransactionId"`
	FailureCode           *string       `json:"failureCode"`
	Balance               *decimalMoney `json:"resultingBalance"`
}
type balanceData struct {
	WalletID      string           `json:"walletId"`
	TransactionID string           `json:"transactionId"`
	Direction     ledger.Direction `json:"direction"`
	Money         decimalMoney     `json:"money"`
	Before        decimalMoney     `json:"balanceBefore"`
	After         decimalMoney     `json:"balanceAfter"`
	WalletVersion int64            `json:"walletVersion"`
}
type envelope[T any] struct {
	ID            string    `json:"eventId"`
	Type          Type      `json:"eventType"`
	AggregateID   string    `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   *string   `json:"causationId"`
	At            time.Time `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          T         `json:"data"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func NewProcessed(t *wt.WagerTransaction, balance money.Money) (Event, error) {
	return transactionEvent(t, wt.PROCESSED, Processed, &balance)
}
func NewRejected(t *wt.WagerTransaction, balance money.Money) (Event, error) {
	return transactionEvent(t, wt.REJECTED, Rejected, &balance)
}
func NewPendingReference(t *wt.WagerTransaction) (Event, error) {
	return transactionEvent(t, wt.PENDING_REFERENCE, PendingReference, nil)
}
func transactionEvent(t *wt.WagerTransaction, status wt.Status, kind Type, balance *money.Money) (Event, error) {
	if t == nil || t.ID() == "" || t.Status() != status {
		return Event{}, ErrInvalidEvent
	}
	m, err := decimal(t.Money())
	if err != nil {
		return Event{}, err
	}
	d := transactionData{ProviderID: t.ProviderID(), ExternalTransactionID: t.ExternalTransactionID(), TransactionID: t.ID(), PlayerID: t.PlayerID(), WalletID: t.WalletID(), RoundID: t.RoundID(), GameID: t.GameID(), Kind: t.Kind(), Status: t.Status(), Money: m, Reference: optional(t.ReferenceExternalTransactionID()), FailureCode: optional(string(t.FailureCode()))}
	if balance != nil {
		n, err := balance.MinorUnits()
		if err != nil {
			return Event{}, err
		}
		if n < 0 {
			return Event{}, ErrInvalidEvent
		}
		b, err := decimal(*balance)
		if err != nil {
			return Event{}, err
		}
		d.Balance = &b
	}
	return build(kind, t.ID(), t.ID(), t.UpdatedAt(), d)
}
func NewBalanceChanged(entry ledger.Entry, version int64) (Event, error) {
	if entry.ID() == "" || version < 1 {
		return Event{}, ErrInvalidEvent
	}
	m, err := decimal(entry.Money())
	if err != nil {
		return Event{}, err
	}
	before, err := decimal(entry.BalanceBefore())
	if err != nil {
		return Event{}, err
	}
	after, err := decimal(entry.BalanceAfter())
	if err != nil {
		return Event{}, err
	}
	return build(BalanceChanged, entry.TransactionID(), entry.WalletID(), entry.CreatedAt(), balanceData{entry.WalletID(), entry.TransactionID(), entry.Direction(), m, before, after, version})
}
func build[T any](kind Type, transactionID, aggregateID string, at time.Time, data T) (Event, error) {
	if transactionID == "" || aggregateID == "" || at.IsZero() {
		return Event{}, ErrInvalidEvent
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Event{}, err
	}
	id := hex.EncodeToString(b[:])
	at = at.UTC().Truncate(time.Microsecond)
	raw, err := json.Marshal(envelope[T]{id, kind, aggregateID, transactionID, nil, at, 1, data})
	if err != nil {
		return Event{}, err
	}
	return Event{id, transactionID, aggregateID, kind, at, 1, string(raw)}, nil
}

// Restore loads the stored envelope without generating a new identity or timestamp.
func Restore(raw []byte) (Event, error) {
	var v envelope[json.RawMessage]
	if err := json.Unmarshal(raw, &v); err != nil {
		return Event{}, errors.Join(ErrInvalidEvent, err)
	}
	switch v.Type {
	case Processed, Rejected, PendingReference, BalanceChanged:
	default:
		return Event{}, ErrInvalidEvent
	}
	if v.ID == "" || v.AggregateID == "" || v.CorrelationID == "" || v.At.IsZero() || v.Version != 1 || len(v.Data) == 0 || v.Data[0] != '{' {
		return Event{}, ErrInvalidEvent
	}
	return Event{v.ID, v.CorrelationID, v.AggregateID, v.Type, v.At.UTC(), v.Version, string(raw)}, nil
}

// NewOpeningProcessed omits provider metadata that does not apply to internal creation.
func NewOpeningProcessed(t *wt.WagerTransaction) (Event, error) {
	if t == nil || t.Kind() != wt.OPENING || t.Status() != wt.PROCESSED {
		return Event{}, ErrInvalidEvent
	}
	m, err := decimal(t.Money())
	if err != nil {
		return Event{}, err
	}
	data := struct {
		TransactionID string       `json:"transactionId"`
		PlayerID      string       `json:"playerId"`
		WalletID      string       `json:"walletId"`
		Kind          wt.Kind      `json:"kind"`
		Status        wt.Status    `json:"status"`
		Money         decimalMoney `json:"money"`
		Balance       decimalMoney `json:"resultingBalance"`
	}{t.ID(), t.PlayerID(), t.WalletID(), t.Kind(), t.Status(), m, m}
	return build(Processed, t.ID(), t.ID(), t.UpdatedAt(), data)
}
