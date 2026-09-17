package money_test

import (
	"errors"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	"math"
	"testing"
)

func TestMinorUnits(t *testing.T) {
	for _, n := range []int64{0, 1, -1, math.MaxInt64, math.MinInt64} {
		m, err := money.FromMinorUnits(n, "brl")
		if err != nil {
			t.Fatal(err)
		}
		got, err := m.MinorUnits()
		if err != nil || got != n {
			t.Fatalf("got %d, %v", got, err)
		}
		c, err := m.Currency()
		if err != nil || c != "BRL" {
			t.Fatal(c, err)
		}
	}
	for _, c := range []string{"", "BR", "B1L", " BRL"} {
		if _, err := money.FromMinorUnits(1, c); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Fatal(err)
		}
	}
	if _, err := (money.Money{}).MinorUnits(); !errors.Is(err, money.ErrInvalidMoney) {
		t.Fatal(err)
	}
}
