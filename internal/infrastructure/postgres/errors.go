package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
)

var (
	ErrNotFound                   = financial.ErrNotFound
	ErrDuplicateWalletIdentity    = errors.New("duplicate wallet identity")
	ErrDuplicateFinancialIdentity = errors.New("duplicate financial identity")
	ErrStaleWalletWrite           = errors.New("stale wallet write")
	ErrStaleOutcomeWrite          = errors.New("stale transaction outcome write")
	ErrInvalidPersistedData       = financial.ErrInvalidPersistedData
	ErrTransactionRequired        = errors.New("existing transaction required")
)

// db uses only the two pgx operations shared by a pool and a transaction.
type db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func databaseError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "wallets_player_currency_unique":
			return fmt.Errorf("%w: %w", ErrDuplicateWalletIdentity, err)
		case "wager_financial_identity_unique":
			return fmt.Errorf("%w: %w", ErrDuplicateFinancialIdentity, err)
		}
	}
	return fmt.Errorf("postgres: %w", err)
}
func persistedError(err error) error { return fmt.Errorf("%w: %w", ErrInvalidPersistedData, err) }
func persistedMoney(n int64, currency string) (money.Money, error) {
	m, err := money.FromMinorUnits(n, currency)
	if err != nil {
		return money.Money{}, persistedError(err)
	}
	canonical, err := m.Currency()
	if err != nil {
		return money.Money{}, persistedError(err)
	}
	// FromMinorUnits normalizes input; a stored value must already be canonical.
	if canonical != currency {
		return money.Money{}, persistedError(money.ErrInvalidCurrency)
	}
	return m, nil
}

func scanError(err error) error {
	var scanErr pgx.ScanArgError
	if errors.As(err, &scanErr) {
		return persistedError(err)
	}
	return databaseError(err)
}
