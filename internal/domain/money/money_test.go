package money_test

import (
	"errors"
	"testing"

	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
)

func mustParse(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func assertAmount(t *testing.T, m money.Money, want string) {
	t.Helper()
	got, err := m.Amount()
	if err != nil || got != want {
		t.Fatalf("Amount() = %q, %v; want %q", got, err, want)
	}
}

func TestParse(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"0", "0.00"}, {"0.0", "0.00"}, {"0.00", "0.00"},
		{"10", "10.00"}, {"10.5", "10.50"}, {"10.50", "10.50"},
		{"999999.99", "999999.99"}, {"0.01", "0.01"}, {"00010.50", "10.50"},
		{"92233720368547758.07", "92233720368547758.07"},
	} {
		t.Run(tc.input, func(t *testing.T) { assertAmount(t, mustParse(t, tc.input), tc.want) })
	}
}

func TestInvalidParsing(t *testing.T) {
	for _, input := range []string{"", "-1", "-0.01", "-0", "1.001", "0.000", "NaN", "Infinity", "1e3", " 10.00 ", "+10.00", "1.", ".50", ".", "1.2.3", "1,00", "1_000", "１２", "1\n", "1\x00"} {
		t.Run(input, func(t *testing.T) {
			_, err := money.Parse(input, "BRL")
			if !errors.Is(err, money.ErrInvalidAmount) {
				t.Fatalf("got %v", err)
			}
		})
	}
	for _, input := range []string{"92233720368547758.08", "92233720368547759", "92233720368547758.1", "999999999999999999999999999999999999"} {
		t.Run(input, func(t *testing.T) {
			_, err := money.Parse(input, "BRL")
			if !errors.Is(err, money.ErrOverflow) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestCurrencyAndZero(t *testing.T) {
	for _, code := range []string{"BRL", "brl", "bRl", "USD", "eur", "XYZ"} {
		t.Run(code, func(t *testing.T) {
			for _, create := range []func(string) (money.Money, error){money.Zero, func(c string) (money.Money, error) { return money.Parse("0", c) }} {
				m, err := create(code)
				if err != nil {
					t.Fatal(err)
				}
				assertAmount(t, m, "0.00")
				want := map[string]string{"BRL": "BRL", "brl": "BRL", "bRl": "BRL", "USD": "USD", "eur": "EUR", "XYZ": "XYZ"}[code]
				got, err := m.Currency()
				if err != nil || got != want {
					t.Fatalf("Currency() = %q, %v; want %q", got, err, want)
				}
			}
		})
	}
	for _, code := range []string{"", "B", "BR", "BRLL", " BRL", "BRL ", "B1L", "B-L", "€", "éA", "ſab"} {
		t.Run("invalid/"+code, func(t *testing.T) {
			_, err := money.Zero(code)
			if !errors.Is(err, money.ErrInvalidCurrency) {
				t.Fatalf("Zero: %v", err)
			}
			_, err = money.Parse("1", code)
			if !errors.Is(err, money.ErrInvalidCurrency) {
				t.Fatalf("Parse: %v", err)
			}
		})
	}
}

func TestArithmetic(t *testing.T) {
	zero := mustParse(t, "0")
	one := mustParse(t, "0.01")
	max := mustParse(t, "92233720368547758.07")
	negativeMax, err := max.Negate()
	if err != nil {
		t.Fatal(err)
	}
	min, err := negativeMax.Subtract(one)
	if err != nil {
		t.Fatal(err)
	}
	negativeOne, err := one.Negate()
	if err != nil {
		t.Fatal(err)
	}
	assertAmount(t, min, "-92233720368547758.08")
	for _, tc := range []struct {
		name     string
		a, b     money.Money
		subtract bool
		want     string
		err      error
	}{
		{"add", mustParse(t, "10.50"), mustParse(t, "2.25"), false, "12.75", nil},
		{"subtract", mustParse(t, "10.50"), mustParse(t, "2.25"), true, "8.25", nil},
		{"fractional carry", mustParse(t, "0.99"), one, false, "1.00", nil},
		{"fractional borrow", mustParse(t, "1.00"), one, true, "0.99", nil},
		{"negative result", zero, one, true, "-0.01", nil},
		{"cancel", one, negativeOne, false, "0.00", nil},
		{"add negative", negativeOne, negativeOne, false, "-0.02", nil},
		{"subtract negative", one, negativeOne, true, "0.02", nil},
		{"max plus zero", max, zero, false, "92233720368547758.07", nil},
		{"min plus zero", min, zero, false, "-92233720368547758.08", nil},
		{"max plus min", max, min, false, "-0.01", nil},
		{"min minus min", min, min, true, "0.00", nil},
		{"negative one minus min", negativeOne, min, true, "92233720368547758.07", nil},
		{"add overflow", max, one, false, "", money.ErrOverflow},
		{"add underflow", min, negativeOne, false, "", money.ErrOverflow},
		{"subtract underflow", min, one, true, "", money.ErrOverflow},
		{"subtract overflow", max, negativeOne, true, "", money.ErrOverflow},
		{"zero minus min", zero, min, true, "", money.ErrOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aBefore, bBefore := tc.a, tc.b
			var got money.Money
			var err error
			if tc.subtract {
				got, err = tc.a.Subtract(tc.b)
			} else {
				got, err = tc.a.Add(tc.b)
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("got %v; want %v", err, tc.err)
			}
			if err == nil {
				assertAmount(t, got, tc.want)
			}
			if tc.a != aBefore || tc.b != bBefore {
				t.Fatal("operands changed")
			}
		})
	}
	for _, tc := range []struct {
		name string
		m    money.Money
		want string
		err  error
	}{
		{"zero", zero, "0.00", nil}, {"positive", one, "-0.01", nil},
		{"negative", negativeOne, "0.01", nil}, {"max", max, "-92233720368547758.07", nil},
		{"min", min, "", money.ErrOverflow},
	} {
		t.Run("negate/"+tc.name, func(t *testing.T) {
			before := tc.m
			got, err := tc.m.Negate()
			if !errors.Is(err, tc.err) {
				t.Fatalf("got %v; want %v", err, tc.err)
			}
			if err == nil {
				assertAmount(t, got, tc.want)
			}
			if tc.m != before {
				t.Fatal("operand changed")
			}
		})
	}
	for _, tc := range []struct {
		name string
		a, b money.Money
		want int
	}{
		{"less", one, max, -1}, {"greater", max, one, 1}, {"equal", one, one, 0},
		{"extremes", min, max, -1}, {"reverse extremes", max, min, 1}, {"negative", negativeOne, zero, -1},
	} {
		t.Run("compare/"+tc.name, func(t *testing.T) {
			got, err := tc.a.Compare(tc.b)
			if err != nil || got != tc.want {
				t.Fatalf("Compare() = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestNormalizedCurrencyOperations(t *testing.T) {
	for _, tc := range []struct {
		left, right, want string
	}{
		{"brl", "BRL", "BRL"},
		{"BRL", "brl", "BRL"},
		{"usd", "USD", "USD"},
	} {
		t.Run(tc.left+"/"+tc.right, func(t *testing.T) {
			a, err := money.Parse("1.00", tc.left)
			if err != nil {
				t.Fatal(err)
			}
			b, err := money.Parse("1.00", tc.right)
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range []struct {
				name       string
				apply      func(money.Money) (money.Money, error)
				wantAmount string
			}{
				{"Add", a.Add, "2.00"},
				{"Subtract", a.Subtract, "0.00"},
			} {
				t.Run(op.name, func(t *testing.T) {
					got, err := op.apply(b)
					if err != nil {
						t.Fatal(err)
					}
					assertAmount(t, got, op.wantAmount)
					currency, err := got.Currency()
					if err != nil || currency != tc.want {
						t.Fatalf("Currency() = %q, %v; want %q", currency, err, tc.want)
					}
				})
			}
			if got, err := a.Compare(b); err != nil || got != 0 {
				t.Fatalf("Compare() = %d, %v; want 0", got, err)
			}
		})
	}
}

func TestInvalidOperands(t *testing.T) {
	valid := mustParse(t, "1")
	usd, err := money.Parse("1", "USD")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		a, b money.Money
		want error
	}{
		{"currency mismatch", valid, usd, money.ErrCurrencyMismatch},
		{"invalid receiver", money.Money{}, valid, money.ErrInvalidMoney},
		{"invalid argument", valid, money.Money{}, money.ErrInvalidMoney},
		{"both invalid", money.Money{}, money.Money{}, money.ErrInvalidMoney},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, addErr := tc.a.Add(tc.b)
			_, subErr := tc.a.Subtract(tc.b)
			_, cmpErr := tc.a.Compare(tc.b)
			for name, err := range map[string]error{"Add": addErr, "Subtract": subErr, "Compare": cmpErr} {
				if !errors.Is(err, tc.want) {
					t.Errorf("%s: got %v; want %v", name, err, tc.want)
				}
			}
		})
	}
	var invalid money.Money
	_, negateErr := invalid.Negate()
	_, amountErr := invalid.Amount()
	_, currencyErr := invalid.Currency()
	for name, err := range map[string]error{"Negate": negateErr, "Amount": amountErr, "Currency": currencyErr} {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, money.ErrInvalidMoney) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
