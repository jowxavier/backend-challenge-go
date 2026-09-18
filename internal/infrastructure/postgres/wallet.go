package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
)

type WalletRepository struct {
	db db
	tx pgx.Tx
}

func NewWalletRepository(pool *pgxpool.Pool) *WalletRepository { return &WalletRepository{db: pool} }

const walletColumns = "id, player_id, currency, balance_minor, version, created_at, updated_at"

func (r *WalletRepository) Insert(ctx context.Context, w wallet.Wallet) error {
	n, err := w.Balance().MinorUnits()
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `INSERT INTO wallets (`+walletColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`, w.ID(), w.PlayerID(), w.Currency(), n, w.Version(), w.CreatedAt(), w.UpdatedAt())
	return databaseError(err)
}
func (r *WalletRepository) GetByID(ctx context.Context, id string) (wallet.Wallet, error) {
	return scanWallet(r.db.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id=$1`, id))
}
func (r *WalletRepository) GetForUpdate(ctx context.Context, id string) (wallet.Wallet, error) {
	if r.tx == nil {
		return wallet.Wallet{}, ErrTransactionRequired
	}
	return scanWallet(r.tx.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id=$1 FOR UPDATE`, id))
}
func (r *WalletRepository) UpdateBalance(ctx context.Context, w wallet.Wallet, expectedVersion int64) error {
	n, err := w.Balance().MinorUnits()
	if err != nil {
		return err
	}
	tag, err := r.db.Exec(ctx, `UPDATE wallets SET balance_minor=$1, version=$2, updated_at=$3 WHERE id=$4 AND version=$5`, n, w.Version(), w.UpdatedAt(), w.ID(), expectedVersion)
	if err != nil {
		return databaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleWalletWrite
	}
	return nil
}
func scanWallet(row pgx.Row) (wallet.Wallet, error) {
	var s wallet.State
	var n int64
	if err := row.Scan(&s.ID, &s.PlayerID, &s.Currency, &n, &s.Version, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return wallet.Wallet{}, scanError(err)
	}
	m, err := persistedMoney(n, s.Currency)
	if err != nil {
		return wallet.Wallet{}, err
	}
	s.Balance = m
	s.CreatedAt = s.CreatedAt.UTC()
	s.UpdatedAt = s.UpdatedAt.UTC()
	w, err := wallet.Rehydrate(s)
	if err != nil {
		return wallet.Wallet{}, persistedError(err)
	}
	return w, nil
}
