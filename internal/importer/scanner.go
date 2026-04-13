// Package importer moves completed downloads into the configured library
// directory using a configurable naming template, and reconciles pre-existing
// library files against the tracked book database.
package importer

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/models"
)

// Scanner checks for completed downloads and imports them into the library.
type Scanner struct {
	downloads    *db.DownloadRepo
	clients      *db.DownloadClientRepo
	books        *db.BookRepo
	authors      *db.AuthorRepo
	history      *db.HistoryRepo
	renamer      *Renamer
	remapper     *Remapper
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

	// Audiobook path: move the entire download directory as a unit so
	// multi-part m4b/mp3 files, cover art, and cue sheets stay together.
	if book != nil && book.MediaType == models.MediaTypeAudiobook {
		destDir := UniqueDir(s.renamer.AudiobookDestDir(s.audiobookDir, author, book))
		slog.Info("importing audiobook folder", "src", downloadPath, "dst", destDir)
		if err := MoveDir(downloadPath, destDir); err != nil {
			slog.Error("failed to import audiobook folder", "src", downloadPath, "error", err)
			return
		}
		s.books.SetFilePath(ctx, book.ID, destDir)
		s.downloads.UpdateStatus(ctx, dl.ID, models.DownloadStatusImported)
		slog.Info("audiobook imported", "title", book.Title, "path", destDir)

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

		destPath := s.renamer.DestPath(s.libraryDir, author, book, srcFile)
		slog.Info("importing book", "src", srcFile, "dst", destPath)

		if err := MoveFile(srcFile, destPath); err != nil {
			slog.Error("failed to import", "src", srcFile, "error", err)
			failed++
			continue
		}
		imported++

		// Update book status and file path
		s.books.SetFilePath(ctx, book.ID, destPath)
		s.downloads.UpdateStatus(ctx, dl.ID, models.DownloadStatusImported)
		slog.Info("book imported", "title", book.Title, "path", destPath)

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

// titleMatch returns true when bookTitle and parsedTitle share enough significant words
// to be considered the same work. It requires at least 2 overlapping words of 3+ chars,
// or all words if the parsed title has fewer than 2 such words. Both titles must
// contribute at least one word to avoid single-character false positives.
func titleMatch(bookTitle, parsedTitle string) bool {
	if parsedTitle == "" {
		return false
	}
	sigWords := func(s string) []string {
		var out []string
		for _, w := range strings.Fields(strings.ToLower(s)) {
			// Strip non-alpha chars (punctuation, hyphens)
			w = strings.Map(func(r rune) rune {
				if r >= 'a' && r <= 'z' {
					return r
				}
				return -1
			}, w)
			if len(w) >= 3 {
				out = append(out, w)
			}
		}
		return out
	}

	btWords := sigWords(bookTitle)
	ptWords := sigWords(parsedTitle)

	if len(btWords) == 0 || len(ptWords) == 0 {
		return false
	}

	// Build lookup set for book title words
	btSet := make(map[string]bool, len(btWords))
	for _, w := range btWords {
		btSet[w] = true
	}

	overlap := 0
	for _, w := range ptWords {
		if btSet[w] {
			overlap++
		}
	}

	// Require at least 2 matching words, or all words if parsed title has < 2
	minOverlap := 2
	if len(ptWords) < 2 {
		minOverlap = len(ptWords)
	}
	return overlap >= minOverlap
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
		return
	}

	// Load all books from the DB for reconciliation
	allBooks, err := s.books.List(ctx)
	if err != nil {
		slog.Error("library scan: failed to list books", "error", err)
		return
	}

	// Build a set of file paths already tracked in the DB
	trackedPaths := make(map[string]bool, len(allBooks))
	for _, b := range allBooks {
		if b.FilePath != "" {
			trackedPaths[b.FilePath] = true
		}
	}

	var reconciled, unmatched int
	for _, path := range foundFiles {
		// Skip files already tracked
		if trackedPaths[path] {
			continue
		}

		// Parse the filename to extract title/author hints
		parsed := ParseFilename(path)

		// Search existing books for a title match using word overlap
		matched := false
		if parsed.Title != "" {
			for _, b := range allBooks {
				if b.Status == models.BookStatusWanted && titleMatch(b.Title, parsed.Title) {
					// Match found — update file path and status
					if err := s.books.SetFilePath(ctx, b.ID, path); err != nil {
						slog.Error("library scan: failed to update book", "id", b.ID, "error", err)
						continue
					}
					slog.Info("library scan: reconciled book", "title", b.Title, "path", path)
					trackedPaths[path] = true
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
}
