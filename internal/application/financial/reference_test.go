package financial

import (
	"github.com/jowxavier/backend-challenge-go/internal/domain/ledger"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"testing"
	"time"
)

func refTransaction(t *testing.T, kind wt.Kind, status wt.Status, amount string) *wt.WagerTransaction {
	t.Helper()
	m, _ := money.Parse(amount, "BRL")
	input := wt.ExternalInput{ID: "id", ProviderID: "p", ExternalTransactionID: "e", PlayerID: "u", WalletID: "w", RoundID: "r", GameID: "g", Kind: kind, Money: m}
	if kind == wt.REFUND || kind == wt.ROLLBACK {
		input.ReferenceExternalTransactionID = "original"
	}
	var code wt.FailureCode
	if status == wt.REJECTED {
		code = wt.ReferenceNotFound
	}
	if status == wt.FAILED {
		code = wt.PermanentInfrastructureFailure
	}
	tx, err := wt.Rehydrate(wt.State{ExternalInput: input, Status: status, FailureCode: code, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
func TestReferenceRules(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		kind, original         wt.Kind
		status                 wt.Status
		amount, originalAmount string
		direction              ledger.Direction
		code                   wt.FailureCode
		pending                bool
	}{
		{"refund", wt.REFUND, wt.BET, wt.PROCESSED, "10", "10", ledger.Credit, "", false},
		{"rollback bet", wt.ROLLBACK, wt.BET, wt.PROCESSED, "10", "10", ledger.Credit, "", false},
		{"rollback win", wt.ROLLBACK, wt.WIN, wt.PROCESSED, "10", "10", ledger.Debit, "", false},
		{"rollback refund", wt.ROLLBACK, wt.REFUND, wt.PROCESSED, "10", "10", ledger.Debit, "", false},
		{"win", wt.WIN, wt.BET, wt.PROCESSED, "20", "10", ledger.Credit, "", false},
		{"partial", wt.REFUND, wt.BET, wt.PROCESSED, "1", "10", "", wt.ReferenceMismatch, false},
		{"loss", wt.ROLLBACK, wt.LOSS, wt.PROCESSED, "1", "0", "", wt.ReferenceNotAllowed, false},
		{"rollback rollback", wt.ROLLBACK, wt.ROLLBACK, wt.PROCESSED, "10", "10", "", wt.ReferenceNotAllowed, false},
		{"win type", wt.WIN, wt.WIN, wt.PROCESSED, "10", "10", "", wt.ReferenceNotAllowed, false},
		{"pending", wt.REFUND, wt.BET, wt.PENDING, "10", "10", "", "", true},
		{"pending reference", wt.ROLLBACK, wt.REFUND, wt.PENDING_REFERENCE, "10", "10", "", "", true},
		{"rejected", wt.REFUND, wt.BET, wt.REJECTED, "10", "10", "", wt.ReferenceUnsuccessful, false},
		{"failed", wt.REFUND, wt.BET, wt.FAILED, "10", "10", "", wt.ReferenceUnsuccessful, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, c, p := referenceRules(refTransaction(t, tc.kind, wt.PENDING, tc.amount), refTransaction(t, tc.original, tc.status, tc.originalAmount))
			if d != tc.direction || c != tc.code || p != tc.pending {
				t.Fatal(d, c, p)
			}
		})
	}
}

func TestProcessorConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ttl   time.Duration
		clock func() time.Time
	}{
		{"zero", 0, time.Now}, {"negative", -time.Hour, time.Now}, {"nil clock", time.Hour, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewProcessor(transactionFunc(nil), tc.ttl, tc.clock); err == nil {
				t.Fatal("invalid processor configuration accepted")
			}
		})
	}
}
