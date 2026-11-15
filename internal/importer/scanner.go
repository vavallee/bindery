// Package importer moves completed downloads into the configured library
// directory using a configurable naming template, and reconciles pre-existing
// library files against the tracked book database.
package importer

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/importer/formatsniff"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/models"
)

// grimmoryPusher mirrors a just-imported ebook into Grimmory's BookDrop (#826).
// Narrow local interface, same pattern as calibreAdder below.
type grimmoryPusher interface {
	PushOnImport(ctx context.Context, bookID int64, title, filePath string)
}

// calibreDeliveryQueue queues a just-imported ebook file for Calibre and
// wakes the worker that delivers it (#2832). *calibre.Deliverer implements it.
type calibreDeliveryQueue interface {
	Enqueue(ctx context.Context, bookID, bookFileID int64, editionID *int64, path string) (bool, error)
	Kick()
}

// absNotifier is called after a successful audiobook import to trigger an
// Audiobookshelf library scan so the new item is surfaced promptly rather than
// waiting for the next scheduled ABS scan (which defaults to every 24 h).
// Failures are best-effort — the import is never rolled back on ABS errors.
type absNotifier interface {
	ScanLibrary(ctx context.Context, libraryID string) error
}

// eventNotifier publishes a webhook event for a downstream notification target
// (Discord/ntfy/etc.). It is the narrow shape the scanner needs from
// *notifier.Notifier — declared locally so tests can spy on Send without
// pulling the whole notifier package and its HTTP/SSRF machinery into the
// fixture (issue #849).
type eventNotifier interface {
	Send(ctx context.Context, eventType string, payload map[string]interface{})
}

// Event-type strings. Duplicated from internal/notifier to avoid an import
// cycle risk and keep this package free of notifier internals. Must stay in
// sync with notifier.Event* constants (covered by their respective tests).
const (
	notifierEventGrabbed        = "grabbed"
	notifierEventBookImported   = "bookImported"
	notifierEventUpgrade        = "upgrade"
	notifierEventDownloadFailed = "downloadFailed"
)

// Scanner checks for completed downloads and imports them into the library.
type Scanner struct {
	// windowsPaths makes path comparisons fold case and trailing dots, as
	// Windows does (#3468). Set from the host OS; tests inject it.
	windowsPaths bool
	// covers stores cover art read from book files; nil disables it. See
	// WithCoverStore.
	covers *covers.Store
	// noFileCover remembers files read for a cover that had none, keyed by
	// path, so the library scan's backfill does not reread them every scan.
	// See fillCoverFromFile.
	noFileCover sync.Map
	// readCover reads a file's cover art; nil uses readFileCover. Tests set
	// it to count reads.
	readCover   func(string) []byte
	downloads   *db.DownloadRepo
	clients     *db.DownloadClientRepo
	books       *db.BookRepo
	authors     *db.AuthorRepo
	editions    *db.EditionRepo
	history     *db.HistoryRepo
	rootFolders *db.RootFolderRepo
	// replacedRemover is the guarded delete for the file an upgrade replaced
	// (#2420). Nil means an upgrade never deletes anything.
	replacedRemover ReplacedFileRemover
	series          *db.SeriesRepo
	renamer         *Renamer
	remapper        *Remapper
	calibreQueue    calibreDeliveryQueue
	grimmory        grimmoryPusher
	calibreMode     func() calibre.Mode
	settings        *db.SettingsRepo
	// qualityProfiles and blocklist back the post-download format check
	// (#1782). Both nil disables it entirely, which is what every caller that
	// has not been wired up gets.
	qualityProfiles *db.QualityProfileRepo
	blocklist       *db.BlocklistRepo
	// metadataProfiles backs the post-download language check (#2998): a
	// downloaded EPUB declaring a language the author's metadata profile does
	// not allow is rejected and blocklisted instead of relabelling the book.
	// Nil disables the check, exactly as an unwired format check does.
	metadataProfiles *db.MetadataProfileRepo
	// authorAliases backs the ebook content check (#3015): a file whose
	// dc:creator is a pen name or house name the author is known under counts as
	// the author. Nil leaves the check comparing the author's own name only.
	authorAliases *db.AuthorAliasRepo
	// users resolves a download owner's role for downloadMatchScope. Nil
	// treats every owner as a non admin, which only ever narrows the match.
	users *db.UserRepo
	// onContentCheck, when non-nil, is called each time a content check
	// actually runs, with "ebook" or "audiobook". A test seam: it proves the
	// check runs for a polled download and never for a library scan or a
	// manual import.
	onContentCheck func(kind string)
	// readTags reads one audio file's tags for the audiobook content check;
	// nil uses ReadAudioTags. A test seam that counts and bounds the reads.
	readTags             func(path string) (AudioTags, error)
	libraryDir           string
	audiobookDir         string
	audiobookDownloadDir string
	// downloadDir is the general download folder; see WithDownloadDir.
	downloadDir     string
	absLib          absNotifier
	absLibraryIDsFn func() []string
	notif           eventNotifier
	// unmatchedUnits stores the books a library scan could not match, for
	// library adoption. Nil stores nothing (see WithUnmatchedUnits).
	unmatchedUnits UnmatchedUnitStore

	// jobs, when set, tracks the detached scan goroutine launched by StartScan
	// so process shutdown can cancel and drain it before the database closes
	// (#1458). When nil, StartScan falls back to an untracked goroutine on the
	// caller's context (tests, non-wired callers).
	jobs *jobs.Group

	// scanRunning is the single-flight guard for library scans (#1460).
	// Concurrent full walks race on book creation (books has no unique
	// constraint equivalent to book_files.path) and clobber library.lastScan,
	// so only one scan — manual or scheduled — runs at a time.
	scanRunning atomic.Bool
	// scanMu orders the claim and release of scanRunning against
	// scanPending, so a request that arrives as a scan finishes is either
	// run by that scan's follow-up or starts a scan of its own, never lost.
	scanMu sync.Mutex
	// scanPending records that a scan was requested while one was running
	// (#3014). The running scan runs one follow-up when it finishes, however
	// many requests arrived.
	scanPending bool
	// scanSeq counts the scans this process has started; scanID is the id of
	// the one running or last run. Both under scanMu. The id lets a client
	// that asked for a scan recognise that scan's result (#3014).
	scanSeq int64
	scanID  string

	// testScanHook, when non-nil, runs at the start of every library scan
	// walk. A test seam: it lets a test hold a scan in flight while it makes
	// a second request (#3014).
	testScanHook func()

	// testImportHook, when non-nil, intercepts tryImportInternal before any
	// state transition or file operation and replaces the import entirely.
	// It is a test seam for the per-client dispatch/completion matrix
	// (scanner_dispatch_matrix_test.go, issue #1019): those tests drive the
	// real CheckDownloads dispatch against protocol stubs and assert that the
	// importer boundary was handed the right path by the right typed poller,
	// without re-exercising the staging machinery the per-client scanner
	// tests already cover. Never set outside tests.
	testImportHook func(dl *models.Download, downloadPath, clientType string, explicitFiles []string)

	// importSkips counts consecutive declined import retries per download so a
	// download whose files never appear cannot skip forever (#1884 follow-up).
	// In-memory on purpose: it is poll bookkeeping, not durable state, and a
	// restart simply re-starts the streak — the row is still StateImportFailed
	// with its reason, so nothing is lost but the elapsed patience.
	importSkipsMu sync.Mutex
	importSkips   map[int64]importSkipState

	// manualBookLocks holds one *sync.Mutex per book ID so manual imports
	// against the same book run one at a time (#2935). Two of them racing
	// both passed the already-imported check, both ran UniqueDir before
	// either created its folder, and split one audiobook's tracks across
	// "Title" and "Title (2)".
	manualBookLocks sync.Map

	// contentBreaker stops blocklisting on a download client's content
	// verdict when the client itself looks broken (#3024), and clientHealth,
	// when set, is where that is reported. Nil clientHealth reports nothing.
	contentBreaker contentFailureBreaker
	clientHealth   *downloader.HealthStore
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
		windowsPaths: onWindows,
	}
}

// destRenamer returns a Renamer built from the CURRENT ebook/audiobook naming
// templates in settings, so a template saved in the Settings UI takes effect on
// the next import or reorganize without a Bindery restart. The boot-time
// s.renamer is only a fallback for wiring that has no settings repo (tests).
// Mirrors how the per-track audiobook template is already re-read live per
// import (#1126).
func (s *Scanner) destRenamer(ctx context.Context) *Renamer {
	if s.settings == nil {
		return s.renamer
	}
	ebook, audiobook := ResolveNamingTemplates(ctx, s.settings)
	return NewRenamerWithAudiobook(ebook, audiobook)
}

// WithDownloadDir sets the general download folder. The library scan leaves it
// out when it sits inside a library root, as it does the audiobook download
// folder.
func (s *Scanner) WithDownloadDir(dir string) *Scanner {
	s.downloadDir = dir
	return s
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

// WithJobs registers the process-wide background-jobs group so a StartScan()
// launched scan is tracked and drained on shutdown before the database closes
// (#1458).
func (s *Scanner) WithJobs(g *jobs.Group) *Scanner {
	s.jobs = g
	return s
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
	if path := s.defaultRootFolderPath(ctx, "library.defaultRootFolderId"); path != "" {
		return path
	}
	return s.libraryDir
}

// effectiveAudiobookDir returns the audiobook root to use for the given author.
// Priority: (1) author's explicit AudiobookRootFolderID, (2)
// library.defaultAudiobookRootFolderId setting (#2166), (3) global audiobookDir
// from env-var. It deliberately mirrors effectiveLibraryDir but consults a
// separate per-author field and a separate default: routing audiobooks through
// the ebook RootFolderID or the ebook default would send them into the ebook
// root whenever either is set, silently ignoring BINDERY_AUDIOBOOK_DIR (#421).
func (s *Scanner) effectiveAudiobookDir(ctx context.Context, author *models.Author) string {
	if author != nil && author.AudiobookRootFolderID != nil && s.rootFolders != nil {
		if rf, err := s.rootFolders.GetByID(ctx, *author.AudiobookRootFolderID); err == nil && rf != nil {
			return rf.Path
		}
	}
	if path := s.defaultRootFolderPath(ctx, "library.defaultAudiobookRootFolderId"); path != "" {
		return path
	}
	return s.audiobookDir
}

// defaultRootFolderPath resolves a default root folder setting to its path.
// Empty when the setting is unset, malformed, or names a root folder that no
// longer exists, so callers fall through to the env-var default.
func (s *Scanner) defaultRootFolderPath(ctx context.Context, key string) string {
	if s.settings == nil || s.rootFolders == nil {
		return ""
	}
	setting, err := s.settings.Get(ctx, key)
	if err != nil || setting == nil || setting.Value == "" {
		return ""
	}
	id, err := strconv.ParseInt(setting.Value, 10, 64)
	if err != nil || id <= 0 {
		return ""
	}
	rf, err := s.rootFolders.GetByID(ctx, id)
	if err != nil || rf == nil {
		return ""
	}
	return rf.Path
}

// effectiveRootForFormat picks the correct root (ebook or audiobook) to
// gate a reconcile match against, based on the detected format of the file
// being matched. Reconcile tiers used to always check effectiveLibraryDir
// (the ebook root), which silently rejected correctly-placed audiobook
// files whenever BINDERY_AUDIOBOOK_DIR differed from the ebook root (#2033)
// — the same class of bug already fixed for the import-placement path
// under #421.
func (s *Scanner) effectiveRootForFormat(ctx context.Context, author *models.Author, detectedFmt string) string {
	if detectedFmt == models.MediaTypeAudiobook {
		return s.effectiveAudiobookDir(ctx, author)
	}
	return s.effectiveLibraryDir(ctx, author)
}

// WithCalibreDeliveries attaches the Calibre delivery queue (#2832). mode is
// read on every import: with the integration off nothing is queued. Imports
// never talk to Calibre themselves; they queue the file and kick the worker.
func (s *Scanner) WithCalibreDeliveries(mode func() calibre.Mode, q calibreDeliveryQueue) *Scanner {
	s.calibreMode = mode
	s.calibreQueue = q
	return s
}

// WithGrimmory attaches the Grimmory push integration (#826). The pusher reads
// its config live on every import, so enabling/disabling in the UI takes
// effect without restarting. Pushes are best-effort by contract: a Grimmory
// failure never blocks or fails the underlying import.
func (s *Scanner) WithGrimmory(p grimmoryPusher) *Scanner {
	s.grimmory = p
	return s
}

// WithABSNotifier attaches an Audiobookshelf scan notifier. libraryIDsFn is
// called at import time to retrieve the current ABS audiobook library IDs;
// returning an empty slice disables the notification for that import.
func (s *Scanner) WithABSNotifier(n absNotifier, libraryIDsFn func() []string) *Scanner {
	s.absLib = n
	s.absLibraryIDsFn = libraryIDsFn
	return s
}

// WithNotifier attaches a webhook event notifier so import-success and
// download-failure transitions publish to user-configured ntfy/Discord/etc.
// endpoints. Before this wiring (issue #849) only manual grabs from the queue
// page emitted events — every other site that wrote a history row was silent.
func (s *Scanner) WithNotifier(n eventNotifier) *Scanner {
	s.notif = n
	return s
}

// WithFormatEnforcement wires the repos the post-download format check needs
// (#1782). Without it the check does not run and imports behave exactly as
// before, which is what keeps every existing test and unwired caller working.
func (s *Scanner) WithFormatEnforcement(profiles *db.QualityProfileRepo, blocklist *db.BlocklistRepo) *Scanner {
	s.qualityProfiles = profiles
	s.blocklist = blocklist
	return s
}

// WithLanguageEnforcement wires the repos the post-download language check
// needs (#2998). Without it the check does not run and every import behaves as
// it did before: the file's declared language relabels the book (#1933).
func (s *Scanner) WithLanguageEnforcement(profiles *db.MetadataProfileRepo, blocklist *db.BlocklistRepo) *Scanner {
	s.metadataProfiles = profiles
	s.blocklist = blocklist
	return s
}

// allowedFormat reports whether a file of the given format may fill the given
// slot, and the rejection reason when it may not. slotMediaType is the media
// type the download is being imported as (detectDownloadFormat's answer, or an
// explicit formatHint).
//
// An empty format means the file could not be identified, which is passed for
// the same reason decision.QualityAllowed passes an unparseable release name:
// the filter can only speak to formats it can actually see, and rejecting on
// ignorance would block legitimate books.
//
// The slot check runs BEFORE the quality profile and is not a profile question
// at all (#2307). detectDownloadFormat routes a download by file extension
// while formatsniff.Detect reads content, so the two disagree on an
// extensionless or mis-extensioned file: an MP3 named with no extension is
// routed to the ebook branch and sniffed as "mp3". Until #2307 the quality
// profile happened to stop it, because an ebook-only profile rejected every
// audio token. Now that a profile only answers for its own media type, nothing
// else would, and the file would be recorded as the book's EBOOK and placed in
// the ebook library. A file whose content belongs to the other media type is
// wrong for this slot whatever the profile says, so say so here.
func (s *Scanner) allowedFormat(ctx context.Context, author *models.Author, format, slotMediaType string) (bool, string) {
	if format == "" {
		return true, ""
	}
	if fileMediaType := indexer.MediaTypeForFormat(format); fileMediaType != "" &&
		slotMediaType != "" && fileMediaType != slotMediaType {
		return false, fmt.Sprintf("file format %q is a %s file but this download is being imported as %s",
			format, fileMediaType, slotMediaType)
	}
	if s.qualityProfiles == nil || author == nil {
		return true, ""
	}
	profile := db.ResolveAuthorQualityProfile(ctx, s.qualityProfiles, author)
	if profile == nil {
		return true, ""
	}
	return decision.QualityAllowed{Profile: profile}.IsSatisfiedBy(
		decision.Release{Format: format}, models.Book{})
}

// blocklistRejectedRelease records a release rejected for its format (#1782)
// or its language (#2998), or one the download client reported as broken
// (#3024), so the next search does not grab the same file again.
//
// Without this the rejection is a loop: the book stays wanted, the next scan
// finds the same release, grabs it, downloads it, and rejects it again. The
// blocklist is the only thing that makes a rejection stick, and it is also why
// this must stay narrow: it fires on a format or language the user explicitly
// disallowed, or on a client status that is about the release's content, never
// on a transient import or transport failure.
//
// scope says whom the entry blocks (#3563). A format, language or content
// rejection is about this book: its profile refused the file, or the file did
// not match its metadata, so the entry is models.BlocklistScopeBook and
// another book that wants the same release, possibly another user's under a
// profile that allows it, can still grab it. A content failure the download
// client reported is about the release, so it is models.BlocklistScopeRelease
// and blocks it for every book.
//
// An entry that already blocks what this one would is not added twice: a
// manual grab can send a blocklisted release again, and its second failure
// says nothing new. For a book scoped entry that is an entry for this book or
// a release scoped one. For a release scoped entry only another release scoped
// entry counts: a book scoped entry for this or any other book blocks the
// release for that book alone, so skipping the write would leave a broken
// release open to every other book.
func (s *Scanner) blocklistRejectedRelease(ctx context.Context, dl *models.Download, scope, reason string) {
	if s.blocklist == nil || dl == nil || strings.TrimSpace(dl.GUID) == "" {
		return
	}
	forBook := dl.BookID
	if scope == models.BlocklistScopeRelease {
		forBook = nil
	}
	if blocked, err := s.blocklist.BlocksBook(ctx, dl.GUID, forBook); err == nil && blocked {
		return
	}
	entry := &models.BlocklistEntry{
		BookID:    dl.BookID,
		GUID:      dl.GUID,
		Title:     dl.Title,
		IndexerID: dl.IndexerID,
		Reason:    reason,
		Scope:     scope,
	}
	if err := s.blocklist.Create(ctx, entry); err != nil {
		slog.Warn("could not blocklist a rejected release; it may be grabbed again",
			"guid", dl.GUID, "title", dl.Title, "error", err)
	}
}

// notify is a nil-safe Send wrapper. Keeps every call site a one-liner without
// repeating the guard, and lets tests construct a Scanner without a notifier.
func (s *Scanner) notify(ctx context.Context, eventType string, payload map[string]interface{}) {
	if s.notif == nil {
		return
	}
	s.notif.Send(ctx, eventType, payload)
}

// importedPayload builds the EventBookImported webhook payload. The book may
// be nil in the unmatched-import edge case; in that case fall back to the
// download title so the webhook still carries a meaningful "title". Path is
// optional ("" omits it from the payload) — the ebook path varies per file in
// a multi-format bundle so emitting one of them would be misleading.
// skippedFiles is non-empty only for a shared-folder merge (#1959) that
// declined to overwrite one or more same-named files already present from
// the book's other format — surfaced here so a webhook/notifier consumer
// sees it alongside the normal import event instead of it only reaching a
// log line.
func importedPayload(book *models.Book, dl *models.Download, format, path string, skippedFiles []string) map[string]interface{} {
	title := ""
	if book != nil {
		title = book.Title
	}
	if title == "" && dl != nil {
		title = dl.Title
	}
	p := map[string]interface{}{
		"title":  title,
		"format": format,
	}
	if path != "" {
		p["path"] = path
	}
	if len(skippedFiles) > 0 {
		p["skippedFiles"] = skippedFiles
	}
	return p
}

// WithSettings attaches a SettingsRepo to the scanner so scan results can be
// persisted under the "library.lastScan" key and surfaced via the API.
func (s *Scanner) WithSettings(sr *db.SettingsRepo) *Scanner {
	s.settings = sr
	return s
}

// rejectedDownloadFormat reports whether every candidate file in a download is
// a format the author's quality profile disallows, and why.
//
// All-or-nothing on purpose. A mixed download, an epub next to a disallowed
// pdf, should import the file the user allowed rather than be blocked whole;
// the per-file skip in the ebook loop handles that. This gate is for the case
// where importing anything at all would mean importing something the user
// explicitly said they did not want, which is also the only case where
// blocklisting the release is the right answer.
//
// Files whose format cannot be identified count as allowed, mirroring
// QualityAllowed's own behaviour on an unparseable release name.
func (s *Scanner) rejectedDownloadFormat(ctx context.Context, author *models.Author, files []string, slotMediaType string) (bool, string) {
	if len(files) == 0 {
		return false, ""
	}
	var firstReason string
	for _, f := range files {
		format := formatsniff.Detect(f)
		ok, reason := s.allowedFormat(ctx, author, format, slotMediaType)
		if ok {
			return false, ""
		}
		if firstReason == "" {
			firstReason = reason
		}
	}
	if firstReason == "" {
		return false, ""
	}
	return true, firstReason + ". Not imported. Use manual import to override, or allow the format in the quality profile."
}
