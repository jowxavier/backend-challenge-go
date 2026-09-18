package wagertransaction_test

import (
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"testing"
	"time"
)

func TestFinancialFailureCodes(t *testing.T) {
	for _, code := range []wt.FailureCode{wt.CurrencyMismatch, wt.MonetaryOverflow} {
		t.Run(string(code), func(t *testing.T) {
			m, _ := money.Parse("1", "BRL")
			at := time.Now().UTC()
			input := wt.ExternalInput{ID: "id", ProviderID: "p", ExternalTransactionID: "e", PlayerID: "u", WalletID: "w", RoundID: "r", GameID: "g", Kind: wt.WIN, Money: m}
			tx, err := wt.NewExternal(input, at)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Fail(code, at); err == nil {
				t.Fatal("business code accepted as infrastructure failure")
			}
			if err := tx.Reject(code, at); err != nil {
				t.Fatal(err)
			}
			restored, err := wt.Rehydrate(wt.State{ExternalInput: input, Status: wt.REJECTED, FailureCode: code, CreatedAt: at, UpdatedAt: at})
			if err != nil || *restored != *tx {
				t.Fatal(restored, err)
			}
		})
	}
}
