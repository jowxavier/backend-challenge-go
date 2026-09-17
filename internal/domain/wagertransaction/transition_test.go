package wagertransaction

import (
	"errors"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
)

// PENDING and arbitrary target states intentionally have no public transition method.
func TestUnsupportedTargets(t *testing.T) {
	m, err := money.Parse("1", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	for _, target := range []Status{PENDING, "", "UNKNOWN"} {
		t.Run(string(target), func(t *testing.T) {
			tx, err := NewExternal(ExternalInput{ID: "i", ProviderID: "p", ExternalTransactionID: "e", PlayerID: "u", WalletID: "w", RoundID: "r", GameID: "g", Kind: REFUND, Money: m, ReferenceExternalTransactionID: "ref"}, at)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.WaitForReference(at); err != nil {
				t.Fatal(err)
			}
			before := *tx
			if err := tx.transition(target, "", at); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("got %v", err)
			}
			if *tx != before {
				t.Fatal("unsupported transition mutated entity")
			}
		})
	}
}
