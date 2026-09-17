package wallet_test

import (
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
	"math"
	"testing"
	"time"
)

func TestRehydrate(t *testing.T) {
	base := wallet.State{ID: "w", PlayerID: "p", Currency: "BRL", Balance: amount(t, "10", "BRL"), Version: 42, CreatedAt: created, UpdatedAt: created.Add(-time.Hour)}
	for _, version := range []int64{1, 42, math.MaxInt64} {
		s := base
		s.Version = version
		w, err := wallet.Rehydrate(s)
		if err != nil {
			t.Fatal(err)
		}
		if w.ID() != s.ID || w.PlayerID() != s.PlayerID || w.Currency() != s.Currency || w.Balance() != s.Balance || w.Version() != version || w.CreatedAt() != s.CreatedAt || w.UpdatedAt() != s.UpdatedAt {
			t.Fatal("state not preserved")
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*wallet.State)
	}{
		{"id", func(s *wallet.State) { s.ID = "" }}, {"player", func(s *wallet.State) { s.PlayerID = " " }},
		{"currency", func(s *wallet.State) { s.Currency = "brl" }}, {"mismatch", func(s *wallet.State) { s.Currency = "USD" }},
		{"money", func(s *wallet.State) { s.Balance = money.Money{} }}, {"negative", func(s *wallet.State) { s.Balance = negative(t) }},
		{"version zero", func(s *wallet.State) { s.Version = 0 }}, {"version negative", func(s *wallet.State) { s.Version = -1 }},
		{"created", func(s *wallet.State) { s.CreatedAt = time.Time{} }}, {"updated", func(s *wallet.State) { s.UpdatedAt = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.edit(&s)
			if _, err := wallet.Rehydrate(s); err == nil {
				t.Fatal("accepted invalid state")
			}
		})
	}
}
