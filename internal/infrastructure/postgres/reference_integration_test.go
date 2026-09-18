//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
)

func referenceRequest(t *testing.T, id string, kind wt.Kind, amount int64, ref string) financial.ProcessRequest {
	r := request(t, id, "w", kind, amount)
	r.ReferenceExternalTransactionID = ref
	return r
}
func processOK(t *testing.T, p *financial.Processor, r financial.ProcessRequest) financial.ProcessResult {
	t.Helper()
	got, err := p.Process(testContext(t), r)
	must(t, err)
	return got
}
func assertOutcome(t *testing.T, r financial.ProcessResult, status wt.Status, code wt.FailureCode) {
	t.Helper()
	if r.Status != status || r.FailureCode != code {
		t.Fatal(r)
	}
}
func TestReferenceFinancialPaths(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    wt.Kind
		refKind wt.Kind
		amount  int64
		code    wt.FailureCode
	}{
		{"refund", wt.REFUND, wt.BET, 8000, ""}, {"partial refund", wt.REFUND, wt.BET, 7000, wt.ReferenceMismatch},
		{"refund win", wt.REFUND, wt.WIN, 8000, wt.ReferenceNotAllowed},
		{"rollback bet", wt.ROLLBACK, wt.BET, 8000, ""}, {"rollback win", wt.ROLLBACK, wt.WIN, 8000, ""},
		{"rollback amount", wt.ROLLBACK, wt.BET, 7000, wt.ReferenceMismatch},
		{"rollback loss", wt.ROLLBACK, wt.LOSS, 1, wt.ReferenceNotAllowed},
		{"win reference", wt.WIN, wt.BET, 9000, ""}, {"win wrong type", wt.WIN, wt.WIN, 8000, wt.ReferenceNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
			p := newProcessor(t, NewRunner(pool))
			n := int64(8000)
			if tc.refKind == wt.LOSS {
				n = 0
			}
			original := processOK(t, p, request(t, "original", "w", tc.refKind, n))
			before, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			ledgerBefore := count(t, pool, "wallet_ledger_entries")
			req := referenceRequest(t, "dependent", tc.kind, tc.amount, "original")
			req.GameID = "different-game"
			result := processOK(t, p, req)
			if tc.code != "" {
				assertOutcome(t, result, wt.REJECTED, tc.code)
				after, err := NewWalletRepository(pool).GetByID(ctx, "w")
				must(t, err)
				if after != before || count(t, pool, "wallet_ledger_entries") != ledgerBefore {
					t.Fatal("rejection moved money")
				}
			} else {
				assertOutcome(t, result, wt.PROCESSED, "")
				want, _ := before.Balance().MinorUnits()
				direction := "CREDIT"
				if tc.kind == wt.ROLLBACK && tc.refKind == wt.WIN {
					want -= tc.amount
					direction = "DEBIT"
				} else {
					want += tc.amount
				}
				assertBalance(t, result, want)
				var refID, dir string
				must(t, pool.QueryRow(ctx, `SELECT reference_transaction_id FROM wager_transactions WHERE id=$1`, result.TransactionID).Scan(&refID))
				must(t, pool.QueryRow(ctx, `SELECT direction FROM wallet_ledger_entries WHERE transaction_id=$1`, result.TransactionID).Scan(&dir))
				if refID != original.TransactionID || dir != direction || count(t, pool, "wallet_ledger_entries") != ledgerBefore+1 {
					t.Fatal("reference/ledger")
				}
			}
			replay := processOK(t, p, req)
			if !replay.IdempotentReplay || replay.Status != result.Status {
				t.Fatal(replay)
			}
		})
	}
}
func TestRollbackRefundAndExclusivity(t *testing.T) {
	pool := testPool(t)
	must(t, NewWalletRepository(pool).Insert(testContext(t), testWallet(t, "w", 10000)))
	p := newProcessor(t, NewRunner(pool))
	processOK(t, p, request(t, "bet", "w", wt.BET, 8000))
	processOK(t, p, referenceRequest(t, "refund", wt.REFUND, 8000, "bet"))
	assertOutcome(t, processOK(t, p, referenceRequest(t, "direct", wt.ROLLBACK, 8000, "bet")), wt.REJECTED, wt.AlreadyReversed)
	rollback := processOK(t, p, referenceRequest(t, "undo-refund", wt.ROLLBACK, 8000, "refund"))
	assertBalance(t, rollback, 2000)
	assertOutcome(t, processOK(t, p, referenceRequest(t, "new-refund", wt.REFUND, 8000, "bet")), wt.REJECTED, wt.AlreadyReversed)
	assertOutcome(t, processOK(t, p, referenceRequest(t, "undo-rollback", wt.ROLLBACK, 8000, "undo-refund")), wt.REJECTED, wt.ReferenceNotAllowed)
	for i := 0; i < 2; i++ {
		r := processOK(t, p, referenceRequest(t, fmt.Sprintf("win-%d", i), wt.WIN, 100, "bet"))
		assertOutcome(t, r, wt.PROCESSED, "")
	}
}
func TestReferenceMonetaryFailures(t *testing.T) {
	for _, name := range []string{"insufficient", "overflow"} {
		t.Run(name, func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			p := newProcessor(t, NewRunner(pool))
			initial := int64(10000)
			if name == "overflow" {
				initial = math.MaxInt64
			}
			must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", initial)))
			var req financial.ProcessRequest
			var code wt.FailureCode
			if name == "insufficient" {
				processOK(t, p, request(t, "win", "w", wt.WIN, 8000))
				processOK(t, p, request(t, "spend", "w", wt.BET, 18000))
				req = referenceRequest(t, "reverse", wt.ROLLBACK, 8000, "win")
				code = wt.ReversalInsufficientFunds
			} else {
				processOK(t, p, request(t, "bet", "w", wt.BET, 100))
				processOK(t, p, request(t, "win", "w", wt.WIN, 100))
				req = referenceRequest(t, "reverse", wt.ROLLBACK, 100, "bet")
				code = wt.MonetaryOverflow
			}
			before, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			snapshot := ledgerSnapshot(t, pool)
			got := processOK(t, p, req)
			assertOutcome(t, got, wt.REJECTED, code)
			after, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			if after != before || snapshot != ledgerSnapshot(t, pool) {
				t.Fatal("failed reversal changed wallet")
			}
		})
	}
}
func TestPendingResolutionAndExpiration(t *testing.T) {
	for _, kind := range []wt.Kind{wt.REFUND, wt.ROLLBACK, wt.WIN} {
		for _, expire := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_expire_%t", kind, expire), func(t *testing.T) {
				pool := testPool(t)
				ctx := testContext(t)
				w := testWallet(t, "w", 10000)
				must(t, NewWalletRepository(pool).Insert(ctx, w))
				var clock atomic.Int64
				clock.Store(testTime.UnixNano())
				now := func() time.Time { return time.Unix(0, clock.Load()) }
				p, err := financial.NewProcessor(NewRunner(pool), time.Hour, now)
				must(t, err)
				req := referenceRequest(t, "dependent", kind, 8000, "original")
				pending := processOK(t, p, req)
				assertOutcome(t, pending, wt.PENDING_REFERENCE, "")
				if pending.Balance != nil {
					t.Fatal("pending result balance")
				}
				stored, err := NewWagerTransactionRepository(pool).FindByProviderID(ctx, "provider", pending.TransactionID)
				must(t, err)
				if stored.ReferenceDeadline == nil || !stored.ReferenceDeadline.Equal(testTime.Add(time.Hour)) {
					t.Fatal(stored)
				}
				before, err := NewWalletRepository(pool).GetByID(ctx, "w")
				must(t, err)
				if before != w || count(t, pool, "wallet_ledger_entries") != 0 {
					t.Fatal("pending movement")
				}
				clock.Store(testTime.Add(30 * time.Minute).UnixNano())
				alias := req
				alias.IdempotencyKey = "alias"
				replay := processOK(t, p, alias)
				if !replay.IdempotentReplay || replay.TransactionID != pending.TransactionID {
					t.Fatal(replay)
				}
				result, err := p.ResolvePendingReference(ctx, "provider", pending.TransactionID)
				must(t, err)
				assertOutcome(t, result, wt.PENDING_REFERENCE, "")
				if _, err := p.ResolvePendingReference(ctx, "other-provider", pending.TransactionID); !errors.Is(err, financial.ErrNotFound) {
					t.Fatal(err)
				}
				if expire {
					clock.Store(testTime.Add(2 * time.Hour).UnixNano())
				}
				processOK(t, p, request(t, "original", "w", wt.BET, 8000))
				result, err = p.ResolvePendingReference(ctx, "provider", pending.TransactionID)
				must(t, err)
				if expire {
					assertOutcome(t, result, wt.REJECTED, wt.ReferenceNotFound)
					assertBalance(t, result, 2000)
				} else {
					assertOutcome(t, result, wt.PROCESSED, "")
					assertBalance(t, result, 10000)
				}
				processOK(t, p, request(t, "later", "w", wt.WIN, 500))
				replay = processOK(t, p, req)
				if !replay.IdempotentReplay || *replay.Balance != *result.Balance || replay.Status != result.Status {
					t.Fatal("historical replay", replay)
				}
				final, err := NewWagerTransactionRepository(pool).FindByProviderID(ctx, "provider", pending.TransactionID)
				must(t, err)
				if !final.ReferenceDeadline.Equal(*stored.ReferenceDeadline) {
					t.Fatal("deadline changed")
				}
				snapshot := wagerSnapshot(t, pool, pending.TransactionID)
				_, err = p.ResolvePendingReference(ctx, "provider", pending.TransactionID)
				must(t, err)
				if snapshot != wagerSnapshot(t, pool, pending.TransactionID) {
					t.Fatal("terminal resolution changed record")
				}
			})
		}
	}
}
func TestReferenceContextAndStatus(t *testing.T) {
	for _, name := range []string{"player", "wallet", "currency", "round", "provider", "pending", "pending-reference", "rejected", "failed"} {
		t.Run(name, func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			wr := NewWalletRepository(pool)
			must(t, wr.Insert(ctx, testWallet(t, "w", 10000)))
			must(t, wr.Insert(ctx, testWallet(t, "other", 10000)))
			input := wt.ExternalInput{ID: "original", ProviderID: "provider", ExternalTransactionID: "original", PlayerID: "player-w", WalletID: "w", RoundID: "round", GameID: "game", Kind: wt.BET, Money: testMoney(t, 100)}
			status := wt.PROCESSED
			expected := wt.ReferenceMismatch
			switch name {
			case "player":
				input.PlayerID = "other-player"
			case "wallet":
				input.WalletID = "other"
			case "currency":
				input.Money, _ = money.FromMinorUnits(100, "USD")
			case "round":
				input.RoundID = "other-round"
			case "provider":
				input.ProviderID = "other-provider"
			case "pending":
				status = wt.PENDING
			case "pending-reference":
				status = wt.PENDING_REFERENCE
				input.Kind = wt.REFUND
				input.ReferenceExternalTransactionID = "missing"
			case "rejected":
				status = wt.REJECTED
				expected = wt.ReferenceUnsuccessful
			case "failed":
				status = wt.FAILED
				expected = wt.ReferenceUnsuccessful
			}
			tx, err := wt.NewExternal(input, testTime)
			must(t, err)
			tr := NewWagerTransactionRepository(pool)
			must(t, tr.Insert(ctx, tx))
			switch status {
			case wt.PROCESSED:
				must(t, tx.MarkProcessed(testTime))
				must(t, tr.CompleteOutcome(ctx, tx, wt.PENDING, testMoney(t, 10000)))
			case wt.REJECTED:
				must(t, tx.Reject(wt.BetInsufficientFunds, testTime))
				must(t, tr.CompleteOutcome(ctx, tx, wt.PENDING, testMoney(t, 10000)))
			case wt.FAILED:
				must(t, tx.Fail(wt.PermanentInfrastructureFailure, testTime))
				must(t, tr.UpdateOutcome(ctx, tx, wt.PENDING))
			case wt.PENDING_REFERENCE:
				must(t, tx.WaitForReference(testTime))
				must(t, tr.MarkPendingReference(ctx, tx, testTime.Add(time.Hour)))
			}
			kind := wt.REFUND
			if name == "pending-reference" {
				kind = wt.ROLLBACK
			}
			p := newProcessor(t, NewRunner(pool))
			got := processOK(t, p, referenceRequest(t, "dependent", kind, 100, "original"))
			if name == "provider" || name == "pending" || name == "pending-reference" {
				assertOutcome(t, got, wt.PENDING_REFERENCE, "")
			} else {
				assertOutcome(t, got, wt.REJECTED, expected)
			}
			if count(t, pool, "wallet_ledger_entries") != 0 {
				t.Fatal("invalid reference moved money")
			}
		})
	}
}
func TestReferenceMigrationRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	p := newProcessor(t, NewRunner(pool))
	result := processOK(t, p, request(t, "bet", "w", wt.BET, 8000))
	before := ledgerSnapshot(t, pool)
	up, err := os.ReadFile("../../../migrations/000004_references.up.sql")
	must(t, err)
	down, err := os.ReadFile("../../../migrations/000004_references.down.sql")
	must(t, err)
	_, err = pool.Exec(ctx, string(down))
	must(t, err)
	_, err = pool.Exec(ctx, string(up))
	must(t, err)
	replay := processOK(t, p, request(t, "bet", "w", wt.BET, 8000))
	if replay.TransactionID != result.TransactionID || *replay.Balance != *result.Balance || ledgerSnapshot(t, pool) != before {
		t.Fatal("migration changed financial history")
	}
}
func TestReversalUniqueIndex(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	p := newProcessor(t, NewRunner(pool))
	processOK(t, p, request(t, "bet", "w", wt.BET, 8000))
	first := processOK(t, p, referenceRequest(t, "refund", wt.REFUND, 8000, "bet"))
	_, err := pool.Exec(ctx, `INSERT INTO wager_transactions SELECT 'duplicate',provider_id,'duplicate',player_id,wallet_id,round_id,game_id,'ROLLBACK',amount_minor,currency,reference_external_transaction_id,status,failure_code,created_at,updated_at,payload_hash,result_balance_minor,result_currency,reference_transaction_id,reference_deadline_at FROM wager_transactions WHERE id=$1`, first.TransactionID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "wager_successful_reversal_unique" {
		t.Fatal(err)
	}
}

func TestReversalRaces(t *testing.T) {
	for _, pair := range [][2]wt.Kind{{wt.REFUND, wt.REFUND}, {wt.ROLLBACK, wt.ROLLBACK}, {wt.REFUND, wt.ROLLBACK}, {wt.ROLLBACK, wt.REFUND}} {
		t.Run(fmt.Sprint(pair), func(t *testing.T) {
			pool := testPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
			p := newProcessor(t, NewRunner(pool))
			processOK(t, p, request(t, "bet", "w", wt.BET, 8000))
			a, b := newRaceActor(pool), newRaceActor(pool)
			doneA, doneB := make(chan concurrentResult, 1), make(chan concurrentResult, 1)
			joinedA, joinedB := make(chan struct{}), make(chan struct{})
			reqA, reqB := referenceRequest(t, "a", pair[0], 8000, "bet"), referenceRequest(t, "b", pair[1], 8000, "bet")
			pa, pb := newProcessor(t, a), newProcessor(t, b)
			go func() { defer close(joinedA); r, e := pa.Process(ctx, reqA); doneA <- concurrentResult{r, e} }()
			go func() { defer close(joinedB); r, e := pb.Process(ctx, reqB); doneB <- concurrentResult{r, e} }()
			defer func() { cancel(); <-joinedA; <-joinedB }()
			pidA, pidB := awaitPID(t, ctx, a.ready), awaitPID(t, ctx, b.ready)
			close(a.start)
			awaitSignal(t, ctx, a.bound)
			close(b.start)
			awaitBlocking(t, ctx, pool, pidA, pidB)
			close(a.release)
			close(b.release)
			awaitSignal(t, ctx, joinedA)
			awaitSignal(t, ctx, joinedB)
			first, second := <-doneA, <-doneB
			must(t, first.err)
			must(t, second.err)
			assertOutcome(t, first.result, wt.PROCESSED, "")
			assertOutcome(t, second.result, wt.REJECTED, wt.AlreadyReversed)
			assertBalance(t, first.result, 10000)
			assertBalance(t, second.result, 10000)
			if count(t, pool, "wallet_ledger_entries") != 2 {
				t.Fatal("duplicate reversal")
			}
		})
	}
}
func awaitBlocking(t *testing.T, ctx context.Context, pool *pgxpool.Pool, holder, waiter int) {
	t.Helper()
	for {
		var blocked bool
		must(t, pool.QueryRow(ctx, `SELECT $1::int=ANY(pg_blocking_pids($2::int))`, holder, waiter).Scan(&blocked))
		if blocked {
			return
		}
	}
}

type resolverGate struct {
	runner  *Runner
	started chan int
	locked  chan struct{}
	release chan struct{}
	hold    bool
}

func (g resolverGate) WithinFinancialTransaction(ctx context.Context, work func(financial.Repositories) error) error {
	return g.runner.WithinFinancialTransaction(ctx, func(r financial.Repositories) error {
		g.started <- int(r.Wallets.(*WalletRepository).tx.Conn().PgConn().PID())
		if g.hold {
			r.Transactions = heldResolution{Transactions: r.Transactions, locked: g.locked, release: g.release}
		}
		return work(r)
	})
}

type heldResolution struct {
	financial.Transactions
	locked  chan struct{}
	release chan struct{}
}

func (h heldResolution) GetForResolution(ctx context.Context, p, id string) (financial.Record, error) {
	r, err := h.Transactions.GetForResolution(ctx, p, id)
	if err != nil {
		return r, err
	}
	close(h.locked)
	select {
	case <-h.release:
		return r, nil
	case <-ctx.Done():
		return financial.Record{}, ctx.Err()
	}
}
func TestResolverRaces(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay_%t", replay), func(t *testing.T) {
			pool := testPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
			p := newProcessor(t, NewRunner(pool))
			req := referenceRequest(t, "refund", wt.REFUND, 8000, "bet")
			pending := processOK(t, p, req)
			processOK(t, p, request(t, "bet", "w", wt.BET, 8000))
			gateA := resolverGate{runner: NewRunner(pool), started: make(chan int, 1), locked: make(chan struct{}), release: make(chan struct{}), hold: true}
			gateB := resolverGate{runner: NewRunner(pool), started: make(chan int, 1)}
			a, b := newProcessor(t, gateA), newProcessor(t, gateB)
			doneA, doneB := make(chan concurrentResult, 1), make(chan concurrentResult, 1)
			joinedA, joinedB := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(joinedA)
				r, e := a.ResolvePendingReference(ctx, "provider", pending.TransactionID)
				doneA <- concurrentResult{r, e}
			}()
			// Register cancellation before waiting for any synchronization point.
			defer func() { cancel(); <-joinedA }()
			pidA := awaitPID(t, ctx, gateA.started)
			awaitSignal(t, ctx, gateA.locked)
			alias := req
			alias.IdempotencyKey = "alias"
			go func() {
				defer close(joinedB)
				var r financial.ProcessResult
				var e error
				if replay {
					r, e = b.Process(ctx, alias)
				} else {
					r, e = b.ResolvePendingReference(ctx, "provider", pending.TransactionID)
				}
				doneB <- concurrentResult{r, e}
			}()
			defer func() { cancel(); <-joinedB }()
			pidB := awaitPID(t, ctx, gateB.started)
			awaitBlocking(t, ctx, pool, pidA, pidB)
			close(gateA.release)
			awaitSignal(t, ctx, joinedA)
			awaitSignal(t, ctx, joinedB)
			first, second := <-doneA, <-doneB
			must(t, first.err)
			must(t, second.err)
			assertOutcome(t, first.result, wt.PROCESSED, "")
			if !second.result.IdempotentReplay {
				t.Fatal(second.result)
			}
			if second.result.Status != wt.PROCESSED && (!replay || second.result.Status != wt.PENDING_REFERENCE) {
				t.Fatal(second.result)
			}
			if count(t, pool, "wallet_ledger_entries") != 2 {
				t.Fatal("duplicate resolution")
			}
			final := processOK(t, p, req)
			assertOutcome(t, final, wt.PROCESSED, "")
			assertBalance(t, final, 10000)
		})
	}
}
func TestOriginalAndReversalRace(t *testing.T) {
	for _, originalFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(originalFirst), func(t *testing.T) {
			pool := testPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
			a, b := newRaceActor(pool), newRaceActor(pool)
			firstReq, secondReq := request(t, "bet", "w", wt.BET, 8000), referenceRequest(t, "refund", wt.REFUND, 8000, "bet")
			if !originalFirst {
				firstReq, secondReq = secondReq, firstReq
			}
			pa, pb := newProcessor(t, a), newProcessor(t, b)
			doneA, doneB := make(chan concurrentResult, 1), make(chan concurrentResult, 1)
			joinedA, joinedB := make(chan struct{}), make(chan struct{})
			go func() { defer close(joinedA); r, e := pa.Process(ctx, firstReq); doneA <- concurrentResult{r, e} }()
			go func() { defer close(joinedB); r, e := pb.Process(ctx, secondReq); doneB <- concurrentResult{r, e} }()
			defer func() { cancel(); <-joinedA; <-joinedB }()
			pidA, pidB := awaitPID(t, ctx, a.ready), awaitPID(t, ctx, b.ready)
			close(a.start)
			awaitSignal(t, ctx, a.bound)
			close(b.start)
			awaitBlocking(t, ctx, pool, pidA, pidB)
			close(a.release)
			close(b.release)
			awaitSignal(t, ctx, joinedA)
			awaitSignal(t, ctx, joinedB)
			first, second := <-doneA, <-doneB
			must(t, first.err)
			must(t, second.err)
			p := newProcessor(t, NewRunner(pool))
			if !originalFirst {
				assertOutcome(t, first.result, wt.PENDING_REFERENCE, "")
				r, e := p.ResolvePendingReference(ctx, "provider", first.result.TransactionID)
				must(t, e)
				assertOutcome(t, r, wt.PROCESSED, "")
			}
			if count(t, pool, "wallet_ledger_entries") != 2 {
				t.Fatal("missing/duplicate ledger")
			}
		})
	}
}
func TestResolutionFailureRollback(t *testing.T) {
	for _, stage := range []string{"wallet", "ledger", "outcome"} {
		t.Run(stage, func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
			p := newProcessor(t, NewRunner(pool))
			req := referenceRequest(t, "refund", wt.REFUND, 8000, "bet")
			pending := processOK(t, p, req)
			processOK(t, p, request(t, "bet", "w", wt.BET, 8000))
			before := wagerSnapshot(t, pool, pending.TransactionID)
			beforeLedger := ledgerSnapshot(t, pool)
			w, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			failed := newProcessor(t, faultRunner{NewRunner(pool), stage})
			r, err := failed.ResolvePendingReference(ctx, "provider", pending.TransactionID)
			if !errors.Is(err, injected) || r != (financial.ProcessResult{}) {
				t.Fatal(r, err)
			}
			after, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			if after != w || wagerSnapshot(t, pool, pending.TransactionID) != before || ledgerSnapshot(t, pool) != beforeLedger {
				t.Fatal("partial resolution survived")
			}
			r, err = p.ResolvePendingReference(ctx, "provider", pending.TransactionID)
			must(t, err)
			assertOutcome(t, r, wt.PROCESSED, "")
		})
	}
}

func TestAbsentReferenceDeadlineAndUnexpectedPending(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	at := testTime
	p, err := financial.NewProcessor(NewRunner(pool), time.Hour, func() time.Time { return at })
	must(t, err)
	req := referenceRequest(t, "refund", wt.REFUND, 8000, "missing")
	pending := processOK(t, p, req)
	at = at.Add(time.Hour)
	result, err := p.ResolvePendingReference(ctx, "provider", pending.TransactionID)
	must(t, err)
	assertOutcome(t, result, wt.REJECTED, wt.ReferenceNotFound)
	assertBalance(t, result, 10000)
	if count(t, pool, "wallet_ledger_entries") != 0 {
		t.Fatal("expiry moved money")
	}
	original := testWager(t, "pending", "w")
	must(t, NewWagerTransactionRepository(pool).Insert(ctx, original))
	if _, err := p.ResolvePendingReference(ctx, "provider", original.ID()); !errors.Is(err, financial.ErrInvalidPersistedData) {
		t.Fatal(err)
	}
}
func TestPendingReferenceBecomesUnsuccessful(t *testing.T) {
	for _, status := range []wt.Status{wt.REJECTED, wt.FAILED} {
		t.Run(string(status), func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
			tr := NewWagerTransactionRepository(pool)
			original := testWager(t, "original", "w")
			must(t, tr.Insert(ctx, original))
			p := newProcessor(t, NewRunner(pool))
			pending := processOK(t, p, referenceRequest(t, "refund", wt.REFUND, 1, original.ExternalTransactionID()))
			assertOutcome(t, pending, wt.PENDING_REFERENCE, "")
			if status == wt.REJECTED {
				must(t, original.Reject(wt.BetInsufficientFunds, testTime))
				must(t, tr.CompleteOutcome(ctx, original, wt.PENDING, testMoney(t, 10000)))
			} else {
				must(t, original.Fail(wt.PermanentInfrastructureFailure, testTime))
				must(t, tr.UpdateOutcome(ctx, original, wt.PENDING))
			}
			result, err := p.ResolvePendingReference(ctx, "provider", pending.TransactionID)
			must(t, err)
			assertOutcome(t, result, wt.REJECTED, wt.ReferenceUnsuccessful)
		})
	}
}
func TestReferenceDowngradeProtectsMetadata(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	p := newProcessor(t, NewRunner(pool))
	pending := processOK(t, p, referenceRequest(t, "refund", wt.REFUND, 8000, "missing"))
	before := wagerSnapshot(t, pool, pending.TransactionID)
	down, err := os.ReadFile("../../../migrations/000004_references.down.sql")
	must(t, err)
	conn, err := pool.Acquire(ctx)
	must(t, err)
	defer conn.Release()
	_, err = conn.Exec(ctx, string(down))
	if err == nil {
		t.Fatal("unsafe downgrade accepted")
	}
	_, err = conn.Exec(ctx, "ROLLBACK")
	must(t, err)
	if wagerSnapshot(t, pool, pending.TransactionID) != before {
		t.Fatal("reference metadata lost")
	}
}

func TestCrossWalletReferenceDoesNotLockOtherWallet(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	wr := NewWalletRepository(pool)
	must(t, wr.Insert(ctx, testWallet(t, "w", 10000)))
	must(t, wr.Insert(ctx, testWallet(t, "other", 10000)))
	p := newProcessor(t, NewRunner(pool))
	processOK(t, p, request(t, "bet", "other", wt.BET, 8000))
	tx, err := pool.Begin(ctx)
	must(t, err)
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(c)
	}()
	_, err = tx.Exec(ctx, `SELECT id FROM wallets WHERE id='other' FOR UPDATE`)
	must(t, err)
	got := processOK(t, p, referenceRequest(t, "refund", wt.REFUND, 8000, "bet"))
	assertOutcome(t, got, wt.REJECTED, wt.ReferenceMismatch)
}
func TestReversalVersionExhaustion(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	wr := NewWalletRepository(pool)
	must(t, wr.Insert(ctx, testWallet(t, "w", 10000)))
	p := newProcessor(t, NewRunner(pool))
	processOK(t, p, request(t, "bet", "w", wt.BET, 8000))
	_, err := pool.Exec(ctx, `UPDATE wallets SET version=$1 WHERE id='w'`, int64(math.MaxInt64))
	must(t, err)
	before, err := wr.GetByID(ctx, "w")
	must(t, err)
	snapshot := ledgerSnapshot(t, pool)
	_, err = p.Process(ctx, referenceRequest(t, "refund", wt.REFUND, 8000, "bet"))
	if !errors.Is(err, wallet.ErrVersionOverflow) {
		t.Fatal(err)
	}
	after, err := wr.GetByID(ctx, "w")
	must(t, err)
	if after != before || snapshot != ledgerSnapshot(t, pool) || count(t, pool, "wager_transactions") != 1 || count(t, pool, "financial_idempotency_keys") != 1 {
		t.Fatal("capacity failure was committed")
	}
}
