// Package audiblesynclistsyncer syncs Audible lists, read through a
// self-hosted Audible Sync instance, into Bindery's catalogue as wanted
// audiobooks: the Audible Sync counterpart of hardcoverlistsyncer.
//
// A list reads one of two sources (models.ImportList.URL): the account's
// wishlist, or the books in its library Audible no longer lets it download,
// typically titles withdrawn from the Plus catalogue. Audible does bring such
// titles back, and Audible Sync then downloads them, so an unavailable books
// list stops searching for a book it added once that happens.
//
// Each item is an ASIN. It is resolved to the primary provider's work
// where one matches (the same canonicalisation Add Book by ASIN uses), so a
// wishlist book links to the work the rest of the library uses; an audiobook
// the primary provider does not know is added as an Audible book under its
// author, resolved the same way. An item whose author cannot be resolved is
// skipped rather than given a synthetic author no refresh could ever fill.
//
// A list can also skip books the Audible account already owns (Audible Sync
// downloads those itself) and take books off the wishlist once Bindery has
// imported them.
package audiblesynclistsyncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vavallee/bindery/internal/audiblesync"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// Trigger values reported on SyncProgress.
const (
	TriggerManual    = "manual"
	TriggerScheduled = "scheduled"
)

// SyncStats summarises one list pass.
type SyncStats struct {
	// Total is the number of books on the wishlist.
	Total     int `json:"total"`
	Processed int `json:"processed"`
	// Imported books were created as wanted audiobooks.
	Imported int `json:"imported"`
	// Skipped books were already tracked, excluded, or owned by another user.
	Skipped int `json:"skipped"`
	// Owned books are on the wishlist but the Audible account owns them, and
	// the list skips owned books.
	Owned int `json:"owned"`
	// Unresolved books have an author the metadata provider could not match.
	Unresolved int `json:"unresolved"`
	// Removed books were taken off the Audible wishlist after Bindery
	// imported them.
	Removed int `json:"removed"`
	// Released books, added by an unavailable books list, are downloadable on
	// Audible again: Audible Sync is fetching them, so they are no longer
	// searched for.
	Released int `json:"released"`
	Failed   int `json:"failed"`
}

// SyncProgress is the polled shape for GET /importlist/sync/status?type=audiblesync.
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

// Sentinel errors, mapped to HTTP status codes by the API.
var (
	ErrNotFound           = errors.New("import list not found")
	ErrWrongType          = errors.New("import list is not an Audible Sync list")
	ErrDisabled           = errors.New("import list is disabled")
	ErrNotConfigured      = errors.New("the Audible Sync connection is not configured: enable it and save its URL and token in Settings, Audible Sync")
	ErrSyncAlreadyRunning = errors.New("a wishlist sync from Audible Sync is already running")
	ErrShuttingDown       = errors.New("server is shutting down")
)

// Client is what the syncer asks of Audible Sync.
type Client interface {
	Wishlist(ctx context.Context, accountID int) ([]audiblesync.WishlistItem, []audiblesync.WishlistAccount, error)
	RemoveFromWishlist(ctx context.Context, accountID int, asin string) error
	Library(ctx context.Context, accountID int, status string) ([]audiblesync.LibraryBook, error)
	LibraryBook(ctx context.Context, id int) (*audiblesync.LibraryBook, error)
	CatalogBook(ctx context.Context, asin string, accountID int) (*audiblesync.CatalogBook, error)
}

// ClientSource builds a client from the current Audible Sync settings, or
// returns ErrNotConfigured. Read on every pass, so a settings change applies
// to the next sync without a restart.
type ClientSource func(ctx context.Context) (Client, error)

// Resolver maps Audible data onto the primary metadata provider. Satisfied by
// *metadata.Aggregator.
type Resolver interface {
	GetCanonicalBookByASIN(ctx context.Context, asin string) (*models.Book, error)
	ResolveCanonicalAuthor(ctx context.Context, name string) (*models.Author, error)
}

// wantedSearcher starts the immediate search a newly wanted book earns.
// *scheduler.Scheduler satisfies it.
type wantedSearcher interface {
	SearchAndGrabBook(ctx context.Context, book models.Book)
}

// Searches a pass dispatches are bounded and paced like the Hardcover list
// sync's and the scheduled sweep's (#1515).
const searchConcurrency = 2

// searchPace is a variable so tests can shorten it.
var searchPace = 3 * time.Second

// ListSyncer syncs enabled Audible Sync wishlist lists.
type ListSyncer struct {
	importLists *db.ImportListRepo
	authors     *db.AuthorRepo
	books       *db.BookRepo

	clients  ClientSource
	resolver Resolver
	searcher wantedSearcher
	jobs     *jobs.Group

	syncRunning atomic.Bool

	progressMu sync.Mutex
	progress   SyncProgress
}

// New returns a syncer. clients and resolver are required; a nil client
// source makes every list report ErrNotConfigured.
func New(importLists *db.ImportListRepo, authors *db.AuthorRepo, books *db.BookRepo, clients ClientSource, resolver Resolver) *ListSyncer {
	return &ListSyncer{importLists: importLists, authors: authors, books: books, clients: clients, resolver: resolver}
}

// WithSearcher wires the search started when a pass makes a book wanted and
// monitored.
func (s *ListSyncer) WithSearcher(searcher wantedSearcher) *ListSyncer {
	if searcher != nil {
		s.searcher = searcher
	}
	return s
}

// WithJobs tracks background syncs in the process-wide jobs group, so
// shutdown cancels and drains them before the database closes (#1458).
func (s *ListSyncer) WithJobs(g *jobs.Group) *ListSyncer {
	s.jobs = g
	return s
}

// Progress returns a snapshot of the current or most recent pass.
func (s *ListSyncer) Progress() SyncProgress {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	return s.progress
}

// Sync runs every enabled Audible Sync list. The scheduler calls it.
func (s *ListSyncer) Sync(ctx context.Context) error {
	if s.jobs == nil {
		return s.syncAll(ctx)
	}
	var err error
	if !s.jobs.Run("audiblesync-list-sync-scheduled", func(jobCtx context.Context) {
		err = s.syncAll(jobCtx)
	}) {
		slog.Info("Audible Sync wishlist sync skipped; process is shutting down")
		return nil
	}
	return err
}

func (s *ListSyncer) syncAll(ctx context.Context) error {
	if !s.syncRunning.CompareAndSwap(false, true) {
		slog.Info("Audible Sync wishlist sync already running; skipping scheduled run")
		return nil
	}
	defer s.syncRunning.Store(false)

	lists, err := s.importLists.ListByType(ctx, models.ImportListTypeAudibleSync)
	if err != nil {
		return fmt.Errorf("list Audible Sync import lists: %w", err)
	}
	var firstErr error
	for _, il := range lists {
		s.beginProgress(il, TriggerScheduled)
		if err := s.syncListTracked(ctx, il); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RunSync is Sync for callers that only log errors.
func (s *ListSyncer) RunSync(ctx context.Context) {
	if err := s.Sync(ctx); err != nil {
		slog.Error("Audible Sync wishlist sync error (top-level)", "error", err)
	}
}

// SyncOne syncs one list and waits for it.
func (s *ListSyncer) SyncOne(ctx context.Context, id int64) error {
	il, err := s.loadSyncableList(ctx, id)
	if err != nil {
		return err
	}
	if !s.syncRunning.CompareAndSwap(false, true) {
		return ErrSyncAlreadyRunning
	}
	defer s.syncRunning.Store(false)
	s.beginProgress(*il, TriggerManual)
	return s.syncListTracked(ctx, *il)
}

// StartOne validates the list and runs its sync in the background; the API's
// "Sync now" calls it and answers 202. Every book created is stamped with the
// list's owner, never with whoever pressed the button.
func (s *ListSyncer) StartOne(ctx context.Context, id int64) error {
	il, err := s.loadSyncableList(ctx, id)
	if err != nil {
		return err
	}
	if !s.syncRunning.CompareAndSwap(false, true) {
		return ErrSyncAlreadyRunning
	}
	list := *il
	s.beginProgress(list, TriggerManual)
	run := func(ctx context.Context) {
		defer s.syncRunning.Store(false)
		_ = s.syncListTracked(ctx, list)
	}
	if s.jobs != nil {
		if !s.jobs.Go("audiblesync-list-sync", run) {
			s.syncRunning.Store(false)
			s.finishProgress(ErrShuttingDown)
			return ErrShuttingDown
		}
		return nil
	}
	go run(ctx)
	return nil
}

func (s *ListSyncer) loadSyncableList(ctx context.Context, id int64) (*models.ImportList, error) {
	il, err := s.importLists.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load import list %d: %w", id, err)
	}
	if il == nil {
		return nil, ErrNotFound
	}
	if il.Type != models.ImportListTypeAudibleSync {
		return nil, ErrWrongType
	}
	if !il.Enabled {
		return nil, ErrDisabled
	}
	if s.clients == nil {
		return nil, ErrNotConfigured
	}
	if _, err := s.clients(ctx); err != nil {
		return nil, err
	}
	return il, nil
}

// syncListTracked runs one pass, stamps last_sync_at on success, and always
// finishes the progress snapshot, recovering a panic into an error so a
// background pass cannot take the process down.
func (s *ListSyncer) syncListTracked(ctx context.Context, il models.ImportList) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("the Audible Sync wishlist sync panicked: %v", rec)
			slog.Error("Audible Sync wishlist sync panicked", "list", il.Name, "panic", rec, "stack", string(debug.Stack()))
			s.finishProgress(err)
		}
	}()
	if err = s.syncList(ctx, il); err != nil {
		slog.Error("Audible Sync wishlist sync failed", "list", il.Name, "error", err)
		s.finishProgress(err)
		return err
	}
	if uerr := s.importLists.UpdateLastSyncAt(ctx, il.ID); uerr != nil {
		slog.Error("failed to update last_sync_at", "list", il.Name, "error", uerr)
	}
	s.finishProgress(nil)
	return nil
}

func (s *ListSyncer) beginProgress(il models.ImportList, trigger string) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.progress = SyncProgress{
		Running: true, ListID: il.ID, ListName: il.Name, Trigger: trigger,
		StartedAt: time.Now().UTC(), Message: "reading the list from Audible Sync…",
	}
}

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

func (s *ListSyncer) count(mutate func(*SyncStats)) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	mutate(&s.progress.Stats)
}

// listAccount is the Audible Sync account a list reads; zero means the
// connection's default account.
func listAccount(il models.ImportList) int {
	n, _ := strconv.Atoi(strings.TrimSpace(il.Account))
	return max(n, 0)
}

// listSource is the source a list reads; an empty URL is the wishlist.
func listSource(il models.ImportList) string {
	if strings.TrimSpace(il.URL) == models.ImportListSourceUnavailable {
		return models.ImportListSourceUnavailable
	}
	return models.ImportListSourceWishlist
}

// releaseReturned stops searching for the books an unavailable books list
// created that Audible lets the account download again. Audible Sync fetches
// those itself, so a search would only find a second copy.
//
// Only the list's own books are touched (import_list_books), and only while
// they are still wanted and monitored: an imported book, or one the operator
// has since changed by hand, is forgotten and left as it is. A book still on
// the list costs nothing. Any other is looked up by its Audible Sync id, one
// request each, never by reading the whole library:
//   - waiting, downloading or downloaded: released (unmonitored) and forgotten;
//   - gone from the library (refunded, say): forgotten, and still searched for;
//   - failed, or withdrawn again: kept, and asked after on the next sync.
func (s *ListSyncer) releaseReturned(ctx context.Context, client Client, il models.ImportList, stillUnavailable map[string]bool) {
	created, err := s.importLists.ListBooks(ctx, il.ID)
	if err != nil {
		slog.Warn("Audible Sync list: could not read the books it created", "list", il.Name, "error", err)
		return
	}
	forget := func(rec models.ImportListBook) {
		if err := s.importLists.ForgetBook(ctx, il.ID, rec.BookID); err != nil {
			slog.Warn("Audible Sync list: could not forget a book", "list", il.Name, "book_id", rec.BookID, "error", err)
		}
	}
	for _, rec := range created {
		if ctx.Err() != nil {
			return
		}
		if stillUnavailable[isbnutil.NormalizeASIN(rec.ASIN)] {
			continue
		}
		book, err := s.books.GetByID(ctx, rec.BookID)
		if err != nil {
			slog.Warn("Audible Sync list: could not read a book it created", "list", il.Name, "book_id", rec.BookID, "error", err)
			continue
		}
		if book == nil || book.Status != models.BookStatusWanted || !book.Monitored {
			// Imported, or changed by hand: there is no search to stop.
			forget(rec)
			continue
		}
		if rec.SourceID <= 0 {
			// Nothing to ask Audible Sync with; keep searching, as for a
			// book that left the library.
			forget(rec)
			continue
		}
		lb, err := client.LibraryBook(ctx, int(rec.SourceID))
		if errors.Is(err, audiblesync.ErrNotFound) {
			slog.Info("Audible Sync list: book left the Audible library; still searching for it",
				"list", il.Name, "title", book.Title, "asin", rec.ASIN)
			forget(rec)
			continue
		}
		if err != nil {
			slog.Warn("Audible Sync list: could not check a book that left the unavailable list", "list", il.Name, "title", book.Title, "error", err)
			continue
		}
		switch lb.Status {
		case audiblesync.LibraryStatusWaiting, audiblesync.LibraryStatusDownloading, audiblesync.LibraryStatusDownloaded:
		default:
			// Failed in Audible Sync, or withdrawn again since the list was
			// read: it is not fetching the book, so keep searching.
			continue
		}
		book.Monitored = false
		if err := s.books.Update(ctx, book); err != nil {
			slog.Warn("Audible Sync list: could not stop searching for a returned book", "title", book.Title, "error", err)
			continue
		}
		slog.Info("Audible Sync list: book is downloadable on Audible again; no longer searching for it",
			"list", il.Name, "title", book.Title, "asin", rec.ASIN)
		s.count(func(st *SyncStats) { st.Released++ })
		forget(rec)
	}
}

// scopeUserID is the identity the create-versus-skip reads run under: the list
// owner while tenancy enforcement is on, 0 (unscoped) otherwise. Same rule as
// the Hardcover list sync (#2766).
func scopeUserID(ownerID int64) int64 {
	if !auth.EnforceTenancy() {
		return 0
	}
	return ownerID
}

func (s *ListSyncer) syncList(ctx context.Context, il models.ImportList) error {
	if s.clients == nil {
		return ErrNotConfigured
	}
	client, err := s.clients(ctx)
	if err != nil {
		return err
	}
	accountID := listAccount(il)
	unavailableList := listSource(il) == models.ImportListSourceUnavailable
	var (
		items  []audiblesync.WishlistItem
		states []audiblesync.WishlistAccount
	)
	if unavailableList {
		books, err := client.Library(ctx, accountID, audiblesync.LibraryStatusUnavailable)
		if err != nil {
			return fmt.Errorf("read the unavailable Audible books: %w", err)
		}
		for _, b := range books {
			items = append(items, audiblesync.WishlistItem{CatalogBook: b.CatalogBook(), AccountID: b.AccountID})
		}
		// Every one of these books is owned, and none is on a wishlist.
		il.SkipOwned, il.RemoveOnImport = false, false
	} else {
		items, states, err = client.Wishlist(ctx, accountID)
		if err != nil {
			return fmt.Errorf("read the Audible wishlist: %w", err)
		}
	}
	for _, st := range states {
		if accountID > 0 && st.AccountID != accountID {
			continue
		}
		// Audible Sync serves the wishlist as last read; say when that read
		// is stale so a quiet list is not mistaken for an empty wishlist.
		if st.NeedsLogin || st.Error != "" {
			slog.Warn("Audible Sync wishlist is as last read: its latest read failed",
				"list", il.Name, "account", st.AccountID, "needs_login", st.NeedsLogin, "error", st.Error, "read_at", st.ReadAt)
		}
	}

	var ownerID int64
	if il.OwnerUserID != nil {
		ownerID = *il.OwnerUserID
	}
	scope := scopeUserID(ownerID)

	asins := make([]string, 0, len(items))
	for _, it := range items {
		asins = append(asins, it.ASIN)
	}
	tracked, err := s.books.ListByASINsVisibleTo(ctx, asins, scope)
	if err != nil {
		return fmt.Errorf("look up wishlist books in the library: %w", err)
	}
	excluded := s.exclusionSet(ctx)
	nameIndex := s.buildAuthorNameIndex(ctx, scope)

	slog.Info("syncing Audible Sync list", "list", il.Name, "source", listSource(il), "account", accountID, "books", len(items))
	s.progressMu.Lock()
	s.progress.Stats.Total = len(items)
	s.progress.Message = fmt.Sprintf("importing %d books from %s…", len(items), il.Name)
	s.progressMu.Unlock()

	var searchTargets []models.Book
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.count(func(st *SyncStats) { st.Processed++ })
		asin := isbnutil.NormalizeASIN(item.ASIN)
		if asin == "" {
			s.count(func(st *SyncStats) { st.Skipped++ })
			continue
		}

		// The account this item belongs to: the list's, or with none named
		// the one Audible Sync answered for.
		account := accountID
		if account == 0 {
			account = item.AccountID
		}
		existing, isTracked := tracked[asin]
		// Only once Bindery holds the audiobook: an imported ebook of the
		// same book still wants its audiobook, which is why it is listed.
		if isTracked && il.RemoveOnImport &&
			existing.Status == models.BookStatusImported && existing.AudiobookFilePath != "" {
			s.removeFromWishlist(ctx, client, il, item, asin)
			continue
		}
		// Before widening a tracked ebook: Audible Sync downloads what the
		// account owns, so searching for it would fetch a second copy.
		if il.SkipOwned && item.OwnedBy(account) {
			slog.Debug("Audible Sync wishlist: owned on Audible, skipping", "title", item.Title, "asin", asin)
			s.count(func(st *SyncStats) { st.Owned++ })
			continue
		}
		if isTracked {
			if target, ok := s.widen(ctx, &existing, asin); ok {
				searchTargets = append(searchTargets, target)
			}
			s.count(func(st *SyncStats) { st.Skipped++ })
			continue
		}
		if excluded[audiblesync.IDPrefix+asin] {
			s.count(func(st *SyncStats) { st.Skipped++ })
			continue
		}

		book, ok := s.resolve(ctx, item, asin)
		if !ok {
			s.count(func(st *SyncStats) { st.Unresolved++ })
			continue
		}
		if excluded[book.ForeignID] {
			s.count(func(st *SyncStats) { st.Skipped++ })
			continue
		}

		var sourceID int64
		if c, ok := item.CopyIn(account); ok {
			sourceID = int64(c.BookID)
		}
		if book.Description == "" {
			s.describe(ctx, client, book, item, account, sourceID)
		}
		target, outcome := s.addBook(ctx, il, book, sourceID, ownerID, scope, nameIndex)
		switch outcome {
		case outcomeImported:
			s.count(func(st *SyncStats) { st.Imported++ })
		case outcomeSkipped:
			s.count(func(st *SyncStats) { st.Skipped++ })
		case outcomeFailed:
			s.count(func(st *SyncStats) { st.Failed++ })
		}
		if target != nil {
			searchTargets = append(searchTargets, *target)
		}
	}

	if unavailableList {
		current := make(map[string]bool, len(items))
		for _, it := range items {
			current[isbnutil.NormalizeASIN(it.ASIN)] = true
		}
		s.releaseReturned(ctx, client, il, current)
	}

	if len(searchTargets) > 0 && s.searcher != nil {
		go concurrency.RunBoundedPaced(ctx, searchTargets, searchConcurrency, searchPace, func(ctx context.Context, b models.Book) {
			s.searcher.SearchAndGrabBook(indexer.WithSearchOrigin(ctx, indexer.OriginListSync), b)
		})
	}
	return nil
}

// describe fills the empty description of a book about to be added, from
// the first of these that has one:
//   - the wishlist item itself, from an Audible Sync that stores descriptions
//     with the wishlist;
//   - the account's copy in Audible Sync's library, when it owns the book
//     (every unavailable book): Audible Sync's own database, not Audible;
//   - for a wishlist book, a catalogue lookup: a live request to Audible
//     through the account, so only for a book being added, and one at a time
//     (the sync adds books in turn).
//
// A failed read leaves the book without one; it never stops the add.
func (s *ListSyncer) describe(ctx context.Context, client Client, book *models.Book, item audiblesync.WishlistItem, account int, sourceID int64) {
	if d := strings.TrimSpace(item.Description); d != "" {
		book.Description = d
		return
	}
	if sourceID > 0 {
		// Owned: the library copy is the source, and the catalogue holds
		// nothing it does not, so a miss here is not worth a live request.
		lb, err := client.LibraryBook(ctx, int(sourceID))
		if err != nil {
			slog.Debug("Audible Sync list: could not read a library book's description", "title", book.Title, "asin", book.ASIN, "error", err)
			return
		}
		book.Description = strings.TrimSpace(lb.Description)
		return
	}
	cb, err := client.CatalogBook(ctx, book.ASIN, account)
	if err != nil {
		slog.Debug("Audible Sync list: could not look a book's description up in the catalogue", "title", book.Title, "asin", book.ASIN, "error", err)
		return
	}
	book.Description = strings.TrimSpace(cb.Description)
}

// removeFromWishlist takes an imported book off the Audible wishlist. The
// item's own account is used when the list reads Audible Sync's default, since
// removal must name one.
func (s *ListSyncer) removeFromWishlist(ctx context.Context, client Client, il models.ImportList, item audiblesync.WishlistItem, asin string) {
	account := listAccount(il)
	if account == 0 {
		account = item.AccountID
	}
	if err := client.RemoveFromWishlist(ctx, account, asin); err != nil {
		slog.Warn("Audible Sync wishlist: could not remove an imported book", "title", item.Title, "asin", asin, "error", err)
		s.count(func(st *SyncStats) { st.Failed++ })
		return
	}
	slog.Info("Audible Sync wishlist: removed an imported book from the Audible wishlist", "title", item.Title, "asin", asin, "account", account)
	s.count(func(st *SyncStats) { st.Removed++ })
}

// resolve turns a wishlist item into the book to add: the primary provider's
// work when the ASIN canonicalises to one, otherwise the Audible book under an
// author the primary provider knows. Either way it is an audiobook carrying the
// item's ASIN, narrator, runtime and cover.
func (s *ListSyncer) resolve(ctx context.Context, item audiblesync.WishlistItem, asin string) (*models.Book, bool) {
	fromAudible := audiblesync.ToBook(item.CatalogBook)
	if fromAudible == nil {
		return nil, false
	}
	var book *models.Book
	if s.resolver != nil {
		canonical, err := s.resolver.GetCanonicalBookByASIN(ctx, asin)
		if err != nil {
			slog.Debug("Audible Sync wishlist: ASIN canonicalisation failed; trying the Audible record", "asin", asin, "error", err)
		}
		if canonical != nil && canonical.ForeignID != "" && canonical.Author != nil && canonical.Author.ForeignID != "" {
			copied := *canonical
			author := *canonical.Author
			copied.Author = &author
			book = &copied
		}
	}
	if book == nil {
		name := audiblesync.FirstAuthor(item.CatalogBook)
		if name == "" || s.resolver == nil {
			return nil, false
		}
		author, err := s.resolver.ResolveCanonicalAuthor(ctx, name)
		if err != nil || author == nil || author.ForeignID == "" {
			slog.Info("Audible Sync wishlist: skipped, its author is unknown to the metadata provider",
				"title", item.Title, "asin", asin, "author", name, "error", err)
			return nil, false
		}
		copied := *author
		book = fromAudible
		book.Author = &copied
	}
	book.ASIN = asin
	book.MediaType = models.MediaTypeAudiobook
	if book.Narrator == "" {
		book.Narrator = fromAudible.Narrator
	}
	if book.DurationSeconds == 0 {
		book.DurationSeconds = fromAudible.DurationSeconds
	}
	if book.ImageURL == "" {
		book.ImageURL = fromAudible.ImageURL
	}
	if book.Language == "" {
		book.Language = fromAudible.Language
	}
	if book.ReleaseDate == nil {
		book.ReleaseDate = fromAudible.ReleaseDate
	}
	// Series are not linked from the wishlist yet: Audible's series keys do
	// not match the series the primary provider files the book under, and a
	// second series row for the same series is worse than none.
	book.SeriesRefs = nil
	if book.Genres == nil {
		book.Genres = []string{}
	}
	return book, true
}

type outcome int

const (
	outcomeImported outcome = iota
	outcomeSkipped
	outcomeFailed
)

// addBook creates book unless the library already has it, and returns the
// book to search for when the pass made it wanted and monitored. sourceID is
// Audible Sync's library id for the book, 0 when the account has no copy.
// A failed duplicate check fails the book rather than risk a second copy.
func (s *ListSyncer) addBook(ctx context.Context, il models.ImportList, book *models.Book, sourceID, ownerID, scope int64, nameIndex map[string][]models.Author) (*models.Book, outcome) {
	existing, err := s.books.GetByForeignIDVisibleTo(ctx, book.ForeignID, scope)
	if err != nil {
		slog.Warn("Audible Sync wishlist: lookup by foreign id failed", "title", book.Title, "foreignID", book.ForeignID, "error", err)
		return nil, outcomeFailed
	}
	if existing == nil && scope != 0 {
		foreign, err := s.books.GetByForeignID(ctx, book.ForeignID)
		if err != nil {
			slog.Warn("Audible Sync wishlist: lookup by foreign id failed", "title", book.Title, "foreignID", book.ForeignID, "error", err)
			return nil, outcomeFailed
		}
		if foreign != nil {
			slog.Info("Audible Sync wishlist: book skipped, its foreign id belongs to another user",
				"title", book.Title, "foreignID", book.ForeignID, "list_owner_user_id", scope)
			return nil, outcomeSkipped
		}
	}
	if existing != nil {
		return s.widenTarget(ctx, existing, book.ASIN), outcomeSkipped
	}

	authorID, err := s.ensureAuthor(ctx, book.Author, nameIndex, ownerID, scope, il.QualityProfileID, il.RootFolderID)
	if err != nil {
		slog.Warn("Audible Sync wishlist: failed to ensure author", "title", book.Title, "error", err)
		return nil, outcomeFailed
	}
	if owned, err := s.books.FindByAuthorAndDedupKeyVisibleTo(ctx, authorID, book.Title, scope); err != nil {
		slog.Warn("Audible Sync wishlist: dedup lookup failed", "title", book.Title, "error", err)
		return nil, outcomeFailed
	} else if owned != nil {
		return s.widenTarget(ctx, owned, book.ASIN), outcomeSkipped
	}

	book.AuthorID = authorID
	book.Monitored = il.MonitorNew
	book.Status = models.BookStatusWanted
	book.OwnerUserID = ownerID
	if err := s.books.Create(ctx, book); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, outcomeSkipped
		}
		slog.Warn("Audible Sync wishlist: failed to create book", "title", book.Title, "error", err)
		return nil, outcomeFailed
	}
	slog.Info("imported book from Audible Sync list", "list", il.Name, "title", book.Title, "asin", book.ASIN, "author_id", authorID)
	if err := s.importLists.RecordBook(ctx, il.ID, book.ID, book.ASIN, sourceID); err != nil {
		slog.Warn("Audible Sync list: could not record the book it created", "title", book.Title, "error", err)
	}
	if book.BecameSearchable("", false) {
		return book, outcomeImported
	}
	return nil, outcomeImported
}

// widen makes an existing ebook want its audiobook too, since it is on the
// Audible wishlist, and records the ASIN when the row has none. It returns
// the book to search for when that moved it into wanted and monitored.
func (s *ListSyncer) widen(ctx context.Context, existing *models.Book, asin string) (models.Book, bool) {
	changed := false
	prevStatus, prevMonitored := existing.Status, existing.Monitored
	if existing.MediaType == models.MediaTypeEbook {
		existing.MediaType = models.MediaTypeBoth
		existing.ReevaluateStatus()
		changed = true
	}
	if existing.ASIN == "" && asin != "" &&
		(existing.MediaType == models.MediaTypeAudiobook || existing.MediaType == models.MediaTypeBoth) {
		existing.ASIN = asin
		changed = true
	}
	if !changed {
		return models.Book{}, false
	}
	if err := s.books.Update(ctx, existing); err != nil {
		slog.Warn("Audible Sync wishlist: failed to widen a tracked book", "title", existing.Title, "error", err)
		return models.Book{}, false
	}
	slog.Info("Audible Sync wishlist: tracked book now wants its audiobook", "title", existing.Title, "book_id", existing.ID)
	return *existing, existing.BecameSearchable(prevStatus, prevMonitored)
}

// widenTarget is widen, returning the book to search for or nil.
func (s *ListSyncer) widenTarget(ctx context.Context, existing *models.Book, asin string) *models.Book {
	if target, ok := s.widen(ctx, existing, asin); ok {
		return &target
	}
	return nil
}

// exclusionSet is the import list exclusions, by foreign id. A failed read is
// logged and treated as none.
func (s *ListSyncer) exclusionSet(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	ex, err := s.importLists.ListExclusions(ctx)
	if err != nil {
		slog.Warn("Audible Sync wishlist: could not read import list exclusions", "error", err)
		return out
	}
	for _, e := range ex {
		out[strings.TrimSpace(e.ForeignID)] = true
	}
	return out
}

// buildAuthorNameIndex maps normalised author names to the authors scope can
// see, so an author already in the library is reused (#1223).
func (s *ListSyncer) buildAuthorNameIndex(ctx context.Context, scope int64) map[string][]models.Author {
	index := map[string][]models.Author{}
	authors, err := s.authors.ListByUser(ctx, scope)
	if err != nil {
		slog.Warn("Audible Sync wishlist: could not index authors by name; duplicate-author guard disabled", "error", err)
		return index
	}
	for i := range authors {
		if key := textutil.NormalizeAuthorName(authors[i].Name); key != "" {
			index[key] = append(index[key], authors[i])
		}
	}
	return index
}

// ensureAuthor reuses the author by foreign id, then by a unique normalised
// name, and otherwise creates one, with the same rules as the Hardcover list
// sync (see hardcoverlistsyncer.ensureAuthor for the reasoning): a created
// author is monitored for metadata but wants none of its back catalogue, and
// takes the list's owner, quality profile and root folder.
func (s *ListSyncer) ensureAuthor(ctx context.Context, a *models.Author, nameIndex map[string][]models.Author, ownerID, scope int64, qualityProfileID, rootFolderID *int64) (int64, error) {
	if a == nil || strings.TrimSpace(a.ForeignID) == "" {
		return 0, errors.New("book has no resolvable author")
	}
	existing, err := s.authors.GetByAnyForeignIDForUser(ctx, a.ForeignID, scope)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		return existing.ID, nil
	}
	if key := textutil.NormalizeAuthorName(a.Name); key != "" && len(nameIndex[key]) == 1 {
		matched := nameIndex[key][0]
		if err := s.authors.UpsertAuthorIdentifier(ctx, matched.ID, a.ForeignID); err != nil && !errors.Is(err, db.ErrAuthorIdentifierConflict) {
			slog.Warn("Audible Sync wishlist: failed to attach author alias", "author", matched.Name, "foreignID", a.ForeignID, "error", err)
		}
		return matched.ID, nil
	}

	author := *a
	author.ID = 0
	author.Monitored = true
	author.MonitorMode = models.AuthorMonitorModeNone
	author.MonitorNewItems = models.AuthorMonitorNewItemsNone
	if author.MetadataProvider == "" {
		author.MetadataProvider = models.AuthorProviderFromForeignID(author.ForeignID)
	}
	if author.SortName == "" {
		author.SortName = textutil.SortName(author.Name)
	}
	if author.MetadataProfileID == nil {
		def := models.DefaultMetadataProfileID
		author.MetadataProfileID = &def
	}
	if qualityProfileID != nil {
		id := *qualityProfileID
		author.QualityProfileID = &id
	}
	if rootFolderID != nil {
		id := *rootFolderID
		author.RootFolderID = &id
	}
	if err := s.authors.CreateForUser(ctx, &author, ownerID); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || errors.Is(err, db.ErrAuthorIdentifierConflict) {
			if again, _ := s.authors.GetByAnyForeignIDForUser(ctx, author.ForeignID, scope); again != nil {
				return again.ID, nil
			}
		}
		return 0, fmt.Errorf("create author %q: %w", author.Name, err)
	}
	if key := textutil.NormalizeAuthorName(author.Name); key != "" {
		nameIndex[key] = append(nameIndex[key], author)
	}
	slog.Info("created author from Audible Sync wishlist", "name", author.Name, "foreignID", author.ForeignID)
	return author.ID, nil
}
