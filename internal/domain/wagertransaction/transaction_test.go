package wagertransaction_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

var now = time.Date(2026, 9, 17, 12, 0, 0, 0, time.FixedZone("local", -10800))

func input(t *testing.T, kind wt.Kind) wt.ExternalInput {
	t.Helper()
	value := "1.00"
	if kind == wt.LOSS {
		value = "0"
	}
	m, err := money.Parse(value, "brl")
	if err != nil {
		t.Fatal(err)
	}
	in := wt.ExternalInput{ID: "internal", ProviderID: "provider", ExternalTransactionID: "external", PlayerID: "player", WalletID: "wallet", RoundID: "round", GameID: "game", Kind: kind, Money: m}
	if kind == wt.REFUND || kind == wt.ROLLBACK {
		in.ReferenceExternalTransactionID = "original"
	}
	return in
}
func newTransaction(t *testing.T) *wt.WagerTransaction {
	t.Helper()
	tx, err := wt.NewExternal(input(t, wt.REFUND), now)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestCreationAndReadOnlyAccess(t *testing.T) {
	for _, kind := range []wt.Kind{wt.BET, wt.WIN, wt.LOSS, wt.REFUND, wt.ROLLBACK} {
		t.Run(string(kind), func(t *testing.T) {
			in := input(t, kind)
			tx, err := wt.NewExternal(in, now)
			if err != nil {
				t.Fatal(err)
			}
			if tx.ID() != in.ID || tx.ProviderID() != in.ProviderID || tx.ExternalTransactionID() != in.ExternalTransactionID || tx.PlayerID() != in.PlayerID || tx.WalletID() != in.WalletID || tx.RoundID() != in.RoundID || tx.GameID() != in.GameID || tx.Kind() != kind || tx.Money() != in.Money || tx.ReferenceExternalTransactionID() != in.ReferenceExternalTransactionID {
				t.Fatal("incorrect business fields")
			}
			if tx.Status() != wt.PENDING || tx.FailureCode() != "" {
				t.Fatal("incorrect initial outcome")
			}
			if tx.CreatedAt() != now.UTC() || tx.UpdatedAt() != now.UTC() || tx.CreatedAt().Location() != time.UTC || tx.UpdatedAt().Location() != time.UTC {
				t.Fatal("incorrect timestamps")
			}
			before := *tx
			in.ID = "changed"
			in.Money, err = in.Money.Negate()
			if err != nil {
				t.Fatal(err)
			}
			m := tx.Money()
			_, err = m.Add(m)
			if err != nil {
				t.Fatal(err)
			}
			copy := *tx
			if err := copy.MarkProcessed(now); err != nil {
				t.Fatal(err)
			}
			if *tx != before {
				t.Fatal("external values changed entity")
			}
		})
	}
}

func TestInvalidFields(t *testing.T) {
	for _, field := range []struct {
		name string
		set  func(*wt.ExternalInput, string)
	}{
		{"id", func(i *wt.ExternalInput, s string) { i.ID = s }},
		{"provider", func(i *wt.ExternalInput, s string) { i.ProviderID = s }},
		{"external", func(i *wt.ExternalInput, s string) { i.ExternalTransactionID = s }},
		{"player", func(i *wt.ExternalInput, s string) { i.PlayerID = s }},
		{"wallet", func(i *wt.ExternalInput, s string) { i.WalletID = s }},
		{"round", func(i *wt.ExternalInput, s string) { i.RoundID = s }},
		{"game", func(i *wt.ExternalInput, s string) { i.GameID = s }},
	} {
		for _, value := range []string{"", " ", "a b", "a\x00", "\xff"} {
			t.Run(field.name+"/"+value, func(t *testing.T) {
				in := input(t, wt.BET)
				field.set(&in, value)
				tx, err := wt.NewExternal(in, now)
				if tx != nil || !errors.Is(err, wt.ErrInvalidField) {
					t.Fatalf("got %v, %v", tx, err)
				}
			})
		}
	}
}

func TestAmountRules(t *testing.T) {
	for _, kind := range []wt.Kind{wt.BET, wt.WIN, wt.LOSS, wt.REFUND, wt.ROLLBACK} {
		for _, value := range []string{"0", "1", "negative", "invalid"} {
			t.Run(string(kind)+"/"+value, func(t *testing.T) {
				in := input(t, kind)
				var err error
				switch value {
				case "invalid":
					in.Money = money.Money{}
				case "negative":
					in.Money, err = money.Parse("1", "BRL")
					if err != nil {
						t.Fatal(err)
					}
					in.Money, err = in.Money.Negate()
				default:
					in.Money, err = money.Parse(value, "BRL")
				}
				if err != nil {
					t.Fatal(err)
				}
				_, err = wt.NewExternal(in, now)
				valid := (kind == wt.LOSS && value == "0") || (kind != wt.LOSS && value == "1")
				if valid {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, wt.ErrInvalidAmount) {
					t.Fatalf("got %v", err)
				}
				if value == "invalid" && !errors.Is(err, money.ErrInvalidMoney) {
					t.Fatal("lost Money error")
				}
			})
		}
	}
}

func TestReferences(t *testing.T) {
	for _, kind := range []wt.Kind{wt.BET, wt.WIN, wt.LOSS, wt.REFUND, wt.ROLLBACK} {
		for _, ref := range []string{"", "original", "external", " ", "bad\x00", "\xff"} {
			t.Run(string(kind)+"/"+ref, func(t *testing.T) {
				in := input(t, kind)
				in.ReferenceExternalTransactionID = ref
				_, err := wt.NewExternal(in, now)
				valid := (ref == "" && (kind == wt.BET || kind == wt.LOSS || kind == wt.WIN)) || (ref == "original" && (kind == wt.WIN || kind == wt.REFUND || kind == wt.ROLLBACK))
				if valid {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, wt.ErrInvalidReference) {
					t.Fatalf("got %v", err)
				}
			})
		}
	}
}

func TestInvalidKindAndTime(t *testing.T) {
	for _, kind := range []wt.Kind{"", "OPENING", "bet", "OTHER"} {
		t.Run(string(kind), func(t *testing.T) {
			in := input(t, wt.BET)
			in.Kind = kind
			if _, err := wt.NewExternal(in, now); !errors.Is(err, wt.ErrInvalidKind) {
				t.Fatalf("got %v", err)
			}
		})
	}
	if _, err := wt.NewExternal(input(t, wt.BET), time.Time{}); !errors.Is(err, wt.ErrInvalidTime) {
		t.Fatalf("got %v", err)
	}
}

type operation struct {
	name   string
	target wt.Status
	code   wt.FailureCode
	apply  func(*wt.WagerTransaction, time.Time) error
}

func operations() []operation {
	return []operation{
		{"wait", wt.PENDING_REFERENCE, "", (*wt.WagerTransaction).WaitForReference},
		{"process", wt.PROCESSED, "", (*wt.WagerTransaction).MarkProcessed},
		{"reject", wt.REJECTED, wt.ReferenceNotFound, func(tx *wt.WagerTransaction, at time.Time) error { return tx.Reject(wt.ReferenceNotFound, at) }},
		{"fail", wt.FAILED, wt.PermanentInfrastructureFailure, func(tx *wt.WagerTransaction, at time.Time) error {
			return tx.Fail(wt.PermanentInfrastructureFailure, at)
		}},
	}
}
func inState(t *testing.T, state wt.Status) *wt.WagerTransaction {
	t.Helper()
	tx := newTransaction(t)
	if state == wt.PENDING {
		return tx
	}
	for _, op := range operations() {
		if op.target == state {
			if err := op.apply(tx, now); err != nil {
				t.Fatal(err)
			}
			return tx
		}
	}
	t.Fatal("unknown test state")
	return nil
}

func TestTransitionMatrix(t *testing.T) {
	for _, state := range []wt.Status{wt.PENDING, wt.PENDING_REFERENCE, wt.PROCESSED, wt.REJECTED, wt.FAILED} {
		for _, op := range operations() {
			t.Run(string(state)+"/"+op.name, func(t *testing.T) {
				tx := inState(t, state)
				before := *tx
				// Earlier processing timestamps are intentionally accepted.
				at := now.Add(-time.Hour)
				err := op.apply(tx, at)
				allowed := state == wt.PENDING || (state == wt.PENDING_REFERENCE && op.target != wt.PENDING_REFERENCE)
				if !allowed {
					if !errors.Is(err, wt.ErrInvalidTransition) {
						t.Fatalf("got %v", err)
					}
					if *tx != before {
						t.Fatal("failed transition mutated entity")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if tx.Status() != op.target || tx.FailureCode() != op.code || tx.UpdatedAt() != at.UTC() || tx.UpdatedAt().Location() != time.UTC {
					t.Fatal("incorrect transition outcome")
				}
				if tx.ID() != before.ID() || tx.ProviderID() != before.ProviderID() || tx.ExternalTransactionID() != before.ExternalTransactionID() || tx.PlayerID() != before.PlayerID() || tx.WalletID() != before.WalletID() || tx.RoundID() != before.RoundID() || tx.GameID() != before.GameID() || tx.Kind() != before.Kind() || tx.Money() != before.Money() || tx.ReferenceExternalTransactionID() != before.ReferenceExternalTransactionID() || tx.CreatedAt() != before.CreatedAt() {
					t.Fatal("transition changed original business fields")
				}
			})
		}
	}
}

func TestTransitionValidation(t *testing.T) {
	for _, op := range operations() {
		t.Run(op.name, func(t *testing.T) {
			for _, tx := range []*wt.WagerTransaction{nil, {}} {
				if err := op.apply(tx, now); !errors.Is(err, wt.ErrInvalidTransaction) {
					t.Fatalf("got %v", err)
				}
				if tx != nil && *tx != (wt.WagerTransaction{}) {
					t.Fatal("invalid receiver mutated")
				}
			}
			for _, state := range []wt.Status{wt.PENDING, wt.PENDING_REFERENCE} {
				if state == wt.PENDING_REFERENCE && op.target == wt.PENDING_REFERENCE {
					continue
				}
				tx := inState(t, state)
				before := *tx
				if err := op.apply(tx, time.Time{}); !errors.Is(err, wt.ErrInvalidTime) {
					t.Fatalf("got %v", err)
				}
				if *tx != before {
					t.Fatal("invalid time mutated entity")
				}
			}
		})
	}
	for _, kind := range []wt.Kind{wt.BET, wt.LOSS, wt.WIN} {
		tx, err := wt.NewExternal(input(t, kind), now)
		if err != nil {
			t.Fatal(err)
		}
		before := *tx
		if err := tx.WaitForReference(now); !errors.Is(err, wt.ErrInvalidReference) {
			t.Fatalf("got %v", err)
		}
		if *tx != before {
			t.Fatal("missing reference mutated entity")
		}
	}
	in := input(t, wt.WIN)
	in.ReferenceExternalTransactionID = "bet"
	tx, err := wt.NewExternal(in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.WaitForReference(now); err != nil {
		t.Fatal(err)
	}
}

func TestFailureCodes(t *testing.T) {
	for _, state := range []wt.Status{wt.PENDING, wt.PENDING_REFERENCE} {
		for _, code := range []wt.FailureCode{"", "UNKNOWN", wt.BetInsufficientFunds, wt.ReversalInsufficientFunds, wt.ReferenceNotFound, wt.PermanentInfrastructureFailure} {
			for _, reject := range []bool{true, false} {
				t.Run(string(state)+"/"+string(code)+"/"+map[bool]string{true: "reject", false: "fail"}[reject], func(t *testing.T) {
					tx := inState(t, state)
					before := *tx
					var err error
					if reject {
						err = tx.Reject(code, now)
					} else {
						err = tx.Fail(code, now)
					}
					valid := (!reject && code == wt.PermanentInfrastructureFailure) || (reject && (code == wt.BetInsufficientFunds || code == wt.ReversalInsufficientFunds || code == wt.ReferenceNotFound))
					if valid {
						if err != nil || tx.FailureCode() != code {
							t.Fatalf("got %v", err)
						}
					} else {
						if !errors.Is(err, wt.ErrInvalidFailureCode) {
							t.Fatalf("got %v", err)
						}
						if *tx != before {
							t.Fatal("invalid code mutated entity")
						}
					}
				})
			}
		}
	}
}
