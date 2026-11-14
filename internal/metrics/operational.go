package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Operational metrics (#3851): grabs, download client calls, imports, indexer
// searches, the download queue and scheduler freshness.
//
// Every label here comes from a fixed set. The helpers below map whatever the
// caller passes onto that set and fall back to "other", so a configured value
// Bindery does not know about (a new client type, a future media type, a
// reworded import failure) can never mint an unbounded run of series. Titles,
// authors, paths, user ids, URLs and the names users give their indexers and
// clients are never used as labels.

// LabelOther is the fallback value for any label input outside its set.
const LabelOther = "other"

// Result label values shared by the counters below.
const (
	ResultOK       = "ok"
	ResultError    = "error"
	ResultRejected = "rejected"
	ResultTimeout  = "timeout"
	ResultQuota    = "quota"
)

// Import results.
const (
	ImportResultImported = "imported"
	ImportResultFailed   = "failed"
	ImportResultBlocked  = "blocked"
)

// Import failure reasons. ImportReasonNone is the reason on a successful
// import; every failure maps onto one of the others, LabelOther included.
const (
	ImportReasonNone             = "none"
	ImportReasonPathMissing      = "path_missing"
	ImportReasonNoFiles          = "no_files"
	ImportReasonUnmatched        = "unmatched"
	ImportReasonRejectedFormat   = "rejected_format"
	ImportReasonRejectedLanguage = "rejected_language"
	ImportReasonRejectedContent  = "rejected_content"
	ImportReasonConflict         = "conflict"
	ImportReasonPlacement        = "placement"
	ImportReasonSourceGone       = "source_gone"
	ImportReasonRetryLimit       = "retry_limit"
)

var (
	mediaTypeLabels = set("ebook", "audiobook")

	downloadClientTypeLabels = set("sabnzbd", "nzbget", "transmission", "qbittorrent", "deluge", "rtorrent")

	indexerTypeLabels = set("newznab", "torznab")

	grabResultLabels = set(ResultOK, ResultError)

	clientResultLabels = set(ResultOK, ResultRejected, ResultError)

	indexerResultLabels = set(ResultOK, ResultError, ResultTimeout, ResultQuota)

	importResultLabels = set(ImportResultImported, ImportResultFailed, ImportResultBlocked)

	importReasonLabels = set(
		ImportReasonNone, ImportReasonPathMissing, ImportReasonNoFiles, ImportReasonUnmatched,
		ImportReasonRejectedFormat, ImportReasonRejectedLanguage, ImportReasonRejectedContent,
		ImportReasonConflict, ImportReasonPlacement, ImportReasonSourceGone, ImportReasonRetryLimit,
	)
)

func set(values ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(values))
	for _, v := range values {
		m[v] = struct{}{}
	}
	return m
}

func bound(v string, allowed map[string]struct{}) string {
	if _, ok := allowed[v]; ok {
		return v
	}
	return LabelOther
}

var (
	grabsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bindery_grabs_total",
		Help: "Releases sent to a download client, labelled by media type and result (ok|error).",
	}, []string{"media_type", "result"})

	downloadClientRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bindery_download_client_requests_total",
		Help: "HTTP requests Bindery made to download clients, labelled by client type and result (ok|rejected|error).",
	}, []string{"client_type", "result"})

	downloadClientRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "bindery_download_client_request_duration_seconds",
		Help:    "Download client HTTP request latency, labelled by client type.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"client_type"})

	importsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bindery_imports_total",
		Help: "Download imports, labelled by media type, result (imported|failed|blocked) and failure reason.",
	}, []string{"media_type", "result", "reason"})

	indexerRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bindery_indexer_requests_total",
		Help: "Per indexer searches, labelled by indexer type and result (ok|error|timeout|quota).",
	}, []string{"indexer_type", "result"})

	indexerRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "bindery_indexer_request_duration_seconds",
		Help:    "Per indexer search latency, labelled by indexer type.",
		Buckets: []float64{.25, .5, 1, 2.5, 5, 10, 20, 45, 90},
	}, []string{"indexer_type"})

	schedulerLastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "bindery_scheduler_last_success_timestamp_seconds",
		Help: "Unix time of the last background job run that completed without panicking, labelled by job name.",
	}, []string{"job"})

	queueItemsDesc = prometheus.NewDesc(
		"bindery_queue_items",
		"Download records by state, counted from the database at scrape time.",
		[]string{"state"}, nil,
	)
)

func init() {
	Registry.MustRegister(
		grabsTotal, downloadClientRequestsTotal, downloadClientRequestDuration,
		importsTotal, indexerRequestsTotal, indexerRequestDuration, schedulerLastSuccess,
		queueCollectorInstance,
	)
}

// MediaTypeLabel bounds a media type to ebook, audiobook or other.
func MediaTypeLabel(mediaType string) string { return bound(mediaType, mediaTypeLabels) }

// DownloadClientTypeLabel bounds a download client type to the clients
// Bindery supports, or other.
func DownloadClientTypeLabel(clientType string) string {
	return bound(clientType, downloadClientTypeLabels)
}

// IndexerTypeLabel bounds an indexer type to newznab, torznab or other.
func IndexerTypeLabel(indexerType string) string { return bound(indexerType, indexerTypeLabels) }

// ObserveGrab records one release sent to a download client. A nil err is
// result ok.
func ObserveGrab(mediaType string, err error) {
	result := ResultOK
	if err != nil {
		result = ResultError
	}
	grabsTotal.WithLabelValues(MediaTypeLabel(mediaType), bound(result, grabResultLabels)).Inc()
}

// ObserveImport records one import outcome. result is one of the
// ImportResult constants and reason one of the ImportReason constants;
// anything else is recorded as other.
func ObserveImport(mediaType, result, reason string) {
	importsTotal.WithLabelValues(
		MediaTypeLabel(mediaType), bound(result, importResultLabels), bound(reason, importReasonLabels),
	).Inc()
}

// ObserveIndexerRequest records one search against one indexer. result is
// one of ok, error, timeout or quota.
func ObserveIndexerRequest(indexerType, result string, dur time.Duration) {
	t := IndexerTypeLabel(indexerType)
	indexerRequestsTotal.WithLabelValues(t, bound(result, indexerResultLabels)).Inc()
	indexerRequestDuration.WithLabelValues(t).Observe(dur.Seconds())
}

// DownloadClientTransport wraps a download client's HTTP transport so every
// request it makes is counted and timed under clientType. A transport error
// or a 5xx response is result error; a 4xx is rejected, which includes the
// expected session handshakes (Transmission's 409, qBittorrent's 403 before
// a relogin); anything else is ok.
func DownloadClientTransport(clientType string, inner http.RoundTripper) http.RoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &clientTransport{clientType: DownloadClientTypeLabel(clientType), inner: inner}
}

type clientTransport struct {
	clientType string
	inner      http.RoundTripper
}

func (t *clientTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.inner.RoundTrip(req)
	result := ResultOK
	switch {
	case err != nil || resp == nil || resp.StatusCode >= 500:
		result = ResultError
	case resp.StatusCode >= 400:
		result = ResultRejected
	}
	downloadClientRequestsTotal.WithLabelValues(t.clientType, result).Inc()
	downloadClientRequestDuration.WithLabelValues(t.clientType).Observe(time.Since(start).Seconds())
	return resp, err
}

// Unwrap returns the wrapped transport, so code that looks for a specific
// transport type underneath can still reach it.
func (t *clientTransport) Unwrap() http.RoundTripper { return t.inner }

// QueueCounter returns the number of download records per state.
type QueueCounter func(ctx context.Context) (map[string]int, error)

// queueCollectTimeout bounds the scrape time database query. A scrape that
// cannot get an answer in this window reports no queue samples rather than
// holding the scrape open.
var queueCollectTimeout = 2 * time.Second

type queueCollector struct {
	mu      sync.RWMutex
	states  []string
	known   map[string]struct{}
	counter QueueCounter
}

var queueCollectorInstance = &queueCollector{}

// SetQueueCounter installs the function bindery_queue_items is collected
// from. states is the full list of download states: each is always emitted,
// at zero when absent, so a state emptying out reads as 0 rather than the
// series disappearing. Counts for any state not in the list are summed into
// other. Passing a nil counter turns the gauge off.
func SetQueueCounter(states []string, counter QueueCounter) {
	queueCollectorInstance.mu.Lock()
	defer queueCollectorInstance.mu.Unlock()
	queueCollectorInstance.states = append([]string(nil), states...)
	queueCollectorInstance.known = set(states...)
	queueCollectorInstance.counter = counter
}

func (c *queueCollector) Describe(ch chan<- *prometheus.Desc) { ch <- queueItemsDesc }

func (c *queueCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	states, known, counter := c.states, c.known, c.counter
	timeout := queueCollectTimeout
	c.mu.RUnlock()
	if counter == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	counts, err := counter(ctx)
	if err != nil {
		slog.Debug("metrics: queue count failed, skipping bindery_queue_items", "error", err)
		return
	}
	values := make(map[string]float64, len(states)+1)
	for _, s := range states {
		values[s] = 0
	}
	for state, n := range counts {
		if _, ok := known[state]; !ok {
			state = LabelOther
		}
		values[state] += float64(n)
	}
	for state, v := range values {
		ch <- prometheus.MustNewConstMetric(queueItemsDesc, prometheus.GaugeValue, v, state)
	}
}
