// Package importer moves completed downloads into the configured library
// directory using a configurable naming template, and reconciles pre-existing
// library files against the tracked book database.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// grimmoryPusher mirrors a just-imported ebook into Grimmory's BookDrop (#826).
// Narrow local interface, same pattern as calibreAdder below.
type grimmoryPusher interface {
	PushOnImport(ctx context.Context, bookID int64, title, filePath string)
}

// calibreAdder mirrors a just-imported file into Calibre via calibredb or the
// Bindery Bridge plugin. The scanner only invokes it when Calibre mode is on.
type calibreAdder interface {
	Add(ctx context.Context, filePath string, meta calibre.Metadata) (int64, error)
}

// absNotifier is called after a successful audiobook import to trigger an
// Audiobookshelf library scan so the new item is surfaced promptly rather than
// waiting for the next scheduled ABS scan (which defaults to every 24 h).
// Failures are best-effort — the import is never rolled back on ABS errors.
type absNotifier interface {
	ScanLibrary(ctx context.Context, libraryID string) error
}

// eventNotifier publishes a webhook event for a downstream notification target
// (Discord/ntfy/etc.). It is the narrow shape the scanner needs from
// *notifier.Notifier — declared locally so tests can spy on Send without
// pulling the whole notifier package and its HTTP/SSRF machinery into the
// fixture (issue #849).
type eventNotifier interface {
	Send(ctx context.Context, eventType string, payload map[string]interface{})
}

// Event-type strings. Duplicated from internal/notifier to avoid an import
// cycle risk and keep this package free of notifier internals. Must stay in
// sync with notifier.Event* constants (covered by their respective tests).
const (
	notifierEventGrabbed        = "grabbed"
	notifierEventBookImported   = "bookImported"
	notifierEventDownloadFailed = "downloadFailed"
)

// Scanner checks for completed downloads and imports them into the library.
type Scanner struct {
	downloads            *db.DownloadRepo
	clients              *db.DownloadClientRepo
	books                *db.BookRepo
	authors              *db.AuthorRepo
	editions             *db.EditionRepo
	history              *db.HistoryRepo
	rootFolders          *db.RootFolderRepo
	series               *db.SeriesRepo
	renamer              *Renamer
	remapper             *Remapper
	calibreAdder         calibreAdder
	grimmory             grimmoryPusher
	calibreMode          func() calibre.Mode
	calibreCoverCacheDir string
	settings             *db.SettingsRepo
	libraryDir           string
	audiobookDir         string
	audiobookDownloadDir string
	absLib               absNotifier
	absLibraryIDsFn      func() []string
	notif                eventNotifier

	// jobs, when set, tracks the detached scan goroutine launched by StartScan
	// so process shutdown can cancel and drain it before the database closes
	// (#1458). When nil, StartScan falls back to an untracked goroutine on the
	// caller's context (tests, non-wired callers).
	jobs *jobs.Group

	// scanRunning is the single-flight guard for library scans (#1460).
	// Concurrent full walks race on book creation (books has no unique
	// constraint equivalent to book_files.path) and clobber library.lastScan,
	// so only one scan — manual or scheduled — runs at a time.
	scanRunning atomic.Bool

	// testImportHook, when non-nil, intercepts tryImportInternal before any
	// state transition or file operation and replaces the import entirely.
	// It is a test seam for the per-client dispatch/completion matrix
	// (scanner_dispatch_matrix_test.go, issue #1019): those tests drive the
	// real CheckDownloads dispatch against protocol stubs and assert that the
	// importer boundary was handed the right path by the right typed poller,
	// without re-exercising the staging machinery the per-client scanner
	// tests already cover. Never set outside tests.
	testImportHook func(dl *models.Download, downloadPath, clientType string, explicitFiles []string)
}

// NewScanner creates an import scanner. downloadPathRemap is an optional
// comma-separated list of `from:to` pairs applied to paths reported by the
// download client so bindery can find files moved from a SAB-visible mount
// point to its own (see Remapper).
func NewScanner(downloads *db.DownloadRepo, clients *db.DownloadClientRepo,
	books *db.BookRepo, authors *db.AuthorRepo, history *db.HistoryRepo,
	libraryDir, audiobookDir, namingTemplate, audiobookTemplate, downloadPathRemap string) *Scanner {
	if audiobookDir == "" {
		audiobookDir = libraryDir
	}
	return &Scanner{
		downloads:    downloads,
		clients:      clients,
		books:        books,
		authors:      authors,
		history:      history,
		renamer:      NewRenamerWithAudiobook(namingTemplate, audiobookTemplate),
		remapper:     ParseRemap(downloadPathRemap),
		libraryDir:   libraryDir,
		audiobookDir: audiobookDir,
	}
}

// WithAudiobookDownloadDir records the separate audiobook download watch
// folder. When non-empty, this directory is the expected landing zone for
// completed audiobook downloads (separate from the general download dir).
// The value is surfaced via the storage API so the UI can display it; future
// download-client integrations can use it to route audiobook grabs to a
// dedicated category/folder.
func (s *Scanner) WithAudiobookDownloadDir(dir string) *Scanner {
	s.audiobookDownloadDir = dir
	return s
}

// AudiobookDownloadDir returns the effective audiobook download directory:
// audiobookDownloadDir when configured, or the empty string to signal
// fall-back to the default download dir.
func (s *Scanner) AudiobookDownloadDir() string {
	return s.audiobookDownloadDir
}

// WithJobs registers the process-wide background-jobs group so a StartScan()
// launched scan is tracked and drained on shutdown before the database closes
// (#1458).
func (s *Scanner) WithJobs(g *jobs.Group) *Scanner {
	s.jobs = g
	return s
}

// WithRootFolders attaches the root folder repo so the scanner can resolve
// per-author library directories from their rootFolderId.
func (s *Scanner) WithRootFolders(rf *db.RootFolderRepo) *Scanner {
	s.rootFolders = rf
	return s
}

// WithSeriesRepo attaches the series repo so the scanner can resolve series
// name and position when building rename destination paths, and so ScanLibrary
// can fall back to series+position matching for series-annotated filenames.
func (s *Scanner) WithSeriesRepo(sr *db.SeriesRepo) *Scanner {
	s.series = sr
	return s
}

// WithEditions attaches the edition repo so Calibre handoffs can prefer the
// selected or downloaded edition's ISBN, publisher, date, cover, and language.
func (s *Scanner) WithEditions(er *db.EditionRepo) *Scanner {
	s.editions = er
	return s
}

// primarySeriesFor returns the primary series title and position for the given
// book. Returns empty strings when the series repo is not configured or the
// book has no primary series.
func (s *Scanner) primarySeriesFor(ctx context.Context, book *models.Book) (seriesTitle, seriesNumber string) {
	if s.series == nil || book == nil {
		return "", ""
	}
	title, pos, err := s.series.GetPrimarySeriesForBook(ctx, book.ID)
	if err != nil {
		slog.Warn("renamer: failed to load primary series", "bookID", book.ID, "error", err)
		return "", ""
	}
	return title, pos
}

// effectiveLibraryDir returns the library root to use for the given author.
// Priority: (1) author's explicit RootFolderID, (2) library.defaultRootFolderId
// setting, (3) global libraryDir from env-var.
func (s *Scanner) effectiveLibraryDir(ctx context.Context, author *models.Author) string {
	if author != nil && author.RootFolderID != nil && s.rootFolders != nil {
		if rf, err := s.rootFolders.GetByID(ctx, *author.RootFolderID); err == nil && rf != nil {
			return rf.Path
		}
	}
	if s.settings != nil && s.rootFolders != nil {
		if setting, err := s.settings.Get(ctx, "library.defaultRootFolderId"); err == nil && setting != nil && setting.Value != "" {
			if id, err := strconv.ParseInt(setting.Value, 10, 64); err == nil && id > 0 {
				if rf, err := s.rootFolders.GetByID(ctx, id); err == nil && rf != nil {
					return rf.Path
				}
			}
		}
	}
	return s.libraryDir
}

// effectiveAudiobookDir returns the audiobook root to use for the given author.
// Priority: (1) author's explicit AudiobookRootFolderID, (2) global
// audiobookDir from env-var. It deliberately mirrors effectiveLibraryDir but
// consults a separate per-author field: routing audiobooks through the ebook
// RootFolderID would send them into the ebook root whenever an author has any
// custom ebook root folder assigned, silently ignoring BINDERY_AUDIOBOOK_DIR
// (#421). There is no audiobook equivalent of library.defaultRootFolderId, so
// the only override is the per-author column.
func (s *Scanner) effectiveAudiobookDir(ctx context.Context, author *models.Author) string {
	if author != nil && author.AudiobookRootFolderID != nil && s.rootFolders != nil {
		if rf, err := s.rootFolders.GetByID(ctx, *author.AudiobookRootFolderID); err == nil && rf != nil {
			return rf.Path
		}
	}
	return s.audiobookDir
}

// WithCalibre attaches the Calibre integration. The mode resolver is consulted
// on every import so the operator can switch modes in the UI without restarting.
func (s *Scanner) WithCalibre(mode func() calibre.Mode, adder calibreAdder) *Scanner {
	s.calibreMode = mode
	s.calibreAdder = adder
	return s
}

// WithGrimmory attaches the Grimmory push integration (#826). The pusher reads
// its config live on every import, so enabling/disabling in the UI takes
// effect without restarting. Pushes are best-effort by contract: a Grimmory
// failure never blocks or fails the underlying import.
func (s *Scanner) WithGrimmory(p grimmoryPusher) *Scanner {
	s.grimmory = p
	return s
}

// WithCalibreCoverCache configures a writable cache directory for remote cover
// images that need to be materialized before calibredb can consume them.
func (s *Scanner) WithCalibreCoverCache(dir string) *Scanner {
	s.calibreCoverCacheDir = dir
	return s
}

// WithABSNotifier attaches an Audiobookshelf scan notifier. libraryIDsFn is
// called at import time to retrieve the current ABS audiobook library IDs;
// returning an empty slice disables the notification for that import.
func (s *Scanner) WithABSNotifier(n absNotifier, libraryIDsFn func() []string) *Scanner {
	s.absLib = n
	s.absLibraryIDsFn = libraryIDsFn
	return s
}

// WithNotifier attaches a webhook event notifier so import-success and
// download-failure transitions publish to user-configured ntfy/Discord/etc.
// endpoints. Before this wiring (issue #849) only manual grabs from the queue
// page emitted events — every other site that wrote a history row was silent.
func (s *Scanner) WithNotifier(n eventNotifier) *Scanner {
	s.notif = n
	return s
}

// notify is a nil-safe Send wrapper. Keeps every call site a one-liner without
// repeating the guard, and lets tests construct a Scanner without a notifier.
func (s *Scanner) notify(ctx context.Context, eventType string, payload map[string]interface{}) {
	if s.notif == nil {
		return
	}
	s.notif.Send(ctx, eventType, payload)
}

// importedPayload builds the EventBookImported webhook payload. The book may
// be nil in the unmatched-import edge case; in that case fall back to the
// download title so the webhook still carries a meaningful "title". Path is
// optional ("" omits it from the payload) — the ebook path varies per file in
// a multi-format bundle so emitting one of them would be misleading.
func importedPayload(book *models.Book, dl *models.Download, format, path string) map[string]interface{} {
	title := ""
	if book != nil {
		title = book.Title
	}
	if title == "" && dl != nil {
		title = dl.Title
	}
	p := map[string]interface{}{
		"title":  title,
		"format": format,
	}
	if path != "" {
		p["path"] = path
	}
	return p
}

// WithSettings attaches a SettingsRepo to the scanner so scan results can be
// persisted under the "library.lastScan" key and surfaced via the API.
func (s *Scanner) WithSettings(sr *db.SettingsRepo) *Scanner {
	s.settings = sr
	return s
}

// importRetryLimit is the maximum number of times CheckDownloads will
// automatically retry a download stuck in StateImportFailed before giving
// up and leaving it for manual intervention (Bug #7).
const importRetryLimit = 3

// CheckDownloads polls all enabled download clients for status changes and
// updates the local download records. Every enabled client is polled in
// priority order so that downloads from all configured clients (e.g. a
// SABnzbd + qBittorrent combo) are imported each cycle (issue #1090).
func (s *Scanner) CheckDownloads(ctx context.Context) {
	clients, err := s.clients.ListEnabled(ctx)
	if err != nil || len(clients) == 0 {
		return
	}

	for i := range clients {
		client := &clients[i]
		switch client.Type {
		case "transmission":
			s.checkTransmissionDownloads(ctx, client)
		case "qbittorrent":
			s.checkQbittorrentDownloads(ctx, client)
		case "deluge":
			s.checkDelugeDownloads(ctx, client)
		case "nzbget":
			s.checkNZBGetDownloads(ctx, client)
		case "sabnzbd":
			s.checkSABnzbdDownloads(ctx, client)
		default:
			// An unknown client type must NOT silently fall through to the SABnzbd
			// poller: it would hit SABnzbd's HTTP endpoints against the wrong host,
			// error, and (logged at Debug only) leave every download stuck at
			// "downloading" with no visible failure — exactly the Deluge bug in
			// #1019. Surface it loudly instead.
			slog.Warn("check downloads: unsupported download client type — downloads will not be imported",
				"type", client.Type, "client", client.Name)
		}
	}
}

// wedgedCompletedReconcileCap caps how many StateCompleted rows the boot
// reconciliation re-queues per startup. The cap exists so a site that has
// accumulated many wedged rows (e.g. first boot after the Wave 4 upgrade
// surfaces a long-standing bug) does not thundering-herd the importer in a
// single sweep — subsequent restarts pick up the rest a batch at a time.
// Each sweep is bounded by importRetryLimit per row so a permanently broken
// row terminates after at most that many cycles.
const wedgedCompletedReconcileCap = 50

// RecoverInterruptedImports sweeps downloads stuck mid-import back into a
// retryable state. It must be called once at startup, before the scheduler
// begins polling.
//
// Two distinct wedge shapes are handled:
//
//  1. StateImporting / StateImportPending — a process crash or the per-import
//     timeout can leave the row in one of these non-terminal states with no
//     automatic re-entry. CheckDownloads only retries from StateImportFailed,
//     so without this sweep the download is wedged forever (issue #706
//     finding 1). Moving it to StateImportFailed lets the existing retry path
//     (CheckDownloads, while the source is still in the client) pick it up; if
//     the source has since vanished, the same path now terminally blocks it
//     (finding 4) rather than leaving it silently stuck.
//
//  2. StateCompleted with no book_files row — the state machine allows
//     Completed -> ImportPending, but the transition is driven by the scanner
//     tick, not the state itself. If the process restarts between Completed
//     being set and the next tick, the row sits in Completed forever (Wave 4
//     finding 21). RecoverWedgedCompleted re-queues such rows as
//     ImportPending, bounded by wedgedCompletedReconcileCap so a database
//     full of wedged rows does not stampede the importer on first boot.
//     import_retry_count is bumped on every requeue so a permanently broken
//     row exhausts the budget after importRetryLimit attempts; rows over the
//     budget are logged at ERROR on startup but otherwise left alone.
//
// Release-notes implication: after upgrading past this version, downloads
// that were stuck in Completed before the upgrade (some users have reported
// books quietly sitting there for weeks) will be retried for import on
// startup. The retry surfaces a file that may already be on disk; the
// idempotency guard in the importer means a re-imported file that already
// landed is detected and skipped. This is intended behaviour and resolves
// the wedge without user action.
func (s *Scanner) RecoverInterruptedImports(ctx context.Context) {
	if s.downloads == nil {
		return
	}
	recovered, err := s.downloads.RecoverInterruptedImports(ctx)
	if err != nil {
		slog.Warn("failed to sweep interrupted imports on startup", "error", err)
		// Fall through: some downloads may still have been recovered before the
		// error; emit history for those.
	}
	for _, id := range recovered {
		slog.Info("recovered interrupted import — re-queued for retry", "download_id", id)
		dl, getErr := s.downloadByID(ctx, id)
		if getErr != nil || dl == nil {
			continue
		}
		s.createHistoryEvent(ctx, models.HistoryEventImportFailed, dl.Title, dl.BookID, map[string]string{
			"guid":    dl.GUID,
			"message": "import interrupted (process restart) — re-queued for retry",
			"status":  string(models.StateImportFailed),
		})
		// Intentionally NOT firing EventDownloadFailed here: this is a benign
		// startup-recovery transition that re-queues the import for retry, not a
		// terminal failure. Webhooking every interrupted import on every restart
		// would be noise for the user (issue #849).
	}

	s.recoverWedgedCompleted(ctx)
}

// recoverWedgedCompleted handles the Wave 4 finding 21 case: downloads that
// finished in StateCompleted but never advanced to import (typically because
// the process restarted between Completed being set and the scanner tick that
// would have moved them on). The work is capped per startup so a backlog of
// thousands does not stampede the importer; over-budget rows are surfaced via
// an ERROR log so an operator can see what is being skipped on each boot.
func (s *Scanner) recoverWedgedCompleted(ctx context.Context) {
	if s.downloads == nil {
		return
	}
	recovered, err := s.downloads.RecoverWedgedCompleted(ctx, importRetryLimit, wedgedCompletedReconcileCap)
	if err != nil {
		slog.Warn("failed to sweep wedged completed downloads on startup", "error", err)
		// Fall through: some rows may have been re-queued before the error.
	}
	for _, id := range recovered {
		slog.Warn("re-queueing wedged Completed download for import (boot reconciliation)",
			"download_id", id, "cap", wedgedCompletedReconcileCap)
		dl, getErr := s.downloadByID(ctx, id)
		if getErr != nil || dl == nil {
			continue
		}
		age := ""
		if dl.CompletedAt != nil {
			age = time.Since(*dl.CompletedAt).Truncate(time.Minute).String()
		}
		s.createHistoryEvent(ctx, models.HistoryEventImportFailed, dl.Title, dl.BookID, map[string]string{
			"guid":    dl.GUID,
			"message": "import never started (process restart before scanner tick) — re-queued for retry",
			"status":  string(models.StateImportPending),
			"age":     age,
		})
	}
	overLimit, countErr := s.downloads.CountWedgedCompletedOverRetryLimit(ctx, importRetryLimit)
	if countErr != nil {
		slog.Warn("failed to count over-budget wedged Completed downloads", "error", countErr)
		return
	}
	if overLimit > 0 {
		slog.Error("wedged Completed downloads have exhausted their import retry budget and will not be re-queued — investigate manually",
			"count", overLimit, "retry_limit", importRetryLimit)
	}
}

// downloadByID is a small helper used by recovery/diagnostic paths that only
// have an ID. DownloadRepo has no GetByID, so look the row up via List.
func (s *Scanner) downloadByID(ctx context.Context, id int64) (*models.Download, error) {
	all, err := s.downloads.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, nil
}

func (s *Scanner) setDownloadError(ctx context.Context, id int64, message string) {
	if err := s.downloads.SetError(ctx, id, message); err != nil {
		slog.Warn("failed to persist download error", "download_id", id, "error", err)
	}
}

func (s *Scanner) updateDownloadStatus(ctx context.Context, id int64, status models.DownloadState) {
	if err := s.downloads.UpdateStatus(ctx, id, status); err != nil {
		slog.Warn("failed to update download status", "download_id", id, "status", status, "error", err)
	}
}

// failImport records an import failure with a user-facing reason. It persists
// the status + message and emits an importFailed history event so the cause
// is visible in the Queue/History UI. Also publishes EventDownloadFailed so
// user-configured webhooks see the failure (issue #849) — there is no separate
// "import failed" event in the notifier enum, so importFailed and
// downloadFailed share the channel.
func (s *Scanner) failImport(ctx context.Context, dl *models.Download, status models.DownloadState, reason string) {
	if err := s.downloads.SetErrorWithStatus(ctx, dl.ID, status, reason); err != nil {
		slog.Warn("failed to persist import error", "download_id", dl.ID, "status", status, "error", err)
	}
	s.createHistoryEvent(ctx, models.HistoryEventImportFailed, dl.Title, dl.BookID, map[string]string{
		"guid":    dl.GUID,
		"message": reason,
		"status":  string(status),
	})
	s.notify(ctx, notifierEventDownloadFailed, map[string]interface{}{
		"title":   dl.Title,
		"message": reason,
	})
}

// recordUnmatchedImportPath persists where a completed download's files are so a
// later manual "Match to book" (#1589) can import them directly. Best-effort: a
// failure here only loses the manual-import shortcut, not any files.
func (s *Scanner) recordUnmatchedImportPath(ctx context.Context, downloadID int64, path string) {
	if path == "" {
		return
	}
	if err := s.downloads.SetImportPath(ctx, downloadID, path); err != nil {
		slog.Warn("failed to record unmatched import path", "download_id", downloadID, "path", path, "error", err)
	}
}

func (s *Scanner) createHistoryEvent(ctx context.Context, eventType string, sourceTitle string, bookID *int64, data map[string]string) {
	if s.history == nil {
		return
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		slog.Warn("failed to marshal history event data", "event_type", eventType, "error", err)
		return
	}
	if err := s.history.Create(ctx, &models.HistoryEvent{
		BookID:      bookID,
		EventType:   eventType,
		SourceTitle: sourceTitle,
		Data:        string(dataJSON),
	}); err != nil {
		slog.Warn("failed to create history event", "event_type", eventType, "error", err)
	}
}

func (s *Scanner) markDownloadFailed(ctx context.Context, dl *models.Download, message string) {
	s.setDownloadError(ctx, dl.ID, message)
	s.createHistoryEvent(ctx, models.HistoryEventDownloadFailed, dl.Title, dl.BookID, map[string]string{"guid": dl.GUID, "message": message})
	s.notify(ctx, notifierEventDownloadFailed, map[string]interface{}{
		"title":   dl.Title,
		"message": message,
	})
}

// blockStaleImportFailures terminally blocks downloads that are stuck in
// StateImportFailed with no remaining automatic recovery path (issue #706
// finding 4).
//
// CheckDownloads only retries a StateImportFailed download while its source
// still appears in the client's torrent list / history. Two cases leave such a
// download wedged below the retry limit forever, with no terminal state:
//
//   - the retry budget is exhausted (ImportRetryCount >= importRetryLimit);
//   - the source has vanished from the client (the torrent was removed, or the
//     usenet history entry was cleared), so no future poll will ever revisit it.
//
// For both, this transitions the download to terminal StateImportBlocked with
// an actionable message so the operator sees it needs manual intervention
// rather than a silent stuck queue entry.
//
// seenSourceIDs holds the IDs of every download whose source was observed in
// this poll cycle — a StateImportFailed download NOT in that set has lost its
// source. belongsToClient reports whether a download is owned by the polling
// client (torrent vs. usenet ownership differs, so the caller supplies the
// predicate).
//
// sourceListIsComplete must be true only when seenSourceIDs was built from a
// complete enumeration of the client's sources. An UNFILTERED torrent listing
// qualifies: a missing entry definitively means the source is gone. A
// category/label-filtered or degraded listing does NOT (#1461): a torrent moved
// to another directory/category or stripped of its label is absent from the
// filtered view while still healthy in the client. Usenet history APIs are
// paginated (SABnzbd is capped at 50 slots here), so a missing entry there
// could merely be an aged-out-but-healthy download. For all such callers this
// is false and only the retry-exhaustion case blocks, never the
// vanished-source case.
//
// The download list is re-fetched here rather than reusing the caller's
// snapshot: a retry that ran earlier in the same poll cycle may have changed a
// download's status / retry count, and acting on the stale snapshot could
// misjudge the retry budget.
func (s *Scanner) blockStaleImportFailures(
	ctx context.Context,
	seenSourceIDs map[int64]bool,
	sourceListIsComplete bool,
	belongsToClient func(models.Download) bool,
) {
	allDownloads, err := s.downloads.List(ctx)
	if err != nil {
		slog.Warn("blockStaleImportFailures: failed to list downloads", "error", err)
		return
	}
	for i := range allDownloads {
		dl := allDownloads[i]
		if dl.Status != models.StateImportFailed {
			continue
		}
		if !belongsToClient(dl) {
			continue
		}
		var reason string
		switch {
		case dl.ImportRetryCount >= importRetryLimit:
			reason = fmt.Sprintf("import retry limit reached (%d attempts) — fix the underlying problem, then retry manually", importRetryLimit)
		case sourceListIsComplete && !seenSourceIDs[dl.ID]:
			reason = "download source no longer available in the client — re-download or import the files manually"
		default:
			continue
		}
		slog.Warn("blocking unrecoverable import failure", "title", dl.Title, "download_id", dl.ID, "reason", reason)
		s.failImport(ctx, &dl, models.StateImportBlocked, reason)
	}
}

// alreadyImportedFormat reports whether book already has a tracked, on-disk
// file for the given format. It is the idempotency guard for issue #706
// finding 2: the book-file write, the download-status write and the history
// event are three separate non-transactional ops, so a crash between them
// leaves the file on disk and recorded in book_files while the download stays
// non-terminal. The startup sweep (finding 1) then re-queues the download and
// the retry would import the SAME files a second time — and because the
// audiobook destination is run through UniqueDir, the retry lands a duplicate
// folder ("Title (2)") that INSERT OR IGNORE cannot dedupe.
//
// Checking book_files AND os.Stat (rather than book_files alone) means a
// recorded-but-since-deleted file does not wrongly suppress a legitimate
// re-import.
func (s *Scanner) alreadyImportedFormat(ctx context.Context, book *models.Book, format string) bool {
	if book == nil {
		return false
	}
	files, err := s.books.ListFiles(ctx, book.ID)
	if err != nil {
		slog.Warn("idempotency check: failed to list book files", "bookID", book.ID, "error", err)
		return false
	}
	for _, f := range files {
		if f.Format != format {
			continue
		}
		if _, statErr := os.Stat(f.Path); statErr == nil {
			return true
		}
	}
	return false
}

// isBookAlreadyImported reports whether the book linked to dl already has an
// on-disk file tracked in book_files, scoped to the format(s) the book is
// monitored for.
//
// It is used as a guard for duplicate-add re-grabs (#769): when a torrent is
// re-grabbed after qBittorrent already holds it (409 response), the original
// download files may have been moved to the library by a prior Bindery import
// (move mode), leaving the download path empty. Rather than failing with "no
// book files found" and burning through the retry budget, the caller can check
// this and mark the Download StateImported directly.
//
// Scoping: the check is limited to the format(s) the book is configured for
// (book.MediaType). For single-format books (ebook or audiobook) this avoids
// falsely marking a fresh download as imported when only the other format
// already exists. For dual-format books (MediaTypeBoth) the Download record
// does not carry which format this particular grab targets, so both formats
// are checked; a re-grab of one format where only the other is on disk is
// an accepted narrow false-positive in that case.
func (s *Scanner) isBookAlreadyImported(ctx context.Context, dl *models.Download) bool {
	if dl.BookID == nil {
		return false
	}
	book, err := s.books.GetByID(ctx, *dl.BookID)
	if err != nil || book == nil {
		return false
	}
	switch book.MediaType {
	case models.MediaTypeEbook:
		return s.alreadyImportedFormat(ctx, book, models.MediaTypeEbook)
	case models.MediaTypeAudiobook:
		return s.alreadyImportedFormat(ctx, book, models.MediaTypeAudiobook)
	default: // MediaTypeBoth or unknown
		return s.alreadyImportedFormat(ctx, book, models.MediaTypeAudiobook) ||
			s.alreadyImportedFormat(ctx, book, models.MediaTypeEbook)
	}
}

// alreadyImportedPath reports whether the exact destination path is already
// tracked in book_files for the book and still exists on disk. This is the
// per-file idempotency guard for ebook imports, whose destination paths are
// deterministic (no UniqueDir): a retry recomputes the identical destPath, so
// a match here means that specific file already landed and must not be
// re-imported. It is intentionally per-file so a retry of a genuine PARTIAL
// failure still imports the files that never landed.
func (s *Scanner) alreadyImportedPath(ctx context.Context, book *models.Book, destPath string) bool {
	if book == nil {
		return false
	}
	files, err := s.books.ListFiles(ctx, book.ID)
	if err != nil {
		slog.Warn("idempotency check: failed to list book files", "bookID", book.ID, "error", err)
		return false
	}
	cleanDest := filepath.Clean(destPath)
	for _, f := range files {
		if filepath.Clean(f.Path) != cleanDest {
			continue
		}
		if _, statErr := os.Stat(f.Path); statErr == nil {
			return true
		}
	}
	return false
}

// ImportFromPath creates an import run for a file or folder already on disk,
// bypassing the download-client polling path. formatHint overrides extension-
// based format detection when non-empty ("ebook" or "audiobook").
func (s *Scanner) ImportFromPath(ctx context.Context, dl *models.Download, path, formatHint string) {
	s.tryImportInternal(ctx, dl, path, "", "", formatHint, nil, nil)
}

func (s *Scanner) tryImportInternal(ctx context.Context, dl *models.Download, downloadPath, cleanupClientType, cleanupRemoteID, formatHint string, cleanupFunc func() error, explicitFiles []string) {
	if s.testImportHook != nil {
		s.testImportHook(dl, downloadPath, cleanupClientType, explicitFiles)
		return
	}
	if s.libraryDir == "" {
		slog.Warn("no library directory configured, skipping import")
		// Not writable/configured — needs user action before import can proceed.
		s.failImport(ctx, dl, models.StateImportBlocked, "no library directory configured — set one in Settings")
		return
	}

	s.updateDownloadStatus(ctx, dl.ID, models.StateImportPending)

	// Read the configured import mode ONCE for the whole download (issue #705
	// finding 2). Re-reading "import.mode" per file means an operator toggling
	// copy↔move in the UI mid-run mixes copy and move within a single download;
	// the final cleanup could then RemoveAll the still-seeding source of a file
	// that was deliberately copied. The auto (unset) hardlink-vs-copy choice is
	// made per placement below against the file's real destination root, not
	// s.libraryDir, because per-author / audiobook roots can be on a separate
	// mount (#1254). Usenet downloads remap auto/hardlink to move first —
	// nothing seeds from a finished usenet job, so leaving the source behind
	// is a pure disk leak (#1542); see effectiveConfiguredMode.
	configuredMode := effectiveConfiguredMode(s.configuredImportMode(ctx), cleanupClientType)

	// External mode: skip all file operations and leave the book Wanted so the
	// library scan can reconcile it after the user's external tool (Calibre,
	// Grimmory, etc.) processes and places the file in the library directory.
	//
	// The download is parked in StateImportExternal — a NON-terminal state
	// (issue #706 finding 3). Marking it terminal-StateImported here was wrong:
	// if the external tool never places the file (or places it outside the
	// library dir, where ScanLibrary's path constraint rejects it), the book
	// stays Wanted forever and searchWanted re-grabs the same release every
	// sweep, each prior download reading as a success. StateImportExternal lets
	// searchWanted skip the book while the hand-off is outstanding, and
	// ScanLibrary still reconciles the file the moment it lands.
	if configuredMode == "external" {
		// When a drop folder is configured (#941), external mode renames the
		// finished file into that folder (copy/hardlink, never move) for a
		// sibling tool (CWA, Calibre, Storyteller) to ingest, then still parks
		// StateImportExternal so ScanLibrary reconciles the managed copy. When
		// no drop folder is set, fall through to the original behaviour: leave
		// the file in the download dir and just hand off.
		if s.dropToFolder(ctx, dl, downloadPath, formatHint, explicitFiles) {
			return
		}
		s.updateDownloadStatus(ctx, dl.ID, models.StateImportExternal)
		slog.Info("external import: download handed off, awaiting library scan", "title", dl.Title)
		s.createHistoryEvent(ctx, models.HistoryEventDownloadFolderImport, dl.Title, dl.BookID, map[string]string{"mode": "external", "status": string(models.StateImportExternal)})
		return
	}

	// Find book files. When the caller supplied an explicit per-torrent file
	// list (issue #903: built from the client's authoritative Files() RPC),
	// use that directly; it is strictly more precise than filepath.Walk and
	// avoids importing unrelated siblings when downloadPath is a shared
	// download root. Falling through to the walk preserves behaviour for
	// callers that can't (or don't yet) supply a file list (SABnzbd/NZBGet
	// per-job destDir, plus the RPC-failure fallback path for torrent
	// clients).
	bookFiles := discoverBookFiles(downloadPath, explicitFiles)

	if len(bookFiles) == 0 {
		// Before failing, check whether the book is already in the library. This
		// covers the case where a torrent was re-grabbed after its files were moved
		// to the library by a prior Bindery import (move mode): the download path is
		// empty but the book is already on disk. Marking it StateImported avoids
		// spurious failure noise and retry churn (#769).
		if s.isBookAlreadyImported(ctx, dl) {
			slog.Info("no book files at download path but book already in library — marking as imported",
				"title", dl.Title, "path", downloadPath)
			// Currently at StateImportPending (set at the top of this function).
			// Walk: importPending → importing → imported.
			s.updateDownloadStatus(ctx, dl.ID, models.StateImporting)
			s.updateDownloadStatus(ctx, dl.ID, models.StateImported)
			return
		}
		// Distinguish "path doesn't exist on this host" from "path exists but
		// has no recognised book files" — the former almost always means PathRemap
		// is not configured (qBittorrent and Bindery see different filesystem roots).
		if _, statErr := os.Stat(downloadPath); os.IsNotExist(statErr) {
			slog.Warn("download path not found on this host — check PathRemap setting on the download client",
				"path", downloadPath)
			s.failImport(ctx, dl, models.StateImportFailed,
				fmt.Sprintf("download path not found: %q — configure PathRemap on the download client so Bindery can resolve the path", downloadPath))
			return
		}
		slog.Warn("no book files found in download", "path", downloadPath)
		// No files — retryable if the downloader hasn't flushed them yet.
		s.failImport(ctx, dl, models.StateImportFailed, fmt.Sprintf("no book files found in %q", downloadPath))
		return
	}

	s.updateDownloadStatus(ctx, dl.ID, models.StateImporting)

	// Video-content guard (#1591): a movie/TV release that slipped through
	// release-name filtering can still reach import — e.g. a film whose folder
	// carries one soundtrack .mp3, which both passes discoverBookFiles and tips
	// detectDownloadFormat to audiobook, dragging the whole folder (video file
	// included) into the audiobook library. If the download's dominant file is
	// a video file, the release is not a book: block it for manual review
	// instead of importing. An explicit formatHint is the override — a human
	// declared the format, so their call wins.
	if formatHint == "" && largestFileIsVideo(downloadPath, explicitFiles) {
		s.failImport(ctx, dl, models.StateImportBlocked,
			"download looks like video content (largest file is a video file) — not imported; use manual import with an explicit format to override")
		return
	}

	// Per-import timeout so a stalled NFS copy does not hold the download in
	// StateImporting indefinitely. 30 minutes is generous for any realistic
	// book file; the context-aware file operations return promptly when it fires.
	importCtx, importCancel := context.WithTimeout(ctx, 30*time.Minute)
	defer importCancel()

	// Resolve the book and author for naming. Lookup errors are not fatal -
	// we fall through to the "unmatched import" log below.
	var book *models.Book
	var author *models.Author
	var edition *models.Edition
	if dl.BookID != nil {
		b, err := s.books.GetByID(ctx, *dl.BookID)
		if err != nil {
			slog.Warn("import: failed to load book", "bookId", *dl.BookID, "error", err)
		} else if b != nil {
			book = b
			a, err := s.authors.GetByID(ctx, book.AuthorID)
			if err != nil {
				slog.Warn("import: failed to load author", "authorId", book.AuthorID, "error", err)
			} else {
				author = a
			}
			edition = s.resolveCalibreEdition(ctx, dl, book)
		}
	}

	// Recover an association for downloads grabbed without a BookID (the
	// free-text Search page does not tie a release to a local book) before
	// falling through to the "unmatched" failure. Prefer the file's embedded
	// EPUB metadata (reliable) over the release filename (which encodes
	// author/title/series in inconsistent orders) — issue #1014.
	if book == nil {
		if b, a := s.matchBookForDownload(ctx, bookFiles); b != nil {
			book = b
			author = a
			edition = s.resolveCalibreEdition(ctx, dl, book)
			dl.BookID = &book.ID // so this run's history events + status carry it
			if err := s.downloads.SetBookID(ctx, dl.ID, book.ID); err != nil {
				slog.Warn("failed to persist recovered book association", "downloadID", dl.ID, "bookID", book.ID, "error", err)
			}
			slog.Info("recovered book association for unmatched download", "downloadID", dl.ID, "bookID", book.ID, "title", book.Title)
		}
	}

	// Detect the format of the downloaded files from their extensions.
	// This is authoritative for dual-format books (media_type='both') and
	// also fixes edge-cases where a mislabelled book would be routed to the
	// wrong library directory. formatHint overrides detection when the caller
	// (e.g. manual import) knows the intended format explicitly.
	detectedFormat := detectDownloadFormat(bookFiles)
	if formatHint == models.MediaTypeAudiobook || formatHint == models.MediaTypeEbook {
		detectedFormat = formatHint
	}

	// Audiobook path: place the entire download directory as a unit so
	// multi-part m4b/mp3 files, cover art, and cue sheets stay together.
	if detectedFormat == models.MediaTypeAudiobook {
		// Unmatched audiobook: with no book row we cannot compute a
		// destination (AudiobookDestDir -> renamer.apply dereferences
		// book.ReleaseDate/Title), so this branch would panic the scan
		// goroutine. The ebook branch below treats book == nil as
		// "unmatched, fail with an actionable status"; do the same here
		// rather than crash. book can be nil when the download has no
		// BookID, the lookup errored, or the book row was deleted between
		// grab and import.
		if book == nil {
			// Files are present and valid — only the book match is missing. Record
			// the path so the queue "Match to book" action (#1589) can import these
			// files directly against the book the user picks.
			s.recordUnmatchedImportPath(ctx, dl.ID, downloadPath)
			s.failImport(ctx, dl, models.StateImportFailed, "could not match any book to this download — check the release title")
			return
		}
		// Idempotency guard (issue #706 finding 2): if a prior attempt already
		// placed this audiobook (book_files has an on-disk audiobook entry) but
		// crashed before writing the terminal status, re-importing here would
		// run AudiobookDestDir through UniqueDir and land a duplicate
		// "Title (2)" folder. Short-circuit straight to StateImported instead.
		if s.alreadyImportedFormat(ctx, book, models.MediaTypeAudiobook) {
			slog.Info("audiobook already imported — skipping re-import (idempotency guard)",
				"title", dl.Title, "bookID", book.ID)
			s.updateDownloadStatus(ctx, dl.ID, models.StateImported)
			if cleanupFunc != nil {
				if err := cleanupFunc(); err != nil {
					slog.Warn("cleanup failed", cleanupWarnAttrs(cleanupClientType, cleanupRemoteID, err)...)
				}
			}
			return
		}
		// effectiveAudiobookDir resolves the per-author audiobook root folder
		// (#579) and falls back to BINDERY_AUDIOBOOK_DIR. It deliberately does
		// NOT consult the author's ebook RootFolderID: routing audiobooks
		// through that would send them into the ebook root whenever an author
		// has any custom ebook root folder assigned (#421).
		audiobookRoot := s.effectiveAudiobookDir(ctx, author)
		seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)
		audiobookDest, destErr := s.renamer.AudiobookDestDir(audiobookRoot, author, book, seriesTitle, seriesNum)
		if destErr != nil {
			slog.Error("failed to compute audiobook destination", "src", downloadPath, "error", destErr)
			s.failImport(ctx, dl, models.StateImportBlocked, fmt.Sprintf("audiobook destination invalid: %v", destErr))
			return
		}
		destDir := UniqueDir(audiobookDest)
		// Choose hardlink-vs-copy (when auto) against the audiobook root the
		// files actually land under, not s.libraryDir — they can be on
		// different mounts and a cross-device hardlink would hard-fail (#1254).
		mode := s.resolveImportMode(configuredMode, downloadPath, audiobookRoot)
		// audiobookSource is the path the move/copy/hardlink will operate on.
		// When the caller supplied an explicit per-torrent file list (issue
		// #903), prefer that to downloadPath: downloadPath can be a shared
		// download root for single-file torrents and moving the whole root
		// would drag in unrelated sibling files. resolveAudiobookSource
		// inspects bookFiles and returns either the lone file (single-file
		// torrent), the torrent's strict subdir (multi-file torrent unpacked
		// into its own folder), or "" with usePerFile=true to fall through to
		// per-file placement.
		audiobookSource := downloadPath
		usePerFile := false
		if len(explicitFiles) > 0 {
			src, perFile := s.resolveAudiobookSource(downloadPath, bookFiles)
			usePerFile = perFile
			if !perFile {
				audiobookSource = src
			}
		}
		slog.Info("importing audiobook folder", "src", audiobookSource, "dst", destDir, "mode", mode, "perFile", usePerFile)
		// Single-file audiobook releases (e.g. a lone .m4b from a torrent) give
		// us a file path rather than a folder. MoveDir/CopyDir/HardlinkDir all
		// reject non-directory sources, so place the file inside destDir.
		var dirErr error
		if usePerFile {
			// Files don't share a strict subdir under downloadPath; copy each
			// known file into destDir individually. This loses cover art and
			// cue sheets that the dir walk would have grabbed, but is safer
			// than moving the shared download root.
			if err := os.MkdirAll(destDir, 0o750); err != nil {
				dirErr = fmt.Errorf("create audiobook dest dir: %w", err)
			} else {
				for _, srcFile := range bookFiles {
					dstFile := filepath.Join(destDir, filepath.Base(srcFile))
					var fileErr error
					switch mode {
					case "hardlink":
						fileErr = HardlinkFile(srcFile, dstFile)
					case "copy":
						fileErr = CopyFileCtx(importCtx, srcFile, dstFile)
					default:
						fileErr = MoveFileCtx(importCtx, srcFile, dstFile)
					}
					if fileErr != nil {
						dirErr = fileErr
						break
					}
				}
			}
		} else {
			srcInfo, statErr := os.Stat(audiobookSource)
			if statErr != nil {
				slog.Error("failed to stat audiobook source", "src", audiobookSource, "error", statErr)
				s.failImport(ctx, dl, models.StateImportBlocked, fmt.Sprintf("audiobook source unavailable: %v", statErr))
				return
			}
			if srcInfo.IsDir() {
				// Per-file audiobook renaming (#1126): when a naming template is
				// configured, flatten EVERY audiobook folder (single- or
				// multi-disc) into destDir with each track named from the
				// template's {Part} order. This supersedes both the verbatim
				// folder copy and the "Part NNN" multi-disc flatten. Move mode
				// flattens via copy then removes the source, matching the
				// multi-disc contract below.
				if tmpl := s.audiobookFileTemplate(ctx); tmpl != "" &&
					(mode == "copy" || mode == "hardlink" || mode == "move") {
					flattenMode := mode
					if mode == "move" {
						flattenMode = "copy"
					}
					namer := func(index int, ext string) string {
						return s.renamer.AudiobookFileName(tmpl, author, book, seriesTitle, seriesNum, strings.TrimPrefix(ext, "."), index+1)
					}
					slog.Info("renaming audiobook files per template", "src", audiobookSource, "dst", destDir, "mode", flattenMode, "template", tmpl)
					dirErr = flattenAudiobookDirNamed(importCtx, flattenMode, audiobookSource, destDir, namer)
					if dirErr == nil && mode == "move" && importCtx.Err() == nil {
						if rmErr := os.RemoveAll(audiobookSource); rmErr != nil {
							slog.Warn("could not remove source after move-mode audiobook rename", "src", audiobookSource, "error", rmErr)
						}
					}
				} else if (mode == "copy" || mode == "hardlink" || mode == "move") &&
					// Multi-disc flattening (#886): when enabled AND the source is
					// a multi-disc audiobook, place every track flat into destDir
					// as "Part 001.ext", … instead of mirroring the disc-folder
					// tree. flattenAudiobookDir itself only places via copy or
					// hardlink (it renames files, so it never touches the source);
					// "move" — which since #1542 is what usenet downloads resolve
					// to — flattens via copy and then removes the source, the same
					// copy-then-delete contract as MoveDirCtx's slow path. The
					// source removal is skipped when the import context was
					// cancelled: sidecar carry is best-effort and swallows
					// per-file errors, so only an uncancelled nil return proves
					// the folder was fully placed.
					s.flattenMultiDiscEnabled(ctx) &&
					isMultiDiscAudiobook(audiobookSource) {
					flattenMode := mode
					if mode == "move" {
						flattenMode = "copy"
					}
					slog.Info("flattening multi-disc audiobook", "src", audiobookSource, "dst", destDir, "mode", flattenMode)
					dirErr = flattenAudiobookDir(importCtx, flattenMode, audiobookSource, destDir)
					if dirErr == nil && mode == "move" && importCtx.Err() == nil {
						if rmErr := os.RemoveAll(audiobookSource); rmErr != nil {
							slog.Warn("flatten: could not remove source after move-mode flatten", "src", audiobookSource, "error", rmErr)
						}
					}
				} else {
					switch mode {
					case "hardlink":
						dirErr = HardlinkDir(audiobookSource, destDir)
					case "copy":
						dirErr = CopyDirCtx(importCtx, audiobookSource, destDir)
					default:
						dirErr = MoveDirCtx(importCtx, audiobookSource, destDir)
					}
				}
			} else {
				if err := os.MkdirAll(destDir, 0o750); err != nil {
					dirErr = fmt.Errorf("create audiobook dest dir: %w", err)
				} else {
					dstFile := filepath.Join(destDir, filepath.Base(audiobookSource))
					switch mode {
					case "hardlink":
						dirErr = HardlinkFile(audiobookSource, dstFile)
					case "copy":
						dirErr = CopyFileCtx(importCtx, audiobookSource, dstFile)
					default:
						dirErr = MoveFileCtx(importCtx, audiobookSource, dstFile)
					}
				}
			}
		}
		if dirErr != nil {
			slog.Error("failed to import audiobook folder", "src", audiobookSource, "mode", mode, "error", dirErr)
			s.failImport(ctx, dl, models.StateImportBlocked, fmt.Sprintf("audiobook %s failed: %v", mode, dirErr))
			return
		}
		if book != nil {
			// Recording the audiobook location MUST succeed before the download
			// is marked imported. If it fails and we mark imported anyway, the
			// book has no format file path, still reads as wanted, and the
			// wanted sweep re-grabs it into a "Title (2)" duplicate — while in
			// move mode the original source is already gone (#1459). Unlike the
			// ebook path below (which stages the file, writes the row, then
			// atomically promotes), the folder is already placed here, so the
			// best we can do is retry the write to ride out transient SQLite
			// lock/busy errors and, on persistent failure, refuse to mark the
			// import complete.
			var setErr error
			for attempt := 0; attempt < 3; attempt++ {
				if setErr = s.books.SetFormatFilePath(ctx, book.ID, models.MediaTypeAudiobook, destDir); setErr == nil {
					break
				}
				if ctx.Err() != nil {
					break // a cancelled context won't recover; stop retrying
				}
				time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
			}
			if setErr != nil {
				slog.Error("failed to record audiobook file path — failing import to avoid a re-grab duplicate",
					"bookID", book.ID, "dst", destDir, "error", setErr)
				// copy/hardlink left the source intact, so remove the
				// just-placed destination and let the import retry cleanly.
				// move already consumed the source, so keep the placed folder
				// and point the user at it rather than deleting their only copy.
				if mode == "copy" || mode == "hardlink" {
					if rmErr := os.RemoveAll(destDir); rmErr != nil {
						slog.Warn("failed to remove audiobook destination after DB error", "dst", destDir, "error", rmErr)
					}
					s.failImport(ctx, dl, models.StateImportBlocked,
						fmt.Sprintf("audiobook placed but could not be recorded (%v) — retry the import", setErr))
				} else {
					s.failImport(ctx, dl, models.StateImportBlocked,
						fmt.Sprintf("audiobook moved to %s but could not be recorded (%v); the files are preserved there — retry the import to record them", destDir, setErr))
				}
				return
			}
		}
		s.updateDownloadStatus(ctx, dl.ID, models.StateImported)
		slog.Info("audiobook imported", "title", func() string {
			if book != nil {
				return book.Title
			}
			return dl.Title
		}(), "path", destDir)

		s.pushToCalibre(ctx, book, author, edition, seriesTitle, seriesNum, destDir)
		s.pushToABS(ctx)

		s.createHistoryEvent(ctx, models.HistoryEventBookImported, dl.Title, dl.BookID, map[string]string{"path": destDir, "format": models.MediaTypeAudiobook})
		s.notify(ctx, notifierEventBookImported, importedPayload(book, dl, models.MediaTypeAudiobook, destDir))
		if cleanupFunc != nil {
			if err := cleanupFunc(); err != nil {
				slog.Warn("cleanup failed", cleanupWarnAttrs(cleanupClientType, cleanupRemoteID, err)...)
			}
		}
		return
	}

	var imported, failed int
	var lastFileErr error
	// importedSrcFiles records the source path of every file that landed in the
	// library, so move-mode cleanup can delete exactly those files rather than
	// RemoveAll-ing the whole download path (issue #705 finding 4).
	var importedSrcFiles []string
	// detectedLang holds a language read from an embedded EPUB dc:language, used
	// to backfill an empty book language after the loop (#1160). Captured inside
	// the loop because move mode consumes the source file on commit, so it must
	// be read while srcFile still exists.
	var detectedLang string
	fillLanguage := book != nil && book.Language == "" && !book.IsFieldLocked(models.BookFieldLanguage)
	// Resolve the ebook destination root and (auto) placement mode once: the
	// root is stable for this author across the loop, and choosing hardlink-vs-
	// copy against it rather than s.libraryDir avoids a cross-device hardlink
	// failure when the author's RootFolderID is on a separate mount (#1254).
	ebookRoot := s.effectiveLibraryDir(ctx, author)
	ebookMode := s.resolveImportMode(configuredMode, downloadPath, ebookRoot)
	for _, srcFile := range bookFiles {
		if book == nil {
			// Try to match from filename
			parsed := ParseFilename(srcFile)
			slog.Info("unmatched import", "title", parsed.Title, "author", parsed.Author, "file", srcFile)
			continue
		}

		// Read the embedded EPUB language while the source is still present
		// (move mode deletes it on commit). Only when we actually intend to
		// backfill, so we never open the zip needlessly.
		if fillLanguage && detectedLang == "" && IsEpubFile(srcFile) {
			if meta, err := ReadEpubMetadata(srcFile); err == nil && meta.Language != "" {
				detectedLang = meta.Language
			}
		}

		seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)
		destPath, destErr := s.renamer.DestPath(ebookRoot, author, book, seriesTitle, seriesNum, srcFile)
		if destErr != nil {
			slog.Error("failed to compute book destination", "src", srcFile, "error", destErr)
			lastFileErr = destErr
			failed++
			continue
		}

		// Idempotency guard (issue #706 finding 2): if a prior attempt already
		// landed this exact destination file and recorded it in book_files but
		// crashed before writing the terminal status, re-importing would
		// re-copy the bytes and re-emit the history event. The ebook
		// destination is deterministic, so a match means the file is already
		// imported — count it as imported and move on. Per-file (not
		// per-download) so a retry of a genuine partial failure still imports
		// the files that never landed.
		if s.alreadyImportedPath(ctx, book, destPath) {
			slog.Info("book file already imported — skipping re-import (idempotency guard)",
				"src", srcFile, "dst", destPath)
			imported++
			continue
		}

		mode := ebookMode
		slog.Info("importing book", "src", srcFile, "dst", destPath, "mode", mode)

		// Wave 4 / finding 23: stage the file under a sibling temp name,
		// write the book_files row pointing at destPath, then atomically
		// promote staged -> destPath. Doing the DB write first means a
		// transient SQLite error rolls back to "nothing on disk, nothing
		// in DB" rather than "file at destPath, no book_files row, user
		// sees not-imported and re-grabs". The os.Rename leans on POSIX
		// atomic-rename for the durability invariant: either the user
		// sees the final file with its DB row, or neither.
		stagedPath, commit, rollback, stageErr := StagedImport(importCtx, mode, srcFile, destPath)
		if stageErr != nil {
			slog.Error("failed to stage import", "src", srcFile, "mode", mode, "error", stageErr)
			failed++
			lastFileErr = stageErr
			continue
		}
		// Record each imported file individually in book_files so multi-file
		// downloads (epub + mobi + pdf) are all tracked rather than overwriting.
		// The path is the final destPath, not the staging path — the row
		// reflects where the file WILL live, and commit makes that true
		// atomically immediately below.
		if err := s.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, destPath); err != nil {
			slog.Error("failed to record book file — rolling back staged file", "bookID", book.ID, "staged", stagedPath, "error", err)
			rollback()
			failed++
			lastFileErr = fmt.Errorf("record book file: %w", err)
			continue
		}
		if err := commit(); err != nil {
			// The DB row exists but the rename failed (e.g. a concurrent
			// reader holds destPath, or the FS went read-only between
			// AddBookFile and Rename). Best-effort delete the book_files
			// row so it does not point at a non-existent path. If the
			// delete itself fails the next ScanLibrary will reconcile the
			// dangling row away.
			slog.Error("failed to commit staged file — rolling back book_files row", "bookID", book.ID, "staged", stagedPath, "dst", destPath, "error", err)
			if _, removeErr := s.books.RemoveBookFile(ctx, destPath); removeErr != nil {
				slog.Warn("failed to roll back book_files row after staged-commit failure — ScanLibrary will reconcile", "bookID", book.ID, "path", destPath, "error", removeErr)
			}
			rollback()
			failed++
			lastFileErr = err
			continue
		}
		imported++
		importedSrcFiles = append(importedSrcFiles, srcFile)
		// NOTE: StateImported is intentionally NOT set here (issue #705 finding 1).
		// Writing the terminal "imported" state after the first successful file
		// would mark an incomplete multi-file download as fully imported; a later
		// file failing would then leave a terminal-imported but partial download,
		// and move-mode cleanup would delete the source of the file that never
		// landed. The terminal state is decided once, after the loop.
		slog.Info("book imported", "title", book.Title, "path", destPath)

		s.pushToCalibre(ctx, book, author, edition, seriesTitle, seriesNum, destPath)
		s.pushToCWA(ctx, destPath)
		s.pushToGrimmory(ctx, book, destPath)

		s.createHistoryEvent(ctx, models.HistoryEventBookImported, dl.Title, dl.BookID, map[string]string{"path": destPath, "format": models.MediaTypeEbook})
	}

	// Backfill the book's language from the embedded EPUB dc:language when the
	// catalogue left it empty (#1160). Providers frequently have no work-level
	// language (OpenLibrary especially), so an imported file is often the most
	// reliable source. Best-effort: gated on an empty, unlocked field and at
	// least one imported file; a persistence error just leaves it empty.
	if fillLanguage && imported > 0 && detectedLang != "" {
		if err := s.books.SetLanguage(ctx, book.ID, detectedLang); err != nil {
			slog.Warn("failed to persist EPUB-detected language", "bookID", book.ID, "language", detectedLang, "error", err)
		} else {
			slog.Info("filled book language from embedded EPUB metadata", "bookID", book.ID, "language", detectedLang)
			book.Language = detectedLang
		}
	}

	// If every file failed to copy/move, the destination is likely not writable —
	// mark as blocked so the user knows manual intervention is needed.
	if imported == 0 && failed > 0 {
		reason := fmt.Sprintf("all %d file(s) failed to import", failed)
		if lastFileErr != nil {
			reason = fmt.Sprintf("%s: %v", reason, lastFileErr)
		}
		s.failImport(ctx, dl, models.StateImportBlocked, reason)
		return
	}

	// Matched nothing — no book resolved and nothing imported. Surface what was
	// read from the file (parsed filename + embedded EPUB metadata) so the user
	// can see WHY it didn't match rather than getting a bare "check the release
	// title" (issue #1014 point 5).
	if imported == 0 && failed == 0 && book == nil {
		// Valid files, no matching book — record where they are so the queue
		// "Match to book" action (#1589) can import them against a chosen book.
		s.recordUnmatchedImportPath(ctx, dl.ID, downloadPath)
		s.failImport(ctx, dl, models.StateImportFailed, unmatchedReason(bookFiles))
		return
	}

	// Partial failure (issue #705 finding 1): at least one file imported but at
	// least one other file failed. The download is NOT fully imported. Mark it
	// failed (retryable) and SKIP source cleanup entirely — in move mode the
	// source of the failed file must survive so a retry (or the operator) can
	// recover it, and the already-imported files' sources are left alone too.
	if imported > 0 && failed > 0 {
		reason := fmt.Sprintf("partial import: %d of %d file(s) failed", failed, imported+failed)
		if lastFileErr != nil {
			reason = fmt.Sprintf("%s: %v", reason, lastFileErr)
		}
		slog.Warn("partial import — skipping source cleanup to avoid data loss",
			"title", dl.Title, "imported", imported, "failed", failed)
		s.failImport(ctx, dl, models.StateImportFailed, reason)
		return
	}

	// A clean run (imported > 0, failed == 0): every book file landed. Mark the
	// download imported exactly once, then clean up.
	if imported > 0 && failed == 0 {
		s.updateDownloadStatus(ctx, dl.ID, models.StateImported)
		// One bookImported notification per download (not per file): a
		// multi-format ebook bundle (epub + mobi + pdf) is conceptually one
		// import event from the user's perspective.
		s.notify(ctx, notifierEventBookImported, importedPayload(book, dl, models.MediaTypeEbook, ""))

		// For "move" mode bindery has no further use for the source files. The
		// download folder may, however, be a path shared with sibling torrents
		// (issue #705 finding 4): for single-file-at-root torrents or older
		// qBittorrent clients downloadPath can resolve to the client's save
		// root. os.RemoveAll there would delete other torrents' still-seeding
		// data. So in move mode we delete only the specific imported source
		// files and then prune now-empty parent directories with os.Remove —
		// which fails on non-empty directories and therefore can never destroy
		// a shared root or sibling data.
		if configuredMode == "move" {
			s.cleanupMovedSources(downloadPath, importedSrcFiles)
		}
		if cleanupFunc != nil {
			if err := cleanupFunc(); err != nil {
				slog.Warn("cleanup failed", cleanupWarnAttrs(cleanupClientType, cleanupRemoteID, err)...)
			}
		}
	}
}

// cleanupMovedSources removes the source files that were successfully moved
// into the library and then prunes any directories left empty by their
// removal, walking upward from each file towards downloadPath (inclusive).
//
// It deliberately uses os.Remove (never os.RemoveAll) for directories: os.Remove
// fails on a non-empty directory, so a downloadPath that is actually a shared
// save root holding sibling torrents' data is left untouched. MoveFileCtx
// already deletes the source file on a successful move, so most entries here
// are no-ops on the file itself; the value is the empty-directory pruning.
func (s *Scanner) cleanupMovedSources(downloadPath string, importedSrcFiles []string) {
	cleanDownloadPath := filepath.Clean(downloadPath)

	// Refuse to prune if downloadPath is empty, relative, the filesystem root, or
	// equal to / an ancestor of a configured library root. This is a
	// belt-and-braces guard on top of the os.Remove non-empty-dir protection.
	if cleanDownloadPath == "" || cleanDownloadPath == "." ||
		cleanDownloadPath == string(filepath.Separator) ||
		!filepath.IsAbs(cleanDownloadPath) {
		slog.Warn("move cleanup: refusing to prune unsafe download path", "path", downloadPath)
		return
	}
	for _, root := range []string{s.libraryDir, s.audiobookDir} {
		if root == "" {
			continue
		}
		cleanRoot := filepath.Clean(root)
		if cleanDownloadPath == cleanRoot || pathUnderDir(cleanRoot, cleanDownloadPath) {
			slog.Warn("move cleanup: download path equals or contains a library root — skipping cleanup",
				"downloadPath", cleanDownloadPath, "libraryRoot", cleanRoot)
			return
		}
	}

	// dirsToPrune collects every directory at or below downloadPath that may now
	// be empty, deepest-first so children are removed before parents.
	prune := make(map[string]bool)
	for _, src := range importedSrcFiles {
		cleanSrc := filepath.Clean(src)
		// Only touch files that actually live under downloadPath — never delete
		// something outside the download we were asked to import.
		if cleanSrc != cleanDownloadPath && !pathUnderDir(cleanSrc, cleanDownloadPath) {
			slog.Warn("move cleanup: imported source outside download path, skipping",
				"src", cleanSrc, "downloadPath", cleanDownloadPath)
			continue
		}
		if err := os.Remove(cleanSrc); err != nil && !os.IsNotExist(err) {
			slog.Warn("move cleanup: failed to remove imported source file", "src", cleanSrc, "error", err)
		}
		// Mark every ancestor directory from the file up to (and including)
		// downloadPath as a pruning candidate.
		for dir := filepath.Dir(cleanSrc); ; dir = filepath.Dir(dir) {
			prune[dir] = true
			if dir == cleanDownloadPath || !pathUnderDir(dir, cleanDownloadPath) {
				break
			}
		}
	}

	// Remove empty directories deepest-first. os.Remove silently fails (and is
	// skipped) on any directory still holding sibling files or other torrents'
	// data, so a shared save root is preserved automatically.
	dirs := make([]string, 0, len(prune))
	for d := range prune {
		dirs = append(dirs, d)
	}
	// Deepest paths first: longer cleaned paths sort after shorter ancestors.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		// Never prune above downloadPath.
		if dir != cleanDownloadPath && !pathUnderDir(dir, cleanDownloadPath) {
			continue
		}
		if err := os.Remove(dir); err != nil {
			if !os.IsNotExist(err) {
				// ENOTEMPTY / EEXIST is expected and benign: the directory still
				// holds other data (sibling torrent files, byproducts) and must
				// be kept. Anything else is logged at debug level only.
				slog.Debug("move cleanup: directory not pruned (kept)", "dir", dir, "reason", err)
			}
			continue
		}
		slog.Debug("move cleanup: pruned empty directory", "dir", dir)
	}
}

func safeRemoteID(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

func cleanupWarnAttrs(clientType, remoteID string, err error) []any {
	attrs := []any{"error", err}
	if clientType != "" {
		attrs = append(attrs, "clientType", clientType)
	}
	if remoteID != "" {
		attrs = append(attrs, "remoteID", remoteID)
	}
	return attrs
}

// detectDownloadFormat inspects a list of file paths and returns the media
// type inferred from their extensions. Any audio extension tips the result to
// audiobook; a list of only ebook files returns ebook. An empty list returns
// ebook as a safe default.
func detectDownloadFormat(files []string) string {
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f)) {
		case ".mp3", ".m4b", ".m4a", ".aac", ".flac", ".ogg", ".opus":
			return models.MediaTypeAudiobook
		}
	}
	return models.MediaTypeEbook
}

// videoExtensions lists common video container extensions. None of these are
// book files, so a download whose largest file carries one is a movie/TV
// release regardless of what smaller files ride along (#1591).
var videoExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".wmv": true, ".mov": true,
	".mpg": true, ".mpeg": true, ".m2ts": true, ".ts": true, ".vob": true,
	".webm": true, ".flv": true,
}

// largestFileIsVideo reports whether the download's single largest file by
// size is a video file. When the caller supplied an explicit per-torrent file
// list (#903) only those files are considered — downloadPath can be a shared
// download root there, and walking it would judge this download by an
// unrelated sibling's video file. Without an explicit list, downloadPath is a
// per-job directory (SABnzbd/NZBGet) or a single file and is walked directly.
// Unreadable entries are skipped: this is a rejection heuristic, and an
// unstat-able file must not block an otherwise legitimate import.
func largestFileIsVideo(downloadPath string, explicitFiles []string) bool {
	var largestExt string
	var largestSize int64
	consider := func(path string, size int64) {
		if size > largestSize {
			largestSize = size
			largestExt = strings.ToLower(filepath.Ext(path))
		}
	}
	if len(explicitFiles) > 0 {
		for _, f := range explicitFiles {
			if fi, err := os.Lstat(f); err == nil && fi.Mode().IsRegular() {
				consider(f, fi.Size())
			}
		}
	} else if err := filepath.Walk(downloadPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !info.Mode().IsRegular() {
			return nil //nolint:nilerr // best-effort walk: skip unreadable entries rather than abort the scan
		}
		consider(path, info.Size())
		return nil
	}); err != nil {
		return false
	}
	return videoExtensions[largestExt]
}

// FindExisting searches the library directories for a book file that matches
// the given title and author. The mediaType argument selects which roots are
// walked: MediaTypeEbook restricts to libraryDir, MediaTypeAudiobook restricts
// to audiobookDir (falling back to libraryDir when audiobookDir is unset), and
// MediaTypeBoth or an empty/unknown value walks both with libraryDir first.
// Returns the first matching file path, or "" if none is found. Intended to be
// called before auto-searching so books the user already owns are not
// re-downloaded.
func (s *Scanner) FindExisting(ctx context.Context, title, authorName, mediaType string) string {
	if title == "" {
		return ""
	}
	roots := make([]string, 0, 2)
	switch mediaType {
	case models.MediaTypeEbook:
		if s.libraryDir != "" {
			roots = append(roots, s.libraryDir)
		}
	case models.MediaTypeAudiobook:
		switch {
		case s.audiobookDir != "":
			roots = append(roots, s.audiobookDir)
		case s.libraryDir != "":
			roots = append(roots, s.libraryDir)
		}
	default:
		if s.libraryDir != "" {
			roots = append(roots, s.libraryDir)
		}
		if s.audiobookDir != "" && s.audiobookDir != s.libraryDir {
			roots = append(roots, s.audiobookDir)
		}
	}
	for _, root := range roots {
		if found := s.findExistingInDir(root, title, authorName); found != "" {
			return found
		}
	}
	return ""
}

// findExistingInDir walks a single root directory looking for a book file
// whose title matches and whose parent directory agrees with the expected
// author, preventing cross-author false matches.
func (s *Scanner) findExistingInDir(root, title, authorName string) string {
	var found string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found != "" {
			return nil
		}
		if !IsBookFile(path) {
			return nil
		}
		// Author-folder pre-filter: the first directory under root must match
		// the expected author. This prevents a file under books/David Wong/
		// from being matched as a Matt Dinniman book.
		if authorName != "" {
			if rel, relErr := filepath.Rel(root, path); relErr == nil {
				parts := strings.SplitN(rel, string(filepath.Separator), 2)
				if len(parts) >= 2 && !authorMatch(authorName, parts[0]) {
					return nil
				}
			}
		}
		parsed := ParseFilename(path)
		if titleMatch(parsed.Title, title) && authorMatch(authorName, parsed.Author) {
			found = path
		}
		return nil
	}); err != nil {
		slog.Warn("failed to search library for existing file", "path", root, "error", err)
	}
	return found
}

// normalizeTitle lowercases a title, converts comma-suffix article form to
// leading-article form, then strips the leading article so that all three
// representations of the same title compare equal:
//
//	"A Darker Shade of Magic"       → "darker shade of magic"
//	"Darker Shade of Magic, A"      → "darker shade of magic"
//	"The Fragile Threads of Power"  → "fragile threads of power"
//	"Fragile Threads of Power, The" → "fragile threads of power"
func normalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Invert comma-suffix form: check ", an" before ", a" to avoid prefix collision.
	for _, art := range []string{", the", ", an", ", a"} {
		if strings.HasSuffix(s, art) {
			s = art[2:] + " " + s[:len(s)-len(art)]
			break
		}
	}
	// Strip leading article; check "an " before "a " for the same reason.
	for _, art := range []string{"the ", "an ", "a "} {
		if strings.HasPrefix(s, art) {
			s = s[len(art):]
			break
		}
	}
	return s
}

// titleMatch returns true when bookTitle and parsedTitle refer to the same work.
// It handles numeric titles (1984, 2001), article normalization ("Title, The"),
// and uses dynamic overlap thresholds so short titles still match correctly.
func titleMatch(bookTitle, parsedTitle string) bool {
	if parsedTitle == "" || bookTitle == "" {
		return false
	}

	// Fast path: exact match after normalization
	if normalizeTitle(bookTitle) == normalizeTitle(parsedTitle) {
		return true
	}

	// sigTokens splits on non-alphanumeric runs, preserving digits, and removes stopwords.
	sigTokens := func(s string) []string {
		stopwords := map[string]bool{
			"the": true, "a": true, "an": true, "of": true,
			"and": true, "in": true, "to": true, "for": true,
		}
		var out []string
		var cur []rune
		flush := func() {
			if len(cur) >= 2 {
				w := string(cur)
				if !stopwords[w] {
					out = append(out, w)
				}
			}
			cur = cur[:0]
		}
		for _, r := range strings.ToLower(s) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				cur = append(cur, r)
			} else {
				flush()
			}
		}
		flush()
		return out
	}

	btTok := sigTokens(bookTitle)
	ptTok := sigTokens(parsedTitle)
	if len(btTok) == 0 || len(ptTok) == 0 {
		return false
	}

	btSet := make(map[string]bool, len(btTok))
	for _, w := range btTok {
		btSet[w] = true
	}

	overlap := 0
	for _, w := range ptTok {
		if btSet[w] {
			overlap++
		}
	}

	// Dynamic threshold: require overlap >= min(len(ptTok), len(btTok), 2).
	// For single-token titles (e.g. "1984") require at least 1 match.
	minLen := len(ptTok)
	if len(btTok) < minLen {
		minLen = len(btTok)
	}
	required := 2
	if minLen < 2 {
		required = minLen
	}
	if required == 0 {
		required = 1
	}

	return overlap >= required
}

// authorTokenRegexCache caches per-token compiled word-boundary regexes used
// by authorMatch. ScanLibrary can perform thousands of comparisons in one
// pass, so we amortise the regexp.MustCompile cost across calls.
var authorTokenRegexCache sync.Map // map[string]*regexp.Regexp

// authorTokenRegex returns a cached case-insensitive \btoken\b regex. token
// is already lowercased and stripped of punctuation by the caller.
func authorTokenRegex(token string) *regexp.Regexp {
	if v, ok := authorTokenRegexCache.Load(token); ok {
		return v.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(token) + `\b`)
	authorTokenRegexCache.Store(token, re)
	return re
}

// authorMatch returns true when parsedAuthor is consistent with bookAuthor.
// If parsedAuthor is empty the function returns true (can't disprove).
//
// Significant tokens (>=3 chars, lowercased, punctuation-stripped) are
// extracted from parsedAuthor. Each significant token must appear at a word
// boundary in bookAuthor. Initials (1-2 char tokens like "R." in "George R.
// R. Martin") are dropped — they are treated as optional, so the parsed name
// "George Martin" can still match "George R. R. Martin".
//
// This is stricter than a plain substring check: it eliminates the
// surname-overlap false positives where a co-author shares a token with the
// monitored author (e.g. parsedAuthor="Rachel Reid" should NOT match
// bookAuthor="Rachel Larsen, Adam Reid, and Ozi Akturk" — "rachel" and "reid"
// both appear but not as the same person; we still match because all tokens
// are present, which is a known limitation of word-list matching without
// position. The primary fix is rejecting the inverse case where the
// monitored author's surname coincides with a co-author's surname.)
func authorMatch(bookAuthor, parsedAuthor string) bool {
	if parsedAuthor == "" {
		return true // no author info in filename — don't filter
	}
	tokens := significantAuthorTokens(parsedAuthor)
	if len(tokens) == 0 {
		return true // only initials/punctuation — can't disprove
	}
	for _, tok := range tokens {
		if !authorTokenRegex(tok).MatchString(bookAuthor) {
			return false
		}
	}
	return true
}

// significantAuthorTokens splits name into lowercased, punctuation-trimmed
// tokens of length >=3. Hyphenated names ("Mary-Kate Olsen") are kept as
// single tokens so the word-boundary regex matches the hyphen-delimited form.
func significantAuthorTokens(name string) []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(name)) {
		w = strings.Trim(w, ".,;:()[]'\"")
		if len(w) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

// pathUnderDir reports whether path is located inside dir (or is dir itself).
func pathUnderDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && !strings.HasPrefix(rel, "..")
}

// scanBook is the precomputed per-book data the library-scan reconciliation
// loop needs. Hoisting normalizeTitle into this struct makes it run once per
// book instead of once per (file, book) pair — see ScanLibrary.
type scanBook struct {
	book      *models.Book
	normTitle string
	normLen   int
}

// wordRunsInto appends every maximal [0-9A-Za-z_] run of s, lowercased, to dst
// and returns the result. These are the runs a `\b…\b` regex (authorTokenRegex,
// used by authorMatch) treats as words, so an index of them is an exact
// super-set key for authorMatch candidates. dst is reused to avoid allocations.
func wordRunsInto(dst []string, s string) []string {
	start := -1
	for i := range len(s) {
		c := s[i]
		isWord := c == '_' || (c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if isWord {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			dst = append(dst, strings.ToLower(s[start:i]))
			start = -1
		}
	}
	if start >= 0 {
		dst = append(dst, strings.ToLower(s[start:]))
	}
	return dst
}

// firstWordRun returns the first maximal [0-9A-Za-z_] run of tok, lowercased,
// or "" if tok has no word characters. Whenever authorMatch's `\btok\b` test
// matches an author name, firstWordRun(tok) is one of that name's word runs —
// which is what makes authorWordIndex a guaranteed super-set (see ScanLibrary).
func firstWordRun(tok string) string {
	start := -1
	for i := range len(tok) {
		c := tok[i]
		isWord := c == '_' || (c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if isWord {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			return strings.ToLower(tok[start:i])
		}
	}
	if start >= 0 {
		return strings.ToLower(tok[start:])
	}
	return ""
}

// cleanLayoutTitle strips bracket/paren annotations from a book-folder name —
// Calibre's " (id)", Readarr's " (year)" — so it matches the stored title.
// It also strips a leading Readarr "{Series} #{N} - " prefix
// ("Discworld #8 - Guards! Guards!" → "Guards! Guards!", issue #1234): without
// this the whole folder name, series tag and all, leaks through as the title
// and only series openers (where book title == series title) reconcile.
func cleanLayoutTitle(dir string) string {
	if _, _, title, ok := parseSeriesFolder(dir); ok {
		dir = title
	}
	s := cleanRe.ReplaceAllString(dir, "")
	s = multiSp.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// authorTitleFromLayout derives author and title from a library file's folder
// hierarchy. A file under <root>/<Author>/<Book>/<file> names both
// unambiguously and is dash-safe, unlike splitting an "Author - Title" or
// "Title - Author" filename (#754). title is "" when only the author folder is
// present; ok is false when the file is not nested under any root.
func authorTitleFromLayout(path string, roots ...string) (author, title string, ok bool) {
	for _, root := range roots {
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		switch parts := strings.Split(rel, string(filepath.Separator)); {
		case len(parts) >= 3:
			// <root>/<Author>/…/<Book>/<file>: first dir is the author,
			// the file's immediate parent dir is the book title.
			return strings.TrimSpace(parts[0]), cleanLayoutTitle(parts[len(parts)-2]), true
		case len(parts) == 2:
			// <root>/<Author>/<file>: only the author is unambiguous.
			return strings.TrimSpace(parts[0]), "", true
		}
	}
	return "", "", false
}

// ErrScanAlreadyRunning is returned by StartScan when a library scan (manual
// or scheduled) is already in flight. Matches the ABS importer's
// ErrAlreadyRunning / Grimmory syncer's ErrSyncAlreadyRunning pattern.
var ErrScanAlreadyRunning = errors.New("library scan already running")

// StartScan launches a library scan in the background and returns
// immediately. If a scan is already in flight it returns
// ErrScanAlreadyRunning so callers (the manual-scan endpoint) can surface a
// 409 instead of piling up concurrent full walks (#1460). Callers pass
// context.WithoutCancel(r.Context()) so the HTTP response-send doesn't cancel
// the scan.
func (s *Scanner) StartScan(ctx context.Context) error {
	if !s.scanRunning.CompareAndSwap(false, true) {
		return ErrScanAlreadyRunning
	}
	// When a jobs group is wired, the scan runs on the shutdown-scoped context
	// so SIGTERM cancels and drains it before the DB closes, instead of the
	// never-cancelled WithoutCancel(request) context. Fall back to an untracked
	// goroutine for tests/non-wired callers (#1458).
	if s.jobs != nil {
		s.jobs.Go("library-scan", func(ctx context.Context) {
			defer s.scanRunning.Store(false)
			s.scanLibrary(ctx)
		})
	} else {
		go func() {
			defer s.scanRunning.Store(false)
			s.scanLibrary(ctx)
		}()
	}
	return nil
}

// ScanLibrary runs a library scan synchronously, sharing the single-flight
// guard with StartScan: if a scan is already in flight (e.g. a manual scan
// racing the 6-hourly cron job) the call is skipped with a log line. The
// cron scheduler's SkipIfStillRunning only guards cron-vs-cron, so this is
// what prevents cron-vs-manual overlap (#1460).
func (s *Scanner) ScanLibrary(ctx context.Context) {
	if !s.scanRunning.CompareAndSwap(false, true) {
		slog.Info("library scan already running; skipping")
		return
	}
	defer s.scanRunning.Store(false)
	s.scanLibrary(ctx)
}

// scanLibrary walks the library directory (and the separate audiobook directory
// when configured) for book files not yet tracked in the database and reconciles
// found files with existing "wanted" book records. Callers must hold the
// scanRunning single-flight flag (via StartScan or ScanLibrary).
func (s *Scanner) scanLibrary(ctx context.Context) {
	if s.libraryDir == "" {
		// #965: previously this returned without writing any result, so the UI
		// kept showing a stale prior scan and the #962 "no files found" warning
		// never fired for a literally-unset library dir. Persist a zero-files
		// result so the frontend surfaces the misconfiguration (library_dir is
		// "" → the warning names "?" and no_files_found is true).
		s.writeScanError(ctx, "library directory not configured")
		return
	}

	// walkDir appends all book files found under root to foundFiles, tracking
	// which root each file belongs to so the author-inference fallback can
	// strip the correct prefix.
	walkDir := func(root string) []string {
		var files []string
		if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if IsBookFile(path) {
				files = append(files, path)
			}
			return nil
		}); err != nil {
			slog.Warn("library scan walk encountered errors", "path", root, "error", err)
		}
		return files
	}

	foundFiles := walkDir(s.libraryDir)

	// Also scan the audiobook directory when it is configured separately.
	if s.audiobookDir != "" && s.audiobookDir != s.libraryDir {
		foundFiles = append(foundFiles, walkDir(s.audiobookDir)...)
	}

	slog.Info("library scan found files", "paths", []string{s.libraryDir, s.audiobookDir}, "count", len(foundFiles))

	if len(foundFiles) == 0 {
		s.writeScanResult(ctx, len(foundFiles), 0, 0, 0, 0, nil)
		return
	}

	// Load all books from the DB for reconciliation
	allBooks, err := s.books.List(ctx)
	if err != nil {
		slog.Error("library scan: failed to list books", "error", err)
		// #965: write a result on this early return too — otherwise the UI keeps
		// showing the previous (stale) scan and never reflects that the scan
		// failed.
		s.writeScanError(ctx, fmt.Sprintf("scan failed: %v", err))
		return
	}

	// Build a set of tracked file paths, plus the parent directories of
	// tracked AUDIO files: a multi-track audiobook registers one path but its
	// folder holds sibling tracks belonging to the same book. Ebook parents
	// must not be tracked — in a flat Author/Title.epub layout the parent is
	// the author folder, and tracking it hid every untracked sibling ebook in
	// that folder from the scan (#1436).
	trackedPaths := make(map[string]bool)
	if allPaths, err := s.books.ListAllBookFilePaths(ctx); err == nil {
		for _, p := range allPaths {
			cleanP := filepath.Clean(p)
			trackedPaths[cleanP] = true
			if detectDownloadFormat([]string{cleanP}) == models.MediaTypeAudiobook {
				trackedPaths[filepath.Clean(filepath.Dir(cleanP))] = true
			}
		}
	}

	// Build author name and full-author caches for the reconciliation loop.
	// authorMap is needed for the path-under-library-dir constraint check.
	authorNames := make(map[int64]string)
	authorMap := make(map[int64]*models.Author)
	if allAuthors, err := s.authors.List(ctx); err == nil {
		for i := range allAuthors {
			authorNames[allAuthors[i].ID] = allAuthors[i].Name
			authorMap[allAuthors[i].ID] = &allAuthors[i]
		}
	}

	// reconciledBooks prevents a single DB book from being matched to more
	// than one file in the same scan pass. allBooks is loaded once and is
	// never mutated in-memory, so without this guard a loose titleMatch could
	// assign the same book to multiple files — last write wins, overwriting
	// the correct earlier assignment.
	reconciledBooks := make(map[int64]bool)

	// Precompute per-book reconciliation data once. The title tier previously
	// called normalizeTitle(book.Title) inside the per-file loop — a pure,
	// loop-invariant function recomputed files×books times. Hoisting it here
	// makes it run once per book. The candidate set is filtered down up front
	// and ASIN lookups become O(1).
	//
	// Two book states count as candidates:
	//   - Wanted: the book has no file yet by definition; this is the main case.
	//   - Imported with no file actually on disk: covers two situations the
	//     scanner used to ignore entirely. (a) #875: Calibre import sets
	//     Status=Imported on every book it creates, but in container setups
	//     where Calibre's library mount differs from Bindery's view, FilePath
	//     stays empty and the user's 3700 epubs found 0 reconciliation
	//     targets. (b) The user moved or renamed their files, leaving Imported
	//     rows pointing at locations that no longer exist; re-scan now relinks
	//     them rather than leaving the rows orphaned.
	wantedBooks := make([]scanBook, 0, len(allBooks))
	asinIndex := make(map[string][]scanBook)
	// booksByAuthor maps an author ID to the indices (into wantedBooks, i.e. in
	// library order) of that author's wanted books, so the title tier can build
	// its candidate list from just the matching authors instead of walking
	// every book in the library.
	booksByAuthor := make(map[int64][]int)
	for i := range allBooks {
		b := &allBooks[i]
		if !isReconcileCandidate(b) {
			continue
		}
		sb := scanBook{book: b, normTitle: normalizeTitle(b.Title)}
		sb.normLen = len(sb.normTitle)
		idx := len(wantedBooks)
		wantedBooks = append(wantedBooks, sb)
		booksByAuthor[b.AuthorID] = append(booksByAuthor[b.AuthorID], idx)
		if b.ASIN != "" {
			asinIndex[b.ASIN] = append(asinIndex[b.ASIN], sb)
		}
	}

	// authorWordIndex maps each author-name word run to the authors containing
	// it. The title tier only matches a book when authorMatch(authorName,
	// parsed.Author) holds, so resolving the matching authors up front lets the
	// title comparison skip every other author's books — turning the tier from
	// O(files × books) into roughly O(files × books-per-author).
	authorWordIndex := make(map[string][]int64)
	{
		var runs []string
		for id, name := range authorNames {
			runs = wordRunsInto(runs[:0], name)
			for _, w := range runs {
				authorWordIndex[w] = append(authorWordIndex[w], id)
			}
		}
	}

	// matchingAuthors returns the set of author IDs satisfying authorMatch for
	// parsedAuthor. A nil result means "every author" — authorMatch's contract
	// for an empty or initials-only parsed author. Candidates come from the
	// rarest token's word-run bucket (a guaranteed super-set of authorMatch
	// matches) and are then verified with the exact authorMatch, so the result
	// is identical to scanning every author. Memoised: a {Author}/{Title}/file
	// library yields the same parsed author for all of that author's files.
	authorMatchCache := make(map[string]map[int64]bool)
	matchingAuthors := func(parsedAuthor string) map[int64]bool {
		if parsedAuthor == "" {
			return nil
		}
		if set, ok := authorMatchCache[parsedAuthor]; ok {
			return set
		}
		tokens := significantAuthorTokens(parsedAuthor)
		if len(tokens) == 0 {
			authorMatchCache[parsedAuthor] = nil
			return nil
		}
		var candidates []int64
		haveBucket := false
		for _, tok := range tokens {
			run := firstWordRun(tok)
			if run == "" {
				continue
			}
			if bucket := authorWordIndex[run]; !haveBucket || len(bucket) < len(candidates) {
				candidates, haveBucket = bucket, true
			}
		}
		set := make(map[int64]bool)
		if !haveBucket {
			// No indexable token — fall back to scanning every author.
			for id, name := range authorNames {
				if authorMatch(name, parsedAuthor) {
					set[id] = true
				}
			}
		} else {
			for _, id := range candidates {
				if !set[id] && authorMatch(authorNames[id], parsedAuthor) {
					set[id] = true
				}
			}
		}
		authorMatchCache[parsedAuthor] = set
		return set
	}

	var unmatchedFiles []unmatchedFile
	var reconciled, unmatched, alreadyTracked, tagReadFailed int

	// tryReconcileTitle attempts to reconcile path to the given wanted book via
	// the fuzzy title tier, returning true once a book is claimed. The
	// candidate's author is already known to satisfy authorMatch — see the
	// title tier in the file loop below.
	var titleCand []int // reused candidate-index scratch
	tryReconcileTitle := func(sb *scanBook, path, cleanPath, normParsed, detectedFmt string) bool {
		b := sb.book
		if reconciledBooks[b.ID] {
			return false
		}
		// Length gate: Jaro-Winkler is bounded above by 0.8 + 0.2·(minLen/
		// maxLen), so a score >= 0.85 is impossible once the shorter normalised
		// title drops below a quarter of the longer. Cheap, never skips a match.
		lo, hi := sb.normLen, len(normParsed)
		if lo > hi {
			lo, hi = hi, lo
		}
		if lo == 0 || lo*4 < hi {
			return false
		}
		// Require Jaro-Winkler >= 0.85 to prevent low-confidence matches from
		// reconciling the wrong book after a delete+rescan (#343).
		jwScore := textutil.JaroWinkler(sb.normTitle, normParsed)
		if jwScore < 0.85 {
			return false
		}
		// File must live under the candidate book's effective library root to
		// prevent cross-author mismapping after delete+rescan (#343).
		effDir := s.effectiveLibraryDir(ctx, authorMap[b.AuthorID])
		if !pathUnderDir(path, effDir) {
			slog.Debug("library scan: title+author match rejected (outside library root)",
				"title", b.Title, "path", path, "root", effDir)
			return false
		}
		if err := s.books.AddBookFile(ctx, b.ID, detectedFmt, path); err != nil {
			slog.Error("library scan: failed to update book", "id", b.ID, "error", err)
			return false
		}
		slog.Info("library scan: reconciled book", "title", b.Title, "path", path, "jw", jwScore)
		trackedPaths[cleanPath] = true
		if detectedFmt == models.MediaTypeAudiobook {
			// Sibling tracks of a just-reconciled audiobook folder belong to
			// this book — count them as tracked, not unmatched.
			trackedPaths[filepath.Clean(filepath.Dir(cleanPath))] = true
		}
		reconciledBooks[b.ID] = true
		reconciled++
		return true
	}

	for _, path := range foundFiles {
		// Files already registered, and sibling tracks inside a tracked
		// audiobook folder, are counted instead of silently skipped so
		// files_found always equals reconciled + unmatched + already_tracked
		// (#1436).
		cleanPath := filepath.Clean(path)
		if trackedPaths[cleanPath] || trackedPaths[filepath.Clean(filepath.Dir(cleanPath))] {
			alreadyTracked++
			continue
		}

		// Parse the filename for title/author hints, then let the folder
		// hierarchy correct them. A file under <root>/<Author>/<Book>/<file>
		// names author and title unambiguously and dash-safe, unlike splitting
		// an "Author - Title" / "Title - Author" filename — the scan must not
		// assume a single filename order (#754).
		parsed := ParseFilename(path)
		var layoutTitle string
		if a, t, ok := authorTitleFromLayout(path, s.libraryDir, s.audiobookDir); ok {
			if a != "" {
				parsed.Author = a
			}
			if t != "" {
				parsed.Title = t
				layoutTitle = t
			}
		}

		// Prefer embedded audio tags over filename and folder parsing for
		// audiobook files. Well-tagged M4B/MP3 releases carry the author,
		// title and often an ASIN in their ID3/iTunes atoms; using them avoids
		// the fuzzy-match noise seen on users' organised libraries (#303).
		if IsAudioTagFile(path) {
			if tags, err := ReadAudioTags(path); err != nil {
				slog.Warn("library scan: tag read failed, falling back to filename",
					"path", path, "error", err)
				tagReadFailed++
			} else {
				// Multi-part audiobooks tag each track with its chapter name
				// ("04 - Sinister Grey Mists..."). When the folder hierarchy
				// already gave a real book title, don't let a per-chapter tag
				// title clobber it — every track would otherwise parse to a
				// different "book" and none would reconcile (#1239).
				if tags.Title != "" && (layoutTitle == "" || !looksLikeChapterTitle(tags.Title)) {
					parsed.Title = tags.Title
				}
				if tags.Author != "" {
					parsed.Author = tags.Author
				}
				if tags.ASIN != "" {
					parsed.ASIN = tags.ASIN
				}
			}
		}

		// Search existing books for a match: ASIN takes priority over fuzzy title+author.
		matched := false
		detectedFmt := detectDownloadFormat([]string{path})
		if parsed.ASIN != "" {
			for _, sb := range asinIndex[parsed.ASIN] {
				b := sb.book
				if reconciledBooks[b.ID] {
					continue
				}
				// File must live under the candidate book's effective library root.
				effDir := s.effectiveLibraryDir(ctx, authorMap[b.AuthorID])
				if !pathUnderDir(path, effDir) {
					slog.Debug("library scan: ASIN match rejected (outside library root)",
						"asin", parsed.ASIN, "path", path, "root", effDir)
					continue
				}
				if err := s.books.AddBookFile(ctx, b.ID, detectedFmt, path); err != nil {
					slog.Error("library scan: failed to update book", "id", b.ID, "error", err)
					continue
				}
				slog.Info("library scan: reconciled book via ASIN", "asin", parsed.ASIN, "title", b.Title, "path", path)
				trackedPaths[cleanPath] = true
				if detectedFmt == models.MediaTypeAudiobook {
					trackedPaths[filepath.Clean(filepath.Dir(cleanPath))] = true
				}
				reconciledBooks[b.ID] = true
				reconciled++
				matched = true
				break
			}
		}
		if !matched && parsed.Title != "" {
			// normalizeTitle strips leading articles and inverts comma-suffix
			// sort form ("Title, A" → "title") so librarian-sorted folders
			// reconcile correctly (#513). Hoisted: computed once per file.
			normParsed := normalizeTitle(parsed.Title)
			// authorMatch is part of the title-tier predicate. Resolving the
			// matching authors first lets the candidate list be built from just
			// their books (booksByAuthor), iterated in library order. A nil set
			// means the parsed author is empty/initials-only — authorMatch then
			// accepts any author, so every wanted book is a candidate.
			if authorSet := matchingAuthors(parsed.Author); authorSet == nil {
				for i := range wantedBooks {
					if tryReconcileTitle(&wantedBooks[i], path, cleanPath, normParsed, detectedFmt) {
						matched = true
						break
					}
				}
			} else {
				titleCand = titleCand[:0]
				for id := range authorSet {
					titleCand = append(titleCand, booksByAuthor[id]...)
				}
				slices.Sort(titleCand) // restore library order across authors
				for _, idx := range titleCand {
					if tryReconcileTitle(&wantedBooks[idx], path, cleanPath, normParsed, detectedFmt) {
						matched = true
						break
					}
				}
			}
		}

		if !matched && s.series != nil && parsed.Series != "" && parsed.SeriesNumber != "" {
			book, seriesErr := s.series.GetBookBySeriesPosition(ctx, parsed.Series, parsed.SeriesNumber)
			if seriesErr != nil {
				slog.Warn("library scan: series position lookup error",
					"series", parsed.Series, "position", parsed.SeriesNumber, "error", seriesErr)
			} else if book != nil && !reconciledBooks[book.ID] {
				effDir := s.effectiveLibraryDir(ctx, authorMap[book.AuthorID])
				if pathUnderDir(path, effDir) {
					if err := s.books.AddBookFile(ctx, book.ID, detectedFmt, path); err != nil {
						slog.Error("library scan: failed to update book via series match", "id", book.ID, "error", err)
					} else {
						slog.Info("library scan: reconciled book via series position",
							"series", parsed.Series, "position", parsed.SeriesNumber, "title", book.Title, "path", path)
						trackedPaths[cleanPath] = true
						if detectedFmt == models.MediaTypeAudiobook {
							trackedPaths[filepath.Clean(filepath.Dir(cleanPath))] = true
						}
						reconciledBooks[book.ID] = true
						reconciled++
						matched = true
					}
				}
			}
		}

		if !matched {
			slog.Debug("library scan: unmatched file", "path", path, "parsedTitle", parsed.Title, "parsedAuthor", parsed.Author)
			unmatched++
			// Collect up to 1000 unmatched entries for UI display
			if len(unmatchedFiles) < 1000 {
				unmatchedFiles = append(unmatchedFiles, unmatchedFile{
					Path:         path,
					ParsedTitle:  parsed.Title,
					ParsedAuthor: parsed.Author,
				})
			}
		}
	}

	// Surface every walked root in the completion log. The scan can union
	// the audiobook root with the library root (see ScanLibrary), and the
	// pre-fix log only showed s.libraryDir, which let users with separate
	// roots interpret the file count as coming from the wrong directory
	// (issue #905, second symptom).
	scanRoots := []string{s.libraryDir}
	if s.audiobookDir != "" && s.audiobookDir != s.libraryDir {
		scanRoots = append(scanRoots, s.audiobookDir)
	}
	slog.Info("library scan complete", "paths", scanRoots, "bookFiles", len(foundFiles),
		"reconciled", reconciled, "unmatched", unmatched, "tagReadFailed", tagReadFailed)

	s.writeScanResult(ctx, len(foundFiles), reconciled, unmatched, alreadyTracked, tagReadFailed, unmatchedFiles)
}

// isReconcileCandidate reports whether a book should be considered for
// file-to-book matching during library scan. Returns true for:
//
//   - Status=Wanted books (the default case: the book has no file yet).
//   - Status=Imported books whose recorded file paths either are empty or
//     point at locations that no longer exist on disk. This covers #875
//     (Calibre import leaves Status=Imported with FilePath unpopulated when
//     Calibre's library mount differs from Bindery's view) and the related
//     case of users moving their library and re-running a scan.
//
// Books with a valid on-disk file at any recorded path are skipped — the
// scanner has no reason to re-reconcile a file that's already where it
// should be, and re-attaching would churn book_files rows for no benefit.
// chapterTitleRe matches embedded-tag titles that name a track/chapter rather
// than the book: a leading track number followed by a dash/dot/underscore and a
// NON-digit ("04 - Title", "04. Title", "04_Title"), or a Chapter/Track/Part/
// Disc/CD keyword followed by a number.
//
// The shape is deliberately narrow to avoid discarding real numeric titles:
//   - the {1,3}-digit cap skips years ("1984", "2001: A Space Odyssey");
//   - the colon is NOT a separator, so subtitle titles survive ("24: Live
//     Another Day", "7: Seven");
//   - requiring a non-digit after the separator skips number-dash/dot-number
//     titles ("1-800 Where R You", "3.14 …").
//
// A residual ambiguity remains: a genuinely numbered title ("21 - Jump Street",
// "8 - Mile") is indistinguishable from "04 - Chapter Name" without sibling or
// semantic context, so it is treated as a chapter. The blast radius is small —
// this only suppresses the tag title when the folder hierarchy already resolved
// a title to fall back to (see the caller), so the worst case is using the
// folder's title for such a book rather than the tag's.
var chapterTitleRe = regexp.MustCompile(`(?i)^(\d{1,3}\s*[-._]\s*\D|(chapter|track|part|disc|cd)\b\s*\.?\s*\d)`)

// looksLikeChapterTitle reports whether an embedded-tag title looks like a
// per-track chapter name rather than a book title (#1239).
func looksLikeChapterTitle(title string) bool {
	return chapterTitleRe.MatchString(strings.TrimSpace(title))
}

func isReconcileCandidate(b *models.Book) bool {
	if b == nil {
		return false
	}
	if b.Status == models.BookStatusWanted {
		return true
	}
	if b.Status != models.BookStatusImported {
		return false
	}
	// A dual-format book still missing a format on disk is a candidate even
	// though its other format is present, so the scan can attach the missing
	// edition (#1148). Without this, a 'both' book with the audiobook imported
	// but the ebook absent would never reconcile the ebook, because the loop
	// below short-circuits the moment any one path resolves.
	if b.NeedsEbook() || b.NeedsAudiobook() {
		return true
	}
	// Imported books reconcile only when no path we have on file actually
	// resolves to a real file. A book row may carry up to three legacy
	// path columns plus the modern book_files rows; the scanner already
	// trusts the trackedPaths set for book_files, so here we only need to
	// gate on the legacy columns.
	for _, p := range []string{b.FilePath, b.EbookFilePath, b.AudiobookFilePath} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return false
		}
	}
	return true
}

// unmatchedFile represents a file that could not be reconciled during library scan.
type unmatchedFile struct {
	Path         string `json:"path"`
	ParsedTitle  string `json:"parsed_title"`
	ParsedAuthor string `json:"parsed_author"`
}

// writeScanError persists a failed-scan result so the UI reflects the failure
// instead of a stale prior scan (#965). It reuses the same "library.lastScan"
// shape as a normal scan — zero counts plus a non-empty scan_error message the
// frontend renders the same way as the other scan-outcome warnings.
func (s *Scanner) writeScanError(ctx context.Context, message string) {
	s.writeScanResultWithError(ctx, 0, 0, 0, 0, 0, nil, message)
}

// writeScanResult persists the scan summary to the settings table under
// "library.lastScan" so the UI can surface the result without polling logs.
func (s *Scanner) writeScanResult(ctx context.Context, filesFound, reconciled, unmatched, alreadyTracked, tagReadFailed int, unmatchedFiles []unmatchedFile) {
	s.writeScanResultWithError(ctx, filesFound, reconciled, unmatched, alreadyTracked, tagReadFailed, unmatchedFiles, "")
}

// writeScanResultWithError is the shared writer for both successful scans and
// early-return failures. scanError is empty for a normal scan and a
// user-facing message when the scan could not complete (#965).
func (s *Scanner) writeScanResultWithError(ctx context.Context, filesFound, reconciled, unmatched, alreadyTracked, tagReadFailed int, unmatchedFiles []unmatchedFile, scanError string) {
	if s.settings == nil {
		return
	}

	// Marshal unmatched files to JSON
	var unmatchedJSON string
	if len(unmatchedFiles) > 0 {
		bytes, err := json.Marshal(unmatchedFiles)
		if err != nil {
			slog.Warn("library scan: failed to marshal unmatched files", "error", err)
			unmatchedJSON = "[]"
		} else {
			unmatchedJSON = string(bytes)
		}
	} else {
		unmatchedJSON = "[]"
	}

	// Surface the resolved roots that were actually walked so the UI can tell
	// the user WHICH paths produced the file count. The scanner unions the
	// audiobook root with the library root only when it differs (see
	// ScanLibrary), so mirror that logic here.
	scannedPaths := []string{s.libraryDir}
	if s.audiobookDir != "" && s.audiobookDir != s.libraryDir {
		scannedPaths = append(scannedPaths, s.audiobookDir)
	}
	pathsJSON := "[]"
	if bytes, err := json.Marshal(scannedPaths); err == nil {
		pathsJSON = string(bytes)
	}

	// noFilesFound is the explicit zero-files signal: the scan walked the
	// configured roots and found nothing. This distinguishes "wrong/empty
	// path" from "all files matched" (both can show files_found semantics the
	// user misreads) and lets the UI name the offending directory.
	noFilesFound := filesFound == 0

	// scan_error is "" for a successful scan and a user-facing message when the
	// scan could not complete (#965). Marshalled separately so any quoting in
	// the message (e.g. an underlying error string) is escaped correctly.
	scanErrorJSON := "\"\""
	if scanError != "" {
		if b, err := json.Marshal(scanError); err == nil {
			scanErrorJSON = string(b)
		}
	}

	payload := fmt.Sprintf(
		`{"ran_at":%q,"files_found":%d,"reconciled":%d,"unmatched":%d,"already_tracked":%d,"tag_read_failed":%d,"unmatched_files":%s,"library_dir":%q,"audiobook_dir":%q,"scanned_paths":%s,"no_files_found":%t,"scan_error":%s}`,
		time.Now().UTC().Format(time.RFC3339),
		filesFound, reconciled, unmatched, alreadyTracked, tagReadFailed,
		unmatchedJSON,
		s.libraryDir, s.audiobookDir, pathsJSON, noFilesFound, scanErrorJSON,
	)
	if err := s.settings.Set(ctx, "library.lastScan", payload); err != nil {
		slog.Warn("library scan: failed to persist scan result", "error", err)
	}
}
