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
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// ErrScanAlreadyRunning was StartScan's answer to a request made while a
// library scan was in flight (#1460). Since #3014 such a request is queued and
// StartScan returns ErrScanQueued; this stays for callers that still match it.
var ErrScanAlreadyRunning = errors.New("library scan already running")

// ErrScanQueued is returned by StartScan when a scan is already in flight. The
// request is not dropped: the running scan runs one more scan as soon as it
// finishes, so a file placed in a folder the walk had already passed is still
// picked up (#3014). Any number of requests made during one scan coalesce into
// that single follow-up.
var ErrScanQueued = errors.New("library scan queued behind the running scan")

// ErrShuttingDown is returned by StartScan when the scan could not be launched
// because the process is already draining its background jobs. The single-flight
// gate is released before it is returned, so a restarted process starts clean.
// Matches hardcoverlistsyncer.ErrShuttingDown.
var ErrShuttingDown = errors.New("server is shutting down")

// claimScan takes the single-flight gate. When a scan already holds it,
// claimScan reports false and, if queue is set, records that one more scan is
// wanted once the running one finishes.
func (s *Scanner) claimScan(queue bool) bool {
	ok, _ := s.claimScanID(queue)
	return ok
}

// scanBootID makes scan ids unique across restarts, since the stored result
// outlives the process and the sequence restarts at 1.
var scanBootID = strconv.FormatInt(time.Now().UnixNano(), 36)

func scanIDFor(seq int64) string {
	return scanBootID + "-" + strconv.FormatInt(seq, 10)
}

// claimScanID is claimScan that also names the scan whose result will answer
// this request: the next one to start, whether that is the scan this call
// starts or the follow-up it queues behind the running one.
func (s *Scanner) claimScanID(queue bool) (bool, string) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	next := scanIDFor(s.scanSeq + 1)
	if s.scanRunning.CompareAndSwap(false, true) {
		return true, next
	}
	if queue {
		s.scanPending = true
	}
	return false, next
}

// beginScan numbers the scan about to walk, under the gate.
func (s *Scanner) beginScan() {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.scanSeq++
	s.scanID = scanIDFor(s.scanSeq)
}

// currentScanID is the id of the scan running or last run, "" before any.
func (s *Scanner) currentScanID() string {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	return s.scanID
}

// ScanState reports whether a library scan is walking and whether another is
// queued behind it, for the scan status endpoint (#3014).
func (s *Scanner) ScanState() (running, queued bool) {
	if s == nil {
		return false, false
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	return s.scanRunning.Load(), s.scanPending
}

// finishScan is called by the scan holding the gate when its walk is done. It
// keeps the gate and reports true when a scan was queued meanwhile, so the
// caller runs the follow-up; otherwise it releases the gate. A cancelled
// context drops the queued request, since the process is shutting down.
func (s *Scanner) finishScan(ctx context.Context) bool {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanPending && ctx.Err() == nil {
		s.scanPending = false
		return true
	}
	s.scanPending = false
	s.scanRunning.Store(false)
	return false
}

// releaseScan drops the gate and any queued request without running anything.
func (s *Scanner) releaseScan() {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.scanPending = false
	s.scanRunning.Store(false)
}

// runScans runs a scan, then the one follow-up a request made during it asked
// for, and releases the gate. The caller must hold the gate. The deferred
// release covers a scan that panics, which used to be covered by a deferred
// Store(false).
func (s *Scanner) runScans(ctx context.Context) {
	released := false
	defer func() {
		if !released {
			s.releaseScan()
		}
	}()
	for {
		s.beginScan()
		s.scanLibrary(ctx)
		if !s.finishScan(ctx) {
			released = true
			return
		}
		slog.Info("library scan: running the scan requested while the last one was in flight")
	}
}

// StartScan launches a library scan in the background and returns
// immediately. If a scan is already in flight it does not start a second walk
// beside it (#1460): it queues one follow-up scan and returns ErrScanQueued,
// so callers (the manual-scan endpoint) can say the request was accepted
// (#3014). Callers pass context.WithoutCancel(r.Context()) so the HTTP
// response-send doesn't cancel the scan.
func (s *Scanner) StartScan(ctx context.Context) error {
	_, err := s.StartScanTracked(ctx)
	return err
}

// StartScanTracked is StartScan that also returns the id the requested scan's
// result will carry as scan_id in library.lastScan, so a caller can tell its
// own scan's result from an earlier one without comparing clocks (#3014).
// The id is returned with ErrScanQueued too, naming the follow-up scan.
func (s *Scanner) StartScanTracked(ctx context.Context) (string, error) {
	claimed, id := s.claimScanID(true)
	if !claimed {
		return id, ErrScanQueued
	}
	// When a jobs group is wired, the scan runs on the shutdown-scoped context
	// so SIGTERM cancels and drains it before the DB closes, instead of the
	// never-cancelled WithoutCancel(request) context. Fall back to an untracked
	// goroutine for tests/non-wired callers (#1458).
	if s.jobs != nil {
		// Go is a documented no-op once the group has begun shutting down, and
		// the single-flight gate is already claimed above. Ignoring the return
		// left scanRunning true for the rest of the process's life and answered
		// the manual-scan endpoint with success for a scan that never ran
		// (#2372).
		if !s.jobs.Go("library-scan", s.runScans) {
			s.releaseScan()
			return "", ErrShuttingDown
		}
	} else {
		go s.runScans(ctx)
	}
	return id, nil
}

// ScanLibrary runs a library scan synchronously, sharing the single-flight
// guard with StartScan: if a scan is already in flight (e.g. a manual scan
// racing the 6-hourly cron job) the call is skipped with a log line. The
// cron scheduler's SkipIfStillRunning only guards cron-vs-cron, so this is
// what prevents cron-vs-manual overlap (#1460). A manual request made while
// this scan runs is queued, and runs here once the walk finishes (#3014).
func (s *Scanner) ScanLibrary(ctx context.Context) {
	if !s.claimScan(false) {
		slog.Info("library scan already running; skipping")
		return
	}
	s.runScans(ctx)
}

// walkRoot is filepath.Walk for a configured library or audiobook root that
// may itself be a symlink (/books -> /mnt/storage/books, or a container
// volume path that is a link). filepath.Walk Lstats its root, so a linked
// root is reported once as a link and never entered, and a scan of it found
// nothing. walkRoot resolves the root first and walks the target, then hands
// fn every path rewritten under the configured root, so what a caller
// reports, compares or stores is in the same form as the book_files rows
// imports write and the root the serving containment checks resolve.
//
// Only the root is resolved. Entries inside it are still Lstat'ed and a
// linked folder inside the library is reported, not entered, exactly as
// filepath.Walk does. A root that cannot be resolved (missing, unreadable)
// is walked as given, so fn sees the same error it always did.
//
// Folders that are not library content are not entered (see skipLibraryDir):
// a hidden folder, a folder holding a .binderyignore file, and the excluded
// folders, which are the download folders when a client saves inside a
// library root. The root itself is always walked.
func walkRoot(root string, excluded []string, fn filepath.WalkFunc) error {
	skip := libraryDirSkipper(root, excluded)
	visit := func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && skip(path) {
			return filepath.SkipDir
		}
		return fn(path, info, err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved == filepath.Clean(root) {
		return filepath.Walk(root, visit)
	}
	return filepath.Walk(resolved, func(path string, info os.FileInfo, err error) error {
		if rel, relErr := filepath.Rel(resolved, path); relErr == nil {
			path = filepath.Join(root, rel)
		}
		return visit(path, info, err)
	})
}

// LibraryIgnoreFile marks a folder the library scan leaves alone, with
// everything under it, like Audiobookshelf's .ignore and Plex's .plexignore.
const LibraryIgnoreFile = ".binderyignore"

// junkDirNames are folder names, matched exactly and case-insensitively, that
// hold no library content: trash, torrent and incomplete-download folders,
// macOS and Windows volume metadata, NAS thumbnail and recycle folders, file
// system snapshots, sync tool state and version control.
var junkDirNames = map[string]bool{
	".torrents":               true,
	".trashes":                true,
	".incomplete":             true,
	".appledouble":            true,
	".appledb":                true,
	".spotlight-v100":         true,
	".fseventsd":              true,
	".temporaryitems":         true,
	".documentrevisions-v100": true,
	".thumbnails":             true,
	".stfolder":               true,
	".stversions":             true,
	".sync":                   true,
	".git":                    true,
	".svn":                    true,
	".recycle":                true,
	".snapshot":               true,
	// NAS and file system folders a library on a NAS share can hold:
	// Synology's thumbnail and recycle folders, QNAP's thumbnails, ZFS and
	// btrfs (snapper) snapshots, which can hold whole copies of the library,
	// and fsck's lost+found.
	"@eadir":     true,
	"#recycle":   true,
	".@__thumb":  true,
	".zfs":       true,
	".snapshots": true,
	"lost+found": true,
}

// junkDirPrefixes are folder name prefixes of the same kind: a per-user trash
// (".Trash-1000"), Bindery's own staging folders and Syncthing's temporary
// folders.
var junkDirPrefixes = []string{".trash-", ".bindery-stage-", ".syncthing."}

// JunkLibraryDirName reports whether a folder name is on the deny-list of
// folders that are never library content. It looks at the name only, so it
// is safe to call on a path built from a request and costs no filesystem
// access. It is deliberately not "starts with a dot": Bindery's own naming
// keeps leading dots, so "...And Then There Were None", ".hack Sign" and an
// author folder ".38 Special" are real library folders. Any other dot folder
// can be left out with a LibraryIgnoreFile.
func JunkLibraryDirName(name string) bool {
	lower := strings.ToLower(name)
	if junkDirNames[lower] {
		return true
	}
	for _, p := range junkDirPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// SkipLibraryDir reports whether a folder found while walking a library is
// not library content: one on the JunkLibraryDirName deny-list (".torrents",
// ".Trash-1000", a client's ".incomplete"), or one holding a LibraryIgnoreFile.
// Torrents saved into a hidden folder inside the library are the case this
// exists for: their copies would otherwise be scanned as unmatched books, one
// click from being imported or moved out of the client's folder.
func SkipLibraryDir(dir string) bool {
	if JunkLibraryDirName(filepath.Base(dir)) {
		return true
	}
	_, err := os.Lstat(filepath.Join(dir, LibraryIgnoreFile))
	return err == nil
}

// libraryDirSkipper returns the folder test walkRoot applies below root:
// SkipLibraryDir, plus the excluded folders, compared as given and resolved
// so a download folder named through a symlink is still recognised.
func libraryDirSkipper(root string, excluded []string) func(string) bool {
	root = filepath.Clean(root)
	skipped := make(map[string]bool, 2*len(excluded))
	for _, dir := range excluded {
		if dir == "" {
			continue
		}
		skipped[filepath.Clean(dir)] = true
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			skipped[resolved] = true
		}
	}
	return func(dir string) bool {
		dir = filepath.Clean(dir)
		if dir == root {
			return false
		}
		if skipped[dir] || SkipLibraryDir(dir) {
			return true
		}
		if len(skipped) > 0 {
			if resolved, err := filepath.EvalSymlinks(dir); err == nil && skipped[resolved] {
				return true
			}
		}
		return false
	}
}

// excludedLibraryDirs is what the library walks leave out besides hidden and
// ignored folders: the download folders, so a client saving inside a library
// root (a non-hidden layout such as /books/downloads) is not scanned as
// library content.
func (s *Scanner) excludedLibraryDirs() []string {
	var out []string
	for _, dir := range []string{s.downloadDir, s.audiobookDownloadDir} {
		if dir != "" {
			out = append(out, dir)
		}
	}
	return out
}

// refreshStaleRenderedPaths re-derives the file path shown for books that
// track more than one file of the same format and are currently rendering one
// that has vanished (#2186).
//
// The derivation fix in BookRepo lands whenever a file is registered or
// removed, which covers every future move. It does not reach a library that
// broke before the fix shipped: the moved file is already tracked, so the scan
// records it as such and never calls AddBookFile, and nothing else recomputes
// the book. That is why "no number of rescans corrects it" was in the report.
// This sweep is what makes a rescan correct it.
//
// It is scoped to stay cheap on a large library: only books with a same-format
// sibling can have had a live row outranked by a dead one, and each candidate
// costs at most two os.Stat calls. A book whose rendered paths still resolve
// is skipped without a write, so a healthy library does no database work here
// at all. Nothing is deleted — the stale row stays on the book's Files tab for
// the user to deregister.
//
// Best-effort: a scan that has already done its real work must not fail here.
func (s *Scanner) refreshStaleRenderedPaths(ctx context.Context) {
	candidates, err := s.books.MultiFileBookPaths(ctx)
	if err != nil {
		slog.Warn("library scan: could not list multi-file books for stale-path sweep", "error", err)
		return
	}
	repaired := 0
	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		stale := (c.EbookPath != "" && !db.BookFilePathResolves(c.EbookPath)) ||
			(c.AudiobookPath != "" && !db.BookFilePathResolves(c.AudiobookPath))
		if !stale && c.EbookPath != "" {
			// A rendered path that still resolves can be the wrong one: books
			// imported before the format preference (#3467) render whichever
			// format came first. Only multi-file books reach this point, so
			// the extra lookup is not paid by the single-file majority, and a
			// book already rendering its preferred file is not written.
			want, err := s.books.DerivedEbookPath(ctx, c.BookID)
			if err != nil {
				slog.Warn("library scan: could not derive preferred ebook path",
					"book_id", c.BookID, "error", err)
				continue
			}
			stale = want != "" && want != c.EbookPath
		}
		if !stale {
			continue
		}
		if err := s.books.RefreshBookStatus(ctx, c.BookID); err != nil {
			slog.Warn("library scan: could not refresh book after stale path",
				"book_id", c.BookID, "error", err)
			continue
		}
		repaired++
	}
	if repaired > 0 {
		slog.Info("library scan: re-derived file paths for books rendering a vanished file",
			"books", repaired)
	}
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
// chapterTitleRe matches embedded-tag titles that name a track/chapter rather
// than the book: a leading track number followed by a dash/dot/underscore and a
// NON-digit ("04 - Title", "04. Title", "04_Title"), or a Chapter/Track/Part/
// Disc/CD keyword followed by a number.
//
// The shape is deliberately narrow to avoid discarding real numeric titles:
//   - the {1,3}-digit cap skips years ("1984", "2001: A Space Odyssey");
//   - the colon is NOT a separator, so subtitle titles survive ("24: Live
//     Another Day", "7: Seven");
//   - requiring a non-digit after the separator skips number-dash/dot-number
//     titles ("1-800 Where R You", "3.14 …").
//
// A residual ambiguity remains: a genuinely numbered title ("21 - Jump Street",
// "8 - Mile") is indistinguishable from "04 - Chapter Name" without sibling or
// semantic context, so it is treated as a chapter. The blast radius is small —
// this only suppresses the tag title when the folder hierarchy already resolved
// a title to fall back to (see the caller), so the worst case is using the
// folder's title for such a book rather than the tag's.
// The last branch is a bare track counter with nothing else, "001-190" or
// "07 of 12": some rips tag every track's title that way, and letting it win
// over a folder-derived book title left every file unmatched (#2547).
var chapterTitleRe = regexp.MustCompile(`(?i)^(\d{1,3}\s*[-._]\s*\D|(chapter|track|part|disc|cd)\b\s*\.?\s*\d|\d{1,4}\s*(?:-|/|of)\s*\d{1,4}$)`)

// looksLikeChapterTitle reports whether an embedded-tag title looks like a
// per-track chapter name rather than a book title (#1239).
func looksLikeChapterTitle(title string) bool {
	return chapterTitleRe.MatchString(strings.TrimSpace(title))
}

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
	// A dual-format book still missing a format on disk is a candidate even
	// though its other format is present, so the scan can attach the missing
	// edition (#1148). Without this, a 'both' book with the audiobook imported
	// but the ebook absent would never reconcile the ebook, because the loop
	// below short-circuits the moment any one path resolves.
	if b.NeedsEbook() || b.NeedsAudiobook() {
		return true
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

// Reasons a scanned file stayed unmatched, persisted with each unmatched entry
// so the UI can give advice that fits the actual failure instead of always
// blaming the author's book catalogue (#1958). These strings are a wire
// contract with the frontend — add cases, don't rename them.
const (
	// unmatchedReasonAuthorNotInLibrary: the parsed author matched no author in
	// the library at all, so no book could even be considered. The file's tags
	// or folder name are the thing to look at — refreshing an author cannot
	// help. This is the case that cost the #1958 reporter two weeks.
	unmatchedReasonAuthorNotInLibrary = "author_not_in_library"
	// unmatchedReasonNoCandidateBooks: the author matched, but has no book that
	// the scan can attach a file to (no books at all, or every book already has
	// its files on disk). Populating the author's catalogue is the right advice
	// here — this is #875's case.
	unmatchedReasonNoCandidateBooks = "no_candidate_books"
	// unmatchedReasonNoTitleMatch: candidate books existed but no title cleared
	// the fuzzy-match gate (or the file sits outside the author's library root).
	unmatchedReasonNoTitleMatch = "no_title_match"
	// unmatchedReasonNoTitleParsed: nothing in the path parsed as a title, so
	// the title tier never ran. Reporting no_title_match here told the user "no
	// book by that author matched this title" when there was no title to match
	// — the file needs renaming, not a catalogue refresh.
	unmatchedReasonNoTitleParsed = "no_title_parsed"
	// unmatchedReasonTooSmall: an ebook format file under
	// MinPlausibleEbookBytes, a notes or readme file rather than a book
	// (#2944). It is listed so the user can see and ignore it, but it gets no
	// suggestions, its own row, and adoption refuses it.
	unmatchedReasonTooSmall = "too_small"
)

// writeScanError persists a failed-scan result so the UI reflects the failure
// instead of a stale prior scan (#965). It reuses the same "library.lastScan"
// shape as a normal scan — zero counts plus a non-empty scan_error message the
// frontend renders the same way as the other scan-outcome warnings.
func (s *Scanner) writeScanError(ctx context.Context, message string) {
	s.writeScanResultWithError(ctx, 0, 0, 0, 0, 0, s.unmatchedUnitCounts(ctx, false), message)
}

// writeScanResult persists the scan summary to the settings table under
// "library.lastScan" so the UI can surface the result without polling logs.
// The key is written as a string literal to avoid an import cycle; it is
// api.SettingLibraryLastScan on the read side, where the settings endpoints
// reserve it for admins because the blob carries absolute paths (#2361). Keep
// the two spellings in sync.
func (s *Scanner) writeScanResult(ctx context.Context, filesFound, reconciled, unmatched, alreadyTracked, tagReadFailed int, units unitCounts) {
	s.writeScanResultWithError(ctx, filesFound, reconciled, unmatched, alreadyTracked, tagReadFailed, units, "")
}

// writeScanResultWithError is the shared writer for both successful scans and
// early-return failures. scanError is empty for a normal scan and a
// user-facing message when the scan could not complete (#965).
func (s *Scanner) writeScanResultWithError(ctx context.Context, filesFound, reconciled, unmatched, alreadyTracked, tagReadFailed int, units unitCounts, scanError string) {
	if s.settings == nil {
		return
	}

	// Surface the resolved roots that were actually walked so the UI can tell
	// the user WHICH paths produced the file count. The scanner unions the
	// audiobook root with the library root only when it differs (see
	// ScanLibrary), so mirror that logic here.
	scannedPaths := []string{s.libraryDir}
	if s.audiobookDir != "" && s.audiobookDir != s.libraryDir {
		scannedPaths = append(scannedPaths, s.audiobookDir)
	}
	pathsJSON := "[]"
	if bytes, err := json.Marshal(scannedPaths); err == nil {
		pathsJSON = string(bytes)
	}

	// noFilesFound is the explicit zero-files signal: the scan walked the
	// configured roots and found nothing. This distinguishes "wrong/empty
	// path" from "all files matched" (both can show files_found semantics the
	// user misreads) and lets the UI name the offending directory.
	noFilesFound := filesFound == 0

	// scan_error is "" for a successful scan and a user-facing message when the
	// scan could not complete (#965). Marshalled separately so any quoting in
	// the message (e.g. an underlying error string) is escaped correctly.
	scanErrorJSON := "\"\""
	if scanError != "" {
		if b, err := json.Marshal(scanError); err == nil {
			scanErrorJSON = string(b)
		}
	}

	// The unmatched files now live in unmatched_units, one row per book, and
	// the blob carries only their counts. unmatched_files stays as an empty
	// list so a web bundle cached from before library adoption still parses
	// the result. Added 2026-09 for v1.37; remove it in the release after.
	payload := fmt.Sprintf(
		`{"ran_at":%q,"scan_id":%q,"files_found":%d,"reconciled":%d,"unmatched":%d,"already_tracked":%d,"tag_read_failed":%d,"unmatched_files":[],"unmatched_units":%d,"ignored_units":%d,"units_truncated":%t,"library_dir":%q,"audiobook_dir":%q,"scanned_paths":%s,"no_files_found":%t,"scan_error":%s}`,
		time.Now().UTC().Format(time.RFC3339), s.currentScanID(),
		filesFound, reconciled, unmatched, alreadyTracked, tagReadFailed,
		units.pending, units.ignored, units.truncated,
		s.libraryDir, s.audiobookDir, pathsJSON, noFilesFound, scanErrorJSON,
	)
	if err := s.settings.Set(ctx, "library.lastScan", payload); err != nil {
		slog.Warn("library scan: failed to persist scan result", "error", err)
	}
}
