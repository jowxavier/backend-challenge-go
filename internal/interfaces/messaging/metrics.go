package messaging

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jowxavier/backend-challenge-go/internal/observability"
)

func sampleMetrics(ctx context.Context, pool *pgxpool.Pool, client *sdk.Client, redrive string) {
	var policy struct {
		ARN string `json:"deadLetterTargetArn"`
	}
	_ = json.Unmarshal([]byte(redrive), &policy)
	parts := strings.Split(policy.ARN, ":")
	for ctx.Err() == nil {
		sample, cancel := context.WithTimeout(ctx, 5*time.Second)
		var lag int64
		err := pool.QueryRow(sample, `SELECT COALESCE(GREATEST(0,EXTRACT(EPOCH FROM clock_timestamp()-MIN(occurred_at)))::bigint,0) FROM outbox_events WHERE published_at IS NULL`).Scan(&lag)
		if err == nil {
			observability.OutboxLagSeconds.Store(lag)
		}
		if len(parts) > 0 && policy.ARN != "" {
			q, e := client.GetQueueUrl(sample, &sdk.GetQueueUrlInput{QueueName: aws.String(parts[len(parts)-1])})
			if e == nil {
				a, e := client.GetQueueAttributes(sample, &sdk.GetQueueAttributesInput{QueueUrl: q.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages}})
				if e == nil {
					n, e := strconv.ParseInt(a.Attributes["ApproximateNumberOfMessages"], 10, 64)
					if e == nil {
						observability.DLQDepth.Store(n)
					}
				}
			}
		}
		cancel()
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
