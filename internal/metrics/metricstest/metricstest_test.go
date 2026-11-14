package metricstest

import (
	"slices"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metrics"
)

// TestValue_ReadsEachMetricType checks Value reads a counter's value, a
// gauge's value and a histogram's sample count, matches labels exactly, and
// reads a missing sample or metric as 0.
func TestValue_ReadsEachMetricType(t *testing.T) {
	before := Value(t, "bindery_grabs_total", map[string]string{"media_type": "ebook", "result": "ok"})
	metrics.ObserveGrab("ebook", nil)
	metrics.ObserveGrab("ebook", nil)
	if got := Value(t, "bindery_grabs_total", map[string]string{"media_type": "ebook", "result": "ok"}) - before; got != 2 {
		t.Errorf("counter moved by %v, want 2", got)
	}

	hBefore := Value(t, "bindery_indexer_request_duration_seconds", map[string]string{"indexer_type": "torznab"})
	metrics.ObserveIndexerRequest("torznab", "ok", 3*time.Second)
	if got := Value(t, "bindery_indexer_request_duration_seconds", map[string]string{"indexer_type": "torznab"}) - hBefore; got != 1 {
		t.Errorf("histogram sample count moved by %v, want 1", got)
	}

	metrics.ObserveSchedulerRun("metricstest-job", "ok", time.Millisecond)
	if got := Value(t, "bindery_scheduler_last_success_timestamp_seconds", map[string]string{"job": "metricstest-job"}); got < float64(time.Now().Add(-time.Minute).Unix()) {
		t.Errorf("gauge read %v, want a recent timestamp", got)
	}

	if got := Value(t, "bindery_grabs_total", map[string]string{"media_type": "ebook"}); got != 0 {
		t.Errorf("a partial label set matched a sample: %v", got)
	}
	if got := Value(t, "bindery_grabs_total", map[string]string{"media_type": "ebook", "result": "nope"}); got != 0 {
		t.Errorf("a wrong label value matched a sample: %v", got)
	}
	if got := Value(t, "no_such_metric", nil); got != 0 {
		t.Errorf("a missing metric read %v, want 0", got)
	}
}

// TestLabelValues_ListsEverySample checks LabelValues returns the label's
// value from every sample of the metric, and nothing for a missing metric.
func TestLabelValues_ListsEverySample(t *testing.T) {
	metrics.ObserveImport("ebook", "imported", "none")
	metrics.ObserveImport("audiobook", "blocked", "conflict")
	got := LabelValues(t, "bindery_imports_total", "media_type")
	for _, want := range []string{"ebook", "audiobook"} {
		if !slices.Contains(got, want) {
			t.Errorf("LabelValues = %v, missing %q", got, want)
		}
	}
	if v := LabelValues(t, "no_such_metric", "x"); len(v) != 0 {
		t.Errorf("missing metric gave %v", v)
	}
}
