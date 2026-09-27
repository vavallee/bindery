package calibre

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// SyncError is a per-book failure entry returned to the UI.
type SyncError struct {
	BookID int64  `json:"bookId"`
	Title  string `json:"title"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason"`
}

// SyncSkip is a per-book record of a book the bulk push did not attempt.
// Before these existed, a skipped book appeared in no counter, no error row
// and no log line, so "Pushed 0, already in Calibre 0, failed 0" could mean
// either "your library is already in Calibre" or "every book was dropped by a
// filter you cannot see" (discussion #1592).
type SyncSkip struct {
	BookID int64  `json:"bookId"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// Skip reasons. A book that has not been imported is not among them: it has
// no file, so it was never a candidate, and listing it drowned the reasons a
// user can act on (a 3,000 book wanted list filled the 50 row sample before
// any unmonitored or missing file book was reached). Kept free of commas so they read correctly when the zero-case
// message joins several of them into one sentence.
const (
	// SkipReasonNotMonitored is an imported book that is unmonitored. The
	// bulk push has always inherited this filter from ListByStatus; it was
	// simply invisible.
	SkipReasonNotMonitored = "not monitored"
	// SkipReasonNoFile is an imported book with no file path at all, which
	// means the importer crashed or the file was deleted underneath Bindery.
	SkipReasonNoFile = "no file on disk"
	// SkipReasonAudiobookOnly is an imported book that only has an audiobook.
	// The plugin endpoint takes one ebook file, so there is nothing to send.
	SkipReasonAudiobookOnly = "audiobook only with no ebook"
)

// maxSyncErrors caps the per-book error list kept in SyncProgress.Errors (and
// thus returned on every status poll). A run that fails on every book — e.g. a
// library path the Calibre container can't see (#1346) — would otherwise grow a
// SyncError per book and re-serialize thousands of near-identical entries to the
// UI on each poll. The full failure count is always in Stats.Failed; the list is
// a sample for display.
const maxSyncErrors = 50

// SyncStats summarises one bulk-push run. Totals are cumulative across
// every imported book with a file path; `Pushed` counts newly-added,
// `AlreadyInCalibre` counts 409 Conflict responses (treated as success
// for idempotency), and `Failed` counts everything else.
type SyncStats struct {
	Total            int `json:"total"`
	Processed        int `json:"processed"`
	Pushed           int `json:"pushed"`
	AlreadyInCalibre int `json:"alreadyInCalibre"`
	Failed           int `json:"failed"`
	// Skipped counts books the run never attempted. It is deliberately not
	// part of Total: Total is the denominator of the progress bar and only
	// counts work the run is actually going to do.
	Skipped int `json:"skipped"`
}

// SyncProgress is the polled shape for /calibre/sync/status. Running=false
// with a non-nil FinishedAt means the last run is complete; Running=false
// with StartedAt zero means nothing has been run yet this process.
type SyncProgress struct {
	Running    bool        `json:"running"`
	StartedAt  time.Time   `json:"startedAt"`
	FinishedAt *time.Time  `json:"finishedAt,omitempty"`
	Message    string      `json:"message,omitempty"`
	Error      string      `json:"error,omitempty"`
	Stats      SyncStats   `json:"stats"`
	Errors     []SyncError `json:"errors"`
	// Skips samples the books the run did not attempt, capped the same way
	// Errors is. Stats.Skipped always holds the full count.
	Skips []SyncSkip `json:"skips"`
}

// pluginPusher captures the subset of *PluginClient the syncer needs, so
// tests can inject a fake without standing up an HTTP server.
type pluginPusher interface {
	Add(ctx context.Context, filePath string, meta Metadata) (int64, error)
	Library(ctx context.Context) (string, error)
}

// BookLister is the subset of *db.BookRepo the syncer uses. Keeps the
// dependency narrow for tests.
type BookLister interface {
	// List returns the whole visible catalogue. It exists only so the run can
	// say why a book was left out; eligibility still comes from ListByStatus,
	// so what gets pushed is unchanged.
	List(ctx context.Context) ([]models.Book, error)
	ListByStatus(ctx context.Context, status string) ([]models.Book, error)
	SetCalibreID(ctx context.Context, id, calibreID int64) error
}

// SeriesGetter is the subset of *db.SeriesRepo used to attach series and
// series index to a bulk-pushed book, which a live import has always sent and
// the bulk push never did.
type SeriesGetter interface {
	GetPrimarySeriesForBook(ctx context.Context, bookID int64) (string, string, error)
}

// AuthorGetter is the subset of *db.AuthorRepo used to add author metadata
// to plugin sync requests.
type AuthorGetter interface {
	GetByID(ctx context.Context, id int64) (*models.Author, error)
}

// EditionLister is the subset of *db.EditionRepo used to identify the
// specific edition for the file being pushed.
type EditionLister interface {
	ListByBook(ctx context.Context, bookID int64) ([]models.Edition, error)
}

// Syncer orchestrates the "Push all to Calibre" bulk job. One sync runs
// at a time — a second call returns ErrSyncAlreadyRunning. Progress is
// mutex-protected and can be polled concurrently with the running job.
type Syncer struct {
	books     BookLister
	authors   AuthorGetter
	editions  EditionLister
	series    SeriesGetter
	newClient func(cfg Config) pluginPusher

	mu       sync.Mutex
	running  bool
	progress SyncProgress
}

// NewSyncer wires a syncer against the books repo. The plugin client is
// built per-run from the current settings so mode/URL changes take effect
// without restarting Bindery.
func NewSyncer(books BookLister) *Syncer {
	return &Syncer{
		books: books,
		newClient: func(cfg Config) pluginPusher {
			return NewPluginClient(cfg.PluginURL, cfg.PluginAPIKey).WithPushPathRemap(cfg.PushPathRemap)
		},
	}
}

// WithMetadata attaches optional repositories used to enrich plugin sync
// requests. Keeping this separate preserves the narrow constructor used in
// tests while production can export authors and edition identifiers.
func (s *Syncer) WithMetadata(authors AuthorGetter, editions EditionLister) *Syncer {
	s.authors = authors
	s.editions = editions
	return s
}

// WithSeries attaches the series lookup so bulk-pushed books carry the same
// series and series index a live import sends.
func (s *Syncer) WithSeries(series SeriesGetter) *Syncer {
	s.series = series
	return s
}

// ErrSyncAlreadyRunning is returned when Start is called while a previous
// sync is still executing. Maps to 409 Conflict at the API layer.
var ErrSyncAlreadyRunning = errors.New("calibre sync already running")

// ErrSyncModeNotPlugin is returned when Start is called while the Calibre
// integration is not in plugin mode — bulk push only makes sense against
// the plugin, not the calibredb CLI (which already lives next to the
// library file on the Bindery host).
var ErrSyncModeNotPlugin = errors.New("calibre sync requires mode=plugin")

// Running reports whether a sync is currently in flight.
func (s *Syncer) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Progress returns a snapshot of the current (or most recent) sync.
func (s *Syncer) Progress() SyncProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Defensive copy of the slice so a later append by the worker does
	// not mutate a snapshot already handed to the HTTP layer.
	snap := s.progress
	if len(s.progress.Errors) > 0 {
		snap.Errors = append([]SyncError(nil), s.progress.Errors...)
	}
	if len(s.progress.Skips) > 0 {
		snap.Skips = append([]SyncSkip(nil), s.progress.Skips...)
	}
	return snap
}

// Start launches a sync in the background. Caller passes
// context.WithoutCancel(r.Context()) so the HTTP response-send doesn't
// cancel the long-running job.
func (s *Syncer) Start(ctx context.Context, cfg Config, mode Mode) error {
	if mode != ModePlugin {
		return ErrSyncModeNotPlugin
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrSyncAlreadyRunning
	}
	s.running = true
	s.progress = SyncProgress{
		Running:   true,
		StartedAt: time.Now().UTC(),
		Message:   "listing imported books…",
		Errors:    []SyncError{},
		Skips:     []SyncSkip{},
	}
	s.mu.Unlock()

	go s.run(ctx, cfg)
	return nil
}

func (s *Syncer) run(ctx context.Context, cfg Config) {
	defer func() {
		now := time.Now().UTC()
		s.mu.Lock()
		s.progress.Running = false
		s.progress.FinishedAt = &now
		s.running = false
		s.mu.Unlock()
	}()

	books, err := s.books.ListByStatus(ctx, models.BookStatusImported)
	if err != nil {
		s.fail("list imported books: " + err.Error())
		return
	}

	// Restrict to books that actually have a file on disk. A book row with
	// status=imported but empty file_path means the importer crashed or the
	// file was deleted out from under us, so there is nothing to push.
	eligible := make([]models.Book, 0, len(books))
	skips := make([]SyncSkip, 0)
	skipCounts := map[string]int{}
	recordSkip := func(b *models.Book, reason string) {
		skipCounts[reason]++
		if len(skips) < maxSyncErrors {
			skips = append(skips, SyncSkip{BookID: b.ID, Title: b.Title, Reason: reason})
		}
	}
	for i := range books {
		b := &books[i]
		switch {
		case pushPath(b) != "":
			eligible = append(eligible, *b)
		case strings.TrimSpace(b.AudiobookFilePath) != "":
			recordSkip(b, SkipReasonAudiobookOnly)
		default:
			recordSkip(b, SkipReasonNoFile)
		}
	}
	// Books that never reached the eligible list at all. ListByStatus filters
	// on status AND monitored, so both are reported separately rather than as
	// one undifferentiated "not eligible".
	if all, listErr := s.books.List(ctx); listErr != nil {
		slog.Warn("calibre sync: full catalogue unavailable; skip reasons limited to the imported set", "error", listErr)
	} else {
		considered := make(map[int64]struct{}, len(books))
		for i := range books {
			considered[books[i].ID] = struct{}{}
		}
		for i := range all {
			b := &all[i]
			if _, ok := considered[b.ID]; ok {
				continue
			}
			// A book with no import has no file and was never a candidate,
			// so it is left out of the skips rather than reported as one.
			if b.Status == models.BookStatusImported {
				recordSkip(b, SkipReasonNotMonitored)
			}
		}
	}
	skipped := 0
	for _, n := range skipCounts {
		skipped += n
	}

	s.setProgress(func(p *SyncProgress) {
		p.Stats.Total = len(eligible)
		p.Stats.Skipped = skipped
		p.Skips = skips
		if len(eligible) == 0 {
			p.Message = nothingToPushMessage(skipCounts)
		} else {
			p.Message = "pushing books to Calibre…"
		}
	})
	if skipped > 0 {
		slog.Info("calibre sync: books skipped", "skipped", skipped, "reasons", skipSummary(skipCounts))
	}

	client := s.newClient(cfg)
	sameLibrary := sameCalibreLibrary(ctx, cfg, client)

	// Log the first book to hit each distinct failure reason at WARN. The
	// summary line below only reports failed=N, and the per-book SyncErrors live
	// in the polled progress (not the log), so a user staring at the log had no
	// way to see *why* every push failed (#1346 — a path the Calibre container
	// can't resolve). Dedupe by reason so a library-wide path mismatch logs once,
	// not once per book.
	loggedReasons := make(map[string]bool)
	recordFailure := func(b *models.Book, path, reason string) {
		s.setProgress(func(p *SyncProgress) {
			p.Stats.Failed++
			p.Stats.Processed++
			if len(p.Errors) < maxSyncErrors {
				p.Errors = append(p.Errors, SyncError{
					BookID: b.ID,
					Title:  b.Title,
					Path:   path,
					Reason: reason,
				})
			}
		})
		if !loggedReasons[reason] {
			loggedReasons[reason] = true
			slog.Warn("calibre sync: book push failed",
				"bookId", b.ID, "title", b.Title, "path", path, "reason", reason)
		}
	}

	for i := range eligible {
		if err := ctx.Err(); err != nil {
			s.fail("cancelled: " + err.Error())
			return
		}
		b := &eligible[i]
		path := pushPath(b)
		meta, err := s.metadataForBook(ctx, b, path, sameLibrary)
		if err != nil {
			recordFailure(b, path, err.Error())
			continue
		}
		id, addErr := client.Add(ctx, path, meta)
		switch {
		case addErr == nil:
			if perr := s.persistCalibreID(ctx, b, id, sameLibrary); perr != nil {
				slog.Warn("calibre sync: persist calibre_id failed", "bookId", b.ID, "calibreId", id, "error", perr)
			}
			s.setProgress(func(p *SyncProgress) {
				p.Stats.Pushed++
				p.Stats.Processed++
			})
		case errors.Is(addErr, ErrAlreadyInCalibre):
			if perr := s.persistCalibreID(ctx, b, id, sameLibrary); perr != nil {
				slog.Warn("calibre sync: persist calibre_id failed", "bookId", b.ID, "calibreId", id, "error", perr)
			}
			s.setProgress(func(p *SyncProgress) {
				p.Stats.AlreadyInCalibre++
				p.Stats.Processed++
			})
		default:
			recordFailure(b, path, addErr.Error())
		}
	}

	s.setProgress(func(p *SyncProgress) {
		// A run with nothing to do keeps the message that named why, so the
		// modal does not overwrite the only useful thing it has to say.
		if p.Stats.Total > 0 {
			p.Message = "done"
		}
	})
	// Snapshot under lock — setProgress writes s.progress concurrently with the
	// HTTP poller reading it, so read the stats through the locked getter.
	final := s.Progress()
	slog.Info("calibre sync complete",
		"total", len(eligible),
		"pushed", final.Stats.Pushed,
		"alreadyInCalibre", final.Stats.AlreadyInCalibre,
		"failed", final.Stats.Failed,
		"skipped", final.Stats.Skipped,
		"distinctFailureReasons", len(loggedReasons))
}

// pushPath returns the on-disk path to send to Calibre for the given
// book, preferring the ebook-specific column (populated by dual-format
// imports) and falling back to the legacy single file_path.
func pushPath(b *models.Book) string {
	if b.EbookFilePath != "" {
		return b.EbookFilePath
	}
	return b.FilePath
}

func (s *Syncer) metadataForBook(ctx context.Context, b *models.Book, path string, sameLibrary bool) (Metadata, error) {
	edition, err := s.editionForPath(ctx, b, path)
	if err != nil {
		return Metadata{}, err
	}
	authors, authorSort, err := s.authorMetadata(ctx, b)
	if err != nil {
		return Metadata{}, err
	}
	identifiers := IdentifiersForBook(b, edition)
	if !sameLibrary && isCalibreOrigin(b) {
		delete(identifiers, "calibre")
	}
	seriesTitle, seriesIndex := s.primarySeries(ctx, b)
	return BuildMetadata(MetadataSource{
		Book:        b,
		Authors:     authors,
		AuthorSort:  authorSort,
		Edition:     edition,
		SeriesTitle: seriesTitle,
		SeriesIndex: seriesIndex,
		Identifiers: identifiers,
	}), nil
}

// primarySeries mirrors the importer's lookup. A failure is not worth failing
// the push over: the book still reaches Calibre, just without its series.
func (s *Syncer) primarySeries(ctx context.Context, b *models.Book) (string, string) {
	if s.series == nil || b == nil {
		return "", ""
	}
	title, index, err := s.series.GetPrimarySeriesForBook(ctx, b.ID)
	if err != nil {
		slog.Debug("calibre sync: primary series lookup failed", "bookId", b.ID, "error", err)
		return "", ""
	}
	return title, index
}

// nothingToPushMessage replaces the old generic "no imported books with files
// to push" with the reasons the run actually found, so the #1592 report is
// self-diagnosing.
func nothingToPushMessage(counts map[string]int) string {
	summary := skipSummary(counts)
	if summary == "" {
		return "no books to push: no imported book has an ebook file yet"
	}
	return "no books to push: " + summary
}

// skipSummary renders the skip tally in a stable order, most common first.
func skipSummary(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	type entry struct {
		reason string
		n      int
	}
	entries := make([]entry, 0, len(counts))
	for reason, n := range counts {
		entries = append(entries, entry{reason, n})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].n != entries[j].n {
			return entries[i].n > entries[j].n
		}
		return entries[i].reason < entries[j].reason
	})
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, strconv.Itoa(e.n)+" "+e.reason)
	}
	return strings.Join(parts, "; ")
}

func (s *Syncer) authorMetadata(ctx context.Context, b *models.Book) ([]string, string, error) {
	if b.Author != nil && strings.TrimSpace(b.Author.Name) != "" {
		return []string{strings.TrimSpace(b.Author.Name)}, strings.TrimSpace(b.Author.SortName), nil
	}
	if s.authors == nil || b.AuthorID == 0 {
		return nil, "", nil
	}
	author, err := s.authors.GetByID(ctx, b.AuthorID)
	if err != nil {
		return nil, "", err
	}
	if author == nil || strings.TrimSpace(author.Name) == "" {
		return nil, "", nil
	}
	return []string{strings.TrimSpace(author.Name)}, strings.TrimSpace(author.SortName), nil
}

func (s *Syncer) editionForPath(ctx context.Context, b *models.Book, path string) (*models.Edition, error) {
	if s.editions == nil {
		return nil, nil
	}
	editions, err := s.editions.ListByBook(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	if len(editions) == 0 {
		return nil, nil
	}

	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	if ext != "" {
		format := strings.ToUpper(strings.TrimSpace(ext))
		var firstFormatMatch *models.Edition
		for i := range editions {
			if strings.ToUpper(strings.TrimSpace(editions[i].Format)) != format {
				continue
			}
			if b.SelectedEditionID != nil && editions[i].ID == *b.SelectedEditionID {
				return &editions[i], nil
			}
			if firstFormatMatch == nil {
				firstFormatMatch = &editions[i]
			}
		}
		if firstFormatMatch != nil {
			return firstFormatMatch, nil
		}
	}

	if b.SelectedEditionID != nil {
		for i := range editions {
			if editions[i].ID == *b.SelectedEditionID {
				return &editions[i], nil
			}
		}
	}
	return &editions[0], nil
}

func (s *Syncer) persistCalibreID(ctx context.Context, b *models.Book, id int64, sameLibrary bool) error {
	if id <= 0 {
		return nil
	}
	if isCalibreOrigin(b) && !sameLibrary {
		slog.Debug("calibre sync: not overwriting source calibre_id with target library id",
			"bookId", b.ID, "targetCalibreId", id)
		return nil
	}
	return s.books.SetCalibreID(ctx, b.ID, id)
}

func sameCalibreLibrary(ctx context.Context, cfg Config, client pluginPusher) bool {
	source := cleanLibraryPath(cfg.LibraryPath)
	if source == "" {
		return false
	}
	target, err := client.Library(ctx)
	if err != nil {
		slog.Warn("calibre sync: target library identity unavailable; treating plugin target as separate from import source", "error", err)
		return false
	}
	target = cleanLibraryPath(target)
	if target == "" {
		return false
	}
	return source == target
}

func cleanLibraryPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func isCalibreOrigin(b *models.Book) bool {
	if b == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(b.MetadataProvider), "calibre") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(b.ForeignID)), "calibre:book:")
}

func (s *Syncer) fail(msg string) {
	slog.Error("calibre sync failed", "error", msg)
	s.setProgress(func(p *SyncProgress) {
		p.Error = msg
		p.Message = "failed"
	})
}

func (s *Syncer) setProgress(mutate func(*SyncProgress)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mutate(&s.progress)
}
