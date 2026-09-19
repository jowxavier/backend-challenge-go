package sqs

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	sdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jowxavier/backend-challenge-go/internal/application/consumer"
	"github.com/jowxavier/backend-challenge-go/internal/application/events"
	"github.com/jowxavier/backend-challenge-go/internal/config"
)

func NewClient(ctx context.Context, c config.MessagingConfig) (*sdk.Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(c.Region))
	if err != nil {
		return nil, err
	}
	return sdk.NewFromConfig(cfg, func(o *sdk.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
	}), nil
}

type Queue struct {
	client           *sdk.Client
	url              string
	poll, visibility int32
}

func NewQueue(client *sdk.Client, url string, poll, visibility time.Duration) *Queue {
	return &Queue{client, url, int32(poll / time.Second), int32(visibility / time.Second)}
}
func (q *Queue) Receive(ctx context.Context) (*consumer.Delivery, error) {
	out, err := q.client.ReceiveMessage(ctx, &sdk.ReceiveMessageInput{QueueUrl: &q.url, MaxNumberOfMessages: 1, WaitTimeSeconds: q.poll, VisibilityTimeout: q.visibility, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount}})
	if err != nil {
		return nil, err
	}
	if len(out.Messages) == 0 {
		return nil, nil
	}
	m := out.Messages[0]
	if m.Body == nil || m.ReceiptHandle == nil {
		return nil, errors.New("invalid SQS response")
	}
	count, err := strconv.Atoi(m.Attributes["ApproximateReceiveCount"])
	if err != nil || count < 1 {
		count = 1
	}
	return &consumer.Delivery{Body: *m.Body, Receipt: *m.ReceiptHandle, ReceiveCount: count}, nil
}
func (q *Queue) Delete(ctx context.Context, receipt string) error {
	_, err := q.client.DeleteMessage(ctx, &sdk.DeleteMessageInput{QueueUrl: &q.url, ReceiptHandle: &receipt})
	return err
}
func (q *Queue) ChangeVisibility(ctx context.Context, receipt string, d time.Duration) error {
	_, err := q.client.ChangeMessageVisibility(ctx, &sdk.ChangeMessageVisibilityInput{QueueUrl: &q.url, ReceiptHandle: &receipt, VisibilityTimeout: int32(d / time.Second)})
	return err
}

type Publisher struct {
	client *sdk.Client
	url    string
}

func NewPublisher(client *sdk.Client, url string) *Publisher { return &Publisher{client, url} }
func (p *Publisher) Publish(ctx context.Context, e events.Event) error {
	if !e.Valid() {
		return events.ErrInvalidEvent
	}
	_, err := p.client.SendMessage(ctx, &sdk.SendMessageInput{QueueUrl: &p.url, MessageBody: aws.String(string(e.JSON()))})
	return err
}
