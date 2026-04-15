package calibre

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DropFolderConfig is the subset of Calibre settings the drop-folder writer
// needs. DropFolderPath is the Calibre-watched directory (the "Add books to
// library from folders" target); LibraryPath is the same metadata.db
// location the calibredb flow uses, needed for the post-ingest lookup.
type DropFolderConfig struct {
	DropFolderPath string
	LibraryPath    string

	// PollInterval + PollAttempts control how patiently we wait for Calibre
	// to pick up the dropped file. Calibre's folder-watch runs on a timer
	// (default 60s, often shortened to a few seconds) so the default
	// 10 × 3s = 30s total budget covers the common case without blocking
	// the import goroutine for long. Zero values fall back to the defaults.
	PollInterval time.Duration
	PollAttempts int
}

// DefaultPollInterval / DefaultPollAttempts — 10 × 3s is enough for
// Calibre's folder-watch at its typical tick rate and still returns within
// the importer's per-book budget. Operators who run Calibre with the
// watched-folder poll turned up to a minute will need to tune these.
const (
	DefaultPollInterval = 3 * time.Second
	DefaultPollAttempts = 10
)

// ErrDropFolderNotConfigured is returned when the writer is asked to ingest
// a file but no drop folder has been set in settings. Callers treat it as
// a soft-skip (same semantics as ErrDisabled for the calibredb client).
var ErrDropFolderNotConfigured = errors.New("calibre drop_folder_path is not configured")

// DropFolderWriter copies newly-imported files into a Calibre-watched
// directory and then polls metadata.db until Calibre assigns a book id.
// The zero value is unusable; construct with NewDropFolderWriter.
type DropFolderWriter struct {
	cfg    DropFolderConfig
	lookup lookupFn
	// now is injected so tests can keep backoff deterministic without
	// waiting seconds on the wall clock.
	now func() time.Time
}

// lookupFn abstracts LookupByTitleAuthor so tests can drive the poller with
// predictable found/not-found sequences instead of seeding a live sqlite.
type lookupFn func(ctx context.Context, libraryPath, title, author string) (int64, bool, error)

// NewDropFolderWriter builds a writer against cfg. The real
// LookupByTitleAuthor is used for metadata.db reads; tests can use
// newDropFolderWriterFunc to stub it.
func NewDropFolderWriter(cfg DropFolderConfig) *DropFolderWriter {
	return &DropFolderWriter{cfg: cfg, lookup: LookupByTitleAuthor, now: time.Now}
}

// IngestResult reports what happened for a single Ingest call. Returned
// (instead of raw errors) so the caller can record a metric like
// "dropped-and-found" vs "dropped-but-timeout" without parsing strings.
type IngestResult struct {
	// DroppedPath is where the file now lives inside the drop folder.
	// Populated even on a poll timeout — Calibre may still ingest it later.
	DroppedPath string

	// CalibreID is the id Calibre assigned to the dropped file. Non-zero
	// only when Found is true; callers should persist this on the book row.
	CalibreID int64

	// Found reports whether the poll succeeded within the budget. A false
	// value means Calibre hadn't picked up the file by the time we gave up;
	// the importer treats this as a warn-and-continue rather than an error.
	Found bool
}

// Ingest copies srcPath into the watched drop folder using the
// `<drop>/<Author>/<Title>.ext` layout Calibre expects, then polls
// metadata.db for a matching row. title + author drive both the file name
// and the lookup query, so operators can pass whatever identifier pair
// will appear in Calibre after ingest.
//
// The returned error distinguishes "could not drop the file" (hard fail —
// either a missing config or an I/O problem) from "dropped but Calibre
// didn't pick it up in time" (returned as a result with Found=false and
// nil error). The importer's caller relies on that distinction to choose
// between "warn" and "skip the Calibre mirror entirely".
func (w *DropFolderWriter) Ingest(ctx context.Context, srcPath, title, author string) (IngestResult, error) {
	if w.cfg.DropFolderPath == "" {
		return IngestResult{}, ErrDropFolderNotConfigured
	}
	if srcPath == "" {
		return IngestResult{}, errors.New("drop folder: empty source path")
	}
	if title == "" || author == "" {
		return IngestResult{}, errors.New("drop folder: title and author are required")
	}

	ext := filepath.Ext(srcPath)
	destDir := filepath.Join(w.cfg.DropFolderPath, sanitizeSegment(author))
	destPath := filepath.Join(destDir, sanitizeSegment(title)+ext)

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return IngestResult{}, fmt.Errorf("drop folder: mkdir %q: %w", destDir, err)
	}
	if err := copyFile(srcPath, destPath); err != nil {
		return IngestResult{}, fmt.Errorf("drop folder: copy to %q: %w", destPath, err)
	}

	slog.Info("calibre drop-folder: file written",
		"src", srcPath, "dst", destPath, "title", title, "author", author)

	id, found := w.pollForBook(ctx, title, author)
	return IngestResult{DroppedPath: destPath, CalibreID: id, Found: found}, nil
}

// pollForBook loops LookupByTitleAuthor at the configured cadence until the
// book appears or the attempt budget is exhausted. Lookup errors mid-poll
// (e.g. Calibre transiently holds the DB open for a write) are logged at
// debug but don't short-circuit — the next tick usually succeeds.
func (w *DropFolderWriter) pollForBook(ctx context.Context, title, author string) (int64, bool) {
	interval := w.cfg.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	attempts := w.cfg.PollAttempts
	if attempts <= 0 {
		attempts = DefaultPollAttempts
	}

	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			return 0, false
		}
		id, found, err := w.lookup(ctx, w.cfg.LibraryPath, title, author)
		if err != nil {
			slog.Debug("calibre drop-folder: lookup transient error",
				"attempt", i+1, "error", err)
		}
		if found {
			return id, true
		}
		// Sleep between attempts but stay cancel-aware. A cancelled
		// context from shutdown should drop out immediately rather than
		// waiting out the remaining budget.
		select {
		case <-ctx.Done():
			return 0, false
		case <-time.After(interval):
		}
	}
	return 0, false
}

// sanitizeSegment strips characters that break path segments on the usual
// filesystems. This is intentionally narrower than importer.sanitizePath
// (which also trims) so the drop-folder layout stays predictable: Calibre
// reads back whatever we put on disk, so "Author/Title" must survive the
// round-trip without surprise transformation. Exported via copyFile.
func sanitizeSegment(s string) string {
	// Same set as importer.sanitizePath — kept local so the calibre
	// package doesn't need to import importer (which would cycle).
	replacer := strings.NewReplacer(
		"/", "-", "\\", "-", ":", "-", "*", "", "?", "",
		"\"", "", "<", "", ">", "", "|", "",
	)
	return strings.TrimSpace(replacer.Replace(s))
}

// copyFile performs a streaming copy from src to dst. Keeping this local
// (rather than importing importer.copyFile) avoids a package cycle — the
// scanner imports calibre, so calibre cannot import importer.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
