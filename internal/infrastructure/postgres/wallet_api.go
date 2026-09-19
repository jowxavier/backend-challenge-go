package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/domain/ledger"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
)

type WalletAPIStore struct {
	pool   *pgxpool.Pool
	runner *Runner
}

func NewWalletAPIStore(pool *pgxpool.Pool) *WalletAPIStore {
	return &WalletAPIStore{pool, NewRunner(pool)}
}
func (r *WalletAPIStore) Create(ctx context.Context, c financial.WalletCreation) error {
	return r.runner.WithinTransaction(ctx, func(repos *Repositories) error {
		if err := repos.Wallets.Insert(ctx, c.Wallet); err != nil {
			return err
		}
		if c.Opening == nil {
			return nil
		}
		t := c.Opening
		n, err := t.Money().MinorUnits()
		if err != nil {
			return err
		}
		currency, err := t.Money().Currency()
		if err != nil {
			return err
		}
		_, err = repos.WagerTransactions.db.Exec(ctx, `INSERT INTO wager_transactions(id,player_id,wallet_id,kind,amount_minor,currency,status,created_at,updated_at,result_balance_minor,result_currency) VALUES($1,$2,$3,'OPENING',$4,$5,'PROCESSED',$6,$6,$4,$5)`, t.ID(), t.PlayerID(), t.WalletID(), n, currency, t.CreatedAt())
		if err != nil {
			return databaseError(err)
		}
		if err = (&LedgerRepository{db: repos.WagerTransactions.db}).Insert(ctx, c.Entry); err != nil {
			return err
		}
		writer := OutboxWriter{db: repos.WagerTransactions.db}
		for _, event := range c.Events {
			if err = writer.Insert(ctx, event); err != nil {
				return err
			}
		}
		return nil
	})
}
func (r *WalletAPIStore) Get(ctx context.Context, id string) (wallet.Wallet, error) {
	return NewWalletRepository(r.pool).GetByID(ctx, id)
}

type ledgerCursor struct {
	Wallet, ID string
	At         time.Time
}

func (r *WalletAPIStore) Ledger(ctx context.Context, id, cursor string, limit int) (financial.LedgerPage, error) {
	if limit < 1 || limit > 100 {
		return financial.LedgerPage{}, financial.ErrInvalidInput
	}
	if _, err := r.Get(ctx, id); err != nil {
		return financial.LedgerPage{}, err
	}
	var c ledgerCursor
	if cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(b, &c) != nil || c.Wallet != id || c.ID == "" || c.At.IsZero() {
			return financial.LedgerPage{}, financial.ErrInvalidInput
		}
	}
	rows, err := r.pool.Query(ctx, `SELECT id,transaction_id,direction,amount_minor,currency,balance_before_minor,balance_after_minor,created_at FROM wallet_ledger_entries WHERE wallet_id=$1 AND ($2='' OR (created_at,id)>($3,$2)) ORDER BY created_at,id LIMIT $4`, id, c.ID, c.At, limit+1)
	if err != nil {
		return financial.LedgerPage{}, databaseError(err)
	}
	defer rows.Close()
	result := financial.LedgerPage{Entries: []ledger.Entry{}}
	for rows.Next() {
		var eid, tid, currency string
		var direction ledger.Direction
		var amount, before, after int64
		var at time.Time
		if err = rows.Scan(&eid, &tid, &direction, &amount, &currency, &before, &after, &at); err != nil {
			return result, err
		}
		m, err := persistedMoney(amount, currency)
		if err != nil {
			return result, err
		}
		b, err := persistedMoney(before, currency)
		if err != nil {
			return result, err
		}
		a, err := persistedMoney(after, currency)
		if err != nil {
			return result, err
		}
		entry, err := ledger.New(eid, id, tid, direction, m, b, a, at.UTC())
		if err != nil {
			return result, persistedError(err)
		}
		result.Entries = append(result.Entries, entry)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Entries) > limit {
		result.Entries = result.Entries[:limit]
		last := result.Entries[limit-1]
		b, _ := json.Marshal(ledgerCursor{id, last.ID(), last.CreatedAt()})
		result.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return result, nil
}
func (r *WalletAPIStore) Reconcile(ctx context.Context, id string) (financial.Reconciliation, error) {
	var stored int64
	var currency, sum string
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT w.balance_minor,w.currency,COALESCE(sum(CASE l.direction WHEN 'CREDIT' THEN l.amount_minor::numeric ELSE -l.amount_minor::numeric END),0)::text,count(l.id) FROM wallets w LEFT JOIN wallet_ledger_entries l ON l.wallet_id=w.id WHERE w.id=$1 GROUP BY w.id`, id).Scan(&stored, &currency, &sum, &count)
	if err != nil {
		return financial.Reconciliation{}, scanError(err)
	}
	n, err := strconv.ParseInt(sum, 10, 64)
	if err != nil {
		return financial.Reconciliation{}, err
	}
	s, err := money.FromMinorUnits(stored, currency)
	if err != nil {
		return financial.Reconciliation{}, err
	}
	c, err := money.FromMinorUnits(n, currency)
	if err != nil {
		return financial.Reconciliation{}, err
	}
	difference, err := s.Subtract(c)
	if err != nil {
		return financial.Reconciliation{}, err
	}
	return financial.Reconciliation{WalletID: id, Stored: s, Calculated: c, Difference: difference, Consistent: stored == n, CheckedEntries: count}, nil
}
