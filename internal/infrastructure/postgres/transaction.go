package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Runner struct{ pool *pgxpool.Pool }

func NewRunner(pool *pgxpool.Pool) *Runner { return &Runner{pool: pool} }

// Repositories is scoped to one callback and must not escape it or be used concurrently.
type Repositories struct {
	Wallets           *WalletRepository
	WagerTransactions *WagerTransactionRepository
}

func (r *Runner) WithinTransaction(ctx context.Context, work func(*Repositories) error) (err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		// Cleanup must still run after request cancellation or a callback panic.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		rbErr := tx.Rollback(cleanup)
		if rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("rollback transaction: %w", rbErr))
		}
	}()
	repos := &Repositories{Wallets: &WalletRepository{db: tx, tx: tx}, WagerTransactions: &WagerTransactionRepository{db: tx}}
	if err = work(repos); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// A failed commit may have an unknown outcome. Never retry here.
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
