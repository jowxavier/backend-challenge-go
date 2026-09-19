package observability

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMetrics(t *testing.T) {
	before := Outcomes[2].Load()
	dupes := Duplicates.Load()
	Outcome("PROCESSED", true, time.Millisecond)
	if Outcomes[2].Load() != before+1 || Duplicates.Load() != dupes+1 {
		t.Fatal("missing observation")
	}
	var b bytes.Buffer
	Write(&b)
	for _, name := range []string{"financial_outcomes_total", "financial_duplicates_total", "worker_retries_total", "concurrency_conflicts_total", "processing_duration_seconds_sum", "sqs_dlq_visible_messages", "outbox_oldest_pending_seconds"} {
		if !strings.Contains(b.String(), name) {
			t.Fatal(name)
		}
	}
}
