package config

import (
	"testing"
	"time"
)

func TestMessagingConfig(t *testing.T) {
	c, err := loadMessaging()
	if err != nil {
		t.Fatal(err)
	}
	if c.Visibility != 60*time.Second || c.Processing != 20*time.Second || c.Polling != 20*time.Second || c.Concurrency != 4 {
		t.Fatal(c)
	}
	for _, tc := range []struct{ k, v string }{{"SQS_CONCURRENCY", "0"}, {"SQS_CONCURRENCY", "bad"}, {"SQS_PROCESSING_TIMEOUT", "0s"}, {"SQS_VISIBILITY_TIMEOUT", "20s"}, {"SQS_LONG_POLL", "21s"}, {"SQS_LONG_POLL", "1ms"}, {"AWS_REGION", ""}, {"SQS_CONSUMER_NAME", ""}} {
		t.Run(tc.k+tc.v, func(t *testing.T) {
			t.Setenv(tc.k, tc.v)
			if _, err := loadMessaging(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
