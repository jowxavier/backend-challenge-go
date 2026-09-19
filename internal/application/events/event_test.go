package events

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/domain/ledger"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

func TestTransactionSnapshots(t *testing.T) {
	at := time.Date(2026, 9, 18, 10, 0, 0, 123456000, time.FixedZone("offset", -3*3600))
	for _, kind := range []wt.Kind{wt.BET, wt.WIN, wt.LOSS, wt.REFUND, wt.ROLLBACK} {
		for _, status := range []wt.Status{wt.PROCESSED, wt.REJECTED, wt.PENDING_REFERENCE} {
			if status == wt.PENDING_REFERENCE && (kind == wt.BET || kind == wt.LOSS) {
				continue
			}
			t.Run(string(kind)+"/"+string(status), func(t *testing.T) {
				amount, _ := money.FromMinorUnits(math.MaxInt64, "brl")
				if kind == wt.LOSS {
					amount, _ = money.Zero("BRL")
				}
				ref := ""
				if kind == wt.REFUND || kind == wt.ROLLBACK || status == wt.PENDING_REFERENCE {
					ref = "original"
				}
				tx, err := wt.NewExternal(wt.ExternalInput{ID: "t", ProviderID: "p", ExternalTransactionID: "e", PlayerID: "u", WalletID: "w", RoundID: "r", GameID: "g", Kind: kind, Money: amount, ReferenceExternalTransactionID: ref}, at)
				if err != nil {
					t.Fatal(err)
				}
				balance, _ := money.FromMinorUnits(123, "USD")
				var event Event
				switch status {
				case wt.PROCESSED:
					err = tx.MarkProcessed(at)
					if err == nil {
						event, err = NewProcessed(tx, balance)
					}
				case wt.REJECTED:
					err = tx.Reject(wt.CurrencyMismatch, at)
					if err == nil {
						event, err = NewRejected(tx, balance)
					}
				case wt.PENDING_REFERENCE:
					err = tx.WaitForReference(at)
					if err == nil {
						event, err = NewPendingReference(tx)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				var e envelope[transactionData]
				if err = json.Unmarshal(event.JSON(), &e); err != nil {
					t.Fatal(err)
				}
				wantType := Processed
				if status == wt.REJECTED {
					wantType = Rejected
				}
				if status == wt.PENDING_REFERENCE {
					wantType = PendingReference
				}
				if len(e.ID) != 32 || e.Type != wantType || e.CorrelationID != "t" || e.AggregateID != "t" || e.Version != 1 || e.CausationID != nil || !e.At.Equal(at) || e.At.Location() != time.UTC {
					t.Fatal(e)
				}
				wantAmount := "92233720368547758.07"
				if kind == wt.LOSS {
					wantAmount = "0.00"
				}
				if e.Data.Money.Amount != wantAmount || e.Data.Money.Currency != "BRL" || e.Data.Status != status || e.Data.TransactionID != "t" || e.Data.ProviderID != "p" {
					t.Fatal(e.Data)
				}
				var fields map[string]json.RawMessage
				dataJSON, _ := json.Marshal(e.Data)
				json.Unmarshal(dataJSON, &fields)
				for _, key := range []string{"referenceExternalTransactionId", "failureCode", "resultingBalance"} {
					if _, ok := fields[key]; !ok {
						t.Fatal("missing explicit field", key)
					}
				}
				if (e.Data.Reference == nil) != (ref == "") || (e.Data.FailureCode == nil) != (status != wt.REJECTED) || (e.Data.Balance == nil) != (status == wt.PENDING_REFERENCE) {
					t.Fatal(e.Data)
				}
				if e.Data.Balance != nil && *e.Data.Balance != (decimalMoney{"1.23", "USD"}) {
					t.Fatal(e.Data.Balance)
				}
				raw := event.JSON()
				raw[0] = 'x'
				if !json.Valid(event.JSON()) {
					t.Fatal("external mutation")
				}
				restored, err := Restore(event.JSON())
				if err != nil || restored.ID() != event.ID() || string(restored.JSON()) != string(event.JSON()) {
					t.Fatal(restored, err)
				}
			})
		}
	}
}
func TestBalanceSnapshotAndInvalidEvents(t *testing.T) {
	m, _ := money.Parse("0.01", "BRL")
	before, _ := money.Parse("1", "BRL")
	after, _ := money.Parse("0.99", "BRL")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry, err := ledger.New("l", "w", "t", ledger.Debit, m, before, after, at)
	if err != nil {
		t.Fatal(err)
	}
	event, err := NewBalanceChanged(entry, 2)
	if err != nil {
		t.Fatal(err)
	}
	var e envelope[balanceData]
	if err = json.Unmarshal(event.JSON(), &e); err != nil {
		t.Fatal(err)
	}
	if e.Type != BalanceChanged || e.AggregateID != "w" || e.CorrelationID != "t" || e.Data.Before.Amount != "1.00" || e.Data.After.Amount != "0.99" || e.Data.Money.Amount != "0.01" || e.Data.WalletVersion != 2 || e.Data.Direction != ledger.Debit {
		t.Fatal(e)
	}
	for _, f := range []func() (Event, error){func() (Event, error) { return NewProcessed(nil, m) }, func() (Event, error) { return NewRejected(&wt.WagerTransaction{}, m) }, func() (Event, error) { return NewPendingReference(nil) }, func() (Event, error) { return NewBalanceChanged(ledger.Entry{}, 1) }, func() (Event, error) { return NewBalanceChanged(entry, 0) }} {
		if _, err := f(); !errors.Is(err, ErrInvalidEvent) {
			t.Fatal(err)
		}
	}
	if (Event{}).Valid() {
		t.Fatal("valid zero")
	}
	for _, raw := range []string{"null", "{}", "invalid", `{"eventType":"Unknown"}`} {
		if _, err := Restore([]byte(raw)); !errors.Is(err, ErrInvalidEvent) {
			t.Fatal(err)
		}
	}
}
