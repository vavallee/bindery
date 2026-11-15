package scheduler

import (
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metrics/metricstest"
)

// TestRunJob_SetsLastSuccessTimestamp runs a job through the same runJob
// wrapper Start registers every cron job with. A clean run stamps
// bindery_scheduler_last_success_timestamp_seconds; a panicking run does not.
func TestRunJob_SetsLastSuccessTimestamp(t *testing.T) {
	const okJob, panicJob = "metrics-test-ok-job", "metrics-test-panic-job"
	before := float64(time.Now().Add(-time.Second).Unix())

	runJob(okJob, func() {})()
	runJob(panicJob, func() { panic("boom") })()

	got := metricstest.Value(t, "bindery_scheduler_last_success_timestamp_seconds", map[string]string{"job": okJob})
	if got < before {
		t.Errorf("last success for a clean run = %v, want a timestamp at or after %v", got, before)
	}
	if v := metricstest.Value(t, "bindery_scheduler_last_success_timestamp_seconds", map[string]string{"job": panicJob}); v != 0 {
		t.Errorf("a panicking run set last success to %v, want it unset", v)
	}
}
