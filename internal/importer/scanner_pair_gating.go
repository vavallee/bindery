package importer

import (
	"context"
	"log/slog"
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
func (s *Scanner) dropPairGated(ctx context.Context, dl *models.Download, book *models.Book, author *models.Author, downloadPath string, bookFiles, explicitFiles []string, format, folder, layout, linkMode string) bool {
	siblingFormat := models.MediaTypeAudiobook
	if format == models.MediaTypeAudiobook {
		siblingFormat = models.MediaTypeEbook
	}

	heldSibling := s.findHeldSibling(ctx, book.ID, dl.ID, siblingFormat)
	if !siblingFormatOnDisk(book, siblingFormat) && heldSibling == nil {
		// The other format isn't here yet — hold this one until it arrives (or
		// the timeout escape hatch fires).
		s.parkHeldFormat(ctx, dl, format, downloadPath)
		return true
	}

	// Sibling is present: drop this format now.
	dest, blocked, err := s.placeDroppedFormat(ctx, book, author, downloadPath, bookFiles, explicitFiles, format, folder, layout, linkMode)
	if err != nil {
		s.failDrop(ctx, dl, blocked, err)
		return true
	}

	// Release the held sibling alongside it so the pair lands together.
	if heldSibling != nil {
		s.releaseHeldSibling(ctx, heldSibling, book, author, siblingFormat, folder, layout, linkMode)
	}

	s.finishDrop(ctx, dl, layout, linkMode, format, dest)
	return true
}

// parkHeldFormat holds a completed format under pair gating: it records the
// download's on-disk location (so the files can be released later without
// re-deriving the path) and parks it in StateImportHeld. The source files are
// left untouched in the download dir — still seeding — until the sibling
// completes or the timeout drops this format alone.
func (s *Scanner) parkHeldFormat(ctx context.Context, dl *models.Download, format, downloadPath string) {
	if err := s.downloads.SetImportPath(ctx, dl.ID, downloadPath); err != nil {
		slog.Warn("drop: pair gating — failed to record held download path", "title", dl.Title, "path", downloadPath, "error", err)
	}
	s.updateDownloadStatus(ctx, dl.ID, models.StateImportHeld)
	slog.Info("drop: pair gating — holding format until its sibling arrives",
		"title", dl.Title, "format", format, "path", downloadPath)
	s.createHistoryEvent(ctx, models.HistoryEventDownloadFolderImport, dl.Title, dl.BookID, map[string]string{
		"mode":   "drop-hold",
		"status": string(models.StateImportHeld),
		"format": format,
		"path":   downloadPath,
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
		files := discoverBookFiles(h.ImportPath, nil)
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
	files := discoverBookFiles(held.ImportPath, nil)
	if len(files) == 0 {
		s.failImport(ctx, held, models.StateImportFailed,
			"pair gating release: held download files are no longer present — retry the import")
		return
	}
	dest, blocked, err := s.placeDroppedFormat(ctx, book, author, held.ImportPath, files, nil, format, folder, layout, linkMode)
	if err != nil {
		s.failDrop(ctx, held, blocked, err)
		return
	}
	slog.Info("drop: pair gating — releasing held format now that its sibling arrived",
		"title", held.Title, "format", format, "dst", dest)
	s.finishDrop(ctx, held, layout, linkMode, format, dest)
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
	folder, layout, linkMode := s.dropSettings(ctx)
	if folder == "" {
		// No drop folder configured any more — nowhere to release to. Leave the
		// held rows as-is; a manual retry can recover them.
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
		files := discoverBookFiles(h.ImportPath, nil)
		format := detectDownloadFormat(files)
		slog.Warn("drop: pair gating timeout — releasing held format alone; its sibling never arrived",
			"title", h.Title, "format", format, "held_for", age.Truncate(time.Minute).String(), "timeout", timeout.String())
		if len(files) == 0 {
			s.failImport(ctx, h, models.StateImportFailed,
				"pair gating timeout: held download files are no longer present — retry the import")
			continue
		}
		dest, blocked, err := s.placeDroppedFormat(ctx, book, author, h.ImportPath, files, nil, format, folder, layout, linkMode)
		if err != nil {
			s.failDrop(ctx, h, blocked, err)
			continue
		}
		s.finishDrop(ctx, h, layout, linkMode, format, dest)
	}
}
