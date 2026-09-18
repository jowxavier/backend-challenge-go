package postgres

import (
	"context"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
)

func (r *Runner) WithinFinancialTransaction(ctx context.Context, work func(financial.Repositories) error) error {
	return r.WithinTransaction(ctx, func(repos *Repositories) error {
		return work(financial.Repositories{Wallets: repos.Wallets, Transactions: repos.WagerTransactions, Keys: &IdempotencyRepository{db: repos.WagerTransactions.db}, Ledger: &LedgerRepository{db: repos.WagerTransactions.db}})
	})
}

var _ financial.Transactor = (*Runner)(nil)
