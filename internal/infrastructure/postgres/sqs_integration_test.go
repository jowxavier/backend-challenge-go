//go:build integration && sqsintegration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jowxavier/backend-challenge-go/internal/application/consumer"
	"github.com/jowxavier/backend-challenge-go/internal/application/events"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/application/outbox"
	"github.com/jowxavier/backend-challenge-go/internal/config"
	sqsadapter "github.com/jowxavier/backend-challenge-go/internal/infrastructure/sqs"
)

type sqsFixture struct {
	client             *sdk.Client
	input, output, dlq string
}

func testSQS(t *testing.T) sqsFixture {
	t.Helper()
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Fatal("TEST_SQS_ENDPOINT required")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	ctx := testContext(t)
	client, err := sqsadapter.NewClient(ctx, config.MessagingConfig{Endpoint: endpoint, Region: "us-east-1"})
	must(t, err)
	prefix := fmt.Sprintf("test-%x", randomBytes(t))
	f := sqsFixture{client: client}
	create := func(name string, attrs map[string]string) string {
		o, err := client.CreateQueue(ctx, &sdk.CreateQueueInput{QueueName: &name, Attributes: attrs})
		must(t, err)
		url := *o.QueueUrl
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := client.DeleteQueue(cleanup, &sdk.DeleteQueueInput{QueueUrl: &url})
			if err != nil {
				t.Error(err)
			}
		})
		return url
	}
	f.dlq = create(prefix+"-dlq.fifo", map[string]string{"FifoQueue": "true"})
	attrs, err := client.GetQueueAttributes(ctx, &sdk.GetQueueAttributesInput{QueueUrl: &f.dlq, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	must(t, err)
	policy, _ := json.Marshal(map[string]any{"deadLetterTargetArn": attrs.Attributes["QueueArn"], "maxReceiveCount": 2})
	f.input = create(prefix+".fifo", map[string]string{"FifoQueue": "true", "VisibilityTimeout": "2", "RedrivePolicy": string(policy)})
	f.output = create(prefix+"-events", nil)
	return f
}
func command(t *testing.T, id, wallet string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"messageId": id, "type": "WagerTransactionRequested", "occurredAt": "2026-01-01T00:00:00Z", "data": map[string]any{"providerId": "provider", "externalTransactionId": id, "idempotencyKey": "key-" + id, "playerId": "player-" + wallet, "walletId": wallet, "roundId": "round", "gameId": "game", "kind": "BET", "money": map[string]string{"amount": "1.00", "currency": "BRL"}}})
	must(t, err)
	return string(b)
}
func sendCommand(t *testing.T, f sqsFixture, body, wallet, id string) {
	t.Helper()
	group := sha256.Sum256([]byte(wallet))
	dedup := sha256.Sum256([]byte(id))
	_, err := f.client.SendMessage(testContext(t), &sdk.SendMessageInput{QueueUrl: &f.input, MessageBody: &body, MessageGroupId: aws.String(hex.EncodeToString(group[:])), MessageDeduplicationId: aws.String(hex.EncodeToString(dedup[:]))})
	must(t, err)
}
func receiveCommand(t *testing.T, q *sqsadapter.Queue) *consumer.Delivery {
	t.Helper()
	ctx := testContext(t)
	for {
		m, err := q.Receive(ctx)
		must(t, err)
		if m != nil {
			return m
		}
	}
}
func TestSQSInboundCommitDeleteAndRestart(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	f := testSQS(t)
	must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, "w", 10000)))
	p := newProcessor(t, NewRunner(pool))
	q := sqsadapter.NewQueue(f.client, f.input, time.Second, 2*time.Second)
	service, err := consumer.New(q, p, "consumer", time.Second)
	must(t, err)
	body := command(t, "msg", "w")
	sendCommand(t, f, body, "w", "msg")
	first := receiveCommand(t, q)
	decoded, err := consumer.Decode(first.Body, "consumer")
	must(t, err)
	_, err = p.ProcessMessage(ctx, decoded)
	must(t, err) // Crash window: no DeleteMessage.
	must(t, q.ChangeVisibility(ctx, first.Receipt, 0))
	restarted, err := consumer.New(q, newProcessor(t, NewRunner(pool)), "consumer", time.Second)
	must(t, err)
	second := receiveCommand(t, q)
	must(t, restarted.Handle(ctx, *second))
	// New broker message with the same envelope bypasses transport dedup intentionally.
	sendCommand(t, f, body, "w", "different-transport-id")
	third := receiveCommand(t, q)
	must(t, service.Handle(ctx, *third))
	if count(t, pool, "inbox_messages") != 1 || count(t, pool, "wallet_ledger_entries") != 1 || count(t, pool, "outbox_events") != 2 {
		t.Fatal("duplicate effect")
	}
	m, err := q.Receive(ctx)
	must(t, err)
	if m != nil {
		t.Fatal("message not deleted")
	}
}

type failingMessageProcessor struct{}

func (failingMessageProcessor) ProcessMessage(context.Context, financial.Message) (financial.MessageResult, error) {
	return financial.MessageResult{}, injected
}
func TestSQSTransientVisibility(t *testing.T) {
	f := testSQS(t)
	ctx := testContext(t)
	q := sqsadapter.NewQueue(f.client, f.input, time.Second, 2*time.Second)
	s, err := consumer.New(q, failingMessageProcessor{}, "consumer", time.Second)
	must(t, err)
	sendCommand(t, f, command(t, "retry", "w"), "w", "retry")
	first := receiveCommand(t, q)
	if err = s.Handle(ctx, *first); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	second := receiveCommand(t, q)
	if second.ReceiveCount < 2 || second.Body != first.Body {
		t.Fatal("message acknowledged or not retried")
	}
	must(t, q.Delete(ctx, second.Receipt))
}
func TestSQSPoisonRedrive(t *testing.T) {
	f := testSQS(t)
	ctx := testContext(t)
	q := sqsadapter.NewQueue(f.client, f.input, time.Second, 2*time.Second)
	s, err := consumer.New(q, failingMessageProcessor{}, "consumer", time.Second)
	must(t, err)
	sendCommand(t, f, "malformed", "w", "poison")
	for i := 0; i < 2; i++ {
		m := receiveCommand(t, q)
		if err = s.Handle(ctx, *m); !errors.Is(err, consumer.ErrInvalidMessage) {
			t.Fatal(err)
		}
	}
	// Receiving the source queue drives native redrive after maxReceiveCount.
	dlq := sqsadapter.NewQueue(f.client, f.dlq, time.Second, 2*time.Second)
	for {
		m, err := dlq.Receive(ctx)
		must(t, err)
		if m != nil {
			if m.Body != "malformed" {
				t.Fatal(m)
			}
			must(t, dlq.Delete(ctx, m.Receipt))
			break
		}
		m, err = q.Receive(ctx)
		must(t, err)
		if m != nil {
			t.Fatal("poison exceeded configured deliveries")
		}
	}
}

type ambiguousSQS struct {
	publisher outbox.EventPublisher
	sent      bool
}

func (p *ambiguousSQS) Publish(ctx context.Context, e events.Event) error {
	if err := p.publisher.Publish(ctx, e); err != nil {
		return err
	}
	if !p.sent {
		p.sent = true
		return injected
	}
	return nil
}
func TestSQSOutboundSnapshotAndAmbiguousSend(t *testing.T) {
	pool := testPool(t)
	ctx := testContext(t)
	f := testSQS(t)
	id := createOutboxFixture(t, pool)
	var raw []byte
	must(t, pool.QueryRow(ctx, `SELECT payload FROM outbox_events WHERE transaction_id=$1`, id).Scan(&raw))
	event, err := events.Restore(raw)
	must(t, err)
	clock := time.Now().Add(time.Hour)
	publisher := &ambiguousSQS{publisher: sqsadapter.NewPublisher(f.client, f.output)}
	service, err := outbox.NewService(NewOutboxDelivery(pool), publisher, outbox.DefaultConfig(), func() time.Time { return clock })
	must(t, err)
	_, err = service.RunOnce(ctx)
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	var published *time.Time
	must(t, pool.QueryRow(ctx, `SELECT published_at FROM outbox_events`).Scan(&published))
	if published != nil {
		t.Fatal("ambiguous send marked published")
	}
	clock = clock.Add(time.Second)
	_, err = service.RunOnce(ctx)
	must(t, err)
	output := sqsadapter.NewQueue(f.client, f.output, time.Second, 2*time.Second)
	for i := 0; i < 2; i++ {
		m := receiveCommand(t, output)
		if m.Body != string(raw) {
			t.Fatal("snapshot reconstructed")
		}
		got, err := events.Restore([]byte(m.Body))
		must(t, err)
		if got.ID() != event.ID() {
			t.Fatal("event identity changed")
		}
		must(t, output.Delete(ctx, m.Receipt))
	}
	must(t, pool.QueryRow(ctx, `SELECT published_at FROM outbox_events`).Scan(&published))
	if published == nil {
		t.Fatal("not marked published")
	}
}
func TestSQSMultipleConsumers(t *testing.T) {
	pool := testPool(t)
	f := testSQS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, id := range []string{"w", "other"} {
		must(t, NewWalletRepository(pool).Insert(ctx, testWallet(t, id, 10000)))
		sendCommand(t, f, command(t, "msg-"+id, id), id, "msg-"+id)
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	results := make(chan error, 2)
	services := make([]*consumer.Service, 2)
	queues := make([]*sqsadapter.Queue, 2)
	for i := range services {
		queues[i] = sqsadapter.NewQueue(f.client, f.input, time.Second, 2*time.Second)
		var err error
		services[i], err = consumer.New(queues[i], newProcessor(t, NewRunner(pool)), "consumer", time.Second)
		must(t, err)
	}
	for i := range services {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			ready <- struct{}{}
			select {
			case <-start:
			case <-ctx.Done():
				return
			}
			m, err := queues[i].Receive(ctx)
			if err == nil && m != nil {
				err = services[i].Handle(ctx, *m)
			} else if err == nil {
				err = errors.New("no message")
			}
			results <- err
		}(i)
	}
	defer func() { cancel(); <-done; <-done }()
	<-ready
	<-ready
	close(start)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			must(t, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if count(t, pool, "inbox_messages") != 2 || count(t, pool, "wallet_ledger_entries") != 2 {
		t.Fatal("lost work")
	}
}
