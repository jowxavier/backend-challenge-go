package postgres

import (
	"context"
	"errors"
	"github.com/jowxavier/backend-challenge-go/internal/observability"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
)

// Claims only schedule work. Financial locks are acquired by the existing resolver
// after this statement commits, preserving wallet-before-transaction lock ordering.
type ReferenceWorker struct {
	Pool      *pgxpool.Pool
	Processor *financial.Processor
}

func (w *ReferenceWorker) RunOnce(ctx context.Context) (bool, error) {
	var provider, id string
	err := w.Pool.QueryRow(ctx, `WITH candidate AS (
 SELECT id FROM wager_transactions WHERE status='PENDING_REFERENCE'
 AND COALESCE(reference_next_attempt_at,created_at)<=clock_timestamp()
 ORDER BY COALESCE(reference_next_attempt_at,created_at),id
 FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE wager_transactions t SET reference_attempts=LEAST(reference_attempts+1,30),
 reference_next_attempt_at=CASE WHEN reference_deadline_at>clock_timestamp()
 THEN LEAST(reference_deadline_at,clock_timestamp()+make_interval(secs=>LEAST(60,power(2,LEAST(reference_attempts,6)))::double precision))
 ELSE clock_timestamp()+interval '60 seconds' END
 FROM candidate c WHERE t.id=c.id RETURNING t.provider_id,t.id`).Scan(&provider, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	observability.Retries.Add(1)
	start := time.Now()
	r, err := w.Processor.ResolvePendingReference(ctx, provider, id)
	if err != nil {
		observability.Outcome("ERROR", false, time.Since(start))
	} else if !r.IdempotentReplay {
		observability.Outcome(string(r.Status), false, time.Since(start))
	}
	observability.Logger.Info("reference resolution", "providerId", provider, "transactionId", id, "status", r.Status, "failed", err != nil)
	return true, err
}
func (w *ReferenceWorker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		work, cancel := context.WithTimeout(ctx, 20*time.Second)
		found, err := w.RunOnce(work)
		cancel()
		if err != nil {
			observability.Logger.Warn("reference worker retry", "failed", true)
		}
		if !found || err != nil {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
