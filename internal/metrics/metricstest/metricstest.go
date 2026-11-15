// Package metricstest reads Bindery's metrics registry from tests in other
// packages, so a test can assert that the real code path moved a counter
// without each package parsing the exposition format itself.
package metricstest

import (
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/vavallee/bindery/internal/metrics"
)

// Value returns the value of the sample of metric name whose labels equal
// labels exactly. Counters and gauges report their value, histograms their
// sample count. A sample that does not exist reads as 0.
func Value(t testing.TB, name string, labels map[string]string) float64 {
	t.Helper()
	for _, m := range family(t, name) {
		if labelsEqual(m.GetLabel(), labels) {
			switch {
			case m.Counter != nil:
				return m.GetCounter().GetValue()
			case m.Gauge != nil:
				return m.GetGauge().GetValue()
			case m.Histogram != nil:
				return float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return 0
}

// LabelValues returns every value label takes across the samples of metric
// name, so a test can check they all fall in the bounded set.
func LabelValues(t testing.TB, name, label string) []string {
	t.Helper()
	var out []string
	for _, m := range family(t, name) {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == label {
				out = append(out, lp.GetValue())
			}
		}
	}
	return out
}

func family(t testing.TB, name string) []*dto.Metric {
	t.Helper()
	mfs, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf.GetMetric()
		}
	}
	return nil
}

func labelsEqual(got []*dto.LabelPair, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, lp := range got {
		if v, ok := want[lp.GetName()]; !ok || v != lp.GetValue() {
			return false
		}
	}
	return true
}
