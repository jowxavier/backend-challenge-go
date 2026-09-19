package outbox

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/events"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

type fakeDelivery struct {
	claims                                             []Claim
	claimErr, markErr, retryErr                        error
	markID, markToken, retryID, retryToken, diagnostic string
	next, markedAt                                     time.Time
	now                                                time.Time
	limit                                              int
	lease                                              time.Duration
}

func (d *fakeDelivery) ClaimBatch(_ context.Context, now time.Time, limit int, lease time.Duration) ([]Claim, error) {
	d.now = now
	d.limit = limit
	d.lease = lease
	return d.claims, d.claimErr
}
func (d *fakeDelivery) MarkPublished(_ context.Context, id, token string, at time.Time) error {
	d.markID = id
	d.markToken = token
	d.markedAt = at
	return d.markErr
}
func (d *fakeDelivery) Reschedule(_ context.Context, id, token string, next time.Time, diagnostic string) error {
	d.retryID = id
	d.retryToken = token
	d.next = next
	d.diagnostic = diagnostic
	return d.retryErr
}

type publishFunc func(context.Context, events.Event) error

func (f publishFunc) Publish(ctx context.Context, e events.Event) error { return f(ctx, e) }
func testEvent(t *testing.T) events.Event {
	t.Helper()
	m, _ := money.Parse("1", "BRL")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tx, err := wt.NewExternal(wt.ExternalInput{ID: "t", ProviderID: "p", ExternalTransactionID: "e", PlayerID: "u", WalletID: "w", RoundID: "r", GameID: "g", Kind: wt.WIN, Money: m}, at)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.MarkProcessed(at); err != nil {
		t.Fatal(err)
	}
	e, err := events.NewProcessed(tx, m)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestServiceDelivery(t *testing.T) {
	e := testEvent(t)
	at := e.OccurredAt()
	sentinel := errors.New("secret token and payload must not be persisted")
	dbErr := errors.New("db failure")
	for _, tc := range []struct {
		name                 string
		publish, mark, retry error
		diagnostic           string
	}{
		{"success", nil, nil, nil, ""}, {"failure", sentinel, nil, nil, "publish_failed"}, {"timeout", context.DeadlineExceeded, nil, nil, "publish_timeout"}, {"cancelled", context.Canceled, nil, nil, "publish_cancelled"}, {"mark failure", nil, dbErr, nil, ""}, {"reschedule failure", sentinel, nil, dbErr, "publish_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDelivery{claims: []Claim{{e, "token", 3}}, markErr: tc.mark, retryErr: tc.retry}
			calls := 0
			p := publishFunc(func(ctx context.Context, got events.Event) error {
				calls++
				if got.ID() != e.ID() {
					t.Fatal(got)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("no timeout")
				}
				at = at.Add(time.Second)
				return tc.publish
			})
			s, err := NewService(d, p, DefaultConfig(), func() time.Time { return at })
			if err != nil {
				t.Fatal(err)
			}
			worked, err := s.RunOnce(context.Background())
			if !worked || calls != 1 {
				t.Fatal(worked, calls)
			}
			for _, want := range []error{tc.publish, tc.mark, tc.retry} {
				if want != nil && !errors.Is(err, want) {
					t.Fatal(err, want)
				}
			}
			if tc.publish == nil {
				if d.markID != e.ID() || d.markToken != "token" || !d.markedAt.Equal(at) || d.retryID != "" {
					t.Fatal(d)
				}
			} else {
				if d.retryID != e.ID() || d.retryToken != "token" || d.diagnostic != tc.diagnostic || !d.next.Equal(at.Add(4*time.Second)) || d.markID != "" || strings.Contains(d.diagnostic, "secret") {
					t.Fatal(d)
				}
			}
			if d.limit != 1 || d.lease != 30*time.Second {
				t.Fatal(d)
			}
		})
	}
}
func TestServiceBackoffAndValidation(t *testing.T) {
	d := &fakeDelivery{}
	p := publishFunc(func(context.Context, events.Event) error { t.Fatal("unexpected publish"); return nil })
	now := func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	s, err := NewService(d, p, DefaultConfig(), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		attempt int64
		want    time.Duration
	}{{1, time.Second}, {2, 2 * time.Second}, {9, 256 * time.Second}, {10, 5 * time.Minute}, {math.MaxInt64, 5 * time.Minute}} {
		if got := s.backoff(tc.attempt); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	worked, err := s.RunOnce(context.Background())
	if worked || err != nil {
		t.Fatal(worked, err)
	}
	d.claimErr = errors.New("db")
	if _, err = s.RunOnce(context.Background()); !errors.Is(err, d.claimErr) {
		t.Fatal(err)
	}
	for _, c := range []Config{{}, {time.Second, time.Second, time.Second, time.Minute}, {time.Second, 2 * time.Second, time.Minute, time.Second}} {
		if _, err = NewService(d, p, c, now); !errors.Is(err, ErrInvalidConfig) {
			t.Fatal(err)
		}
	}
	if _, err = NewService(nil, p, DefaultConfig(), now); err == nil {
		t.Fatal("nil delivery")
	}
	if _, err = NewService(d, nil, DefaultConfig(), now); err == nil {
		t.Fatal("nil publisher")
	}
	if _, err = NewService(d, p, DefaultConfig(), nil); err == nil {
		t.Fatal("nil clock")
	}
}
