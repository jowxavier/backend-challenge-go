package consumer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
)

const body = `{"messageId":"msg","type":"WagerTransactionRequested","occurredAt":"2026-01-01T00:00:00Z","data":{"providerId":"p","externalTransactionId":"e","idempotencyKey":"key","playerId":"u","walletId":"w","roundId":"r","gameId":"g","kind":"BET","money":{"amount":"25.00","currency":"brl"},"referenceExternalTransactionId":null}}`

func TestDecode(t *testing.T) {
	m, err := Decode(body, "consumer")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := m.Request.Money.Amount()
	c, _ := m.Request.Money.Currency()
	if m.MessageID != "msg" || m.Request.IdempotencyKey != "key" || a != "25.00" || c != "BRL" || string(m.Body) != body {
		t.Fatal(m)
	}
	for _, bad := range []string{"{", body + "{}", strings.Replace(body, "WagerTransactionRequested", "other", 1), strings.Replace(body, `"msg"`, `""`, 1), strings.Replace(body, `"25.00"`, `25.00`, 1), strings.Replace(body, "25.00", "-1", 1), strings.Replace(body, "2026-01-01T00:00:00Z", "bad", 1), strings.Replace(body, "25.00", "1e3", 1)} {
		if _, err := Decode(bad, "consumer"); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	for _, ref := range []string{`"bet"`, `null`} {
		m, err = Decode(strings.Replace(body, `"referenceExternalTransactionId":null`, `"referenceExternalTransactionId":`+ref, 1), "consumer")
		if err != nil {
			t.Fatal(err)
		}
	}
}

type fakeQueue struct {
	deleted, changed int
	delay            time.Duration
	changeErr        error
}

func (q *fakeQueue) Receive(context.Context) (*Delivery, error) { return nil, nil }
func (q *fakeQueue) Delete(context.Context, string) error       { q.deleted++; return nil }
func (q *fakeQueue) ChangeVisibility(_ context.Context, _ string, d time.Duration) error {
	q.changed++
	q.delay = d
	return q.changeErr
}

type processorFunc func(context.Context, financial.Message) (financial.MessageResult, error)

func (f processorFunc) ProcessMessage(ctx context.Context, m financial.Message) (financial.MessageResult, error) {
	return f(ctx, m)
}
func TestAcknowledgement(t *testing.T) {
	sentinel := errors.New("commit failed")
	for _, name := range []string{"success", "duplicate", "failure", "malformed", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{}
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := processorFunc(func(ctx context.Context, m financial.Message) (financial.MessageResult, error) {
				calls++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("no deadline")
				}
				if name == "failure" {
					return financial.MessageResult{}, sentinel
				}
				if name == "cancelled" {
					cancel()
					return financial.MessageResult{}, context.Canceled
				}
				return financial.MessageResult{TransactionID: "tx", Duplicate: name == "duplicate"}, nil
			})
			s, err := New(q, p, "consumer", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			d := Delivery{Body: body, Receipt: "receipt", ReceiveCount: 3}
			if name == "malformed" {
				d.Body = "bad"
			}
			err = s.Handle(ctx, d)
			if name == "success" || name == "duplicate" {
				if err != nil || q.deleted != 1 || q.changed != 0 {
					t.Fatal(err, q)
				}
			} else {
				if err == nil || q.deleted != 0 || q.changed != 1 || q.delay != 4*time.Second {
					t.Fatal(err, q)
				}
			}
			if name == "malformed" && calls != 0 {
				t.Fatal("invalid body called processor")
			}
		})
	}
	for _, tc := range []struct {
		n    int
		want time.Duration
	}{{1, time.Second}, {2, 2 * time.Second}, {6, 32 * time.Second}, {7, 60 * time.Second}, {100000, 60 * time.Second}} {
		if Backoff(tc.n) != tc.want {
			t.Fatal(tc)
		}
	}
}

type blockingQueue struct {
	fakeQueue
	deliveries chan *Delivery
	receives   chan struct{}
}

func (q *blockingQueue) Receive(ctx context.Context) (*Delivery, error) {
	select {
	case q.receives <- struct{}{}:
	default:
	}
	select {
	case m := <-q.deliveries:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestConsumerShutdown(t *testing.T) {
	for _, abort := range []bool{false, true} {
		t.Run(fmt.Sprint(abort), func(t *testing.T) {
			q := &blockingQueue{deliveries: make(chan *Delivery, 1), receives: make(chan struct{}, 2)}
			q.deliveries <- &Delivery{Body: body, Receipt: "r", ReceiveCount: 1}
			entered, release := make(chan struct{}), make(chan struct{})
			p := processorFunc(func(ctx context.Context, m financial.Message) (financial.MessageResult, error) {
				close(entered)
				select {
				case <-release:
					return financial.MessageResult{TransactionID: "t"}, nil
				case <-ctx.Done():
					return financial.MessageResult{}, ctx.Err()
				}
			})
			s, err := New(q, p, "consumer", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			receiveCtx, stopReceive := context.WithCancel(context.Background())
			workCtx, stopWork := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); s.Run(receiveCtx, workCtx) }()
			defer func() { stopReceive(); stopWork(); <-done }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("processing did not start")
			}
			stopReceive()
			if abort {
				stopWork()
			} else {
				close(release)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("shutdown did not complete")
			}
			if abort {
				if q.deleted != 0 || q.changed != 1 {
					t.Fatal(q.fakeQueue)
				}
			} else if q.deleted != 1 {
				t.Fatal("active work not drained")
			}
		})
	}
}
