//go:build integration

package postgres

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
)

type processCommand struct{ ID, Wallet string }
type processReply struct {
	Status  wt.Status
	Replay  bool
	Balance int64
	Error   string
	Code    wt.FailureCode
}

func TestFinancialProcessChild(t *testing.T) {
	if os.Getenv("WAGER_CHILD") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	must(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = os.Getenv("WAGER_SCHEMA")
	cfg.ConnConfig.RuntimeParams["application_name"] = os.Getenv("WAGER_SCHEMA")
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	must(t, err)
	defer pool.Close()
	must(t, pool.Ping(ctx))
	p, err := financial.NewProcessor(NewRunner(pool), time.Hour, time.Now)
	must(t, err)
	workerCtx, stopWorker := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() { defer workers.Done(); (&ReferenceWorker{pool, p}).Run(workerCtx) }()
	defer func() { stopWorker(); workers.Wait() }()
	fmt.Println("READY")
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var c processCommand
		if err := dec.Decode(&c); err == io.EOF {
			return
		} else {
			must(t, err)
		}
		r, e := p.Process(ctx, request(t, c.ID, c.Wallet, wt.BET, 8000))
		reply := processReply{Status: r.Status, Replay: r.IdempotentReplay, Code: r.FailureCode}
		if e != nil {
			reply.Error = "processing failed"
		}
		if r.Balance != nil {
			reply.Balance, _ = r.Balance.MinorUnits()
		}
		must(t, enc.Encode(reply))
	}
}

type childProcess struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Reader
	done   chan struct{}
	err    error
}

func startFinancialChild(t *testing.T, ctx context.Context, schema string) *childProcess {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFinancialProcessChild$")
	cmd.Env = append(os.Environ(), "WAGER_CHILD=1", "WAGER_SCHEMA="+schema)
	input, err := cmd.StdinPipe()
	must(t, err)
	out, err := cmd.StdoutPipe()
	must(t, err)
	cmd.Stderr = os.Stderr
	must(t, cmd.Start())
	c := &childProcess{cmd: cmd, input: input, output: bufio.NewReader(out), done: make(chan struct{})}
	go func() { c.err = cmd.Wait(); close(c.done) }()
	t.Cleanup(func() {
		input.Close()
		select {
		case <-c.done:
			if c.err != nil {
				t.Error(c.err)
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-c.done
			t.Error("child failed to stop")
		}
	})
	line, err := c.output.ReadString('\n')
	must(t, err)
	if line != "READY\n" {
		t.Fatal("child not ready", line)
	}
	return c
}
func (c *childProcess) send(t *testing.T, id, w string) {
	t.Helper()
	must(t, json.NewEncoder(c.input).Encode(processCommand{id, w}))
}
func (c *childProcess) receive(t *testing.T) processReply {
	t.Helper()
	line, err := c.output.ReadBytes('\n')
	must(t, err)
	var r processReply
	must(t, json.Unmarshal(line, &r))
	if r.Error != "" {
		t.Fatal(r.Error)
	}
	return r
}
func TestThreeIndependentProcesses(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "other", 10000)))
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]
	children := []*childProcess{startFinancialChild(t, ctx, schema), startFinancialChild(t, ctx, schema), startFinancialChild(t, ctx, schema)}
	// Hold the wallet until both competing sessions are waiting in PostgreSQL.
	lock, err := pool.Begin(ctx)
	must(t, err)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = lock.Rollback(cleanup)
	}()
	_, err = lock.Exec(ctx, `SELECT id FROM wallets WHERE id='w' FOR UPDATE`)
	must(t, err)
	children[0].send(t, "a", "w")
	children[1].send(t, "b", "w")
	children[2].send(t, "independent", "other")
	if r := children[2].receive(t); r.Status != wt.PROCESSED {
		t.Fatal(r)
	}
	// Observe lock waits, not elapsed time, before releasing the barrier.
	for {
		var n int
		must(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%wallets%' AND query LIKE '%FOR UPDATE%' AND application_name=$1`, schema).Scan(&n))
		if n >= 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
	}
	must(t, lock.Commit(ctx))
	a, b := children[0].receive(t), children[1].receive(t)
	if !((a.Status == wt.PROCESSED && b.Status == wt.REJECTED) || (b.Status == wt.PROCESSED && a.Status == wt.REJECTED)) || a.Balance != 2000 || b.Balance != 2000 {
		t.Fatal(a, b)
	}
	if (a.Status == wt.REJECTED && a.Code != wt.BetInsufficientFunds) || (b.Status == wt.REJECTED && b.Code != wt.BetInsufficientFunds) {
		t.Fatal(a, b)
	}
	children[2].send(t, "a", "w")
	if r := children[2].receive(t); !r.Replay || r.Status != a.Status || r.Balance != a.Balance {
		t.Fatal(r)
	}
	// Stop one OS process and replay through its replacement with a fresh pool.
	children[0].input.Close()
	replacement := startFinancialChild(t, ctx, schema)
	replacement.send(t, "b", "w")
	if r := replacement.receive(t); !r.Replay || r.Status != b.Status || r.Balance != b.Balance {
		t.Fatal(r)
	}
	var balance, sum int64
	var debits int
	must(t, pool.QueryRow(ctx, `SELECT balance_minor FROM wallets WHERE id='w'`).Scan(&balance))
	must(t, pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(amount_minor),0) FROM wallet_ledger_entries WHERE wallet_id='w' AND direction='DEBIT'`).Scan(&debits, &sum))
	if balance != 2000 || debits != 1 || sum != 8000 {
		t.Fatal(balance, debits, sum)
	}
}

func TestReferenceWorkerProcessRestart(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	p, err := financial.NewProcessor(NewRunner(pool), time.Hour, time.Now)
	must(t, err)
	pending := processOK(t, p, referenceRequest(t, "pending-process", wt.REFUND, 8000, "original-process"))
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]
	child := startFinancialChild(t, ctx, schema)
	for {
		var attempts int
		must(t, pool.QueryRow(ctx, `SELECT reference_attempts FROM wager_transactions WHERE id=$1`, pending.TransactionID).Scan(&attempts))
		if attempts > 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
	}
	child.input.Close()
	select {
	case <-child.done:
		must(t, child.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var before int
	must(t, pool.QueryRow(ctx, `SELECT reference_attempts FROM wager_transactions WHERE id=$1`, pending.TransactionID).Scan(&before))
	processOK(t, p, request(t, "original-process", "w", wt.BET, 8000))
	// Expire the persisted schedule deliberately; restart must discover it itself.
	_, err = pool.Exec(ctx, `UPDATE wager_transactions SET reference_next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1`, pending.TransactionID)
	must(t, err)
	startFinancialChild(t, ctx, schema)
	for {
		var status string
		var attempts int
		must(t, pool.QueryRow(ctx, `SELECT status,reference_attempts FROM wager_transactions WHERE id=$1`, pending.TransactionID).Scan(&status, &attempts))
		if status == "PROCESSED" {
			if attempts <= before {
				t.Fatal("attempt history reset")
			}
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
	}
	if count(t, pool, "wallet_ledger_entries") != 2 || count(t, pool, "outbox_events") != 5 {
		t.Fatal("restart effects")
	}
}
