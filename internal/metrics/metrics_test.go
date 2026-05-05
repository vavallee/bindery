package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHandler_ExposesGoRuntimeMetrics verifies the standard collectors are
// registered — operators dashboard go_goroutines / process_resident_memory
// before anything else, so a missing-collector regression would be a real
// degradation.
func TestHandler_ExposesGoRuntimeMetrics(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, name := range []string{"go_goroutines", "process_cpu_seconds_total"} {
		if !strings.Contains(body, name) {
			t.Errorf("expected %q in /metrics output, not found", name)
		}
	}
}

// TestHTTPMiddleware_RecordsRequestsByRoute verifies the middleware emits
// counter+histogram observations using the route template (not the raw URL
// path), so per-request URL parameters don't blow up label cardinality.
func TestHTTPMiddleware_RecordsRequestsByRoute(t *testing.T) {
	mw := HTTPMiddleware(func(_ *http.Request) string { return "/api/v1/book/{id}" })
	wrapped := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/book/42", nil))

	body := scrape(t)
	wantSubstring := `bindery_http_requests_total{method="POST",route="/api/v1/book/{id}",status="201"}`
	if !strings.Contains(body, wantSubstring) {
		t.Errorf("expected %q in metrics, got:\n%s", wantSubstring, body)
	}
}

// TestHTTPMiddleware_DefaultsToStatus200 verifies handlers that don't call
// WriteHeader explicitly are recorded as 200, matching net/http's default.
func TestHTTPMiddleware_DefaultsToStatus200(t *testing.T) {
	mw := HTTPMiddleware(func(_ *http.Request) string { return "/health" })
	wrapped := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))

	body := scrape(t)
	if !strings.Contains(body, `route="/health",status="200"`) {
		t.Errorf("expected status=200 for handler that only Write()d, got:\n%s", body)
	}
}

// TestHTTPMiddleware_FallsBackWhenRouteEmpty verifies the safety fallback
// for unmatched routes (404s, before chi has set the route pattern). A blank
// route label would silently group every 404 together — "other" makes the
// behavior explicit on the dashboard.
func TestHTTPMiddleware_FallsBackWhenRouteEmpty(t *testing.T) {
	mw := HTTPMiddleware(func(_ *http.Request) string { return "" })
	wrapped := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))

	body := scrape(t)
	if !strings.Contains(body, `route="other"`) {
		t.Errorf("expected route=\"other\" fallback, got:\n%s", body)
	}
}

// TestObserveSchedulerRun_RecordsCounterAndDuration verifies a job run shows
// up in both bindery_scheduler_runs_total and bindery_scheduler_run_duration.
func TestObserveSchedulerRun_RecordsCounterAndDuration(t *testing.T) {
	ObserveSchedulerRun("test-job-unique-name", "ok", 250*time.Millisecond)
	body := scrape(t)
	if !strings.Contains(body, `bindery_scheduler_runs_total{job="test-job-unique-name",result="ok"}`) {
		t.Errorf("missing counter entry, got:\n%s", body)
	}
	if !strings.Contains(body, `bindery_scheduler_run_duration_seconds_count{job="test-job-unique-name"}`) {
		t.Errorf("missing duration histogram, got:\n%s", body)
	}
}

// TestSetBuildInfo_PopulatesLabels verifies the build-info gauge carries the
// version/commit/date tuple — what dashboards key off to display the running
// version of Bindery.
func TestSetBuildInfo_PopulatesLabels(t *testing.T) {
	SetBuildInfo("v1.2.3", "abcdef", "2026-01-01")
	body := scrape(t)
	want := `bindery_build_info{commit="abcdef",date="2026-01-01",version="v1.2.3"} 1`
	if !strings.Contains(body, want) {
		t.Errorf("expected %q in metrics, got:\n%s", want, body)
	}
}

func scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics handler status=%d", rec.Code)
	}
	return rec.Body.String()
}
