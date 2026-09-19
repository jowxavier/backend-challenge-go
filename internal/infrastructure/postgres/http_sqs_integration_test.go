//go:build integration && sqsintegration

package postgres

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/consumer"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	sqsadapter "github.com/jowxavier/backend-challenge-go/internal/infrastructure/sqs"
	httpapi "github.com/jowxavier/backend-challenge-go/internal/interfaces/http"
)

func TestHTTPAndSQSConcurrentIdentity(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	f := testSQS(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	p := newProcessor(t, NewRunner(pool))
	api := httpapi.NewAPI(p, financial.NewWalletService(NewWalletAPIStore(pool), time.Now), NewWagerTransactionRepository(pool), httpVerifier{}, healthy{}).Handler()
	server := httptest.NewServer(api)
	defer server.Close()
	body := command(t, "cross", "w")
	sendCommand(t, f, body, "w", "cross")
	q := sqsadapter.NewQueue(f.client, f.input, time.Second, 30*time.Second)
	delivery := receiveCommand(t, q)
	worker, err := consumer.New(q, p, "cross", 10*time.Second)
	must(t, err)
	var envelope struct{ Data json.RawMessage }
	must(t, json.Unmarshal([]byte(body), &envelope))
	var fields map[string]json.RawMessage
	must(t, json.Unmarshal(envelope.Data, &fields))
	delete(fields, "idempotencyKey")
	payload, err := json.Marshal(fields)
	must(t, err)
	lock, err := pool.Begin(ctx)
	must(t, err)
	defer lock.Rollback(ctx)
	_, err = lock.Exec(ctx, `SELECT id FROM wallets WHERE id='w' FOR UPDATE`)
	must(t, err)
	sqsDone := make(chan error, 1)
	httpDone := make(chan error, 1)
	responses := make(chan map[string]json.RawMessage, 1)
	go func() { sqsDone <- worker.Handle(ctx, *delivery) }()
	go func() {
		r, e := http.NewRequestWithContext(ctx, "POST", server.URL+"/wagering/transactions", strings.NewReader(string(payload)))
		if e != nil {
			httpDone <- e
			return
		}
		r.Header.Set("Authorization", "Bearer provider")
		r.Header.Set("Idempotency-Key", "key-cross")
		response, e := server.Client().Do(r)
		if e != nil {
			httpDone <- e
			return
		}
		defer response.Body.Close()
		var data map[string]json.RawMessage
		e = json.NewDecoder(response.Body).Decode(&data)
		responses <- data
		httpDone <- e
	}()
	for {
		var n int
		must(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%wallets%' AND query LIKE '%FOR UPDATE%' AND application_name=$1`, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&n))
		if n >= 2 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
	}
	must(t, lock.Commit(ctx))
	must(t, <-sqsDone)
	must(t, <-httpDone)
	response := <-responses
	if value(t, response, "status") != "PROCESSED" {
		t.Fatal(response)
	}
	replay := httpCall(t, api, "POST", "/wagering/transactions", "provider", "key-cross", string(payload), 200)
	if string(replay["idempotentReplay"]) != "true" || value(t, replay, "transactionId") != value(t, response, "transactionId") || string(replay["balance"]) != string(response["balance"]) {
		t.Fatal("inconsistent replay")
	}
	httpCall(t, api, "GET", "/wagering/transactions/"+value(t, response, "transactionId"), "other", "", "", 404)
	if count(t, pool, "wallet_ledger_entries") != 1 || count(t, pool, "wager_transactions") != 1 || count(t, pool, "inbox_messages") != 1 {
		t.Fatal("duplicate effects")
	}
	w, err := NewWalletRepository(pool).GetByID(ctx, "w")
	must(t, err)
	n, _ := w.Balance().MinorUnits()
	if n != 9900 {
		t.Fatal(n)
	}
}
