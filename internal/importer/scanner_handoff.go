package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/models"
)

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

// configuredImportMode returns the operator-set "import.mode" ("move", "copy",
// "hardlink", or "external"), or "" when the setting is absent/unrecognised,
// meaning "auto" (let resolveImportMode pick hardlink-vs-copy per destination).
// Read once per download so a mid-run UI toggle can't mix modes (#705).
func (s *Scanner) configuredImportMode(ctx context.Context) string {
	if s.settings != nil {
		setting, err := s.settings.Get(ctx, "import.mode")
		if err == nil && setting != nil {
			switch setting.Value {
			case "move", "copy", "hardlink", "external":
				return setting.Value
			}
		}
	}
	return ""
}

// resolveImportMode picks the effective placement mode for a destination root.
// An explicit operator setting (configuredMode) is honoured as-is. For the auto
// default it returns "hardlink" when src and destRoot are on the same filesystem
// (free, preserves seeding) or "copy" otherwise. destRoot MUST be the root the
// files actually land under — per-author RootFolderID and audiobook roots can
// live on a different mount than s.libraryDir, and choosing the mode against
// s.libraryDir there picked an always-failing cross-device hardlink. Pass empty
// strings for src/destRoot to get the cross-device default ("copy") without a
// stat.
func (s *Scanner) resolveImportMode(configuredMode, src, destRoot string) string {
	if configuredMode != "" {
		return configuredMode
	}
	if sameDevice(src, destRoot) {
		return "hardlink"
	}
	slog.Warn("import.mode not set and src/dst are on different filesystems; defaulting to copy — seeding will be preserved but disk usage doubles")
	return "copy"
}

// importMode reads the "import.mode" setting and returns one of "move", "copy",
// "hardlink", or "external", falling back to the same-filesystem auto default
// for src/dst. Retained for the standalone callers/tests; the import pipeline
// uses configuredImportMode + resolveImportMode so the auto check runs against
// the real destination root.
func (s *Scanner) importMode(ctx context.Context, src, dst string) string {
	return s.resolveImportMode(s.configuredImportMode(ctx), src, dst)
}

// flattenMultiDiscEnabled reads the "import.audiobook.flatten_multi_disc"
// setting (#886). It returns true only when the value is explicitly "true";
// the feature is opt-in and OFF by default so existing audiobook imports keep
// preserving the download's internal layout. The key is read as a string
// literal to avoid an import cycle with the api package; keep it in sync with
// api.SettingImportAudiobookFlattenMultiDisc.
func (s *Scanner) flattenMultiDiscEnabled(ctx context.Context) bool {
	if s.settings == nil {
		return false
	}
	setting, err := s.settings.Get(ctx, "import.audiobook.flatten_multi_disc")
	if err != nil || setting == nil {
		return false
	}
	return setting.Value == "true"
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

// discoverBookFiles returns the book files for a download. An explicit
// per-torrent file list (issue #903) is authoritative; otherwise it walks the
// download path for recognised book files. Shared by the normal import flow
// and the external-mode drop handoff.
func discoverBookFiles(downloadPath string, explicitFiles []string) []string {
	if len(explicitFiles) > 0 {
		return filterSymlinks(explicitFiles)
	}
	var bookFiles []string
	if err := filepath.Walk(downloadPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		// Skip symlinks. A malicious release can ship a book-extension symlink
		// pointing at an arbitrary file (e.g. /config/bindery.db, /etc/passwd);
		// importing it in copy mode would os.Open-follow the link and copy the
		// target's bytes into the library where the user can download them.
		// filepath.Walk Lstats, so a symlink shows ModeSymlink here.
		if info.Mode()&os.ModeSymlink != 0 {
			slog.Warn("skipping symlinked file in download path", "path", path)
			return nil
		}
		if IsBookFile(path) {
			bookFiles = append(bookFiles, path)
		}
		return nil
	}); err != nil {
		slog.Warn("failed to walk download path", "path", downloadPath, "error", err)
	}
	return bookFiles
}

// filterSymlinks drops any path that is a symlink (Lstat, so the link itself is
// inspected, not its target). The download client's authoritative file list
// (#903) can include a symlink a malicious release planted to point at an
// arbitrary file; importing it would copy the target's bytes into the library.
func filterSymlinks(paths []string) []string {
	out := paths[:0:0]
	for _, p := range paths {
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			slog.Warn("skipping symlinked file in download path", "path", p)
			continue
		}
		out = append(out, p)
	}
	return out
}

// dropSettings reads the external-mode drop-folder configuration (#941).
// Defaults: layout "flat", link mode "copy". An empty folder means the feature
// is off. Keep the literal keys in sync with the api.SettingImportDrop*
// constants (the importer can't import the api package — cycle).
func (s *Scanner) dropSettings(ctx context.Context) (folder, layout, linkMode string) {
	layout, linkMode = "flat", "copy"
	if s.settings == nil {
		return "", layout, linkMode
	}
	if v, err := s.settings.Get(ctx, "import.drop_folder"); err == nil && v != nil {
		folder = strings.TrimSpace(v.Value)
	}
	if v, err := s.settings.Get(ctx, "import.drop_layout"); err == nil && v != nil && v.Value == "templated" {
		layout = "templated"
	}
	if v, err := s.settings.Get(ctx, "import.drop_link_mode"); err == nil && v != nil && v.Value == "hardlink" {
		linkMode = "hardlink"
	}
	return folder, layout, linkMode
}

// dropToFolder handles import.mode=external WHEN a drop folder is configured:
// it renames the finished download into that folder (copy/hardlink — never
// move, so the torrent keeps seeding) and parks the download in
// StateImportExternal so ScanLibrary reconciles the managed copy the external
// tool (CWA, Calibre, Storyteller) ultimately lands in the library dir.
//
// Returns true when it took ownership of the download (placement attempted,
// whether it succeeded or failed via failImport), false when no drop folder is
// configured so the caller falls back to plain external mode.
func (s *Scanner) dropToFolder(ctx context.Context, dl *models.Download, downloadPath, formatHint string, explicitFiles []string) bool {
	folder, layout, linkMode := s.dropSettings(ctx)
	if folder == "" {
		return false
	}

	bookFiles := discoverBookFiles(downloadPath, explicitFiles)
	if len(bookFiles) == 0 {
		if _, statErr := os.Stat(downloadPath); os.IsNotExist(statErr) {
			s.failImport(ctx, dl, models.StateImportFailed,
				fmt.Sprintf("download path not found: %q — configure PathRemap on the download client", downloadPath))
			return true
		}
		s.failImport(ctx, dl, models.StateImportFailed, fmt.Sprintf("no book files found in %q", downloadPath))
		return true
	}

	// Resolve book + author for naming (best-effort; nil book is fatal for the
	// drop path because we can't compute a destination name).
	var book *models.Book
	var author *models.Author
	if dl.BookID != nil {
		if b, err := s.books.GetByID(ctx, *dl.BookID); err == nil && b != nil {
			book = b
			if a, err := s.authors.GetByID(ctx, b.AuthorID); err == nil {
				author = a
			}
		}
	}
	if book == nil {
		s.failImport(ctx, dl, models.StateImportFailed, "could not match any book to this download — check the release title")
		return true
	}

	detectedFormat := detectDownloadFormat(bookFiles)
	if formatHint == models.MediaTypeAudiobook || formatHint == models.MediaTypeEbook {
		detectedFormat = formatHint
	}

	importCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)

	if detectedFormat == models.MediaTypeAudiobook {
		var dest string
		if layout == "templated" {
			d, err := s.renamer.AudiobookDestDir(folder, author, book, seriesTitle, seriesNum)
			if err != nil {
				s.failImport(ctx, dl, models.StateImportBlocked, fmt.Sprintf("drop folder destination invalid: %v", err))
				return true
			}
			dest = d
		} else {
			dest = filepath.Join(folder, s.renamer.DropAudiobookName(author, book))
		}
		dest = UniqueDir(dest)
		if err := s.dropPlaceAudiobook(importCtx, downloadPath, bookFiles, explicitFiles, dest, linkMode); err != nil {
			slog.Warn("drop: audiobook handoff failed", "title", dl.Title, "dst", dest, "mode", linkMode, "error", err)
			s.failImport(ctx, dl, models.StateImportFailed, fmt.Sprintf("drop folder handoff failed: %v", err))
			return true
		}
		s.finishDrop(ctx, dl, layout, linkMode, models.MediaTypeAudiobook, dest)
		return true
	}

	var placed int
	var lastErr error
	for _, srcFile := range bookFiles {
		var dest string
		if layout == "templated" {
			d, err := s.renamer.DestPath(folder, author, book, seriesTitle, seriesNum, srcFile)
			if err != nil {
				lastErr = err
				continue
			}
			dest = d
		} else {
			dest = filepath.Join(folder, s.renamer.DropEbookName(author, book, srcFile))
		}
		if err := dropPlaceFile(importCtx, srcFile, dest, linkMode); err != nil {
			lastErr = err
			continue
		}
		placed++
	}
	if placed == 0 {
		slog.Warn("drop: ebook handoff placed no files", "title", dl.Title, "error", lastErr)
		s.failImport(ctx, dl, models.StateImportFailed, fmt.Sprintf("drop folder handoff failed: %v", lastErr))
		return true
	}
	s.finishDrop(ctx, dl, layout, linkMode, models.MediaTypeEbook, folder)
	return true
}

// finishDrop parks a successful drop handoff in StateImportExternal and records
// the history event. The book stays out of the wanted re-grab loop while the
// hand-off is outstanding, and ScanLibrary reconciles the managed copy once the
// external tool produces it under the library dir.
func (s *Scanner) finishDrop(ctx context.Context, dl *models.Download, layout, linkMode, format, dest string) {
	s.updateDownloadStatus(ctx, dl.ID, models.StateImportExternal)
	slog.Info("drop: handoff complete, awaiting library scan",
		"title", dl.Title, "dst", dest, "format", format, "layout", layout, "mode", linkMode)
	s.createHistoryEvent(ctx, models.HistoryEventDownloadFolderImport, dl.Title, dl.BookID, map[string]string{
		"mode":   "drop",
		"status": string(models.StateImportExternal),
		"path":   dest,
		"layout": layout,
		"format": format,
	})
}

// dropPlaceFile copies or hardlinks a single file into the drop folder. The
// source is never removed (the download keeps seeding). A stale prior drop is
// removed first so hardlink (os.Link) doesn't fail on EEXIST and copy stays
// deterministic.
func dropPlaceFile(ctx context.Context, src, dst, linkMode string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create drop dir: %w", err)
	}
	if _, err := os.Lstat(dst); err == nil {
		_ = os.Remove(dst)
	}
	switch linkMode {
	case "hardlink":
		if err := HardlinkFile(src, dst); err != nil {
			return err
		}
	default: // copy
		if err := CopyFileCtx(ctx, src, dst); err != nil {
			return err
		}
	}
	slog.Info("drop: placed file", "src", src, "dst", dst, "mode", linkMode)
	return nil
}

// dropPlaceAudiobook places an audiobook (folder, single file, or per-file set)
// into destDir via copy/hardlink only. Mirrors the normal audiobook source
// resolution (#903) but never moves the source. destDir must not already exist
// (the caller passes a UniqueDir).
func (s *Scanner) dropPlaceAudiobook(ctx context.Context, downloadPath string, bookFiles, explicitFiles []string, destDir, linkMode string) error {
	source := downloadPath
	usePerFile := false
	if len(explicitFiles) > 0 {
		src, perFile := s.resolveAudiobookSource(downloadPath, bookFiles)
		usePerFile = perFile
		if !perFile {
			source = src
		}
	}
	if usePerFile {
		if err := os.MkdirAll(destDir, 0o750); err != nil {
			return fmt.Errorf("create drop dir: %w", err)
		}
		for _, f := range bookFiles {
			if err := dropPlaceFile(ctx, f, filepath.Join(destDir, filepath.Base(f)), linkMode); err != nil {
				return err
			}
		}
		return nil
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if linkMode == "hardlink" {
			return HardlinkDir(source, destDir)
		}
		return CopyDirCtx(ctx, source, destDir)
	}
	return dropPlaceFile(ctx, source, filepath.Join(destDir, filepath.Base(source)), linkMode)
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
