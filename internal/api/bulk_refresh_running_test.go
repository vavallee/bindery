package api

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// Review item 3 (#2236): "refresh selected" reports itself as running for as
// long as its fan-out lasts, so scheduled discovery can stop for it.
func TestBulkHandler_RefreshRunningCoversTheFanOut(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	h := NewBulkHandler(nil, nil, nil, nil).WithRefreshFunc(func(context.Context, *models.Author) {
		started <- struct{}{}
		<-release
	})
	if h.RefreshRunning() {
		t.Fatal("idle handler reports a running refresh")
	}
	h.fanOutRefreshes([]*models.Author{{ID: 1}})
	<-started
	if !h.RefreshRunning() {
		t.Fatal("refresh selected is running but RefreshRunning is false")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for h.RefreshRunning() {
		if time.Now().After(deadline) {
			t.Fatal("RefreshRunning stayed true after the fan-out finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
