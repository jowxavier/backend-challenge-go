package http

import (
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	"testing"
)

func TestExternalCurrency(t *testing.T) {
	for _, tc := range []struct {
		currency string
		valid    bool
	}{{"BRL", true}, {"brl", true}, {"USD", false}, {"ZZZ", false}, {"", false}} {
		t.Run(tc.currency, func(t *testing.T) {
			m, err := parseExternalMoney("1.00", tc.currency)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
			if tc.valid {
				c, _ := m.Currency()
				if c != "BRL" {
					t.Fatal(c)
				}
			}
		})
	}
	a, _ := money.Parse("1", "BRL")
	b, _ := money.Parse("1", "USD")
	if _, err := a.Add(b); err != money.ErrCurrencyMismatch {
		t.Fatal(err)
	}
}
