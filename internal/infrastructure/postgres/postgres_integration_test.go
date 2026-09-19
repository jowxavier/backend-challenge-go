//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/config"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
	"go.uber.org/fx"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("test_%x", randomBytes(t))
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		defer admin.Close()
		if _, err := admin.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	cfg.MaxConns = 6
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	files, err := filepath.Glob("../../../migrations/*.up.sql")
	if err != nil || len(files) != 8 {
		t.Fatalf("migrations: %v %v", files, err)
	}
	for _, file := range files {
		sql, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}
func randomBytes(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

var testTime = time.Date(2026, 9, 18, 12, 0, 0, 123456000, time.UTC)

func testMoney(t *testing.T, n int64) money.Money {
	t.Helper()
	m, err := money.FromMinorUnits(n, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func testWallet(t *testing.T, id string, n int64) wallet.Wallet {
	t.Helper()
	w, err := wallet.New(id, "player-"+id, "BRL", testMoney(t, n), testTime)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func testWager(t *testing.T, id, walletID string) *wt.WagerTransaction {
	t.Helper()
	tx, err := wt.NewExternal(wt.ExternalInput{ID: id, ProviderID: "provider", ExternalTransactionID: "external-" + id, PlayerID: "player-" + walletID, WalletID: walletID, RoundID: "round", GameID: "game", Kind: wt.BET, Money: testMoney(t, 1)}, testTime)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestRepositories(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	wr := NewWalletRepository(pool)
	tr := NewWagerTransactionRepository(pool)
	for _, n := range []int64{0, 1, math.MaxInt64} {
		w := testWallet(t, fmt.Sprintf("wallet-%d", n), n)
		must(t, wr.Insert(ctx, w))
		got, err := wr.GetByID(ctx, w.ID())
		must(t, err)
		if got != w {
			t.Fatalf("wallet mismatch: %+v %+v", got, w)
		}
	}
	w := testWallet(t, "main", 10000)
	must(t, wr.Insert(ctx, w))
	for _, status := range []wt.Status{wt.PENDING, wt.PENDING_REFERENCE, wt.PROCESSED, wt.REJECTED, wt.FAILED} {
		tx := testWager(t, string(status), w.ID())
		if status == wt.PENDING_REFERENCE {
			var err error
			tx, err = wt.NewExternal(wt.ExternalInput{ID: string(status), ProviderID: "provider", ExternalTransactionID: "external-" + string(status), PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round", GameID: "game", Kind: wt.REFUND, Money: testMoney(t, 1), ReferenceExternalTransactionID: "missing"}, testTime)
			must(t, err)

		}
		must(t, tr.Insert(ctx, tx))
		if status == wt.PENDING_REFERENCE {
			must(t, tx.WaitForReference(testTime.Add(time.Second)))
		}
		if status == wt.PROCESSED {
			must(t, tx.MarkProcessed(testTime.Add(time.Second)))
		}
		if status == wt.REJECTED {
			must(t, tx.Reject(wt.BetInsufficientFunds, testTime.Add(time.Second)))
		}
		if status == wt.FAILED {
			must(t, tx.Fail(wt.PermanentInfrastructureFailure, testTime.Add(time.Second)))
		}
		if status == wt.PROCESSED || status == wt.REJECTED {
			must(t, tr.CompleteOutcome(ctx, tx, wt.PENDING, w.Balance()))
		} else if status == wt.PENDING_REFERENCE {
			must(t, tr.MarkPendingReference(ctx, tx, testTime.Add(24*time.Hour)))
		} else if status != wt.PENDING {
			must(t, tr.UpdateOutcome(ctx, tx, wt.PENDING))
		}
		got, err := tr.GetByID(ctx, tx.ID())
		must(t, err)
		if *got != *tx {
			t.Fatal("transaction round trip mismatch")
		}
		got, err = tr.GetByFinancialIdentity(ctx, tx.ProviderID(), tx.ExternalTransactionID())
		must(t, err)
		if *got != *tx {
			t.Fatal("financial identity mismatch")
		}
	}
	_, err := wr.GetByID(ctx, "absent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	_, err = tr.GetByID(ctx, "absent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	_, err = tr.GetByFinancialIdentity(ctx, "other-provider", "external-PENDING")
	if !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	_, err = wr.GetForUpdate(ctx, w.ID())
	if !errors.Is(err, ErrTransactionRequired) {
		t.Fatal(err)
	}
	dup, err := wallet.New("different-id", w.PlayerID(), "BRL", w.Balance(), testTime)
	must(t, err)
	err = wr.Insert(ctx, dup)
	if !errors.Is(err, ErrDuplicateWalletIdentity) {
		t.Fatal(err)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "wallets_player_currency_unique" {
		t.Fatal("lost postgres cause")
	}
	// A primary-key collision is not a player/currency conflict.
	pk, err := wallet.New(w.ID(), "other-player", "BRL", w.Balance(), testTime)
	must(t, err)
	err = wr.Insert(ctx, pk)
	if err == nil || errors.Is(err, ErrDuplicateWalletIdentity) {
		t.Fatal("misclassified primary key", err)
	}
	tx := testWager(t, "dup", w.ID())
	must(t, tr.Insert(ctx, tx))
	other, err := wt.NewExternal(wt.ExternalInput{ID: "another", ProviderID: tx.ProviderID(), ExternalTransactionID: tx.ExternalTransactionID(), PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "r", GameID: "g", Kind: wt.WIN, Money: testMoney(t, 1)}, testTime)
	must(t, err)
	if err := tr.Insert(ctx, other); !errors.Is(err, ErrDuplicateFinancialIdentity) {
		t.Fatal(err)
	}
	must(t, w.Debit(testMoney(t, 1), testTime.Add(time.Second)))
	must(t, wr.UpdateBalance(ctx, w, 1))
	if err := wr.UpdateBalance(ctx, w, 1); !errors.Is(err, ErrStaleWalletWrite) {
		t.Fatal(err)
	}
	gotW, err := wr.GetByID(ctx, w.ID())
	must(t, err)
	if gotW != w {
		t.Fatal("wallet update changed wrong fields")
	}
	must(t, tx.Reject(wt.BetInsufficientFunds, testTime.Add(time.Second)))
	must(t, tr.CompleteOutcome(ctx, tx, wt.PENDING, w.Balance()))
	if err := tr.CompleteOutcome(ctx, tx, wt.PENDING, w.Balance()); !errors.Is(err, ErrStaleOutcomeWrite) {
		t.Fatal(err)
	}
	gotT, err := tr.GetByID(ctx, tx.ID())
	must(t, err)
	if *gotT != *tx {
		t.Fatal("outcome update changed wrong fields")
	}
}

func TestRunner(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	runner := NewRunner(pool)
	sentinel := errors.New("callback failed")
	for _, rollback := range []bool{false, true} {
		id := fmt.Sprintf("wallet-%t", rollback)
		w := testWallet(t, id, 1)
		tx := testWager(t, id, id)
		calls := 0
		err := runner.WithinTransaction(ctx, func(r *Repositories) error {
			calls++
			if r.Wallets.db != r.WagerTransactions.db {
				t.Fatal("different transactions")
			}
			var isolation string
			must(t, r.Wallets.tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&isolation))
			if isolation != "read committed" {
				t.Fatal(isolation)
			}
			if err := r.Wallets.Insert(ctx, w); err != nil {
				return err
			}
			if err := r.WagerTransactions.Insert(ctx, tx); err != nil {
				return err
			}
			_, err := r.Wallets.GetForUpdate(ctx, id)
			if err != nil {
				return err
			}
			if rollback {
				return sentinel
			}
			return nil
		})
		if calls != 1 {
			t.Fatal("callback retried")
		}
		if rollback {
			if !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
		} else {
			must(t, err)
		}
		_, we := NewWalletRepository(pool).GetByID(ctx, id)
		_, te := NewWagerTransactionRepository(pool).GetByID(ctx, id)
		if rollback {
			if !errors.Is(we, ErrNotFound) || !errors.Is(te, ErrNotFound) {
				t.Fatal("partial commit", we, te)
			}
		} else {
			must(t, we)
			must(t, te)
		}
	}
	t.Run("cancel", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		err := runner.WithinTransaction(canceled, func(r *Repositories) error {
			if err := r.Wallets.Insert(canceled, testWallet(t, "cancel", 1)); err != nil {
				return err
			}
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		_, err = NewWalletRepository(pool).GetByID(ctx, "cancel")
		if !errors.Is(err, ErrNotFound) {
			t.Fatal("canceled work persisted", err)
		}
	})
	t.Run("commit failure", func(t *testing.T) {
		// PostgreSQL accepts the write but rejects this deferred constraint at commit.
		_, err := pool.Exec(ctx, "CREATE TABLE deferred_test (n int UNIQUE DEFERRABLE INITIALLY DEFERRED)")
		must(t, err)
		calls := 0
		err = runner.WithinTransaction(ctx, func(r *Repositories) error {
			calls++
			_, err := r.Wallets.tx.Exec(ctx, "INSERT INTO deferred_test VALUES (1),(1)")
			return err
		})
		var pgErr *pgconn.PgError
		if err == nil || !strings.Contains(err.Error(), "commit transaction") || !errors.As(err, &pgErr) || pgErr.Code != "23505" || calls != 1 {
			t.Fatal(err)
		}
	})
	t.Run("rollback failure preserves cause", func(t *testing.T) {
		err := runner.WithinTransaction(ctx, func(r *Repositories) error { must(t, r.Wallets.tx.Conn().Close(ctx)); return sentinel })
		if !errors.Is(err, sentinel) {
			t.Error("lost callback error", err)
		}
		if err == nil || !strings.Contains(err.Error(), "rollback transaction:") {
			t.Error("lost rollback diagnostic", err)
		}
	})
	t.Run("panic cleanup", func(t *testing.T) {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("expected panic")
				}
			}()
			_ = runner.WithinTransaction(ctx, func(r *Repositories) error { must(t, r.Wallets.Insert(ctx, testWallet(t, "panic", 1))); panic("stop") })
		}()
		_, err := NewWalletRepository(pool).GetByID(ctx, "panic")
		if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	})
}

func TestRowLock(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	w := testWallet(t, "locked", 100)
	must(t, NewWalletRepository(pool).Insert(ctx, w))
	first, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	must(t, err)
	rollback := func(tx pgx.Tx) {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Error("cleanup rollback:", err)
		}
	}
	defer rollback(first)
	second, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	must(t, err)
	defer rollback(second)
	firstRepo := &WalletRepository{db: first, tx: first}
	secondRepo := &WalletRepository{db: second, tx: second}
	locked, err := firstRepo.GetForUpdate(ctx, w.ID())
	must(t, err)
	pid1, pid2 := first.Conn().PgConn().PID(), second.Conn().PgConn().PID()
	if pid1 == pid2 {
		t.Fatal("connections not independent")
	}
	waiterCtx, cancelWaiter := context.WithCancel(ctx)
	started := make(chan struct{})
	done := make(chan error, 1)
	joined := make(chan struct{})
	// Join before the deferred rollback can reuse the waiter connection.
	defer func() {
		cancelWaiter()
		<-joined
	}()
	go func() {
		defer close(joined)
		close(started)
		got, err := secondRepo.GetForUpdate(waiterCtx, w.ID())
		if err == nil && got.Version() != 2 {
			err = fmt.Errorf("waiter saw version %d", got.Version())
		}
		done <- err
	}()
	<-started
	// Observe an actual PostgreSQL lock dependency, not elapsed time.
	for {
		var blocked bool
		must(t, pool.QueryRow(ctx, "SELECT $1::int = ANY(pg_blocking_pids($2::int))", int(pid1), int(pid2)).Scan(&blocked))
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("lock did not block: %v", err)
		default:
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
	}
	must(t, locked.Credit(testMoney(t, 1), testTime.Add(time.Second)))
	must(t, firstRepo.UpdateBalance(ctx, locked, 1))
	must(t, first.Commit(ctx))
	select {
	case err := <-done:
		must(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	must(t, second.Commit(ctx))
}

func TestInvalidPersistedData(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	w := testWallet(t, "valid", 1)
	must(t, NewWalletRepository(pool).Insert(ctx, w))
	tx := testWager(t, "valid", w.ID())
	must(t, NewWagerTransactionRepository(pool).Insert(ctx, tx))
	// Whitespace inside an ID is allowed by the minimal schema, but not the domain.
	_, err := pool.Exec(ctx, "UPDATE wallets SET player_id='bad player' WHERE id='valid'")
	must(t, err)
	_, err = NewWalletRepository(pool).GetByID(ctx, w.ID())
	if !errors.Is(err, ErrInvalidPersistedData) || !errors.Is(err, wallet.ErrInvalidPlayerID) {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, "UPDATE wager_transactions SET game_id='bad game' WHERE id='valid'")
	must(t, err)
	_, err = NewWagerTransactionRepository(pool).GetByID(ctx, tx.ID())
	if !errors.Is(err, ErrInvalidPersistedData) || !errors.Is(err, wt.ErrInvalidField) {
		t.Fatal(err)
	}
}

func TestPoolLifecycle(t *testing.T) {
	_ = testPool(t)
	cfg := &config.Config{DatabaseURL: os.Getenv("TEST_DATABASE_URL")}
	var pool *pgxpool.Pool
	app := fx.New(fx.NopLogger, fx.Supply(cfg), Module, fx.Populate(&pool))
	ctx := testContext(t)
	must(t, app.Start(ctx))
	must(t, pool.Ping(ctx))
	must(t, app.Stop(ctx))
	if err := pool.Ping(ctx); err == nil {
		t.Fatal("pool still open")
	}
	bad, err := url.Parse(cfg.DatabaseURL)
	must(t, err)
	bad.Path = "/missing_" + fmt.Sprintf("%x", randomBytes(t))
	cfg = &config.Config{DatabaseURL: bad.String()}
	failed := fx.New(fx.NopLogger, fx.Supply(cfg), Module)
	if err := failed.Start(ctx); err == nil {
		_ = failed.Stop(ctx)
		t.Fatal("startup accepted missing database")
	}
}

func TestCorruptMoneyAndTimestamp(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	w := testWallet(t, "corrupt", 1)
	must(t, NewWalletRepository(pool).Insert(ctx, w))
	tx := testWager(t, "corrupt", w.ID())
	must(t, NewWagerTransactionRepository(pool).Insert(ctx, tx))
	for _, table := range []string{"wallets", "wager_transactions"} {
		t.Run(table, func(t *testing.T) {
			dbtx, err := pool.Begin(ctx)
			must(t, err)
			defer dbtx.Rollback(context.Background())
			// Simulate legacy/corrupted data without weakening the committed schema.
			_, err = dbtx.Exec(ctx, "ALTER TABLE "+table+" DROP CONSTRAINT "+table+"_currency_check")
			must(t, err)
			_, err = dbtx.Exec(ctx, "UPDATE "+table+" SET currency='brl'")
			must(t, err)
			if table == "wallets" {
				_, err = (&WalletRepository{db: dbtx}).GetByID(ctx, "corrupt")
			} else {
				_, err = (&WagerTransactionRepository{db: dbtx}).GetByID(ctx, "corrupt")
			}
			if !errors.Is(err, ErrInvalidPersistedData) || !errors.Is(err, money.ErrInvalidCurrency) {
				t.Fatalf("normalized corrupted currency: %v", err)
			}
			_, err = dbtx.Exec(ctx, "UPDATE "+table+" SET currency='BRL',created_at='infinity'")
			must(t, err)
			if table == "wallets" {
				_, err = (&WalletRepository{db: dbtx}).GetByID(ctx, "corrupt")
			} else {
				_, err = (&WagerTransactionRepository{db: dbtx}).GetByID(ctx, "corrupt")
			}
			if !errors.Is(err, ErrInvalidPersistedData) {
				t.Fatalf("invalid timestamp: %v", err)
			}
		})
	}
}
