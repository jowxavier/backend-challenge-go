package ledger_test

import (
	"errors"
	"github.com/jowxavier/backend-challenge-go/internal/domain/ledger"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	"math"
	"testing"
	"time"
)

func TestEntry(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		direction             ledger.Direction
		amount, before, after int64
		invalid               bool
	}{
		{"credit", ledger.Credit, 1, 99, 100, false}, {"debit", ledger.Debit, 1, 100, 99, false},
		{"max credit", ledger.Credit, math.MaxInt64, 0, math.MaxInt64, false},
		{"max debit", ledger.Debit, math.MaxInt64, math.MaxInt64, 0, false},
		{"zero", ledger.Credit, 0, 1, 1, true}, {"negative amount", ledger.Debit, -1, 1, 2, true},
		{"negative balance", ledger.Debit, 2, 1, -1, true}, {"arithmetic", ledger.Credit, 1, 1, 3, true},
		{"overflow", ledger.Credit, 1, math.MaxInt64, 0, true}, {"direction", "unknown", 1, 0, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			amount, _ := money.FromMinorUnits(tc.amount, "BRL")
			before, _ := money.FromMinorUnits(tc.before, "BRL")
			after, _ := money.FromMinorUnits(tc.after, "BRL")
			at := time.Date(2026, 1, 1, 1, 0, 0, 0, time.FixedZone("offset", 3600))
			e, err := ledger.New("e", "w", "t", tc.direction, amount, before, after, at)
			if tc.invalid {
				if !errors.Is(err, ledger.ErrInvalidEntry) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if e.ID() != "e" || e.WalletID() != "w" || e.TransactionID() != "t" || e.Direction() != tc.direction || e.Money() != amount || e.BalanceBefore() != before || e.BalanceAfter() != after || e.CreatedAt().Location() != time.UTC || !e.CreatedAt().Equal(at) {
				t.Fatal("getters")
			}
			copy := e
			_, _ = e.Money().Add(amount)
			if e != copy {
				t.Fatal("entry mutated")
			}
		})
	}
}
func TestInvalidEntryFields(t *testing.T) {
	one, _ := money.Parse("1", "BRL")
	zero, _ := money.Zero("BRL")
	usd, _ := money.Zero("USD")
	for _, tc := range []struct {
		name, id, wallet, transaction string
		amount, before, after         money.Money
		at                            time.Time
	}{
		{"id", "", "w", "t", one, zero, one, time.Now()},
		{"wallet", "e", "bad wallet", "t", one, zero, one, time.Now()},
		{"transaction", "e", "w", "", one, zero, one, time.Now()},
		{"amount", "e", "w", "t", money.Money{}, zero, one, time.Now()},
		{"currency", "e", "w", "t", one, usd, one, time.Now()},
		{"time", "e", "w", "t", one, zero, one, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ledger.New(tc.id, tc.wallet, tc.transaction, ledger.Credit, tc.amount, tc.before, tc.after, tc.at); !errors.Is(err, ledger.ErrInvalidEntry) {
				t.Fatal(err)
			}
		})
	}
}
