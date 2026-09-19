package http

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/config"
	sqsadapter "github.com/jowxavier/backend-challenge-go/internal/infrastructure/sqs"
	"go.uber.org/fx"
)

type Readiness struct {
	pool   *pgxpool.Pool
	client *sdk.Client
	cfg    config.MessagingConfig
}

func NewReadiness(lc fx.Lifecycle, cfg *config.Config, pool *pgxpool.Pool) *Readiness {
	r := &Readiness{pool: pool, cfg: cfg.Messaging}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		var err error
		r.client, err = sqsadapter.NewClient(ctx, r.cfg)
		return err
	}})
	return r
}
func (r *Readiness) Check(ctx context.Context) error {
	if err := r.pool.Ping(ctx); err != nil {
		return err
	}
	for _, name := range []string{r.cfg.InputQueue, r.cfg.OutputQueue} {
		if _, err := r.client.GetQueueUrl(ctx, &sdk.GetQueueUrlInput{QueueName: aws.String(name)}); err != nil {
			return err
		}
	}
	return nil
}
