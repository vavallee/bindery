package indexer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/metrics/metricstest"
	"github.com/vavallee/bindery/internal/models"
)

func indexerRequests(t *testing.T, indexerType, result string) float64 {
	return metricstest.Value(t, "bindery_indexer_requests_total", map[string]string{"indexer_type": indexerType, "result": result})
}

const metricsEmptyFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel><newznab:response offset="0" total="0"/></channel>
</rss>`

// TestSearch_RecordsIndexerRequests runs SearchQuery against one healthy
// torznab indexer, one newznab indexer answering 500 and one with a type
// Bindery does not know, and checks each search lands under the right
// bounded labels.
func TestSearch_RecordsIndexerRequests(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(metricsEmptyFeed))
	}))
	defer ok.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer broken.Close()

	okBefore := indexerRequests(t, "torznab", "ok")
	errBefore := indexerRequests(t, "newznab", "error")
	otherBefore := indexerRequests(t, "other", "ok")
	durBefore := metricstest.Value(t, "bindery_indexer_request_duration_seconds", map[string]string{"indexer_type": "torznab"})

	idxs := []models.Indexer{
		{ID: 1, Name: "My Tracker", Type: "torznab", URL: ok.URL, Enabled: true},
		{ID: 2, Name: "My Usenet", Type: "newznab", URL: broken.URL, Enabled: true},
		{ID: 3, Name: "Free text name", Type: "Some Custom Thing", URL: ok.URL, Enabled: true},
	}
	newTestSearcher().SearchQuery(context.Background(), idxs, "anything")

	if got := indexerRequests(t, "torznab", "ok") - okBefore; got != 1 {
		t.Errorf("torznab ok moved by %v, want 1", got)
	}
	if got := indexerRequests(t, "newznab", "error") - errBefore; got != 1 {
		t.Errorf("newznab error moved by %v, want 1", got)
	}
	if got := indexerRequests(t, "other", "ok") - otherBefore; got != 1 {
		t.Errorf("unknown type moved other by %v, want 1", got)
	}
	if got := metricstest.Value(t, "bindery_indexer_request_duration_seconds", map[string]string{"indexer_type": "torznab"}) - durBefore; got != 1 {
		t.Errorf("duration histogram moved by %v, want 1", got)
	}

	for _, v := range metricstest.LabelValues(t, "bindery_indexer_requests_total", "indexer_type") {
		if !slices.Contains([]string{"newznab", "torznab", "other"}, v) {
			t.Errorf("indexer_type label %q is outside the bounded set", v)
		}
	}
	for _, v := range metricstest.LabelValues(t, "bindery_indexer_requests_total", "result") {
		if !slices.Contains([]string{"ok", "error", "timeout", "quota"}, v) {
			t.Errorf("result label %q is outside the bounded set", v)
		}
	}
}

func TestSearchResult_Classifies(t *testing.T) {
	cases := map[string]error{
		"ok":      nil,
		"quota":   fmt.Errorf("wrapped: %w", newznab.ErrQueryBudgetExhausted),
		"timeout": fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
		"error":   errors.New("connection refused"),
	}
	for want, err := range cases {
		if got := searchResult(err); got != want {
			t.Errorf("searchResult(%v) = %q, want %q", err, got, want)
		}
	}
}
