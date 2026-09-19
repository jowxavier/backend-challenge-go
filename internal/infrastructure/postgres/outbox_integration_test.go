//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/application/events"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/application/outbox"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

func outboxTypes(t *testing.T, pool *pgxpool.Pool, id string) []string {
	t.Helper()
	rows, err := pool.Query(testContext(t), `SELECT event_type FROM outbox_events WHERE transaction_id=$1 ORDER BY event_type`, id)
	must(t, err)
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var kind string
		must(t, rows.Scan(&kind))
		result = append(result, kind)
	}
	must(t, rows.Err())
	return result
}
func assertEvents(t *testing.T, pool *pgxpool.Pool, id string, want ...events.Type) {
	t.Helper()
	expected := []string{}
	for _, v := range want {
		expected = append(expected, string(v))
	}
	sort.Strings(expected)
	if got := outboxTypes(t, pool, id); !reflect.DeepEqual(got, expected) {
		t.Fatalf("events %v want %v", got, expected)
	}
}
func TestOutboxEmissionMatrix(t *testing.T) {
	for _, kind := range []wt.Kind{wt.BET, wt.WIN, wt.LOSS, wt.REFUND, wt.ROLLBACK} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rejected_%t", kind, reject), func(t *testing.T) {
				pool := testPool(t)
				ctx := testContext(t)
				must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
				p := newProcessor(t, NewRunner(pool))
				req := request(t, "op", "w", kind, 100)
				if kind == wt.LOSS {
					req.Money = testMoney(t, 0)
				}
				if kind == wt.REFUND || kind == wt.ROLLBACK {
					processOK(t, p, request(t, "bet", "w", wt.BET, 100))
					req.ReferenceExternalTransactionID = "bet"
				}
				if reject {
					n, _ := req.Money.MinorUnits()
					req.Money, _ = money.FromMinorUnits(n, "USD")
				}
				got := processOK(t, p, req)
				if reject {
					assertEvents(t, pool, got.TransactionID, events.Rejected)
				} else if kind == wt.LOSS {
					assertEvents(t, pool, got.TransactionID, events.Processed)
				} else {
					assertEvents(t, pool, got.TransactionID, events.Processed, events.BalanceChanged)
				}
				var raw []byte
				must(t, pool.QueryRow(ctx, `SELECT payload FROM outbox_events WHERE transaction_id=$1 AND event_type=$2`, got.TransactionID, map[bool]string{true: string(events.Rejected), false: string(events.Processed)}[reject]).Scan(&raw))
				var body struct {
					CorrelationID string `json:"correlationId"`
					Data          struct {
						Balance struct{ Amount, Currency string } `json:"resultingBalance"`
					} `json:"data"`
				}
				must(t, json.Unmarshal(raw, &body))
				amount, _ := got.Balance.Amount()
				if body.CorrelationID != got.TransactionID || body.Data.Balance.Amount != amount || body.Data.Balance.Currency != "BRL" {
					t.Fatal(string(raw))
				}
				before := count(t, pool, "outbox_events")
				processOK(t, p, request(t, "later", "w", wt.WIN, 500))
				before += 2
				processOK(t, p, req)
				req.IdempotencyKey = "alias"
				processOK(t, p, req)
				if count(t, pool, "outbox_events") != before {
					t.Fatal("replay emitted")
				}
				var after []byte
				must(t, pool.QueryRow(ctx, `SELECT payload FROM outbox_events WHERE transaction_id=$1 AND event_type=$2`, got.TransactionID, map[bool]string{true: string(events.Rejected), false: string(events.Processed)}[reject]).Scan(&after))
				if string(after) != string(raw) {
					t.Fatal("historical snapshot changed")
				}
			})
		}
	}
}
func TestOutboxPendingResolution(t *testing.T) {
	for _, kind := range []wt.Kind{wt.WIN, wt.REFUND, wt.ROLLBACK} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/expire_%t", kind, reject), func(t *testing.T) {
				pool := testPool(t)
				ctx := testContext(t)
				must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
				now := testTime
				p, err := financial.NewProcessor(NewRunner(pool), time.Hour, func() time.Time { return now })
				must(t, err)
				req := referenceRequest(t, "dependent", kind, 100, "bet")
				pending := processOK(t, p, req)
				assertEvents(t, pool, pending.TransactionID, events.PendingReference)
				req.IdempotencyKey = "alias"
				processOK(t, p, req)
				_, err = p.ResolvePendingReference(ctx, "provider", pending.TransactionID)
				must(t, err)
				assertEvents(t, pool, pending.TransactionID, events.PendingReference)
				processOK(t, p, request(t, "bet", "w", wt.BET, 100))
				if reject {
					now = now.Add(time.Hour)
				}
				_, err = p.ResolvePendingReference(ctx, "provider", pending.TransactionID)
				must(t, err)
				if reject {
					assertEvents(t, pool, pending.TransactionID, events.PendingReference, events.Rejected)
				} else {
					assertEvents(t, pool, pending.TransactionID, events.PendingReference, events.Processed, events.BalanceChanged)
				}
				total := count(t, pool, "outbox_events")
				processOK(t, p, req)
				_, err = p.ResolvePendingReference(ctx, "provider", pending.TransactionID)
				must(t, err)
				if count(t, pool, "outbox_events") != total {
					t.Fatal("terminal replay emitted")
				}
			})
		}
	}
}

type outboxFaultRunner struct {
	runner *Runner
	mode   string
}

func (f outboxFaultRunner) WithinFinancialTransaction(ctx context.Context, work func(financial.Repositories) error) error {
	return f.runner.WithinFinancialTransaction(ctx, func(r financial.Repositories) error {
		r.Outbox = &outboxFaultWriter{writer: r.Outbox, mode: f.mode}
		return work(r)
	})
}

type outboxFaultWriter struct {
	writer financial.OutboxWriter
	mode   string
	calls  int
}

func (f *outboxFaultWriter) Insert(ctx context.Context, e events.Event) error {
	f.calls++
	if f.mode == "before" {
		return injected
	}
	if err := f.writer.Insert(ctx, e); err != nil {
		return err
	}
	if f.mode == "after first" || (f.mode == "after second" && f.calls == 2) {
		return injected
	}
	return nil
}
func TestOutboxFinancialRollback(t *testing.T) {
	for _, mode := range []string{"before", "after first", "after second", "database"} {
		t.Run(mode, func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			w := testWallet(t, "w", 10000)
			must(t, NewWalletRepository(pool).Insert(ctx, w))
			runner := financial.Transactor(outboxFaultRunner{NewRunner(pool), mode})
			if mode == "database" {
				_, err := pool.Exec(ctx, `CREATE FUNCTION fail_outbox_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER fail_outbox BEFORE INSERT ON outbox_events FOR EACH STATEMENT EXECUTE FUNCTION fail_outbox_insert()`)
				must(t, err)
				runner = NewRunner(pool)
			}
			p := newProcessor(t, runner)
			got, err := p.Process(ctx, request(t, "op", "w", wt.BET, 100))
			if err == nil || got != (financial.ProcessResult{}) {
				t.Fatal(got, err)
			}
			after, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			if after != w {
				t.Fatal("wallet survived rollback")
			}
			for _, table := range []string{"wager_transactions", "wallet_ledger_entries", "financial_idempotency_keys", "outbox_events"} {
				if count(t, pool, table) != 0 {
					t.Fatal(table)
				}
			}
		})
	}
}
func TestOutboxResolutionRollback(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	p := newProcessor(t, NewRunner(pool))
	pending := processOK(t, p, referenceRequest(t, "refund", wt.REFUND, 100, "bet"))
	processOK(t, p, request(t, "bet", "w", wt.BET, 100))
	before, err := NewWalletRepository(pool).GetByID(ctx, "w")
	must(t, err)
	total := count(t, pool, "outbox_events")
	broken := newProcessor(t, outboxFaultRunner{NewRunner(pool), "after second"})
	_, err = broken.ResolvePendingReference(ctx, "provider", pending.TransactionID)
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	after, err := NewWalletRepository(pool).GetByID(ctx, "w")
	must(t, err)
	if after != before || count(t, pool, "outbox_events") != total || count(t, pool, "wallet_ledger_entries") != 1 {
		t.Fatal("partial resolution")
	}
	assertEvents(t, pool, pending.TransactionID, events.PendingReference)
	r, err := NewWagerTransactionRepository(pool).FindByProviderID(ctx, "provider", pending.TransactionID)
	must(t, err)
	if r.Transaction.Status() != wt.PENDING_REFERENCE {
		t.Fatal(r)
	}
	_, err = p.ResolvePendingReference(ctx, "provider", pending.TransactionID)
	must(t, err)
	assertEvents(t, pool, pending.TransactionID, events.PendingReference, events.Processed, events.BalanceChanged)
}
func createOutboxFixture(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	must(t, NewWalletRepository(pool).Insert(testContext(t), testWallet(t, "w", 10000)))
	return processOK(t, newProcessor(t, NewRunner(pool)), request(t, "loss", "w", wt.LOSS, 0)).TransactionID
}
func TestOutboxSchema(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	id := createOutboxFixture(t, pool)
	for _, sql := range []string{
		`UPDATE outbox_events SET id=id||'x'`, `UPDATE outbox_events SET transaction_id='other'`, `UPDATE outbox_events SET aggregate_id='other'`, `UPDATE outbox_events SET event_type='WagerTransactionRejected'`, `UPDATE outbox_events SET event_version=2`, `UPDATE outbox_events SET payload=payload||'{"extra":true}'::jsonb`, `UPDATE outbox_events SET occurred_at=occurred_at+interval '1 second'`, `DELETE FROM outbox_events`, `TRUNCATE outbox_events`,
		`UPDATE outbox_events SET attempt_count=-1`, `UPDATE outbox_events SET lease_token='token'`,
		`UPDATE outbox_events SET published_at=now(),lease_token='token',lease_until=now()`,
	} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Fatal("accepted", sql)
		}
	}
	_, err := pool.Exec(ctx, `INSERT INTO outbox_events SELECT id||'x',transaction_id,aggregate_id,event_type,event_version,jsonb_set(payload,'{eventId}',to_jsonb(id||'x')),occurred_at,NULL,0,next_attempt_at,NULL,NULL,NULL FROM outbox_events`)
	if err == nil {
		t.Fatal("duplicate logical identity")
	}
	for _, field := range []string{"eventId", "aggregateId", "eventType", "version", "correlationId", "causationId", "occurredAt", "data"} {
		_, err = pool.Exec(ctx, `INSERT INTO outbox_events(id,transaction_id,aggregate_id,event_type,event_version,payload,occurred_at,next_attempt_at) SELECT id||'x',transaction_id,aggregate_id,'WagerTransactionRejected',event_version,(jsonb_set(jsonb_set(payload,'{eventId}',to_jsonb(id||'x')),'{eventType}','"WagerTransactionRejected"'::jsonb) - $1::text),occurred_at,next_attempt_at FROM outbox_events`, field)
		if err == nil {
			t.Fatal("accepted missing field", field)
		}
	}
	delivery := NewOutboxDelivery(pool)
	claims, err := delivery.ClaimBatch(ctx, time.Now().Add(time.Hour), 1, time.Minute)
	must(t, err)
	if len(claims) != 1 || claims[0].Event.TransactionID() != id {
		t.Fatal(claims)
	}
	c := claims[0]
	must(t, delivery.Reschedule(ctx, c.Event.ID(), c.LeaseToken, time.Now(), "secret"))
	var diagnostic string
	must(t, pool.QueryRow(ctx, `SELECT last_error FROM outbox_events`).Scan(&diagnostic))
	if diagnostic != "publish_failed" {
		t.Fatal(diagnostic)
	}
}
func TestOutboxMigrationRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	up, err := os.ReadFile("../../../migrations/000005_outbox.up.sql")
	must(t, err)
	down, err := os.ReadFile("../../../migrations/000005_outbox.down.sql")
	must(t, err)
	_, err = pool.Exec(ctx, string(down))
	must(t, err)
	_, err = pool.Exec(ctx, string(up))
	must(t, err)
	createOutboxFixture(t, pool)
	conn, err := pool.Acquire(ctx)
	must(t, err)
	defer conn.Release()
	_, err = conn.Exec(ctx, string(down))
	if err == nil {
		t.Fatal("destructive downgrade")
	}
	_, err = conn.Exec(ctx, "ROLLBACK")
	must(t, err)
	if count(t, pool, "outbox_events") != 1 {
		t.Fatal("lost event")
	}
}
func TestOutboxClaimsAndFencing(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	createOutboxFixture(t, pool)
	now := time.Now().Add(time.Hour)
	d := NewOutboxDelivery(pool)
	claims, err := d.ClaimBatch(ctx, now, 1, time.Minute)
	must(t, err)
	if len(claims) != 1 {
		t.Fatal(claims)
	}
	old := claims[0]
	none, err := NewOutboxDelivery(pool).ClaimBatch(ctx, now, 1, time.Minute)
	must(t, err)
	if len(none) != 0 {
		t.Fatal("duplicate active claim")
	}
	claims, err = NewOutboxDelivery(pool).ClaimBatch(ctx, now.Add(time.Minute), 1, time.Minute)
	must(t, err)
	if len(claims) != 1 {
		t.Fatal(claims)
	}
	fresh := claims[0]
	if fresh.Event.ID() != old.Event.ID() || fresh.LeaseToken == old.LeaseToken || fresh.Attempt != 2 || string(fresh.Event.JSON()) != string(old.Event.JSON()) {
		t.Fatal("reclaim identity")
	}
	if err = d.MarkPublished(ctx, old.Event.ID(), old.LeaseToken, now); !errors.Is(err, outbox.ErrClaimLost) {
		t.Fatal(err)
	}
	if err = d.Reschedule(ctx, old.Event.ID(), old.LeaseToken, now, "publish_failed"); !errors.Is(err, outbox.ErrClaimLost) {
		t.Fatal(err)
	}
	must(t, d.MarkPublished(ctx, fresh.Event.ID(), fresh.LeaseToken, now.Add(time.Minute)))
	none, err = d.ClaimBatch(ctx, now.Add(time.Hour), 1, time.Minute)
	must(t, err)
	if len(none) != 0 {
		t.Fatal("published reclaimed")
	}
}
func TestOutboxConcurrentClaims(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	createOutboxFixture(t, pool)
	processOK(t, newProcessor(t, NewRunner(pool)), request(t, "second", "w", wt.LOSS, 0))
	now := time.Now().Add(time.Hour)
	// Hold one eligible row to prove SKIP LOCKED lets another connection progress.
	tx, err := pool.Begin(ctx)
	must(t, err)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	var locked string
	must(t, tx.QueryRow(ctx, `SELECT id FROM outbox_events ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE`).Scan(&locked))
	claims, err := NewOutboxDelivery(pool).ClaimBatch(ctx, now, 1, time.Second)
	must(t, err)
	if len(claims) != 1 || claims[0].Event.ID() == locked {
		t.Fatal("did not skip locked row")
	}
	must(t, tx.Rollback(ctx))
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	done := make(chan struct{}, 2)
	type answer struct {
		claims []outbox.Claim
		err    error
	}
	results := make(chan answer, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			ready <- struct{}{}
			select {
			case <-start:
			case <-ctx.Done():
				return
			}
			got, err := NewOutboxDelivery(pool).ClaimBatch(ctx, now.Add(time.Minute), 1, time.Minute)
			results <- answer{got, err}
		}()
	}
	defer func() { cancel(); <-done; <-done }()
	for i := 0; i < 2; i++ {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(start)
	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case a := <-results:
			must(t, a.err)
			if len(a.claims) != 1 {
				t.Fatal(a.claims)
			}
			id := a.claims[0].Event.ID()
			if ids[id] {
				t.Fatal("same event claimed twice")
			}
			ids[id] = true
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

type outboxPublishFunc func(context.Context, events.Event) error

func (f outboxPublishFunc) Publish(ctx context.Context, e events.Event) error { return f(ctx, e) }

type lostMark struct{ outbox.Delivery }

func (l lostMark) MarkPublished(context.Context, string, string, time.Time) error { return injected }
func TestOutboxPublisherRecovery(t *testing.T) {
	for _, mode := range []string{"success", "failure", "crash after publish"} {
		t.Run(mode, func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			createOutboxFixture(t, pool)
			now := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
			delivery := NewOutboxDelivery(pool)
			var ids []string
			publisher := outboxPublishFunc(func(ctx context.Context, e events.Event) error {
				// A separate connection sees the committed claim while the network call is in progress.
				var token *string
				must(t, pool.QueryRow(ctx, `SELECT lease_token FROM outbox_events WHERE id=$1`, e.ID()).Scan(&token))
				if token == nil {
					t.Fatal("uncommitted claim")
				}
				ids = append(ids, e.ID())
				if mode == "failure" && len(ids) == 1 {
					return injected
				}
				return nil
			})
			d := outbox.Delivery(delivery)
			if mode == "crash after publish" {
				d = lostMark{delivery}
			}
			s, err := outbox.NewService(d, publisher, outbox.DefaultConfig(), func() time.Time { return now })
			must(t, err)
			worked, err := s.RunOnce(ctx)
			if !worked {
				t.Fatal("not claimed")
			}
			if mode == "success" {
				must(t, err)
			} else if !errors.Is(err, injected) {
				t.Fatal(err)
			}
			if mode == "failure" {
				var at time.Time
				var diagnostic string
				must(t, pool.QueryRow(ctx, `SELECT next_attempt_at,last_error FROM outbox_events`).Scan(&at, &diagnostic))
				if !at.Equal(now.Add(time.Second)) || diagnostic != "publish_failed" {
					t.Fatal(at, diagnostic)
				}
			}
			// New service/repository instance models restart; no local claim state survives.
			s, err = outbox.NewService(NewOutboxDelivery(pool), publisher, outbox.DefaultConfig(), func() time.Time { return now })
			must(t, err)
			worked, err = s.RunOnce(ctx)
			must(t, err)
			if worked {
				t.Fatal("claim retried before due/lease")
			}
			now = now.Add(time.Minute)
			worked, err = s.RunOnce(ctx)
			must(t, err)
			if mode == "success" {
				if worked || len(ids) != 1 {
					t.Fatal(worked, ids)
				}
			} else {
				if !worked || len(ids) != 2 || ids[0] != ids[1] {
					t.Fatal("unstable duplicate identity", ids)
				}
			}
			var published *time.Time
			var lease *string
			must(t, pool.QueryRow(ctx, `SELECT published_at,lease_token FROM outbox_events`).Scan(&published, &lease))
			if published == nil || lease != nil {
				t.Fatal("not completed")
			}
		})
	}
}

type outboxGateRunner struct {
	runner   *Runner
	inserted chan struct{}
	release  chan struct{}
}

func (g outboxGateRunner) WithinFinancialTransaction(ctx context.Context, work func(financial.Repositories) error) error {
	return g.runner.WithinFinancialTransaction(ctx, func(r financial.Repositories) error {
		if err := work(r); err != nil {
			return err
		}
		close(g.inserted)
		select {
		case <-g.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}
func TestOutboxUncommittedInvisible(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	gate := outboxGateRunner{NewRunner(pool), make(chan struct{}), make(chan struct{})}
	p := newProcessor(t, gate)
	joined := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer close(joined)
		_, err := p.Process(ctx, request(t, "bet", "w", wt.BET, 100))
		result <- err
	}()
	defer func() { cancel(); <-joined }()
	select {
	case <-gate.inserted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if count(t, pool, "outbox_events") != 0 {
		t.Fatal("uncommitted event visible")
	}
	claims, err := NewOutboxDelivery(pool).ClaimBatch(ctx, time.Now().Add(time.Hour), 10, time.Minute)
	must(t, err)
	if len(claims) != 0 {
		t.Fatal("uncommitted event claimed")
	}
	close(gate.release)
	select {
	case err := <-result:
		must(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if count(t, pool, "outbox_events") != 2 {
		t.Fatal("events missing after commit")
	}
}
func TestOutboxNoOrphanOrFailedEvent(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 100)))
	tx := testWager(t, "t", "w")
	must(t, tx.MarkProcessed(testTime))
	event, err := events.NewProcessed(tx, testMoney(t, 99))
	must(t, err)
	if err = (&OutboxWriter{db: pool}).Insert(ctx, event); err == nil {
		t.Fatal("orphan event")
	}
	failed := testWager(t, "failed", "w")
	repo := NewWagerTransactionRepository(pool)
	must(t, repo.Insert(ctx, failed))
	must(t, failed.Fail(wt.PermanentInfrastructureFailure, testTime))
	must(t, repo.UpdateOutcome(ctx, failed, wt.PENDING))
	assertEvents(t, pool, failed.ID())
}
func TestOutboxConcurrentResolution(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	p := newProcessor(t, NewRunner(pool))
	pending := processOK(t, p, referenceRequest(t, "refund", wt.REFUND, 100, "bet"))
	processOK(t, p, request(t, "bet", "w", wt.BET, 100))
	gateA := resolverGate{runner: NewRunner(pool), started: make(chan int, 1), locked: make(chan struct{}), release: make(chan struct{}), hold: true}
	gateB := resolverGate{runner: NewRunner(pool), started: make(chan int, 1)}
	a, b := newProcessor(t, gateA), newProcessor(t, gateB)
	doneA, doneB := make(chan struct{}), make(chan struct{})
	errorsA, errorsB := make(chan error, 1), make(chan error, 1)
	go func() {
		defer close(doneA)
		_, err := a.ResolvePendingReference(ctx, "provider", pending.TransactionID)
		errorsA <- err
	}()
	defer func() { cancel(); <-doneA }()
	pidA := awaitPID(t, ctx, gateA.started)
	awaitSignal(t, ctx, gateA.locked)
	go func() {
		defer close(doneB)
		_, err := b.ResolvePendingReference(ctx, "provider", pending.TransactionID)
		errorsB <- err
	}()
	defer func() { cancel(); <-doneB }()
	pidB := awaitPID(t, ctx, gateB.started)
	awaitBlocking(t, ctx, pool, pidA, pidB)
	close(gateA.release)
	awaitSignal(t, ctx, doneA)
	awaitSignal(t, ctx, doneB)
	must(t, <-errorsA)
	must(t, <-errorsB)
	assertEvents(t, pool, pending.TransactionID, events.PendingReference, events.Processed, events.BalanceChanged)
	if count(t, pool, "wallet_ledger_entries") != 2 {
		t.Fatal("duplicate movement")
	}
}
func TestOutboxPendingAndRejectedInsertFailure(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending_%t", pending), func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			w := testWallet(t, "w", 100)
			must(t, NewWalletRepository(pool).Insert(ctx, w))
			req := request(t, "op", "w", wt.BET, 101)
			if pending {
				req = referenceRequest(t, "op", wt.REFUND, 100, "absent")
			}
			_, err := newProcessor(t, outboxFaultRunner{NewRunner(pool), "after first"}).Process(ctx, req)
			if !errors.Is(err, injected) {
				t.Fatal(err)
			}
			after, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			if after != w {
				t.Fatal("wallet changed")
			}
			for _, table := range []string{"wager_transactions", "wallet_ledger_entries", "financial_idempotency_keys", "outbox_events"} {
				if count(t, pool, table) != 0 {
					t.Fatal(table)
				}
			}
		})
	}
}
