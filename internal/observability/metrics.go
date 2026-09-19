package observability

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

var Logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
var Outcomes [6]atomic.Uint64
var Duplicates, Retries, Conflicts, LatencyCount, LatencyMicros atomic.Uint64
var DLQDepth, OutboxLagSeconds atomic.Int64

func Outcome(status string, duplicate bool, elapsed time.Duration) {
	index := 5
	switch status {
	case "PENDING":
		index = 0
	case "PENDING_REFERENCE":
		index = 1
	case "PROCESSED":
		index = 2
	case "REJECTED":
		index = 3
	case "FAILED":
		index = 4
	}
	Outcomes[index].Add(1)
	if duplicate {
		Duplicates.Add(1)
	}
	LatencyCount.Add(1)
	LatencyMicros.Add(uint64(max(0, elapsed.Microseconds())))
}
func Write(w io.Writer) {
	for i, status := range []string{"PENDING", "PENDING_REFERENCE", "PROCESSED", "REJECTED", "FAILED", "ERROR"} {
		fmt.Fprintf(w, "financial_outcomes_total{status=%q} %d\n", status, Outcomes[i].Load())
	}
	fmt.Fprintf(w, "financial_duplicates_total %d\nworker_retries_total %d\nconcurrency_conflicts_total %d\nprocessing_duration_seconds_count %d\nprocessing_duration_seconds_sum %.6f\nsqs_dlq_visible_messages %d\noutbox_oldest_pending_seconds %d\n", Duplicates.Load(), Retries.Load(), Conflicts.Load(), LatencyCount.Load(), float64(LatencyMicros.Load())/1e6, DLQDepth.Load(), OutboxLagSeconds.Load())
}
