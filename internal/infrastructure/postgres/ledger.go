package postgres

import (
	"context"
	"github.com/jowxavier/backend-challenge-go/internal/domain/ledger"
)

type LedgerRepository struct{ db db }

func (r *LedgerRepository) Insert(ctx context.Context, e ledger.Entry) error {
	n, err := e.Money().MinorUnits()
	if err != nil {
		return err
	}
	c, err := e.Money().Currency()
	if err != nil {
		return err
	}
	before, err := e.BalanceBefore().MinorUnits()
	if err != nil {
		return err
	}
	after, err := e.BalanceAfter().MinorUnits()
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `INSERT INTO wallet_ledger_entries(id,wallet_id,transaction_id,direction,amount_minor,currency,balance_before_minor,balance_after_minor,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, e.ID(), e.WalletID(), e.TransactionID(), e.Direction(), n, c, before, after, e.CreatedAt())
	return databaseError(err)
}
