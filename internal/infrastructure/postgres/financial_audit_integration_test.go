//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

func ledgerSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var snapshot string
	must(t, pool.QueryRow(testContext(t), `SELECT COALESCE(json_agg(row_to_json(e) ORDER BY id)::text,'[]') FROM wallet_ledger_entries e`).Scan(&snapshot))
	return snapshot
}
func wagerSnapshot(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var snapshot string
	must(t, pool.QueryRow(testContext(t), `SELECT row_to_json(w)::text FROM wager_transactions w WHERE id=$1`, id).Scan(&snapshot))
	return snapshot
}
func outcomeFixture(t *testing.T, id string, status wt.Status) *wt.WagerTransaction {
	t.Helper()
	input := wt.ExternalInput{ID: id, ProviderID: "provider", ExternalTransactionID: "external-" + id, PlayerID: "player-w", WalletID: "w", RoundID: "round", GameID: "game", Kind: wt.REFUND, Money: testMoney(t, 1), ReferenceExternalTransactionID: "reference"}
	var code wt.FailureCode
	if status == wt.REJECTED {
		code = wt.ReferenceNotFound
	}
	if status == wt.FAILED {
		code = wt.PermanentInfrastructureFailure
	}
	tx, err := wt.Rehydrate(wt.State{ExternalInput: input, Status: status, FailureCode: code, CreatedAt: testTime, UpdatedAt: testTime.Add(time.Hour)})
	must(t, err)
	return tx
}
func TestUpdateOutcomeTerminalProtection(t *testing.T) {
	for _, status := range []wt.Status{wt.PROCESSED, wt.REJECTED, wt.FAILED} {
		t.Run(string(status), func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			wr := NewWalletRepository(pool)
			tr := NewWagerTransactionRepository(pool)
			must(t, wr.Insert(ctx, testWallet(t, "w", 10000)))
			var id string
			if status == wt.FAILED {
				tx := testWager(t, "failed", "w")
				must(t, tr.Insert(ctx, tx))
				must(t, tx.Fail(wt.PermanentInfrastructureFailure, testTime.Add(time.Second)))
				must(t, tr.UpdateOutcome(ctx, tx, wt.PENDING))
				id = tx.ID()
			} else {
				amount := int64(8000)
				if status == wt.REJECTED {
					amount = 20000
				}
				result, err := financial.NewProcessor(NewRunner(pool)).Process(ctx, request(t, "op", "w", wt.BET, amount))
				must(t, err)
				id = result.TransactionID
			}
			before := wagerSnapshot(t, pool, id)
			beforeLedger := ledgerSnapshot(t, pool)
			beforeWallet, err := wr.GetByID(ctx, "w")
			must(t, err)
			for _, destination := range []wt.Status{wt.PENDING, wt.PENDING_REFERENCE, wt.PROCESSED, wt.REJECTED, wt.FAILED} {
				candidate := outcomeFixture(t, id, destination)
				if err := tr.UpdateOutcome(ctx, candidate, status); !errors.Is(err, wt.ErrInvalidTransition) {
					t.Fatalf("%s -> %s: %v", status, destination, err)
				}
			}
			// Even a caller claiming a nonterminal source cannot match the terminal row.
			if err := tr.UpdateOutcome(ctx, outcomeFixture(t, id, wt.FAILED), wt.PENDING); !errors.Is(err, ErrStaleOutcomeWrite) {
				t.Fatal(err)
			}
			afterWallet, err := wr.GetByID(ctx, "w")
			must(t, err)
			if wagerSnapshot(t, pool, id) != before || ledgerSnapshot(t, pool) != beforeLedger || afterWallet != beforeWallet {
				t.Fatal("terminal operation changed")
			}
		})
	}
}
func TestUpdateOutcomeTransitionPairs(t *testing.T) {
	statuses := []wt.Status{wt.PENDING, wt.PENDING_REFERENCE, wt.PROCESSED, wt.REJECTED, wt.FAILED, "UNKNOWN"}
	for _, source := range statuses {
		for _, destination := range statuses[:5] {
			t.Run(fmt.Sprintf("%s_%s", source, destination), func(t *testing.T) {
				allowed := (source == wt.PENDING && (destination == wt.PENDING_REFERENCE || destination == wt.FAILED)) || (source == wt.PENDING_REFERENCE && destination == wt.FAILED)
				if !allowed {
					// A nil db proves invalid pairs are rejected before attempting SQL.
					err := (&WagerTransactionRepository{}).UpdateOutcome(context.Background(), outcomeFixture(t, "t", destination), source)
					if !errors.Is(err, wt.ErrInvalidTransition) {
						t.Fatal(err)
					}
					return
				}
				pool := testPool(t)
				ctx := testContext(t)
				must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 100)))
				tr := NewWagerTransactionRepository(pool)
				initial := outcomeFixture(t, "t", source)
				must(t, tr.Insert(ctx, initial))
				must(t, tr.UpdateOutcome(ctx, outcomeFixture(t, "t", destination), source))
				got, err := tr.GetByID(ctx, "t")
				must(t, err)
				if got.Status() != destination {
					t.Fatal(got.Status())
				}
			})
		}
	}
}

// These wrappers gate real repository calls; the production processor and SQL
// transaction runner are unchanged.
type raceActor struct {
	runner  *Runner
	ready   chan int
	start   chan struct{}
	bound   chan struct{}
	release chan struct{}
	finds   int
	lost    bool
}

func newRaceActor(pool *pgxpool.Pool) *raceActor {
	return &raceActor{runner: NewRunner(pool), ready: make(chan int, 1), start: make(chan struct{}), bound: make(chan struct{}), release: make(chan struct{})}
}
func (a *raceActor) WithinFinancialTransaction(ctx context.Context, work func(financial.Repositories) error) error {
	return a.runner.WithinFinancialTransaction(ctx, func(r financial.Repositories) error {
		pid := int(r.Wallets.(*WalletRepository).tx.Conn().PgConn().PID())
		r.Transactions = &raceTransactions{Transactions: r.Transactions, actor: a, pid: pid}
		r.Keys = &raceKeys{Keys: r.Keys, actor: a}
		return work(r)
	})
}

type raceTransactions struct {
	financial.Transactions
	actor *raceActor
	pid   int
}

func (r *raceTransactions) FindFinancial(ctx context.Context, p, e string) (financial.Record, error) {
	record, err := r.Transactions.FindFinancial(ctx, p, e)
	r.actor.finds++
	if r.actor.finds == 1 {
		if !errors.Is(err, financial.ErrNotFound) {
			return record, fmt.Errorf("initial identity lookup was not absent: %v", err)
		}
		r.actor.ready <- r.pid
		select {
		case <-r.actor.start:
		case <-ctx.Done():
			return financial.Record{}, ctx.Err()
		}
	}
	return record, err
}
func (r *raceTransactions) TryInsertExternal(ctx context.Context, t *wt.WagerTransaction, h [32]byte) (bool, error) {
	inserted, err := r.Transactions.TryInsertExternal(ctx, t, h)
	if err == nil && !inserted {
		r.actor.lost = true
	}
	return inserted, err
}

type raceKeys struct {
	financial.Keys
	actor *raceActor
}

func (k *raceKeys) Bind(ctx context.Context, p, key, id string) error {
	if err := k.Keys.Bind(ctx, p, key, id); err != nil {
		return err
	}
	close(k.actor.bound)
	select {
	case <-k.actor.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func awaitPID(t *testing.T, ctx context.Context, ch <-chan int) int {
	t.Helper()
	select {
	case pid := <-ch:
		return pid
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return 0
	}
}
func awaitSignal(t *testing.T, ctx context.Context, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
func TestDeterministicFinancialRaces(t *testing.T) {
	for _, scenario := range []string{"same identity same payload", "same key different identities", "same identity different payload"} {
		t.Run(scenario, func(t *testing.T) {
			pool := testPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			wr := NewWalletRepository(pool)
			w1, w2 := testWallet(t, "a", 10000), testWallet(t, "b", 10000)
			must(t, wr.Insert(ctx, w1))
			must(t, wr.Insert(ctx, w2))
			firstReq := request(t, "original", "a", wt.BET, 8000)
			secondReq := firstReq
			switch scenario {
			case "same key different identities":
				secondReq = request(t, "other", "b", wt.BET, 8000)
				secondReq.IdempotencyKey = firstReq.IdempotencyKey
			case "same identity different payload":
				secondReq.WalletID = "b"
				secondReq.PlayerID = "player-b"
				secondReq.IdempotencyKey = "other-key"
			}
			winner, loser := newRaceActor(pool), newRaceActor(pool)
			firstDone, secondDone := make(chan concurrentResult, 1), make(chan concurrentResult, 1)
			firstJoined, secondJoined := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(firstJoined)
				r, e := financial.NewProcessor(winner).Process(ctx, firstReq)
				firstDone <- concurrentResult{r, e}
			}()
			go func() {
				defer close(secondJoined)
				r, e := financial.NewProcessor(loser).Process(ctx, secondReq)
				secondDone <- concurrentResult{r, e}
			}()
			defer func() { cancel(); <-firstJoined; <-secondJoined }()
			firstPID, secondPID := awaitPID(t, ctx, winner.ready), awaitPID(t, ctx, loser.ready)
			if firstPID == secondPID {
				t.Fatal("transactions share a connection")
			}
			close(winner.start)
			awaitSignal(t, ctx, winner.bound)
			close(loser.start)
			// The winner holds its transaction open until PostgreSQL reports the
			// specific loser blocked on it (wallet, key, or financial identity).
			for {
				var blocked bool
				must(t, pool.QueryRow(ctx, `SELECT $1::int=ANY(pg_blocking_pids($2::int))`, firstPID, secondPID).Scan(&blocked))
				if blocked {
					break
				}
				select {
				case r := <-secondDone:
					t.Fatalf("loser completed before contention: %v", r.err)
				default:
				}
			}
			close(loser.release)
			close(winner.release)
			awaitSignal(t, ctx, firstJoined)
			awaitSignal(t, ctx, secondJoined)
			first, second := <-firstDone, <-secondDone
			must(t, first.err)
			if first.result.Status != wt.PROCESSED || first.result.IdempotentReplay {
				t.Fatal(first.result)
			}
			assertBalance(t, first.result, 2000)
			if scenario == "same identity same payload" {
				must(t, second.err)
				assertBalance(t, second.result, 2000)
				if !second.result.IdempotentReplay || second.result.TransactionID != first.result.TransactionID {
					t.Fatal(second.result)
				}
			} else if !errors.Is(second.err, financial.ErrConflict) || second.result != (financial.ProcessResult{}) {
				t.Fatal(second.result, second.err)
			}
			if scenario != "same key different identities" && (!loser.lost || loser.finds != 2) {
				t.Fatalf("missing losing insert/fresh lookup: lost=%v finds=%d", loser.lost, loser.finds)
			}
			if count(t, pool, "wager_transactions") != 1 || count(t, pool, "financial_idempotency_keys") != 1 || count(t, pool, "wallet_ledger_entries") != 1 {
				t.Fatal("orphan or duplicate records")
			}
			gotA, err := wr.GetByID(ctx, "a")
			must(t, err)
			gotB, err := wr.GetByID(ctx, "b")
			must(t, err)
			n, _ := gotA.Balance().MinorUnits()
			if n != 2000 || gotA.Version() != 2 || gotB != w2 {
				t.Fatal("wrong wallet effects")
			}
			saved, err := NewWagerTransactionRepository(pool).FindFinancial(ctx, firstReq.ProviderID, firstReq.ExternalTransactionID)
			must(t, err)
			if saved.Transaction.ID() != first.result.TransactionID || saved.Balance == nil || !reflect.DeepEqual(*saved.Balance, *first.result.Balance) {
				t.Fatal("original result changed")
			}
			var bound string
			must(t, pool.QueryRow(ctx, `SELECT transaction_id FROM financial_idempotency_keys`).Scan(&bound))
			if bound != first.result.TransactionID {
				t.Fatal("wrong key winner")
			}
		})
	}
}
