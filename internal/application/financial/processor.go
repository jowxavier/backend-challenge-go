package financial

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jowxavier/backend-challenge-go/internal/domain/ledger"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
)

type Processor struct{ transactions Transactor }

func NewProcessor(transactions Transactor) *Processor { return &Processor{transactions: transactions} }

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (p *Processor) Process(ctx context.Context, r ProcessRequest) (ProcessResult, error) {
	if r.Kind != wt.BET && r.Kind != wt.WIN && r.Kind != wt.LOSS {
		return ProcessResult{}, ErrUnsupportedOperation
	}
	if r.Kind == wt.WIN && r.ReferenceExternalTransactionID != "" {
		return ProcessResult{}, ErrUnsupportedOperation
	}
	if r.IdempotencyKey == "" || !utf8.ValidString(r.IdempotencyKey) || strings.IndexFunc(r.IdempotencyKey, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) >= 0 {
		return ProcessResult{}, ErrInvalidInput
	}
	id, err := newID()
	if err != nil {
		return ProcessResult{}, err
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	tx, err := wt.NewExternal(wt.ExternalInput{ID: id, ProviderID: r.ProviderID, ExternalTransactionID: r.ExternalTransactionID, PlayerID: r.PlayerID, WalletID: r.WalletID, RoundID: r.RoundID, GameID: r.GameID, Kind: r.Kind, Money: r.Money, ReferenceExternalTransactionID: r.ReferenceExternalTransactionID}, at)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	hash, err := Fingerprint(r)
	if err != nil {
		return ProcessResult{}, err
	}
	var result ProcessResult
	err = p.transactions.WithinFinancialTransaction(ctx, func(repos Repositories) error {
		replay := func(record Record) error {
			if record.Transaction == nil {
				return ErrInvalidPersistedData
			}
			if record.PayloadHash != hash {
				return ErrConflict
			}
			if err := repos.Keys.Bind(ctx, r.ProviderID, r.IdempotencyKey, record.Transaction.ID()); err != nil {
				return err
			}
			t := record.Transaction
			if (t.Status() == wt.PROCESSED || t.Status() == wt.REJECTED) && record.Balance == nil {
				return ErrInvalidPersistedData
			}
			result = ProcessResult{TransactionID: t.ID(), Status: t.Status(), FailureCode: t.FailureCode(), Balance: record.Balance, IdempotentReplay: true}
			return nil
		}
		record, err := repos.Keys.Lookup(ctx, r.ProviderID, r.IdempotencyKey)
		if err == nil {
			return replay(record)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		record, err = repos.Transactions.FindFinancial(ctx, r.ProviderID, r.ExternalTransactionID)
		if err == nil {
			return replay(record)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		w, err := repos.Wallets.GetForUpdate(ctx, r.WalletID)
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidContext
		}
		if err != nil {
			return err
		}
		if w.PlayerID() != r.PlayerID {
			return ErrInvalidContext
		}
		inserted, err := repos.Transactions.TryInsertExternal(ctx, tx, hash)
		if err != nil {
			return err
		}
		if !inserted {
			record, err := repos.Transactions.FindFinancial(ctx, r.ProviderID, r.ExternalTransactionID)
			if err != nil {
				return err
			}
			return replay(record)
		}
		if err := repos.Keys.Bind(ctx, r.ProviderID, r.IdempotencyKey, tx.ID()); err != nil {
			return err
		}
		before, version := w.Balance(), w.Version()
		currency, _ := r.Money.Currency()
		var code wt.FailureCode
		if currency != w.Currency() {
			code = wt.CurrencyMismatch
		} else {
			switch r.Kind {
			case wt.BET:
				err = w.Debit(r.Money, at)
			case wt.WIN:
				err = w.Credit(r.Money, at)
			}
			switch {
			case errors.Is(err, wallet.ErrInsufficientBalance):
				code = wt.BetInsufficientFunds
			case errors.Is(err, money.ErrOverflow):
				code = wt.MonetaryOverflow
			case err != nil:
				return err
			}
		}
		if code != "" {
			if err := tx.Reject(code, at); err != nil {
				return err
			}
		} else {
			if r.Kind != wt.LOSS {
				direction := ledger.Debit
				if r.Kind == wt.WIN {
					direction = ledger.Credit
				}
				entryID, err := newID()
				if err != nil {
					return err
				}
				entry, err := ledger.New(entryID, w.ID(), tx.ID(), direction, r.Money, before, w.Balance(), at)
				if err != nil {
					return err
				}
				if err := repos.Wallets.UpdateBalance(ctx, w, version); err != nil {
					return err
				}
				if err := repos.Ledger.Insert(ctx, entry); err != nil {
					return err
				}
			}
			if err := tx.MarkProcessed(at); err != nil {
				return err
			}
		}
		balance := w.Balance()
		if err := repos.Transactions.CompleteOutcome(ctx, tx, wt.PENDING, balance); err != nil {
			return err
		}
		result = ProcessResult{TransactionID: tx.ID(), Status: tx.Status(), FailureCode: tx.FailureCode(), Balance: &balance}
		return nil
	})
	// A callback result is provisional until COMMIT is confirmed.
	if err != nil {
		return ProcessResult{}, err
	}
	return result, nil
}
