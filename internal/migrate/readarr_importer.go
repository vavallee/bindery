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

	// Catalogue backfill. After the database import finishes, the catalogue of
	// each author it added is fetched in the background, 3 seconds per author.
	// These stay readable (and keep moving) after running turns false, and
	// reset when the next import starts. In memory only: a restart zeroes them.
	CatalogueRunning bool `json:"catalogueRunning"`
	CatalogueTotal   int  `json:"catalogueTotal"`
	CatalogueDone    int  `json:"catalogueDone"`
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
	settings    *db.SettingsRepo
	meta        *metadata.Aggregator
	onNewAuthor func(*models.Author)

	mu       sync.Mutex
	running  bool
	progress ReadarrProgress

	// catalogue counts the detached catalogue backfill, which outlives the
	// import goroutine. It has its own lock.
	catalogue catalogueTracker

	// testStartHook, if set, is called at goroutine start before import work
	// begins. Tests use it to block the goroutine and exercise the 409 guard.
	testStartHook func()
}

// NewReadarrImporter wires the repos the import writes to.
func NewReadarrImporter(
	authors *db.AuthorRepo,
	indexers *db.IndexerRepo,
	clients *db.DownloadClientRepo,
	blocklist *db.BlocklistRepo,
	settings *db.SettingsRepo,
	meta *metadata.Aggregator,
	onNewAuthor func(*models.Author),
) *ReadarrImporter {
	return &ReadarrImporter{
		authors:     authors,
		indexers:    indexers,
		clients:     clients,
		blocklist:   blocklist,
		settings:    settings,
		meta:        meta,
		onNewAuthor: onNewAuthor,
	}
}

// SetTestStartHook registers a function to be called at goroutine start,
// before any import work begins. Call before Start. Used only in tests to
// synchronize the goroutine with the test's second HTTP request.
func (imp *ReadarrImporter) SetTestStartHook(fn func()) { imp.testStartHook = fn }

// Progress returns a snapshot of the current (or most recent) import.
// Safe to call at any time.
func (imp *ReadarrImporter) Progress() ReadarrProgress {
	imp.mu.Lock()
	p := imp.progress
	imp.mu.Unlock()
	c := imp.catalogue.snapshot()
	p.CatalogueRunning, p.CatalogueTotal, p.CatalogueDone = c.Running, c.Total, c.Done
	return p
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
	imp.catalogue.reset()

	go func() {
		defer os.Remove(tmpPath) // always clean up the spool file

		if imp.testStartHook != nil {
			imp.testStartHook()
		}
		slog.Info("readarr import: starting", "tmpPath", tmpPath)
		res, err := ImportReadarr(withCatalogueTracker(ctx, &imp.catalogue), tmpPath,
			imp.authors, imp.indexers, imp.clients, imp.blocklist, imp.settings, imp.meta, imp.onNewAuthor)

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
