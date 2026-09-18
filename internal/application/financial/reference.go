package financial

import (
	"context"
	"errors"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/domain/ledger"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
)

func resultOf(r Record, replay bool) ProcessResult {
	return ProcessResult{TransactionID: r.Transaction.ID(), Status: r.Transaction.Status(), FailureCode: r.Transaction.FailureCode(), Balance: r.Balance, IdempotentReplay: replay}
}
func terminal(s wt.Status) bool { return s == wt.PROCESSED || s == wt.REJECTED || s == wt.FAILED }

func (p *Processor) ResolvePendingReference(ctx context.Context, providerID, transactionID string) (ProcessResult, error) {
	var result ProcessResult
	err := p.transactions.WithinFinancialTransaction(ctx, func(repos Repositories) error {
		record, err := repos.Transactions.FindByProviderID(ctx, providerID, transactionID)
		if err != nil {
			return err
		}
		if terminal(record.Transaction.Status()) {
			result = resultOf(record, true)
			return nil
		}
		if record.Transaction.Status() != wt.PENDING_REFERENCE {
			return ErrInvalidPersistedData
		}
		w, err := repos.Wallets.GetForUpdate(ctx, record.Transaction.WalletID())
		if err != nil {
			return err
		}
		record, err = repos.Transactions.GetForResolution(ctx, providerID, transactionID)
		if err != nil {
			return err
		}
		if terminal(record.Transaction.Status()) {
			result = resultOf(record, true)
			return nil
		}
		if record.Transaction.Status() != wt.PENDING_REFERENCE || record.ReferenceDeadline == nil || w.PlayerID() != record.Transaction.PlayerID() {
			return ErrInvalidPersistedData
		}
		result, err = p.execute(ctx, repos, record, &w, p.processingTime())
		return err
	})
	if err != nil {
		return ProcessResult{}, err
	}
	return result, nil
}

// Reference rules use immutable business context. gameId is deliberately not
// compared: the README requires equality only for provider/player/wallet/currency/round.
func referenceRules(t, ref *wt.WagerTransaction) (ledger.Direction, wt.FailureCode, bool) {
	eligible := ref.Kind() == wt.BET || (t.Kind() == wt.ROLLBACK && (ref.Kind() == wt.WIN || ref.Kind() == wt.REFUND))
	if !eligible {
		return "", wt.ReferenceNotAllowed, false
	}
	currency, _ := t.Money().Currency()
	refCurrency, _ := ref.Money().Currency()
	if t.ProviderID() != ref.ProviderID() || t.PlayerID() != ref.PlayerID() || t.WalletID() != ref.WalletID() || currency != refCurrency || t.RoundID() != ref.RoundID() {
		return "", wt.ReferenceMismatch, false
	}
	if t.Kind() != wt.WIN {
		cmp, err := t.Money().Compare(ref.Money())
		if err != nil || cmp != 0 {
			return "", wt.ReferenceMismatch, false
		}
	}
	if ref.Status() == wt.REJECTED || ref.Status() == wt.FAILED {
		return "", wt.ReferenceUnsuccessful, false
	}
	if ref.Status() != wt.PROCESSED {
		return "", "", true
	}
	if t.Kind() == wt.ROLLBACK && ref.Kind() != wt.BET {
		return ledger.Debit, "", false
	}
	return ledger.Credit, "", false
}

func (p *Processor) execute(ctx context.Context, repos Repositories, record Record, w *wallet.Wallet, at time.Time) (ProcessResult, error) {
	t := record.Transaction
	expected := t.Status()
	if at.IsZero() {
		return ProcessResult{}, wt.ErrInvalidTime
	}
	direction := ledger.Credit
	if t.Kind() == wt.BET {
		direction = ledger.Debit
	}
	var code wt.FailureCode
	currency, _ := t.Money().Currency()
	if expected == wt.PENDING_REFERENCE && record.ReferenceDeadline != nil && !at.Before(*record.ReferenceDeadline) {
		code = wt.ReferenceNotFound
	} else if currency != w.Currency() {
		code = wt.CurrencyMismatch
	} else if t.ReferenceExternalTransactionID() != "" {
		ref, err := repos.Transactions.FindFinancial(ctx, t.ProviderID(), t.ReferenceExternalTransactionID())
		pending := errors.Is(err, ErrNotFound)
		if err != nil && !pending {
			return ProcessResult{}, err
		}
		if !pending {
			direction, code, pending = referenceRules(t, ref.Transaction)
		}
		if pending {
			if expected == wt.PENDING {
				if err := t.WaitForReference(at); err != nil {
					return ProcessResult{}, err
				}
				deadline := at.Add(p.pendingTTL)
				if err := repos.Transactions.MarkPendingReference(ctx, t, deadline); err != nil {
					return ProcessResult{}, err
				}
			}
			return resultOf(record, expected == wt.PENDING_REFERENCE), nil
		}
		if code == "" {
			if err := repos.Transactions.SetResolvedReference(ctx, t, expected, ref.Transaction.ID()); err != nil {
				return ProcessResult{}, err
			}
			if t.Kind() == wt.REFUND || t.Kind() == wt.ROLLBACK {
				reversed, err := repos.Transactions.HasSuccessfulReversal(ctx, t.ProviderID(), t.ReferenceExternalTransactionID())
				if err != nil {
					return ProcessResult{}, err
				}
				if reversed {
					code = wt.AlreadyReversed
				}
			}
		}
	}
	before, version := w.Balance(), w.Version()
	if code == "" && t.Kind() != wt.LOSS {
		var err error
		if direction == ledger.Debit {
			err = w.Debit(t.Money(), at)
		} else {
			err = w.Credit(t.Money(), at)
		}
		switch {
		case errors.Is(err, wallet.ErrInsufficientBalance):
			if t.Kind() == wt.BET {
				code = wt.BetInsufficientFunds
			} else {
				code = wt.ReversalInsufficientFunds
			}
		case errors.Is(err, money.ErrOverflow):
			code = wt.MonetaryOverflow
		case err != nil:
			return ProcessResult{}, err
		}
	}
	if code != "" {
		if err := t.Reject(code, at); err != nil {
			return ProcessResult{}, err
		}
	} else {
		if t.Kind() != wt.LOSS {
			entryID, err := newID()
			if err != nil {
				return ProcessResult{}, err
			}
			entry, err := ledger.New(entryID, w.ID(), t.ID(), direction, t.Money(), before, w.Balance(), at)
			if err != nil {
				return ProcessResult{}, err
			}
			if err := repos.Wallets.UpdateBalance(ctx, *w, version); err != nil {
				return ProcessResult{}, err
			}
			if err := repos.Ledger.Insert(ctx, entry); err != nil {
				return ProcessResult{}, err
			}
		}
		if err := t.MarkProcessed(at); err != nil {
			return ProcessResult{}, err
		}
	}
	balance := w.Balance()
	if err := repos.Transactions.CompleteOutcome(ctx, t, expected, balance); err != nil {
		return ProcessResult{}, err
	}
	record.Balance = &balance
	return resultOf(record, false), nil
}
