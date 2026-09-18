package postgres

import (
	"context"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
)

type IdempotencyRepository struct{ db db }

func (r *IdempotencyRepository) Lookup(ctx context.Context, provider, key string) (financial.Record, error) {
	return scanFinancial(r.db.QueryRow(ctx, `SELECT `+financialColumns+` FROM wager_transactions WHERE id=(SELECT transaction_id FROM financial_idempotency_keys WHERE provider_id=$1 AND idempotency_key=$2) AND provider_id=$1`, provider, key))
}
func (r *IdempotencyRepository) Bind(ctx context.Context, provider, key, id string) error {
	_, err := r.db.Exec(ctx, `INSERT INTO financial_idempotency_keys(provider_id,idempotency_key,transaction_id) VALUES($1,$2,$3) ON CONFLICT (provider_id,idempotency_key) DO NOTHING`, provider, key, id)
	if err != nil {
		return databaseError(err)
	}
	var actual string
	if err := r.db.QueryRow(ctx, `SELECT transaction_id FROM financial_idempotency_keys WHERE provider_id=$1 AND idempotency_key=$2`, provider, key).Scan(&actual); err != nil {
		return databaseError(err)
	}
	if actual != id {
		return financial.ErrConflict
	}
	return nil
}
