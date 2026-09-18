package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/config"
	"go.uber.org/fx"
)

var Module = fx.Module("postgres", fx.Provide(NewPool, NewRunner, NewWalletRepository, NewWagerTransactionRepository), fx.Invoke(func(*pgxpool.Pool) {}))

func NewPool(lc fx.Lifecycle, cfg *config.Config) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid DATABASE_URL")
	}
	// Connection attempts remain bounded even when callers have no deadline.
	pc.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(context.Background(), pc)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				// Fx does not stop a hook whose own startup failed.
				pool.Close()
				return fmt.Errorf("ping postgres: %w", err)
			}
			return nil
		},
		OnStop: func(context.Context) error { pool.Close(); return nil },
	})
	return pool, nil
}
