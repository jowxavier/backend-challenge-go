package outbox

import (
	"context"
	"errors"
	"github.com/jowxavier/backend-challenge-go/internal/observability"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/events"
)

var (
	ErrInvalidConfig = errors.New("invalid outbox delivery configuration")
	ErrInvalidClaim  = errors.New("invalid outbox claim")
	ErrClaimLost     = errors.New("outbox claim lost")
)

type Claim struct {
	Event      events.Event
	LeaseToken string
	Attempt    int64
}
type Delivery interface {
	ClaimBatch(context.Context, time.Time, int, time.Duration) ([]Claim, error)
	MarkPublished(context.Context, string, string, time.Time) error
	Reschedule(context.Context, string, string, time.Time, string) error
}
type EventPublisher interface {
	Publish(context.Context, events.Event) error
}
type Config struct{ PublishTimeout, LeaseDuration, InitialBackoff, MaxBackoff time.Duration }

func DefaultConfig() Config {
	return Config{10 * time.Second, 30 * time.Second, time.Second, 5 * time.Minute}
}
func (c Config) valid() bool {
	return c.PublishTimeout > 0 && c.LeaseDuration > c.PublishTimeout && c.InitialBackoff > 0 && c.MaxBackoff >= c.InitialBackoff
}

type Service struct {
	delivery  Delivery
	publisher EventPublisher
	cfg       Config
	now       func() time.Time
}

func NewService(d Delivery, p EventPublisher, c Config, now func() time.Time) (*Service, error) {
	if d == nil || p == nil || now == nil || !c.valid() {
		return nil, ErrInvalidConfig
	}
	return &Service{d, p, c, now}, nil
}
func (s *Service) backoff(attempt int64) time.Duration {
	delay := s.cfg.InitialBackoff
	for i := int64(1); i < attempt && delay < s.cfg.MaxBackoff; i++ {
		if delay > s.cfg.MaxBackoff/2 {
			return s.cfg.MaxBackoff
		}
		delay *= 2
	}
	return delay
}

// RunOnce claims one event per available caller, avoiding leases waiting in a local queue.
func (s *Service) RunOnce(ctx context.Context) (bool, error) {
	now := s.now().UTC()
	if now.IsZero() {
		return false, ErrInvalidConfig
	}
	claims, err := s.delivery.ClaimBatch(ctx, now, 1, s.cfg.LeaseDuration)
	if err != nil {
		return false, err
	}
	if len(claims) == 0 {
		return false, nil
	}
	c := claims[0]
	if len(claims) != 1 || !c.Event.Valid() || c.LeaseToken == "" || c.Attempt < 1 {
		return true, ErrInvalidClaim
	}
	publishCtx, cancel := context.WithTimeout(ctx, s.cfg.PublishTimeout)
	err = s.publisher.Publish(publishCtx, c.Event)
	cancel()
	observability.Logger.Info("outbox publish", "eventId", c.Event.ID(), "transactionId", c.Event.TransactionID(), "failed", err != nil)
	if err != nil {
		observability.Retries.Add(1)
	}
	at := s.now().UTC()
	if at.IsZero() {
		return true, errors.Join(err, ErrInvalidConfig)
	}
	if err == nil {
		return true, s.delivery.MarkPublished(ctx, c.Event.ID(), c.LeaseToken, at)
	}
	// Transport errors may include credentials or payloads; persist only a fixed category.
	diagnostic := "publish_failed"
	if errors.Is(err, context.DeadlineExceeded) {
		diagnostic = "publish_timeout"
	} else if errors.Is(err, context.Canceled) {
		diagnostic = "publish_cancelled"
	}
	retryErr := s.delivery.Reschedule(ctx, c.Event.ID(), c.LeaseToken, at.Add(s.backoff(c.Attempt)), diagnostic)
	return true, errors.Join(err, retryErr)
}
