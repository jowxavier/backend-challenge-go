//go:build integration && sqsintegration

package messaging

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/config"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
	"github.com/jowxavier/backend-challenge-go/internal/infrastructure/postgres"
	sqsadapter "github.com/jowxavier/backend-challenge-go/internal/infrastructure/sqs"
	"go.uber.org/fx"
)

func TestProductionWorkersLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url, endpoint := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_SQS_ENDPOINT")
	if url == "" || endpoint == "" {
		t.Fatal("TEST_DATABASE_URL and TEST_SQS_ENDPOINT required")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("runtime_%x", random)
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, err := admin.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{name}.Sanitize()+" CASCADE")
		if err != nil {
			t.Error(err)
		}
	}()
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.RuntimeParams["search_path"] = name
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	files, _ := filepath.Glob("../../../migrations/*.up.sql")
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Messaging: config.MessagingConfig{Endpoint: endpoint, Region: "us-east-1", InputQueue: name + ".fifo", OutputQueue: name + "-events", ConsumerName: "runtime", Concurrency: 2, Visibility: 60 * time.Second, Processing: 20 * time.Second, Polling: time.Second}}
	client, err := sqsadapter.NewClient(ctx, cfg.Messaging)
	if err != nil {
		t.Fatal(err)
	}
	create := func(n string, attrs map[string]string) string {
		q, err := client.CreateQueue(ctx, &sdk.CreateQueueInput{QueueName: &n, Attributes: attrs})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client.DeleteQueue(cleanup, &sdk.DeleteQueueInput{QueueUrl: q.QueueUrl})
		})
		return *q.QueueUrl
	}
	dlq := create(name+"-dlq.fifo", map[string]string{"FifoQueue": "true"})
	attrs, err := client.GetQueueAttributes(ctx, &sdk.GetQueueAttributesInput{QueueUrl: &dlq, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatal(err)
	}
	in := create(cfg.Messaging.InputQueue, map[string]string{"FifoQueue": "true", "RedrivePolicy": fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":5}`, attrs.Attributes["QueueArn"])})
	out := create(cfg.Messaging.OutputQueue, nil)
	balance, _ := money.Parse("100", "BRL")
	w, err := wallet.New("w", "p", "BRL", balance, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.NewWalletRepository(pool).Insert(ctx, w); err != nil {
		t.Fatal(err)
	}
	processor, err := financial.NewProcessor(postgres.NewRunner(pool), time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"messageId":"runtime","type":"WagerTransactionRequested","occurredAt":"2026-01-01T00:00:00Z","data":{"providerId":"provider","externalTransactionId":"runtime","idempotencyKey":"runtime","playerId":"p","walletId":"w","roundId":"r","gameId":"g","kind":"BET","money":{"amount":"1.00","currency":"BRL"}}}`
	group, dedup := sha256.Sum256([]byte("w")), sha256.Sum256([]byte("runtime"))
	_, err = client.SendMessage(ctx, &sdk.SendMessageInput{QueueUrl: &in, MessageBody: &body, MessageGroupId: aws.String(hex.EncodeToString(group[:])), MessageDeduplicationId: aws.String(hex.EncodeToString(dedup[:]))})
	if err != nil {
		t.Fatal(err)
	}
	app := fx.New(fx.Supply(cfg, pool, processor), Module, fx.NopLogger)
	if err = app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.Stop(stop); err != nil {
			t.Error(err)
		}
	}()
	output := sqsadapter.NewQueue(client, out, time.Second, 60*time.Second)
	for received := 0; received < 2; {
		m, err := output.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m != nil {
			received++
			if err = output.Delete(ctx, m.Receipt); err != nil {
				t.Fatal(err)
			}
		}
	}
	var completed int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM inbox_messages WHERE completed_at IS NOT NULL`).Scan(&completed); err != nil || completed != 1 {
		t.Fatal(completed, err)
	}
	var n int64
	if err = pool.QueryRow(ctx, `SELECT balance_minor FROM wallets WHERE id='w'`).Scan(&n); err != nil || n != 9900 {
		t.Fatal(n, err)
	}
}
