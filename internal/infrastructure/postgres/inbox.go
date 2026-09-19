package postgres

import (
	"context"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
)

type InboxRepository struct{ db db }

func (r *InboxRepository) TryInsert(ctx context.Context, m financial.InboxRecord) (bool, error) {
	if m.ConsumerName == "" || m.MessageID == "" || m.ReceivedAt.IsZero() || m.CompletedAt != nil || m.TransactionID != "" {
		return false, financial.ErrInvalidInput
	}
	tag, err := r.db.Exec(ctx, `INSERT INTO inbox_messages(consumer_name,message_id,payload_hash,received_at) VALUES($1,$2,$3,$4) ON CONFLICT(consumer_name,message_id) DO NOTHING`, m.ConsumerName, m.MessageID, m.Hash[:], m.ReceivedAt.UTC())
	if err != nil {
		return false, databaseError(err)
	}
	return tag.RowsAffected() == 1, nil
}
func (r *InboxRepository) Find(ctx context.Context, consumer, id string) (financial.InboxRecord, error) {
	var m financial.InboxRecord
	var hash []byte
	var transaction *string
	err := r.db.QueryRow(ctx, `SELECT consumer_name,message_id,payload_hash,received_at,completed_at,transaction_id FROM inbox_messages WHERE consumer_name=$1 AND message_id=$2`, consumer, id).Scan(&m.ConsumerName, &m.MessageID, &hash, &m.ReceivedAt, &m.CompletedAt, &transaction)
	if err != nil {
		return m, scanError(err)
	}
	if len(hash) != 32 || (m.CompletedAt == nil) != (transaction == nil) {
		return m, financial.ErrInvalidPersistedData
	}
	m.Hash = [32]byte(hash)
	m.ReceivedAt = m.ReceivedAt.UTC()
	if m.CompletedAt != nil {
		at := m.CompletedAt.UTC()
		m.CompletedAt = &at
	}
	if transaction != nil {
		m.TransactionID = *transaction
	}
	return m, nil
}
func (r *InboxRepository) Complete(ctx context.Context, consumer, id, transaction string, at time.Time) error {
	if transaction == "" || at.IsZero() {
		return financial.ErrInvalidInput
	}
	tag, err := r.db.Exec(ctx, `UPDATE inbox_messages SET transaction_id=$3,completed_at=$4 WHERE consumer_name=$1 AND message_id=$2 AND completed_at IS NULL AND transaction_id IS NULL`, consumer, id, transaction, at.UTC())
	if err != nil {
		return databaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleOutcomeWrite
	}
	return nil
}
