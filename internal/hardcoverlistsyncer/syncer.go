// Package hardcoverlistsyncer syncs Hardcover reading lists into Bindery's
// book catalogue as "wanted" books.
package hardcoverlistsyncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vavallee/bindery/internal/bookhydrate"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// Trigger values reported on SyncProgress so the UI can tell a user-initiated
// run apart from the scheduler's.
const (
	TriggerManual    = "manual"
	TriggerScheduled = "scheduled"
)

// SyncStats summarises one list's pass. Processed counts books seen on the
// Hardcover list, Imported the ones created in the catalogue, Skipped the ones
// already tracked (by foreign id or canonical dedup key), and Failed the ones
// whose author resolution or insert errored.
type SyncStats struct {
	Total     int `json:"total"`
	Processed int `json:"processed"`
	Imported  int `json:"imported"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
}

// SyncProgress is the polled shape for GET /importlist/sync/status. It always
// describes the most recent (or in-flight) list pass: Running=false with a
// non-nil FinishedAt means it completed, Running=false with a zero StartedAt
// means nothing has synced yet this process. Mirrors grimmory.SyncProgress /
// abs.ImportProgress so the UI polls the same way everywhere (#1854).
type SyncProgress struct {
	Running    bool       `json:"running"`
	ListID     int64      `json:"listId,omitempty"`
	ListName   string     `json:"listName,omitempty"`
	Trigger    string     `json:"trigger,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Message    string     `json:"message,omitempty"`
	Error      string     `json:"error,omitempty"`
	Stats      SyncStats  `json:"stats"`
}

// ListSyncer syncs enabled Hardcover import lists into Bindery's book catalogue.
type ListSyncer struct {
	importLists *db.ImportListRepo
	authors     *db.AuthorRepo
	books       *db.BookRepo
	series      seriesLinker

	tokenSource   func(context.Context) string
	clientFactory hardcoverClientFactory
	enricher      bookhydrate.AudiobookEnricher

	// jobs, when set, tracks the detached goroutine StartOne launches so
	// process shutdown can cancel and drain a sync before the database closes
	// (#1458). When nil, StartOne falls back to an untracked goroutine.
	jobs *jobs.Group

	// syncRunning is the single-flight gate shared by every entry point —
	// manual StartOne/SyncOne and the scheduled Sync — so a "Sync now" can
	// never overlap the cron job (or itself) and double-walk the same shelf.
	// Same CompareAndSwap shape as importer.Scanner.scanRunning.
	syncRunning atomic.Bool

	// progressMu guards progress, which is written by the running sync and read
	// by status polls on other goroutines.
	progressMu sync.Mutex
	progress   SyncProgress
}

type hardcoverClient interface {
	GetUserLists(context.Context) ([]hardcover.HCList, error)
	GetListBooks(context.Context, int) ([]models.Book, error)
	GetEditions(context.Context, string) ([]models.Edition, error)
}

// seriesLinker is the slice of *db.SeriesRepo the syncer needs to persist
// the primary-series association attached to imported books. Declared as an
// interface so tests can stub it without standing up the full SQL schema.
type seriesLinker interface {
	CreateOrGet(ctx context.Context, s *models.Series) error
	LinkBook(ctx context.Context, seriesID, bookID int64, position string, primary bool) error
}

type hardcoverClientFactory func(string) hardcoverClient

// New creates a new ListSyncer.
func New(importLists *db.ImportListRepo, authors *db.AuthorRepo, books *db.BookRepo) *ListSyncer {
	return &ListSyncer{
		importLists:   importLists,
		authors:       authors,
		books:         books,
		clientFactory: func(apiKey string) hardcoverClient { return hardcover.NewAuthenticated(apiKey) },
	}
}

// WithSeriesRepo wires the series persistence layer so that books imported
// from Hardcover lists carry forward their primary-series association.
// Without it, SeriesRefs on imported books are silently dropped.
func (s *ListSyncer) WithSeriesRepo(repo *db.SeriesRepo) *ListSyncer {
	if repo == nil {
		s.series = nil
		return s
	}
	s.series = repo
	return s
}

// WithAudiobookEnricher wires Audnex enrichment for list-synced books whose
// ASIN arrived inline on the list response. Replaces the enrichment that
// previously ran inside the per-book edition hydration (#1694): same trigger
// condition (a promoted ASIN on an audio-typed book), no edition fetch.
func (s *ListSyncer) WithAudiobookEnricher(enricher bookhydrate.AudiobookEnricher) *ListSyncer {
	s.enricher = enricher
	return s
}

// WithClientFactory overrides the Hardcover client factory used by tests.
func (s *ListSyncer) WithClientFactory(factory hardcoverClientFactory) *ListSyncer {
	if factory != nil {
		s.clientFactory = factory
	}
	return s
}

// WithTokenSource configures the fallback Hardcover API token used when an
// import list has no per-list override token.
func (s *ListSyncer) WithTokenSource(source func(context.Context) string) *ListSyncer {
	s.tokenSource = source
	return s
}

// WithJobs registers the process-wide background-jobs group so a StartOne()-
// launched sync is tracked and drained on shutdown before the database closes
// (#1458), instead of racing teardown on a never-cancelled context.
func (s *ListSyncer) WithJobs(g *jobs.Group) *ListSyncer {
	s.jobs = g
	return s
}

// Progress returns a snapshot of the current (or most recent) list sync.
func (s *ListSyncer) Progress() SyncProgress {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	return s.progress
}

// Sync processes all enabled import lists of type "hardcover". It shares the
// single-flight gate with the manual paths: if a "Sync now" is already in
// flight the scheduled run is skipped with a log line rather than double-
// walking the same shelves (the same cron-vs-manual guard importer.Scanner
// applies to library scans).
//
// With a jobs group wired the whole pass runs inside jobs.Group.Run, so it is
// tracked and drained on shutdown exactly like the manual path. The scheduler
// calls this on the process-lifecycle context, which SIGTERM cancels
// immediately; without the group the run would be racing database.Close()
// while still inside books.Create (#1458).
func (s *ListSyncer) Sync(ctx context.Context) error {
	if s.jobs == nil {
		return s.syncAll(ctx)
	}
	var err error
	if !s.jobs.Run("hardcover-list-sync-scheduled", func(jobCtx context.Context) {
		err = s.syncAll(jobCtx)
	}) {
		slog.Info("hardcover list sync skipped; process is shutting down")
		return nil
	}
	return err
}

// syncAll is Sync's body, split out so the jobs-group wrapper stays readable.
func (s *ListSyncer) syncAll(ctx context.Context) error {
	if !s.syncRunning.CompareAndSwap(false, true) {
		slog.Info("hardcover list sync already running; skipping scheduled run")
		return nil
	}
	defer s.syncRunning.Store(false)

	lists, err := s.importLists.ListByType(ctx, "hardcover")
	if err != nil {
		return fmt.Errorf("list hardcover import lists: %w", err)
	}
	if len(lists) == 0 {
		slog.Debug("no enabled hardcover import lists")
		return nil
	}

	var firstErr error
	for _, il := range lists {
		if err := s.runList(ctx, il, TriggerScheduled); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// SyncOne syncs a single hardcover import list by ID and waits for it to
// finish. StartOne is what the API uses; this synchronous form remains for
// callers (and tests) that want the completed result. Returns ErrNotFound if
// the list doesn't exist, ErrWrongType if it's not a hardcover list,
// ErrDisabled/ErrMissingToken if it can't run, ErrSyncAlreadyRunning if
// another sync holds the single-flight gate, or the underlying sync error.
func (s *ListSyncer) SyncOne(ctx context.Context, id int64) error {
	il, err := s.loadSyncableList(ctx, id)
	if err != nil {
		return err
	}
	if !s.syncRunning.CompareAndSwap(false, true) {
		return ErrSyncAlreadyRunning
	}
	defer s.syncRunning.Store(false)
	return s.runList(ctx, *il, TriggerManual)
}

// StartOne validates the list and then runs its sync in the background,
// returning as soon as the job is launched. This is what the manual "Sync now"
// endpoint calls: a large shelf takes minutes once per-book enrichment is in
// play, and running it inside the request capped it at the server's 60s
// request timeout — 519 of 1,660 books imported, the rest lost to "context
// deadline exceeded" (#1854). The validation reads still happen synchronously
// so a bad list id, a wrong type, a disabled list, or a missing token is still
// an immediate 4xx instead of a job that fails invisibly.
//
// Tenancy is unchanged by the move off the request: every book and author the
// sync creates is stamped with the owner recorded on the list row
// (models.ImportList.OwnerUserID), never with the identity of whoever pressed
// the button, so a background context cannot leak one user's list into
// another's library.
func (s *ListSyncer) StartOne(ctx context.Context, id int64) error {
	il, err := s.loadSyncableList(ctx, id)
	if err != nil {
		return err
	}
	if !s.syncRunning.CompareAndSwap(false, true) {
		return ErrSyncAlreadyRunning
	}
	// Publish the "running" snapshot before returning so the 202 response and
	// the first status poll can't observe a stale finished run.
	list := *il
	s.beginProgress(list, TriggerManual)

	run := func(ctx context.Context) {
		defer s.syncRunning.Store(false)
		_ = s.syncListTracked(ctx, list)
	}
	// With a jobs group wired the sync runs on the shutdown-scoped context, so
	// SIGTERM cancels and drains it before the DB closes. Fall back to an
	// untracked goroutine for tests and non-wired callers (#1458).
	if s.jobs != nil {
		// Go is a documented no-op once the group has begun shutting down. The
		// gate and the "running" snapshot are published above, so a dropped
		// launch has to be undone here or syncRunning stays true and Progress()
		// keeps describing a run that will never finish.
		if !s.jobs.Go("hardcover-list-sync", run) {
			s.syncRunning.Store(false)
			s.finishProgress(ErrShuttingDown)
			return ErrShuttingDown
		}
		return nil
	}
	go run(ctx)
	return nil
}

// loadSyncableList resolves an import list id to a list that is eligible to
// sync right now, returning the sentinel for whichever precondition failed.
// Shared by SyncOne and StartOne so the synchronous and background paths reject
// exactly the same inputs.
func (s *ListSyncer) loadSyncableList(ctx context.Context, id int64) (*models.ImportList, error) {
	il, err := s.importLists.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load import list %d: %w", id, err)
	}
	if il == nil {
		return nil, ErrNotFound
	}
	if il.Type != "hardcover" {
		return nil, ErrWrongType
	}
	if !il.Enabled {
		return nil, ErrDisabled
	}
	if s.tokenForList(ctx, *il) == "" {
		return nil, ErrMissingToken
	}
	return il, nil
}

// runList publishes the starting progress snapshot and then syncs one list.
// Callers must already hold the single-flight gate.
func (s *ListSyncer) runList(ctx context.Context, il models.ImportList, trigger string) error {
	s.beginProgress(il, trigger)
	return s.syncListTracked(ctx, il)
}

// syncListTracked syncs one list, stamps last_sync_at on success, and finishes
// the progress snapshot either way. Callers must already hold the single-flight
// gate and have published the starting snapshot.
//
// A panic anywhere in the sync (the Hardcover client, the audiobook enricher, a
// repository call) is recovered and returned as an ordinary error. Before
// #1854 this body ran inside the HTTP handler, where chi's middleware.Recoverer
// turned such a panic into a 500 and the process lived; on a background
// goroutine there is no such backstop, so without this the same panic would
// take the service down. Recovering here — rather than only in jobs.Group —
// also guarantees the progress snapshot reaches a terminal state instead of
// reading "running" forever.
func (s *ListSyncer) syncListTracked(ctx context.Context, il models.ImportList) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("hardcover list sync panicked: %v", rec)
			slog.Error("hardcover list sync panicked",
				"list", il.Name, "panic", rec, "stack", string(debug.Stack()))
			s.finishProgress(err)
		}
	}()

	err = s.syncList(ctx, il)
	if err != nil {
		slog.Error("hardcover list sync failed", "list", il.Name, "error", err)
		s.finishProgress(err)
		return err
	}
	if uerr := s.importLists.UpdateLastSyncAt(ctx, il.ID); uerr != nil {
		slog.Error("failed to update last_sync_at", "list", il.Name, "error", uerr)
	}
	s.finishProgress(nil)
	return nil
}

// beginProgress resets the snapshot for a new list pass.
func (s *ListSyncer) beginProgress(il models.ImportList, trigger string) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.progress = SyncProgress{
		Running:   true,
		ListID:    il.ID,
		ListName:  il.Name,
		Trigger:   trigger,
		StartedAt: time.Now().UTC(),
		Message:   "reading list from Hardcover…",
	}
}

// finishProgress closes out the snapshot, recording err (if any) as the
// user-visible failure reason.
func (s *ListSyncer) finishProgress(err error) {
	now := time.Now().UTC()
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.progress.Running = false
	s.progress.FinishedAt = &now
	if err != nil {
		s.progress.Error = err.Error()
		s.progress.Message = "sync failed"
		return
	}
	s.progress.Message = "sync complete"
}

// setProgress mutates the live snapshot under the lock.
func (s *ListSyncer) setProgress(mutate func(*SyncProgress)) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	mutate(&s.progress)
}

// Sentinel errors for SyncOne/StartOne so the API handler can map them to HTTP
// status codes without string-matching.
var (
	ErrNotFound     = errors.New("import list not found")
	ErrWrongType    = errors.New("import list is not a hardcover list")
	ErrDisabled     = errors.New("import list is disabled")
	ErrMissingToken = errors.New("hardcover API token is not configured")
	// ErrSyncAlreadyRunning is returned when a sync is already in flight.
	// Matches importer.ErrScanAlreadyRunning / grimmory.ErrSyncAlreadyRunning
	// so the endpoint can answer 409 instead of piling up concurrent walks of
	// the same shelf.
	ErrSyncAlreadyRunning = errors.New("hardcover list sync already running")
	// ErrShuttingDown is returned when a background sync could not be launched
	// because the process is already draining. The gate is released before it
	// is returned, so a restarted process starts clean.
	ErrShuttingDown = errors.New("server is shutting down")
)

func (s *ListSyncer) syncList(ctx context.Context, il models.ImportList) error {
	token := s.tokenForList(ctx, il)
	if token == "" {
		return ErrMissingToken
	}
	client := s.clientFactory(token)

	// Resolve the list by slug
	userLists, err := client.GetUserLists(ctx)
	if err != nil {
		return fmt.Errorf("get user lists: %w", err)
	}

	var listID int
	for _, ul := range userLists {
		if ul.Slug == il.URL {
			listID = ul.ID
			break
		}
	}
	if listID == 0 {
		return fmt.Errorf("list with slug %q not found in user's Hardcover lists", il.URL)
	}

	books, err := client.GetListBooks(ctx, listID)
	if err != nil {
		return fmt.Errorf("get list books: %w", err)
	}

	slog.Info("syncing hardcover list", "list", il.Name, "slug", il.URL, "books", len(books))
	s.setProgress(func(p *SyncProgress) {
		p.Stats.Total = len(books)
		p.Message = fmt.Sprintf("importing %d books from %s…", len(books), il.Name)
	})

	// Owner to stamp on every book and author this sync creates. The list runs
	// on a background scheduler with no request identity, so the owner is read
	// from the list row (migration 065). 0 = global (NULL owner_user_id), the
	// legacy behaviour, kept for shared shelves. Without this, scheduler-synced
	// content was always NULL-owned and therefore visible to every user under
	// tenancy — the create path the v1.25.0 audit missed (hoxtonia report).
	var ownerID int64
	if il.OwnerUserID != nil {
		ownerID = *il.OwnerUserID
	}

	// Quality profile to stamp on every author this sync creates. A list already
	// carries one (models.ImportList.QualityProfileID, set from the per-list
	// picker), but the syncer used to drop it: authors it created landed with a
	// NULL quality_profile_id, ResolveAuthorQualityProfile returned nil for them,
	// and format enforcement was skipped for every book underneath — even on a
	// list whose whole point was "audiobooks only" (#1781). Like ownerID it is
	// applied on create only, so a re-synced list never overwrites a profile the
	// user has since changed on the author.
	qualityProfileID := il.QualityProfileID

	// Index existing authors by normalized name so a Hardcover author already in
	// the library under a different provider's foreign id (e.g. an OpenLibrary
	// author imported via ABS) is reused instead of duplicated (#1223).
	nameIndex := s.buildAuthorNameIndex(ctx)

	// countStat records one book's outcome on the polled progress snapshot.
	countStat := func(mutate func(*SyncStats)) {
		s.setProgress(func(p *SyncProgress) { mutate(&p.Stats) })
	}

	for _, book := range books {
		// The sync now runs on the shutdown-scoped background context (#1854),
		// so cancellation means the process is going down: stop walking rather
		// than grinding through the remaining books with a dead context and a
		// database that is about to close.
		if err := ctx.Err(); err != nil {
			return err
		}
		countStat(func(st *SyncStats) { st.Processed++ })

		if book.ForeignID == "" {
			countStat(func(st *SyncStats) { st.Skipped++ })
			continue
		}

		// Skip if already tracked
		existing, _ := s.books.GetByForeignID(ctx, book.ForeignID)
		if existing != nil {
			slog.Debug("book already tracked, skipping", "title", book.Title, "foreignID", book.ForeignID)
			countStat(func(st *SyncStats) { st.Skipped++ })
			continue
		}

		// Look up or create the author
		authorID, err := s.ensureAuthor(ctx, &book, nameIndex, ownerID, qualityProfileID)
		if err != nil {
			slog.Warn("failed to ensure author for book", "title", book.Title, "error", err)
			countStat(func(st *SyncStats) { st.Failed++ })
			continue
		}

		// The book may already exist in the library under this author via a
		// different source (ABS, Calibre, manual) with no Hardcover foreign id.
		// Bind to that row instead of creating a duplicate "wanted" entry — the
		// canonical dedup key collapses subtitle/case/foreign-id differences
		// (#1223), the same cross-source bind the Calibre/ABS importers use.
		if owned, derr := s.books.FindByAuthorAndDedupKey(ctx, authorID, book.Title); derr != nil {
			slog.Warn("hardcover list sync: dedup-key lookup failed", "title", book.Title, "error", derr)
		} else if owned != nil {
			slog.Debug("book already owned under author, skipping", "title", book.Title, "author_id", authorID, "existing_id", owned.ID)
			countStat(func(st *SyncStats) { st.Skipped++ })
			continue
		}

		book.AuthorID = authorID
		book.Monitored = true
		book.Status = models.BookStatusWanted
		// Stamp the list owner so the synced book is scoped to that user (0 =
		// global). BookRepo.Create honours b.OwnerUserID directly.
		book.OwnerUserID = ownerID
		// A list can pin the format its books are created as, overriding the
		// Hardcover-derived media type (most works report both editions, so
		// without this an "Audiobooks" and an "Ebooks" list yield identical
		// media types). Applied on create only — books that already exist are
		// skipped above, so two single-format lists never combine into "both"
		// and a manually-set media type survives re-sync.
		if il.MediaType != "" {
			book.MediaType = il.MediaType
			// The list response may have carried an audiobook ASIN (#1694).
			// A list pinned to a non-audio format must not keep it, matching
			// the pre-existing rule that an ebook-pinned book never takes the
			// audio edition's ASIN (#1732).
			if book.MediaType != models.MediaTypeAudiobook && book.MediaType != models.MediaTypeBoth {
				book.ASIN = ""
			}
		}
		if book.Genres == nil {
			book.Genres = []string{}
		}

		if err := s.books.Create(ctx, &book); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				slog.Debug("book already exists (race)", "title", book.Title)
				countStat(func(st *SyncStats) { st.Skipped++ })
				continue
			}
			slog.Warn("failed to create book", "title", book.Title, "error", err)
			countStat(func(st *SyncStats) { st.Failed++ })
			continue
		}
		countStat(func(st *SyncStats) { st.Imported++ })
		slog.Info("imported book from hardcover list", "title", book.Title, "author_id", authorID)

		// The list response can carry an audiobook ASIN inline (#1694). When
		// it does, run the same Audnex enrichment the per-book edition
		// hydration used to trigger on ASIN promotion — narrator, refined
		// duration, summary — then persist. Failures are logged and skipped:
		// enrichment is best-effort and must not block the import loop.
		if s.enricher != nil && book.ASIN != "" {
			if err := s.enricher.EnrichAudiobook(ctx, &book); err != nil {
				slog.Debug("audiobook enrichment skipped", "title", book.Title, "asin", book.ASIN, "error", err)
			} else if err := s.books.Update(ctx, &book); err != nil {
				slog.Warn("failed to persist enriched book", "title", book.Title, "error", err)
			}
		}

		s.linkSeriesRefs(ctx, &book)
	}

	return nil
}

// linkSeriesRefs persists each Hardcover SeriesRef into the series table and
// links it to the freshly imported book. Best-effort: a failed link must not
// roll back or block the book import — log and move on.
func (s *ListSyncer) linkSeriesRefs(ctx context.Context, book *models.Book) {
	if s.series == nil || len(book.SeriesRefs) == 0 || book.ID == 0 {
		return
	}
	for _, ref := range book.SeriesRefs {
		ser := &models.Series{ForeignID: ref.ForeignID, Title: ref.Title}
		if err := s.series.CreateOrGet(ctx, ser); err != nil {
			slog.Warn("hardcover list sync: upsert series failed", "series", ref.Title, "book", book.Title, "error", err)
			continue
		}
		if err := s.series.LinkBook(ctx, ser.ID, book.ID, ref.Position, ref.Primary); err != nil {
			slog.Warn("hardcover list sync: link book to series failed", "series", ref.Title, "book", book.Title, "error", err)
		}
	}
}

func (s *ListSyncer) tokenForList(ctx context.Context, il models.ImportList) string {
	if token := hardcover.NormalizeAPIToken(il.APIKey); token != "" {
		return token
	}
	if s.tokenSource == nil {
		return ""
	}
	return hardcover.NormalizeAPIToken(s.tokenSource(ctx))
}

// buildAuthorNameIndex maps each existing author's normalized name to the
// matching rows. Used by ensureAuthor to reconcile a Hardcover author against
// one already in the library under a different provider's foreign id (#1223).
// A failed list is non-fatal: the index is empty and ensureAuthor falls back to
// creating authors as before.
func (s *ListSyncer) buildAuthorNameIndex(ctx context.Context) map[string][]models.Author {
	index := make(map[string][]models.Author)
	authors, err := s.authors.List(ctx)
	if err != nil {
		slog.Warn("hardcover list sync: failed to index authors by name; duplicate-author guard disabled", "error", err)
		return index
	}
	for i := range authors {
		key := textutil.NormalizeAuthorName(authors[i].Name)
		if key == "" {
			continue
		}
		index[key] = append(index[key], authors[i])
	}
	return index
}

// uniqueAuthorByName returns the single existing author whose normalized name
// matches, or nil when there is no match or the match is ambiguous (more than
// one author shares the normalized name — e.g. disambiguated namesakes). The
// ambiguous case deliberately falls through to creating a new author rather
// than guessing which namesake to merge into.
func uniqueAuthorByName(index map[string][]models.Author, name string) *models.Author {
	key := textutil.NormalizeAuthorName(name)
	if key == "" {
		return nil
	}
	matches := index[key]
	if len(matches) != 1 {
		return nil
	}
	return &matches[0]
}

// ensureAuthor looks up the author by foreign ID, then by normalized name,
// creating a minimal record only if neither matches. Returns the author's
// database ID. ownerID (0 = global) and qualityProfileID (nil = the list has
// none configured) are stamped only on a freshly created author; a reused
// existing author keeps whatever owner and profile it already had, so a list
// never silently reassigns another user's — or a shared/global — author.
func (s *ListSyncer) ensureAuthor(ctx context.Context, book *models.Book, nameIndex map[string][]models.Author, ownerID int64, qualityProfileID *int64) (int64, error) {
	if book.Author == nil {
		return 0, fmt.Errorf("book %q has no author metadata", book.Title)
	}

	existing, err := s.authors.GetByAnyForeignID(ctx, book.Author.ForeignID)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		return existing.ID, nil
	}

	// Name fallback: reuse an existing author with the same normalized name
	// instead of spawning a parallel row, and attach the Hardcover foreign id
	// as an alias so future syncs match by id (#1223).
	if matched := uniqueAuthorByName(nameIndex, book.Author.Name); matched != nil {
		if fid := strings.TrimSpace(book.Author.ForeignID); fid != "" {
			if err := s.authors.UpsertAuthorIdentifier(ctx, matched.ID, fid); err != nil && !errors.Is(err, db.ErrAuthorIdentifierConflict) {
				slog.Warn("hardcover list sync: failed to attach author alias", "author", matched.Name, "foreignID", fid, "error", err)
			}
		}
		return matched.ID, nil
	}

	// Create a minimal author record. List-sync authors are kept monitored so
	// the scheduler refreshes their metadata, but their MonitorMode is pinned to
	// "none": only the specific book(s) on the user's Hardcover list should end
	// up wanted, never the author's entire back-catalogue (#1290). The listed
	// book is monitored explicitly in syncList (book.Monitored = true), and the
	// catalogue-discovery pass (FetchAuthorBooks) leaves already-tracked books
	// untouched while gating newly-discovered works on shouldMonitorBookForAuthor
	// — which returns false under "none". Leaving MonitorMode == "" would instead
	// make that predicate treat the author as "all" and auto-want every work.
	author := book.Author
	author.Monitored = true
	author.MonitorMode = models.AuthorMonitorModeNone
	author.MetadataProvider = "hardcover"
	if author.SortName == "" {
		author.SortName = sortName(author.Name)
	}
	// Mirror applyAuthorCreateOptions' fallback (internal/api/authors.go) so a
	// list-sync-created author gets the same default metadata profile every
	// other creation path stamps, instead of staying unset (#1736).
	if author.MetadataProfileID == nil {
		def := models.DefaultMetadataProfileID
		author.MetadataProfileID = &def
	}
	// Carry the list's own quality profile onto the author so the format filter
	// the user configured on the list is actually enforced for its books
	// (#1781). Copied by value: the same pointer is reused for every author in
	// the sync, and each row must own its field.
	//
	// There is deliberately no fallback to the seeded id=1 profile when the list
	// has none. Unlike metadata profiles, quality profiles are user-scoped:
	// migration 025 backfills owner_user_id=1 onto every profile that predates
	// multi-user, so on a tenanted install "profile 1" is one particular user's
	// private profile, and stamping it here would hand it to authors created by
	// somebody else's list. A list with no profile configured therefore still
	// leaves this nil, which ResolveAuthorQualityProfile documents as "no format
	// filter" — the same result as today, but now fixable from the list's
	// settings instead of being unreachable.
	if qualityProfileID != nil {
		id := *qualityProfileID
		author.QualityProfileID = &id
	}

	if err := s.authors.CreateForUser(ctx, author, ownerID); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || errors.Is(err, db.ErrAuthorIdentifierConflict) {
			// Race: author created between our check and insert
			existing, _ = s.authors.GetByAnyForeignID(ctx, author.ForeignID)
			if existing != nil {
				return existing.ID, nil
			}
		}
		return 0, fmt.Errorf("create author %q: %w", author.Name, err)
	}
	slog.Info("created author from hardcover list", "name", author.Name, "foreignID", author.ForeignID)
	return author.ID, nil
}

func sortName(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	last := parts[len(parts)-1]
	rest := strings.Join(parts[:len(parts)-1], " ")
	return last + ", " + rest
}

// HCListSyncer is satisfied by *ListSyncer — the scheduler uses this
// interface to avoid a direct import of this package.
type HCListSyncer interface {
	Sync(ctx context.Context) error
}

// Ensure ListSyncer implements HCListSyncer at compile time.
var _ HCListSyncer = (*ListSyncer)(nil)

// RunSync implements the scheduler.CalibreSyncer-style signature so the
// scheduler can call a single method with no return value, ignoring errors
// (they are already logged inside Sync).
func (s *ListSyncer) RunSync(ctx context.Context) {
	if err := s.Sync(ctx); err != nil {
		slog.Error("hardcover list sync error (top-level)", "error", err)
	}
}

// Ensure *ListSyncer satisfies the narrow RunSync shape used by the scheduler.
var _ interface{ RunSync(context.Context) } = (*ListSyncer)(nil)
