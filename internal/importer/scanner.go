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
	"time"

	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/downloader/nzbget"
	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/downloader/transmission"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

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
	calibreMode          func() calibre.Mode
	calibreCoverCacheDir string
	settings             *db.SettingsRepo
	libraryDir           string
	audiobookDir         string
	audiobookDownloadDir string
	absLib               absNotifier
	absLibraryIDsFn      func() []string
	notif                eventNotifier
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

// pushToABS triggers an ABS library scan after a successful audiobook import.
// Failures are logged and swallowed — ABS sync is best-effort and must never
// roll back an otherwise-good Bindery import.
func (s *Scanner) pushToABS(ctx context.Context) {
	if s.absLib == nil || s.absLibraryIDsFn == nil {
		return
	}
	for _, libraryID := range uniqueNonEmptyStrings(s.absLibraryIDsFn()) {
		if err := s.absLib.ScanLibrary(ctx, libraryID); err != nil {
			slog.Warn("abs: library scan after audiobook import failed", "libraryID", libraryID, "error", err)
			continue
		}
		slog.Info("abs: triggered library scan after audiobook import", "libraryID", libraryID)
	}
}

func uniqueNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// WithSettings attaches a SettingsRepo to the scanner so scan results can be
// persisted under the "library.lastScan" key and surfaced via the API.
func (s *Scanner) WithSettings(sr *db.SettingsRepo) *Scanner {
	s.settings = sr
	return s
}

// importMode reads the "import.mode" setting and returns one of "move",
// "copy", "hardlink", or "external". When the setting is absent or
// unrecognised, it defaults to "hardlink" if src and dst are on the same
// filesystem (free, preserves seeding) or "copy" when they are on different
// filesystems. Pass empty strings for src/dst to get the cross-device default
// ("copy") without performing a stat.
func (s *Scanner) importMode(ctx context.Context, src, dst string) string {
	if s.settings != nil {
		setting, err := s.settings.Get(ctx, "import.mode")
		if err == nil && setting != nil {
			switch setting.Value {
			case "move", "copy", "hardlink", "external":
				return setting.Value
			}
		}
	}
	// No explicit setting — choose the safest mode that also preserves seeding.
	if sameDevice(src, dst) {
		return "hardlink"
	}
	slog.Warn("import.mode not set and src/dst are on different filesystems; defaulting to copy — seeding will be preserved but disk usage doubles")
	return "copy"
}

// pushToCWA copies the just-imported file into the directory watched by a
// sibling Calibre-Web-Automated container, when the cwa.ingest_path setting
// is configured. CWA's auto-ingest deletes whatever lands in that folder
// after processing, so we copy rather than move — bindery's own library
// stays intact regardless. Failures are logged and swallowed; CWA sync is
// best-effort and must never roll back an otherwise-good import.
//
// Only fires for ebook imports — CWA is built around ebook libraries
// (Calibre under the hood); audiobook handoff is a separate problem.
func (s *Scanner) pushToCWA(ctx context.Context, srcPath string) {
	if s.settings == nil || srcPath == "" {
		return
	}
	setting, err := s.settings.Get(ctx, "cwa.ingest_path")
	if err != nil || setting == nil || setting.Value == "" {
		return
	}
	ingestDir := setting.Value
	dst := filepath.Join(ingestDir, filepath.Base(srcPath))
	if err := CopyFileCtx(ctx, srcPath, dst); err != nil {
		slog.Warn("cwa: copy to ingest folder failed", "src", srcPath, "dst", dst, "error", err)
		return
	}
	slog.Info("cwa: file copied to ingest folder", "src", srcPath, "dst", dst)
}

// pushToCalibre mirrors a just-imported book into Calibre via calibredb add.
// Failures are logged and swallowed — Calibre sync is best-effort and must
// never roll back an otherwise-good Bindery import.
func (s *Scanner) pushToCalibre(ctx context.Context, book *models.Book, author *models.Author, edition *models.Edition, seriesTitle, seriesNum, path string) {
	if s.calibreMode == nil || book == nil {
		return
	}
	mode := s.calibreMode()
	if mode == calibre.ModeCalibredb || mode == calibre.ModePlugin {
		s.pushCalibreAdd(ctx, book, s.calibreMetadata(ctx, book, author, edition, seriesTitle, seriesNum, mode), path, mode)
	}
}

// pushCalibreAdd invokes the configured adder (calibredb CLI or plugin HTTP
// client) and persists the resulting calibre_id. Failures are best-effort —
// logged and swallowed so Bindery's own import stays good.
func (s *Scanner) pushCalibreAdd(ctx context.Context, book *models.Book, meta calibre.Metadata, path string, mode calibre.Mode) {
	if s.calibreAdder == nil {
		slog.Debug("calibre: adder is nil, skipping", "mode", mode, "bookId", book.ID)
		return
	}
	id, err := s.calibreAdder.Add(ctx, path, meta)
	if err != nil {
		if errors.Is(err, calibre.ErrDisabled) {
			return
		}
		if errors.Is(err, calibre.ErrAlreadyInCalibre) {
			slog.Info("calibre: book already in library", "mode", mode, "bookId", book.ID, "path", path, "calibreId", id)
			if id > 0 {
				if perr := s.books.SetCalibreID(ctx, book.ID, id); perr != nil {
					slog.Warn("calibre: persist calibre_id failed", "bookId", book.ID, "calibreId", id, "error", perr)
				}
			}
			return
		}
		slog.Warn("calibre: add failed, continuing", "mode", mode, "bookId", book.ID, "path", path, "error", err)
		return
	}
	if err := s.books.SetCalibreID(ctx, book.ID, id); err != nil {
		slog.Warn("calibre: persist calibre_id failed", "bookId", book.ID, "calibreId", id, "error", err)
		return
	}
	slog.Info("calibre: book mirrored", "mode", mode, "bookId", book.ID, "calibreId", id, "path", path)
}

func (s *Scanner) calibreMetadata(ctx context.Context, book *models.Book, author *models.Author, edition *models.Edition, seriesTitle, seriesNum string, mode calibre.Mode) calibre.Metadata {
	if book == nil {
		return calibre.Metadata{}
	}
	meta := calibre.Metadata{
		Title:         book.Title,
		Description:   book.Description,
		Genres:        book.Genres,
		Language:      calibre.NormalizeLanguageForCalibre(book.Language),
		Series:        seriesTitle,
		SeriesIndex:   seriesNum,
		PublishedDate: calibre.FormatPublishedDate(book.ReleaseDate),
		Rating:        book.AverageRating,
		Identifiers:   calibre.IdentifiersForBook(book, edition),
	}
	if author != nil {
		meta.Authors = []string{author.Name}
		meta.AuthorSort = author.SortName
	}
	imageURL := book.ImageURL
	if edition != nil {
		if strings.TrimSpace(edition.Publisher) != "" {
			meta.Publisher = edition.Publisher
		}
		if edition.PublishDate != nil {
			meta.PublishedDate = calibre.FormatPublishedDate(edition.PublishDate)
		}
		if strings.TrimSpace(edition.Language) != "" {
			meta.Language = calibre.NormalizeLanguageForCalibre(edition.Language)
		}
		if strings.TrimSpace(edition.ImageURL) != "" {
			imageURL = edition.ImageURL
		}
	}
	if mode == calibre.ModeCalibredb {
		if coverPath, err := calibre.MaterializeCover(ctx, s.calibreCoverCacheDir, imageURL); err != nil {
			slog.Debug("calibre: cover materialization skipped", "bookId", book.ID, "error", err)
		} else if coverPath != "" {
			meta.CoverPath = coverPath
		}
	}
	return meta
}

func firstString(values ...*string) string {
	for _, v := range values {
		if v != nil && strings.TrimSpace(*v) != "" {
			return strings.TrimSpace(*v)
		}
	}
	return ""
}

func (s *Scanner) resolveCalibreEdition(ctx context.Context, dl *models.Download, book *models.Book) *models.Edition {
	if s.editions == nil || book == nil {
		return nil
	}
	editions, err := s.editions.ListByBook(ctx, book.ID)
	if err != nil {
		slog.Debug("calibre: failed to list editions for metadata", "bookId", book.ID, "error", err)
		return nil
	}
	if len(editions) == 0 {
		return nil
	}
	if dl != nil && dl.EditionID != nil {
		if ed := findEditionByID(editions, *dl.EditionID); ed != nil {
			return ed
		}
	}
	if book.SelectedEditionID != nil {
		if ed := findEditionByID(editions, *book.SelectedEditionID); ed != nil {
			return ed
		}
	}
	for i := range editions {
		if editionHasCalibreMetadata(editions[i]) {
			return &editions[i]
		}
	}
	return &editions[0]
}

func findEditionByID(editions []models.Edition, id int64) *models.Edition {
	for i := range editions {
		if editions[i].ID == id {
			return &editions[i]
		}
	}
	return nil
}

func editionHasCalibreMetadata(e models.Edition) bool {
	return firstString(e.ISBN13, e.ISBN10, e.ASIN) != "" ||
		strings.TrimSpace(e.Publisher) != "" ||
		e.PublishDate != nil ||
		strings.TrimSpace(e.Language) != "" ||
		strings.TrimSpace(e.ImageURL) != ""
}

// importRetryLimit is the maximum number of times CheckDownloads will
// automatically retry a download stuck in StateImportFailed before giving
// up and leaving it for manual intervention (Bug #7).
const importRetryLimit = 3

// CheckDownloads polls all enabled download clients for status changes and
// updates the local download records. Every enabled client is polled in
// priority order so that downloads from secondary clients (e.g. a second
// qBittorrent instance) are never silently ignored (Bug #1).
func (s *Scanner) CheckDownloads(ctx context.Context) {
	client, err := s.clients.GetFirstEnabled(ctx)
	if err != nil || client == nil {
		return
	}

	switch client.Type {
	case "transmission":
		s.checkTransmissionDownloads(ctx, client)
	case "qbittorrent":
		s.checkQbittorrentDownloads(ctx, client)
	case "nzbget":
		s.checkNZBGetDownloads(ctx, client)
	default:
		s.checkSABnzbdDownloads(ctx, client)
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
// complete enumeration of the client's sources. Torrent clients return every
// torrent, so a missing entry definitively means the source is gone. Usenet
// history APIs are paginated (SABnzbd is capped at 50 slots here), so a missing
// entry there could merely be an aged-out-but-healthy download — for those
// callers this is false and only the retry-exhaustion case blocks, never the
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
		slog.Debug("blockStaleImportFailures: failed to list downloads", "error", err)
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

// checkSABnzbdDownloads polls SABnzbd for status changes.
func (s *Scanner) checkSABnzbdDownloads(ctx context.Context, client *models.DownloadClient) {
	sab := downloader.SabnzbdFor(client)

	// Check history for completed downloads (no category filter — match by NZO ID)
	history, err := sab.GetHistory(ctx, "", 50)
	if err != nil {
		slog.Debug("failed to fetch SABnzbd history", "error", err)
		return
	}

	// Track which downloads' sources we observed this cycle so stale
	// StateImportFailed downloads (history entry cleared / aged out) can be
	// terminally blocked rather than left stuck (issue #706 finding 4).
	seenSourceIDs := make(map[int64]bool)

	for _, slot := range history.Slots {
		dl, err := s.downloads.GetByNzoID(ctx, slot.NzoID)
		if err != nil || dl == nil {
			continue
		}
		seenSourceIDs[dl.ID] = true

		switch slot.Status {
		case "Completed":
			if dl.Status == models.StateDownloading || dl.Status == models.StateGrabbed {
				localPath := s.remapDownloadClientPath(client, slot.Path)
				if localPath != slot.Path {
					slog.Debug("remapped download path", "sab", slot.Path, "local", localPath)
				}
				slog.Info("download completed", "title", dl.Title, "path", localPath)
				s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
				s.tryImportSABnzbd(ctx, sab, dl, slot.NzoID, localPath)
			} else if dl.Status == models.StateImportFailed && dl.ImportRetryCount < importRetryLimit {
				// Bug #7: retry a previously failed import.
				localPath := s.remapDownloadClientPath(client, slot.Path)
				slog.Info("retrying failed import", "title", dl.Title, "path", localPath,
					"attempt", dl.ImportRetryCount+1, "limit", importRetryLimit)
				if err := s.downloads.IncrementImportRetryCount(ctx, dl.ID); err != nil {
					slog.Warn("failed to increment import retry count", "download_id", dl.ID, "error", err)
				}
				s.tryImportSABnzbd(ctx, sab, dl, slot.NzoID, localPath)
			}
		case "Failed":
			if dl.Status != models.StateFailed {
				slog.Warn("download failed", "title", dl.Title, "message", slot.FailMessage)
				s.setDownloadError(ctx, dl.ID, slot.FailMessage)
				s.createHistoryEvent(ctx, models.HistoryEventDownloadFailed, dl.Title, dl.BookID, map[string]string{"guid": dl.GUID, "message": slot.FailMessage})
				s.notify(ctx, notifierEventDownloadFailed, map[string]interface{}{
					"title":   dl.Title,
					"message": slot.FailMessage,
				})
			}
		}
	}

	// Terminally block StateImportFailed downloads whose retry budget is spent
	// (issue #706 finding 4). sourceListIsComplete is false: SABnzbd history is
	// paginated (capped at 50 slots above), so a download missing from this poll
	// may simply have aged out while still healthy — only retry-exhaustion is a
	// safe, definitive signal here.
	s.blockStaleImportFailures(ctx, seenSourceIDs, false, func(d models.Download) bool {
		return d.DownloadClientID != nil && *d.DownloadClientID == client.ID
	})
}

// checkNZBGetDownloads polls NZBGet for status changes using its JSON-RPC API.
func (s *Scanner) checkNZBGetDownloads(ctx context.Context, client *models.DownloadClient) {
	ng := downloader.NzbgetFor(client)

	// Check history for completed/failed downloads (matched by NZBID stored as sabnzbd_nzo_id).
	history, err := ng.GetHistory(ctx)
	if err != nil {
		slog.Debug("failed to fetch NZBGet history", "error", err)
		return
	}

	// Track which downloads' sources we observed this cycle (issue #706 finding 4).
	seenSourceIDs := make(map[int64]bool)

	for _, item := range history {
		nzbIDStr := strconv.Itoa(item.NZBID)
		dl, err := s.downloads.GetByNzoID(ctx, nzbIDStr)
		if err != nil || dl == nil {
			continue
		}
		seenSourceIDs[dl.ID] = true

		if nzbget.IsSuccess(item.Status) {
			if dl.Status == models.StateDownloading || dl.Status == models.StateGrabbed {
				localPath := s.remapDownloadClientPath(client, item.DestDir)
				if localPath != item.DestDir {
					slog.Debug("remapped download path", "nzbget", item.DestDir, "local", localPath)
				}
				slog.Info("download completed", "title", dl.Title, "path", localPath)
				s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
				s.tryImportNZBGet(ctx, ng, dl, item.NZBID, localPath)
			} else if dl.Status == models.StateImportFailed && dl.ImportRetryCount < importRetryLimit {
				// Bug #7: retry a previously failed import.
				localPath := s.remapDownloadClientPath(client, item.DestDir)
				slog.Info("retrying failed import", "title", dl.Title, "path", localPath,
					"attempt", dl.ImportRetryCount+1, "limit", importRetryLimit)
				if err := s.downloads.IncrementImportRetryCount(ctx, dl.ID); err != nil {
					slog.Warn("failed to increment import retry count", "download_id", dl.ID, "error", err)
				}
				s.tryImportNZBGet(ctx, ng, dl, item.NZBID, localPath)
			}
		} else if nzbget.IsFailure(item.Status) {
			if dl.Status != models.StateFailed {
				msg := fmt.Sprintf("NZBGet reported status: %s", item.Status)
				slog.Warn("download failed", "title", dl.Title, "status", item.Status)
				s.setDownloadError(ctx, dl.ID, msg)
				s.createHistoryEvent(ctx, models.HistoryEventDownloadFailed, dl.Title, dl.BookID, map[string]string{"guid": dl.GUID, "message": msg})
				s.notify(ctx, notifierEventDownloadFailed, map[string]interface{}{
					"title":   dl.Title,
					"message": msg,
				})
			}
		}
	}

	// Terminally block StateImportFailed downloads whose retry budget is spent
	// (issue #706 finding 4). sourceListIsComplete is false: the NZBGet history
	// response is not a guaranteed-complete enumeration of every source we might
	// still retry, so only retry-exhaustion is acted on here.
	s.blockStaleImportFailures(ctx, seenSourceIDs, false, func(d models.Download) bool {
		return d.DownloadClientID != nil && *d.DownloadClientID == client.ID
	})
}

// tryImportNZBGet attempts to import a completed NZBGet download into the library.
// ng is used to clean up the NZBGet history entry once bindery has taken ownership.
//
// NZBGet always lands a job inside a per-job DestDir, so walking that path is
// safe; the issue #903 file-list API addition does not apply here.
func (s *Scanner) tryImportNZBGet(ctx context.Context, ng *nzbget.Client, dl *models.Download, nzbID int, downloadPath string) {
	nzbIDStr := strconv.Itoa(nzbID)
	s.tryImportInternal(ctx, dl, downloadPath, "nzbget", nzbIDStr, func() error {
		return ng.RemoveHistory(ctx, nzbID)
	}, nil)
}

// checkTransmissionDownloads polls Transmission for status changes.
func (s *Scanner) checkTransmissionDownloads(ctx context.Context, client *models.DownloadClient) {
	trans := downloader.TransmissionFor(client)

	// Get all torrents — Category is used as the download directory filter so
	// Bindery only sees its own torrents on a shared Transmission instance.
	torrents, err := trans.GetTorrents(ctx, client.Category)
	if err != nil {
		slog.Debug("failed to fetch Transmission torrents", "error", err)
		return
	}

	// Get all active downloads from DB (not yet completed/imported)
	allDownloads, err := s.downloads.List(ctx)
	if err != nil {
		slog.Debug("failed to list downloads", "error", err)
		return
	}
	torrentsMap := make(map[string]transmission.Torrent)
	for _, t := range torrents {
		torrentsMap[fmt.Sprintf("%d", t.ID)] = t
	}

	// Track which downloads' sources we observed this cycle so stale
	// StateImportFailed downloads (torrent removed) can be terminally blocked
	// rather than left stuck below the retry limit (issue #706 finding 4).
	seenSourceIDs := make(map[int64]bool)

	for _, dl := range allDownloads {
		if dl.DownloadClientID == nil || *dl.DownloadClientID != client.ID || dl.TorrentID == nil {
			continue
		}
		torrent, ok := torrentsMap[*dl.TorrentID]
		if !ok {
			continue
		}
		seenSourceIDs[dl.ID] = true

		if dl.Status == models.StateImported || dl.Status == models.StateFailed {
			continue
		}

		// Status codes: 0=stopped, 1=checking, 2=downloading, 3=seeding, 4=allocating, 5=checking, 6=stopped
		isComplete := torrent.Status == 3 || (torrent.PercentDone >= 1.0)
		isStopped := torrent.Status == 0 || torrent.Status == 6
		stopError := strings.TrimSpace(torrent.ErrorString)

		if isComplete && (dl.Status == models.StateDownloading || dl.Status == models.StateGrabbed) {
			// Download is complete
			downloadPath := s.remapDownloadClientPath(client, torrent.DownloadDir)
			// Issue #903: ask Transmission for the authoritative file list so
			// the importer only touches files belonging to THIS torrent rather
			// than walking the shared download root. A nil return signals
			// transmissionFilesFor to fall back to the legacy dir walk (a WARN
			// is already emitted inside the helper).
			bookFiles := s.transmissionFilesFor(ctx, trans, client, torrent)
			slog.Info("download completed", "title", dl.Title, "path", downloadPath, "files", len(bookFiles))
			s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
			s.tryImportTransmission(ctx, &dl, downloadPath, bookFiles)
		} else if isComplete && dl.Status == models.StateImportFailed && dl.ImportRetryCount < importRetryLimit {
			// Bug #7: retry a previously failed import.
			downloadPath := s.remapDownloadClientPath(client, torrent.DownloadDir)
			bookFiles := s.transmissionFilesFor(ctx, trans, client, torrent)
			slog.Info("retrying failed import", "title", dl.Title, "path", downloadPath,
				"attempt", dl.ImportRetryCount+1, "limit", importRetryLimit, "files", len(bookFiles))
			if err := s.downloads.IncrementImportRetryCount(ctx, dl.ID); err != nil {
				slog.Warn("failed to increment import retry count", "download_id", dl.ID, "error", err)
			}
			s.tryImportTransmission(ctx, &dl, downloadPath, bookFiles)
		} else if isStopped && !isComplete && dl.Status != models.StateFailed {
			if stopError == "" {
				// Transmission also reports user-paused torrents as stopped.
				continue
			}
			slog.Warn("download failed", "title", dl.Title, "error", stopError)
			s.markDownloadFailed(ctx, &dl, stopError)
		}
	}

	// Terminally block StateImportFailed downloads whose torrent has been
	// removed from Transmission, or whose retry budget is spent (issue #706
	// finding 4). sourceListIsComplete is true: GetTorrents returns every
	// torrent, so a missing entry definitively means the source is gone.
	s.blockStaleImportFailures(ctx, seenSourceIDs, true, func(d models.Download) bool {
		return d.DownloadClientID != nil && *d.DownloadClientID == client.ID
	})
}

// checkQbittorrentDownloads polls qBittorrent for status changes.
func (s *Scanner) checkQbittorrentDownloads(ctx context.Context, client *models.DownloadClient) {
	qb := downloader.QbittorrentFor(client)

	allDownloads, err := s.downloads.List(ctx)
	if err != nil {
		slog.Debug("failed to list downloads", "error", err)
		return
	}

	// Poll every category this client may have grabbed under. Audiobook grabs
	// use CategoryAudiobook (e.g. "audiobooks") while ebook grabs use Category
	// (e.g. "ebook"); querying only Category leaves audiobook torrents out of
	// the result, so their downloads never match here and hang in "downloading"
	// forever. CategoriesToPoll returns both. The stall/health adapters were
	// already fixed for #700; this is the main import poll that was missed.
	torrentsMap := make(map[string]qbittorrent.Torrent)
	for _, cat := range downloader.CategoriesToPoll(client) {
		torrents, err := qb.GetTorrents(ctx, cat)
		if err != nil {
			slog.Debug("failed to fetch qBittorrent torrents", "category", cat, "error", err)
			return
		}
		for _, t := range torrents {
			torrentsMap[strings.ToLower(t.Hash)] = t
		}
	}

	slog.Debug("qbittorrent poll", "torrents", len(torrentsMap), "downloads", len(allDownloads), "categories", downloader.CategoriesToPoll(client))

	// Track which downloads' sources we observed this cycle (issue #706 finding 4).
	seenSourceIDs := make(map[int64]bool)

	for _, dl := range allDownloads {
		if dl.DownloadClientID == nil || *dl.DownloadClientID != client.ID || dl.TorrentID == nil {
			continue
		}
		torrent, ok := torrentsMap[strings.ToLower(*dl.TorrentID)]
		if !ok {
			// The torrent is not in qBittorrent's list. Common causes: category
			// filter mismatch, the torrent was manually removed, or the hash stored
			// in Bindery doesn't match what qBittorrent returned. This is
			// blockStaleImportFailures territory; only log at Debug to avoid noise.
			slog.Debug("qbittorrent: download not found in torrent list",
				"title", dl.Title, "hash", *dl.TorrentID, "dl_status", dl.Status)
			continue
		}
		seenSourceIDs[dl.ID] = true

		if dl.Status == models.StateImported || dl.Status == models.StateFailed {
			continue
		}

		state := strings.ToLower(torrent.State)
		isComplete := torrent.Progress >= 1.0 || strings.Contains(state, "upload") || strings.Contains(state, "stalledup") || strings.Contains(state, "checkingup")
		isFailed := strings.Contains(state, "error")

		slog.Debug("qbittorrent: torrent status",
			"title", dl.Title,
			"qbit_state", torrent.State,
			"progress", fmt.Sprintf("%.1f%%", torrent.Progress*100),
			"dl_status", dl.Status,
			"is_complete", isComplete)

		if isComplete && (dl.Status == models.StateDownloading || dl.Status == models.StateGrabbed) {
			rawPath, ok := resolveQbitContentPath(torrent)
			if !ok {
				// content_path is absent or points to a path that no longer exists.
				// This can happen when files were moved to the library by a prior
				// Bindery import (move mode) and the torrent is re-grabbed via a
				// 409 duplicate-add (#769). If the book is already in the library,
				// close out this download immediately rather than looping forever.
				if s.isBookAlreadyImported(ctx, &dl) {
					slog.Info("qbittorrent: content path gone but book already in library — marking as imported",
						"title", dl.Title)
					// Walk the state machine: grabbed/downloading → completed →
					// importPending → importing → imported.
					s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
					s.updateDownloadStatus(ctx, dl.ID, models.StateImportPending)
					s.updateDownloadStatus(ctx, dl.ID, models.StateImporting)
					s.updateDownloadStatus(ctx, dl.ID, models.StateImported)
					continue
				}
				// Path doesn't exist on disk yet (qBittorrent may sanitise characters
				// in the torrent name that differ from what the API reports, e.g. ':'→'_').
				// Do NOT fall back to torrent.SavePath — for multi-file torrents that is
				// the shared download root and walking it would import every unrelated file.
				// Leave the status unchanged so the next check cycle retries.
				slog.Warn("qbittorrent: content path not found, will retry next cycle",
					"title", dl.Title,
					"save_path", torrent.SavePath,
					"name", torrent.Name)
				continue
			}
			downloadPath := s.remapDownloadClientPath(client, rawPath)

			// Issue #903: ask qBittorrent for the authoritative file list so
			// the importer only touches files belonging to THIS torrent.
			// qbittorrentFilesFor returns nil and logs a WARN on RPC error
			// or empty-file response; tryImportInternal then falls back to
			// the legacy filepath.Walk(downloadPath).
			bookFiles := s.qbittorrentFilesFor(ctx, qb, client, torrent)
			slog.Info("download completed", "title", dl.Title, "path", downloadPath, "raw_path", rawPath, "files", len(bookFiles))
			s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
			s.tryImportQbittorrent(ctx, &dl, downloadPath, bookFiles)
		} else if isComplete && dl.Status == models.StateImportFailed && dl.ImportRetryCount < importRetryLimit {
			// Bug #7: a previous import attempt failed (e.g. transient filesystem
			// error, path mismatch). The torrent is still seeding so we have the
			// files — retry the import rather than leaving it stuck permanently.
			rawPath, ok := resolveQbitContentPath(torrent)
			if !ok {
				// Same guard as the StateGrabbed branch: if the book is already in
				// the library (files moved by a prior import), close out cleanly.
				if s.isBookAlreadyImported(ctx, &dl) {
					slog.Info("qbittorrent: content path gone but book already in library — marking as imported",
						"title", dl.Title)
					s.updateDownloadStatus(ctx, dl.ID, models.StateImportPending)
					s.updateDownloadStatus(ctx, dl.ID, models.StateImporting)
					s.updateDownloadStatus(ctx, dl.ID, models.StateImported)
					continue
				}
				slog.Warn("qbittorrent: content path not found during import retry, will retry next cycle",
					"title", dl.Title,
					"save_path", torrent.SavePath,
					"name", torrent.Name,
					"attempt", dl.ImportRetryCount+1)
				continue
			}
			downloadPath := s.remapDownloadClientPath(client, rawPath)
			bookFiles := s.qbittorrentFilesFor(ctx, qb, client, torrent)
			slog.Info("retrying failed import", "title", dl.Title, "path", downloadPath,
				"attempt", dl.ImportRetryCount+1, "limit", importRetryLimit, "files", len(bookFiles))
			if err := s.downloads.IncrementImportRetryCount(ctx, dl.ID); err != nil {
				slog.Warn("failed to increment import retry count", "download_id", dl.ID, "error", err)
			}
			s.tryImportQbittorrent(ctx, &dl, downloadPath, bookFiles)
		} else if isFailed && dl.Status != models.StateFailed {
			slog.Warn("download failed", "title", dl.Title, "state", torrent.State)
			s.markDownloadFailed(ctx, &dl, "Torrent failed in qBittorrent")
		}
	}

	// Terminally block StateImportFailed downloads whose torrent has been
	// removed from qBittorrent, or whose retry budget is spent (issue #706
	// finding 4). sourceListIsComplete is true: GetTorrents returns every
	// torrent, so a missing entry definitively means the source is gone.
	s.blockStaleImportFailures(ctx, seenSourceIDs, true, func(d models.Download) bool {
		return d.DownloadClientID != nil && *d.DownloadClientID == client.ID
	})
}

// tryImportSABnzbd attempts to import a completed SABnzbd download into the library.
// sab is used to clear the SABnzbd history entry once bindery has taken
// ownership of the files; nzoID is the history slot's NZO identifier.
//
// SAB always lands a job inside a per-job completed-folder (`storage` in the
// history slot), so walking that path is safe; the issue #903 file-list
// API addition does not apply here.
func (s *Scanner) tryImportSABnzbd(ctx context.Context, sab *sabnzbd.Client, dl *models.Download, nzoID, downloadPath string) {
	s.tryImportInternal(ctx, dl, downloadPath, "sabnzbd", nzoID, func() error {
		// Clean up SABnzbd history
		return sab.DeleteHistory(ctx, nzoID, false)
	}, nil)
}

// tryImportTransmission attempts to import a completed Transmission download into the library.
//
// explicitFiles, when non-nil and non-empty, is the absolute Bindery-side
// path of every file that belongs to this specific torrent (built from the
// Transmission torrent-get "files" RPC and run through PathRemap). Passing
// it avoids the legacy filepath.Walk(downloadPath) and the issue #903 class
// of bug where a single-file torrent at a shared download root would cause
// every unrelated sibling to be imported. Pass nil to fall back to the
// directory walk.
func (s *Scanner) tryImportTransmission(ctx context.Context, dl *models.Download, downloadPath string, explicitFiles []string) {
	s.tryImportInternal(ctx, dl, downloadPath, "transmission", safeRemoteID(dl.TorrentID), nil, explicitFiles)
}

// tryImportQbittorrent attempts to import a completed qBittorrent download. See
// tryImportTransmission for the semantics of explicitFiles.
func (s *Scanner) tryImportQbittorrent(ctx context.Context, dl *models.Download, downloadPath string, explicitFiles []string) {
	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", safeRemoteID(dl.TorrentID), nil, explicitFiles)
}

// torrentFile is the minimal shape resolveTorrentFiles consumes; it matches
// transmission.File / qbittorrent.File / deluge.File without taking a
// dependency on any of them. Each downloader's File type is converted to
// []torrentFile at the call site.
type torrentFile struct {
	Name string
	Size int64
}

// resolveTorrentFiles maps a downloader's per-torrent file list onto absolute
// Bindery-side book-file paths. For each file:
//
//  1. Join the client's save path with the file's relative name, producing
//     the path the download client sees on its filesystem.
//  2. Apply the download-client's PathRemap (and the global scanner remapper
//     when no client-level rule matches) so Bindery sees the file at its
//     local mount point. This is the same helper checkXxxDownloads already
//     uses for the per-torrent downloadPath, so a single shared rule covers
//     both the parent dir and the files inside it.
//  3. Filter to book files via IsBookFile to match what the legacy
//     filepath.Walk path produced.
//
// Files with empty or path-traversing names ("..", absolute paths) are
// rejected and logged at WARN; they shouldn't reach here from a sane client
// response and treating them as legitimate could resolve outside the
// download root.
//
// The Bindery-side absolute path is returned with filepath.Clean applied so
// downstream code (cleanupMovedSources, alreadyImportedPath) compares clean
// forms consistently.
func (s *Scanner) resolveTorrentFiles(client *models.DownloadClient, clientSavePath string, files []torrentFile) []string {
	if len(files) == 0 || strings.TrimSpace(clientSavePath) == "" {
		return nil
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			continue
		}
		// Reject absolute paths and any ".." path segment: both can resolve
		// outside the torrent's save path; a sane client never produces them
		// in the files-list response, so treat them as malformed and skip
		// rather than silently quoting an attacker-controlled name through
		// Join. Splitting and matching per-segment avoids false positives on
		// legitimate names like "My..Book.epub" while still catching
		// "MyBook/../escape.epub".
		if filepath.IsAbs(name) || hasDotDotSegment(name) {
			slog.Warn("import: rejecting malformed file name from download client",
				"client", client.Name, "name", name)
			continue
		}
		clientPath := filepath.Join(clientSavePath, name)
		binderyPath := filepath.Clean(s.remapDownloadClientPath(client, clientPath))
		if !IsBookFile(binderyPath) {
			continue
		}
		out = append(out, binderyPath)
	}
	return out
}

// hasDotDotSegment reports whether p contains a ".." path segment under
// either forward-slash or platform separators. The downloader Files() APIs
// normalise to forward slash already, but checking both is defensive — a
// rogue Windows-format response then can't smuggle a "..\\" past the
// guard.
func hasDotDotSegment(p string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// resolveAudiobookSource decides what to move/copy/hardlink for an audiobook
// import when the caller supplied an explicit per-torrent file list. The
// audiobook flow normally moves the whole download folder so cover art,
// cue sheets and other non-book companions land together, which is wrong
// for a single-file torrent whose downloadPath is a shared download root
// (issue #903).
//
// Returns either (path, false) when a safe source path is found:
//
//   - For a single book file the file's path itself (the existing
//     not-a-directory branch then places it inside destDir).
//   - For multiple book files sharing a common directory strictly under
//     downloadPath, that common directory (so companion files within the
//     torrent's folder ride along).
//
// Returns ("", true) when no safe directory exists, signalling the caller
// to fall back to per-file placement. This covers the shape where bookFiles
// share no parent below the (shared) downloadPath, i.e. exactly the
// dangerous case the issue describes.
func (s *Scanner) resolveAudiobookSource(downloadPath string, bookFiles []string) (string, bool) {
	if len(bookFiles) == 0 {
		return downloadPath, false
	}
	if len(bookFiles) == 1 {
		return bookFiles[0], false
	}
	common := filepath.Clean(filepath.Dir(bookFiles[0]))
	for _, f := range bookFiles[1:] {
		fDir := filepath.Clean(filepath.Dir(f))
		// Walk common upward until it sits at or above fDir.
		for common != fDir && !pathUnderDir(fDir, common) {
			parent := filepath.Dir(common)
			if parent == common {
				break
			}
			common = parent
		}
	}
	cleanDownload := filepath.Clean(downloadPath)
	// Only accept a common directory that is strictly under downloadPath.
	// Equal-to-downloadPath means the bookFiles sit at the shared download
	// root (the issue #903 shape) and moving downloadPath would catch
	// unrelated siblings. Outside-downloadPath should never happen if the
	// remap is consistent; treat the same way.
	if common == cleanDownload || !pathUnderDir(common, cleanDownload) {
		return "", true
	}
	return common, false
}

// transmissionFilesFor calls Transmission's torrent-get "files" RPC for the
// supplied torrent and returns the absolute Bindery-side book-file paths,
// or nil when the call fails or the torrent reported no files yet. A nil
// return signals tryImportInternal to fall back to filepath.Walk; the
// caller is responsible for emitting the WARN log that records the
// fallback so an operator can spot a misconfigured / unreachable client.
func (s *Scanner) transmissionFilesFor(ctx context.Context, trans *transmission.Client, client *models.DownloadClient, torrent transmission.Torrent) []string {
	files, err := trans.Files(ctx, torrent.ID)
	if err != nil {
		slog.Warn("import: Transmission Files RPC failed, falling back to directory walk (issue #903 fallback)",
			"title", torrent.Name, "id", torrent.ID, "error", err)
		return nil
	}
	if len(files) == 0 {
		slog.Warn("import: Transmission reported no files for torrent yet, falling back to directory walk",
			"title", torrent.Name, "id", torrent.ID)
		return nil
	}
	conv := make([]torrentFile, 0, len(files))
	for _, f := range files {
		conv = append(conv, torrentFile{Name: f.Name, Size: f.Size})
	}
	return s.resolveTorrentFiles(client, torrent.DownloadDir, conv)
}

// qbittorrentFilesFor calls qBittorrent's /torrents/files API for the
// supplied torrent and returns the absolute Bindery-side book-file paths,
// or nil when the call fails or qBittorrent reported no files yet.
//
// SavePath, not ContentPath, is the join base: qBittorrent's files API
// returns names that include the torrent's display folder (e.g.
// "MyBook/file.epub") when the torrent has one, and just the basename for
// single-file torrents. Joining against SavePath reproduces what's on disk
// in both cases. ContentPath is the wrong base for multi-file torrents
// because the file names already include the folder.
func (s *Scanner) qbittorrentFilesFor(ctx context.Context, qb *qbittorrent.Client, client *models.DownloadClient, torrent qbittorrent.Torrent) []string {
	files, err := qb.Files(ctx, torrent.Hash)
	if err != nil {
		slog.Warn("import: qBittorrent Files API failed, falling back to directory walk (issue #903 fallback)",
			"title", torrent.Name, "hash", torrent.Hash, "error", err)
		return nil
	}
	if len(files) == 0 {
		slog.Warn("import: qBittorrent reported no files for torrent yet, falling back to directory walk",
			"title", torrent.Name, "hash", torrent.Hash)
		return nil
	}
	conv := make([]torrentFile, 0, len(files))
	for _, f := range files {
		conv = append(conv, torrentFile{Name: f.Name, Size: f.Size})
	}
	return s.resolveTorrentFiles(client, torrent.SavePath, conv)
}

func (s *Scanner) remapDownloadClientPath(client *models.DownloadClient, rawPath string) string {
	if client != nil && strings.TrimSpace(client.PathRemap) != "" {
		if localPath := ParseRemap(client.PathRemap).Apply(rawPath); localPath != rawPath {
			return localPath
		}
	}
	return s.remapper.Apply(rawPath)
}

// resolveQbitContentPath returns the on-disk content path for a completed torrent.
//
// qBittorrent ≥ 4.1.x populates content_path with the authoritative on-disk path,
// correctly reflecting any character sanitisation it applied to the torrent name
// (e.g. ':' → '_'). When content_path is available it is used directly.
//
// For older clients that omit content_path the function falls back to
// filepath.Join(SavePath, Name) and verifies the path exists with os.Stat.
//
// SavePath is deliberately never returned on its own. For multi-file torrents
// SavePath is the shared download root; falling back to it would cause Bindery
// to walk and import every unrelated file in that directory.
func resolveQbitContentPath(t qbittorrent.Torrent) (string, bool) {
	if t.ContentPath != "" {
		return t.ContentPath, true
	}
	candidate := filepath.Join(t.SavePath, t.Name)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, true
	}
	return "", false
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

// tryImportInternal is the common import logic shared by SABnzbd, NZBGet,
// Transmission and qBittorrent.
//
// explicitFiles, when non-nil and non-empty, is the authoritative list of
// absolute Bindery-side file paths that belong to the download. Passing it
// short-circuits the legacy filepath.Walk(downloadPath) discovery and is
// the issue #903 fix: with a shared download root (Transmission's default)
// a single-file torrent's downloadPath is the entire shared root and a
// directory walk would import every unrelated sibling. The list is built
// from the per-client Files() RPC by the caller; pass nil when no such
// list is available (older client API, RPC error) and the legacy walk
// runs as the fallback.
func (s *Scanner) tryImportInternal(ctx context.Context, dl *models.Download, downloadPath, cleanupClientType, cleanupRemoteID string, cleanupFunc func() error, explicitFiles []string) {
	if s.libraryDir == "" {
		slog.Warn("no library directory configured, skipping import")
		// Not writable/configured — needs user action before import can proceed.
		s.failImport(ctx, dl, models.StateImportBlocked, "no library directory configured — set one in Settings")
		return
	}

	s.updateDownloadStatus(ctx, dl.ID, models.StateImportPending)

	// Resolve the import mode ONCE for the whole download (issue #705 finding 2).
	// Re-reading "import.mode" per file means an operator toggling copy↔move in
	// the UI mid-run mixes copy and move within a single download; the final
	// cleanup could then RemoveAll the still-seeding source of a file that was
	// deliberately copied. The decided mode is threaded through every call site
	// below — no further DB reads of "import.mode" occur for this import.
	importMode := s.importMode(ctx, downloadPath, s.libraryDir)

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
	if importMode == "external" {
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
	var bookFiles []string
	if len(explicitFiles) > 0 {
		bookFiles = explicitFiles
	} else {
		if err := filepath.Walk(downloadPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if IsBookFile(path) {
				bookFiles = append(bookFiles, path)
			}
			return nil
		}); err != nil {
			slog.Warn("failed to walk download path", "path", downloadPath, "error", err)
		}
	}

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

	// Detect the format of the downloaded files from their extensions.
	// This is authoritative for dual-format books (media_type='both') and
	// also fixes edge-cases where a mislabelled book would be routed to the
	// wrong library directory.
	detectedFormat := detectDownloadFormat(bookFiles)

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
		mode := importMode
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
				switch mode {
				case "hardlink":
					dirErr = HardlinkDir(audiobookSource, destDir)
				case "copy":
					dirErr = CopyDirCtx(importCtx, audiobookSource, destDir)
				default:
					dirErr = MoveDirCtx(importCtx, audiobookSource, destDir)
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
			if err := s.books.SetFormatFilePath(ctx, book.ID, models.MediaTypeAudiobook, destDir); err != nil {
				slog.Error("failed to update audiobook file path", "bookID", book.ID, "error", err)
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
	for _, srcFile := range bookFiles {
		if book == nil {
			// Try to match from filename
			parsed := ParseFilename(srcFile)
			slog.Info("unmatched import", "title", parsed.Title, "author", parsed.Author, "file", srcFile)
			continue
		}

		seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)
		destPath, destErr := s.renamer.DestPath(s.effectiveLibraryDir(ctx, author), author, book, seriesTitle, seriesNum, srcFile)
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

		mode := importMode
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

		s.createHistoryEvent(ctx, models.HistoryEventBookImported, dl.Title, dl.BookID, map[string]string{"path": destPath, "format": models.MediaTypeEbook})
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

	// Matched nothing — no book resolved and nothing imported. Surface that
	// so the user can manually intervene rather than seeing a silent queue.
	if imported == 0 && failed == 0 && book == nil {
		s.failImport(ctx, dl, models.StateImportFailed, "could not match any book to this download — check the release title")
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
		if importMode == "move" {
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
func cleanLayoutTitle(dir string) string {
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

// ScanLibrary walks the library directory (and the separate audiobook directory
// when configured) for book files not yet tracked in the database and reconciles
// found files with existing "wanted" book records.
func (s *Scanner) ScanLibrary(ctx context.Context) {
	if s.libraryDir == "" {
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
		s.writeScanResult(ctx, len(foundFiles), 0, 0, 0, nil)
		return
	}

	// Load all books from the DB for reconciliation
	allBooks, err := s.books.List(ctx)
	if err != nil {
		slog.Error("library scan: failed to list books", "error", err)
		return
	}

	// Build a set of tracked file paths AND their parent directories.
	// Populated from book_files (all registered paths, not just the first per
	// format) so that multi-file books don't show their non-first files as
	// untracked on subsequent scans.
	trackedPaths := make(map[string]bool)
	if allPaths, err := s.books.ListAllBookFilePaths(ctx); err == nil {
		for _, p := range allPaths {
			cleanP := filepath.Clean(p)
			trackedPaths[cleanP] = true
			trackedPaths[filepath.Clean(filepath.Dir(cleanP))] = true
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
	var reconciled, unmatched, tagReadFailed int

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
		reconciledBooks[b.ID] = true
		reconciled++
		return true
	}

	for _, path := range foundFiles {
		// Skip files already tracked, or files inside a tracked directory
		// (individual tracks inside an already-imported audiobook folder).
		cleanPath := filepath.Clean(path)
		if trackedPaths[cleanPath] || trackedPaths[filepath.Clean(filepath.Dir(cleanPath))] {
			continue
		}

		// Parse the filename for title/author hints, then let the folder
		// hierarchy correct them. A file under <root>/<Author>/<Book>/<file>
		// names author and title unambiguously and dash-safe, unlike splitting
		// an "Author - Title" / "Title - Author" filename — the scan must not
		// assume a single filename order (#754).
		parsed := ParseFilename(path)
		if a, t, ok := authorTitleFromLayout(path, s.libraryDir, s.audiobookDir); ok {
			if a != "" {
				parsed.Author = a
			}
			if t != "" {
				parsed.Title = t
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
				if tags.Title != "" {
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

	s.writeScanResult(ctx, len(foundFiles), reconciled, unmatched, tagReadFailed, unmatchedFiles)
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

// writeScanResult persists the scan summary to the settings table under
// "library.lastScan" so the UI can surface the result without polling logs.
func (s *Scanner) writeScanResult(ctx context.Context, filesFound, reconciled, unmatched, tagReadFailed int, unmatchedFiles []unmatchedFile) {
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

	payload := fmt.Sprintf(
		`{"ran_at":%q,"files_found":%d,"reconciled":%d,"unmatched":%d,"tag_read_failed":%d,"unmatched_files":%s}`,
		time.Now().UTC().Format(time.RFC3339),
		filesFound, reconciled, unmatched, tagReadFailed,
		unmatchedJSON,
	)
	if err := s.settings.Set(ctx, "library.lastScan", payload); err != nil {
		slog.Warn("library scan: failed to persist scan result", "error", err)
	}
}
