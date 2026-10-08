package calibre

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/models"
)

// DeliveryStore is the subset of *db.CalibreDeliveryRepo the worker uses.
type DeliveryStore interface {
	Enqueue(ctx context.Context, bookID, bookFileID int64, editionID *int64, filePath, format string) (bool, error)
	DueBatch(ctx context.Context, now time.Time, limit int) ([]models.CalibreDelivery, error)
	MarkDelivered(ctx context.Context, id, calibreID int64, outcome, targetLibrary string) error
	MarkFailed(ctx context.Context, id int64, code, msg string, nextAttemptAt time.Time, terminal bool) error
	MarkSkipped(ctx context.Context, id int64, reason string) error
	DeliveredByCalibreID(ctx context.Context, calibreID int64) ([]models.CalibreDelivery, error)
	ListByBook(ctx context.Context, bookID int64) ([]models.CalibreDelivery, error)
	RearmSkipped(ctx context.Context, reason string) (int64, error)
	HasSkipped(ctx context.Context, reason string) (bool, error)
	// The pull transport (#2833) reads rows by id and pages the queue by book.
	Get(ctx context.Context, id int64) (*models.CalibreDelivery, error)
	DueBooksAfter(ctx context.Context, now time.Time, afterBookID int64, books int) ([]models.CalibreDelivery, error)
	CountDue(ctx context.Context, now time.Time) (int, error)
}

// DeliveryBooks is the subset of *db.BookRepo the worker uses.
type DeliveryBooks interface {
	GetByID(ctx context.Context, id int64) (*models.Book, error)
	ListFiles(ctx context.Context, bookID int64) ([]models.BookFile, error)
	SetCalibreIDIfUnset(ctx context.Context, id, calibreID int64) (bool, error)
}

// deliveryHealthProber is what the worker asks before a pass. Only the plugin
// client implements it; calibredb runs locally and has nothing to probe.
type deliveryHealthProber interface {
	HealthDetail(ctx context.Context) (HealthState, error)
}

// calibredbLocator is what the worker asks before a calibredb pass. Only the
// calibredb client implements it.
type calibredbLocator interface {
	Locate() (string, error)
}

// metadataUpdater writes to a Calibre row that already exists
// (PATCH /v1/books/{id}). It is what turns a 409 into a correction.
type metadataUpdater interface {
	SupportsMetadataUpdate(ctx context.Context) bool
	UpdateMetadata(ctx context.Context, id int64, meta Metadata) ([]string, error)
}

// formatAdder is a client that can put a second file of a Bindery book on
// the Calibre row the first one made (calibre-bridge 0.7.0, add_format).
type formatAdder interface {
	SupportsAddFormat(ctx context.Context) bool
	AddWithOptions(ctx context.Context, filePath string, meta Metadata, opts AddOptions) (AddResult, error)
}

// Delivery outcomes recorded on the ledger row.
const (
	DeliveryOutcomeAdded   = "added"
	DeliveryOutcomeAlready = "already"
	// DeliveryOutcomeFormatAdded is a file that joined the Calibre row an
	// earlier file of the same book made, as another format of it.
	DeliveryOutcomeFormatAdded = "format_added"
)

// Skip reasons recorded on the ledger row.
const (
	deliverySkipBookGone = "book removed"
	deliverySkipFileGone = "file removed"
	deliverySkipNoFile   = "file missing on disk"
	// DeliverySkipNeedsAddFormat is a second format of a book Calibre already
	// has, held back because the target cannot add a format to an existing
	// row. The worker re-queues these rows by this exact text once the
	// bridge advertises add_format, so it must not change casually: rows
	// already skipped under the old text would never be re-armed.
	DeliverySkipNeedsAddFormat = "bridge cannot add a second format; update the Calibre plugin to 0.7.0"
)

const (
	// deliveryBatchSize bounds one pass. The next tick picks up the rest.
	deliveryBatchSize = 50
	// deliveryHealthTimeout bounds the reachability probe at the start of a
	// pass, so a Calibre host that does not answer costs five seconds a
	// minute rather than the plugin client's thirty second request timeout.
	deliveryHealthTimeout = 5 * time.Second
	// deliveryMaxAttempts is the attempt that gives up for good.
	deliveryMaxAttempts = 8
	// deliveryIdleRearmInterval is how often an otherwise idle queue asks
	// the bridge whether it can now add formats, while rows are skipped for
	// want of that. The ask is one health request; the interval keeps it
	// from becoming one a minute for as long as the plugin is old.
	deliveryIdleRearmInterval = 15 * time.Minute
)

// deliveryBackoff is the wait after the Nth failed attempt (index N-1). Past
// the end of the list the last step repeats. A var so tests can read it.
var deliveryBackoff = []time.Duration{
	time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	time.Hour,
	6 * time.Hour,
	24 * time.Hour,
}

// deliveryTerminalCodes are plugin error codes no retry can fix: the file is
// a format Calibre will not take, or the plugin refuses the path outright.
var deliveryTerminalCodes = map[string]bool{
	"bad_format":     true,
	"path_forbidden": true,
}

// nextDeliveryAttempt returns when a row that has now failed attempts times
// should be tried again, and whether it should instead give up.
func nextDeliveryAttempt(now time.Time, attempts int) (time.Time, bool) {
	if attempts >= deliveryMaxAttempts {
		return now, true
	}
	i := attempts - 1
	if i < 0 {
		i = 0
	}
	if i >= len(deliveryBackoff) {
		i = len(deliveryBackoff) - 1
	}
	return now.Add(deliveryBackoff[i]), false
}

// Deliverer sends queued ebook files to Calibre (#2832). Imports enqueue a
// calibre_deliveries row and kick it; a scheduler job runs it every minute as
// well, so a book imported while Calibre was closed is delivered once Calibre
// is back.
//
// Passes never overlap: the tick and a kick share one mutex taken with
// TryLock, and whichever arrives second simply returns. Nothing holds a
// transaction across a Calibre call; every ledger write is one statement.
type Deliverer struct {
	store    DeliveryStore
	books    DeliveryBooks
	mode     func() Mode
	config   func() Config
	adderFor func(Mode) Adder

	authors  AuthorGetter
	editions EditionLister
	series   SeriesGetter
	covers   CoverSource
	jobs     *jobs.Group
	now      func() time.Time

	mu sync.Mutex
	// idleRearmAt is when an idle pass last probed the bridge on behalf of
	// rows skipped for want of add_format. Guarded by mu.
	idleRearmAt time.Time

	healthMu sync.Mutex
	health   DeliveryHealth

	// transport is read at the start of each pass; nil means push. In pull
	// the plugin fetches from the queue itself, so the pass stands down.
	transport func() Transport

	pullMu sync.Mutex
	pull   pullState

	// hydrateEditions fills in a book's editions on demand (#1853); nil
	// leaves a book without editions as it is. hydratedAt, guarded by
	// hydrateMu, is when each book was last asked about.
	hydrateEditions EditionHydrator
	hydrateApplies  func(*models.Book) bool
	hydrateMu       sync.Mutex
	hydratedAt      map[int64]time.Time
}

// DeliveryHealth is what the worker last learned about the push target, for
// the settings queue view. It is only as fresh as the last pass that had
// something to deliver: an idle queue does not probe Calibre, so CheckedAt
// can be old while nothing is waiting.
type DeliveryHealth struct {
	// LastPassAt is when a pass last looked at the queue.
	LastPassAt *time.Time `json:"lastPassAt,omitempty"`
	// CheckedAt is when the worker last learned whether Calibre answers.
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	// Reachable is nil until the worker has had a reason to find out.
	Reachable *bool `json:"reachable,omitempty"`
	// LastError is why Calibre could not be reached, when it could not.
	LastError string `json:"lastError,omitempty"`
}

// Health returns a copy of what the worker last learned about the target.
func (d *Deliverer) Health() DeliveryHealth {
	d.healthMu.Lock()
	defer d.healthMu.Unlock()
	h := d.health
	if h.LastPassAt != nil {
		t := *h.LastPassAt
		h.LastPassAt = &t
	}
	if h.CheckedAt != nil {
		t := *h.CheckedAt
		h.CheckedAt = &t
	}
	if h.Reachable != nil {
		v := *h.Reachable
		h.Reachable = &v
	}
	return h
}

func (d *Deliverer) notePass() {
	now := d.now().UTC()
	d.healthMu.Lock()
	d.health.LastPassAt = &now
	d.healthMu.Unlock()
}

// noteReachable records whether Calibre answered, and why not when it did not.
func (d *Deliverer) noteReachable(ok bool, cause string) {
	now := d.now().UTC()
	d.healthMu.Lock()
	d.health.CheckedAt = &now
	d.health.Reachable = &ok
	d.health.LastError = cause
	d.healthMu.Unlock()
}

// NewDeliverer builds a worker. mode and config are read at the start of each
// pass and adderFor resolves the client for the mode, so a settings change
// takes effect on the next pass.
func NewDeliverer(store DeliveryStore, books DeliveryBooks, mode func() Mode, config func() Config, adderFor func(Mode) Adder) *Deliverer {
	return &Deliverer{
		store:    store,
		books:    books,
		mode:     mode,
		config:   config,
		adderFor: adderFor,
		now:      time.Now,
	}
}

// WithMetadata attaches the lookups that fill the author, edition and series
// fields of the payload. Each is optional.
func (d *Deliverer) WithMetadata(authors AuthorGetter, editions EditionLister, series SeriesGetter) *Deliverer {
	d.authors = authors
	d.editions = editions
	d.series = series
	return d
}

// EditionHydrator fetches and stores the editions of a book that has none on
// record. It must not change the book itself.
type EditionHydrator func(ctx context.Context, book *models.Book) error

// editionHydrateRetry is how long a book Hardcover answered for, with no
// editions it could store, waits before a delivery asks again.
const editionHydrateRetry = 6 * time.Hour

// editionHydratePerRun caps the edition fetches one push pass or one pull
// listing makes (#1853). A pull listing runs inside the plugin's request, and
// fifty books each waiting on Hardcover could outlast the server's write
// timeout. A book over the cap is not delivered or listed this time; the next
// pass or listing, a minute or so later, fetches it.
const editionHydratePerRun = 5

// editionHydrateTimeout bounds one book's fetch.
const editionHydrateTimeout = 15 * time.Second

// WithEditionHydrator lets a delivery fetch the editions of a book that has
// none on record before it builds the metadata (#1853). Optional.
// applies picks the books h fetches for; a book it does not apply to never
// spends the fetch allowance. Nil applies to every book.
func (d *Deliverer) WithEditionHydrator(h EditionHydrator, applies func(*models.Book) bool) *Deliverer {
	d.hydrateEditions = h
	d.hydrateApplies = applies
	return d
}

// hydrateBudgetKey carries a run's remaining edition fetches in its context.
type hydrateBudgetKey struct{}

// withHydrateBudget gives a push pass or a pull listing its fetch allowance.
// A context without one, such as the cover route, never fetches.
func withHydrateBudget(ctx context.Context) context.Context {
	n := editionHydratePerRun
	return context.WithValue(ctx, hydrateBudgetKey{}, &n)
}

// hydrateRecentlyAnswered reports whether Hardcover answered for bookID
// within editionHydrateRetry.
func (d *Deliverer) hydrateRecentlyAnswered(bookID int64) bool {
	d.hydrateMu.Lock()
	defer d.hydrateMu.Unlock()
	last, ok := d.hydratedAt[bookID]
	return ok && d.now().Sub(last) < editionHydrateRetry
}

// markHydrateAnswered records that Hardcover answered for bookID.
func (d *Deliverer) markHydrateAnswered(bookID int64) {
	d.hydrateMu.Lock()
	defer d.hydrateMu.Unlock()
	if d.hydratedAt == nil {
		d.hydratedAt = map[int64]time.Time{}
	}
	d.hydratedAt[bookID] = d.now()
}

// prepareEditions fetches the editions of a book that has none on record
// before it is delivered or listed (#1853): typically a book a Hardcover list
// sync added, since #1784 stopped fetching editions there, so the handoff
// carries its ISBN, publisher and the rest. It reports deferred when the
// run's fetch allowance is spent: the caller then leaves the row for the next
// pass or listing rather than sending it without them.
//
// Only an answer from Hardcover is remembered, so the book is not asked about
// again for editionHydrateRetry. A fetch that failed, timed out or was cut off
// with its request is not: the row goes ahead without editions this time,
// and the next delivery of the book may ask again.
func (d *Deliverer) prepareEditions(ctx context.Context, book *models.Book) (deferred bool) {
	if d.hydrateEditions == nil || d.editions == nil || d.hydrateRecentlyAnswered(book.ID) {
		return false
	}
	if d.hydrateApplies != nil && !d.hydrateApplies(book) {
		return false
	}
	editions, err := d.editions.ListByBook(ctx, book.ID)
	if err != nil || len(editions) > 0 {
		return false
	}
	budget, _ := ctx.Value(hydrateBudgetKey{}).(*int)
	if budget == nil {
		return false
	}
	if *budget <= 0 {
		return true
	}
	*budget--
	fctx, cancel := context.WithTimeout(ctx, editionHydrateTimeout)
	herr := d.hydrateEditions(fctx, book)
	cancel()
	if herr != nil {
		slog.Debug("calibre delivery: fetching the book's editions failed", "bookId", book.ID, "error", herr)
		return false
	}
	d.markHydrateAnswered(book.ID)
	return false
}

// WithCovers lets deliveries carry the book's cover.
func (d *Deliverer) WithCovers(src CoverSource) *Deliverer {
	d.covers = src
	return d
}

// WithJobs tracks passes in the process-wide jobs group, so shutdown waits
// for an in-flight delivery before it closes the database.
func (d *Deliverer) WithJobs(g *jobs.Group) *Deliverer {
	d.jobs = g
	return d
}

// Enqueue queues one imported ebook file. The format is the file extension,
// lower case. editionID is the edition the download was grabbed for, when
// known; otherwise the worker matches one by format at delivery time.
func (d *Deliverer) Enqueue(ctx context.Context, bookID, bookFileID int64, editionID *int64, path string) (bool, error) {
	format := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	return d.store.Enqueue(ctx, bookID, bookFileID, editionID, path, format)
}

// Kick starts a pass in the background and returns at once. If a pass is
// already running the kick is dropped; the running pass or the next tick
// picks the new row up.
func (d *Deliverer) Kick() {
	if d.jobs != nil {
		d.jobs.Go("calibre-deliver-kick", d.runLocked)
		return
	}
	go d.runLocked(context.Background())
}

// RunDeliveries runs one pass on the calling goroutine. It is the scheduler
// job; it returns at once when a pass is already running.
func (d *Deliverer) RunDeliveries(ctx context.Context) {
	if d.jobs != nil {
		d.jobs.Run("calibre-deliver", d.runLocked)
		return
	}
	d.runLocked(ctx)
}

func (d *Deliverer) runLocked(ctx context.Context) {
	if !d.mu.TryLock() {
		return
	}
	defer d.mu.Unlock()
	d.pass(ctx)
}

// deliveryResult is what one row came to.
type deliveryResult int

const (
	deliveryDelivered deliveryResult = iota
	deliveryFailed
	deliveryGaveUp
	deliverySkipped
	// deliveryUntouched left the row as it was: Calibre went away mid
	// batch, or a local read failed. Not an attempt.
	deliveryUntouched
	// deliveryStop is deliveryUntouched and ends the pass too.
	deliveryStop
)

// deliveryTarget is what a pass knows about where it is delivering.
type deliveryTarget struct {
	mode    Mode
	adder   Adder
	cfg     Config
	library string
	// addFormat is true when the bridge advertised add_format at the start
	// of the pass. Never true for calibredb.
	addFormat bool
	// anyLibrary is the pull transport before the plugin has said which
	// library it adds to; see libraryMatches.
	anyLibrary bool
}

func (d *Deliverer) pass(ctx context.Context) {
	mode := d.mode()
	if mode != ModePlugin && mode != ModeCalibredb {
		return
	}
	if mode == ModePlugin && d.pulling() {
		// The plugin connects in and takes rows off the queue itself
		// (#2833). Probing or delivering here as well would send a book
		// twice, and there may be no plugin URL to reach at all.
		return
	}
	// The due query is a local read, so it goes first: an idle queue must
	// not cost a request to the Calibre host every minute.
	d.notePass()
	rows, err := d.store.DueBatch(ctx, d.now(), deliveryBatchSize)
	if err != nil {
		slog.Warn("calibre delivery: reading the queue failed", "error", err)
		return
	}
	if len(rows) == 0 && !d.idleRearmDue(ctx, mode) {
		return
	}
	adder := d.adderFor(mode)
	if adder == nil {
		return
	}
	target := deliveryTarget{mode: mode, adder: adder, cfg: d.config()}
	if mode == ModeCalibredb {
		target.library = target.cfg.LibraryPath
	}
	if locator, ok := adder.(calibredbLocator); ok && mode == ModeCalibredb {
		// A calibredb that is not there fails every row the same way, and
		// no retry fixes it (#1940). Leave the rows waiting, untouched and
		// without spending attempts, and say why on the settings queue view,
		// so switching to the Bridge plugin delivers them.
		if _, lerr := locator.Locate(); lerr != nil {
			slog.Debug("calibre delivery: calibredb not found, waiting", "pending", len(rows), "error", lerr)
			d.noteReachable(false, lerr.Error())
			return
		}
	}
	if prober, ok := adder.(deliveryHealthProber); ok && mode == ModePlugin {
		pctx, cancel := context.WithTimeout(ctx, deliveryHealthTimeout)
		state, herr := prober.HealthDetail(pctx)
		cancel()
		if herr != nil {
			// Calibre is closed or the host is down. That is not an
			// attempt, so no row is touched; the next tick asks again.
			slog.Debug("calibre delivery: bridge unreachable, waiting", "pending", len(rows), "error", herr)
			d.noteReachable(false, herr.Error())
			return
		}
		if state.Degraded {
			slog.Debug("calibre delivery: bridge degraded, waiting", "pending", len(rows), "reason", state.Reason)
			d.noteReachable(false, "bridge degraded: "+state.Reason)
			return
		}
		d.noteReachable(true, "")
		target.library = state.Library
		if fa, ok := adder.(formatAdder); ok && fa.SupportsAddFormat(ctx) {
			target.addFormat = true
			rows = d.rearmFormatSkips(ctx, rows)
		}
	}
	if len(rows) == 0 {
		return
	}
	orderDeliveries(rows)
	ctx = withHydrateBudget(ctx)

	var delivered, failed, gaveUp, skipped int
	for i := range rows {
		if ctx.Err() != nil {
			break
		}
		res := d.deliver(ctx, &rows[i], target)
		switch res {
		case deliveryDelivered:
			delivered++
		case deliveryFailed:
			failed++
		case deliveryGaveUp:
			gaveUp++
		case deliverySkipped:
			skipped++
		}
		if res == deliveryStop {
			break
		}
	}
	if delivered+failed+gaveUp+skipped > 0 {
		slog.Info("calibre delivery pass",
			"mode", mode, "delivered", delivered, "failed", failed+gaveUp, "gaveUp", gaveUp, "skipped", skipped)
	}
}

// resolvedDelivery is a row that is ready to go: its book, the file as the
// book tracks it now, the file's size, and whether it is a second format of
// a book the target already holds.
type resolvedDelivery struct {
	book      *models.Book
	path      string
	size      int64
	secondary bool
}

// resolveDelivery loads what a row needs before it can be sent, by push or by
// pull, and applies the rules both share: a row whose book or file is gone is
// skipped, a file that cannot be read fails, and a second format is held back
// with DeliverySkipNeedsAddFormat unless the target can add one. ok is false
// when the row was dealt with here; the result then says how.
func (d *Deliverer) resolveDelivery(ctx context.Context, row *models.CalibreDelivery, target deliveryTarget) (resolvedDelivery, deliveryResult, bool) {
	var out resolvedDelivery
	book, err := d.books.GetByID(ctx, row.BookID)
	if err != nil {
		if ctx.Err() != nil {
			return out, deliveryStop, false
		}
		slog.Warn("calibre delivery: loading the book failed", "bookId", row.BookID, "error", err)
		return out, deliveryUntouched, false
	}
	if book == nil {
		return out, d.skip(ctx, row, deliverySkipBookGone), false
	}
	files, err := d.books.ListFiles(ctx, book.ID)
	if err != nil {
		if ctx.Err() != nil {
			return out, deliveryStop, false
		}
		slog.Warn("calibre delivery: listing the book's files failed", "bookId", book.ID, "error", err)
		return out, deliveryUntouched, false
	}
	path := ""
	for _, f := range files {
		if f.ID == row.BookFileID {
			path = f.Path
			break
		}
	}
	if path == "" {
		return out, d.skip(ctx, row, deliverySkipFileGone), false
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, d.skip(ctx, row, deliverySkipNoFile), false
		}
		return out, d.fail(ctx, row, "file_unreadable", err), false
	}

	// A book Calibre already holds a file of gets this file as another
	// format of the same record, which only a bridge with add_format can do.
	// Without it the file would be refused as a duplicate at best, so it is
	// held back with a reason that is re-armed once the bridge can.
	secondary, err := d.hasDeliveredSibling(ctx, row, target)
	if err != nil {
		if ctx.Err() != nil {
			return out, deliveryStop, false
		}
		slog.Warn("calibre delivery: reading the book's deliveries failed", "bookId", book.ID, "error", err)
		return out, deliveryUntouched, false
	}
	if secondary && !target.addFormat {
		return out, d.skip(ctx, row, DeliverySkipNeedsAddFormat), false
	}
	if d.prepareEditions(ctx, book) {
		// Over this run's edition fetch allowance: the next run fetches
		// the book's editions and sends it then. Not an attempt.
		return out, deliveryUntouched, false
	}
	return resolvedDelivery{book: book, path: path, size: info.Size(), secondary: secondary}, 0, true
}

// deliver sends one row and records how it went.
func (d *Deliverer) deliver(ctx context.Context, row *models.CalibreDelivery, target deliveryTarget) deliveryResult {
	rd, res, ok := d.resolveDelivery(ctx, row, target)
	if !ok {
		return res
	}
	book, path, secondary := rd.book, rd.path, rd.secondary

	meta, edition := d.metadata(ctx, book, row, path, target)
	meta.CoverPath = d.covers.PathFor(ctx, CoverSourceFor(book, edition), target.adder)
	// The plugin client is asked through AddWithOptions even for a first
	// format, so a format_added answer is seen either way: 0.7.0 also gives
	// it when a push repairs a row a failed add left empty.
	var added AddResult
	var addErr error
	if fa, ok := target.adder.(formatAdder); ok {
		added, addErr = fa.AddWithOptions(ctx, path, meta, AddOptions{AddFormat: secondary})
	} else {
		added.ID, addErr = target.adder.Add(ctx, path, meta)
	}
	id := added.ID
	// Once Calibre has the book, the result is recorded even if shutdown
	// cancelled ctx while the add was in flight. Losing it would only cost a
	// 409 on the next start, but there is no reason to lose it.
	record := context.WithoutCancel(ctx)
	if addErr == nil || errors.Is(addErr, ErrAlreadyInCalibre) {
		d.noteReachable(true, "")
	}
	switch {
	case addErr == nil:
		outcome := DeliveryOutcomeAdded
		if added.FormatAdded {
			outcome = DeliveryOutcomeFormatAdded
		}
		if err := d.recordDelivered(record, row, book, id, outcome, target); err != nil {
			return d.lostRow(row, err)
		}
		slog.Debug("calibre delivery: book added", "bookId", book.ID, "calibreId", id, "path", path, "outcome", outcome)
		return deliveryDelivered
	case errors.Is(addErr, ErrAlreadyInCalibre):
		owned := d.ownsBeforeRecording(ctx, book.ID, id, target.library)
		if err := d.recordDelivered(record, row, book, id, DeliveryOutcomeAlready, target); err != nil {
			return d.lostRow(row, err)
		}
		slog.Info("calibre delivery: book already in Calibre", "bookId", book.ID, "calibreId", id, "owned", owned)
		if owned {
			d.refreshMetadata(ctx, book.ID, id, meta, target.adder)
		}
		return deliveryDelivered
	case errors.Is(addErr, ErrDisabled):
		// The adder was built from the same settings read as the mode, so
		// this can only be a wiring bug. Stop without counting an attempt.
		slog.Warn("calibre delivery: the adder reports the integration disabled while the configured mode is on; this is a wiring bug, please report it",
			"mode", target.mode, "bookId", book.ID)
		return deliveryStop
	case IsCalibredbUnusable(addErr):
		// calibredb is there but cannot start (the pass's Locate only sees
		// that the file exists), or it went away since. Same as the check:
		// not the book's fault, not an attempt.
		d.noteReachable(false, addErr.Error())
		return deliveryStop
	case isDeliveryUnreachable(ctx, addErr):
		// Calibre went away mid batch. Not the book's fault and not an
		// attempt: leave the row and stop the pass.
		slog.Debug("calibre delivery: Calibre became unreachable, stopping the pass", "bookId", book.ID, "error", addErr)
		if ctx.Err() == nil {
			d.noteReachable(false, addErr.Error())
		}
		return deliveryStop
	default:
		return d.fail(ctx, row, PluginErrorCode(addErr), addErr)
	}
}

// ownsBeforeRecording is owns for an add Calibre answered "already there".
// It has to run before the row is marked delivered: ownership is read from
// the ledger, so only an earlier delivery of this book to this id counts.
func (d *Deliverer) ownsBeforeRecording(ctx context.Context, bookID, calibreID int64, library string) bool {
	return calibreID > 0 && d.owns(ctx, bookID, calibreID, library)
}

// recordDelivered marks the row delivered and applies the books.calibre_id
// rule. Push and pull both finish a delivery through here.
func (d *Deliverer) recordDelivered(ctx context.Context, row *models.CalibreDelivery, book *models.Book, calibreID int64, outcome string, target deliveryTarget) error {
	if err := d.store.MarkDelivered(ctx, row.ID, calibreID, outcome, target.library); err != nil {
		return err
	}
	if book != nil {
		d.recordSourceID(ctx, book, calibreID, target)
	}
	return nil
}

// fail records one failed attempt with backoff, or gives up for good when the
// error cannot be fixed by waiting or the row is out of attempts.
func (d *Deliverer) fail(ctx context.Context, row *models.CalibreDelivery, code string, cause error) deliveryResult {
	terminal, err := d.recordFailure(ctx, row, code, cause.Error(), false)
	if err != nil {
		return d.lostRow(row, err)
	}
	if terminal {
		return deliveryGaveUp
	}
	return deliveryFailed
}

// recordFailure writes one failed attempt: the backoff schedule decides when
// the row comes due again, and a code no retry can fix, the last attempt, or
// forceTerminal gives up instead. It reports whether the row gave up. Push
// and pull both record failures through here.
func (d *Deliverer) recordFailure(ctx context.Context, row *models.CalibreDelivery, code, msg string, forceTerminal bool) (bool, error) {
	attempts := row.Attempts + 1
	next, terminal := nextDeliveryAttempt(d.now(), attempts)
	if forceTerminal || deliveryTerminalCodes[code] {
		terminal = true
	}
	if err := d.store.MarkFailed(ctx, row.ID, code, msg, next, terminal); err != nil {
		return false, err
	}
	if terminal {
		slog.Warn("calibre delivery: giving up on a book",
			"bookId", row.BookID, "path", row.FilePath, "code", code, "attempts", attempts, "error", msg)
		return true, nil
	}
	// The first failure is a WARN so the cause is visible at the default
	// log level long before the row gives up; the repeats are not.
	level := slog.LevelDebug
	if attempts == 1 {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "calibre delivery: add failed, will retry",
		"bookId", row.BookID, "path", row.FilePath, "code", code, "attempts", attempts, "next", next, "error", msg)
	return false, nil
}

func (d *Deliverer) skip(ctx context.Context, row *models.CalibreDelivery, reason string) deliveryResult {
	if err := d.store.MarkSkipped(ctx, row.ID, reason); err != nil {
		return d.lostRow(row, err)
	}
	slog.Info("calibre delivery: skipped", "bookId", row.BookID, "path", row.FilePath, "reason", reason)
	return deliverySkipped
}

// lostRow handles a ledger write that failed. A row that vanished (cleared,
// or cascaded with its file) is not a problem; anything else is logged.
func (d *Deliverer) lostRow(row *models.CalibreDelivery, err error) deliveryResult {
	if !errors.Is(err, db.ErrCalibreDeliveryNotFound) {
		slog.Warn("calibre delivery: recording the result failed", "deliveryId", row.ID, "bookId", row.BookID, "error", err)
	}
	return deliveryUntouched
}

// owns reports whether Bindery put calibreID into the target library for
// this book itself: the ledger holds an earlier delivery of it. A backfilled
// row has no target library recorded and counts for any target.
func (d *Deliverer) owns(ctx context.Context, bookID, calibreID int64, library string) bool {
	rows, err := d.store.DeliveredByCalibreID(ctx, calibreID)
	if err != nil {
		slog.Warn("calibre delivery: ownership lookup failed", "bookId", bookID, "calibreId", calibreID, "error", err)
		return false
	}
	for _, r := range rows {
		if r.BookID != bookID {
			continue
		}
		if libraryMatches(r.TargetLibrary, library, false) {
			return true
		}
	}
	return false
}

// libraryMatches reports whether a delivered row recorded against recorded
// counts for the target library. A backfilled row has no target recorded and
// counts for any target. anyTarget is the pull transport before the plugin
// has reported which library it adds to: then every delivered row counts,
// because treating a second format as a first would make a duplicate record.
func libraryMatches(recorded, library string, anyTarget bool) bool {
	if anyTarget || strings.TrimSpace(recorded) == "" {
		return true
	}
	return cleanLibraryPath(recorded) == cleanLibraryPath(library)
}

// refreshMetadata fills empty fields on a Calibre row Bindery created itself.
// The first time Bindery meets a row it did not create, it writes nothing:
// that row may be one the user curated in Calibre.
func (d *Deliverer) refreshMetadata(ctx context.Context, bookID, calibreID int64, meta Metadata, adder Adder) {
	if meta.IsEmpty() {
		return
	}
	updater, ok := adder.(metadataUpdater)
	if !ok || !updater.SupportsMetadataUpdate(ctx) {
		return
	}
	fields, err := updater.UpdateMetadata(ctx, calibreID, meta)
	if err != nil {
		slog.Warn("calibre delivery: metadata update failed", "bookId", bookID, "calibreId", calibreID, "error", err)
		return
	}
	if len(fields) > 0 {
		slog.Info("calibre delivery: filled empty fields on a book Bindery had already pushed",
			"bookId", bookID, "calibreId", calibreID, "fields", strings.Join(fields, ","))
	}
}

// recordSourceID fills books.calibre_id from a delivery, which is only right
// when the id belongs to the library at calibre.library_path. So: the book
// has no id yet, it did not come from a Calibre import (its id is a source
// id and a push must not replace it), and either no library path is set or
// the target is that same library.
func (d *Deliverer) recordSourceID(ctx context.Context, book *models.Book, calibreID int64, target deliveryTarget) {
	if calibreID <= 0 || book.CalibreID != nil || isCalibreOrigin(book) {
		return
	}
	if cleanLibraryPath(target.cfg.LibraryPath) != "" && !sameLibraryPath(target.cfg.LibraryPath, target.library) {
		return
	}
	if _, err := d.books.SetCalibreIDIfUnset(ctx, book.ID, calibreID); err != nil {
		slog.Warn("calibre delivery: persist calibre_id failed", "bookId", book.ID, "calibreId", calibreID, "error", err)
	}
}

// metadata builds the payload at delivery time, from the book as it is now,
// and returns the edition it matched. The cover is left to the caller: push
// resolves it to a path for the target, pull serves it on its own route.
func (d *Deliverer) metadata(ctx context.Context, book *models.Book, row *models.CalibreDelivery, path string, target deliveryTarget) (Metadata, *models.Edition) {
	edition := d.edition(ctx, book, row, path)
	var author *models.Author
	if d.authors != nil && book.AuthorID != 0 {
		a, err := d.authors.GetByID(ctx, book.AuthorID)
		if err != nil {
			slog.Debug("calibre delivery: author lookup failed", "bookId", book.ID, "error", err)
		}
		author = a
	}
	if author == nil && book.Author != nil {
		author = book.Author
	}
	var seriesTitle, seriesIndex string
	if d.series != nil {
		t, i, err := d.series.GetPrimarySeriesForBook(ctx, book.ID)
		if err != nil {
			slog.Debug("calibre delivery: primary series lookup failed", "bookId", book.ID, "error", err)
		} else {
			seriesTitle, seriesIndex = t, i
		}
	}
	identifiers := IdentifiersForBook(book, edition)
	if isCalibreOrigin(book) && !sameLibraryPath(target.cfg.LibraryPath, target.library) {
		// A source library id means nothing in another library.
		delete(identifiers, "calibre")
	}
	meta := BuildMetadata(MetadataSource{
		Book:        book,
		Author:      author,
		Edition:     edition,
		SeriesTitle: seriesTitle,
		SeriesIndex: seriesIndex,
		Identifiers: identifiers,
	})
	return meta, edition
}

// edition is the row's edition when it still exists, else one matched to the
// file the way the bulk push matches it.
func (d *Deliverer) edition(ctx context.Context, book *models.Book, row *models.CalibreDelivery, path string) *models.Edition {
	if d.editions == nil {
		return nil
	}
	editions, err := d.editions.ListByBook(ctx, book.ID)
	if err != nil {
		slog.Debug("calibre delivery: edition lookup failed", "bookId", book.ID, "error", err)
		return nil
	}
	if row.EditionID != nil {
		for i := range editions {
			if editions[i].ID == *row.EditionID {
				return &editions[i]
			}
		}
	}
	return editionForFile(editions, book, path)
}

// hasDeliveredSibling reports whether another file of row's book is already
// delivered to this target, which makes row a second format of a book
// Calibre holds. A backfilled row has no target recorded and counts, the
// same rule owns applies.
func (d *Deliverer) hasDeliveredSibling(ctx context.Context, row *models.CalibreDelivery, target deliveryTarget) (bool, error) {
	rows, err := d.store.ListByBook(ctx, row.BookID)
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		if r.ID == row.ID || r.BookFileID == row.BookFileID || r.State != models.CalibreDeliveryDelivered {
			continue
		}
		if libraryMatches(r.TargetLibrary, target.library, target.anyLibrary) {
			return true, nil
		}
	}
	return false, nil
}

// idleRearmDue reports whether a pass with nothing due should still ask the
// bridge about add_format: rows are waiting on it, and the last idle ask was
// long enough ago. Called with mu held.
func (d *Deliverer) idleRearmDue(ctx context.Context, mode Mode) bool {
	if mode != ModePlugin {
		return false
	}
	now := d.now()
	if !d.idleRearmAt.IsZero() && now.Sub(d.idleRearmAt) < deliveryIdleRearmInterval {
		return false
	}
	waiting, err := d.store.HasSkipped(ctx, DeliverySkipNeedsAddFormat)
	if err != nil {
		slog.Debug("calibre delivery: reading skipped rows failed", "error", err)
		return false
	}
	if !waiting {
		return false
	}
	d.idleRearmAt = now
	return true
}

// rearmFormatSkips re-queues the rows skipped because the bridge could not
// add a format, now that it can, and returns the due batch including them.
// One UPDATE per pass; it touches nothing when there is nothing to re-arm.
func (d *Deliverer) rearmFormatSkips(ctx context.Context, rows []models.CalibreDelivery) []models.CalibreDelivery {
	n, err := d.store.RearmSkipped(ctx, DeliverySkipNeedsAddFormat)
	if err != nil {
		slog.Warn("calibre delivery: re-queueing formats held for a plugin update failed", "error", err)
		return rows
	}
	if n == 0 {
		return rows
	}
	slog.Info("calibre delivery: the Calibre plugin can now add formats; re-queued the formats held for it", "rows", n)
	due, err := d.store.DueBatch(ctx, d.now(), deliveryBatchSize)
	if err != nil {
		slog.Warn("calibre delivery: reading the queue failed", "error", err)
		return rows
	}
	return due
}

// orderDeliveries puts each book's files in format preference order, so the
// preferred format makes the Calibre record and the others join it. Books
// keep the order they came in, and so does everything within one format.
func orderDeliveries(rows []models.CalibreDelivery) {
	first := make(map[int64]int, len(rows))
	for i, r := range rows {
		if _, ok := first[r.BookID]; !ok {
			first[r.BookID] = i
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.BookID != b.BookID {
			return first[a.BookID] < first[b.BookID]
		}
		return lessByFormatPreference(a.FilePath, b.FilePath)
	})
}

// isDeliveryUnreachable reports whether err means Calibre could not be
// reached or is busy, rather than that it looked at this book and refused.
func isDeliveryUnreachable(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// 503 is the plugin saying Calibre is mid library swap, and the client
	// has already retried it for about thirty seconds. That is Calibre
	// being busy, not the book being bad.
	var pe *PluginError
	return errors.As(err, &pe) && pe.Status == http.StatusServiceUnavailable
}
