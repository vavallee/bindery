package calibre

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// SyncError is a per-book failure entry returned to the UI.
type SyncError struct {
	BookID int64  `json:"bookId"`
	Title  string `json:"title"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason"`
}

// SyncStats summarises one bulk-push run. Totals are cumulative across
// every imported book with a file path; `Pushed` counts newly-added,
// `AlreadyInCalibre` counts 409 Conflict responses (treated as success
// for idempotency), and `Failed` counts everything else.
type SyncStats struct {
	Total            int `json:"total"`
	Processed        int `json:"processed"`
	Pushed           int `json:"pushed"`
	AlreadyInCalibre int `json:"alreadyInCalibre"`
	Failed           int `json:"failed"`
}

// SyncProgress is the polled shape for /calibre/sync/status. Running=false
// with a non-nil FinishedAt means the last run is complete; Running=false
// with StartedAt zero means nothing has been run yet this process.
type SyncProgress struct {
	Running    bool        `json:"running"`
	StartedAt  time.Time   `json:"startedAt"`
	FinishedAt *time.Time  `json:"finishedAt,omitempty"`
	Message    string      `json:"message,omitempty"`
	Error      string      `json:"error,omitempty"`
	Stats      SyncStats   `json:"stats"`
	Errors     []SyncError `json:"errors"`
}

// pluginPusher captures the subset of *PluginClient the syncer needs, so
// tests can inject a fake without standing up an HTTP server.
type pluginPusher interface {
	Add(ctx context.Context, filePath string) (int64, error)
}

// BookLister is the subset of *db.BookRepo the syncer uses. Keeps the
// dependency narrow for tests.
type BookLister interface {
	ListByStatus(ctx context.Context, status string) ([]models.Book, error)
	SetCalibreID(ctx context.Context, id, calibreID int64) error
}

// Syncer orchestrates the "Push all to Calibre" bulk job. One sync runs
// at a time — a second call returns ErrSyncAlreadyRunning. Progress is
// mutex-protected and can be polled concurrently with the running job.
type Syncer struct {
	books    BookLister
	newClient func(cfg Config) pluginPusher

	mu       sync.Mutex
	running  bool
	progress SyncProgress
}

// NewSyncer wires a syncer against the books repo. The plugin client is
// built per-run from the current settings so mode/URL changes take effect
// without restarting Bindery.
func NewSyncer(books BookLister) *Syncer {
	return &Syncer{
		books: books,
		newClient: func(cfg Config) pluginPusher {
			return NewPluginClient(cfg.PluginURL, cfg.PluginAPIKey)
		},
	}
}

// ErrSyncAlreadyRunning is returned when Start is called while a previous
// sync is still executing. Maps to 409 Conflict at the API layer.
var ErrSyncAlreadyRunning = errors.New("calibre sync already running")

// ErrSyncModeNotPlugin is returned when Start is called while the Calibre
// integration is not in plugin mode — bulk push only makes sense against
// the plugin, not the calibredb CLI (which already lives next to the
// library file on the Bindery host).
var ErrSyncModeNotPlugin = errors.New("calibre sync requires mode=plugin")

// Running reports whether a sync is currently in flight.
func (s *Syncer) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Progress returns a snapshot of the current (or most recent) sync.
func (s *Syncer) Progress() SyncProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Defensive copy of the slice so a later append by the worker does
	// not mutate a snapshot already handed to the HTTP layer.
	snap := s.progress
	if len(s.progress.Errors) > 0 {
		snap.Errors = append([]SyncError(nil), s.progress.Errors...)
	}
	return snap
}

// Start launches a sync in the background. Caller passes
// context.WithoutCancel(r.Context()) so the HTTP response-send doesn't
// cancel the long-running job.
func (s *Syncer) Start(ctx context.Context, cfg Config, mode Mode) error {
	if mode != ModePlugin {
		return ErrSyncModeNotPlugin
	}
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

	go s.run(ctx, cfg)
	return nil
}

func (s *Syncer) run(ctx context.Context, cfg Config) {
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

	// Restrict to books that actually have a file on disk. A book row with
	// status=imported but empty file_path means the importer crashed or the
	// file was deleted out from under us — nothing to push.
	eligible := make([]models.Book, 0, len(books))
	for i := range books {
		if pushPath(&books[i]) != "" {
			eligible = append(eligible, books[i])
		}
	}

	s.setProgress(func(p *SyncProgress) {
		p.Stats.Total = len(eligible)
		if len(eligible) == 0 {
			p.Message = "no imported books with files to push"
		} else {
			p.Message = "pushing books to Calibre…"
		}
	})

	client := s.newClient(cfg)

	for i := range eligible {
		if err := ctx.Err(); err != nil {
			s.fail("cancelled: " + err.Error())
			return
		}
		b := &eligible[i]
		path := pushPath(b)
		id, addErr := client.Add(ctx, path)
		switch {
		case addErr == nil:
			if id > 0 {
				if perr := s.books.SetCalibreID(ctx, b.ID, id); perr != nil {
					slog.Warn("calibre sync: persist calibre_id failed", "bookId", b.ID, "calibreId", id, "error", perr)
				}
			}
			s.setProgress(func(p *SyncProgress) {
				p.Stats.Pushed++
				p.Stats.Processed++
			})
		case errors.Is(addErr, ErrAlreadyInCalibre):
			if id > 0 {
				if perr := s.books.SetCalibreID(ctx, b.ID, id); perr != nil {
					slog.Warn("calibre sync: persist calibre_id failed", "bookId", b.ID, "calibreId", id, "error", perr)
				}
			}
			s.setProgress(func(p *SyncProgress) {
				p.Stats.AlreadyInCalibre++
				p.Stats.Processed++
			})
		default:
			s.setProgress(func(p *SyncProgress) {
				p.Stats.Failed++
				p.Stats.Processed++
				p.Errors = append(p.Errors, SyncError{
					BookID: b.ID,
					Title:  b.Title,
					Path:   path,
					Reason: addErr.Error(),
				})
			})
		}
	}

	s.setProgress(func(p *SyncProgress) {
		p.Message = "done"
	})
	slog.Info("calibre sync complete",
		"total", len(eligible),
		"pushed", s.progress.Stats.Pushed,
		"alreadyInCalibre", s.progress.Stats.AlreadyInCalibre,
		"failed", s.progress.Stats.Failed)
}

// pushPath returns the on-disk path to send to Calibre for the given
// book, preferring the ebook-specific column (populated by dual-format
// imports) and falling back to the legacy single file_path.
func pushPath(b *models.Book) string {
	if b.EbookFilePath != "" {
		return b.EbookFilePath
	}
	return b.FilePath
}

func (s *Syncer) fail(msg string) {
	slog.Error("calibre sync failed", "error", msg)
	s.setProgress(func(p *SyncProgress) {
		p.Error = msg
		p.Message = "failed"
	})
}

func (s *Syncer) setProgress(mutate func(*SyncProgress)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mutate(&s.progress)
}
