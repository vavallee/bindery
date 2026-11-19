package importer

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// dropPairGatingDefaultTimeout is the escape-hatch fallback: how long a held
// format waits for its sibling before being dropped alone. Overridable via the
// import.drop_pair_gating_timeout_hours setting (#942).
const dropPairGatingDefaultTimeout = 72 * time.Hour

// dropPairGatingEnabled reports whether the opt-in "hold until both formats
// present" gate is on for drop-folder handoff (#942). It returns true only when
// the value is explicitly "true"; the feature is OFF by default so existing
// drops hand off the moment a format completes. Read as a string literal to
// avoid an import cycle with the api package; keep in sync with
// api.SettingImportDropPairGating.
func (s *Scanner) dropPairGatingEnabled(ctx context.Context) bool {
	if s.settings == nil {
		return false
	}
	setting, err := s.settings.Get(ctx, "import.drop_pair_gating")
	if err != nil || setting == nil {
		return false
	}
	return setting.Value == "true"
}

// dropPairGatingTimeout returns how long a held format waits for its sibling
// before the escape hatch releases it alone. It reads
// import.drop_pair_gating_timeout_hours (a positive integer number of hours),
// falling back to dropPairGatingDefaultTimeout when unset, empty, non-numeric,
// or non-positive. Read as a string literal; keep in sync with
// api.SettingImportDropPairGatingTimeoutHours.
func (s *Scanner) dropPairGatingTimeout(ctx context.Context) time.Duration {
	if s.settings == nil {
		return dropPairGatingDefaultTimeout
	}
	setting, err := s.settings.Get(ctx, "import.drop_pair_gating_timeout_hours")
	if err != nil || setting == nil {
		return dropPairGatingDefaultTimeout
	}
	n, err := strconv.Atoi(strings.TrimSpace(setting.Value))
	if err != nil || n <= 0 {
		return dropPairGatingDefaultTimeout
	}
	return time.Duration(n) * time.Hour
}

// siblingFormatOnDisk reports whether the given format's file path is already
// recorded on the book — i.e. that format is present in the library (a mixed
// config where one format imports normally and the other drops). In the common
// pure-drop-both case neither path is ever set (the external tool owns the
// library), so pairing is resolved through the held-download record instead;
// this covers the mixed case the issue frames as "both present locally".
func siblingFormatOnDisk(book *models.Book, format string) bool {
	if book == nil {
		return false
	}
	if format == models.MediaTypeAudiobook {
		return strings.TrimSpace(book.AudiobookFilePath) != ""
	}
	return strings.TrimSpace(book.EbookFilePath) != ""
}

// dropPairGated implements the hold/release gate for a media_type=both book on
// the drop path (#942). When the sibling format is not yet present (neither on
// disk nor already held), this format is parked in StateImportHeld with its
// files left in place. When the sibling IS present, this format drops now and,
// if the sibling was held, it is released into the drop folder too so the pair
// lands together. Returns true — it always takes ownership of the download.
func (s *Scanner) dropPairGated(ctx context.Context, dl *models.Download, book *models.Book, author *models.Author, downloadPath string, bookFiles, explicitFiles []string, format, folder, layout, linkMode string, contentCheck map[string]any) bool {
	siblingFormat := siblingFormatOf(format)

	heldSibling := s.findHeldSibling(ctx, book.ID, dl.ID, siblingFormat)
	if !siblingFormatOnDisk(book, siblingFormat) && heldSibling == nil {
		// The other format isn't here yet — hold this one until it arrives (or
		// the timeout escape hatch fires).
		s.parkHeldFormat(ctx, dl, format, s.heldRecord(ctx, format, downloadPath, bookFiles, explicitFiles))
		return true
	}

	// Sibling is present: drop this format now.
	dest, sources, blocked, err := s.placeDroppedFormat(ctx, book, author, downloadPath, bookFiles, explicitFiles, format, folder, layout, linkMode)
	if err != nil {
		s.failDrop(ctx, dl, blocked, err)
		return true
	}

	// Release the held sibling alongside it so the pair lands together, into
	// the sibling's own drop folder: audiobooks can have one of their own
	// (#1632), and then the pair lands in two folders at once.
	if heldSibling != nil {
		siblingFolder := s.dropFolderFor(ctx, siblingFormat)
		if siblingFolder == "" {
			siblingFolder = folder
		}
		s.releaseHeldSibling(ctx, heldSibling, book, author, siblingFormat, siblingFolder, layout, linkMode)
	}

	s.finishDrop(ctx, dl, layout, linkMode, format, sources, dest, contentCheck)
	return true
}

// heldContext returns ctx marked as manual or automatic according to the held
// download itself, ignoring whatever the releasing call carried (#3945). A
// manual import is never refused by the checks, and an automatic one always is,
// whichever import tick or sibling download releases it. An unreadable value
// counts as automatic: the checks run.
func (s *Scanner) heldContext(ctx context.Context, held *models.Download) context.Context {
	manual, err := s.downloads.ImportManual(ctx, held.ID)
	if err != nil {
		slog.Warn("drop: pair gating — could not read whether the held download was a manual import; checking it", "title", held.Title, "error", err)
		manual = false
	}
	return withManualFlag(ctx, manual)
}

// siblingFormatOf returns the other format of a media_type=both pair.
func siblingFormatOf(format string) string {
	if format == models.MediaTypeAudiobook {
		return models.MediaTypeEbook
	}
	return models.MediaTypeAudiobook
}

// heldRecord is what a hold writes to downloads.import_path: exactly what the
// download owns, never a folder it shares with other downloads. Transmission
// reports the client's shared category folder as the download path and passes
// the torrent's own files separately, so recording the path would make the
// release rediscover (and drop) every unrelated book in that folder.
//
//   - No explicit files, or the torrent's own folder (SABnzbd, qBittorrent,
//     Deluge, rTorrent): the download path is the download's own, recorded as is.
//   - An audiobook whose files share a folder strictly below the shared path:
//     that folder, which is what a drop places whole.
//   - Anything else: the files themselves, a single one as a plain path and
//     several as a JSON array (models.EncodeHandedOffSources), the encoding the
//     hand-off already uses.
//
// A value that is not a recorded file list is read as a folder, which is how a
// row held before this change (always a folder) keeps releasing.
func (s *Scanner) heldRecord(ctx context.Context, format, downloadPath string, bookFiles, explicitFiles []string) string {
	if len(explicitFiles) == 0 || isTorrentOwnFolder(ctx) {
		return downloadPath
	}
	if format == models.MediaTypeAudiobook {
		if unit, perFile := s.resolveAudiobookSource(downloadPath, bookFiles, false); !perFile && unit != "" {
			return unit
		}
	}
	if encoded := models.EncodeHandedOffSources(bookFiles); encoded != "" {
		return encoded
	}
	return downloadPath
}

// heldSource reads back what heldRecord wrote: the path to place from, the
// explicit file list when the hold recorded several files (nil for a folder or
// a single path), and the book files to check and place. A folder recorded
// before the file list existed is walked as it always was.
func heldSource(held *models.Download) (path string, explicit, files []string) {
	paths := held.HandedOffSources()
	switch len(paths) {
	case 0:
		return "", nil, nil
	case 1:
		path = paths[0]
	default:
		explicit = paths
		path = commonParentDir(paths)
	}
	return path, explicit, discoverHeldFiles(path, explicit)
}

// commonParentDir is the deepest folder holding every path.
func commonParentDir(paths []string) string {
	common := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		for dir := filepath.Dir(p); !pathUnderDir(dir, common); {
			parent := filepath.Dir(common)
			if parent == common {
				return common
			}
			common = parent
		}
	}
	return common
}

// parkHeldFormat holds a completed format under pair gating: it records what
// the download owns (see heldRecord) so the files can be released later
// without re-deriving them, and parks it in StateImportHeld. The source files
// are left untouched in the download dir, still seeding, until the sibling
// completes or the timeout drops this format alone.
func (s *Scanner) parkHeldFormat(ctx context.Context, dl *models.Download, format, record string) {
	if err := s.downloads.SetImportPath(ctx, dl.ID, record); err != nil {
		slog.Warn("drop: pair gating failed to record the held download", "title", dl.Title, "recorded", record, "error", err)
	}
	// A release runs under another call's context, so the hold keeps whether
	// this was a manual import (#3945).
	if err := s.downloads.SetImportManual(ctx, dl.ID, isManualImport(ctx)); err != nil {
		slog.Warn("drop: pair gating — failed to record whether the held download was a manual import", "title", dl.Title, "error", err)
	}
	s.updateDownloadStatus(ctx, dl.ID, models.StateImportHeld)
	slog.Info("drop: pair gating — holding format until its sibling arrives",
		"title", dl.Title, "format", format, "path", record)
	s.createHistoryEvent(ctx, models.HistoryEventDownloadFolderImport, dl.Title, dl.BookID, dl.OwnerUserID, map[string]string{
		"mode":   "drop-hold",
		"status": string(models.StateImportHeld),
		"format": format,
		"path":   record,
	})
}

// findHeldSibling returns a held download for the same book whose format is
// wantFormat (excluding excludeID). It re-detects each held download's format
// from the files still at its recorded path, so a held download whose files
// have since vanished can't be classified and is skipped.
func (s *Scanner) findHeldSibling(ctx context.Context, bookID, excludeID int64, wantFormat string) *models.Download {
	if s.downloads == nil {
		return nil
	}
	held, err := s.downloads.ListByStatus(ctx, models.StateImportHeld)
	if err != nil {
		slog.Warn("drop: pair gating — failed to list held downloads", "error", err)
		return nil
	}
	for i := range held {
		h := &held[i]
		if h.ID == excludeID || h.BookID == nil || *h.BookID != bookID {
			continue
		}
		_, _, files := heldSource(h)
		if len(files) == 0 {
			continue
		}
		if detectDownloadFormat(files) == wantFormat {
			return h
		}
	}
	return nil
}

// releaseHeldSibling drops a previously held format into the drop folder and
// finishes it, re-discovering its files from the recorded path. Used when the
// second format of a pair completes.
func (s *Scanner) releaseHeldSibling(ctx context.Context, held *models.Download, book *models.Book, author *models.Author, format, folder, layout, linkMode string) {
	path, explicit, files := heldSource(held)
	if len(files) == 0 {
		s.failImport(ctx, held, models.StateImportFailed,
			"pair gating release: held download files are no longer present — retry the import")
		return
	}
	// The files are re-read here, so they get the same checks again (#3945):
	// the hold stored only where they are, and they may have changed since. A
	// refusal records the held value as it stands, for Match to book.
	files, contentCheck, ok := s.checkBeforeDrop(s.heldContext(ctx, held), held, book, author, held.ImportPath, files, format, true)
	if !ok {
		return
	}
	dest, sources, blocked, err := s.placeDroppedFormat(ctx, book, author, path, files, explicit, format, folder, layout, linkMode)
	if err != nil {
		s.failDrop(ctx, held, blocked, err)
		return
	}
	slog.Info("drop: pair gating — releasing held format now that its sibling arrived",
		"title", held.Title, "format", format, "dst", dest)
	s.finishDrop(ctx, held, layout, linkMode, format, sources, dest, contentCheck)
}

// sweepHeldPairGating is the escape hatch (#942): it drops any held format whose
// sibling never arrived within the configured timeout, alone, and finishes the
// download so it can't wait forever. It is evaluated lazily — invoked on every
// external-mode import tick from tryImportInternal — rather than on a dedicated
// scheduled sweep, so a held format is released on the next drop-folder activity
// after its deadline. The hold-start time is taken from completed_at (stamped
// when the download completed, moments before it was held), falling back to
// added_at. The sweep runs regardless of the gating setting so that turning the
// feature off still drains anything already held.
func (s *Scanner) sweepHeldPairGating(ctx context.Context) {
	if s.downloads == nil {
		return
	}
	held, err := s.downloads.ListByStatus(ctx, models.StateImportHeld)
	if err != nil {
		slog.Warn("drop: pair gating — failed to list held downloads for timeout sweep", "error", err)
		return
	}
	if len(held) == 0 {
		return
	}
	timeout := s.dropPairGatingTimeout(ctx)
	now := time.Now()
	for i := range held {
		h := &held[i]
		start := h.AddedAt
		if h.CompletedAt != nil {
			start = *h.CompletedAt
		}
		age := now.Sub(start)
		if age < timeout {
			continue
		}
		book, author := s.resolveBookAuthor(ctx, h.BookID)
		if book == nil {
			slog.Warn("drop: pair gating timeout — held download has no matching book, skipping", "title", h.Title)
			continue
		}
		path, explicit, files := heldSource(h)
		format := detectDownloadFormat(files)
		// The drop folder is per format (#1632). When this format no longer
		// drops at all (no drop folder any more, or its import mode is no
		// longer external) there is nowhere to release it to. Leave the held
		// row as-is; a manual retry imports it under the current settings.
		if !s.formatDrops(ctx, format) {
			continue
		}
		folder, layout, linkMode := s.dropSettings(ctx, format)
		slog.Warn("drop: pair gating timeout — releasing held format alone; its sibling never arrived",
			"title", h.Title, "format", format, "held_for", age.Truncate(time.Minute).String(), "timeout", timeout.String())
		if len(files) == 0 {
			s.failImport(ctx, h, models.StateImportFailed,
				"pair gating timeout: held download files are no longer present — retry the import")
			continue
		}
		files, contentCheck, ok := s.checkBeforeDrop(s.heldContext(ctx, h), h, book, author, h.ImportPath, files, format, true)
		if !ok {
			continue
		}
		dest, sources, blocked, err := s.placeDroppedFormat(ctx, book, author, path, files, explicit, format, folder, layout, linkMode)
		if err != nil {
			s.failDrop(ctx, h, blocked, err)
			continue
		}
		s.finishDrop(ctx, h, layout, linkMode, format, sources, dest, contentCheck)
	}
}

// discoverHeldBookFiles re-reads the files of a download parked by pair gating.
// The hold was decided with the import's own context, which a later release or
// sweep no longer has. An automatic import blocks a download holding only files
// too small to be a book before it can be held, so a held path whose ebook
// files are all that small was a manual import (#3224): they are kept. Beside a
// real book, small files are still left out as notes.
func discoverHeldBookFiles(path string) []string {
	return discoverHeldFiles(path, nil)
}

// discoverHeldFiles is discoverHeldBookFiles over an explicit file list when
// the hold recorded one, so a folder shared with other downloads is never
// walked.
func discoverHeldFiles(path string, explicit []string) []string {
	files, tooSmall := discoverBookFilesSized(path, explicit, true)
	if len(files) == 0 && len(tooSmall) > 0 {
		files, _ = discoverBookFilesSized(path, explicit, false)
	}
	return files
}
