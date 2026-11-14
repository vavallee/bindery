package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/dirperm"
	"github.com/vavallee/bindery/internal/importer/formatsniff"
	"github.com/vavallee/bindery/internal/models"
)

// ImportFromPath creates an import run for a file or folder already on disk,
// bypassing the download-client polling path. formatHint overrides extension-
// based format detection when non-empty ("ebook" or "audiobook").
func (s *Scanner) ImportFromPath(ctx context.Context, dl *models.Download, path, formatHint string) {
	defer s.lockManualBook(dl)()
	s.tryImportInternal(withManualImport(ctx), dl, path, "", "", formatHint, nil, nil)
}

// ImportFilesFromPath imports several files already on disk as ONE unit: the
// manual import of an audiobook's loose tracks, picked as separate rows from
// a folder scan (#2935). It runs the same explicit file list path a torrent
// download takes (#903), so the tracks land together in one audiobook folder
// for the book, named per the audiobook file template, instead of each track
// becoming its own single file audiobook. dir is the deepest folder holding
// every file; files are absolute paths.
func (s *Scanner) ImportFilesFromPath(ctx context.Context, dl *models.Download, dir string, files []string, formatHint string) {
	defer s.lockManualBook(dl)()
	s.tryImportInternal(withManualImport(ctx), dl, dir, "", "", formatHint, nil, files)
}

// ManualDownloadGUIDPrefix starts the GUID of every synthetic download the
// manual import handler creates, so the importer can tell an import a person
// asked for from a grabbed download.
const ManualDownloadGUIDPrefix = "manual-"

// lockManualBook serialises manual imports of the same book (#2935) and
// returns the unlock. A download with no book has nothing to collide on.
func (s *Scanner) lockManualBook(dl *models.Download) func() {
	if dl == nil || dl.BookID == nil {
		return func() {}
	}
	v, _ := s.manualBookLocks.LoadOrStore(*dl.BookID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
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
	//
	// The mode is per format (#1632): audiobooks can override import.mode, so
	// a CWA + Audiobookshelf library drops ebooks into CWA's ingest folder
	// (external) while audiobooks land in the audiobook root. With no override
	// both formats share import.mode and this is the same single read as
	// before.
	configuredMode := effectiveConfiguredMode(s.downloadImportMode(ctx, downloadPath, formatHint, explicitFiles), cleanupClientType)

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
		// Escape hatch for pair gating (#942): before handing off, release any
		// formats that have been held past the timeout waiting for a sibling
		// that never arrived. Evaluated here — on every external-mode tick —
		// rather than on a dedicated scheduled sweep, so held formats drain on
		// the next drop-folder activity after their deadline. No-op when nothing
		// is held.
		s.sweepHeldPairGating(ctx)
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
		s.createHistoryEvent(ctx, models.HistoryEventDownloadFolderImport, dl.Title, dl.BookID, dl.OwnerUserID, map[string]string{"mode": "external", "status": string(models.StateImportExternal)})
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
	//
	// An ebook file under MinPlausibleEbookBytes is left out: a release's
	// notes or link .txt is not a second format of the book (#3224). A manual
	// import keeps every file, as it is the override for a book that small.
	bookFiles, tooSmallFiles := discoverBookFilesSized(downloadPath, explicitFiles, !isManualImport(ctx))
	for _, f := range tooSmallFiles {
		slog.Info("skipping a file too small to be a book", "file", f, "minBytes", MinPlausibleEbookBytes)
	}

	if len(bookFiles) == 0 {
		// Before failing, check whether the book is already in the library. This
		// covers the case where a torrent was re-grabbed after its files were moved
		// to the library by a prior Bindery import (move mode): the download path is
		// empty but the book is already on disk. Marking it StateImported avoids
		// spurious failure noise and retry churn (#769). The check is scoped to
		// THIS download's format (#1885) — a freshly imported audiobook must not
		// close out the still-downloading ebook of the same title.
		if s.isBookAlreadyImported(ctx, dl, formatHint) {
			slog.Info("no book files at download path but book already in library — marking as imported",
				"title", dl.Title, "path", downloadPath)
			// Currently at StateImportPending (set at the top of this function).
			// Walk: importPending → importing → imported.
			s.updateDownloadStatus(ctx, dl.ID, models.StateImporting)
			s.updateDownloadStatus(ctx, dl.ID, models.StateImported)
			if cleanupFunc != nil {
				if err := cleanupFunc(); err != nil {
					slog.Warn("cleanup failed", cleanupWarnAttrs(cleanupClientType, cleanupRemoteID, err)...)
				}
			}
			return
		}
		// Everything the download held was too small to be a book. Retrying
		// cannot change that, so block it and point at the override.
		if len(tooSmallFiles) > 0 {
			s.blockTinyOnlyDownload(ctx, dl, downloadPath)
			return
		}
		// Distinguish "path doesn't exist on this host" from "path exists but has
		// no recognised book files".
		//
		// The message deliberately does NOT assert that PathRemap is the cause
		// (#1884). It named PathRemap outright, which is only one of three
		// reasons the path can be absent, and it was the wrong one for the
		// reporter: their mounts were identical and the real cause was that the
		// download had not finished, so qBittorrent's payload was still in its
		// temp/incomplete directory and the final save path had not been created
		// yet. A confident, wrong diagnosis sent them auditing a path mapping
		// that was correct. State the fact, then list the causes in the order
		// they actually occur.
		if _, statErr := os.Stat(downloadPath); os.IsNotExist(statErr) {
			slog.Warn("download path does not exist on this host — the download may not have finished, the files may have been moved or deleted, or PathRemap may be needed",
				"path", downloadPath, "title", dl.Title)
			s.failImport(ctx, dl, models.StateImportFailed,
				fmt.Sprintf("nothing at %q on this host — the download may still be finishing (qBittorrent writes to its temp/incomplete directory first), the files may have been moved or deleted since, or Bindery and the download client may see different filesystem roots (set PathRemap on the download client)", downloadPath))
			return
		}
		if archive := archiveAwaitingExtraction(downloadPath); archive != "" {
			// A RAR-packed release (#3203). The retries that follow wait for an
			// unpacker without spending attempts (importSourcePresent), and the
			// message says what the download is waiting for.
			slog.Info("download holds only an archive; waiting for it to be extracted",
				"path", downloadPath, "archive", filepath.Base(archive))
			s.failImport(ctx, dl, models.StateImportFailed,
				fmt.Sprintf("waiting for %s to be extracted: this download holds only an archive, and Bindery does not unpack archives. An unpacker such as unpackerr has to extract it into %q; the import runs once the files are there.", filepath.Base(archive), downloadPath))
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

	// Whether the grab named a book. Only such a download is content checked
	// (#3015, #3246): one matched from its own metadata just below agrees with
	// that book by construction.
	//
	// Known limit: the recovery below saves its match with SetBookID, and the
	// downloads row has no field saying the book was a guess, so on a Retry
	// import of such a download this is true and the saved book is checked as
	// if the grab had named it. Telling the two apart needs a column.
	grabbedForBook := dl.BookID != nil

	// Recover an association for downloads grabbed without a BookID (the
	// free-text Search page does not tie a release to a local book) before
	// falling through to the "unmatched" failure. Prefer the file's embedded
	// EPUB metadata (reliable) over the release filename (which encodes
	// author/title/series in inconsistent orders) — issue #1014.
	if book == nil {
		if b, a := s.matchBookForDownload(ctx, s.downloadMatchScope(ctx, dl), bookFiles, dl.Title); b != nil {
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

	// Post-download format enforcement (#1782).
	//
	// decision.QualityAllowed runs once, at grab time, against the release
	// *name*, and deliberately passes anything whose title carries no
	// recognisable format token. Nothing downstream ever re-checked, so the
	// case that filter admits it cannot judge is exactly the case that arrived
	// unchecked: the reported reproduction landed with no extension at all,
	// was a MOBI, the active profile disallowed mobi, and it imported anyway.
	//
	// Now there is a real file to look at. An explicit formatHint is the
	// override throughout, on the same principle as the video guard below: a
	// human declared the format, so their call wins.
	if formatHint == "" && len(bookFiles) > 0 {
		if reject, reason := s.rejectedDownloadFormat(ctx, author, bookFiles, detectedFormat); reject {
			// Record the path first so the queue's "Match to book" action can
			// still force this in, then blocklist so the next scan does not
			// grab the identical release and repeat the whole cycle.
			s.recordUnmatchedImportPath(ctx, dl.ID, downloadPath)
			s.blocklistRejectedRelease(ctx, dl, models.BlocklistScopeBook, reason)
			s.failImport(ctx, dl, models.StateImportBlocked, reason)
			return
		}
	}

	// Multi-book pack guard (#2276).
	//
	// A download row carries one BookID and both placement branches below
	// compute exactly one destination from it, so a release that is explicitly
	// several books has no correct import: every file in it lands in one
	// book's folder. The reported case was a four-book, 117-file audiobook
	// pack auto-grabbed for a single book.
	//
	// decision.MultiBookPackSpec now stops these being selected
	// automatically, but a pack can still reach here — hand-picked from
	// interactive search (where the spec is deliberately absent), grabbed by
	// an older version, or already sitting in the queue across the upgrade.
	// Block it before any filesystem write rather than after.
	//
	// Deliberately not blocklisted: unlike a format rejection, this release
	// may have been chosen on purpose, and blocklisting would fight the user
	// on their next attempt. The decision spec is what prevents an automatic
	// re-grab.
	//
	// formatHint is the override, on the same principle as the video guard: a
	// human driving manual import has already said what these files are.
	if formatHint == "" && book != nil {
		if marker := s.packMarker(ctx, dl.Title, book); marker != "" {
			slog.Warn("import blocked: release is a multi-book pack linked to one book",
				"title", dl.Title, "marker", marker, "bookID", book.ID, "book", book.Title)
			// Record the path so the queue's manual import can place the files
			// belonging to each book by hand, which is the only way a pack can
			// be imported correctly today.
			s.recordUnmatchedImportPath(ctx, dl.ID, downloadPath)
			s.failImport(ctx, dl, models.StateImportBlocked, fmt.Sprintf(
				"release is titled %q but is linked to the single book %q. Bindery cannot split a pack across book records, so nothing was imported. Use manual import to place each book's files",
				marker, book.Title))
			return
		}
	}

	// Post-download language enforcement (#2998). The release name filter
	// passes a name that does not say its language, so this is the first
	// point where the language is actually known: the EPUB's dc:language.
	// When it names a language the book's allowed languages exclude, the
	// release is the wrong one, handled exactly like a disallowed format
	// above: path recorded for Match to book, release blocklisted, download
	// blocked, book left Wanted, nothing placed and nothing relabelled. See
	// language_enforcement.go for what counts as allowed.
	//
	// A release that mixes allowed and disallowed EPUBs is not rejected: the
	// ebook loop below skips the disallowed ones (langCheck.skip), as it skips
	// a disallowed format.
	//
	// A manual import is never refused for its language: a person chose this
	// file for this book, and the warning in the log is all it gets. The
	// relabelling below then records the book's real language, as before.
	var langCheck languageCheck
	if detectedFormat != models.MediaTypeAudiobook && len(bookFiles) > 0 {
		langCheck = s.checkDownloadLanguage(ctx, book, author, bookFiles, detectedFormat)
		if isManualImport(ctx) {
			if langCheck.reject {
				slog.Warn("manual import of a file in a language the profile does not allow; importing it as asked",
					"title", dl.Title, "bookID", book.ID, "languages", langCheck.declared)
			}
			// A person picked these files; place them all.
			langCheck.skip = nil
		} else if langCheck.reject {
			slog.Warn("import blocked: file language is not allowed",
				"title", dl.Title, "bookID", book.ID, "languages", langCheck.declared)
			s.recordUnmatchedImportPath(ctx, dl.ID, downloadPath)
			s.blocklistRejectedRelease(ctx, dl, models.BlocklistScopeBook, langCheck.reason)
			s.failImport(ctx, dl, models.StateImportBlocked, langCheck.reason)
			return
		}
	}

	// Post-download content check, ebook half (#3015). The file's own dc:title
	// and dc:creator are compared with the book the download was grabbed for.
	// In block mode a definite mismatch is refused exactly like the language
	// check above; in warn mode (the default) the import goes ahead and the
	// finding rides on the bookImported event and webhook (contentCheck).
	// Skipped for a manual import, Match to book and a book matched from the
	// file itself. See content_check.go.
	var contentCheck map[string]any
	// An import that would upgrade the book's ebook (#2420) is held to a
	// stricter check below, and may later remove the file it replaces.
	var upgrade *ebookUpgrade
	var upgradeVerifiedFormats []string
	if detectedFormat != models.MediaTypeAudiobook && len(bookFiles) > 0 {
		upgrade = s.detectEbookUpgrade(ctx, dl, book, author, bookFiles, grabbedForBook)
	}
	if detectedFormat != models.MediaTypeAudiobook && len(bookFiles) > 0 && grabbedForBook && book != nil && !isManualImport(ctx) {
		res := s.checkEbookContent(ctx, book, author, bookFiles)
		upgradeVerifiedFormats = fileFormats(res.verifiedFiles)
		if res.flagged || res.data != nil {
			// A wrong-book upgrade must never replace a good file, so for an
			// upgrade any finding refuses the download whatever the mode, an
			// uncertain one included. Nothing is placed yet, so the owned
			// file is untouched.
			if res.blocks() || (upgrade != nil && res.flagged) {
				slog.Warn("import blocked: file looks like a different book",
					"title", dl.Title, "bookID", book.ID, "book", book.Title)
				s.recordUnmatchedImportPath(ctx, dl.ID, downloadPath)
				s.blocklistRejectedRelease(ctx, dl, models.BlocklistScopeBook, res.reason)
				s.failImportWithCheck(ctx, dl, models.StateImportBlocked, res.reason, res.data)
				return
			}
			if res.flagged {
				why := "importing it because import.ebook.content_check is warn"
				if res.uncertain && res.mode == contentCheckBlock {
					why = "importing it because the finding is uncertain and only noted, even in block mode"
				}
				slog.Warn("downloaded file looks like a different book; "+why,
					"title", dl.Title, "bookID", book.ID, "book", book.Title)
			}
			contentCheck = res.data
		}
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
		if existing := s.importedFormatPath(ctx, book, models.MediaTypeAudiobook); existing != "" {
			// A manual import is a person asking for these files to be
			// placed, not a retry of one that already was. Marking it
			// imported left every file where it was while the queue and
			// history said otherwise: the second to forty-fifth tracks of a
			// folder imported one row at a time all ended up there (#2935).
			if strings.HasPrefix(dl.GUID, ManualDownloadGUIDPrefix) {
				slog.Warn("manual audiobook import refused: the book already has an audiobook",
					"title", dl.Title, "bookID", book.ID, "existing", existing, "src", downloadPath)
				s.failImport(ctx, dl, models.StateImportBlocked, fmt.Sprintf(
					"this book already has an audiobook at %s, so nothing was imported and the files were left where they are. "+
						"To import an audiobook made of several files, import all of its tracks together in one batch; "+
						"to replace the existing one, delete its files first", existing))
				return
			}
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
		// Post-download language check, audiobook half (#3497): the same rule
		// and outcome as the EPUB check above, read from the tracks' language
		// tags. Only a clear majority of tracks in a disallowed language
		// rejects; untagged files never do. A manual import is never refused.
		if len(bookFiles) > 0 {
			if lc := s.checkAudiobookLanguage(ctx, book, author, bookFiles); lc.reject && isManualImport(ctx) {
				slog.Warn("manual import of audio files in a language the profile does not allow; importing them as asked",
					"title", dl.Title, "bookID", book.ID, "languages", lc.declared)
			} else if lc.reject {
				slog.Warn("import blocked: audio file language is not allowed",
					"title", dl.Title, "bookID", book.ID, "languages", lc.declared)
				s.recordUnmatchedImportPath(ctx, dl.ID, downloadPath)
				s.blocklistRejectedRelease(ctx, dl, models.BlocklistScopeBook, lc.reason)
				s.failImport(ctx, dl, models.StateImportBlocked, lc.reason)
				return
			}
		}
		// Post-download content check, audiobook half (#3246). The tags of the
		// download's audio files are read once and compared with the book; a
		// download that holds other works is refused in block mode (the same
		// path as the ebook half and the language check) and noted on the
		// bookImported event and webhook in warn mode, the default. It runs
		// after the idempotency guard above, so a download that was already
		// imported is not read again, and before any destination is computed
		// or file placed. Skipped for a manual import, Match to book and a
		// book matched from the file's own metadata. See content_check_audio.go.
		if grabbedForBook && !isManualImport(ctx) {
			if res := s.checkAudiobookContent(ctx, book, author, bookFiles); res.flagged || res.data != nil {
				if res.blocks() {
					slog.Warn("import blocked: audio tags show other books in the download",
						"title", dl.Title, "bookID", book.ID, "book", book.Title)
					s.recordUnmatchedImportPath(ctx, dl.ID, downloadPath)
					s.blocklistRejectedRelease(ctx, dl, models.BlocklistScopeBook, res.reason)
					s.failImportWithCheck(ctx, dl, models.StateImportBlocked, res.reason, res.data)
					return
				}
				if res.flagged {
					slog.Warn("downloaded audiobook holds other books; importing it because import.audiobook.content_check is warn",
						"title", dl.Title, "bookID", book.ID, "book", book.Title)
				}
				contentCheck = res.data
			}
		}
		// effectiveAudiobookDir resolves the per-author audiobook root folder
		// (#579), then the default audiobook root folder setting (#2166), and
		// falls back to BINDERY_AUDIOBOOK_DIR. It deliberately does NOT consult
		// the author's ebook RootFolderID or the ebook default: routing audiobooks
		// through that would send them into the ebook root whenever an author
		// has any custom ebook root folder assigned (#421).
		audiobookRoot := s.effectiveAudiobookDir(ctx, author)
		seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)
		audiobookDest, destErr := s.destRenamer(ctx).AudiobookDestDir(audiobookRoot, author, book, seriesTitle, seriesNum)
		if destErr != nil {
			slog.Error("failed to compute audiobook destination", "src", downloadPath, "error", destErr)
			s.failImport(ctx, dl, models.StateImportBlocked, fmt.Sprintf("audiobook destination invalid: %v", destErr))
			return
		}
		destDir := UniqueDir(audiobookDest)
		// Set by the plain whole-folder placement branch below when it
		// merges into the book's existing shared folder (#1959), so the
		// skips it declined to overwrite can be surfaced on the import's
		// History entry and notification rather than only in a log line.
		var mergeSkippedFiles []string
		// Whether the placement below merged into the book's own existing
		// folder rather than a folder of its own. Consulted by the
		// post-placement rollback, which must not treat a shared folder as
		// this import's to delete.
		var mergedIntoExistingFolder bool
		// Whether the audiobook source was a lone file rather than a folder:
		// a single file usenet job, whose job folder the placement below does
		// not move and so must be cleaned up afterwards (#3647).
		var audiobookSourceIsFile bool
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
			src, perFile := s.resolveAudiobookSource(downloadPath, bookFiles, isTorrentOwnFolder(ctx))
			usePerFile = perFile
			if !perFile {
				audiobookSource = src
			}
		}
		// A download folder that is itself a symlink is refused in every
		// mode: moving it would place the link, and copying or hardlinking
		// through it would import whatever it names. A torrent client's file
		// list reaches through such a link even though a plain walk would
		// not. Per-file placement is left alone: there audiobookSource is the
		// client's shared save path, which an operator may well reach
		// through a link of their own.
		if !usePerFile {
			if err := refuseSymlinkedDownloadDir(audiobookSource); err != nil {
				slog.Error("refusing audiobook import from a symlinked download folder", "path", audiobookSource, "mode", mode)
				s.failImport(ctx, dl, models.StateImportBlocked, fmt.Sprintf("audiobook %s refused: %v", mode, err))
				return
			}
			// A source folder that already sits in the library (a Fix match
			// of an audiobook folder, or a manual import pointed inside the
			// library) can hold a file another book tracks. Placing the
			// folder whole would carry that file to this book's folder while
			// its own row kept the old path (#3556), the same thing the
			// Reorganize guard refuses. Only move mode carries anything off:
			// copy and hardlink leave the source folder and everything in it
			// in place. A download folder holds no tracked files, so this
			// costs one empty lookup there. Fix match runs the same check
			// (FolderRelocationRefusal) before it detaches the source row, so
			// a refusal there never leaves the folder tracked by nobody; this
			// one is the backstop.
			if mode == "move" {
				if reason, err := s.heldFolderReason(ctx, audiobookSource, book.ID, dl.OwnerUserID); err != nil {
					slog.Error("failed to check the audiobook source for other books' files", "path", audiobookSource, "error", err)
					s.failImport(ctx, dl, models.StateImportFailed, fmt.Sprintf("check audiobook source: %v", err))
					return
				} else if reason != "" {
					slog.Warn("audiobook source folder holds a file another book tracks; refusing import", "path", audiobookSource)
					s.failImport(ctx, dl, models.StateImportBlocked, "audiobook move refused: "+reason)
					return
				}
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
			//
			// Collision preflight first (#2275): flattening by basename can
			// give two files from different source directories the same
			// destination, and the placement calls below would then lose one
			// of them (silently, in copy and move mode). Checking before the
			// MkdirAll means a collision blocks the import with nothing
			// written, rather than halfway through a folder.
			names, nameErr := s.perFileAudiobookNames(ctx, downloadPath, bookFiles, author, book, seriesTitle, seriesNum)
			if nameErr != nil {
				slog.Error("audiobook import blocked: per-file names", "src", audiobookSource, "dst", destDir, "error", nameErr)
				s.recordUnmatchedImportPath(ctx, dl.ID, downloadPath)
				s.failImport(ctx, dl, models.StateImportBlocked, nameErr.Error())
				return
			}
			if collErr := checkPerFileCollisionsNamed(destDir, bookFiles, names); collErr != nil {
				slog.Error("audiobook import blocked: per-file destination collision",
					"src", audiobookSource, "dst", destDir, "error", collErr)
				// Record the path so the queue's manual-import action can
				// still place these files against the right books by hand.
				s.recordUnmatchedImportPath(ctx, dl.ID, downloadPath)
				s.failImport(ctx, dl, models.StateImportBlocked, collErr.Error())
				return
			}
			if err := os.MkdirAll(destDir, dirperm.Library()); err != nil {
				dirErr = fmt.Errorf("create audiobook dest dir: %w", err)
			} else {
				// Basenames rather than full paths: rollbackPlacedFiles
				// removes them through an os.Root opened on destDir.
				placed := make([]string, 0, len(bookFiles))
				for i, srcFile := range bookFiles {
					dstFile := filepath.Join(destDir, names[i])
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
					placed = append(placed, names[i])
				}
				// An unexpected placement failure (a full disk, a revoked
				// permission) used to leave the already-placed files behind
				// in a folder the book never records, so a retry ran
				// UniqueDir and built a second partial "Title (2)". Undo this
				// attempt instead — but only when the sources still exist.
				// After a move they do not, and those files are the only
				// copy, so they stay put and the error tells the user where.
				//
				// The rollback runs even when nothing was placed (#2504).
				// Failing on the FIRST file leaves placed empty, and gating on
				// it skipped the cleanup for exactly the case where there is
				// nothing to weigh against removing the folder: the MkdirAll
				// above had already created it, so the empty directory
				// survived, and the next attempt's UniqueDir read it as a
				// collision and built "Title (2)" beside it. In move mode with
				// files already placed the folder still stays, since removing
				// it would take the only copy of them with it.
				if dirErr != nil {
					if mode == "move" && len(placed) > 0 {
						slog.Warn("audiobook move failed partway; placed files left in place because their sources are already gone",
							"dst", destDir, "placed", len(placed))
					} else {
						rollbackPlacedFiles(destDir, placed)
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
					namer := func(index, count int, ext string) string {
						// A folder holding one track is a single-file
						// audiobook too, so it gets the same name a lone
						// .m4b does on the branch below (#2900).
						if count == 1 {
							return s.renamer.AudiobookSingleFileName(tmpl, author, book, seriesTitle, seriesNum, strings.TrimPrefix(ext, "."))
						}
						return s.renamer.AudiobookFileName(tmpl, author, book, seriesTitle, seriesNum, strings.TrimPrefix(ext, "."), index+1)
					}
					slog.Info("renaming audiobook files per template", "src", audiobookSource, "dst", destDir, "mode", flattenMode, "template", tmpl)
					dirErr = flattenAudiobookDirFiltered(importCtx, flattenMode, audiobookSource, destDir, namer, flattenSidecarSkip(mode))
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
					dirErr = flattenAudiobookDirFiltered(importCtx, flattenMode, audiobookSource, destDir, partName, flattenSidecarSkip(mode))
					if dirErr == nil && mode == "move" && importCtx.Err() == nil {
						if rmErr := os.RemoveAll(audiobookSource); rmErr != nil {
							slog.Warn("flatten: could not remove source after move-mode flatten", "src", audiobookSource, "error", rmErr)
						}
					}
				} else if existingDir, merging := s.existingEbookDir(ctx, book); merging && s.pathKey(audiobookDest) == s.pathKey(existingDir) {
					// Shared-folder layout (#1959): this book's ebook already
					// occupies exactly the folder the audiobook destination
					// resolves to, so the collision UniqueDir disambiguated
					// above is this same book, not an unrelated one. Merge
					// into that folder instead, undoing the " (2)" suffix.
					//
					// Deliberately not extended to the flatten paths above,
					// though the single-file branch below shares it (#2686).
					// The flatten paths cannot take a pre-existing destDir:
					// they document and rely on having created it themselves,
					// and remove it wholesale (os.RemoveAll) to roll back any
					// error, which against a shared folder would delete the
					// ebook sitting in it. They keep the historical UniqueDir
					// behaviour, so a library using an audiobook naming
					// template or multi-disc flattening still splits into
					// "Title (2)" until flatten's rollback is reworked to only
					// remove what it placed. The per-file placement branch
					// (usePerFile) keeps it for the same reason: it flattens
					// several sources by basename into one folder, so a merge
					// there needs a per-file collision story of its own.
					destDir = existingDir
					mergedIntoExistingFolder = true
					slog.Info("merging audiobook into the book's existing shared folder",
						"title", book.Title, "bookID", book.ID, "dst", destDir, "mode", mode)
					switch mode {
					case "hardlink":
						mergeSkippedFiles, dirErr = HardlinkDirMerge(audiobookSource, destDir)
					case "copy":
						mergeSkippedFiles, dirErr = CopyDirMergeCtx(importCtx, audiobookSource, destDir)
					default:
						mergeSkippedFiles, dirErr = MoveDirMergeCtx(importCtx, audiobookSource, destDir)
					}
					if len(mergeSkippedFiles) > 0 {
						slog.Warn("audiobook merge skipped same-named file(s) already present in the shared folder",
							"title", book.Title, "bookID", book.ID, "dst", destDir, "skipped", mergeSkippedFiles)
					}
				} else {
					switch mode {
					case "hardlink":
						dirErr = HardlinkDir(audiobookSource, destDir)
					case "copy":
						dirErr = CopyDirCtx(importCtx, audiobookSource, destDir)
					default:
						dirErr = MoveDownloadDirCtx(importCtx, audiobookSource, destDir)
					}
				}
			} else {
				audiobookSourceIsFile = true
				// Shared-folder layout (#1959) for a source that is a single
				// FILE rather than a folder: a lone .m4b, which is what a
				// manual import of one audiobook file and a single-file
				// torrent both resolve to. The merge above sits on the folder
				// branch only, so this branch always kept UniqueDir's
				// " (2)" suffix even though the collision is this same book's
				// own folder (#2686). Unlike the flatten paths, nothing here
				// needs to own destDir: it places exactly one named file and
				// rolls back exactly that name, so merging into a folder that
				// already holds this book's ebook is safe.
				if existingDir, merging := s.existingEbookDir(ctx, book); merging && s.pathKey(audiobookDest) == s.pathKey(existingDir) {
					destDir = existingDir
					mergedIntoExistingFolder = true
					slog.Info("merging audiobook file into the book's existing shared folder",
						"title", book.Title, "bookID", book.ID, "dst", destDir, "mode", mode)
				}
				if err := os.MkdirAll(destDir, dirperm.Library()); err != nil {
					dirErr = fmt.Errorf("create audiobook dest dir: %w", err)
				} else {
					name := s.singleAudiobookFileName(ctx, author, book, seriesTitle, seriesNum, audiobookSource)
					if name != filepath.Base(audiobookSource) {
						slog.Info("renaming audiobook file per template", "src", audiobookSource, "dst", destDir, "name", name, "mode", mode)
					}
					dstFile := filepath.Join(destDir, name)
					// A merge never overwrites what is already in the book's
					// folder, the same contract CopyDirMergeCtx and friends
					// keep on the folder branch. A same-named file there is
					// skipped and reported rather than clobbered, and the
					// import still succeeds; in move mode the source stays put
					// because the skipped file's other copy is the only one.
					skipped := false
					if mergedIntoExistingFolder {
						if _, statErr := os.Stat(dstFile); statErr == nil {
							skipped = true
							mergeSkippedFiles = append(mergeSkippedFiles, name)
							slog.Warn("audiobook merge skipped a same-named file already present in the shared folder",
								"title", book.Title, "bookID", book.ID, "dst", destDir, "skipped", name)
						}
					}
					switch {
					case skipped:
						// nothing to place
					case mode == "hardlink":
						dirErr = HardlinkFile(audiobookSource, dstFile)
					case mode == "copy":
						dirErr = CopyFileCtx(importCtx, audiobookSource, dstFile)
					default:
						dirErr = MoveFileCtx(importCtx, audiobookSource, dstFile)
					}
					// A lone .m4b had no cleanup at all, so any failure here
					// left the directory MkdirAll had just made, and the next
					// attempt's UniqueDir built "Title (2)" beside the empty
					// original (#2504). Move mode passes no name: the file
					// either arrived or it did not, and a partial destination
					// whose source is already gone is not ours to delete.
					// rollbackPlacedFiles removes the folder through its
					// parent, which only succeeds while it is empty, so a
					// shared folder holding this book's ebook is safe either
					// way: the half-placed file goes, the folder and the ebook
					// in it stay.
					if dirErr != nil {
						if mode == "move" {
							rollbackPlacedFiles(destDir, nil)
						} else {
							rollbackPlacedFiles(destDir, []string{name})
						}
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
			var owned *db.PathOwnedError
			for attempt := 0; attempt < 3; attempt++ {
				if setErr = s.recordImportedFile(ctx, dl, book, models.MediaTypeAudiobook, destDir); setErr == nil {
					break
				}
				if ctx.Err() != nil || errors.As(setErr, &owned) {
					// A cancelled context won't recover, and neither will a
					// path another book holds (#2937); stop retrying.
					break
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
				switch {
				case owned != nil && !mergedIntoExistingFolder && (mode == "copy" || mode == "hardlink"):
					// Another book holds the folder's path (#2937). destDir
					// came from UniqueDir, so this import created it and it
					// holds only what was just placed: remove it, and name
					// the owner, since retrying will not change the answer.
					if rmErr := os.RemoveAll(destDir); rmErr != nil {
						slog.Warn("failed to remove audiobook destination after a path conflict", "dst", destDir, "error", rmErr)
					}
					s.failImport(ctx, dl, models.StateImportBlocked,
						fmt.Sprintf("audiobook not imported: %v", setErr))
				case owned != nil && mergedIntoExistingFolder:
					// Retrying cannot record a path another book holds, so
					// point at that book (setErr names it and Fix match)
					// rather than at a retry.
					s.failImport(ctx, dl, models.StateImportBlocked,
						fmt.Sprintf("audiobook merged into %s but not recorded: %v", destDir, setErr))
				case owned != nil:
					// Move mode: the source is gone, so a retry finds
					// nothing at the download path. Same pointer as above.
					s.failImport(ctx, dl, models.StateImportBlocked,
						fmt.Sprintf("audiobook moved to %s but not recorded: %v", destDir, setErr))
				case mergedIntoExistingFolder:
					// destDir is the book's own folder, shared with the
					// already-imported ebook, so the copy/hardlink rollback
					// below would delete that ebook. A merge never unwinds —
					// the same invariant CopyDirMergeCtx and friends
					// document — and it does not need to: the placed files
					// stay put and a retry is idempotent, because the merge
					// skips whatever is already there.
					s.failImport(ctx, dl, models.StateImportBlocked,
						fmt.Sprintf("audiobook merged into %s but could not be recorded (%v); the files are preserved there — retry the import to record them", destDir, setErr))
				case mode == "copy" || mode == "hardlink":
					if rmErr := os.RemoveAll(destDir); rmErr != nil {
						slog.Warn("failed to remove audiobook destination after DB error", "dst", destDir, "error", rmErr)
					}
					s.failImport(ctx, dl, models.StateImportBlocked,
						fmt.Sprintf("audiobook placed but could not be recorded (%v) — retry the import", setErr))
				default:
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

		// No Calibre delivery for an audiobook: the Calibre hand off takes
		// one ebook file (see enqueueCalibreDelivery).
		s.pushToABS(ctx)
		s.writeOPFSidecar(ctx, destDir, []string{audiobookRoot}, book, author, edition, seriesTitle, seriesNum)

		historyMeta := importHistoryProvenance(dl, map[string]any{"path": destDir, "format": models.MediaTypeAudiobook})
		if len(mergeSkippedFiles) > 0 {
			historyMeta["skippedFiles"] = strings.Join(mergeSkippedFiles, ", ")
		}
		s.fillCoverFromFile(ctx, book, destDir)
		s.createHistoryEventData(ctx, models.HistoryEventBookImported, dl.Title, dl.BookID, dl.OwnerUserID, withContentCheck(historyMeta, contentCheck))
		observeImportSuccess(dl, models.MediaTypeAudiobook)
		s.recordFixMatchMove(ctx, book, destDir)
		s.notify(ctx, notifierEventBookImported, withContentCheckPayload(importedPayload(book, dl, models.MediaTypeAudiobook, destDir, mergeSkippedFiles), contentCheck))
		// A folder source moved wholesale, so nothing is left of it. A lone
		// file moved out of a usenet job folder leaves that folder behind,
		// with whatever extras the release carried (#3647). A merge that
		// skipped a same-named file left that file in the source, so it is
		// left alone. The sweep prunes the emptied folder itself, behind its
		// client folder guard, so no cleanupMovedSources here.
		if mode == "move" && audiobookSourceIsFile && len(mergeSkippedFiles) == 0 && isUsenetClient(cleanupClientType) {
			s.sweepUsenetJobExtras(ctx, audiobookSource, []string{audiobookSource}, bookFiles)
		}
		if cleanupFunc != nil {
			if err := cleanupFunc(); err != nil {
				slog.Warn("cleanup failed", cleanupWarnAttrs(cleanupClientType, cleanupRemoteID, err)...)
			}
		}
		return
	}

	var imported, failed int
	// ownedFailed counts files refused because another book already tracks
	// their destination (#2937). A retry cannot fix those, so a partial
	// import whose only failures are these is blocked, not left retryable.
	var ownedFailed int
	var lastFileErr error
	// importedSrcFiles records the source path of every file that landed in the
	// library, so move-mode cleanup can delete exactly those files rather than
	// RemoveAll-ing the whole download path (issue #705 finding 4).
	var importedSrcFiles []string
	// detectedLang holds a language read from an embedded EPUB dc:language, used
	// after the loop to fill or correct the book's language (#1160, #1933).
	// Captured inside the loop because move mode consumes the source file on
	// commit, so it must be read while srcFile still exists.
	//
	// The tag is read on every import, not only when the catalogue left the
	// field empty. A provider describes a *work*, which has editions in many
	// languages; the file on disk is one specific edition, so when they
	// disagree the file is the one telling the truth (#1933). Only a user's own
	// edit outranks it, and that locks the field — in which case the EPUB is
	// not opened at all.
	var detectedLang string
	// sidecarDir records the folder a successfully imported ebook file landed
	// in, so the OPF sidecar can be written once after the loop rather than
	// once per file — and, critically, after applyEmbeddedLanguage below has
	// had a chance to backfill book.Language, so a first-time import of a
	// book with no catalogue language doesn't write a sidecar missing
	// dc:language moments before the DB gains one. All files of one book
	// share a destination directory (only the extension varies), so any
	// imported file's directory is the right one.
	var sidecarDir string
	// importedDestPaths records the library path of every ebook file this
	// download put in the library, so the single bookImported history event
	// written after the loop can name the formats that landed (#2764). The
	// history row used to be written inside the loop, which gave a three format
	// bundle three rows with the same title and the same second and nothing to
	// tell them apart. One download is one import event, the same rule the
	// bookImported notification below and the audiobook branch above already
	// follow. Files the idempotency guard counts are appended too: a previous
	// attempt placed them, they are in the library, so a retry's row still lists
	// every format the download delivered.
	var importedDestPaths []string
	readLanguage := book != nil && !book.IsFieldLocked(models.BookFieldLanguage)
	// Resolve the ebook destination root and (auto) placement mode once: the
	// root is stable for this author across the loop, and choosing hardlink-vs-
	// copy against it rather than s.libraryDir avoids a cross-device hardlink
	// failure when the author's RootFolderID is on a separate mount (#1254).
	ebookRoot := s.effectiveLibraryDir(ctx, author)
	ebookMode := s.resolveImportMode(configuredMode, downloadPath, ebookRoot)
	calibreQueued := false
	for _, srcFile := range orderByBookTitle(book, bookFiles) {
		if book == nil {
			// Try to match from filename
			parsed := ParseFilename(srcFile)
			slog.Info("unmatched import", "title", parsed.Title, "author", parsed.Author, "file", srcFile)
			continue
		}

		// Skip a file the quality profile disallows, so a mixed download
		// imports the format the user allowed and leaves the rest (#1782).
		// A download where EVERY file is disallowed never reaches this loop:
		// the gate above blocks and blocklists it, because that is the case
		// where importing anything would mean importing something unwanted.
		if formatHint == "" {
			if ok, reason := s.allowedFormat(ctx, author, formatsniff.Detect(srcFile), detectedFormat); !ok {
				slog.Info("skipping a file the quality profile disallows",
					"file", srcFile, "reason", reason)
				continue
			}
		}

		// Skip an EPUB in a language the profile does not allow when the same
		// download also carries one in an allowed language (#2998). A download
		// where every EPUB is disallowed never reaches this loop: the language
		// gate above blocks and blocklists it.
		if langCheck.skip[srcFile] {
			slog.Info("skipping a file in a language the profile does not allow",
				"file", srcFile)
			continue
		}

		// Read the embedded EPUB language while the source is still present
		// (move mode deletes it on commit). Only when we actually intend to
		// backfill, so we never open the zip needlessly. Under a restricted
		// profile the first allowed declared language wins (relabelLanguage).
		if readLanguage && detectedLang == "" && HasEmbeddedMetadata(srcFile) {
			if meta, err := ReadEbookMetadata(srcFile); err == nil && meta.Language != "" {
				detectedLang = langCheck.relabelLanguage(meta)
			}
		}

		seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)
		destPath, destErr := s.destRenamer(ctx).DestPath(ebookRoot, author, book, seriesTitle, seriesNum, srcFile)
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
			importedDestPaths = append(importedDestPaths, destPath)
			continue
		}
		destPath, destErr = s.uniqueEbookDestination(ctx, book, destPath)
		if destErr != nil {
			slog.Error("failed to find unused book destination", "src", srcFile, "error", destErr)
			lastFileErr = destErr
			failed++
			continue
		}
		// A prior attempt may have selected a suffix and completed just this
		// file before it stopped. The deterministic base-name check above
		// cannot see that row, so repeat the idempotency guard after collision
		// resolution rather than re-copying the existing suffixed file.
		if s.alreadyImportedPath(ctx, book, destPath) {
			slog.Info("book file already imported at collision-safe destination — skipping re-import",
				"src", srcFile, "dst", destPath)
			imported++
			importedDestPaths = append(importedDestPaths, destPath)
			continue
		}

		// A destination below a folder another book tracks (an audiobook is
		// tracked as its whole folder) is not this book's to write to: a later
		// Reorganize of that folder would carry the file away while this book's
		// row kept the old path. A retry lands in the same place, so block it.
		if under, err := s.books.PathUnderLiveOtherBookDir(ctx, destPath, book.ID); err != nil {
			slog.Error("failed to check destination ownership", "dst", destPath, "error", err)
			lastFileErr = fmt.Errorf("check destination ownership: %w", err)
			failed++
			continue
		} else if under {
			slog.Warn("destination is inside a folder another book tracks — refusing import", "src", srcFile, "dst", destPath)
			lastFileErr = fmt.Errorf("%s is inside a folder tracked by another book; remove the duplicate book or change the naming template so the two books do not share a folder", destPath)
			failed++
			ownedFailed++
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
		//
		// A destPath another book already tracks fails here (#2937), before
		// commit, so the staged file is discarded and the file that book
		// tracks at destPath is never replaced.
		if err := s.recordImportedFile(ctx, dl, book, models.MediaTypeEbook, destPath); err != nil {
			slog.Error("failed to record book file — rolling back staged file", "bookID", book.ID, "staged", stagedPath, "error", err)
			rollback()
			failed++
			lastFileErr = fmt.Errorf("record book file: %w", err)
			var owned *db.PathOwnedError
			if errors.As(err, &owned) {
				ownedFailed++
			}
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
		importedDestPaths = append(importedDestPaths, destPath)
		sidecarDir = filepath.Dir(destPath)
		// NOTE: StateImported is intentionally NOT set here (issue #705 finding 1).
		// Writing the terminal "imported" state after the first successful file
		// would mark an incomplete multi-file download as fully imported; a later
		// file failing would then leave a terminal-imported but partial download,
		// and move-mode cleanup would delete the source of the file that never
		// landed. The terminal state is decided once, after the loop.
		slog.Info("book imported", "title", book.Title, "path", destPath)
		s.fillCoverFromFile(ctx, book, destPath)

		if s.enqueueCalibreDelivery(ctx, book, dl, edition, destPath) {
			calibreQueued = true
		}
		s.pushToCWA(ctx, destPath)
		s.pushToGrimmory(ctx, book, destPath)

		// NOTE: the bookImported history event is deliberately NOT written here
		// (#2764). It is written once after the loop, beside the notification,
		// for the reason stated there.
	}

	// Wake the Calibre delivery worker once every file of this download is
	// queued. It delivers in the background, so the import never waits on
	// Calibre; if Calibre is closed the rows wait for the next pass.
	if calibreQueued {
		s.calibreQueue.Kick()
	}

	// Reconcile the book's language with the file that just landed (#1160,
	// #1933). Gated on at least one imported file: a failed import must not
	// rewrite the catalogue from a file that is not in the library.
	if readLanguage && imported > 0 && detectedLang != "" {
		s.applyEmbeddedLanguage(ctx, book, detectedLang, dl.Title)
	}

	// Written once here, after applyEmbeddedLanguage above, rather than once
	// per file inside the loop: a sidecar written mid-loop could carry a
	// blank dc:language that the backfill above fills in moments later, and
	// a multi-file download (epub + mobi + pdf sharing one folder) would
	// otherwise regenerate the identical sidecar once per format.
	if imported > 0 && sidecarDir != "" {
		seriesTitle, seriesNum := s.primarySeriesFor(ctx, book)
		s.writeOPFSidecar(ctx, sidecarDir, []string{ebookRoot}, book, author, edition, seriesTitle, seriesNum)
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
		status := models.StateImportFailed
		if ownedFailed == failed {
			// Every failure is a file another book holds (#2937), which a
			// retry would only hit again: block it, as the single file case
			// does, so it does not spend the retry budget. lastFileErr names
			// the owner.
			status = models.StateImportBlocked
		}
		s.failImport(ctx, dl, status, reason)
		return
	}

	// A clean run (imported > 0, failed == 0): every book file landed. Mark the
	// download imported exactly once, then clean up.
	if imported > 0 && failed == 0 {
		s.updateDownloadStatus(ctx, dl.ID, models.StateImported)
		// One bookImported history row and one bookImported notification per
		// download (not per file): a multi-format ebook bundle (epub + mobi +
		// pdf) is conceptually one import event from the user's perspective.
		//
		// Only the clean run writes the row. A partial import (some files
		// landed, some did not) is not an import from the user's side: it is
		// left retryable and failImport already records an importFailed row
		// saying how many files failed. A later retry re-walks the same files,
		// counts the ones already placed through the idempotency guard, and
		// lands here, so the download still ends up with exactly one
		// bookImported row naming every format.
		importData := importHistoryProvenance(dl, ebookImportHistoryData(importedDestPaths))
		var replaced []string
		if upgrade != nil {
			replaced = s.removeReplaced(ctx, book, upgrade, importedDestPaths, upgradeVerifiedFormats)
			importData["upgrade"] = true
			importData["replaced"] = append([]string{}, replaced...)
			importData["previous"] = append([]string(nil), upgrade.existing...)
		}
		s.createHistoryEventData(ctx, models.HistoryEventBookImported, dl.Title, dl.BookID, dl.OwnerUserID, withContentCheck(importData, contentCheck))
		observeImportSuccess(dl, models.MediaTypeEbook)
		if importPath, ok := importData["path"].(string); ok {
			s.recordFixMatchMove(ctx, book, importPath)
		}
		if upgrade != nil {
			// An upgrade is announced as an upgrade, not also as an import, so
			// a notification set to both does not fire twice for one book.
			payload := importedPayload(book, dl, upgradeFormat(importedDestPaths), "", nil)
			payload["replacedFiles"] = len(replaced)
			s.notify(ctx, notifierEventUpgrade, withContentCheckPayload(payload, contentCheck))
		} else {
			s.notify(ctx, notifierEventBookImported, withContentCheckPayload(importedPayload(book, dl, models.MediaTypeEbook, "", nil), contentCheck))
		}

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
			// A usenet job's own folder is not a shared save root and
			// nothing seeds from it, so the release extras the book came
			// with (.nfo, cover scans, par2) go too, under the guards in
			// sweepUsenetJobExtras (#3647). Never for a torrent.
			if isUsenetClient(cleanupClientType) {
				s.sweepUsenetJobExtras(ctx, downloadPath, importedSrcFiles, bookFiles)
			}
		}
		if cleanupFunc != nil {
			if err := cleanupFunc(); err != nil {
				slog.Warn("cleanup failed", cleanupWarnAttrs(cleanupClientType, cleanupRemoteID, err)...)
			}
		}
	}
}
