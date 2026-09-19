package config

import (
	"fmt"
	"strconv"
	"time"
)

type MessagingConfig struct {
	Endpoint, Region, InputQueue, OutputQueue, ConsumerName string
	Concurrency                                             int
	Visibility, Processing, Polling                         time.Duration
}

func loadMessaging() (MessagingConfig, error) {
	c := MessagingConfig{Endpoint: getEnv("SQS_ENDPOINT", "http://localhost:4566"), Region: getEnv("AWS_REGION", "us-east-1"), InputQueue: getEnv("SQS_INPUT_QUEUE", "wager-transactions.fifo"), OutputQueue: getEnv("SQS_OUTPUT_QUEUE", "wager-events"), ConsumerName: getEnv("SQS_CONSUMER_NAME", "wager-financial-v1")}
	var err error
	c.Concurrency, err = strconv.Atoi(getEnv("SQS_CONCURRENCY", "4"))
	if err != nil || c.Concurrency < 1 || c.Concurrency > 100 {
		return c, fmt.Errorf("invalid SQS_CONCURRENCY")
	}
	for _, item := range []struct {
		key, fallback string
		target        *time.Duration
	}{{"SQS_VISIBILITY_TIMEOUT", "60s", &c.Visibility}, {"SQS_PROCESSING_TIMEOUT", "20s", &c.Processing}, {"SQS_LONG_POLL", "20s", &c.Polling}} {
		*item.target, err = time.ParseDuration(getEnv(item.key, item.fallback))
		if err != nil {
			return c, fmt.Errorf("invalid %s", item.key)
		}
	}
	if c.Region == "" || c.InputQueue == "" || c.OutputQueue == "" || c.InputQueue == c.OutputQueue || c.ConsumerName == "" || c.Processing <= 0 || c.Visibility <= c.Processing || c.Visibility > 12*time.Hour || c.Visibility%time.Second != 0 || c.Polling < time.Second || c.Polling > 20*time.Second || c.Polling%time.Second != 0 {
		return c, fmt.Errorf("invalid SQS configuration")
	}
	return c, nil
}
