package wallet

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
)

// The version limit cannot practically be reached through repeated public calls.
func TestVersionLimit(t *testing.T) {
	at := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	one, err := money.Parse("1", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	zero, err := money.Zero("BRL")
	if err != nil {
		t.Fatal(err)
	}
	w, err := New("w", "p", "BRL", one, at)
	if err != nil {
		t.Fatal(err)
	}
	w.version = math.MaxInt64
	for _, op := range []struct {
		name  string
		apply func(money.Money, time.Time) error
	}{
		{"credit", w.Credit}, {"debit", w.Debit},
	} {
		t.Run(op.name, func(t *testing.T) {
			before := w
			err := op.apply(one, at)
			if !errors.Is(err, ErrVersionOverflow) || w != before {
				t.Fatal("version overflow changed state", err)
			}
			err = op.apply(zero, time.Time{})
			if err != nil || w != before {
				t.Fatal("zero amount should remain a no-op", err)
			}
		})
	}
}
