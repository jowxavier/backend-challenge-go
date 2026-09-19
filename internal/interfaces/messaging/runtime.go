package messaging

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/application/consumer"
	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/application/outbox"
	"github.com/jowxavier/backend-challenge-go/internal/config"
	"github.com/jowxavier/backend-challenge-go/internal/infrastructure/postgres"
	sqsadapter "github.com/jowxavier/backend-challenge-go/internal/infrastructure/sqs"
	"go.uber.org/fx"
)

var Module = fx.Module("messaging", fx.Invoke(Register))

func Register(lc fx.Lifecycle, cfg *config.Config, pool *pgxpool.Pool, processor *financial.Processor) {
	var stopReceive, stopWork context.CancelFunc
	var done chan struct{}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			c := cfg.Messaging
			client, err := sqsadapter.NewClient(ctx, c)
			if err != nil {
				return err
			}
			input, err := client.GetQueueUrl(ctx, &sdk.GetQueueUrlInput{QueueName: aws.String(c.InputQueue)})
			if err != nil {
				return err
			}
			output, err := client.GetQueueUrl(ctx, &sdk.GetQueueUrlInput{QueueName: aws.String(c.OutputQueue)})
			if err != nil {
				return err
			}
			attrs, err := client.GetQueueAttributes(ctx, &sdk.GetQueueAttributesInput{QueueUrl: input.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameFifoQueue, types.QueueAttributeNameRedrivePolicy}})
			if err != nil {
				return err
			}
			if attrs.Attributes["FifoQueue"] != "true" || attrs.Attributes["RedrivePolicy"] == "" {
				return errors.New("input FIFO/redrive not provisioned")
			}
			outputAttrs, err := client.GetQueueAttributes(ctx, &sdk.GetQueueAttributesInput{QueueUrl: output.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameFifoQueue}})
			if err != nil {
				return err
			}
			if outputAttrs.Attributes["FifoQueue"] == "true" {
				return errors.New("output queue must be Standard")
			}

			worker, err := consumer.New(sqsadapter.NewQueue(client, *input.QueueUrl, c.Polling, c.Visibility), processor, c.ConsumerName, c.Processing)
			if err != nil {
				return err
			}
			publisher, err := outbox.NewService(postgres.NewOutboxDelivery(pool), sqsadapter.NewPublisher(client, *output.QueueUrl), outbox.DefaultConfig(), time.Now)
			if err != nil {
				return err
			}
			var receiveCtx, workCtx context.Context
			receiveCtx, stopReceive = context.WithCancel(context.Background())
			workCtx, stopWork = context.WithCancel(context.Background())
			done = make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < c.Concurrency; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); worker.Run(receiveCtx, workCtx) }()
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				for receiveCtx.Err() == nil {
					worked, err := publisher.RunOnce(workCtx)
					if !worked || err != nil {
						timer := time.NewTimer(time.Second)
						select {
						case <-receiveCtx.Done():
							timer.Stop()
							return
						case <-timer.C:
						}
					}
				}
			}()
			go func() { wg.Wait(); close(done) }()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			stopReceive()
			select {
			case <-done:
				stopWork()
				return nil
			case <-ctx.Done():
				stopWork()
			}
			timer := time.NewTimer(11 * time.Second)
			defer timer.Stop()
			select {
			case <-done:
				return ctx.Err()
			case <-timer.C:
				return errors.New("messaging shutdown did not finish after cancellation")
			}
		},
	})
}
