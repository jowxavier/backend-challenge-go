package postgres

import (
	"context"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"time"
)

func (r *WagerTransactionRepository) FindByProviderID(ctx context.Context, p, id string) (financial.Record, error) {
	return scanFinancial(r.db.QueryRow(ctx, `SELECT `+financialColumns+` FROM wager_transactions WHERE provider_id=$1 AND id=$2`, p, id))
}

// The application acquires the wallet lock before this operation-row lock.
func (r *WagerTransactionRepository) GetForResolution(ctx context.Context, p, id string) (financial.Record, error) {
	if r.tx == nil {
		return financial.Record{}, ErrTransactionRequired
	}
	return scanFinancial(r.tx.QueryRow(ctx, `SELECT `+financialColumns+` FROM wager_transactions WHERE provider_id=$1 AND id=$2 FOR UPDATE`, p, id))
}
func (r *WagerTransactionRepository) HasSuccessfulReversal(ctx context.Context, p, external string) (bool, error) {
	var found bool
	err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wager_transactions WHERE provider_id=$1 AND reference_external_transaction_id=$2 AND status='PROCESSED' AND kind IN ('REFUND','ROLLBACK'))`, p, external).Scan(&found)
	return found, databaseError(err)
}
func (r *WagerTransactionRepository) MarkPendingReference(ctx context.Context, t *wt.WagerTransaction, deadline time.Time) error {
	if t == nil || t.Status() != wt.PENDING_REFERENCE || deadline.IsZero() {
		return wt.ErrInvalidTransition
	}
	tag, err := r.db.Exec(ctx, `UPDATE wager_transactions SET status='PENDING_REFERENCE',updated_at=$1,reference_deadline_at=$2 WHERE id=$3 AND status='PENDING' AND reference_deadline_at IS NULL`, t.UpdatedAt(), deadline.UTC(), t.ID())
	if err != nil {
		return databaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleOutcomeWrite
	}
	return nil
}
func (r *WagerTransactionRepository) SetResolvedReference(ctx context.Context, t *wt.WagerTransaction, expected wt.Status, referenceID string) error {
	if t == nil || referenceID == "" || (expected != wt.PENDING && expected != wt.PENDING_REFERENCE) {
		return wt.ErrInvalidTransition
	}
	tag, err := r.db.Exec(ctx, `UPDATE wager_transactions SET reference_transaction_id=$1 WHERE id=$2 AND status=$3 AND (reference_transaction_id IS NULL OR reference_transaction_id=$1)`, referenceID, t.ID(), expected)
	if err != nil {
		return databaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleOutcomeWrite
	}
	return nil
}
