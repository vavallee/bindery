package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metrics"
	"github.com/vavallee/bindery/internal/metrics/metricstest"
	"github.com/vavallee/bindery/internal/models"
)

func queueItems(t *testing.T, state string) float64 {
	return metricstest.Value(t, "bindery_queue_items", map[string]string{"state": state})
}

// TestQueueItems_CountsDownloadsByState wires the gauge the way main does,
// against a real database, and checks a scrape reports each state's count,
// zero for an empty state, and only bounded state labels.
func TestQueueItems_CountsDownloadsByState(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewDownloadRepo(database)
	ctx := context.Background()
	for i, st := range []models.DownloadState{models.StateDownloading, models.StateDownloading, models.StateImportFailed} {
		d := &models.Download{GUID: "queue-metric-" + string(rune('a'+i)), Title: "t", Status: st, NZBURL: "fake://x"}
		if err := repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	// A state written by something outside the known set must not become its
	// own series.
	if _, err := database.ExecContext(ctx, "UPDATE downloads SET status='made-up' WHERE guid='queue-metric-c'"); err != nil {
		t.Fatal(err)
	}

	metrics.SetQueueCounter(downloadStateLabels(), repo.CountByStatus)
	t.Cleanup(func() { metrics.SetQueueCounter(nil, nil) })

	if got := queueItems(t, "downloading"); got != 2 {
		t.Errorf("downloading = %v, want 2", got)
	}
	if got := queueItems(t, "other"); got != 1 {
		t.Errorf("other = %v, want 1", got)
	}
	if got := queueItems(t, "importBlocked"); got != 0 {
		t.Errorf("importBlocked = %v, want 0", got)
	}
	allowed := append(downloadStateLabels(), "other")
	values := metricstest.LabelValues(t, "bindery_queue_items", "state")
	if len(values) != len(allowed) {
		t.Errorf("got %d state series, want %d: %v", len(values), len(allowed), values)
	}
	for _, v := range values {
		if !slices.Contains(allowed, v) {
			t.Errorf("state label %q is outside the bounded set", v)
		}
	}
}

// TestQueueItems_FailedCountEmitsNothing checks a failing count leaves the
// gauge out of the scrape instead of failing the whole scrape.
func TestQueueItems_FailedCountEmitsNothing(t *testing.T) {
	metrics.SetQueueCounter(downloadStateLabels(), func(context.Context) (map[string]int, error) {
		return nil, errors.New("database is locked")
	})
	t.Cleanup(func() { metrics.SetQueueCounter(nil, nil) })
	if v := metricstest.LabelValues(t, "bindery_queue_items", "state"); len(v) != 0 {
		t.Errorf("expected no queue samples after a failed count, got %v", v)
	}
}
