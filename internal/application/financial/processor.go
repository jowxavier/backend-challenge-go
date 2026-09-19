package financial

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jowxavier/backend-challenge-go/internal/observability"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

type Processor struct {
	transactions Transactor
	pendingTTL   time.Duration
	now          func() time.Time
}

func NewProcessor(transactions Transactor, pendingTTL time.Duration, now func() time.Time) (*Processor, error) {
	if transactions == nil || pendingTTL <= 0 || now == nil {
		return nil, ErrInvalidInput
	}
	return &Processor{transactions: transactions, pendingTTL: pendingTTL, now: now}, nil
}
func (p *Processor) processingTime() time.Time { return p.now().UTC().Truncate(time.Microsecond) }

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (p *Processor) prepare(r ProcessRequest) (*wt.WagerTransaction, [32]byte, error) {
	if r.Kind != wt.BET && r.Kind != wt.WIN && r.Kind != wt.LOSS && r.Kind != wt.REFUND && r.Kind != wt.ROLLBACK {
		return nil, [32]byte{}, ErrUnsupportedOperation
	}
	if r.IdempotencyKey == "" || !utf8.ValidString(r.IdempotencyKey) || strings.IndexFunc(r.IdempotencyKey, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) >= 0 {
		return nil, [32]byte{}, ErrInvalidInput
	}
	id, err := newID()
	if err != nil {
		return nil, [32]byte{}, err
	}
	at := p.processingTime()
	tx, err := wt.NewExternal(wt.ExternalInput{ID: id, ProviderID: r.ProviderID, ExternalTransactionID: r.ExternalTransactionID, PlayerID: r.PlayerID, WalletID: r.WalletID, RoundID: r.RoundID, GameID: r.GameID, Kind: r.Kind, Money: r.Money, ReferenceExternalTransactionID: r.ReferenceExternalTransactionID}, at)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	hash, err := Fingerprint(r)
	if err != nil {
		return nil, [32]byte{}, err
	}
	return tx, hash, nil
}

func (p *Processor) Process(ctx context.Context, r ProcessRequest) (result ProcessResult, finalErr error) {
	start := time.Now()
	defer func() {
		observability.Outcome(string(result.Status), result.IdempotentReplay, time.Since(start))
		observability.Logger.Info("financial outcome", "providerId", r.ProviderID, "externalTransactionId", r.ExternalTransactionID, "walletId", r.WalletID, "transactionId", result.TransactionID, "correlationId", result.TransactionID, "status", result.Status, "failed", finalErr != nil)
	}()
	tx, hash, err := p.prepare(r)
	if err != nil {
		return ProcessResult{}, err
	}
	err = p.transactions.WithinFinancialTransaction(ctx, func(repos Repositories) error {
		result, err = p.process(ctx, repos, r, tx, hash)
		return err
	})
	if err != nil {
		return ProcessResult{}, err
	}
	return result, nil
}

// process uses only repositories bound to the caller's transaction.
func (p *Processor) process(ctx context.Context, repos Repositories, r ProcessRequest, tx *wt.WagerTransaction, hash [32]byte) (ProcessResult, error) {
	var result ProcessResult
	err := func() error {
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
		result, err = p.execute(ctx, repos, Record{Transaction: tx}, &w, p.processingTime())
		if err != nil {
			return err
		}
		return nil
	}()
	return result, err
}
