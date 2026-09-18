package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

type WagerTransactionRepository struct{ db db }

func NewWagerTransactionRepository(pool *pgxpool.Pool) *WagerTransactionRepository {
	return &WagerTransactionRepository{db: pool}
}

const wagerColumns = "id, provider_id, external_transaction_id, player_id, wallet_id, round_id, game_id, kind, amount_minor, currency, reference_external_transaction_id, status, failure_code, created_at, updated_at"

func optional(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (r *WagerTransactionRepository) Insert(ctx context.Context, t *wt.WagerTransaction) error {
	if t == nil {
		return wt.ErrInvalidTransaction
	}
	n, err := t.Money().MinorUnits()
	if err != nil {
		return err
	}
	c, err := t.Money().Currency()
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `INSERT INTO wager_transactions (`+wagerColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, t.ID(), t.ProviderID(), t.ExternalTransactionID(), t.PlayerID(), t.WalletID(), t.RoundID(), t.GameID(), t.Kind(), n, c, optional(t.ReferenceExternalTransactionID()), t.Status(), optional(string(t.FailureCode())), t.CreatedAt(), t.UpdatedAt())
	return databaseError(err)
}
func (r *WagerTransactionRepository) GetByID(ctx context.Context, id string) (*wt.WagerTransaction, error) {
	return scanWager(r.db.QueryRow(ctx, `SELECT `+wagerColumns+` FROM wager_transactions WHERE id=$1`, id))
}
func (r *WagerTransactionRepository) GetByFinancialIdentity(ctx context.Context, providerID, externalID string) (*wt.WagerTransaction, error) {
	return scanWager(r.db.QueryRow(ctx, `SELECT `+wagerColumns+` FROM wager_transactions WHERE provider_id=$1 AND external_transaction_id=$2`, providerID, externalID))
}
func (r *WagerTransactionRepository) UpdateOutcome(ctx context.Context, t *wt.WagerTransaction, expectedStatus wt.Status) error {
	if t == nil {
		return wt.ErrInvalidTransaction
	}
	tag, err := r.db.Exec(ctx, `UPDATE wager_transactions SET status=$1, failure_code=$2, updated_at=$3 WHERE id=$4 AND status=$5`, t.Status(), optional(string(t.FailureCode())), t.UpdatedAt(), t.ID(), expectedStatus)
	if err != nil {
		return databaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleOutcomeWrite
	}
	return nil
}
func scanWager(row pgx.Row) (*wt.WagerTransaction, error) {
	var s wt.State
	var n int64
	var c string
	var ref, code *string
	if err := row.Scan(&s.ID, &s.ProviderID, &s.ExternalTransactionID, &s.PlayerID, &s.WalletID, &s.RoundID, &s.GameID, &s.Kind, &n, &c, &ref, &s.Status, &code, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, scanError(err)
	}
	if ref != nil {
		if *ref == "" {
			return nil, persistedError(wt.ErrInvalidReference)
		}
		s.ReferenceExternalTransactionID = *ref
	}
	if code != nil {
		if *code == "" {
			return nil, persistedError(wt.ErrInvalidFailureCode)
		}
		s.FailureCode = wt.FailureCode(*code)
	}
	m, err := persistedMoney(n, c)
	if err != nil {
		return nil, err
	}
	s.Money = m
	s.CreatedAt = s.CreatedAt.UTC()
	s.UpdatedAt = s.UpdatedAt.UTC()
	t, err := wt.Rehydrate(s)
	if err != nil {
		return nil, persistedError(err)
	}
	return t, nil
}
