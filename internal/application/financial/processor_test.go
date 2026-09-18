package financial

import (
	"context"
	"errors"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"testing"
	"time"
)

type transactionFunc func(context.Context, func(Repositories) error) error

func (f transactionFunc) WithinFinancialTransaction(ctx context.Context, work func(Repositories) error) error {
	return f(ctx, work)
}

type replayKeys struct{ record Record }

func (k replayKeys) Lookup(context.Context, string, string) (Record, error) { return k.record, nil }
func (k replayKeys) Bind(context.Context, string, string, string) error     { return nil }

func TestUnconfirmedCommitDiscardsResult(t *testing.T) {
	m, _ := money.Parse("1", "BRL")
	req := ProcessRequest{ProviderID: "p", IdempotencyKey: "k", ExternalTransactionID: "e", PlayerID: "u", WalletID: "w", RoundID: "r", GameID: "g", Kind: wt.BET, Money: m}
	tx, err := wt.NewExternal(wt.ExternalInput{ID: "t", ProviderID: "p", ExternalTransactionID: "e", PlayerID: "u", WalletID: "w", RoundID: "r", GameID: "g", Kind: wt.BET, Money: m}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.MarkProcessed(time.Now()); err != nil {
		t.Fatal(err)
	}
	hash, _ := Fingerprint(req)
	sentinel := errors.New("commit outcome unknown")
	calls := 0
	p := testProcessor(t, transactionFunc(func(ctx context.Context, work func(Repositories) error) error {
		calls++
		if err := work(Repositories{Keys: replayKeys{Record{Transaction: tx, PayloadHash: hash, Balance: &m}}}); err != nil {
			return err
		}
		return sentinel
	}))
	result, err := p.Process(context.Background(), req)
	if !errors.Is(err, sentinel) || result != (ProcessResult{}) || calls != 1 {
		t.Fatal(result, err, calls)
	}
}
func TestInputRejectedBeforeTransaction(t *testing.T) {
	m, _ := money.Parse("1", "BRL")
	base := ProcessRequest{ProviderID: "p", IdempotencyKey: "k", ExternalTransactionID: "e", PlayerID: "u", WalletID: "w", RoundID: "r", GameID: "g", Kind: wt.BET, Money: m}
	p := testProcessor(t, transactionFunc(func(context.Context, func(Repositories) error) error {
		t.Fatal("transaction started for invalid input")
		return nil
	}))
	for _, tc := range []struct {
		name   string
		change func(*ProcessRequest)
	}{
		{"refund", func(r *ProcessRequest) { r.Kind = wt.REFUND }},
		{"rollback", func(r *ProcessRequest) { r.Kind = wt.ROLLBACK }},
		{"opening", func(r *ProcessRequest) { r.Kind = "OPENING" }},
		{"self reference", func(r *ProcessRequest) { r.Kind = wt.WIN; r.ReferenceExternalTransactionID = r.ExternalTransactionID }},
		{"key", func(r *ProcessRequest) { r.IdempotencyKey = "" }},
		{"money", func(r *ProcessRequest) { r.Money = money.Money{} }},
		{"player", func(r *ProcessRequest) { r.PlayerID = "" }},
		{"zero bet", func(r *ProcessRequest) { r.Money, _ = money.Zero("BRL") }},
		{"positive loss", func(r *ProcessRequest) { r.Kind = wt.LOSS }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.change(&r)
			if _, err := p.Process(context.Background(), r); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func testProcessor(t *testing.T, tr Transactor) *Processor {
	t.Helper()
	p, err := NewProcessor(tr, 24*time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
