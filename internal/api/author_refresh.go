package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// SettingAuthorBulkRefresh is the settings key under which the "refresh all
// authors" background job persists its progress as a JSON string. Mirrors the
// "library.lastScan" convention so the status survives a page reload.
const SettingAuthorBulkRefresh = "authors.bulkRefresh"

// refreshStatusKey is the settings key a run's status lives under. A run
// started by a non admin under tenancy covers only that user's authors and
// keeps its status under a key of its own, so one user never reads another
// user's run or the instance wide author count. scope 0 is the instance run.
func refreshStatusKey(scope int64) string {
	if scope == 0 {
		return SettingAuthorBulkRefresh
	}
	return db.AuthorBulkRefreshUserKey(scope)
}

// authorLister is the subset of *db.AuthorRepo used to enumerate every author
// for the bulk refresh. Narrowed to one method so tests can stub it.
type authorLister interface {
	List(ctx context.Context) ([]models.Author, error)
}

// AuthorRefreshHandler runs a background job that refreshes metadata for every
// author, reporting progress through a JSON status persisted in the settings
// table. This is the "Refresh all" / first-run "populate everything I just
// imported" path; the per-selection bulk refresh lives in bulk.go and is
// independent. The two never share state.
//
// Modelled on LibraryHandler.Scan/ScanStatus: a single background goroutine
// launched under context.WithoutCancel, progress persisted as a JSON string,
// and a status endpoint that returns that JSON verbatim (or 404).
type AuthorRefreshHandler struct {
	authors  authorLister
	settings *db.SettingsRepo

	// refresh repopulates a single author's catalogue from the metadata
	// provider (metadata only, never auto-grab). Wire it to the same closure
	// the per-author Refresh handler uses. Nil when not available, in which
	// case RefreshAll is rejected with 400.
	refresh func(*models.Author)

	// mu guards running. running is the in-memory truth of whether a job is
	// active in *this* process; the persisted status JSON is the cross-restart
	// record. The two can disagree after a crash/restart (status says
	// "running" but no goroutine exists) — RefreshAllStatus reconciles that.
	mu      sync.Mutex
	running bool
	// runScope is the user the active (or next) run is limited to, 0 for
	// every author. Set with running, under mu.
	runScope int64
}

// NewAuthorRefreshHandler builds the handler with the author lister and the
// per-author refresh callback. Pass a nil refresh to disable the endpoint
// (RefreshAll then returns 400).
func NewAuthorRefreshHandler(authors authorLister, refresh func(*models.Author)) *AuthorRefreshHandler {
	return &AuthorRefreshHandler{authors: authors, refresh: refresh}
}

// WithSettings attaches a SettingsRepo so the handler can persist and serve
// refresh progress. Chainable to match LibraryHandler's style.
func (h *AuthorRefreshHandler) WithSettings(sr *db.SettingsRepo) *AuthorRefreshHandler {
	h.settings = sr
	return h
}

// authorRefreshStatus is the JSON payload persisted under
// SettingAuthorBulkRefresh and served verbatim by RefreshAllStatus.
type authorRefreshStatus struct {
	Status      string `json:"status"` // "running" | "completed" | "failed"
	Total       int    `json:"total"`
	Done        int    `json:"done"`
	Failed      int    `json:"failed"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at,omitempty"`
	Message     string `json:"message,omitempty"`
}

// RefreshAll launches the background refresh-all-authors job and returns 202.
// Rejects with 400 if no refresh callback is wired, or 409 if a job is already
// running in this process.
func (h *AuthorRefreshHandler) RefreshAll(w http.ResponseWriter, r *http.Request) {
	if h.refresh == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "refresh not available"})
		return
	}

	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a metadata refresh is already running"})
		return
	}
	h.running = true
	// Under tenancy a non admin refreshes only the authors they own. Shared
	// (unowned) authors are left out: refreshing one rewrites a catalogue
	// every user sees, which is an admin's call.
	h.runScope = auth.ListScopeUserID(r.Context())
	h.mu.Unlock()

	// context.WithoutCancel so the goroutine isn't killed when the HTTP
	// response is sent and the request context is cancelled.
	go h.run(context.WithoutCancel(r.Context()))
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "metadata refresh started"})
}

// Running reports whether a refresh-all job is in progress in this process.
// Scheduled discovery skips its tick while one is, since the job already runs
// the same sync over every author (#2236).
func (h *AuthorRefreshHandler) Running() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running
}

// run is the background job. It enumerates every author (or, for a run
// limited to one user, the authors that user owns) and refreshes them
// sequentially (provider rate limits — never parallelise), tolerating a panic
// in any single author's refresh. Progress is persisted to the settings table
// after each author; the terminal state ("completed"/"failed") is always
// written last.
func (h *AuthorRefreshHandler) run(ctx context.Context) {
	h.mu.Lock()
	scope := h.runScope
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.running = false
		h.runScope = 0
		h.mu.Unlock()
	}()

	authors, err := h.authors.List(ctx)
	if err == nil && scope != 0 {
		own := authors[:0:0]
		for _, a := range authors {
			if a.OwnerUserID == scope {
				own = append(own, a)
			}
		}
		authors = own
	}
	if err != nil {
		slog.Error("refresh all authors: list failed", "error", err)
		h.persist(ctx, scope, authorRefreshStatus{
			Status:    "failed",
			StartedAt: time.Now().UTC().Format(time.RFC3339),
			Message:   "failed to list authors: " + err.Error(),
		})
		return
	}

	status := authorRefreshStatus{
		Status:    "running",
		Total:     len(authors),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	h.persist(ctx, scope, status)

	for i := range authors {
		// Loop var is per-iteration in Go 1.22+, so &authors[i] is safe to
		// capture; use the index form to be explicit about it.
		author := authors[i]
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					status.Failed++
					slog.Error("refresh all authors: refresh panicked",
						"author_id", author.ID, "author", author.Name, "panic", rec)
				}
			}()
			h.refresh(&author)
		}()
		status.Done++

		// Persist after each author so progress survives a page reload. On a
		// huge library this is one settings UPSERT per author, which is cheap
		// relative to a provider round-trip per author; the bottleneck is the
		// network refresh, not the local write.
		h.persist(ctx, scope, status)
	}

	status.Status = "completed"
	status.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	h.persist(ctx, scope, status)
}

// persist writes the status JSON to the settings table. Failures are logged
// but never abort the run — a missed progress write is recoverable; aborting
// mid-refresh is not.
func (h *AuthorRefreshHandler) persist(ctx context.Context, scope int64, status authorRefreshStatus) {
	if h.settings == nil {
		return
	}
	b, err := json.Marshal(status)
	if err != nil {
		slog.Warn("refresh all authors: marshal status failed", "error", err)
		return
	}
	if err := h.settings.Set(ctx, refreshStatusKey(scope), string(b)); err != nil {
		slog.Warn("refresh all authors: persist status failed", "error", err)
	}
}

// RefreshAllStatus serves the last refresh-all progress as JSON, or a 200
// {"status":"idle"} when no job has ever run. (It previously 404'd for "no job
// run", which the Authors page polls on every load — surfacing a 404 per page
// view and conflating "no job" with a real error; idle is the honest signal.)
// If the stored status says "running" but no job is actually running in this
// process (e.g. the server restarted mid-job), it is reported as "failed" with
// an "interrupted by restart" message so the UI banner does not hang forever.
// The reconciled value is written back once so subsequent reads are consistent.
//
// Under tenancy a non admin is served their own run's status only (see
// refreshStatusKey), and idle when they never started one.
func (h *AuthorRefreshHandler) RefreshAllStatus(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "idle"})
		return
	}
	scope := auth.ListScopeUserID(r.Context())
	key := refreshStatusKey(scope)
	setting, err := h.settings.Get(r.Context(), key)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if setting == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "idle"})
		return
	}

	value := setting.Value

	// Reconcile a stale "running": if the persisted status claims a job is in
	// flight but this process has no active goroutine, the job was interrupted
	// by a restart. Rewrite it as "failed" once so the banner clears.
	var status authorRefreshStatus
	if err := json.Unmarshal([]byte(value), &status); err == nil && status.Status == "running" {
		h.mu.Lock()
		running := h.running && h.runScope == scope
		h.mu.Unlock()
		if !running {
			status.Status = "failed"
			status.Message = "interrupted by restart"
			if b, mErr := json.Marshal(status); mErr == nil {
				value = string(b)
				if sErr := h.settings.Set(r.Context(), key, value); sErr != nil {
					slog.Warn("refresh all authors: reconcile stale running failed", "error", sErr)
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(value)); err != nil {
		slog.Warn("failed to write author refresh status", "error", err)
	}
}
