//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

func inbound(t *testing.T, id string, kind wt.Kind, n int64) financial.Message {
	return financial.Message{ConsumerName: "wager-financial-v1", MessageID: id, Body: []byte("original-body-" + id), Request: request(t, "op", "w", kind, n)}
}
func TestInboxOutcomesAndReplay(t *testing.T) {
	for _, kind := range []string{"processed", "rejected", "pending", "financial replay"} {
		t.Run(kind, func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
			p := newProcessor(t, NewRunner(pool))
			m := inbound(t, "msg", wt.BET, 100)
			if kind == "rejected" {
				m.Request = request(t, "op", "w", wt.BET, 20000)
			}
			if kind == "pending" {
				m.Request = referenceRequest(t, "op", wt.REFUND, 100, "absent")
			}
			if kind == "financial replay" {
				processOK(t, p, m.Request)
			}
			first, err := p.ProcessMessage(ctx, m)
			must(t, err)
			if first.Duplicate || first.TransactionID == "" {
				t.Fatal(first)
			}
			repo := &InboxRepository{db: pool}
			stored, err := repo.Find(ctx, m.ConsumerName, m.MessageID)
			must(t, err)
			if stored.CompletedAt == nil || stored.TransactionID != first.TransactionID {
				t.Fatal(stored)
			}
			before, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			ledgerCount, eventsCount := count(t, pool, "wallet_ledger_entries"), count(t, pool, "outbox_events")
			restarted := newProcessor(t, NewRunner(pool))
			duplicate, err := restarted.ProcessMessage(ctx, m)
			must(t, err)
			if !duplicate.Duplicate || duplicate.TransactionID != first.TransactionID {
				t.Fatal(duplicate)
			}
			m.Body = append(m.Body, ' ')
			if _, err = restarted.ProcessMessage(ctx, m); !errors.Is(err, financial.ErrInboxConflict) {
				t.Fatal(err)
			}
			m.MessageID = "another-message"
			m.Request.IdempotencyKey = "alias"
			_, err = restarted.ProcessMessage(ctx, m)
			must(t, err)
			after, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			if before != after || count(t, pool, "wallet_ledger_entries") != ledgerCount || count(t, pool, "outbox_events") != eventsCount {
				t.Fatal("duplicate effects")
			}
			if count(t, pool, "inbox_messages") != 2 {
				t.Fatal("missing completed replay")
			}
			var incomplete int
			must(t, pool.QueryRow(ctx, `SELECT count(*) FROM inbox_messages WHERE completed_at IS NULL`).Scan(&incomplete))
			if incomplete != 0 {
				t.Fatal(incomplete)
			}
		})
	}
}

type inboxFault struct{ financial.Inbox }

func (f inboxFault) Complete(context.Context, string, string, string, time.Time) error {
	return injected
}

type inboxFaultRunner struct{ runner *Runner }

func (f inboxFaultRunner) WithinFinancialTransaction(ctx context.Context, work func(financial.Repositories) error) error {
	return f.runner.WithinFinancialTransaction(ctx, func(r financial.Repositories) error { r.Inbox = inboxFault{r.Inbox}; return work(r) })
}
func TestInboxCompleteFailureRollsBack(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	w := testWallet(t, "w", 10000)
	must(t, NewWalletRepository(pool).Insert(ctx, w))
	_, err := newProcessor(t, inboxFaultRunner{NewRunner(pool)}).ProcessMessage(ctx, inbound(t, "msg", wt.BET, 100))
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	after, err := NewWalletRepository(pool).GetByID(ctx, "w")
	must(t, err)
	if after != w {
		t.Fatal("balance committed")
	}
	for _, table := range []string{"inbox_messages", "wager_transactions", "wallet_ledger_entries", "outbox_events", "financial_idempotency_keys"} {
		if count(t, pool, table) != 0 {
			t.Fatal(table)
		}
	}
}

type inboxGate struct {
	financial.Inbox
	inserted, release chan struct{}
}

func (g inboxGate) TryInsert(ctx context.Context, r financial.InboxRecord) (bool, error) {
	inserted, err := g.Inbox.TryInsert(ctx, r)
	if err != nil {
		return false, err
	}
	close(g.inserted)
	select {
	case <-g.release:
		return inserted, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

type inboxGateRunner struct {
	runner            *Runner
	started           chan int
	inserted, release chan struct{}
}

func (g inboxGateRunner) WithinFinancialTransaction(ctx context.Context, work func(financial.Repositories) error) error {
	return g.runner.WithinFinancialTransaction(ctx, func(r financial.Repositories) error {
		g.started <- int(r.Wallets.(*WalletRepository).tx.Conn().PgConn().PID())
		if g.inserted != nil {
			r.Inbox = inboxGate{r.Inbox, g.inserted, g.release}
		}
		return work(r)
	})
}
func TestInboxConcurrentConsumers(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_%t", same), func(t *testing.T) {
			pool := testPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
			must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "other", 10000)))
			a := inboxGateRunner{NewRunner(pool), make(chan int, 1), make(chan struct{}), make(chan struct{})}
			b := inboxGateRunner{runner: NewRunner(pool), started: make(chan int, 1)}
			p, q := newProcessor(t, a), newProcessor(t, b)
			m := inbound(t, "msg", wt.BET, 100)
			n := m
			if !same {
				n.MessageID = "other"
				n.Body = []byte("other")
				n.Request = request(t, "other", "other", wt.BET, 100)
			}
			da, db := make(chan struct{}), make(chan struct{})
			ea, eb := make(chan error, 1), make(chan error, 1)
			go func() { defer close(da); _, err := p.ProcessMessage(ctx, m); ea <- err }()
			defer func() { cancel(); <-da }()
			pidA := awaitPID(t, ctx, a.started)
			awaitSignal(t, ctx, a.inserted)
			go func() { defer close(db); _, err := q.ProcessMessage(ctx, n); eb <- err }()
			defer func() { cancel(); <-db }()
			pidB := awaitPID(t, ctx, b.started)
			if same {
				awaitBlocking(t, ctx, pool, pidA, pidB)
			} else {
				awaitSignal(t, ctx, db)
				must(t, <-eb)
			}
			close(a.release)
			awaitSignal(t, ctx, da)
			must(t, <-ea)
			if same {
				awaitSignal(t, ctx, db)
				must(t, <-eb)
			}
			want := 1
			if !same {
				want = 2
			}
			if count(t, pool, "inbox_messages") != want || count(t, pool, "wallet_ledger_entries") != want || count(t, pool, "outbox_events") != 2*want {
				t.Fatal("wrong effect count")
			}
		})
	}
}
func TestInboxMigrationAndGuards(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	up, err := os.ReadFile("../../../migrations/000006_inbox.up.sql")
	must(t, err)
	down, err := os.ReadFile("../../../migrations/000006_inbox.down.sql")
	must(t, err)
	_, err = pool.Exec(ctx, string(down))
	must(t, err)
	_, err = pool.Exec(ctx, string(up))
	must(t, err)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 1000)))
	p := newProcessor(t, NewRunner(pool))
	m := inbound(t, "msg", wt.BET, 100)
	result, err := p.ProcessMessage(ctx, m)
	must(t, err)
	repo := &InboxRepository{db: pool}
	if err = repo.Complete(ctx, m.ConsumerName, m.MessageID, result.TransactionID, testTime); !errors.Is(err, ErrStaleOutcomeWrite) {
		t.Fatal(err)
	}
	for _, sql := range []string{`UPDATE inbox_messages SET message_id='changed'`, `UPDATE inbox_messages SET consumer_name='changed'`, `UPDATE inbox_messages SET payload_hash=decode(repeat('ff',32),'hex')`, `UPDATE inbox_messages SET received_at=received_at+interval '1 second'`, `UPDATE inbox_messages SET completed_at=NULL,transaction_id=NULL`, `DELETE FROM inbox_messages`, `TRUNCATE inbox_messages`} {
		if _, err = pool.Exec(ctx, sql); err == nil {
			t.Fatal(sql)
		}
	}
	conn, err := pool.Acquire(ctx)
	must(t, err)
	defer conn.Release()
	_, err = conn.Exec(ctx, string(down))
	if err == nil {
		t.Fatal("destructive down")
	}
	_, err = conn.Exec(ctx, "ROLLBACK")
	must(t, err)
	rec := financial.InboxRecord{ConsumerName: m.ConsumerName, MessageID: "incomplete", ReceivedAt: testTime}
	_, err = repo.TryInsert(ctx, rec)
	must(t, err)
	m.MessageID = "incomplete"
	m.Body = nil // invalid identity/body is rejected before claiming.
	if _, err = p.ProcessMessage(ctx, m); !errors.Is(err, financial.ErrInvalidInput) {
		t.Fatal(err)
	}
	// Store the matching hash to exercise the confirmed-incomplete integrity path.
	rec.MessageID = "incomplete-matching"
	rec.Hash = [32]byte{}
	m.Body = []byte("body")
	// The immutable hash is set on insertion, not repaired afterward.
	rec.Hash = sha256.Sum256(m.Body)
	_, err = repo.TryInsert(ctx, rec)
	must(t, err)
	m.MessageID = rec.MessageID
	if _, err = p.ProcessMessage(ctx, m); !errors.Is(err, financial.ErrInvalidPersistedData) {
		t.Fatal(err)
	}
}

type ambiguousInboxRunner struct{ runner *Runner }

func (r ambiguousInboxRunner) WithinFinancialTransaction(ctx context.Context, work func(financial.Repositories) error) error {
	if err := r.runner.WithinFinancialTransaction(ctx, work); err != nil {
		return err
	}
	return injected
}
func TestInboxUnknownCommit(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	m := inbound(t, "msg", wt.BET, 100)
	got, err := newProcessor(t, ambiguousInboxRunner{NewRunner(pool)}).ProcessMessage(ctx, m)
	if !errors.Is(err, injected) || got != (financial.MessageResult{}) {
		t.Fatal(got, err)
	}
	got, err = newProcessor(t, NewRunner(pool)).ProcessMessage(ctx, m)
	must(t, err)
	if !got.Duplicate || count(t, pool, "inbox_messages") != 1 || count(t, pool, "wallet_ledger_entries") != 1 {
		t.Fatal("unsafe ambiguous commit recovery")
	}
}
