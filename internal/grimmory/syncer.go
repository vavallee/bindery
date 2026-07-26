package grimmory

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/models"
)

// SyncError is a per-book failure entry returned to the UI.
type SyncError struct {
	BookID int64  `json:"bookId"`
	Title  string `json:"title"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason"`
}

// maxSyncErrors caps the per-book error list kept in SyncProgress.Errors, for
// the same reason as the Calibre syncer (#1346): a run that fails on every
// book would otherwise re-serialize thousands of near-identical entries on
// each status poll. Stats.Failed always carries the full count.
const maxSyncErrors = 50

// SyncStats summarises one bulk-push run. AlreadyPushed counts files skipped
// because a previous push recorded them (grimmory_pushes) — the idempotency
// path, treated as success.
type SyncStats struct {
	Total         int `json:"total"`
	Processed     int `json:"processed"`
	Pushed        int `json:"pushed"`
	AlreadyPushed int `json:"alreadyPushed"`
	Failed        int `json:"failed"`
}

// SyncProgress is the polled shape for /grimmory/sync/status. Running=false
// with a non-nil FinishedAt means the last run is complete; Running=false
// with StartedAt zero means nothing has run yet this process.
type SyncProgress struct {
	Running    bool        `json:"running"`
	StartedAt  time.Time   `json:"startedAt"`
	FinishedAt *time.Time  `json:"finishedAt,omitempty"`
	Message    string      `json:"message,omitempty"`
	Error      string      `json:"error,omitempty"`
	Stats      SyncStats   `json:"stats"`
	Errors     []SyncError `json:"errors"`
}

// BookLister is the subset of *db.BookRepo the syncer uses.
type BookLister interface {
	ListByStatus(ctx context.Context, status string) ([]models.Book, error)
}

// Syncer orchestrates the "Push library to Grimmory" bulk job. One sync runs
// at a time — a second Start returns ErrSyncAlreadyRunning. Progress is
// mutex-protected and can be polled concurrently with the running job.
type Syncer struct {
	books  BookLister
	pusher *Pusher

	// jobs, when set, tracks the detached sync goroutine so process shutdown
	// can cancel and drain it before the database closes (#1458). A sync dying
	// mid-upload would otherwise re-push everything next run (BookDrop has no
	// server-side dedup). When nil, Start falls back to an untracked goroutine.
	jobs *jobs.Group

	mu       sync.Mutex
	running  bool
	progress SyncProgress
}

// NewSyncer wires a syncer against the books repo and the shared pusher, so
// bulk pushes reuse the pusher's client (and its JWT session) and its
// idempotency store.
func NewSyncer(books BookLister, pusher *Pusher) *Syncer {
	return &Syncer{books: books, pusher: pusher}
}

// WithJobs registers the process-wide background-jobs group so a Start()-launched
// sync is tracked and drained on shutdown before the database closes (#1458).
func (s *Syncer) WithJobs(g *jobs.Group) *Syncer {
	s.jobs = g
	return s
}

// Progress returns a snapshot of the current (or most recent) sync.
func (s *Syncer) Progress() SyncProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.progress
	if len(s.progress.Errors) > 0 {
		snap.Errors = append([]SyncError(nil), s.progress.Errors...)
	}
	return snap
}

// Start launches a sync in the background. Callers pass
// context.WithoutCancel(r.Context()) so the HTTP response-send doesn't cancel
// the long-running job.
func (s *Syncer) Start(ctx context.Context, cfg PushConfig) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrSyncAlreadyRunning
	}
	s.running = true
	s.progress = SyncProgress{
		Running:   true,
		StartedAt: time.Now().UTC(),
		Message:   "listing imported books…",
		Errors:    []SyncError{},
	}
	s.mu.Unlock()

	// See the abs importer for the rationale: when a jobs group is wired the
	// sync runs on the shutdown-scoped context so SIGTERM cancels and drains it
	// before the DB closes, instead of the never-cancelled WithoutCancel(request)
	// context. Fall back to an untracked goroutine for tests/non-wired callers.
	if s.jobs != nil {
		s.jobs.Go("grimmory-sync", func(ctx context.Context) { s.run(ctx, cfg) })
	} else {
		go s.run(ctx, cfg)
	}
	return nil
}

func (s *Syncer) run(ctx context.Context, cfg PushConfig) {
	defer func() {
		now := time.Now().UTC()
		s.mu.Lock()
		s.progress.Running = false
		s.progress.FinishedAt = &now
		s.running = false
		s.mu.Unlock()
	}()

	books, err := s.books.ListByStatus(ctx, models.BookStatusImported)
	if err != nil {
		s.fail("list imported books: " + err.Error())
		return
	}

	// Only ebook files for now (#826 open question 2 — start with one
	// format): BookDrop takes a single file per upload, and a multi-part
	// audiobook folder doesn't reduce to that shape.
	eligible := make([]models.Book, 0, len(books))
	for i := range books {
		if pushPath(&books[i]) != "" {
			eligible = append(eligible, books[i])
		}
	}

	s.setProgress(func(p *SyncProgress) {
		p.Stats.Total = len(eligible)
		if len(eligible) == 0 {
			p.Message = "no imported books with ebook files to push"
		} else {
			p.Message = "pushing books to Grimmory BookDrop…"
		}
	})

	// Log the first book to hit each distinct failure reason at WARN, so a
	// library-wide misconfiguration logs once instead of once per book
	// (the #1346 Calibre lesson).
	warned := map[string]bool{}

	for i := range eligible {
		b := &eligible[i]
		path := pushPath(b)
		outcome, pushErr := s.pusher.pushTracked(ctx, cfg, b.ID, path)
		s.setProgress(func(p *SyncProgress) {
			p.Stats.Processed++
			switch outcome {
			case OutcomePushed:
				p.Stats.Pushed++
			case OutcomeAlreadyPushed:
				p.Stats.AlreadyPushed++
			case OutcomeFailed:
				p.Stats.Failed++
				if len(p.Errors) < maxSyncErrors {
					p.Errors = append(p.Errors, SyncError{
						BookID: b.ID, Title: b.Title, Path: path, Reason: pushErr.Error(),
					})
				}
			}
		})
		if outcome == OutcomeFailed && !warned[pushErr.Error()] {
			warned[pushErr.Error()] = true
			slog.Warn("grimmory sync: push failed", "title", b.Title, "path", path, "error", pushErr)
		}
	}

	s.setProgress(func(p *SyncProgress) {
		p.Message = "sync complete"
	})
	final := s.Progress()
	slog.Info("grimmory sync complete",
		"total", final.Stats.Total, "pushed", final.Stats.Pushed,
		"alreadyPushed", final.Stats.AlreadyPushed, "failed", final.Stats.Failed,
		"distinctFailureReasons", len(warned))
}

func (s *Syncer) fail(msg string) {
	s.setProgress(func(p *SyncProgress) {
		p.Error = msg
		p.Message = "sync failed"
	})
	slog.Warn("grimmory sync failed", "error", msg)
}

func (s *Syncer) setProgress(fn func(*SyncProgress)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.progress)
}

// pushPath returns the ebook file to push for a book, preferring the
// per-format path over the legacy single-path column.
func pushPath(b *models.Book) string {
	if b.EbookFilePath != "" {
		return b.EbookFilePath
	}
	return b.FilePath
}
