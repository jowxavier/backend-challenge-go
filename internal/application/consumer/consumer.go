package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jowxavier/backend-challenge-go/internal/observability"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

var ErrInvalidMessage = errors.New("invalid inbound message")

type Delivery struct {
	Body, Receipt string
	ReceiveCount  int
}
type Queue interface {
	Receive(context.Context) (*Delivery, error)
	Delete(context.Context, string) error
	ChangeVisibility(context.Context, string, time.Duration) error
}
type Processor interface {
	ProcessMessage(context.Context, financial.Message) (financial.MessageResult, error)
}
type Service struct {
	queue     Queue
	processor Processor
	name      string
	timeout   time.Duration
}

func New(q Queue, p Processor, name string, timeout time.Duration) (*Service, error) {
	if q == nil || p == nil || !validID(name) || timeout <= 0 {
		return nil, ErrInvalidMessage
	}
	return &Service{q, p, name, timeout}, nil
}
func validID(s string) bool {
	return s != "" && utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
func Decode(body string, consumerName string) (financial.Message, error) {
	var e struct {
		MessageID  string    `json:"messageId"`
		Type       string    `json:"type"`
		OccurredAt time.Time `json:"occurredAt"`
		Data       struct {
			ProviderID            string  `json:"providerId"`
			ExternalTransactionID string  `json:"externalTransactionId"`
			IdempotencyKey        string  `json:"idempotencyKey"`
			PlayerID              string  `json:"playerId"`
			WalletID              string  `json:"walletId"`
			RoundID               string  `json:"roundId"`
			GameID                string  `json:"gameId"`
			Kind                  wt.Kind `json:"kind"`
			Money                 struct {
				Amount   string `json:"amount"`
				Currency string `json:"currency"`
			} `json:"money"`
			Reference *string `json:"referenceExternalTransactionId"`
		} `json:"data"`
	}
	d := json.NewDecoder(strings.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil {
		return financial.Message{}, ErrInvalidMessage
	}
	if err := d.Decode(new(json.RawMessage)); err != io.EOF {
		return financial.Message{}, ErrInvalidMessage
	}
	if e.Type != "WagerTransactionRequested" || !validID(e.MessageID) || e.OccurredAt.IsZero() {
		return financial.Message{}, ErrInvalidMessage
	}
	m, err := money.Parse(e.Data.Money.Amount, e.Data.Money.Currency)
	if err == nil {
		currency, _ := m.Currency()
		if currency != "BRL" {
			err = money.ErrInvalidCurrency
		}
	}
	if err != nil {
		return financial.Message{}, fmt.Errorf("%w: %w", ErrInvalidMessage, err)
	}
	r := financial.ProcessRequest{ProviderID: e.Data.ProviderID, ExternalTransactionID: e.Data.ExternalTransactionID, IdempotencyKey: e.Data.IdempotencyKey, PlayerID: e.Data.PlayerID, WalletID: e.Data.WalletID, RoundID: e.Data.RoundID, GameID: e.Data.GameID, Kind: e.Data.Kind, Money: m}
	if e.Data.Reference != nil {
		r.ReferenceExternalTransactionID = *e.Data.Reference
	}
	return financial.Message{ConsumerName: consumerName, MessageID: e.MessageID, Body: []byte(body), Request: r}, nil
}
func Backoff(count int) time.Duration {
	delay := time.Second
	for i := 1; i < count && delay < 60*time.Second; i++ {
		delay *= 2
	}
	if delay > 60*time.Second {
		return 60 * time.Second
	}
	return delay
}
func (s *Service) Handle(ctx context.Context, d Delivery) (finalErr error) {
	var messageID string
	defer func() {
		observability.Logger.Info("SQS handling", "messageId", messageID, "failed", finalErr != nil)
		if finalErr != nil {
			observability.Retries.Add(1)
		}
	}()
	work, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	m, err := Decode(d.Body, s.name)
	messageID = m.MessageID
	if err == nil {
		_, err = s.processor.ProcessMessage(work, m)
	}
	if err == nil {
		if err = work.Err(); err == nil {
			return s.queue.Delete(work, d.Receipt)
		}
	}
	// Error bodies can contain sensitive data; callers should log categories only.
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	visibilityErr := s.queue.ChangeVisibility(cleanup, d.Receipt, Backoff(d.ReceiveCount))
	return errors.Join(err, visibilityErr)
}
func (s *Service) Run(receiveCtx, workCtx context.Context) {
	for receiveCtx.Err() == nil {
		delivery, err := s.queue.Receive(receiveCtx)
		if err != nil {
			if !pause(receiveCtx, time.Second) {
				return
			}
			continue
		}
		if delivery == nil {
			continue
		}
		if receiveCtx.Err() != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.queue.ChangeVisibility(cleanup, delivery.Receipt, 0)
			cancel()
			return
		}
		_ = s.Handle(workCtx, *delivery)
	}
}
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
