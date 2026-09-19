//go:build integration

package postgres

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

func TestReferenceWorkerRecovery(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	now := time.Now().UTC()
	p, err := financial.NewProcessor(NewRunner(pool), time.Hour, func() time.Time { return now })
	must(t, err)
	pending := processOK(t, p, referenceRequest(t, "refund", wt.REFUND, 8000, "bet"))
	worker := ReferenceWorker{pool, p}
	found, err := worker.RunOnce(ctx)
	must(t, err)
	if !found {
		t.Fatal("pending not discovered")
	}
	var attempts int
	var next, deadline time.Time
	must(t, pool.QueryRow(ctx, `SELECT reference_attempts,reference_next_attempt_at,reference_deadline_at FROM wager_transactions WHERE id=$1`, pending.TransactionID).Scan(&attempts, &next, &deadline))
	if attempts != 1 || !next.After(now) {
		t.Fatal("schedule not persisted", attempts, next)
	}
	restarted := ReferenceWorker{pool, p}
	found, err = restarted.RunOnce(ctx)
	must(t, err)
	if found {
		t.Fatal("restart reset backoff")
	}
	var status string
	must(t, pool.QueryRow(ctx, `SELECT status FROM wager_transactions WHERE id=$1`, pending.TransactionID).Scan(&status))
	if status != "PENDING_REFERENCE" {
		t.Fatal(status)
	}
	// Make the next attempt due without a wall-clock sleep.
	_, err = pool.Exec(ctx, `UPDATE wager_transactions SET reference_next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1`, pending.TransactionID)
	must(t, err)
	found, err = restarted.RunOnce(ctx)
	must(t, err)
	if !found {
		t.Fatal("retry missing")
	}
	var second time.Time
	must(t, pool.QueryRow(ctx, `SELECT reference_attempts,reference_next_attempt_at FROM wager_transactions WHERE id=$1`, pending.TransactionID).Scan(&attempts, &second))
	if attempts != 2 || !second.After(next) {
		t.Fatal("backoff reset", attempts, second, next)
	}
	processOK(t, p, request(t, "bet", "w", wt.BET, 8000))
	_, err = pool.Exec(ctx, `UPDATE wager_transactions SET reference_next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1`, pending.TransactionID)
	must(t, err)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, e := (&ReferenceWorker{pool, p}).RunOnce(ctx); errs <- e }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		must(t, e)
	}
	r, err := NewWagerTransactionRepository(pool).FindByProviderID(ctx, "provider", pending.TransactionID)
	must(t, err)
	assertOutcome(t, resultOfTest(r), wt.PROCESSED, "")
	if count(t, pool, "wallet_ledger_entries") != 2 || count(t, pool, "outbox_events") != 5 {
		t.Fatal("duplicate/missing effects")
	}
	if !r.ReferenceDeadline.Equal(deadline) {
		t.Fatal("deadline moved")
	}
	exp := processOK(t, p, referenceRequest(t, "expired", wt.REFUND, 1, "absent"))
	now = now.Add(time.Hour)
	_, err = pool.Exec(ctx, `UPDATE wager_transactions SET reference_next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1`, exp.TransactionID)
	must(t, err)
	_, err = worker.RunOnce(ctx)
	must(t, err)
	r, err = NewWagerTransactionRepository(pool).FindByProviderID(ctx, "provider", exp.TransactionID)
	must(t, err)
	if r.Transaction.Status() != wt.REJECTED || r.Transaction.FailureCode() != wt.ReferenceNotFound {
		t.Fatal("expiration not durable")
	}
	if count(t, pool, "outbox_events") != 7 {
		t.Fatal("expiration event missing")
	}
}
func resultOfTest(r financial.Record) financial.ProcessResult {
	return financial.ProcessResult{Status: r.Transaction.Status(), FailureCode: r.Transaction.FailureCode()}
}
func TestReferenceScheduleMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	for _, name := range []string{"000008_reference_schedule.down.sql", "000008_reference_schedule.up.sql"} {
		sql, err := os.ReadFile("../../../migrations/" + name)
		must(t, err)
		_, err = pool.Exec(ctx, string(sql))
		must(t, err)
	}
}
