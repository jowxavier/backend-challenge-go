package financial

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/jowxavier/backend-challenge-go/internal/observability"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrInboxConflict = errors.New("inbox identity reused with different body")

type Message struct {
	ConsumerName, MessageID string
	Body                    []byte
	Request                 ProcessRequest
}
type InboxRecord struct {
	ConsumerName, MessageID string
	Hash                    [32]byte
	ReceivedAt              time.Time
	CompletedAt             *time.Time
	TransactionID           string
}
type Inbox interface {
	TryInsert(context.Context, InboxRecord) (bool, error)
	Find(context.Context, string, string) (InboxRecord, error)
	Complete(context.Context, string, string, string, time.Time) error
}
type MessageResult struct {
	TransactionID string
	Duplicate     bool
}

func validMessageID(s string) bool {
	return s != "" && utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
func (p *Processor) ProcessMessage(ctx context.Context, m Message) (result MessageResult, finalErr error) {
	start := time.Now()
	var outcome ProcessResult
	defer func() {
		status := string(outcome.Status)
		if finalErr != nil {
			status = "ERROR"
		}
		if result.Duplicate && finalErr == nil {
			observability.Duplicates.Add(1)
		} else {
			observability.Outcome(status, outcome.IdempotentReplay, time.Since(start))
		}
		observability.Logger.Info("financial message outcome", "messageId", m.MessageID, "providerId", m.Request.ProviderID, "walletId", m.Request.WalletID, "transactionId", result.TransactionID, "correlationId", result.TransactionID, "status", status, "failed", finalErr != nil)
	}()
	if !validMessageID(m.ConsumerName) || !validMessageID(m.MessageID) || len(m.Body) == 0 {
		return MessageResult{}, ErrInvalidInput
	}
	record := InboxRecord{ConsumerName: m.ConsumerName, MessageID: m.MessageID, Hash: sha256.Sum256(m.Body), ReceivedAt: p.processingTime()}
	if record.ReceivedAt.IsZero() {
		return MessageResult{}, ErrInvalidInput
	}
	err := p.transactions.WithinFinancialTransaction(ctx, func(r Repositories) error {
		inserted, err := r.Inbox.TryInsert(ctx, record)
		if err != nil {
			return err
		}
		if !inserted {
			existing, err := r.Inbox.Find(ctx, m.ConsumerName, m.MessageID)
			if err != nil {
				return err
			}
			if existing.Hash != record.Hash {
				return ErrInboxConflict
			}
			if existing.CompletedAt == nil || existing.TransactionID == "" {
				return ErrInvalidPersistedData
			}
			result = MessageResult{existing.TransactionID, true}
			return nil
		}
		tx, hash, err := p.prepare(m.Request)
		if err != nil {
			return err
		}
		outcome, err = p.process(ctx, r, m.Request, tx, hash)
		if err != nil {
			return err
		}
		if err = r.Inbox.Complete(ctx, m.ConsumerName, m.MessageID, outcome.TransactionID, p.processingTime()); err != nil {
			return err
		}
		result = MessageResult{outcome.TransactionID, false}
		return nil
	})
	if err != nil {
		return MessageResult{}, err
	}
	return result, nil
}
