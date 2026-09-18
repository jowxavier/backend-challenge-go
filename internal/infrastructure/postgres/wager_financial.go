package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

const financialColumns = wagerColumns + ", payload_hash, result_balance_minor, result_currency, reference_transaction_id, reference_deadline_at"

func (r *WagerTransactionRepository) FindFinancial(ctx context.Context, provider, external string) (financial.Record, error) {
	return scanFinancial(r.db.QueryRow(ctx, `SELECT `+financialColumns+` FROM wager_transactions WHERE provider_id=$1 AND external_transaction_id=$2`, provider, external))
}

func (r *WagerTransactionRepository) TryInsertExternal(ctx context.Context, t *wt.WagerTransaction, hash [32]byte) (bool, error) {
	if t == nil || t.Status() != wt.PENDING {
		return false, wt.ErrInvalidTransaction
	}
	n, err := t.Money().MinorUnits()
	if err != nil {
		return false, err
	}
	c, err := t.Money().Currency()
	if err != nil {
		return false, err
	}
	tag, err := r.db.Exec(ctx, `INSERT INTO wager_transactions (`+wagerColumns+`,payload_hash) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) ON CONFLICT ON CONSTRAINT wager_financial_identity_unique DO NOTHING`, t.ID(), t.ProviderID(), t.ExternalTransactionID(), t.PlayerID(), t.WalletID(), t.RoundID(), t.GameID(), t.Kind(), n, c, optional(t.ReferenceExternalTransactionID()), t.Status(), nil, t.CreatedAt(), t.UpdatedAt(), hash[:])
	if err != nil {
		return false, databaseError(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *WagerTransactionRepository) CompleteOutcome(ctx context.Context, t *wt.WagerTransaction, expected wt.Status, balance money.Money) error {
	if t == nil || (t.Status() != wt.PROCESSED && t.Status() != wt.REJECTED) || (expected != wt.PENDING && expected != wt.PENDING_REFERENCE) {
		return wt.ErrInvalidTransition
	}
	n, err := balance.MinorUnits()
	if err != nil {
		return err
	}
	if n < 0 {
		return money.ErrInvalidAmount
	}
	c, err := balance.Currency()
	if err != nil {
		return err
	}
	tag, err := r.db.Exec(ctx, `UPDATE wager_transactions SET status=$1,failure_code=$2,updated_at=$3,result_balance_minor=$4,result_currency=$5 WHERE id=$6 AND status=$7`, t.Status(), optional(string(t.FailureCode())), t.UpdatedAt(), n, c, t.ID(), expected)
	if err != nil {
		return databaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleOutcomeWrite
	}
	return nil
}

func scanFinancial(row pgx.Row) (financial.Record, error) {
	var s wt.State
	var n int64
	var c string
	var reference, code, resultCurrency *string
	var hash []byte
	var balance *int64
	var referenceID *string
	var deadline *time.Time
	if err := row.Scan(&s.ID, &s.ProviderID, &s.ExternalTransactionID, &s.PlayerID, &s.WalletID, &s.RoundID, &s.GameID, &s.Kind, &n, &c, &reference, &s.Status, &code, &s.CreatedAt, &s.UpdatedAt, &hash, &balance, &resultCurrency, &referenceID, &deadline); err != nil {
		return financial.Record{}, scanError(err)
	}
	if reference != nil {
		if *reference == "" {
			return financial.Record{}, persistedError(wt.ErrInvalidReference)
		}
		s.ReferenceExternalTransactionID = *reference
	}
	if code != nil {
		if *code == "" {
			return financial.Record{}, persistedError(wt.ErrInvalidFailureCode)
		}
		s.FailureCode = wt.FailureCode(*code)
	}
	m, err := persistedMoney(n, c)
	if err != nil {
		return financial.Record{}, err
	}
	s.Money = m
	s.CreatedAt = s.CreatedAt.UTC()
	s.UpdatedAt = s.UpdatedAt.UTC()
	t, err := wt.Rehydrate(s)
	if err != nil {
		return financial.Record{}, persistedError(err)
	}
	if len(hash) != 32 || (balance == nil) != (resultCurrency == nil) {
		return financial.Record{}, persistedError(financial.ErrInvalidPersistedData)
	}
	record := financial.Record{Transaction: t, PayloadHash: [32]byte(hash), ReferenceDeadline: deadline}
	if referenceID != nil {
		if *referenceID == "" || *referenceID == t.ID() || t.ReferenceExternalTransactionID() == "" {
			return financial.Record{}, persistedError(wt.ErrInvalidReference)
		}
		record.ReferenceTransactionID = *referenceID
	}
	if deadline != nil {
		d := deadline.UTC()
		record.ReferenceDeadline = &d
		if d.IsZero() || t.ReferenceExternalTransactionID() == "" {
			return financial.Record{}, persistedError(wt.ErrInvalidTime)
		}
	}
	if (t.Status() == wt.PENDING_REFERENCE && deadline == nil) || (t.Status() == wt.PROCESSED && t.ReferenceExternalTransactionID() != "" && referenceID == nil) {
		return financial.Record{}, persistedError(wt.ErrInvalidReference)
	}
	if balance != nil {
		m, err := persistedMoney(*balance, *resultCurrency)
		if err != nil {
			return financial.Record{}, err
		}
		if *balance < 0 {
			return financial.Record{}, persistedError(money.ErrInvalidAmount)
		}
		record.Balance = &m
	}
	if ((s.Status == wt.PROCESSED || s.Status == wt.REJECTED) && record.Balance == nil) || ((s.Status == wt.PENDING || s.Status == wt.PENDING_REFERENCE) && record.Balance != nil) {
		return financial.Record{}, persistedError(financial.ErrInvalidPersistedData)
	}
	return record, nil
}
