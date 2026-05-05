// Package metrics exposes Bindery runtime metrics in Prometheus exposition
// format. The registry, metric instances, and HTTP handler are all owned by
// this package so callers don't import client_golang directly — that keeps
// the surface area small and lets us swap the implementation later.
//
// The metrics here are deliberately minimal — a starter set that covers the
// production-monitoring use case (HTTP error rates, scheduler health, build
// info) without instrumenting every code path. Add metrics here and call
// the helpers from instrumented sites.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Registry is Bindery's process-wide Prometheus registry. Exported so
	// the test suite (and any future custom collectors) can register against
	// the same registry the /metrics handler scrapes from.
	Registry = prometheus.NewRegistry()

	httpRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bindery_http_requests_total",
		Help: "Total HTTP requests served, labelled by method, route template, and status code.",
	}, []string{"method", "route", "status"})

	httpRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "bindery_http_request_duration_seconds",
		Help:    "HTTP request handling latency, labelled by method and route template.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})

	schedulerRunsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bindery_scheduler_runs_total",
		Help: "Background job invocations, labelled by job name and result (ok|panic).",
	}, []string{"job", "result"})

	schedulerRunDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "bindery_scheduler_run_duration_seconds",
		Help:    "Background job duration, labelled by job name.",
		Buckets: []float64{.1, .5, 1, 5, 15, 60, 300, 1800},
	}, []string{"job"})

	buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "bindery_build_info",
		Help: "Bindery build metadata. Always 1; the labels carry version/commit/date.",
	}, []string{"version", "commit", "date"})
)

func init() {
	// Standard collectors give us go_* (runtime) and process_* (RSS, CPU, FDs)
	// for free — these are what most operators dashboard first.
	Registry.MustRegister(collectors.NewGoCollector())
	Registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	Registry.MustRegister(httpRequestsTotal, httpRequestDuration, schedulerRunsTotal, schedulerRunDuration, buildInfo)
}

// SetBuildInfo records the running binary's version/commit/date as a
// constant 1-valued gauge. Call once at startup.
func SetBuildInfo(version, commit, date string) {
	buildInfo.WithLabelValues(version, commit, date).Set(1)
}

// Handler returns the http.Handler that serves /metrics.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry})
}

// HTTPMiddleware wraps an http.Handler and records bindery_http_requests_total
// and bindery_http_request_duration_seconds for each request. Pass a routeFn
// closure that returns the route template (e.g. "/api/v1/book/{id}") rather
// than the raw URL path — high-cardinality URL parameters would otherwise
// blow up the label space. routeFn is called AFTER the inner handler runs
// because route templates (e.g. via chi.RouteContext) are only populated
// once routing has matched a pattern.
func HTTPMiddleware(routeFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(rw, r)
			route := routeFn(r)
			if route == "" {
				route = "other"
			}
			dur := time.Since(start).Seconds()
			httpRequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(rw.status)).Inc()
			httpRequestDuration.WithLabelValues(r.Method, route).Observe(dur)
		})
	}
}

// ObserveSchedulerRun records a background job run. result should be "ok" on
// normal completion or "panic" if the job goroutine recovered from a panic.
// Pair the call with a defer so the duration is always recorded:
//
//	defer metrics.ObserveSchedulerRun("scan-library", time.Now(), &result)
func ObserveSchedulerRun(job, result string, dur time.Duration) {
	schedulerRunsTotal.WithLabelValues(job, result).Inc()
	schedulerRunDuration.WithLabelValues(job).Observe(dur.Seconds())
}

// statusRecorder captures the response status code for the metrics
// middleware. We only need the status; the response body is pass-through.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}
