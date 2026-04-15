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
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
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
				s.downloads.UpdateStatus(ctx, dl.ID, models.DownloadStatusCompleted)
				s.tryImport(ctx, sab, dl, slot.NzoID, localPath)
			}
		case "Failed":
			if dl.Status != models.DownloadStatusFailed {
				slog.Warn("download failed", "title", dl.Title, "message", slot.FailMessage)
				s.downloads.SetError(ctx, dl.ID, slot.FailMessage)
				eventData, _ := json.Marshal(map[string]string{"guid": dl.GUID, "message": slot.FailMessage})
				s.history.Create(ctx, &models.HistoryEvent{
					BookID:      dl.BookID,
					EventType:   models.HistoryEventDownloadFailed,
					SourceTitle: dl.Title,
					Data:        string(eventData),
				})
			}
		}
	}
}

// tryImport attempts to import a completed download into the library.
// sab is used to clear the SABnzbd history entry once bindery has taken
// ownership of the files; nzoID is the history slot's NZO identifier.
func (s *Scanner) tryImport(ctx context.Context, sab *sabnzbd.Client, dl *models.Download, nzoID, downloadPath string) {
	if s.libraryDir == "" {
		slog.Warn("no library directory configured, skipping import")
		return
	}

	// Find book files in the download path
	var bookFiles []string
	filepath.Walk(downloadPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if IsBookFile(path) {
			bookFiles = append(bookFiles, path)
		}
		return nil
	})

	if len(bookFiles) == 0 {
		slog.Warn("no book files found in download", "path", downloadPath)
		return
	}

	// Resolve the book and author for naming. Lookup errors are not fatal —
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

	// Audiobook path: move the entire download directory as a unit so
	// multi-part m4b/mp3 files, cover art, and cue sheets stay together.
	if detectedFormat == models.MediaTypeAudiobook {
		audiobookRoot := s.audiobookDir
		if effLib := s.effectiveLibraryDir(ctx, author); effLib != s.libraryDir {
			audiobookRoot = effLib
		}
		destDir := UniqueDir(s.renamer.AudiobookDestDir(audiobookRoot, author, book))
		slog.Info("importing audiobook folder", "src", downloadPath, "dst", destDir)
		if err := MoveDir(downloadPath, destDir); err != nil {
			slog.Error("failed to import audiobook folder", "src", downloadPath, "error", err)
			return
		}
		if book != nil {
			if err := s.books.SetFormatFilePath(ctx, book.ID, models.MediaTypeAudiobook, destDir); err != nil {
				slog.Error("failed to update audiobook file path", "bookID", book.ID, "error", err)
			}
		}
		s.downloads.UpdateStatus(ctx, dl.ID, models.DownloadStatusImported)
		slog.Info("audiobook imported", "title", func() string {
			if book != nil {
				return book.Title
			}
			return dl.Title
		}(), "path", destDir)

		s.pushToCalibre(ctx, book, author, destDir)

		eventData, _ := json.Marshal(map[string]string{"path": destDir})
		s.history.Create(ctx, &models.HistoryEvent{
			BookID:      dl.BookID,
			EventType:   models.HistoryEventBookImported,
			SourceTitle: dl.Title,
			Data:        string(eventData),
		})
		s.clearSABHistory(ctx, sab, nzoID)
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
		slog.Info("importing book", "src", srcFile, "dst", destPath)

		if err := MoveFile(srcFile, destPath); err != nil {
			slog.Error("failed to import", "src", srcFile, "error", err)
			failed++
			continue
		}
		imported++

		// Update book status and file path
		if err := s.books.SetFormatFilePath(ctx, book.ID, models.MediaTypeEbook, destPath); err != nil {
			slog.Error("failed to update ebook file path", "bookID", book.ID, "error", err)
		}
		s.downloads.UpdateStatus(ctx, dl.ID, models.DownloadStatusImported)
		slog.Info("book imported", "title", book.Title, "path", destPath)

		s.pushToCalibre(ctx, book, author, destPath)

		eventData, _ := json.Marshal(map[string]string{"path": destPath})
		s.history.Create(ctx, &models.HistoryEvent{
			BookID:      dl.BookID,
			EventType:   models.HistoryEventBookImported,
			SourceTitle: dl.Title,
			Data:        string(eventData),
		})
	}

	// A clean run leaves the SABnzbd job folder holding only non-book
	// byproducts (par2, nfo, sfv, sample) — bindery has no further use
	// for them, so drop the folder and the matching history entry so
	// the completed-downloads view doesn't accumulate stale rows.
	if imported > 0 && failed == 0 {
		if err := os.RemoveAll(downloadPath); err != nil {
			slog.Warn("failed to remove download folder after import", "path", downloadPath, "error", err)
		}
		s.clearSABHistory(ctx, sab, nzoID)
	}
}

// clearSABHistory tells SABnzbd to drop the history entry for a job bindery
// has finished importing. deleteFiles=false because the importer has already
// moved the contents; asking SAB to delete would either no-op or wipe files
// that were moved cross-filesystem and are still resolving.
func (s *Scanner) clearSABHistory(ctx context.Context, sab *sabnzbd.Client, nzoID string) {
	if sab == nil || nzoID == "" {
		return
	}
	if err := sab.DeleteHistory(ctx, nzoID, false); err != nil {
		slog.Warn("failed to delete SABnzbd history entry", "nzoID", nzoID, "error", err)
	}
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
	filepath.Walk(s.libraryDir, func(path string, info os.FileInfo, err error) error {
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
	})
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

// ScanLibrary walks the library directory for book files not yet tracked in the database
// and reconciles found files with existing "wanted" book records.
func (s *Scanner) ScanLibrary(ctx context.Context) {
	if s.libraryDir == "" {
		return
	}

	// Collect all book files on disk
	var foundFiles []string
	filepath.Walk(s.libraryDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if IsBookFile(path) {
			foundFiles = append(foundFiles, path)
		}
		return nil
	})

	slog.Info("library scan found files", "path", s.libraryDir, "count", len(foundFiles))

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
		// fall back to the first directory component relative to libraryDir —
		// most library layouts are {Author}/{Title}/filename.ext.
		parsed := ParseFilename(path)
		if parsed.Author == "" {
			if rel, err := filepath.Rel(s.libraryDir, path); err == nil {
				parts := strings.SplitN(rel, string(filepath.Separator), 2)
				if len(parts) >= 2 {
					parsed.Author = parts[0]
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
