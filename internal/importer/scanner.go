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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
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
	absLibraryIDFn       func() string
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

// WithABSNotifier attaches an Audiobookshelf scan notifier. libraryIDFn is
// called at import time to retrieve the current ABS audiobook library ID;
// returning an empty string disables the notification for that import.
func (s *Scanner) WithABSNotifier(n absNotifier, libraryIDFn func() string) *Scanner {
	s.absLib = n
	s.absLibraryIDFn = libraryIDFn
	return s
}

// pushToABS triggers an ABS library scan after a successful audiobook import.
// Failures are logged and swallowed — ABS sync is best-effort and must never
// roll back an otherwise-good Bindery import.
func (s *Scanner) pushToABS(ctx context.Context) {
	if s.absLib == nil || s.absLibraryIDFn == nil {
		return
	}
	libraryID := s.absLibraryIDFn()
	if libraryID == "" {
		return
	}
	if err := s.absLib.ScanLibrary(ctx, libraryID); err != nil {
		slog.Warn("abs: library scan after audiobook import failed", "libraryID", libraryID, "error", err)
		return
	}
	slog.Info("abs: triggered library scan after audiobook import", "libraryID", libraryID)
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

// RecoverInterruptedImports sweeps downloads stuck mid-import back into a
// retryable state. It must be called once at startup, before the scheduler
// begins polling.
//
// A process crash or the 30-minute per-import timeout can leave a download in
// StateImporting (or the earlier StateImportPending) — both non-terminal states
// with no automatic re-entry. CheckDownloads only retries from
// StateImportFailed, so without this sweep such a download is wedged forever
// (issue #706 finding 1). Moving it to StateImportFailed lets the existing
// retry path (CheckDownloads, while the source is still in the client) pick it
// up; if the source has since vanished, the same path now terminally blocks it
// (finding 4) rather than leaving it silently stuck.
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
// is visible in the Queue/History UI.
func (s *Scanner) failImport(ctx context.Context, dl *models.Download, status models.DownloadState, reason string) {
	if err := s.downloads.SetErrorWithStatus(ctx, dl.ID, status, reason); err != nil {
		slog.Warn("failed to persist import error", "download_id", dl.ID, "status", status, "error", err)
	}
	s.createHistoryEvent(ctx, models.HistoryEventImportFailed, dl.Title, dl.BookID, map[string]string{
		"guid":    dl.GUID,
		"message": reason,
		"status":  string(status),
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
	sab := sabnzbd.New(client.Host, client.Port, client.APIKey, client.URLBase, client.UseSSL)

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
	ng := nzbget.New(client.Host, client.Port, client.Username, client.Password, client.URLBase, client.UseSSL)

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
func (s *Scanner) tryImportNZBGet(ctx context.Context, ng *nzbget.Client, dl *models.Download, nzbID int, downloadPath string) {
	nzbIDStr := strconv.Itoa(nzbID)
	s.tryImportInternal(ctx, dl, downloadPath, "nzbget", nzbIDStr, func() error {
		return ng.RemoveHistory(ctx, nzbID)
	})
}

// checkTransmissionDownloads polls Transmission for status changes.
func (s *Scanner) checkTransmissionDownloads(ctx context.Context, client *models.DownloadClient) {
	trans := transmission.New(client.Host, client.Port, client.Username, client.Password, client.URLBase, client.UseSSL)

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
			slog.Info("download completed", "title", dl.Title, "path", downloadPath)
			s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
			s.tryImportTransmission(ctx, &dl, downloadPath)
		} else if isComplete && dl.Status == models.StateImportFailed && dl.ImportRetryCount < importRetryLimit {
			// Bug #7: retry a previously failed import.
			downloadPath := s.remapDownloadClientPath(client, torrent.DownloadDir)
			slog.Info("retrying failed import", "title", dl.Title, "path", downloadPath,
				"attempt", dl.ImportRetryCount+1, "limit", importRetryLimit)
			if err := s.downloads.IncrementImportRetryCount(ctx, dl.ID); err != nil {
				slog.Warn("failed to increment import retry count", "download_id", dl.ID, "error", err)
			}
			s.tryImportTransmission(ctx, &dl, downloadPath)
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
	qb := qbittorrent.New(client.Host, client.Port, client.Username, client.Password, client.URLBase, client.UseSSL)

	torrents, err := qb.GetTorrents(ctx, client.Category)
	if err != nil {
		slog.Debug("failed to fetch qBittorrent torrents", "error", err)
		return
	}

	allDownloads, err := s.downloads.List(ctx)
	if err != nil {
		slog.Debug("failed to list downloads", "error", err)
		return
	}
	torrentsMap := make(map[string]qbittorrent.Torrent)
	for _, t := range torrents {
		torrentsMap[strings.ToLower(t.Hash)] = t
	}

	// Track which downloads' sources we observed this cycle (issue #706 finding 4).
	seenSourceIDs := make(map[int64]bool)

	for _, dl := range allDownloads {
		if dl.DownloadClientID == nil || *dl.DownloadClientID != client.ID || dl.TorrentID == nil {
			continue
		}
		torrent, ok := torrentsMap[strings.ToLower(*dl.TorrentID)]
		if !ok {
			continue
		}
		seenSourceIDs[dl.ID] = true

		if dl.Status == models.StateImported || dl.Status == models.StateFailed {
			continue
		}

		state := strings.ToLower(torrent.State)
		isComplete := torrent.Progress >= 1.0 || strings.Contains(state, "upload") || strings.Contains(state, "stalledup") || strings.Contains(state, "checkingup")
		isFailed := strings.Contains(state, "error")

		if isComplete && (dl.Status == models.StateDownloading || dl.Status == models.StateGrabbed) {
			rawPath, ok := resolveQbitContentPath(torrent)
			if !ok {
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

			slog.Info("download completed", "title", dl.Title, "path", downloadPath)
			s.updateDownloadStatus(ctx, dl.ID, models.StateCompleted)
			s.tryImportQbittorrent(ctx, &dl, downloadPath)
		} else if isComplete && dl.Status == models.StateImportFailed && dl.ImportRetryCount < importRetryLimit {
			// Bug #7: a previous import attempt failed (e.g. transient filesystem
			// error, path mismatch). The torrent is still seeding so we have the
			// files — retry the import rather than leaving it stuck permanently.
			rawPath, ok := resolveQbitContentPath(torrent)
			if !ok {
				slog.Warn("qbittorrent: content path not found during import retry, will retry next cycle",
					"title", dl.Title,
					"save_path", torrent.SavePath,
					"name", torrent.Name,
					"attempt", dl.ImportRetryCount+1)
				continue
			}
			downloadPath := s.remapDownloadClientPath(client, rawPath)
			slog.Info("retrying failed import", "title", dl.Title, "path", downloadPath,
				"attempt", dl.ImportRetryCount+1, "limit", importRetryLimit)
			if err := s.downloads.IncrementImportRetryCount(ctx, dl.ID); err != nil {
				slog.Warn("failed to increment import retry count", "download_id", dl.ID, "error", err)
			}
			s.tryImportQbittorrent(ctx, &dl, downloadPath)
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
func (s *Scanner) tryImportSABnzbd(ctx context.Context, sab *sabnzbd.Client, dl *models.Download, nzoID, downloadPath string) {
	s.tryImportInternal(ctx, dl, downloadPath, "sabnzbd", nzoID, func() error {
		// Clean up SABnzbd history
		return sab.DeleteHistory(ctx, nzoID, false)
	})
}

// tryImportTransmission attempts to import a completed Transmission download into the library.
func (s *Scanner) tryImportTransmission(ctx context.Context, dl *models.Download, downloadPath string) {
	s.tryImportInternal(ctx, dl, downloadPath, "transmission", safeRemoteID(dl.TorrentID), nil)
}

func (s *Scanner) tryImportQbittorrent(ctx context.Context, dl *models.Download, downloadPath string) {
	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", safeRemoteID(dl.TorrentID), nil)
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

// tryImportInternal is the common import logic shared by SABnzbd and Transmission.
func (s *Scanner) tryImportInternal(ctx context.Context, dl *models.Download, downloadPath, cleanupClientType, cleanupRemoteID string, cleanupFunc func() error) {
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

	// Find book files in the download path
	var bookFiles []string
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

	if len(bookFiles) == 0 {
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
		// audiobookRoot always starts from BINDERY_AUDIOBOOK_DIR (set at
		// startup). effectiveLibraryDir is format-agnostic — it resolves the
		// per-author ebook root folder — so applying it here would send
		// audiobooks into the ebook root whenever the author has any custom
		// root folder assigned, silently ignoring BINDERY_AUDIOBOOK_DIR
		// (#421). Until a per-author audiobook root folder field exists we
		// leave audiobookRoot as-is.
		audiobookRoot := s.audiobookDir
		seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)
		audiobookDest, destErr := s.renamer.AudiobookDestDir(audiobookRoot, author, book, seriesTitle, seriesNum)
		if destErr != nil {
			slog.Error("failed to compute audiobook destination", "src", downloadPath, "error", destErr)
			s.failImport(ctx, dl, models.StateImportBlocked, fmt.Sprintf("audiobook destination invalid: %v", destErr))
			return
		}
		destDir := UniqueDir(audiobookDest)
		mode := importMode
		slog.Info("importing audiobook folder", "src", downloadPath, "dst", destDir, "mode", mode)
		// Single-file audiobook releases (e.g. a lone .m4b from a torrent) give
		// us a file path rather than a folder. MoveDir/CopyDir/HardlinkDir all
		// reject non-directory sources, so place the file inside destDir.
		srcInfo, statErr := os.Stat(downloadPath)
		if statErr != nil {
			slog.Error("failed to stat audiobook source", "src", downloadPath, "error", statErr)
			s.failImport(ctx, dl, models.StateImportBlocked, fmt.Sprintf("audiobook source unavailable: %v", statErr))
			return
		}
		var dirErr error
		if srcInfo.IsDir() {
			switch mode {
			case "hardlink":
				dirErr = HardlinkDir(downloadPath, destDir)
			case "copy":
				dirErr = CopyDirCtx(importCtx, downloadPath, destDir)
			default:
				dirErr = MoveDirCtx(importCtx, downloadPath, destDir)
			}
		} else {
			if err := os.MkdirAll(destDir, 0o750); err != nil {
				dirErr = fmt.Errorf("create audiobook dest dir: %w", err)
			} else {
				dstFile := filepath.Join(destDir, filepath.Base(downloadPath))
				switch mode {
				case "hardlink":
					dirErr = HardlinkFile(downloadPath, dstFile)
				case "copy":
					dirErr = CopyFileCtx(importCtx, downloadPath, dstFile)
				default:
					dirErr = MoveFileCtx(importCtx, downloadPath, dstFile)
				}
			}
		}
		if dirErr != nil {
			slog.Error("failed to import audiobook folder", "src", downloadPath, "mode", mode, "error", dirErr)
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

		var fileErr error
		switch mode {
		case "hardlink":
			fileErr = HardlinkFile(srcFile, destPath)
		case "copy":
			fileErr = CopyFileCtx(importCtx, srcFile, destPath)
		default:
			fileErr = MoveFileCtx(importCtx, srcFile, destPath)
		}
		if fileErr != nil {
			slog.Error("failed to import", "src", srcFile, "mode", mode, "error", fileErr)
			failed++
			lastFileErr = fileErr
			continue
		}
		imported++
		importedSrcFiles = append(importedSrcFiles, srcFile)

		// Record each imported file individually in book_files so multi-file
		// downloads (epub + mobi + pdf) are all tracked rather than overwriting.
		if err := s.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, destPath); err != nil {
			slog.Error("failed to record book file", "bookID", book.ID, "error", err)
		}
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

	var unmatchedFiles []unmatchedFile
	var reconciled, unmatched, tagReadFailed int
	for _, path := range foundFiles {
		// Skip files already tracked, or files inside a tracked directory
		// (individual tracks inside an already-imported audiobook folder).
		cleanPath := filepath.Clean(path)
		if trackedPaths[cleanPath] || trackedPaths[filepath.Clean(filepath.Dir(cleanPath))] {
			continue
		}

		// Parse the filename for title/author hints. If the filename alone
		// doesn't yield an author (e.g. the file is named just "book.epub"),
		// fall back to the first directory component relative to whichever
		// library root contains the file — most layouts are
		// {Author}/{Title}/filename.ext.
		parsed := ParseFilename(path)

		// Prefer embedded audio tags over filename parsing for audiobook
		// files. Well-tagged M4B/MP3 releases carry the author, title and
		// often an ASIN in their ID3/iTunes atoms; using them avoids the
		// fuzzy-match noise seen on users' organised libraries (#303).
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

		if parsed.Author == "" {
			for _, root := range []string{s.libraryDir, s.audiobookDir} {
				if root == "" {
					continue
				}
				if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
					parts := strings.SplitN(rel, string(filepath.Separator), 2)
					if len(parts) >= 2 {
						parsed.Author = parts[0]
						break
					}
				}
			}
		}

		// Search existing books for a match: ASIN takes priority over fuzzy title+author.
		matched := false
		detectedFmt := detectDownloadFormat([]string{path})
		if parsed.ASIN != "" {
			for _, b := range allBooks {
				if reconciledBooks[b.ID] {
					continue
				}
				if b.Status != models.BookStatusWanted || b.ASIN != parsed.ASIN {
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
			for _, b := range allBooks {
				if reconciledBooks[b.ID] {
					continue
				}
				// Require Jaro-Winkler >= 0.85 on normalised titles to prevent
				// low-confidence matches from reconciling the wrong book after a
				// delete+rescan cycle (#343). normalizeTitle strips leading articles
				// and inverts comma-suffix sort form ("Title, A" → "title") so that
				// librarian-sorted folders reconcile correctly (#513).
				jwScore := textutil.JaroWinkler(normalizeTitle(b.Title), normalizeTitle(parsed.Title))
				if b.Status != models.BookStatusWanted ||
					jwScore < 0.85 ||
					!authorMatch(authorNames[b.AuthorID], parsed.Author) {
					continue
				}
				// File must live under the candidate book's effective library root
				// to prevent cross-author mismapping after delete+rescan (#343).
				effDir := s.effectiveLibraryDir(ctx, authorMap[b.AuthorID])
				if !pathUnderDir(path, effDir) {
					slog.Debug("library scan: title+author match rejected (outside library root)",
						"title", b.Title, "path", path, "root", effDir)
					continue
				}
				if err := s.books.AddBookFile(ctx, b.ID, detectedFmt, path); err != nil {
					slog.Error("library scan: failed to update book", "id", b.ID, "error", err)
					continue
				}
				slog.Info("library scan: reconciled book", "title", b.Title, "path", path, "jw", jwScore)
				trackedPaths[cleanPath] = true
				reconciledBooks[b.ID] = true
				reconciled++
				matched = true
				break
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

	slog.Info("library scan complete", "path", s.libraryDir, "bookFiles", len(foundFiles),
		"reconciled", reconciled, "unmatched", unmatched, "tagReadFailed", tagReadFailed)

	s.writeScanResult(ctx, len(foundFiles), reconciled, unmatched, tagReadFailed, unmatchedFiles)
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
