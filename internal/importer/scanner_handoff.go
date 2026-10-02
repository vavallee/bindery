package importer

import (
	"context"
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
// "hardlink", or "external"), or "" when the setting is absent, "auto", or
// unrecognised — all of which mean "auto" (let resolveImportMode pick
// hardlink-vs-copy per destination). "auto" is the explicit UI value for this
// default (api.SettingImportMode); it maps to "" here just like an unset key.
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

// configuredImportModeFor returns the operator-set import mode for one media
// format (#1632). Ebooks always use "import.mode". Audiobooks use
// "import.audiobook.mode" when it names a mode, so a Calibre-Web-Automated +
// Audiobookshelf library can drop ebooks into an ingest folder while
// audiobooks are placed by copy or hardlink in the audiobook root. An unset,
// empty or unrecognised override means "same as import.mode", which keeps
// every existing install behaving exactly as before. An explicit "auto"
// override returns "" (the auto default) even when import.mode is set,
// because that is what the operator asked for. Keep the literal in sync with
// api.SettingImportAudiobookMode.
func (s *Scanner) configuredImportModeFor(ctx context.Context, format string) string {
	if format == models.MediaTypeAudiobook && s.settings != nil {
		if setting, err := s.settings.Get(ctx, "import.audiobook.mode"); err == nil && setting != nil {
			switch v := strings.TrimSpace(setting.Value); v {
			case "move", "copy", "hardlink", "external":
				return v
			case "auto":
				return ""
			}
		}
	}
	return s.configuredImportMode(ctx)
}

// downloadImportMode resolves the configured import mode for one download,
// before any placement decision is taken (#1632). When ebooks and audiobooks
// share a mode, which is every install that never set the audiobook override,
// it returns that mode without touching the filesystem. Only when they differ
// does it work out the download's format, with exactly the inputs the
// placement branches use later (the caller's format hint, else the
// extensions of the discovered book files), so the mode chosen here and the
// branch the download ends up in agree.
func (s *Scanner) downloadImportMode(ctx context.Context, downloadPath, formatHint string, explicitFiles []string) string {
	ebookMode := s.configuredImportModeFor(ctx, models.MediaTypeEbook)
	audiobookMode := s.configuredImportModeFor(ctx, models.MediaTypeAudiobook)
	if ebookMode == audiobookMode {
		return ebookMode
	}
	format := formatHint
	if format != models.MediaTypeAudiobook && format != models.MediaTypeEbook {
		format = detectDownloadFormat(discoverBookFiles(downloadPath, explicitFiles))
	}
	if format == models.MediaTypeAudiobook {
		return audiobookMode
	}
	return ebookMode
}

// isUsenetClient reports whether clientType names a usenet download client.
// Completed usenet job folders have no post-import purpose — nothing seeds
// from them — which is what justifies effectiveConfiguredMode's remapping.
func isUsenetClient(clientType string) bool {
	return clientType == "sabnzbd" || clientType == "nzbget"
}

// effectiveConfiguredMode maps the operator-set import mode through the
// protocol of the client that produced the download (#1542). Hardlink — and
// the auto default that probes for it — exists solely to preserve torrent
// seeding; for a usenet download it has zero benefit and a real cost: the
// completed job folder is left behind forever, and invisibly so, because the
// post-import cleanup removes the client's history entry but not its files.
// Both therefore resolve to "move" for usenet downloads. Explicit "copy" is
// honoured (an operator may want the client's own retention or external
// tooling to see the finished files), as are "move" and "external". Non-usenet
// clients and client-less imports (drop folder, ABS, manual) pass through
// untouched.
func effectiveConfiguredMode(configuredMode, clientType string) string {
	if !isUsenetClient(clientType) {
		return configuredMode
	}
	switch configuredMode {
	case "", "hardlink":
		return "move"
	}
	return configuredMode
}

// resolveImportMode picks the effective placement mode for a destination root.
// An explicit operator setting (configuredMode) is honoured as-is. For the auto
// default it returns "hardlink" when a file can actually be hard-linked from
// src into destRoot (free, preserves seeding) or "copy" otherwise. destRoot MUST be
// the root the files actually land under — per-author RootFolderID and audiobook
// roots can live on a different mount than s.libraryDir, and choosing the mode
// against s.libraryDir there picked an always-failing cross-device hardlink.
// Pass empty strings for src/destRoot to get the cross-device default ("copy")
// without a probe.
//
// hardlinkable (not a bare same-device check) is used deliberately: separate
// bind mounts and Unraid /mnt/user shares report the same st_dev yet reject
// cross-mount hardlinks with EXDEV, so a device-ID match alone would auto-select
// an always-failing hardlink mode.
func (s *Scanner) resolveImportMode(configuredMode, src, destRoot string) string {
	if configuredMode != "" {
		return configuredMode
	}
	if hardlinkable(src, destRoot) {
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

// audiobookFileTemplate reads the "naming.audiobook_file_template" setting
// (#1126). A non-empty value opts the install into per-file audiobook renaming:
// every audiobook folder import is flattened into destDir with each track named
// from this template (its {Part} token carries the playback order). Empty (the
// default) preserves the download's internal layout. Kept as a string literal
// to avoid an import cycle with the api package; keep in sync with
// api.SettingNamingAudiobookFileTemplate.
func (s *Scanner) audiobookFileTemplate(ctx context.Context) string {
	if s.settings == nil {
		return ""
	}
	setting, err := s.settings.Get(ctx, "naming.audiobook_file_template")
	if err != nil || setting == nil {
		return ""
	}
	return strings.TrimSpace(setting.Value)
}

// singleAudiobookFileName is the name a single-file audiobook takes inside its
// folder: the source file's own name, or, when naming.audiobook_file_template
// is set, that template rendered the way the folder branch renders a track,
// with {Part} left out as AudiobookSingleFileName describes (#2900). The
// import's single-file branch and Rename files both call it, so a reorganized
// file lands where a fresh import would put it. The extension is lowercased
// for the template, the same as flattenAudiobookDirNamed does for each track.
// A template that renders to nothing usable keeps the source name rather than
// placing a file called "." or with no name at all.
func (s *Scanner) singleAudiobookFileName(ctx context.Context, author *models.Author, book *models.Book, seriesTitle, seriesNum, src string) string {
	name := filepath.Base(src)
	tmpl := s.audiobookFileTemplate(ctx)
	if tmpl == "" {
		return name
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(src)), ".")
	rendered := s.renamer.AudiobookSingleFileName(tmpl, author, book, seriesTitle, seriesNum, ext)
	if rendered == "" || rendered == "." || rendered == ".." || rendered == string(filepath.Separator) {
		return name
	}
	return rendered
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
		return filterImportableFiles(explicitFiles)
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

// filterImportableFiles reduces a download client's authoritative file list
// (#903) to the entries the importer may actually act on. Two entries are
// dropped:
//
//   - symlinks (Lstat, so the link itself is inspected, not its target). A
//     malicious release can plant a book-extension symlink pointing at an
//     arbitrary file; importing it would copy the target's bytes into the
//     library where the user can download them.
//   - paths that are not on this host at all. The list describes what the
//     TORRENT contains, not what is on disk: a torrent whose payload was moved
//     into the library by a prior Bindery import (move mode) — or deleted with
//     the book from the UI — still enumerates every file, and qBittorrent keeps
//     reporting it at 100%. Passing those phantom paths through made the
//     importer believe it had files to work with (#1955 logged `files=1` for a
//     file that no longer existed), so it skipped the already-in-library and
//     path-missing checks and failed deep inside the mover with an opaque
//     message, three times, before terminally blocking the download.
//
// An Lstat error other than "not exist" (a permission problem on the parent
// directory, say) also drops the entry: the import would fail on it anyway,
// and falling through to tryImportInternal's path checks describes the
// situation better than a failure raised from inside the mover.
func filterImportableFiles(paths []string) []string {
	out := paths[:0:0]
	for _, p := range paths {
		if !importableSourceFile(p) {
			slog.Warn("skipping download-client file: not a regular file on this host (missing, unreadable, or a symlink)",
				"path", p)
			continue
		}
		out = append(out, p)
	}
	return out
}

// importableSourceFile is the single-path predicate behind
// filterImportableFiles, factored out so importSourcePresent — which decides
// whether spending a retry attempt on this file list is honest — applies
// EXACTLY the same test. When the two disagreed (Stat vs Lstat), a symlink with
// a live target counted as present for the retry guard and was dropped by the
// filter, so every poll spent an attempt on a list the importer had already
// emptied.
func importableSourceFile(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSymlink == 0
}

// dropSettings reads the external-mode drop-folder configuration (#941) for
// one media format. Defaults: layout "flat", link mode "copy". An empty folder
// means the feature is off for that format. Layout and link mode are shared by
// both formats; the folder comes from dropFolderFor. Keep the literal keys in
// sync with the api.SettingImportDrop* constants (the importer can't import
// the api package, cycle).
func (s *Scanner) dropSettings(ctx context.Context, format string) (folder, layout, linkMode string) {
	layout, linkMode = "flat", "copy"
	if s.settings == nil {
		return "", layout, linkMode
	}
	folder = s.dropFolderFor(ctx, format)
	if v, err := s.settings.Get(ctx, "import.drop_layout"); err == nil && v != nil && v.Value == "templated" {
		layout = "templated"
	}
	if v, err := s.settings.Get(ctx, "import.drop_link_mode"); err == nil && v != nil && v.Value == "hardlink" {
		linkMode = "hardlink"
	}
	return folder, layout, linkMode
}

// dropFolderFor returns the drop folder one media format is handed off into,
// or "" when that format has none. Audiobooks use import.audiobook.drop_folder
// when it is set and fall back to import.drop_folder otherwise (#1632), so an
// install that never set the audiobook folder drops both formats into the one
// folder exactly as before, which is what Storyteller pair gating (#942)
// relies on. Keep the literal in sync with api.SettingImportAudiobookDropFolder.
func (s *Scanner) dropFolderFor(ctx context.Context, format string) string {
	if s.settings == nil {
		return ""
	}
	if format == models.MediaTypeAudiobook {
		if v, err := s.settings.Get(ctx, "import.audiobook.drop_folder"); err == nil && v != nil {
			if folder := strings.TrimSpace(v.Value); folder != "" {
				return folder
			}
		}
	}
	if v, err := s.settings.Get(ctx, "import.drop_folder"); err == nil && v != nil {
		return strings.TrimSpace(v.Value)
	}
	return ""
}

// formatDrops reports whether a format, under the current settings, is handed
// off into a drop folder: its effective import mode is external AND it has a
// drop folder. Pair gating (#942) uses it to decide whether waiting for the
// sibling format can ever end with the pair landing together.
func (s *Scanner) formatDrops(ctx context.Context, format string) bool {
	return s.configuredImportModeFor(ctx, format) == "external" && s.dropFolderFor(ctx, format) != ""
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
	// No drop folder for either format: plain external mode, decided before
	// touching the download, which may not even be mounted on this host.
	if s.dropFolderFor(ctx, models.MediaTypeEbook) == "" && s.dropFolderFor(ctx, models.MediaTypeAudiobook) == "" {
		return false
	}

	// The format decides the destination (#1632): audiobooks may have a drop
	// folder of their own, so it is resolved before the folder is read.
	bookFiles := discoverBookFiles(downloadPath, explicitFiles)
	detectedFormat := detectDownloadFormat(bookFiles)
	if formatHint == models.MediaTypeAudiobook || formatHint == models.MediaTypeEbook {
		detectedFormat = formatHint
	}

	folder, layout, linkMode := s.dropSettings(ctx, detectedFormat)
	if folder == "" {
		return false
	}

	if len(bookFiles) == 0 {
		if _, statErr := os.Stat(downloadPath); os.IsNotExist(statErr) {
			s.failImport(ctx, dl, models.StateImportFailed,
				fmt.Sprintf("download path not found: %q — configure PathRemap on the download client", downloadPath))
			return true
		}
		s.failImport(ctx, dl, models.StateImportFailed, fmt.Sprintf("no book files found in %q", downloadPath))
		return true
	}

	// Resolve book + author for naming. A nil book is fatal for the drop path
	// because we can't compute a destination name.
	book, author := s.resolveBookAuthor(ctx, dl.BookID)
	if book == nil {
		s.failImport(ctx, dl, models.StateImportFailed, "could not match any book to this download — check the release title")
		return true
	}

	// Pair gating (#942): a media_type=both book only hands off once BOTH
	// formats are present, so the drop of this format may be held back until its
	// sibling arrives. Single-format books, and everything when gating is off,
	// fall through to the immediate placement below unchanged. So does a book
	// whose sibling format is not handed off at all (#1632: audiobooks imported
	// into the library while ebooks drop): nothing but the timeout would ever
	// release the hold, so holding would only delay this format by days.
	if s.dropPairGatingEnabled(ctx) && book.MediaType == models.MediaTypeBoth && s.formatDrops(ctx, siblingFormatOf(detectedFormat)) {
		return s.dropPairGated(ctx, dl, book, author, downloadPath, bookFiles, explicitFiles, detectedFormat, folder, layout, linkMode)
	}

	dest, blocked, err := s.placeDroppedFormat(ctx, book, author, downloadPath, bookFiles, explicitFiles, detectedFormat, folder, layout, linkMode)
	if err != nil {
		s.failDrop(ctx, dl, blocked, err)
		return true
	}
	s.finishDrop(ctx, dl, layout, linkMode, detectedFormat, dest)
	return true
}

// placeDroppedFormat copies/hardlinks one completed format (ebook or audiobook)
// into the configured drop folder using the flat or templated layout, never
// moving the source (the download keeps seeding). It returns the destination
// path recorded in history, a "blocked" flag distinguishing an invalid
// destination (a template/config error — StateImportBlocked, retryable by the
// user after fixing settings) from a placement failure (StateImportFailed), and
// the error. Shared by the immediate drop, the paired release, and the timeout
// escape hatch so all three place files identically.
func (s *Scanner) placeDroppedFormat(ctx context.Context, book *models.Book, author *models.Author, downloadPath string, bookFiles, explicitFiles []string, format, folder, layout, linkMode string) (dest string, blocked bool, err error) {
	importCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)

	if format == models.MediaTypeAudiobook {
		if layout == "templated" {
			d, derr := s.destRenamer(ctx).AudiobookDestDir(folder, author, book, seriesTitle, seriesNum)
			if derr != nil {
				return "", true, derr
			}
			dest = d
		} else {
			dest = filepath.Join(folder, s.renamer.DropAudiobookName(author, book))
		}
		dest = UniqueDir(dest)
		if perr := s.dropPlaceAudiobook(importCtx, downloadPath, bookFiles, explicitFiles, dest, linkMode); perr != nil {
			return "", false, perr
		}
		return dest, false, nil
	}

	var placed int
	var lastErr error
	for _, srcFile := range bookFiles {
		var fileDest string
		if layout == "templated" {
			d, derr := s.destRenamer(ctx).DestPath(folder, author, book, seriesTitle, seriesNum, srcFile)
			if derr != nil {
				lastErr = derr
				continue
			}
			fileDest = d
		} else {
			fileDest = filepath.Join(folder, s.renamer.DropEbookName(author, book, srcFile))
		}
		if perr := dropPlaceFile(importCtx, srcFile, fileDest, linkMode); perr != nil {
			lastErr = perr
			continue
		}
		placed++
	}
	if placed == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("no book files could be placed")
		}
		return "", false, lastErr
	}
	return folder, false, nil
}

// failDrop marks a drop-path import as failed, distinguishing an invalid
// destination (StateImportBlocked — the operator must fix the naming template /
// folder before a retry can succeed) from a transient placement failure
// (StateImportFailed — retryable as-is).
func (s *Scanner) failDrop(ctx context.Context, dl *models.Download, blocked bool, err error) {
	if blocked {
		s.failImport(ctx, dl, models.StateImportBlocked, fmt.Sprintf("drop folder destination invalid: %v", err))
		return
	}
	slog.Warn("drop: handoff failed", "title", dl.Title, "error", err)
	s.failImport(ctx, dl, models.StateImportFailed, fmt.Sprintf("drop folder handoff failed: %v", err))
}

// resolveBookAuthor loads the book (and, best-effort, its author) for a
// download's BookID. Returns nil book when the download has no BookID or the
// lookup fails — callers treat that as "unmatched".
func (s *Scanner) resolveBookAuthor(ctx context.Context, bookID *int64) (*models.Book, *models.Author) {
	if bookID == nil {
		return nil, nil
	}
	b, err := s.books.GetByID(ctx, *bookID)
	if err != nil || b == nil {
		return nil, nil
	}
	if a, err := s.authors.GetByID(ctx, b.AuthorID); err == nil {
		return b, a
	}
	return b, nil
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
		// Same basename-collision preflight as the managed import path
		// (#2275). It matters more here, not less: dropPlaceFile removes an
		// existing destination before placing, so a collision would overwrite
		// silently in both link modes rather than failing on EEXIST.
		if err := checkPerFileCollisions(destDir, bookFiles); err != nil {
			return err
		}
		if err := os.MkdirAll(destDir, 0o750); err != nil {
			return fmt.Errorf("create drop dir: %w", err)
		}
		placed := make([]string, 0, len(bookFiles))
		for _, f := range bookFiles {
			name := filepath.Base(f)
			if err := dropPlaceFile(ctx, f, filepath.Join(destDir, name), linkMode); err != nil {
				// Drop handoff never moves, so every source is still in the
				// download directory and undoing this attempt is safe.
				rollbackPlacedFiles(destDir, placed)
				return err
			}
			placed = append(placed, name)
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

// enqueueCalibreDelivery queues one just-imported ebook file for the Calibre
// delivery worker (#2832). It used to push inline, per file, inside the
// import: a closed Calibre cost up to thirty seconds per file and the book
// was then missed for good, with one WARN line as the only record. Now the
// import only writes a ledger row and the worker delivers it, retrying with
// backoff until Calibre is reachable. It reports whether a row was queued,
// so the caller knows to kick the worker once the loop is done.
//
// Only ebook files are queued. The Calibre hand off takes one ebook file:
// the plugin derives the format from the extension and rejects a folder, and
// calibredb scans a folder against Calibre's BOOK_EXTENSIONS, which carry no
// audio format.
func (s *Scanner) enqueueCalibreDelivery(ctx context.Context, book *models.Book, dl *models.Download, edition *models.Edition, path string) bool {
	if s.calibreQueue == nil || s.calibreMode == nil || book == nil {
		return false
	}
	mode := s.calibreMode()
	if mode != calibre.ModeCalibredb && mode != calibre.ModePlugin {
		return false
	}
	files, err := s.books.ListFiles(ctx, book.ID)
	if err != nil {
		slog.Warn("calibre: could not queue the delivery, listing the book's files failed", "bookId", book.ID, "path", path, "error", err)
		return false
	}
	clean := filepath.Clean(path)
	var fileID int64
	for _, f := range files {
		if f.Format == models.MediaTypeEbook && filepath.Clean(f.Path) == clean {
			fileID = f.ID
			break
		}
	}
	if fileID == 0 {
		slog.Warn("calibre: could not queue the delivery, the file is not tracked under this book", "bookId", book.ID, "path", path)
		return false
	}
	// The edition the download was grabbed for, when there was one. Without
	// it the worker matches an edition by format at delivery time.
	var editionID *int64
	if dl != nil && dl.EditionID != nil && edition != nil && edition.ID == *dl.EditionID {
		id := edition.ID
		editionID = &id
	}
	queued, err := s.calibreQueue.Enqueue(ctx, book.ID, fileID, editionID, path)
	if err != nil {
		slog.Warn("calibre: could not queue the delivery", "bookId", book.ID, "path", path, "error", err)
		return false
	}
	if queued {
		slog.Debug("calibre: delivery queued", "mode", mode, "bookId", book.ID, "path", path)
	}
	return queued
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

// pushToGrimmory mirrors a just-imported ebook into Grimmory's BookDrop.
// All policy (enabled check, idempotency, logging) lives in the pusher;
// this is only the nil-safe call site.
func (s *Scanner) pushToGrimmory(ctx context.Context, book *models.Book, path string) {
	if s.grimmory == nil || book == nil || path == "" {
		return
	}
	s.grimmory.PushOnImport(ctx, book.ID, book.Title, path)
}
