package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// TestQueueCollector_TimesOut checks a count that never answers is cut off
// by the collect timeout, so a stuck database cannot hold a scrape open.
func TestQueueCollector_TimesOut(t *testing.T) {
	prev := queueCollectTimeout
	queueCollectTimeout = 50 * time.Millisecond
	t.Cleanup(func() { queueCollectTimeout = prev; SetQueueCounter(nil, nil) })
	SetQueueCounter([]string{"downloading"}, func(ctx context.Context) (map[string]int, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	start := time.Now()
	body := scrape(t)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("scrape took %v with a stuck queue count", elapsed)
	}
	if strings.Contains(body, "bindery_queue_items{") {
		t.Error("a timed out count still produced queue samples")
	}
}

// TestQueueCollector_CachesCounts checks scrapes inside the cache window
// share one count, a scrape after it counts again, and a failed count is not
// cached.
func TestQueueCollector_CachesCounts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	prevNow := queueNow
	queueNow = func() time.Time { return now }
	t.Cleanup(func() { queueNow = prevNow; SetQueueCounter(nil, nil) })

	calls := 0
	fail := false
	SetQueueCounter([]string{"downloading"}, func(context.Context) (map[string]int, error) {
		calls++
		if fail {
			return nil, errors.New("database is locked")
		}
		return map[string]int{"downloading": calls}, nil
	})

	for i := 0; i < 5; i++ {
		_ = scrape(t)
	}
	if calls != 1 {
		t.Fatalf("5 scrapes inside the window made %d counts, want 1", calls)
	}
	if !strings.Contains(scrape(t), `bindery_queue_items{state="downloading"} 1`) {
		t.Error("cached count not served")
	}

	now = now.Add(queueCacheTTL)
	fail = true
	if strings.Contains(scrape(t), "bindery_queue_items{") {
		t.Error("a failed count after expiry still served samples")
	}
	fail = false
	body := scrape(t)
	if calls != 3 {
		t.Errorf("a failed count was cached: %d counts, want 3", calls)
	}
	if !strings.Contains(body, `bindery_queue_items{state="downloading"} 3`) {
		t.Errorf("fresh count not served after expiry")
	}
}

// TestDownloadClientTransport_Results checks how responses map to result:
// 2xx ok, 4xx rejected, 5xx and transport failures error.
func TestDownloadClientTransport_Results(t *testing.T) {
	codes := map[int]string{200: ResultOK, 409: ResultRejected, 503: ResultError}
	for code, want := range codes {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		before := counterValue(t, downloadClientRequestsTotal.WithLabelValues("deluge", want))
		c := &http.Client{Transport: DownloadClientTransport("deluge", nil)}
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		srv.Close()
		if got := counterValue(t, downloadClientRequestsTotal.WithLabelValues("deluge", want)) - before; got != 1 {
			t.Errorf("status %d: %s moved by %v, want 1", code, want, got)
		}
	}

	failing := DownloadClientTransport("deluge", roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial refused")
	}))
	before := counterValue(t, downloadClientRequestsTotal.WithLabelValues("deluge", ResultError))
	req := httptest.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	resp, err := failing.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected the transport error to pass through")
	}
	if got := counterValue(t, downloadClientRequestsTotal.WithLabelValues("deluge", ResultError)) - before; got != 1 {
		t.Errorf("transport failure moved error by %v, want 1", got)
	}
}

// TestObserveSchedulerRun_LastSuccessOnlyOnOK checks a panic run leaves the
// freshness gauge alone, which is what lets an alert spot a job that keeps
// failing.
func TestObserveSchedulerRun_LastSuccessOnlyOnOK(t *testing.T) {
	ObserveSchedulerRun("freshness-panic-only", "panic", time.Millisecond)
	if strings.Contains(scrape(t), `bindery_scheduler_last_success_timestamp_seconds{job="freshness-panic-only"}`) {
		t.Error("a panic run set the last success gauge")
	}
	ObserveSchedulerRun("freshness-ok", "ok", time.Millisecond)
	if !strings.Contains(scrape(t), `bindery_scheduler_last_success_timestamp_seconds{job="freshness-ok"}`) {
		t.Error("an ok run did not set the last success gauge")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func counterValue(t *testing.T, c interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}
