package migrate

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// ErrAlreadyRunning is returned by ReadarrImporter.Start when a previous
// import is still in progress. The API layer maps this to 409 Conflict.
var ErrAlreadyRunning = errors.New("readarr import already running")

// ReadarrProgress is the poll shape for GET /api/v1/migrate/readarr/status.
type ReadarrProgress struct {
	Running    bool           `json:"running"`
	StartedAt  time.Time      `json:"startedAt"`
	Message    string         `json:"message,omitempty"`
	Error      string         `json:"error,omitempty"`
	FinishedAt *time.Time     `json:"finishedAt,omitempty"`
	Result     *ReadarrResult `json:"result,omitempty"`
}

// ReadarrImporter manages async execution of a Readarr DB import. A single
// instance is shared between the Start and Status HTTP handlers; the mutex
// ensures only one import runs at a time and the progress field is safe to
// read concurrently from the Status poller.
type ReadarrImporter struct {
	authors     *db.AuthorRepo
	indexers    *db.IndexerRepo
	clients     *db.DownloadClientRepo
	blocklist   *db.BlocklistRepo
	meta        *metadata.Aggregator
	onNewAuthor func(*models.Author)

	mu       sync.Mutex
	running  bool
	progress ReadarrProgress
}

// NewReadarrImporter wires the repos the import writes to.
func NewReadarrImporter(
	authors *db.AuthorRepo,
	indexers *db.IndexerRepo,
	clients *db.DownloadClientRepo,
	blocklist *db.BlocklistRepo,
	meta *metadata.Aggregator,
	onNewAuthor func(*models.Author),
) *ReadarrImporter {
	return &ReadarrImporter{
		authors:     authors,
		indexers:    indexers,
		clients:     clients,
		blocklist:   blocklist,
		meta:        meta,
		onNewAuthor: onNewAuthor,
	}
}

// Progress returns a snapshot of the current (or most recent) import.
// Safe to call at any time.
func (imp *ReadarrImporter) Progress() ReadarrProgress {
	imp.mu.Lock()
	defer imp.mu.Unlock()
	return imp.progress
}

// Start kicks off the import of the SQLite file at tmpPath in a goroutine
// and returns immediately. The goroutine owns tmpPath and deletes it on
// completion regardless of outcome. ctx must outlive the HTTP request that
// triggered it (pass context.WithoutCancel so the import survives
// response-send). Returns ErrAlreadyRunning (→ 409) if a previous import
// is still in progress.
func (imp *ReadarrImporter) Start(ctx context.Context, tmpPath string) error {
	imp.mu.Lock()
	if imp.running {
		imp.mu.Unlock()
		return ErrAlreadyRunning
	}
	imp.running = true
	imp.progress = ReadarrProgress{
		Running:   true,
		StartedAt: time.Now(),
		Message:   "import started — this may take several minutes",
	}
	imp.mu.Unlock()

	go func() {
		defer os.Remove(tmpPath) // always clean up the spool file

		slog.Info("readarr import: starting", "tmpPath", tmpPath)
		res, err := ImportReadarr(ctx, tmpPath,
			imp.authors, imp.indexers, imp.clients, imp.blocklist, imp.meta, imp.onNewAuthor)

		now := time.Now()
		imp.mu.Lock()
		imp.running = false
		imp.progress.Running = false
		imp.progress.FinishedAt = &now
		if err != nil {
			slog.Error("readarr import: failed", "error", err)
			imp.progress.Error = err.Error()
			imp.progress.Message = ""
		} else {
			slog.Info("readarr import: complete",
				"authors_added", res.Authors.Added,
				"indexers_added", res.Indexers.Added,
				"clients_added", res.DownloadClients.Added,
				"blocklist_added", res.Blocklist.Added,
			)
			imp.progress.Result = res
			imp.progress.Message = "import complete"
		}
		imp.mu.Unlock()
	}()

	return nil
}
