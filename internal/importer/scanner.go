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
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/downloader/transmission"
	"github.com/vavallee/bindery/internal/models"
)

// calibreAdder is the calibredb half of the Calibre integration: it mirrors
// a just-imported file into Calibre by shelling out to `calibredb add`.
// The scanner holds a reference to one of these but only invokes it when
// the mode resolver says we're in ModeCalibredb.
type calibreAdder interface {
	Add(ctx context.Context, filePath string) (int64, error)
}

// calibreDropFolderWriter is the drop-folder half of the integration:
// writes the file to Calibre's watched directory and polls metadata.db for
// the resulting book id. Kept as an interface so tests can stub it without
// touching the filesystem.
type calibreDropFolderWriter interface {
	Ingest(ctx context.Context, srcPath, title, author string) (calibre.IngestResult, error)
}

// Scanner checks for completed downloads and imports them into the library.
type Scanner struct {
	downloads    *db.DownloadRepo
	clients      *db.DownloadClientRepo
	books        *db.BookRepo
	authors      *db.AuthorRepo
	history      *db.HistoryRepo
	rootFolders  *db.RootFolderRepo
	renamer      *Renamer
	remapper     *Remapper
	calibreAdder calibreAdder
	calibreDrop  calibreDropFolderWriter
	calibreMode  func() calibre.Mode
	settings     *db.SettingsRepo
	libraryDir   string
	audiobookDir string
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

// WithRootFolders attaches the root folder repo so the scanner can resolve
// per-author library directories from their rootFolderId.
func (s *Scanner) WithRootFolders(rf *db.RootFolderRepo) *Scanner {
	s.rootFolders = rf
	return s
}

// effectiveLibraryDir returns the library root to use for the given author.
// If the author has a rootFolderId and the repo is configured, that folder's
// path is returned. Otherwise the global libraryDir is used.
func (s *Scanner) effectiveLibraryDir(ctx context.Context, author *models.Author) string {
	if author != nil && author.RootFolderID != nil && s.rootFolders != nil {
		if rf, err := s.rootFolders.GetByID(ctx, *author.RootFolderID); err == nil && rf != nil {
			return rf.Path
		}
	}
	return s.libraryDir
}

// WithCalibre attaches the Calibre integration pieces. The mode resolver
// is consulted on every import so the operator can switch modes (or turn
// the integration off) in the UI without restarting Bindery. Passing nil
// for any argument disables that branch — a nil resolver skips all Calibre
// calls, a nil adder skips only the calibredb path, a nil drop writer
// skips only the drop-folder path.
func (s *Scanner) WithCalibre(mode func() calibre.Mode, adder calibreAdder, drop calibreDropFolderWriter) *Scanner {
	s.calibreMode = mode
	s.calibreAdder = adder
	s.calibreDrop = drop
	return s
}

// WithSettings attaches a SettingsRepo to the scanner so scan results can be
// persisted under the "library.lastScan" key and surfaced via the API.
func (s *Scanner) WithSettings(sr *db.SettingsRepo) *Scanner {
	s.settings = sr
	return s
}

// importMode reads the "import.mode" setting and returns one of "move",
// "copy", or "hardlink". Defaults to "move" when the setting is absent or
// unrecognised so upgrades are transparent for existing installs.
func (s *Scanner) importMode(ctx context.Context) string {
	if s.settings == nil {
		return "move"
	}
	setting, err := s.settings.Get(ctx, "import.mode")
	if err != nil || setting == nil {
		return "move"
	}
	switch setting.Value {
	case "copy", "hardlink":
		return setting.Value
	default:
		return "move"
	}
}

// pushToCalibre dispatches the just-imported book to the Calibre integration
// selected by the current mode setting. Failures are logged and swallowed —
// Calibre sync is a best-effort mirror, so a missing binary, unreachable
// library, or Calibre-didn't-pick-it-up-in-time must never roll back an
// otherwise-good Bindery import. A successful call stores the returned
// Calibre id on the book row.
//
// author is required for the drop-folder path (it drives both the filename
// layout and the metadata.db lookup); the calibredb path tolerates a nil
// author because calibredb reads metadata from the file itself.
func (s *Scanner) pushToCalibre(ctx context.Context, book *models.Book, author *models.Author, path string) {
	if s.calibreMode == nil || book == nil {
		return
	}
	mode := s.calibreMode()
	switch mode {
	case calibre.ModeCalibredb:
		s.pushCalibredb(ctx, book, path)
	case calibre.ModeDropFolder:
		s.pushCalibreDropFolder(ctx, book, author, path)
	default:
		// ModeOff or any unknown value: no-op.
	}
}

// pushCalibredb is the Path A flow — shell out to calibredb add. Kept as a
// small helper so the mode-dispatch in pushToCalibre reads linearly.
func (s *Scanner) pushCalibredb(ctx context.Context, book *models.Book, path string) {
	if s.calibreAdder == nil {
		slog.Debug("calibre: mode=calibredb but adder is nil, skipping", "bookId", book.ID)
		return
	}
	id, err := s.calibreAdder.Add(ctx, path)
	if err != nil {
		if errors.Is(err, calibre.ErrDisabled) {
			return
		}
		slog.Warn("calibre: add failed, continuing", "bookId", book.ID, "path", path, "error", err)
		return
	}
	if err := s.books.SetCalibreID(ctx, book.ID, id); err != nil {
		slog.Warn("calibre: persist calibre_id failed", "bookId", book.ID, "calibreId", id, "error", err)
		return
	}
	slog.Info("calibre: book mirrored", "mode", "calibredb", "bookId", book.ID, "calibreId", id, "path", path)
}

// pushCalibreDropFolder is the Path B flow — copy the file into Calibre's
// watched directory and poll metadata.db for the resulting book id. A poll
// timeout logs a warning but is not surfaced as a failure, matching the
// rest of the Calibre integration's best-effort posture.
func (s *Scanner) pushCalibreDropFolder(ctx context.Context, book *models.Book, author *models.Author, path string) {
	if s.calibreDrop == nil {
		slog.Debug("calibre: mode=drop_folder but writer is nil, skipping", "bookId", book.ID)
		return
	}
	authorName := "Unknown Author"
	if author != nil && author.Name != "" {
		authorName = author.Name
	}
	// Calibre keys off the file's embedded metadata, not its filename, but
	// we still use the book's title for the lookup on the way back. Use
	// the Bindery title so a ParseFilename-derived one never leaks in.
	res, err := s.calibreDrop.Ingest(ctx, path, book.Title, authorName)
	if err != nil {
		if errors.Is(err, calibre.ErrDropFolderNotConfigured) {
			return
		}
		slog.Warn("calibre: drop-folder ingest failed, continuing",
			"bookId", book.ID, "path", path, "error", err)
		return
	}
	if !res.Found {
		slog.Warn("calibre: drop-folder — Calibre did not ingest within poll budget",
			"bookId", book.ID, "dropped", res.DroppedPath, "title", book.Title, "author", authorName)
		return
	}
	if err := s.books.SetCalibreID(ctx, book.ID, res.CalibreID); err != nil {
		slog.Warn("calibre: persist calibre_id failed",
			"bookId", book.ID, "calibreId", res.CalibreID, "error", err)
		return
	}
	slog.Info("calibre: book mirrored",
		"mode", "drop_folder", "bookId", book.ID, "calibreId", res.CalibreID, "dropped", res.DroppedPath)
}

// CheckDownloads polls SABnzbd for status changes and updates the local download records.
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
	default:
		s.checkSABnzbdDownloads(ctx, client)
	}
}

func isTrackedTorrentDownloadForClient(dl models.Download, clientID int64) bool {
	if dl.DownloadClientID == nil || *dl.DownloadClientID != clientID {
		return false
	}
	if dl.TorrentID == nil {
		return false
	}
	return dl.Status != models.DownloadStatusImported && dl.Status != models.DownloadStatusFailed
}

func (s *Scanner) setDownloadError(ctx context.Context, id int64, message string) {
	if err := s.downloads.SetError(ctx, id, message); err != nil {
		slog.Warn("failed to persist download error", "download_id", id, "error", err)
	}
}

func (s *Scanner) updateDownloadStatus(ctx context.Context, id int64, status string) {
	if err := s.downloads.UpdateStatus(ctx, id, status); err != nil {
		slog.Warn("failed to update download status", "download_id", id, "status", status, "error", err)
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
}

// checkSABnzbdDownloads polls SABnzbd for status changes.
func (s *Scanner) checkSABnzbdDownloads(ctx context.Context, client *models.DownloadClient) {
	sab := sabnzbd.New(client.Host, client.Port, client.APIKey, client.UseSSL)

	// Check history for completed downloads (no category filter — match by NZO ID)
	history, err := sab.GetHistory(ctx, "", 50)
	if err != nil {
		slog.Debug("failed to fetch SABnzbd history", "error", err)
		return
	}

	for _, slot := range history.Slots {
		dl, err := s.downloads.GetByNzoID(ctx, slot.NzoID)
		if err != nil || dl == nil {
			continue
		}

		switch slot.Status {
		case "Completed":
			if dl.Status == models.DownloadStatusDownloading || dl.Status == models.DownloadStatusQueued {
				localPath := s.remapper.Apply(slot.Path)
				if localPath != slot.Path {
					slog.Debug("remapped download path", "sab", slot.Path, "local", localPath)
				}
				slog.Info("download completed", "title", dl.Title, "path", localPath)
				s.updateDownloadStatus(ctx, dl.ID, models.DownloadStatusCompleted)
				s.tryImportSABnzbd(ctx, sab, dl, slot.NzoID, localPath)
			}
		case "Failed":
			if dl.Status != models.DownloadStatusFailed {
				slog.Warn("download failed", "title", dl.Title, "message", slot.FailMessage)
				s.setDownloadError(ctx, dl.ID, slot.FailMessage)
				s.createHistoryEvent(ctx, models.HistoryEventDownloadFailed, dl.Title, dl.BookID, map[string]string{"guid": dl.GUID, "message": slot.FailMessage})
			}
		}
	}
}

// checkTransmissionDownloads polls Transmission for status changes.
func (s *Scanner) checkTransmissionDownloads(ctx context.Context, client *models.DownloadClient) {
	trans := transmission.New(client.Host, client.Port, client.Username, client.Password, client.UseSSL)

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

	for _, dl := range allDownloads {
		if !isTrackedTorrentDownloadForClient(dl, client.ID) {
			continue
		}

		torrent, ok := torrentsMap[*dl.TorrentID]
		if !ok {
			continue
		}

		// Status codes: 0=stopped, 1=checking, 2=downloading, 3=seeding, 4=allocating, 5=checking, 6=stopped
		isComplete := torrent.Status == 3 || (torrent.PercentDone >= 1.0)
		isStopped := torrent.Status == 0 || torrent.Status == 6
		stopError := strings.TrimSpace(torrent.ErrorString)

		if isComplete && (dl.Status == models.DownloadStatusDownloading || dl.Status == models.DownloadStatusQueued) {
			// Download is complete
			slog.Info("download completed", "title", dl.Title, "path", torrent.DownloadDir)
			s.updateDownloadStatus(ctx, dl.ID, models.DownloadStatusCompleted)
			s.tryImportTransmission(ctx, &dl, torrent.DownloadDir)
		} else if isStopped && !isComplete && dl.Status != models.DownloadStatusFailed {
			if stopError == "" {
				// Transmission also reports user-paused torrents as stopped.
				continue
			}
			slog.Warn("download failed", "title", dl.Title, "error", stopError)
			s.markDownloadFailed(ctx, &dl, stopError)
		}
	}
}

// checkQbittorrentDownloads polls qBittorrent for status changes.
func (s *Scanner) checkQbittorrentDownloads(ctx context.Context, client *models.DownloadClient) {
	qb := qbittorrent.New(client.Host, client.Port, client.Username, client.Password, client.UseSSL)

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

	for _, dl := range allDownloads {
		if !isTrackedTorrentDownloadForClient(dl, client.ID) {
			continue
		}

		torrent, ok := torrentsMap[strings.ToLower(*dl.TorrentID)]
		if !ok {
			continue
		}

		state := strings.ToLower(torrent.State)
		isComplete := torrent.Progress >= 1.0 || strings.Contains(state, "upload") || strings.Contains(state, "stalledup") || strings.Contains(state, "checkingup")
		isFailed := strings.Contains(state, "error")

		if isComplete && (dl.Status == models.DownloadStatusDownloading || dl.Status == models.DownloadStatusQueued) {
			downloadPath := torrent.SavePath
			candidate := filepath.Join(torrent.SavePath, torrent.Name)
			if _, err := os.Stat(candidate); err == nil {
				downloadPath = candidate
			}
			downloadPath = s.remapper.Apply(downloadPath)

			slog.Info("download completed", "title", dl.Title, "path", downloadPath)
			s.updateDownloadStatus(ctx, dl.ID, models.DownloadStatusCompleted)
			s.tryImportQbittorrent(ctx, &dl, downloadPath)
		} else if isFailed && dl.Status != models.DownloadStatusFailed {
			slog.Warn("download failed", "title", dl.Title, "state", torrent.State)
			s.markDownloadFailed(ctx, &dl, "Torrent failed in qBittorrent")
		}
	}
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

// tryImportInternal is the common import logic shared by SABnzbd and Transmission.
func (s *Scanner) tryImportInternal(ctx context.Context, dl *models.Download, downloadPath, cleanupClientType, cleanupRemoteID string, cleanupFunc func() error) {
	if s.libraryDir == "" {
		slog.Warn("no library directory configured, skipping import")
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
		return
	}

	// Resolve the book and author for naming. Lookup errors are not fatal -
	// we fall through to the "unmatched import" log below.
	var book *models.Book
	var author *models.Author
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
		audiobookRoot := s.audiobookDir
		if effLib := s.effectiveLibraryDir(ctx, author); effLib != s.libraryDir {
			audiobookRoot = effLib
		}
		destDir := UniqueDir(s.renamer.AudiobookDestDir(audiobookRoot, author, book))
		mode := s.importMode(ctx)
		slog.Info("importing audiobook folder", "src", downloadPath, "dst", destDir, "mode", mode)
		var dirErr error
		switch mode {
		case "hardlink":
			dirErr = HardlinkDir(downloadPath, destDir)
		case "copy":
			dirErr = CopyDir(downloadPath, destDir)
		default:
			dirErr = MoveDir(downloadPath, destDir)
		}
		if dirErr != nil {
			slog.Error("failed to import audiobook folder", "src", downloadPath, "mode", mode, "error", dirErr)
			return
		}
		if book != nil {
			if err := s.books.SetFormatFilePath(ctx, book.ID, models.MediaTypeAudiobook, destDir); err != nil {
				slog.Error("failed to update audiobook file path", "bookID", book.ID, "error", err)
			}
		}
		s.updateDownloadStatus(ctx, dl.ID, models.DownloadStatusImported)
		slog.Info("audiobook imported", "title", func() string {
			if book != nil {
				return book.Title
			}
			return dl.Title
		}(), "path", destDir)

		s.pushToCalibre(ctx, book, author, destDir)

		s.createHistoryEvent(ctx, models.HistoryEventBookImported, dl.Title, dl.BookID, map[string]string{"path": destDir})
		if cleanupFunc != nil {
			if err := cleanupFunc(); err != nil {
				slog.Warn("cleanup failed", cleanupWarnAttrs(cleanupClientType, cleanupRemoteID, err)...)
			}
		}
		return
	}

	var imported, failed int
	for _, srcFile := range bookFiles {
		if book == nil {
			// Try to match from filename
			parsed := ParseFilename(srcFile)
			slog.Info("unmatched import", "title", parsed.Title, "author", parsed.Author, "file", srcFile)
			continue
		}

		destPath := s.renamer.DestPath(s.effectiveLibraryDir(ctx, author), author, book, srcFile)
		mode := s.importMode(ctx)
		slog.Info("importing book", "src", srcFile, "dst", destPath, "mode", mode)

		var fileErr error
		switch mode {
		case "hardlink":
			fileErr = HardlinkFile(srcFile, destPath)
		case "copy":
			fileErr = CopyFile(srcFile, destPath)
		default:
			fileErr = MoveFile(srcFile, destPath)
		}
		if fileErr != nil {
			slog.Error("failed to import", "src", srcFile, "mode", mode, "error", fileErr)
			failed++
			continue
		}
		imported++

		// Update book status and file path
		if err := s.books.SetFormatFilePath(ctx, book.ID, models.MediaTypeEbook, destPath); err != nil {
			slog.Error("failed to update ebook file path", "bookID", book.ID, "error", err)
		}
		s.updateDownloadStatus(ctx, dl.ID, models.DownloadStatusImported)
		slog.Info("book imported", "title", book.Title, "path", destPath)

		s.pushToCalibre(ctx, book, author, destPath)

		s.createHistoryEvent(ctx, models.HistoryEventBookImported, dl.Title, dl.BookID, map[string]string{"path": destPath})
	}

	// A clean run leaves the download folder holding only non-book byproducts
	// (par2, nfo, sfv, sample). For "move" mode bindery has no further use for
	// them so the folder is removed. For "copy"/"hardlink" modes the source must
	// be preserved so the torrent client can continue seeding.
	if imported > 0 && failed == 0 {
		if s.importMode(ctx) == "move" {
			if err := os.RemoveAll(downloadPath); err != nil {
				slog.Warn("failed to remove download folder after import", "path", downloadPath, "error", err)
			}
		}
		if cleanupFunc != nil {
			if err := cleanupFunc(); err != nil {
				slog.Warn("cleanup failed", cleanupWarnAttrs(cleanupClientType, cleanupRemoteID, err)...)
			}
		}
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

// FindExisting searches the library directory for a book file that matches
// the given title and author. Returns the first matching file path, or ""
// if none is found. Intended to be called before auto-searching so books
// the user already owns are not re-downloaded.
func (s *Scanner) FindExisting(ctx context.Context, title, authorName string) string {
	if s.libraryDir == "" || title == "" {
		return ""
	}
	var found string
	if err := filepath.Walk(s.libraryDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found != "" {
			return nil
		}
		if !IsBookFile(path) {
			return nil
		}
		parsed := ParseFilename(path)
		if titleMatch(parsed.Title, title) && authorMatch(authorName, parsed.Author) {
			found = path
		}
		return nil
	}); err != nil {
		slog.Warn("failed to search library for existing file", "path", s.libraryDir, "error", err)
	}
	return found
}

// titleMatch returns true when bookTitle and parsedTitle refer to the same work.
// It handles numeric titles (1984, 2001), article normalization ("Title, The"),
// and uses dynamic overlap thresholds so short titles still match correctly.
func titleMatch(bookTitle, parsedTitle string) bool {
	if parsedTitle == "" || bookTitle == "" {
		return false
	}

	// norm lowercases, handles "Title, The" inversion, and strips leading articles.
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		// Normalize "Title, The" → "the title" (comma-article inversion)
		if idx := strings.LastIndex(s, ", the"); idx != -1 && idx == len(s)-5 {
			s = "the " + s[:idx]
		}
		// Strip leading article for comparison
		for _, art := range []string{"the ", "a ", "an "} {
			if strings.HasPrefix(s, art) {
				s = s[len(art):]
				break
			}
		}
		return s
	}

	// Fast path: exact match after normalization
	if norm(bookTitle) == norm(parsedTitle) {
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

// authorMatch returns true when parsedAuthor is consistent with bookAuthor.
// If parsedAuthor is empty the function returns true (can't disprove).
// Otherwise it checks that the last name of parsedAuthor appears in bookAuthor.
func authorMatch(bookAuthor, parsedAuthor string) bool {
	if parsedAuthor == "" {
		return true // no author info in filename — don't filter
	}
	parts := strings.Fields(strings.ToLower(parsedAuthor))
	if len(parts) == 0 {
		return true
	}
	lastName := parts[len(parts)-1]
	return len(lastName) >= 3 && strings.Contains(strings.ToLower(bookAuthor), lastName)
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
		s.writeScanResult(ctx, len(foundFiles), 0, 0)
		return
	}

	// Load all books from the DB for reconciliation
	allBooks, err := s.books.List(ctx)
	if err != nil {
		slog.Error("library scan: failed to list books", "error", err)
		return
	}

	// Build a set of tracked file paths AND their parent directories.
	// The parent-directory entry is needed for audiobooks: their file_path
	// is stored as a directory (the whole folder is the "file"), but the
	// walk yields individual tracks. Without it every audio track inside an
	// already-imported audiobook folder would look untracked.
	trackedPaths := make(map[string]bool, len(allBooks)*2)
	for _, b := range allBooks {
		if b.FilePath != "" {
			trackedPaths[filepath.Clean(b.FilePath)] = true
			trackedPaths[filepath.Clean(filepath.Dir(b.FilePath))] = true
		}
	}

	// Build an author name cache (authorID → name) for the author-anchor check.
	authorNames := make(map[int64]string)
	if allAuthors, err := s.authors.List(ctx); err == nil {
		for _, a := range allAuthors {
			authorNames[a.ID] = a.Name
		}
	}

	// reconciledBooks prevents a single DB book from being matched to more
	// than one file in the same scan pass. allBooks is loaded once and is
	// never mutated in-memory, so without this guard a loose titleMatch could
	// assign the same book to multiple files — last write wins, overwriting
	// the correct earlier assignment.
	reconciledBooks := make(map[int64]bool)

	var reconciled, unmatched int
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

		// Search existing books for a title + author match
		matched := false
		if parsed.Title != "" {
			detectedFmt := detectDownloadFormat([]string{path})
			for _, b := range allBooks {
				if reconciledBooks[b.ID] {
					continue
				}
				if b.Status == models.BookStatusWanted &&
					titleMatch(b.Title, parsed.Title) &&
					authorMatch(authorNames[b.AuthorID], parsed.Author) {
					// Match found — update the per-format file path and aggregate status.
					if err := s.books.SetFormatFilePath(ctx, b.ID, detectedFmt, path); err != nil {
						slog.Error("library scan: failed to update book", "id", b.ID, "error", err)
						continue
					}
					slog.Info("library scan: reconciled book", "title", b.Title, "path", path)
					trackedPaths[cleanPath] = true
					reconciledBooks[b.ID] = true
					reconciled++
					matched = true
					break
				}
			}
		}

		if !matched {
			slog.Debug("library scan: unmatched file", "path", path, "parsedTitle", parsed.Title, "parsedAuthor", parsed.Author)
			unmatched++
		}
	}

	slog.Info("library scan complete", "path", s.libraryDir, "bookFiles", len(foundFiles),
		"reconciled", reconciled, "unmatched", unmatched)

	s.writeScanResult(ctx, len(foundFiles), reconciled, unmatched)
}

// writeScanResult persists the scan summary to the settings table under
// "library.lastScan" so the UI can surface the result without polling logs.
func (s *Scanner) writeScanResult(ctx context.Context, filesFound, reconciled, unmatched int) {
	if s.settings == nil {
		return
	}
	payload := fmt.Sprintf(
		`{"ran_at":%q,"files_found":%d,"reconciled":%d,"unmatched":%d}`,
		time.Now().UTC().Format(time.RFC3339),
		filesFound, reconciled, unmatched,
	)
	if err := s.settings.Set(ctx, "library.lastScan", payload); err != nil {
		slog.Warn("library scan: failed to persist scan result", "error", err)
	}
}
