package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/metadata/openlibrary"
	"github.com/vavallee/bindery/internal/models"
)

// The wiring is where provider and handler errors become the scheduler's
// flags (#2236). A rate limit must back off, a running sync must count as
// busy, and neither must be mistaken for the other or for an ordinary error.
func TestNewAuthorDiscoverer_MapsErrors(t *testing.T) {
	cases := []struct {
		name        string
		created     int
		err         error
		wantBackoff bool
		wantBusy    bool
		wantUnavail bool
	}{
		{name: "success", created: 3},
		{name: "daily quota", err: &metadata.DailyQuotaError{}, wantBackoff: true},
		{name: "ordinary error", err: errors.New("provider 500")},
		{name: "hardcover rate limit, wrapped", err: fmt.Errorf("author works: %w", hardcover.ErrRateLimited), wantBackoff: true},
		{name: "openlibrary rate limit, wrapped", err: fmt.Errorf("author works: %w", openlibrary.ErrRateLimited), wantBackoff: true},
		{name: "sync already running", err: api.ErrAuthorSyncRunning, wantBusy: true},
		{name: "openlibrary server error", err: fmt.Errorf("works: %w", openlibrary.ErrUnavailable), wantUnavail: true},
		{name: "network failure", err: &url.Error{Op: "Get", URL: "https://openlibrary.org", Err: errors.New("connection refused")}, wantUnavail: true},
		{name: "provider call timeout", err: fmt.Errorf("works: %w", context.DeadlineExceeded), wantUnavail: true},
		{name: "not found is about the author", err: fmt.Errorf("works: %w", openlibrary.ErrNotFound)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newAuthorDiscoverer(func(context.Context, *models.Author) (int, error) {
				return tc.created, tc.err
			}, nil)
			out := d.DiscoverAuthor(context.Background(), &models.Author{ID: 1})
			if out.Created != tc.created || !errors.Is(out.Err, tc.err) || out.Backoff != tc.wantBackoff || out.Busy != tc.wantBusy || out.Unavailable != tc.wantUnavail {
				t.Errorf("outcome = %+v, want created %d, backoff %v, busy %v, unavailable %v", out, tc.created, tc.wantBackoff, tc.wantBusy, tc.wantUnavail)
			}
		})
	}
}

func TestNewAuthorDiscoverer_BulkRunning(t *testing.T) {
	noop := func(context.Context, *models.Author) (int, error) { return 0, nil }
	if newAuthorDiscoverer(noop, nil).BulkRefreshRunning() {
		t.Error("nil bulk check reported a running bulk refresh")
	}
	if !newAuthorDiscoverer(noop, func() bool { return true }).BulkRefreshRunning() {
		t.Error("bulk check was not consulted")
	}
}
