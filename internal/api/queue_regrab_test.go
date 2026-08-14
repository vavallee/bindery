package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// TestRegrabbableState pins exactly which existing download states release
// their GUID for a fresh grab (#1955). The set must contain only states that
// are dead to every automatic path: anything still in flight would be
// duplicated or clobbered by a re-grab.
func TestRegrabbableState(t *testing.T) {
	tests := []struct {
		status models.DownloadState
		want   bool
	}{
		{models.StateFailed, true},
		// #1955: terminal to the pollers, so without this the row pins the GUID
		// forever and every later Grab answers "already grabbed".
		{models.StateImportBlocked, true},
		// Still live work — a re-grab would race the scanner or duplicate the
		// torrent.
		{models.StateGrabbed, false},
		{models.StateDownloading, false},
		{models.StateCompleted, false},
		{models.StateImportPending, false},
		{models.StateImporting, false},
		{models.StateImportFailed, false},
		{models.StateImported, false},
		{models.StateImportExternal, false},
		{models.StateImportHeld, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			if got := regrabbableState(tc.status); got != tc.want {
				t.Errorf("regrabbableState(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

// TestAlreadyGrabbedDetail proves the refusal explains itself. The reporter of
// #1955 saw only the bare "already grabbed" sentinel on a screen that shows no
// queue state, so the message must name the situation and point somewhere.
func TestAlreadyGrabbedDetail(t *testing.T) {
	tests := []struct {
		status  models.DownloadState
		want    []string
		notWant []string
	}{
		{status: models.StateImported, want: []string{"already been imported"}},
		{
			status: models.StateImportFailed,
			want:   []string{"retrying", "Retry import"},
			// The message must offer an action rather than tell the user to
			// wait: "wait for it to settle" was advice for an outcome that,
			// before the skip limit, could never arrive.
			notWant: []string{"wait"},
		},
		{status: models.StateImportExternal, want: []string{"external import tool"}},
		{status: models.StateImportHeld, want: []string{"external import tool"}},
		{status: models.StateDownloading, want: []string{"already in the queue", "downloading"}},
		{status: models.StateGrabbed, want: []string{"already in the queue", "grabbed"}},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			got := alreadyGrabbedDetail(tc.status)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("alreadyGrabbedDetail(%q) = %q, want it to mention %q", tc.status, got, want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(strings.ToLower(got), notWant) {
					t.Errorf("alreadyGrabbedDetail(%q) = %q, must not mention %q", tc.status, got, notWant)
				}
			}
		})
	}
}

// TestQueueGrab_ImportBlockedIsRegrabbable replays flaevers' report (#1955).
//
// Their audiobook download exhausted its import retry budget and was terminally
// blocked. Clicking Grab on the same release from the search page then answered
// 409 "already grabbed" — the row pinned the GUID and there was no way forward
// from that screen. A blocked download must release the release for a re-grab,
// reusing the same row with a clean error message and a fresh retry budget.
func TestQueueGrab_ImportBlockedIsRegrabbable(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	defer indexerSrv.Close()
	addCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-1955"}})
	}))
	defer srv.Close()

	h, _, downloads, clients, _, ctx := queueFixture(t)
	host, port := testServerHostPort(t, srv.URL)
	if err := clients.Create(ctx, &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true,
	}); err != nil {
		t.Fatalf("create client: %v", err)
	}

	blocked := &models.Download{
		GUID:         "guid-1955",
		Title:        "Harriet Tubman: Live in Concert by Bob the Drag Queen [ENG / M4B]",
		NZBURL:       indexerSrv.URL + "/old.nzb",
		Status:       models.StateImportBlocked,
		Protocol:     "usenet",
		ErrorMessage: "import retry limit reached (5 attempts) — fix the underlying problem, then retry manually",
	}
	if err := downloads.Create(ctx, blocked); err != nil {
		t.Fatal(err)
	}
	if err := downloads.IncrementImportRetryCount(ctx, blocked.ID); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"guid":"guid-1955","nzbUrl":"` + indexerSrv.URL + `/new.nzb","title":"New","size":7}`)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("#1955 regression: a terminally blocked download must not block a re-grab; got %d: %s",
			rec.Code, rec.Body.String())
	}
	if addCalls != 1 {
		t.Fatalf("expected the release to be sent to the download client once, got %d calls", addCalls)
	}

	got, err := downloads.GetByGUID(ctx, "guid-1955")
	if err != nil || got == nil {
		t.Fatalf("reload download: %v", err)
	}
	if got.ID != blocked.ID {
		t.Errorf("expected the re-grab to reuse row %d, got %d", blocked.ID, got.ID)
	}
	if got.Status == models.StateImportBlocked {
		t.Errorf("expected the row to leave importBlocked after the re-grab, still %q", got.Status)
	}
	if got.ErrorMessage != "" {
		t.Errorf("expected the stale blocking reason cleared, got %q", got.ErrorMessage)
	}
	if got.ImportRetryCount != 0 {
		t.Errorf("expected a fresh retry budget after the re-grab, got %d", got.ImportRetryCount)
	}
	if got.Title != "New" || got.NZBURL != indexerSrv.URL+"/new.nzb" {
		t.Errorf("expected the re-grab to refresh the release fields, got title=%q url=%q", got.Title, got.NZBURL)
	}
}

// TestQueueGrab_LiveDownloadStillBlocksRegrabWithReason is the other half of
// #1955: widening the re-grab must not open a hole for downloads that are
// still live. The 409 stays, and now carries the explanation the search page
// renders instead of the bare sentinel.
func TestQueueGrab_LiveDownloadStillBlocksRegrabWithReason(t *testing.T) {
	for _, tc := range []struct {
		status models.DownloadState
		want   string
	}{
		{models.StateDownloading, "already in the queue"},
		{models.StateImported, "already been imported"},
		{models.StateImportFailed, "Retry import"},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			h, _, downloads, _, _, ctx := queueFixture(t)
			if err := downloads.Create(ctx, &models.Download{
				GUID: "live-guid", Title: "T", Protocol: "usenet", Status: tc.status,
			}); err != nil {
				t.Fatal(err)
			}

			body := bytes.NewBufferString(`{"guid":"live-guid","nzbUrl":"http://example/x.nzb","title":"T"}`)
			rec := httptest.NewRecorder()
			h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", body))
			if rec.Code != http.StatusConflict {
				t.Fatalf("expected 409 for a %s download, got %d: %s", tc.status, rec.Code, rec.Body.String())
			}
			var payload map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(payload["error"], "already grabbed") {
				t.Errorf("expected the sentinel preserved for clients matching on it, got %q", payload["error"])
			}
			if !strings.Contains(payload["error"], tc.want) {
				t.Errorf("expected the 409 body to explain why (%q), got %q", tc.want, payload["error"])
			}
		})
	}
}
