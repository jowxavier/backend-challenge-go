package wagertransaction_test

import (
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"testing"
	"time"
)

func TestRehydrate(t *testing.T) {
	for _, status := range []wt.Status{wt.PENDING, wt.PENDING_REFERENCE, wt.PROCESSED, wt.REJECTED, wt.FAILED} {
		for _, code := range []wt.FailureCode{"", wt.ReferenceNotFound, wt.BetInsufficientFunds, wt.ReversalInsufficientFunds, wt.PermanentInfrastructureFailure, "UNKNOWN"} {
			s := wt.State{ExternalInput: input(t, wt.REFUND), Status: status, FailureCode: code, CreatedAt: now, UpdatedAt: now.Add(-time.Hour)}
			tx, err := wt.Rehydrate(s)
			valid := (status == wt.REJECTED && (code == wt.ReferenceNotFound || code == wt.BetInsufficientFunds || code == wt.ReversalInsufficientFunds)) || (status == wt.FAILED && code == wt.PermanentInfrastructureFailure) || ((status == wt.PENDING || status == wt.PENDING_REFERENCE || status == wt.PROCESSED) && code == "")
			if !valid {
				if err == nil {
					t.Fatalf("accepted %s/%s", status, code)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if tx.Status() != status || tx.FailureCode() != code || tx.CreatedAt() != s.CreatedAt || tx.UpdatedAt() != s.UpdatedAt || tx.ID() != s.ID || tx.ProviderID() != s.ProviderID || tx.ExternalTransactionID() != s.ExternalTransactionID || tx.PlayerID() != s.PlayerID || tx.WalletID() != s.WalletID || tx.RoundID() != s.RoundID || tx.GameID() != s.GameID || tx.Kind() != s.Kind || tx.Money() != s.Money || tx.ReferenceExternalTransactionID() != s.ReferenceExternalTransactionID {
				t.Fatal("state not preserved")
			}
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*wt.State)
	}{
		{"id", func(s *wt.State) { s.ID = "" }}, {"kind", func(s *wt.State) { s.Kind = "OPENING" }},
		{"money", func(s *wt.State) { s.Money = money.Money{} }}, {"missing reference", func(s *wt.State) { s.ReferenceExternalTransactionID = "" }},
		{"self reference", func(s *wt.State) { s.ReferenceExternalTransactionID = s.ExternalTransactionID }},
		{"status", func(s *wt.State) { s.Status = "UNKNOWN" }}, {"created", func(s *wt.State) { s.CreatedAt = time.Time{} }}, {"updated", func(s *wt.State) { s.UpdatedAt = time.Time{} }},
		{"waiting without dependency", func(s *wt.State) {
			s.Kind = wt.WIN
			s.ReferenceExternalTransactionID = ""
			s.Status = wt.PENDING_REFERENCE
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := wt.State{ExternalInput: input(t, wt.REFUND), Status: wt.PENDING, CreatedAt: now, UpdatedAt: now}
			tc.edit(&s)
			if _, err := wt.Rehydrate(s); err == nil {
				t.Fatal("accepted invalid state")
			}
		})
	}
}
