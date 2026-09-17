package wallet_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
)

var created = time.Date(2026, 9, 17, 12, 0, 0, 0, time.FixedZone("local", -3*3600))

func amount(t *testing.T, text, currency string) money.Money {
	t.Helper()
	m, err := money.Parse(text, currency)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func negative(t *testing.T) money.Money {
	t.Helper()
	m, err := amount(t, "0.01", "BRL").Negate()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNew(t *testing.T) {
	for _, initial := range []string{"0", "100.50", "92233720368547758.07"} {
		t.Run(initial, func(t *testing.T) {
			balance := amount(t, initial, "brl")
			w, err := wallet.New("wallet-1", "player-1", "brl", balance, created)
			if err != nil {
				t.Fatal(err)
			}
			if w.ID() != "wallet-1" || w.PlayerID() != "player-1" || w.Currency() != "BRL" || w.Balance() != balance || w.Version() != 1 {
				t.Fatalf("unexpected wallet: %+v", w)
			}
			if !w.CreatedAt().Equal(created) || !w.UpdatedAt().Equal(created) || w.CreatedAt().Location() != time.UTC || w.UpdatedAt().Location() != time.UTC {
				t.Fatal("incorrect timestamps")
			}
		})
	}
}

func TestInvalidCreation(t *testing.T) {
	valid := amount(t, "1", "BRL")
	for _, tc := range []struct {
		name, id, player, currency string
		balance                    money.Money
		at                         time.Time
		want, cause                error
	}{
		{"empty wallet", "", "p", "BRL", valid, created, wallet.ErrInvalidWalletID, nil},
		{"blank wallet", " \t", "p", "BRL", valid, created, wallet.ErrInvalidWalletID, nil},
		{"wallet whitespace", "w 1", "p", "BRL", valid, created, wallet.ErrInvalidWalletID, nil},
		{"wallet control", "w\x00", "p", "BRL", valid, created, wallet.ErrInvalidWalletID, nil},
		{"empty player", "w", "", "BRL", valid, created, wallet.ErrInvalidPlayerID, nil},
		{"blank player", "w", "\n", "BRL", valid, created, wallet.ErrInvalidPlayerID, nil},
		{"invalid encoding", "w", "\xff", "BRL", valid, created, wallet.ErrInvalidPlayerID, nil},
		{"invalid currency", "w", "p", "B1L", valid, created, money.ErrInvalidCurrency, nil},
		{"empty currency", "w", "p", "", valid, created, money.ErrInvalidCurrency, nil},
		{"initial mismatch", "w", "p", "USD", valid, created, wallet.ErrInvalidInitialBalance, money.ErrCurrencyMismatch},
		{"negative initial", "w", "p", "BRL", negative(t), created, wallet.ErrInvalidInitialBalance, nil},
		{"uninitialized initial", "w", "p", "BRL", money.Money{}, created, wallet.ErrInvalidInitialBalance, money.ErrInvalidMoney},
		{"zero time", "w", "p", "BRL", valid, time.Time{}, wallet.ErrInvalidTime, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := wallet.New(tc.id, tc.player, tc.currency, tc.balance, tc.at)
			if !errors.Is(err, tc.want) || (tc.cause != nil && !errors.Is(err, tc.cause)) {
				t.Fatalf("got %v; want %v, cause %v", err, tc.want, tc.cause)
			}
		})
	}
}

func TestOperations(t *testing.T) {
	for _, tc := range []struct {
		name, initial string
		amount        money.Money
		debit         bool
		want          string
		changed       bool
		err, cause    error
	}{
		{"credit", "100", amount(t, "25.50", "brl"), false, "125.50", true, nil, nil},
		{"debit", "100", amount(t, "25.50", "brl"), true, "74.50", true, nil, nil},
		{"credit zero balance", "0", amount(t, "0.01", "BRL"), false, "0.01", true, nil, nil},
		{"full debit", "100", amount(t, "100", "BRL"), true, "0.00", true, nil, nil},
		{"insufficient", "100", amount(t, "100.01", "BRL"), true, "100.00", false, wallet.ErrInsufficientBalance, nil},
		{"empty balance", "0", amount(t, "0.01", "BRL"), true, "0.00", false, wallet.ErrInsufficientBalance, nil},
		{"overflow", "92233720368547758.07", amount(t, "0.01", "BRL"), false, "92233720368547758.07", false, money.ErrOverflow, nil},
		{"credit zero", "100", amount(t, "0", "BRL"), false, "100.00", false, nil, nil},
		{"debit zero", "0", amount(t, "0", "BRL"), true, "0.00", false, nil, nil},
		{"credit negative", "100", negative(t), false, "100.00", false, wallet.ErrInvalidAmount, nil},
		{"debit negative", "100", negative(t), true, "100.00", false, wallet.ErrInvalidAmount, nil},
		{"credit mismatch", "100", amount(t, "1", "USD"), false, "100.00", false, wallet.ErrInvalidAmount, money.ErrCurrencyMismatch},
		{"debit mismatch", "100", amount(t, "1", "USD"), true, "100.00", false, wallet.ErrInvalidAmount, money.ErrCurrencyMismatch},
		{"zero credit mismatch", "100", amount(t, "0", "USD"), false, "100.00", false, wallet.ErrInvalidAmount, money.ErrCurrencyMismatch},
		{"zero debit mismatch", "100", amount(t, "0", "USD"), true, "100.00", false, wallet.ErrInvalidAmount, money.ErrCurrencyMismatch},
		{"credit invalid", "100", money.Money{}, false, "100.00", false, wallet.ErrInvalidAmount, money.ErrInvalidMoney},
		{"debit invalid", "100", money.Money{}, true, "100.00", false, wallet.ErrInvalidAmount, money.ErrInvalidMoney},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, err := wallet.New("w", "p", "BRL", amount(t, tc.initial, "BRL"), created)
			if err != nil {
				t.Fatal(err)
			}
			before, input := w, tc.amount
			at := created.Add(time.Minute)
			if tc.debit {
				err = w.Debit(tc.amount, at)
			} else {
				err = w.Credit(tc.amount, at)
			}
			if !errors.Is(err, tc.err) || (tc.cause != nil && !errors.Is(err, tc.cause)) {
				t.Fatalf("got %v; want %v, cause %v", err, tc.err, tc.cause)
			}
			text, err := w.Balance().Amount()
			if err != nil || text != tc.want {
				t.Fatalf("balance = %q, %v; want %q", text, err, tc.want)
			}
			wantVersion, wantTime := int64(1), created
			if tc.changed {
				wantVersion, wantTime = 2, at
			}
			if w.Version() != wantVersion || !w.UpdatedAt().Equal(wantTime) || !w.CreatedAt().Equal(created) {
				t.Fatal("incorrect version or timestamps")
			}
			currency, err := w.Balance().Currency()
			if err != nil || currency != w.Currency() || w.Currency() != "BRL" || w.ID() != before.ID() || w.PlayerID() != before.PlayerID() {
				t.Fatal("identity or currency changed")
			}
			if tc.amount != input {
				t.Fatal("input state mutated")
			}
			if !tc.changed && w != before {
				t.Fatal("no-op or failure changed state")
			}
		})
	}
}

func TestTimeAndInvalidWallet(t *testing.T) {
	w, err := wallet.New("w", "p", "BRL", amount(t, "10", "BRL"), created)
	if err != nil {
		t.Fatal(err)
	}
	for _, debit := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			w    wallet.Wallet
			at   time.Time
			want error
		}{
			{"zero wallet", wallet.Wallet{}, created, wallet.ErrInvalidWallet},
			{"zero timestamp", w, time.Time{}, wallet.ErrInvalidTime},
			{"older timestamp", w, created.Add(-time.Second), nil},
			{"equal timestamp", w, created, nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := tc.w
				var err error
				if debit {
					err = tc.w.Debit(amount(t, "1", "BRL"), tc.at)
				} else {
					err = tc.w.Credit(amount(t, "1", "BRL"), tc.at)
				}
				if !errors.Is(err, tc.want) {
					t.Fatalf("got %v; want %v", err, tc.want)
				}
				if err == nil && (!tc.w.UpdatedAt().Equal(tc.at) || tc.w.Version() != before.Version()+1) {
					t.Fatal("successful change did not use supplied time and increment version")
				}
				if err != nil && tc.w != before {
					t.Fatal("failure changed state")
				}
			})
		}
	}
}

func TestSuccessiveChangesAndBalanceIsolation(t *testing.T) {
	w, err := wallet.New("w", "p", "BRL", amount(t, "1", "BRL"), created)
	if err != nil {
		t.Fatal(err)
	}
	original := w
	balance := w.Balance()
	balance, err = balance.Add(amount(t, "10", "BRL"))
	if err != nil {
		t.Fatal(err)
	}
	if w != original || balance == w.Balance() {
		t.Fatal("balance getter exposed mutable state")
	}
	for i := 1; i <= 3; i++ {
		err = w.Debit(amount(t, "0.25", "BRL"), created.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if w.Version() != int64(i+1) {
			t.Fatal("version did not increment exactly once")
		}
	}
	before := w
	err = w.Debit(amount(t, "1", "BRL"), created.Add(4*time.Second))
	if !errors.Is(err, wallet.ErrInsufficientBalance) || w != before {
		t.Fatal("rejected debit changed state")
	}
}

func TestZeroOperationsIgnoreTimestamp(t *testing.T) {
	for _, op := range []struct {
		name  string
		apply func(*wallet.Wallet, money.Money, time.Time) error
	}{
		{"credit", (*wallet.Wallet).Credit},
		{"debit", (*wallet.Wallet).Debit},
	} {
		for _, timestamp := range []struct {
			name string
			at   time.Time
		}{
			{"zero", time.Time{}},
			{"earlier", created.Add(-time.Hour)},
		} {
			t.Run(op.name+"/"+timestamp.name, func(t *testing.T) {
				w, err := wallet.New("w", "p", "BRL", amount(t, "10", "BRL"), created)
				if err != nil {
					t.Fatal(err)
				}
				before := w
				if err := op.apply(&w, amount(t, "0", "BRL"), timestamp.at); err != nil {
					t.Fatal(err)
				}
				if w.Balance() != before.Balance() || w.Version() != before.Version() || w.UpdatedAt() != before.UpdatedAt() || w != before {
					t.Fatal("zero operation changed state")
				}
				for _, invalid := range []struct {
					amount money.Money
					want   error
				}{
					{money.Money{}, money.ErrInvalidMoney},
					{amount(t, "0", "USD"), money.ErrCurrencyMismatch},
				} {
					if err := op.apply(&w, invalid.amount, timestamp.at); !errors.Is(err, invalid.want) {
						t.Fatalf("got %v; want %v", err, invalid.want)
					}
					if w != before {
						t.Fatal("invalid operation changed state")
					}
				}
				var uninitialized wallet.Wallet
				if err := op.apply(&uninitialized, amount(t, "0", "BRL"), timestamp.at); !errors.Is(err, wallet.ErrInvalidWallet) {
					t.Fatalf("got %v", err)
				}
				if uninitialized != (wallet.Wallet{}) {
					t.Fatal("invalid wallet changed")
				}
				if err := op.apply(nil, amount(t, "0", "BRL"), timestamp.at); !errors.Is(err, wallet.ErrInvalidWallet) {
					t.Fatalf("nil wallet: %v", err)
				}
			})
		}
	}
}
