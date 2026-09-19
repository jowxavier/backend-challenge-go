package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/application/events"
	"github.com/jowxavier/backend-challenge-go/internal/application/outbox"
)

type OutboxWriter struct{ db db }

func (r *OutboxWriter) Insert(ctx context.Context, e events.Event) error {
	if !e.Valid() {
		return events.ErrInvalidEvent
	}
	_, err := r.db.Exec(ctx, `INSERT INTO outbox_events(id,transaction_id,aggregate_id,event_type,event_version,payload,occurred_at,next_attempt_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7)`, e.ID(), e.TransactionID(), e.AggregateID(), e.Type(), e.Version(), e.JSON(), e.OccurredAt())
	return databaseError(err)
}

type OutboxDelivery struct{ pool *pgxpool.Pool }

func NewOutboxDelivery(pool *pgxpool.Pool) *OutboxDelivery { return &OutboxDelivery{pool} }
func (r *OutboxDelivery) ClaimBatch(ctx context.Context, now time.Time, limit int, lease time.Duration) (claims []outbox.Claim, err error) {
	if now.IsZero() || limit < 1 || lease <= 0 {
		return nil, outbox.ErrInvalidConfig
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		rb := tx.Rollback(cleanup)
		if rb != nil && !errors.Is(rb, pgx.ErrTxClosed) {
			err = errors.Join(err, rb)
		}
		if err != nil {
			claims = nil
		}
	}()
	var tokenBytes [16]byte
	if _, err = rand.Read(tokenBytes[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	rows, err := tx.Query(ctx, `WITH eligible AS (
 SELECT id FROM outbox_events WHERE published_at IS NULL AND next_attempt_at<=$1 AND (lease_until IS NULL OR lease_until<=$1)
 ORDER BY next_attempt_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
 ) UPDATE outbox_events e SET lease_token=$3,lease_until=$4,attempt_count=e.attempt_count+1
 FROM eligible WHERE e.id=eligible.id RETURNING e.payload,e.attempt_count`, now.UTC(), limit, token, now.Add(lease).UTC())
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		var attempt int64
		if err = rows.Scan(&raw, &attempt); err != nil {
			rows.Close()
			return nil, err
		}
		var event events.Event
		event, err = events.Restore(raw)
		if err != nil {
			rows.Close()
			return nil, err
		}
		claims = append(claims, outbox.Claim{Event: event, LeaseToken: token, Attempt: attempt})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return claims, nil
}
func (r *OutboxDelivery) MarkPublished(ctx context.Context, id, token string, at time.Time) error {
	if id == "" || token == "" || at.IsZero() {
		return outbox.ErrInvalidClaim
	}
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events SET published_at=$3,lease_token=NULL,lease_until=NULL,last_error=NULL WHERE id=$1 AND lease_token=$2 AND published_at IS NULL`, id, token, at.UTC())
	if err != nil {
		return databaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return outbox.ErrClaimLost
	}
	return nil
}
func (r *OutboxDelivery) Reschedule(ctx context.Context, id, token string, next time.Time, diagnostic string) error {
	if id == "" || token == "" || next.IsZero() {
		return outbox.ErrInvalidClaim
	}
	// Only known categories may be persisted, even when called outside the service.
	switch diagnostic {
	case "publish_failed", "publish_timeout", "publish_cancelled":
	default:
		diagnostic = "publish_failed"
	}
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at=$3,last_error=$4,lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND published_at IS NULL`, id, token, next.UTC(), diagnostic)
	if err != nil {
		return databaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return outbox.ErrClaimLost
	}
	return nil
}

var _ outbox.Delivery = (*OutboxDelivery)(nil)
