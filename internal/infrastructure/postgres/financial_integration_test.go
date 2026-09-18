//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/domain/ledger"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
)

func request(t *testing.T, id, walletID string, kind wt.Kind, n int64) financial.ProcessRequest {
	t.Helper()
	return financial.ProcessRequest{ProviderID: "provider", IdempotencyKey: "key-" + id, ExternalTransactionID: id, PlayerID: "player-" + walletID, WalletID: walletID, RoundID: "round", GameID: "game", Kind: kind, Money: testMoney(t, n)}
}
func assertBalance(t *testing.T, result financial.ProcessResult, want int64) {
	t.Helper()
	if result.Balance == nil {
		t.Fatal("missing saved balance")
	}
	got, err := result.Balance.MinorUnits()
	must(t, err)
	if got != want {
		t.Fatalf("balance %d, want %d", got, want)
	}
}
func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	must(t, pool.QueryRow(testContext(t), "SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func TestFinancialOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		kind                  wt.Kind
		initial, amount, want int64
		currency              string
		code                  wt.FailureCode
		movement              bool
	}{
		{"bet", wt.BET, 10000, 8000, 2000, "BRL", "", true},
		{"win", wt.WIN, 10000, 8000, 18000, "BRL", "", true},
		{"loss", wt.LOSS, 10000, 0, 10000, "BRL", "", false},
		{"insufficient", wt.BET, 10000, 10001, 10000, "BRL", wt.BetInsufficientFunds, false},
		{"currency bet", wt.BET, 10000, 1, 10000, "USD", wt.CurrencyMismatch, false},
		{"currency win", wt.WIN, 10000, 1, 10000, "USD", wt.CurrencyMismatch, false},
		{"currency loss", wt.LOSS, 10000, 0, 10000, "USD", wt.CurrencyMismatch, false},
		{"overflow", wt.WIN, math.MaxInt64, 1, math.MaxInt64, "BRL", wt.MonetaryOverflow, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			w := testWallet(t, "w", tc.initial)
			must(t, NewWalletRepository(pool).Insert(ctx, w))
			req := request(t, "op", "w", tc.kind, tc.amount)
			req.Money, _ = money.FromMinorUnits(tc.amount, tc.currency)
			p := newProcessor(t, NewRunner(pool))
			result, err := p.Process(ctx, req)
			must(t, err)
			assertBalance(t, result, tc.want)
			wantStatus := wt.PROCESSED
			if tc.code != "" {
				wantStatus = wt.REJECTED
			}
			if result.Status != wantStatus || result.FailureCode != tc.code || result.IdempotentReplay {
				t.Fatal(result)
			}
			got, err := NewWalletRepository(pool).GetByID(ctx, w.ID())
			must(t, err)
			n, _ := got.Balance().MinorUnits()
			if n != tc.want {
				t.Fatal(n)
			}
			ledgerCount := 0
			if tc.movement {
				ledgerCount = 1
				if got.Version() != w.Version()+1 || got.UpdatedAt().Equal(w.UpdatedAt()) {
					t.Fatal("wallet metadata")
				}
				var direction string
				var amount, before, after int64
				var currency string
				must(t, pool.QueryRow(ctx, `SELECT direction,amount_minor,balance_before_minor,balance_after_minor,currency FROM wallet_ledger_entries WHERE transaction_id=$1`, result.TransactionID).Scan(&direction, &amount, &before, &after, &currency))
				wantDirection := "DEBIT"
				if tc.kind == wt.WIN {
					wantDirection = "CREDIT"
				}
				if direction != wantDirection || amount != tc.amount || before != tc.initial || after != tc.want || currency != "BRL" {
					t.Fatal("ledger mismatch")
				}
			} else if got != w {
				t.Fatal("no-movement operation changed wallet")
			}
			if count(t, pool, "wallet_ledger_entries") != ledgerCount {
				t.Fatal("ledger count")
			}
			var storedBalance int64
			var storedCurrency string
			must(t, pool.QueryRow(ctx, `SELECT result_balance_minor,result_currency FROM wager_transactions WHERE id=$1`, result.TransactionID).Scan(&storedBalance, &storedCurrency))
			if storedBalance != tc.want || storedCurrency != "BRL" {
				t.Fatal("result snapshot")
			}
			replay, err := p.Process(ctx, req)
			must(t, err)
			assertBalance(t, replay, tc.want)
			if !replay.IdempotentReplay || replay.Status != result.Status || replay.FailureCode != result.FailureCode {
				t.Fatal(replay)
			}
		})
	}
}

func TestFinancialContextAndCapacity(t *testing.T) {
	for _, name := range []string{"missing wallet", "wrong player", "version exhausted"} {
		t.Run(name, func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			w := testWallet(t, "w", 100)
			must(t, NewWalletRepository(pool).Insert(ctx, w))
			req := request(t, "op", "w", wt.BET, 1)
			want := financial.ErrInvalidContext
			switch name {
			case "missing wallet":
				req.WalletID = "missing"
			case "wrong player":
				req.PlayerID = "wrong"
			case "version exhausted":
				_, err := pool.Exec(ctx, `UPDATE wallets SET version=$1 WHERE id='w'`, int64(math.MaxInt64))
				must(t, err)
				want = wallet.ErrVersionOverflow
			}
			before, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			result, err := newProcessor(t, NewRunner(pool)).Process(ctx, req)
			if !errors.Is(err, want) || result != (financial.ProcessResult{}) {
				t.Fatal(result, err)
			}
			after, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			if after != before {
				t.Fatal("mutated")
			}
			for _, table := range []string{"wager_transactions", "financial_idempotency_keys", "wallet_ledger_entries"} {
				if count(t, pool, table) != 0 {
					t.Fatal(table)
				}
			}
		})
	}
}

func TestFinancialReplayAndConflicts(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	p := newProcessor(t, NewRunner(pool))
	req := request(t, "bet", "w", wt.BET, 8000)
	original, err := p.Process(ctx, req)
	must(t, err)
	_, err = p.Process(ctx, request(t, "win", "w", wt.WIN, 5000))
	must(t, err)
	var updated time.Time
	must(t, pool.QueryRow(ctx, `SELECT updated_at FROM wager_transactions WHERE id=$1`, original.TransactionID).Scan(&updated))
	// A fresh processor must replay durable history rather than current wallet state.
	p = newProcessor(t, NewRunner(pool))
	for _, key := range []string{req.IdempotencyKey, "alias"} {
		r := req
		r.IdempotencyKey = key
		got, err := p.Process(ctx, r)
		must(t, err)
		assertBalance(t, got, 2000)
		if !got.IdempotentReplay || got.TransactionID != original.TransactionID {
			t.Fatal(got)
		}
		var bound string
		must(t, pool.QueryRow(ctx, `SELECT transaction_id FROM financial_idempotency_keys WHERE provider_id=$1 AND idempotency_key=$2`, r.ProviderID, key).Scan(&bound))
		if bound != original.TransactionID {
			t.Fatal(bound)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*financial.ProcessRequest)
	}{
		{"same key other identity", func(r *financial.ProcessRequest) { r.ExternalTransactionID = "other" }},
		{"same key changed payload", func(r *financial.ProcessRequest) { r.GameID = "other" }},
		{"alias reused", func(r *financial.ProcessRequest) { r.IdempotencyKey = "alias"; r.ExternalTransactionID = "other" }},
		{"new key changed payload", func(r *financial.ProcessRequest) { r.IdempotencyKey = "bad-alias"; r.Money = testMoney(t, 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req
			tc.change(&r)
			if _, err := p.Process(ctx, r); !errors.Is(err, financial.ErrConflict) {
				t.Fatal(err)
			}
		})
	}
	if count(t, pool, "wager_transactions") != 2 || count(t, pool, "wallet_ledger_entries") != 2 || count(t, pool, "financial_idempotency_keys") != 3 {
		t.Fatal("duplicate effects")
	}
	var after time.Time
	must(t, pool.QueryRow(ctx, `SELECT updated_at FROM wager_transactions WHERE id=$1`, original.TransactionID).Scan(&after))
	if !after.Equal(updated) {
		t.Fatal("replay changed terminal time")
	}
	rejection := request(t, "reject", "w", wt.BET, 9000)
	rejected, err := p.Process(ctx, rejection)
	must(t, err)
	assertBalance(t, rejected, 7000)
	_, err = p.Process(ctx, request(t, "fund", "w", wt.WIN, 9000))
	must(t, err)
	replay, err := p.Process(ctx, rejection)
	must(t, err)
	assertBalance(t, replay, 7000)
	if replay.Status != wt.REJECTED || !replay.IdempotentReplay {
		t.Fatal(replay)
	}
	// Identical external ID and key in a different provider are independent.
	other := req
	other.ProviderID = "other-provider"
	result, err := p.Process(ctx, other)
	must(t, err)
	if result.IdempotentReplay || result.TransactionID == original.TransactionID {
		t.Fatal(result)
	}
}

type faultRunner struct {
	runner *Runner
	stage  string
}

func (f faultRunner) WithinFinancialTransaction(ctx context.Context, work func(financial.Repositories) error) error {
	return f.runner.WithinFinancialTransaction(ctx, func(r financial.Repositories) error {
		switch f.stage {
		case "identity":
			r.Transactions = failedIdentity{r.Transactions}
		case "binding":
			r.Keys = failedBinding{r.Keys}
		case "wallet":
			r.Wallets = failedWallets{r.Wallets}
		case "ledger":
			r.Ledger = failedLedger{r.Ledger}
		case "outcome":
			r.Transactions = failedOutcome{r.Transactions}
		}
		return work(r)
	})
}

var injected = errors.New("injected persistence failure")

type failedIdentity struct{ financial.Transactions }

func (f failedIdentity) TryInsertExternal(ctx context.Context, t *wt.WagerTransaction, h [32]byte) (bool, error) {
	inserted, err := f.Transactions.TryInsertExternal(ctx, t, h)
	if err != nil {
		return false, err
	}
	if !inserted {
		return false, errors.New("fault injection expected a new identity")
	}
	return true, injected
}

type failedBinding struct{ financial.Keys }

func (f failedBinding) Bind(ctx context.Context, p, k, id string) error {
	if err := f.Keys.Bind(ctx, p, k, id); err != nil {
		return err
	}
	return injected
}

type failedWallets struct{ financial.Wallets }

func (f failedWallets) UpdateBalance(ctx context.Context, w wallet.Wallet, v int64) error {
	if err := f.Wallets.UpdateBalance(ctx, w, v); err != nil {
		return err
	}
	return injected
}

type failedLedger struct{ financial.Ledger }

func (f failedLedger) Insert(ctx context.Context, e ledger.Entry) error {
	if err := f.Ledger.Insert(ctx, e); err != nil {
		return err
	}
	return injected
}

type failedOutcome struct{ financial.Transactions }

func (f failedOutcome) CompleteOutcome(ctx context.Context, tx *wt.WagerTransaction, s wt.Status, b money.Money) error {
	if err := f.Transactions.CompleteOutcome(ctx, tx, s, b); err != nil {
		return err
	}
	return injected
}

func TestFinancialRollback(t *testing.T) {
	for _, stage := range []string{"identity", "binding", "wallet", "ledger", "outcome"} {
		t.Run(stage, func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			w := testWallet(t, "w", 10000)
			must(t, NewWalletRepository(pool).Insert(ctx, w))
			p := newProcessor(t, faultRunner{NewRunner(pool), stage})
			got, err := p.Process(ctx, request(t, "op", "w", wt.BET, 8000))
			if !errors.Is(err, injected) || got != (financial.ProcessResult{}) {
				t.Fatal(got, err)
			}
			after, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			if after != w {
				t.Fatal("wallet update survived rollback")
			}
			for _, table := range []string{"wager_transactions", "financial_idempotency_keys", "wallet_ledger_entries"} {
				if count(t, pool, table) != 0 {
					t.Fatal(table)
				}
			}
		})
	}
}

func TestLedgerDatabaseConstraints(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	wr := NewWalletRepository(pool)
	must(t, wr.Insert(ctx, testWallet(t, "w", 10000)))
	must(t, wr.Insert(ctx, testWallet(t, "other", 10000)))
	p := newProcessor(t, NewRunner(pool))
	result, err := p.Process(ctx, request(t, "op", "w", wt.BET, 8000))
	must(t, err)
	beforeLedger := ledgerSnapshot(t, pool)
	for _, tc := range []struct{ name, sql, code string }{
		{"duplicate", `INSERT INTO wallet_ledger_entries SELECT 'dup',wallet_id,transaction_id,direction,amount_minor,currency,balance_before_minor,balance_after_minor,created_at FROM wallet_ledger_entries`, "23505"},
		{"arithmetic", `INSERT INTO wallet_ledger_entries SELECT 'bad',wallet_id,transaction_id,direction,amount_minor,currency,balance_before_minor,42,created_at FROM wallet_ledger_entries`, "23514"},
		{"association", `INSERT INTO wallet_ledger_entries SELECT 'bad','other',transaction_id,direction,amount_minor,currency,balance_before_minor,balance_after_minor,created_at FROM wallet_ledger_entries`, "23503"},
		{"negative amount", `INSERT INTO wallet_ledger_entries SELECT 'bad',wallet_id,transaction_id,direction,-1,currency,balance_before_minor,balance_after_minor,created_at FROM wallet_ledger_entries`, "23514"},
		{"update", `UPDATE wallet_ledger_entries SET created_at=created_at + interval '1 second'`, "23514"},
		{"delete", `DELETE FROM wallet_ledger_entries`, "23514"},
		{"truncate", `TRUNCATE wallet_ledger_entries`, "23514"},
		{"key reassignment", `UPDATE financial_idempotency_keys SET idempotency_key='other'`, "23514"},
		{"key delete", `DELETE FROM financial_idempotency_keys`, "23514"},
		{"key truncate", `TRUNCATE financial_idempotency_keys`, "23514"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.sql)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tc.code {
				t.Fatalf("want %s: %v", tc.code, err)
			}
			if tc.name == "update" || tc.name == "delete" || tc.name == "truncate" {
				if pgErr.Message != "wallet_ledger_entries is append-only" {
					t.Fatal("wrong rejection cause", pgErr.Message)
				}
				if got := ledgerSnapshot(t, pool); got != beforeLedger {
					t.Fatal("ledger changed after rejected mutation")
				}
			}
		})
	}
	if count(t, pool, "wallet_ledger_entries") != 1 {
		t.Fatal("ledger changed")
	}
	// Verify the original transaction still has its original replay result.
	replay, err := p.Process(ctx, request(t, "op", "w", wt.BET, 8000))
	must(t, err)
	if replay.TransactionID != result.TransactionID {
		t.Fatal(replay)
	}
}

type concurrentResult struct {
	result financial.ProcessResult
	err    error
}

func runConcurrent(t *testing.T, pool *pgxpool.Pool, requests []financial.ProcessRequest) []concurrentResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := make(chan struct{})
	done := make(chan concurrentResult, len(requests))
	for _, req := range requests {
		go func() {
			<-start
			r, err := newProcessor(t, NewRunner(pool)).Process(ctx, req)
			done <- concurrentResult{r, err}
		}()
	}
	close(start)
	results := make([]concurrentResult, 0, len(requests))
	// Every worker is joined before the pool cleanup runs, including error paths.
	for range requests {
		results = append(results, <-done)
	}
	for _, r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
	}
	return results
}
func TestFinancialConcurrency(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		initial, amount                            int64
		requests, processed, rejected, ledgerCount int
		final                                      int64
		duplicate                                  bool
	}{
		{"two bets", 10000, 8000, 2, 1, 1, 1, 2000, false},
		{"many", 2500, 100, 50, 25, 25, 25, 0, false},
		{"duplicates", 10000, 8000, 50, 50, 0, 1, 2000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := testPool(t)
			ctx := testContext(t)
			must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", tc.initial)))
			requests := make([]financial.ProcessRequest, tc.requests)
			for i := range requests {
				id := fmt.Sprintf("op-%d", i)
				if tc.duplicate {
					id = "same"
				}
				requests[i] = request(t, id, "w", wt.BET, tc.amount)
			}
			results := runConcurrent(t, pool, requests)
			processed, rejected, replays := 0, 0, 0
			for _, r := range results {
				if r.result.Status == wt.PROCESSED {
					processed++
				}
				if r.result.Status == wt.REJECTED {
					rejected++
				}
				if r.result.IdempotentReplay {
					replays++
				}
			}
			if processed != tc.processed || rejected != tc.rejected {
				t.Fatal(processed, rejected)
			}
			if tc.duplicate && replays != tc.requests-1 {
				t.Fatal(replays)
			}
			w, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			n, _ := w.Balance().MinorUnits()
			if n != tc.final || w.Version() != int64(tc.ledgerCount+1) {
				t.Fatal(n, w.Version())
			}
			if count(t, pool, "wallet_ledger_entries") != tc.ledgerCount {
				t.Fatal("ledger count")
			}
			var negative, duplicates int
			must(t, pool.QueryRow(ctx, `SELECT count(*) FROM wallets WHERE balance_minor<0`).Scan(&negative))
			must(t, pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT wallet_id,transaction_id FROM wallet_ledger_entries GROUP BY 1,2 HAVING count(*)>1) d`).Scan(&duplicates))
			if negative != 0 || duplicates != 0 {
				t.Fatal(negative, duplicates)
			}
			for _, req := range requests {
				_, err := newProcessor(t, NewRunner(pool)).Process(ctx, req)
				must(t, err)
			}
			after, err := NewWalletRepository(pool).GetByID(ctx, "w")
			must(t, err)
			if after != w {
				t.Fatal("resends changed wallet")
			}
		})
	}
}

func TestIndependentWallets(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	wr := NewWalletRepository(pool)
	must(t, wr.Insert(ctx, testWallet(t, "a", 100)))
	must(t, wr.Insert(ctx, testWallet(t, "b", 100)))
	first, err := pool.Begin(ctx)
	must(t, err)
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = first.Rollback(c)
	}()
	_, err = first.Exec(ctx, `SELECT id FROM wallets WHERE id='a' FOR UPDATE`)
	must(t, err)
	waiterCtx, cancel := context.WithCancel(ctx)
	done := make(chan concurrentResult, 1)
	joined := make(chan struct{})
	defer func() { cancel(); <-joined }()
	reqA := request(t, "a", "a", wt.BET, 1)
	go func() {
		defer close(joined)
		r, err := newProcessor(t, NewRunner(pool)).Process(waiterCtx, reqA)
		done <- concurrentResult{r, err}
	}()
	pid := int(first.Conn().PgConn().PID())
	for {
		var blocked bool
		must(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked))
		if blocked {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("waiter did not block: %v", r.err)
		default:
		}
	}
	b, err := newProcessor(t, NewRunner(pool)).Process(ctx, request(t, "b", "b", wt.BET, 1))
	must(t, err)
	assertBalance(t, b, 99)
	must(t, first.Commit(ctx))
	a := <-done
	must(t, a.err)
	assertBalance(t, a.result, 99)
}

func TestFinancialMigrationRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	down4, err := os.ReadFile("../../../migrations/000004_references.down.sql")
	must(t, err)
	_, err = pool.Exec(ctx, string(down4))
	must(t, err)
	down, err := os.ReadFile("../../../migrations/000003_financial_core.down.sql")
	must(t, err)
	up, err := os.ReadFile("../../../migrations/000003_financial_core.up.sql")
	must(t, err)
	_, err = pool.Exec(ctx, string(down))
	must(t, err)
	var exists bool
	must(t, pool.QueryRow(ctx, `SELECT to_regclass('wallet_ledger_entries') IS NOT NULL`).Scan(&exists))
	if exists {
		t.Fatal("ledger survived down")
	}
	_, err = pool.Exec(ctx, string(up))
	must(t, err)
	must(t, pool.QueryRow(ctx, `SELECT to_regclass('wallet_ledger_entries') IS NOT NULL`).Scan(&exists))
	if !exists {
		t.Fatal("ledger absent after up")
	}
	// A populated old dataset must fail rather than invent saved results.
	_, err = pool.Exec(ctx, string(down))
	must(t, err)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 100)))
	_, err = pool.Exec(ctx, `INSERT INTO wager_transactions(id,provider_id,external_transaction_id,player_id,wallet_id,round_id,game_id,kind,amount_minor,currency,status,created_at,updated_at) VALUES('t','p','e','player-w','w','r','g','BET',1,'BRL','PENDING',now(),now())`)
	must(t, err)
	conn, err := pool.Acquire(ctx)
	must(t, err)
	defer conn.Release()
	_, err = conn.Exec(ctx, string(up))
	if err == nil || !strings.Contains(err.Error(), "empty wager_transactions") {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, "ROLLBACK")
	must(t, err)
}

func TestConcurrentAliases(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	requests := make([]financial.ProcessRequest, 20)
	for i := range requests {
		requests[i] = request(t, "same", "w", wt.BET, 8000)
		requests[i].IdempotencyKey = fmt.Sprintf("alias-%d", i)
	}
	results := runConcurrent(t, pool, requests)
	id := results[0].result.TransactionID
	for _, r := range results {
		if r.result.TransactionID != id {
			t.Fatal("different identities")
		}
		assertBalance(t, r.result, 2000)
	}
	if count(t, pool, "financial_idempotency_keys") != len(requests) || count(t, pool, "wallet_ledger_entries") != 1 {
		t.Fatal("alias effects")
	}
}

func TestReplayDoesNotLockWallet(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	p := newProcessor(t, NewRunner(pool))
	req := request(t, "op", "w", wt.BET, 8000)
	_, err := p.Process(ctx, req)
	must(t, err)
	tx, err := pool.Begin(ctx)
	must(t, err)
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(c)
	}()
	_, err = tx.Exec(ctx, `SELECT id FROM wallets WHERE id='w' FOR UPDATE`)
	must(t, err)
	req.IdempotencyKey = "new-alias"
	replay, err := p.Process(ctx, req)
	must(t, err)
	assertBalance(t, replay, 2000)
	if !replay.IdempotentReplay {
		t.Fatal(replay)
	}
}

func TestDatabaseFailureRollsBackFinancialOperation(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	w := testWallet(t, "w", 10000)
	must(t, NewWalletRepository(pool).Insert(ctx, w))
	_, err := pool.Exec(ctx, `CREATE FUNCTION fail_ledger_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected database failure'; END $$; CREATE TRIGGER fail_insert BEFORE INSERT ON wallet_ledger_entries FOR EACH STATEMENT EXECUTE FUNCTION fail_ledger_insert()`)
	must(t, err)
	result, err := newProcessor(t, NewRunner(pool)).Process(ctx, request(t, "op", "w", wt.BET, 8000))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || result != (financial.ProcessResult{}) {
		t.Fatal(result, err)
	}
	after, err := NewWalletRepository(pool).GetByID(ctx, "w")
	must(t, err)
	if after != w {
		t.Fatal("wallet changed")
	}
	for _, table := range []string{"wager_transactions", "financial_idempotency_keys", "wallet_ledger_entries"} {
		if count(t, pool, table) != 0 {
			t.Fatal(table)
		}
	}
}

func newProcessor(t *testing.T, tr financial.Transactor) *financial.Processor {
	t.Helper()
	p, err := financial.NewProcessor(tr, 24*time.Hour, time.Now)
	must(t, err)
	return p
}
