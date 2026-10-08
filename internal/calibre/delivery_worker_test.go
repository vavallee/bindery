package calibre

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/models"
)

// fakeBridge stands in for the plugin client: it answers the health probe,
// the add, the cover capability and the metadata update.
type fakeBridge struct {
	mu            sync.Mutex
	adds          []string
	metas         []Metadata
	add           func(path string) (int64, error)
	healthErr     error
	health        HealthState
	probes        int
	supportsPatch bool
	supportsCover bool
	patches       map[int64]Metadata
	// supportsAddFormat is the add_format capability; addFormats records
	// what each push asked for, and addWith, when set, answers a push that
	// asked for it instead of add.
	supportsAddFormat bool
	addFormats        []bool
	addWith           func(path string) (AddResult, error)
}

func (f *fakeBridge) Add(ctx context.Context, path string, meta Metadata) (int64, error) {
	res, err := f.AddWithOptions(ctx, path, meta, AddOptions{})
	return res.ID, err
}

func (f *fakeBridge) SupportsAddFormat(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.supportsAddFormat
}

// AddWithOptions behaves like the real client: addFormat reaches the
// "plugin" only when the capability is advertised.
func (f *fakeBridge) AddWithOptions(_ context.Context, path string, meta Metadata, opts AddOptions) (AddResult, error) {
	f.mu.Lock()
	sent := opts.AddFormat && f.supportsAddFormat
	f.adds = append(f.adds, path)
	f.metas = append(f.metas, meta)
	f.addFormats = append(f.addFormats, sent)
	add, addWith := f.add, f.addWith
	f.mu.Unlock()
	if sent && addWith != nil {
		return addWith(path)
	}
	if add == nil {
		return AddResult{}, errors.New("unexpected add")
	}
	id, err := add(path)
	return AddResult{ID: id}, err
}

func (f *fakeBridge) HealthDetail(context.Context) (HealthState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	return f.health, f.healthErr
}

func (f *fakeBridge) SupportsCover(context.Context) bool { return f.supportsCover }

func (f *fakeBridge) SupportsMetadataUpdate(context.Context) bool { return f.supportsPatch }

func (f *fakeBridge) UpdateMetadata(_ context.Context, id int64, meta Metadata) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.patches == nil {
		f.patches = map[int64]Metadata{}
	}
	f.patches[id] = meta
	return []string{"series"}, nil
}

func (f *fakeBridge) addCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.adds)
}

func (f *fakeBridge) patchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.patches)
}

// fakeCalibredb is the calibredb shape: Add only, nothing to probe.
type fakeCalibredb struct {
	adds []string
	add  func(path string) (int64, error)
}

func (f *fakeCalibredb) Add(_ context.Context, path string, _ Metadata) (int64, error) {
	f.adds = append(f.adds, path)
	return f.add(path)
}

type workerFixture struct {
	ctx      context.Context
	repo     *db.CalibreDeliveryRepo
	books    *db.BookRepo
	editions *db.EditionRepo
	book     *models.Book
	d        *Deliverer
	mode     Mode
	cfg      Config
	adder    Adder
	offset   time.Duration
}

func newWorkerFixture(t *testing.T, mode Mode, adder Adder) *workerFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &workerFixture{
		ctx:      context.Background(),
		repo:     db.NewCalibreDeliveryRepo(database),
		books:    db.NewBookRepo(database),
		editions: db.NewEditionRepo(database),
		mode:     mode,
		adder:    adder,
	}
	authors := db.NewAuthorRepo(database)
	a := &models.Author{ForeignID: "OLA1", Name: "Frank Herbert", SortName: "Herbert, Frank", Monitored: true}
	if err := authors.Create(f.ctx, a); err != nil {
		t.Fatal(err)
	}
	f.book = &models.Book{
		ForeignID: "OLB1", AuthorID: a.ID, Title: "Dune", SortTitle: "Dune",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MetadataProvider: "openlibrary",
	}
	if err := f.books.Create(f.ctx, f.book); err != nil {
		t.Fatal(err)
	}
	f.d = NewDeliverer(f.repo, f.books,
		func() Mode { return f.mode },
		func() Config { return f.cfg },
		func(Mode) Adder { return f.adder }).
		WithMetadata(authors, f.editions, db.NewSeriesRepo(database))
	f.d.now = func() time.Time { return time.Now().Add(f.offset) }
	return f
}

// addFile puts an ebook on disk, tracks it under the book and queues it.
func (f *workerFixture) addFile(t *testing.T, name string) models.CalibreDelivery {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(f.ctx, f.book.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatal(err)
	}
	files, err := f.books.ListFiles(f.ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fileID int64
	for _, bf := range files {
		if bf.Path == path {
			fileID = bf.ID
		}
	}
	if ok, err := f.d.Enqueue(f.ctx, f.book.ID, fileID, nil, path); err != nil || !ok {
		t.Fatalf("enqueue: %v, %v", ok, err)
	}
	row, err := f.repo.GetByBookFile(f.ctx, fileID)
	if err != nil || row == nil {
		t.Fatalf("queued row: %v, %v", row, err)
	}
	return *row
}

func (f *workerFixture) row(t *testing.T, id int64) models.CalibreDelivery {
	t.Helper()
	r, err := f.repo.Get(f.ctx, id)
	if err != nil || r == nil {
		t.Fatalf("row %d: %v, %v", id, r, err)
	}
	return *r
}

func (f *workerFixture) calibreID(t *testing.T) *int64 {
	t.Helper()
	b, err := f.books.GetByID(f.ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	return b.CalibreID
}

func added(id int64) func(string) (int64, error) {
	return func(string) (int64, error) { return id, nil }
}

func TestDeliverer_EnqueueRecordsTheLowerCaseExtension(t *testing.T) {
	f := newWorkerFixture(t, ModePlugin, &fakeBridge{})
	row := f.addFile(t, "Dune.EPUB")
	if row.Format != "epub" || row.State != models.CalibreDeliveryPending || row.Attempts != 0 {
		t.Fatalf("queued row = %+v, want a pending epub with no attempts", row)
	}
}

// An unreachable Calibre is not an attempt: the row must be exactly as it
// was, so a Calibre closed for a week does not burn through the retries.
func TestDeliverer_UnreachableBridgeLeavesRowsUntouched(t *testing.T) {
	for name, bridge := range map[string]*fakeBridge{
		"unreachable": {healthErr: errors.New("connection refused"), add: added(1)},
		"degraded":    {health: HealthState{Degraded: true, Reason: "no api key"}, add: added(1)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newWorkerFixture(t, ModePlugin, bridge)
			before := f.addFile(t, "a.epub")

			f.d.RunDeliveries(f.ctx)

			after := f.row(t, before.ID)
			if bridge.probes != 1 {
				t.Errorf("health probes = %d, want 1", bridge.probes)
			}
			if bridge.addCount() != 0 {
				t.Errorf("adds = %d, want none against an unreachable bridge", bridge.addCount())
			}
			if after.State != models.CalibreDeliveryPending || after.Attempts != 0 || after.LastError != "" ||
				!after.NextAttemptAt.Equal(before.NextAttemptAt) || !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Errorf("row changed: before %+v, after %+v", before, after)
			}
		})
	}
}

// TestDeliverer_HealthRecordsWhatThePassLearned feeds the settings queue view:
// nothing known before a pass, unreachable with the reason after a failed
// probe, reachable again once a delivery lands.
func TestDeliverer_HealthRecordsWhatThePassLearned(t *testing.T) {
	bridge := &fakeBridge{healthErr: errors.New("connection refused"), add: added(1)}
	f := newWorkerFixture(t, ModePlugin, bridge)
	if h := f.d.Health(); h.LastPassAt != nil || h.Reachable != nil {
		t.Fatalf("health before any pass = %+v, want nothing known", h)
	}
	f.addFile(t, "a.epub")

	f.d.RunDeliveries(f.ctx)
	h := f.d.Health()
	if h.LastPassAt == nil || h.CheckedAt == nil || h.Reachable == nil || *h.Reachable || h.LastError != "connection refused" {
		t.Fatalf("health after a refused probe = %+v", h)
	}

	bridge.mu.Lock()
	bridge.healthErr = nil
	bridge.mu.Unlock()
	f.d.RunDeliveries(f.ctx)
	h = f.d.Health()
	if h.Reachable == nil || !*h.Reachable || h.LastError != "" {
		t.Errorf("health after a delivery = %+v, want reachable with no error", h)
	}
}

func TestDeliverer_EmptyQueueDoesNotProbe(t *testing.T) {
	bridge := &fakeBridge{}
	f := newWorkerFixture(t, ModePlugin, bridge)
	f.d.RunDeliveries(f.ctx)
	if bridge.probes != 0 {
		t.Errorf("probes = %d, want none with nothing due", bridge.probes)
	}
}

func TestDeliverer_ModeOffDoesNothing(t *testing.T) {
	bridge := &fakeBridge{add: added(1)}
	f := newWorkerFixture(t, ModeOff, bridge)
	row := f.addFile(t, "a.epub")
	f.d.RunDeliveries(f.ctx)
	if bridge.addCount() != 0 || bridge.probes != 0 {
		t.Fatalf("mode off: adds %d probes %d, want none", bridge.addCount(), bridge.probes)
	}
	if got := f.row(t, row.ID); got.State != models.CalibreDeliveryPending {
		t.Fatalf("state = %s, want pending", got.State)
	}
}

// The adder is resolved per pass from the current mode, so a mode change in
// Settings takes effect on the next pass rather than on the next restart.
func TestDeliverer_ResolvesTheAdderPerPass(t *testing.T) {
	cli := &fakeCalibredb{add: added(1)}
	// b.epub is a second file of a book a.epub already delivered, so the
	// bridge must be able to add a format for it to be sent at all.
	bridge := &fakeBridge{add: added(2), health: HealthState{Library: "/lib"}, supportsAddFormat: true}
	f := newWorkerFixture(t, ModeCalibredb, nil)
	f.d.adderFor = func(m Mode) Adder {
		if m == ModePlugin {
			return bridge
		}
		return cli
	}
	f.addFile(t, "a.epub")
	f.d.RunDeliveries(f.ctx)
	f.mode = ModePlugin
	f.addFile(t, "b.epub")
	f.d.RunDeliveries(f.ctx)
	if len(cli.adds) != 1 || bridge.addCount() != 1 {
		t.Fatalf("calibredb adds %v, plugin adds %v; want one each", cli.adds, bridge.adds)
	}
}

func TestDeliverer_SuccessMarksDeliveredAndRecordsCalibreID(t *testing.T) {
	existing := int64(5)
	cases := []struct {
		name        string
		libraryPath string
		target      string
		provider    string
		foreignID   string
		calibreID   *int64
		want        *int64
	}{
		{name: "no library path set", target: "/lib", want: ptr64(101)},
		{name: "target is the library path", libraryPath: "/lib/", target: "/lib", want: ptr64(101)},
		{name: "different library", libraryPath: "/source", target: "/lib", want: nil},
		{name: "target unknown with a library path set", libraryPath: "/source", target: "", want: nil},
		{name: "calibre origin by provider", target: "/lib", provider: "calibre", want: nil},
		{name: "calibre origin by foreign id", target: "/lib", foreignID: "calibre:book:9", want: nil},
		{name: "existing id is kept", target: "/lib", calibreID: &existing, want: &existing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &fakeBridge{add: added(101), health: HealthState{Library: tc.target}}
			f := newWorkerFixture(t, ModePlugin, bridge)
			f.cfg.LibraryPath = tc.libraryPath
			if tc.provider != "" || tc.foreignID != "" {
				if tc.provider != "" {
					f.book.MetadataProvider = tc.provider
				}
				if tc.foreignID != "" {
					f.book.ForeignID = tc.foreignID
				}
				if err := f.books.Update(f.ctx, f.book); err != nil {
					t.Fatal(err)
				}
			}
			if tc.calibreID != nil {
				if err := f.books.SetCalibreID(f.ctx, f.book.ID, *tc.calibreID); err != nil {
					t.Fatal(err)
				}
			}
			row := f.addFile(t, "a.epub")

			f.d.RunDeliveries(f.ctx)

			got := f.row(t, row.ID)
			if got.State != models.CalibreDeliveryDelivered || got.Outcome != DeliveryOutcomeAdded ||
				got.CalibreID == nil || *got.CalibreID != 101 || got.TargetLibrary != tc.target {
				t.Fatalf("row = %+v, want delivered/added with calibre id 101 in %q", got, tc.target)
			}
			id := f.calibreID(t)
			switch {
			case tc.want == nil && id != nil:
				t.Errorf("books.calibre_id = %d, want it left NULL", *id)
			case tc.want != nil && (id == nil || *id != *tc.want):
				t.Errorf("books.calibre_id = %v, want %d", id, *tc.want)
			}
		})
	}
}

func ptr64(v int64) *int64 { return &v }

// A 409 marks the row delivered as "already", and only a Calibre row the
// ledger says Bindery delivered for this book gets its metadata refreshed.
// books.calibre_id no longer decides it: after #2832 that is the source
// library id, which says nothing about who created the target's row.
func TestDeliverer_409MarksAlreadyAndOwnershipComesFromTheLedger(t *testing.T) {
	conflict := func(string) (int64, error) { return 55, ErrAlreadyInCalibre }

	t.Run("books.calibre_id alone is not ownership", func(t *testing.T) {
		bridge := &fakeBridge{add: conflict, supportsPatch: true, supportsAddFormat: true, health: HealthState{Library: "/lib"}}
		f := newWorkerFixture(t, ModePlugin, bridge)
		if err := f.books.SetCalibreID(f.ctx, f.book.ID, 55); err != nil {
			t.Fatal(err)
		}
		row := f.addFile(t, "a.epub")
		f.d.RunDeliveries(f.ctx)
		got := f.row(t, row.ID)
		if got.State != models.CalibreDeliveryDelivered || got.Outcome != DeliveryOutcomeAlready || got.CalibreID == nil || *got.CalibreID != 55 {
			t.Fatalf("row = %+v, want delivered/already with id 55", got)
		}
		if bridge.patchCount() != 0 {
			t.Errorf("patches = %d, want none for a row the ledger does not own", bridge.patchCount())
		}
	})

	t.Run("an earlier delivery of this book owns it", func(t *testing.T) {
		bridge := &fakeBridge{add: conflict, supportsPatch: true, supportsAddFormat: true, health: HealthState{Library: "/lib"}}
		f := newWorkerFixture(t, ModePlugin, bridge)
		first := f.addFile(t, "a.epub")
		if err := f.repo.MarkDelivered(f.ctx, first.ID, 55, DeliveryOutcomeAdded, "/lib"); err != nil {
			t.Fatal(err)
		}
		row := f.addFile(t, "a.mobi")
		f.d.RunDeliveries(f.ctx)
		if got := f.row(t, row.ID); got.Outcome != DeliveryOutcomeAlready {
			t.Fatalf("row = %+v, want outcome already", got)
		}
		if bridge.patchCount() != 1 || bridge.patches[55].Title != "Dune" {
			t.Errorf("patches = %+v, want one refresh of id 55", bridge.patches)
		}
		// The 409 id is recorded on the book as well: no library path is
		// set and the book is not Calibre origin.
		if id := f.calibreID(t); id == nil || *id != 55 {
			t.Errorf("books.calibre_id = %v, want 55", id)
		}
	})

	t.Run("a delivery to another library does not own it", func(t *testing.T) {
		bridge := &fakeBridge{add: conflict, supportsPatch: true, supportsAddFormat: true, health: HealthState{Library: "/lib"}}
		f := newWorkerFixture(t, ModePlugin, bridge)
		first := f.addFile(t, "a.epub")
		if err := f.repo.MarkDelivered(f.ctx, first.ID, 55, DeliveryOutcomeAdded, "/old-lib"); err != nil {
			t.Fatal(err)
		}
		f.addFile(t, "a.mobi")
		f.d.RunDeliveries(f.ctx)
		if bridge.patchCount() != 0 {
			t.Errorf("patches = %d, want none for a delivery into another library", bridge.patchCount())
		}
	})

	t.Run("a backfilled row owns it", func(t *testing.T) {
		bridge := &fakeBridge{add: conflict, supportsPatch: true, supportsAddFormat: true, health: HealthState{Library: "/lib"}}
		f := newWorkerFixture(t, ModePlugin, bridge)
		first := f.addFile(t, "a.epub")
		if err := f.repo.MarkDelivered(f.ctx, first.ID, 55, "backfilled", ""); err != nil {
			t.Fatal(err)
		}
		f.addFile(t, "a.mobi")
		f.d.RunDeliveries(f.ctx)
		if bridge.patchCount() != 1 {
			t.Errorf("patches = %d, want the backfilled delivery to count as Bindery's", bridge.patchCount())
		}
	})

	t.Run("no update without the capability", func(t *testing.T) {
		bridge := &fakeBridge{add: conflict, supportsPatch: false, supportsAddFormat: true, health: HealthState{Library: "/lib"}}
		f := newWorkerFixture(t, ModePlugin, bridge)
		first := f.addFile(t, "a.epub")
		if err := f.repo.MarkDelivered(f.ctx, first.ID, 55, DeliveryOutcomeAdded, "/lib"); err != nil {
			t.Fatal(err)
		}
		f.addFile(t, "a.mobi")
		f.d.RunDeliveries(f.ctx)
		if bridge.patchCount() != 0 {
			t.Errorf("patches = %d, want none against a plugin that cannot update", bridge.patchCount())
		}
	})
}

func TestDeliverer_TerminalPluginCodesFailAtOnce(t *testing.T) {
	for _, code := range []string{"bad_format", "path_forbidden"} {
		t.Run(code, func(t *testing.T) {
			bridge := &fakeBridge{health: HealthState{Library: "/lib"}, add: func(string) (int64, error) {
				return 0, &PluginError{Status: http.StatusBadRequest, Code: code, Detail: "no (" + code + ")"}
			}}
			f := newWorkerFixture(t, ModePlugin, bridge)
			row := f.addFile(t, "a.epub")
			f.d.RunDeliveries(f.ctx)
			got := f.row(t, row.ID)
			if got.State != models.CalibreDeliveryFailed || got.Attempts != 1 || got.LastErrorCode != code {
				t.Fatalf("row = %+v, want failed after one attempt with code %s", got, code)
			}
		})
	}
}

// Anything else backs off 1m, 5m, 15m, 1h, 6h, then 24h, and the eighth
// failed attempt gives up.
func TestDeliverer_OtherErrorsBackOffThenGiveUp(t *testing.T) {
	bridge := &fakeBridge{health: HealthState{Library: "/lib"}, add: func(string) (int64, error) {
		return 0, &PluginError{Status: http.StatusBadRequest, Code: "path_not_found", Detail: "missing"}
	}}
	f := newWorkerFixture(t, ModePlugin, bridge)
	row := f.addFile(t, "a.epub")
	want := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour, 24 * time.Hour}

	var last time.Time
	for attempt := 1; attempt <= 8; attempt++ {
		passAt := f.d.now()
		f.d.RunDeliveries(f.ctx)
		got := f.row(t, row.ID)
		if got.Attempts != attempt {
			t.Fatalf("attempt %d: attempts = %d", attempt, got.Attempts)
		}
		if attempt == 8 {
			if got.State != models.CalibreDeliveryFailed {
				t.Fatalf("after 8 attempts state = %s, want failed", got.State)
			}
			break
		}
		if got.State != models.CalibreDeliveryPending || got.LastErrorCode != "path_not_found" {
			t.Fatalf("attempt %d: row = %+v, want pending with the code recorded", attempt, got)
		}
		wait := got.NextAttemptAt.Sub(passAt)
		if wait < want[attempt-1] || wait > want[attempt-1]+time.Minute {
			t.Fatalf("attempt %d: next attempt in %s, want %s", attempt, wait, want[attempt-1])
		}
		if !got.NextAttemptAt.After(last) {
			t.Fatalf("attempt %d: next attempt %s is not later than %s", attempt, got.NextAttemptAt, last)
		}
		last = got.NextAttemptAt

		// Not due yet: a pass now must leave it alone.
		f.d.RunDeliveries(f.ctx)
		if again := f.row(t, row.ID); again.Attempts != attempt {
			t.Fatalf("attempt %d: a pass before next_attempt_at retried it", attempt)
		}
		f.offset = time.Until(got.NextAttemptAt) + time.Second
	}
	if bridge.addCount() != 8 {
		t.Errorf("adds = %d, want 8", bridge.addCount())
	}
	if _, terminal := nextDeliveryAttempt(time.Now(), 7); terminal {
		t.Error("attempt 7 is terminal, want only the eighth")
	}
}

// A transport failure mid batch is Calibre going away, not the book failing:
// the pass stops and no row is charged an attempt.
func TestDeliverer_TransportErrorStopsThePassWithoutAnAttempt(t *testing.T) {
	bridge := &fakeBridge{health: HealthState{Library: "/lib"}, add: func(string) (int64, error) {
		return 0, &url.Error{Op: "Post", URL: "http://calibre:8099/v1/books", Err: errors.New("connection refused")}
	}}
	f := newWorkerFixture(t, ModePlugin, bridge)
	a := f.addFile(t, "a.epub")
	b := f.addFile(t, "a.mobi")
	f.d.RunDeliveries(f.ctx)
	if bridge.addCount() != 1 {
		t.Errorf("adds = %d, want the pass to stop after the first", bridge.addCount())
	}
	for _, id := range []int64{a.ID, b.ID} {
		if got := f.row(t, id); got.State != models.CalibreDeliveryPending || got.Attempts != 0 {
			t.Errorf("row %d = %+v, want pending with no attempt", id, got)
		}
	}
}

func TestDeliverer_BusyBridgeIsNotAnAttempt(t *testing.T) {
	bridge := &fakeBridge{health: HealthState{Library: "/lib"}, add: func(string) (int64, error) {
		return 0, &PluginError{Status: http.StatusServiceUnavailable, Detail: "library swap"}
	}}
	f := newWorkerFixture(t, ModePlugin, bridge)
	row := f.addFile(t, "a.epub")
	f.d.RunDeliveries(f.ctx)
	if got := f.row(t, row.ID); got.Attempts != 0 || got.State != models.CalibreDeliveryPending {
		t.Fatalf("row = %+v, want untouched after a 503", got)
	}
}

func TestDeliverer_ErrDisabledIsNotAnAttempt(t *testing.T) {
	f := newWorkerFixture(t, ModeCalibredb, &fakeCalibredb{add: func(string) (int64, error) { return 0, ErrDisabled }})
	row := f.addFile(t, "a.epub")
	f.d.RunDeliveries(f.ctx)
	if got := f.row(t, row.ID); got.Attempts != 0 || got.State != models.CalibreDeliveryPending {
		t.Fatalf("row = %+v, want untouched", got)
	}
}

func TestDeliverer_MissingFileIsSkipped(t *testing.T) {
	bridge := &fakeBridge{add: added(1), health: HealthState{Library: "/lib"}}
	f := newWorkerFixture(t, ModePlugin, bridge)
	row := f.addFile(t, "a.epub")
	if err := os.Remove(row.FilePath); err != nil {
		t.Fatal(err)
	}
	f.d.RunDeliveries(f.ctx)
	got := f.row(t, row.ID)
	if got.State != models.CalibreDeliverySkipped || got.Outcome != deliverySkipNoFile {
		t.Fatalf("row = %+v, want skipped with reason %q", got, deliverySkipNoFile)
	}
	if bridge.addCount() != 0 {
		t.Errorf("adds = %d, want none for a missing file", bridge.addCount())
	}
}

// calibredb has no health endpoint and its failures are exec errors, which
// back off like any other failure. Its target library is the configured
// library path.
func TestDeliverer_CalibredbMode(t *testing.T) {
	cli := &fakeCalibredb{add: func(string) (int64, error) { return 0, errors.New("calibredb add: exit status 1: locked") }}
	f := newWorkerFixture(t, ModeCalibredb, cli)
	f.cfg.LibraryPath = "/lib"
	row := f.addFile(t, "a.epub")

	f.d.RunDeliveries(f.ctx)
	got := f.row(t, row.ID)
	if got.State != models.CalibreDeliveryPending || got.Attempts != 1 {
		t.Fatalf("row = %+v, want one attempt and a retry", got)
	}

	cli.add = added(1234)
	f.offset = time.Until(got.NextAttemptAt) + time.Second
	f.d.RunDeliveries(f.ctx)
	got = f.row(t, row.ID)
	if got.State != models.CalibreDeliveryDelivered || got.TargetLibrary != "/lib" || *got.CalibreID != 1234 {
		t.Fatalf("row = %+v, want delivered into /lib", got)
	}
	if id := f.calibreID(t); id == nil || *id != 1234 {
		t.Errorf("books.calibre_id = %v, want 1234", id)
	}
}

// TestDeliverer_CalibredbMissingWaitsAndSaysWhy replays #1940. On the
// official distroless image there is no calibredb, so calibredb mode can
// never deliver. Every import still looked successful and the only trace
// was a WARN line. A pass must leave the rows waiting without spending their
// attempts, and the settings queue view must say calibredb is missing and
// point at the Bridge plugin.
func TestDeliverer_CalibredbMissingWaitsAndSaysWhy(t *testing.T) {
	cfg := Config{Enabled: true, LibraryPath: t.TempDir(), BinaryPath: filepath.Join(t.TempDir(), "calibredb")}
	f := newWorkerFixture(t, ModeCalibredb, New(cfg))
	f.cfg = cfg
	row := f.addFile(t, "a.epub")

	f.d.RunDeliveries(f.ctx)

	got := f.row(t, row.ID)
	if got.State != models.CalibreDeliveryPending || got.Attempts != 0 {
		t.Fatalf("#1940: a missing calibredb is not the book's fault; row = %+v, want pending with no attempt spent", got)
	}
	h := f.d.Health()
	if h.Reachable == nil || *h.Reachable {
		t.Fatalf("#1940: the queue view must report calibredb unusable, got reachable=%v", h.Reachable)
	}
	for _, want := range []string{"calibredb is not installed", "Calibre Bridge plugin"} {
		if !strings.Contains(h.LastError, want) {
			t.Errorf("#1940: the reason shown must mention %q, got %q", want, h.LastError)
		}
	}
}

// metaCapture is a calibredb shaped adder that keeps the metadata it was
// handed.
type metaCapture struct {
	metas []Metadata
}

func (m *metaCapture) Add(_ context.Context, _ string, meta Metadata) (int64, error) {
	m.metas = append(m.metas, meta)
	return int64(len(m.metas)), nil
}

// TestDeliverer_FetchesEditionsForABookWithNone replays #1853. A Hardcover
// list sync stopped storing editions in #1784, so a list synced book reached
// Calibre with no ISBN, publisher or edition language: the handoff reads
// them from the editions table, and there were none. The delivery must fetch
// the book's editions when it has none on record, once, and use them.
func TestDeliverer_FetchesEditionsForABookWithNone(t *testing.T) {
	cli := &metaCapture{}
	f := newWorkerFixture(t, ModeCalibredb, cli)
	f.cfg.LibraryPath = "/lib"
	isbn := "9780441172719"
	calls := 0
	f.d.WithEditionHydrator(func(ctx context.Context, book *models.Book) error {
		calls++
		return f.editions.Upsert(ctx, &models.Edition{
			ForeignID: "hc-ed:1", BookID: book.ID, Title: book.Title,
			ISBN13: &isbn, Publisher: "Ace", Format: "EPUB", Language: "eng", IsEbook: true,
		})
	}, nil)
	f.addFile(t, "a.epub")

	f.d.RunDeliveries(f.ctx)

	if calls != 1 {
		t.Fatalf("expected one edition fetch for a book with none, got %d", calls)
	}
	if len(cli.metas) != 1 {
		t.Fatalf("expected one add, got %d", len(cli.metas))
	}
	meta := cli.metas[0]
	if meta.Identifiers["isbn"] != isbn {
		t.Errorf("#1853: the handoff must carry the fetched edition's ISBN, got identifiers %v", meta.Identifiers)
	}
	if meta.Publisher != "Ace" {
		t.Errorf("#1853: the handoff must carry the fetched edition's publisher, got %q", meta.Publisher)
	}

	// A book that now has editions on record is not fetched again.
	f.d.prepareEditions(withHydrateBudget(f.ctx), f.book)
	if calls != 1 {
		t.Errorf("editions on record must not be fetched again, got %d fetches", calls)
	}
}

// TestDeliverer_EditionFetchFailureStillDelivers: the fetch is best effort.
// A provider that fails leaves the handoff as it was, and the book still
// reaches Calibre.
func TestDeliverer_EditionFetchFailureStillDelivers(t *testing.T) {
	cli := &metaCapture{}
	f := newWorkerFixture(t, ModeCalibredb, cli)
	f.cfg.LibraryPath = "/lib"
	f.d.WithEditionHydrator(func(context.Context, *models.Book) error { return errors.New("hardcover down") }, nil)
	row := f.addFile(t, "a.epub")

	f.d.RunDeliveries(f.ctx)

	if got := f.row(t, row.ID); got.State != models.CalibreDeliveryDelivered {
		t.Fatalf("a failed edition fetch must not hold the delivery back, row = %+v", got)
	}
	if _, ok := cli.metas[0].Identifiers["isbn"]; ok {
		t.Errorf("no edition, no ISBN; got %v", cli.metas[0].Identifiers)
	}
}

// storingHydrator stores one edition per book it is asked about and counts
// the asks.
func storingHydrator(f *workerFixture, calls *int) EditionHydrator {
	return func(ctx context.Context, book *models.Book) error {
		*calls++
		isbn := fmt.Sprintf("97800000%05d", book.ID)
		return f.editions.Upsert(ctx, &models.Edition{
			ForeignID: fmt.Sprintf("hc-ed:%d", book.ID), BookID: book.ID, Title: book.Title,
			ISBN13: &isbn, Format: "EPUB", IsEbook: true,
		})
	}
}

// TestDeliverer_EditionFetchesAreCappedPerPass: a pass fetches editions for
// at most editionHydratePerRun books. The rest wait, untouched, for the next
// pass rather than going to Calibre without them.
func TestDeliverer_EditionFetchesAreCappedPerPass(t *testing.T) {
	cli := &metaCapture{}
	f := newWorkerFixture(t, ModeCalibredb, cli)
	f.cfg.LibraryPath = "/lib"
	calls := 0
	f.d.WithEditionHydrator(storingHydrator(f, &calls), nil)
	total := editionHydratePerRun + 2
	var rows []models.CalibreDelivery
	for i := 0; i < total; i++ {
		_, r := f.addBook(t, fmt.Sprintf("Capped%d", i), "a.epub")
		rows = append(rows, r...)
	}

	f.d.RunDeliveries(f.ctx)
	if calls != editionHydratePerRun || len(cli.metas) != editionHydratePerRun {
		t.Fatalf("first pass: %d fetches and %d adds, want %d of each", calls, len(cli.metas), editionHydratePerRun)
	}
	waiting := 0
	for _, r := range rows {
		got := f.row(t, r.ID)
		if got.State == models.CalibreDeliveryPending {
			waiting++
			if got.Attempts != 0 {
				t.Errorf("a row deferred for the cap must not spend an attempt, got %+v", got)
			}
		}
	}
	if waiting != total-editionHydratePerRun {
		t.Fatalf("%d rows waiting, want %d", waiting, total-editionHydratePerRun)
	}

	f.d.RunDeliveries(f.ctx)
	if calls != total || len(cli.metas) != total {
		t.Fatalf("second pass: %d fetches and %d adds, want %d of each", calls, len(cli.metas), total)
	}
	for _, m := range cli.metas {
		if m.Identifiers["isbn"] == "" {
			t.Errorf("every delivered book must carry its fetched ISBN, got %v", m.Identifiers)
		}
	}
}

// TestPullList_EditionFetchesAreCapped: a pull listing runs inside the
// plugin's request, so it fetches for at most editionHydratePerRun books and
// leaves the rest to a later listing.
func TestPullList_EditionFetchesAreCapped(t *testing.T) {
	f, _ := newPullFixture(t)
	calls := 0
	f.d.WithEditionHydrator(storingHydrator(f, &calls), nil)
	total := editionHydratePerRun + 3
	for i := 0; i < total; i++ {
		f.addBook(t, fmt.Sprintf("Pulled%d", i), "a.epub")
	}

	page, err := f.d.PullList(f.ctx, PullCaps{}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if calls != editionHydratePerRun {
		t.Fatalf("one listing made %d edition fetches, want at most %d", calls, editionHydratePerRun)
	}
	if len(page.Deliveries) != editionHydratePerRun {
		t.Fatalf("listed %d books, want the %d whose editions were fetched", len(page.Deliveries), editionHydratePerRun)
	}
	for _, it := range page.Deliveries {
		if it.Metadata.Identifiers["isbn"] == "" {
			t.Errorf("a listed book must carry its fetched ISBN, got %v", it.Metadata.Identifiers)
		}
	}

	page, err = f.d.PullList(f.ctx, PullCaps{}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if calls != total || len(page.Deliveries) != total {
		t.Fatalf("second listing: %d fetches, %d listed; want %d of each", calls, len(page.Deliveries), total)
	}
}

// TestDeliverer_OnlyAnAnswerIsRemembered: a fetch that fails or is cut off
// does not hold the book back from being asked again, while one Hardcover
// answered, even with no editions, is not repeated within the retry window.
func TestDeliverer_OnlyAnAnswerIsRemembered(t *testing.T) {
	f := newWorkerFixture(t, ModeCalibredb, &metaCapture{})
	calls := 0
	var answer error = context.DeadlineExceeded
	f.d.WithEditionHydrator(func(context.Context, *models.Book) error {
		calls++
		return answer
	}, nil)

	f.d.prepareEditions(withHydrateBudget(f.ctx), f.book)
	f.d.prepareEditions(withHydrateBudget(f.ctx), f.book)
	if calls != 2 {
		t.Fatalf("a cut off fetch must not block the next ask, got %d fetches", calls)
	}

	answer = nil // Hardcover answered, with nothing to store
	f.d.prepareEditions(withHydrateBudget(f.ctx), f.book)
	f.d.prepareEditions(withHydrateBudget(f.ctx), f.book)
	if calls != 3 {
		t.Fatalf("an answered book must not be asked again within the window, got %d fetches", calls)
	}
	f.offset = editionHydrateRetry + time.Minute
	f.d.prepareEditions(withHydrateBudget(f.ctx), f.book)
	if calls != 4 {
		t.Fatalf("after the window the book is asked again, got %d fetches", calls)
	}
}

// TestDeliverer_HydratorScopeSpendsNoBudget: a book the hydrator does not
// apply to is not fetched and leaves the allowance to books it does.
func TestDeliverer_HydratorScopeSpendsNoBudget(t *testing.T) {
	f := newWorkerFixture(t, ModeCalibredb, &metaCapture{})
	calls := 0
	f.d.WithEditionHydrator(func(context.Context, *models.Book) error { calls++; return nil },
		func(b *models.Book) bool { return b.MetadataProvider == "hardcover" })
	ctx := withHydrateBudget(f.ctx)
	for i := 0; i < editionHydratePerRun+1; i++ {
		if f.d.prepareEditions(ctx, f.book) {
			t.Fatal("a book the hydrator does not apply to must never be deferred")
		}
	}
	if calls != 0 {
		t.Fatalf("got %d fetches for a book out of scope", calls)
	}
}

// The tick and a kick share one lock taken with TryLock: whichever comes
// second returns at once instead of delivering the same row again.
func TestDeliverer_OverlappingPassesDoNotDoubleDeliver(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	bridge := &fakeBridge{health: HealthState{Library: "/lib"}, add: func(string) (int64, error) {
		once.Do(func() { close(entered) })
		<-release
		return 7, nil
	}}
	f := newWorkerFixture(t, ModePlugin, bridge)
	g := jobs.NewGroup(context.Background())
	f.d.WithJobs(g)
	row := f.addFile(t, "a.epub")

	f.d.Kick()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the kicked pass never reached Calibre")
	}

	// A tick while the kicked pass is inside Add must return at once.
	tickDone := make(chan struct{})
	go func() {
		f.d.RunDeliveries(f.ctx)
		close(tickDone)
	}()
	select {
	case <-tickDone:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("the tick waited for the running pass instead of returning")
	}
	f.d.Kick()

	close(release)
	if stuck := g.Shutdown(5 * time.Second); len(stuck) != 0 {
		t.Fatalf("jobs still running: %v", stuck)
	}
	if bridge.addCount() != 1 {
		t.Fatalf("adds = %d, want exactly one delivery", bridge.addCount())
	}
	if got := f.row(t, row.ID); got.State != models.CalibreDeliveryDelivered {
		t.Fatalf("state = %s, want delivered", got.State)
	}
}

// The payload is built at delivery time: the row's edition, the primary
// series and the cover all come from the book as it is when Calibre is
// reached, not when the import queued it.
func TestDeliverer_BuildsMetadataAtDeliveryTime(t *testing.T) {
	bridge := &fakeBridge{add: added(1), supportsCover: true, health: HealthState{Library: "/lib"}}
	f := newWorkerFixture(t, ModePlugin, bridge)

	store := covers.NewStore(t.TempDir())
	src := filepath.Join(t.TempDir(), "cover.jpg")
	if err := os.WriteFile(src, testJPEG(), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put(src)
	if err != nil {
		t.Fatal(err)
	}
	f.d.WithCovers(CoverSource{Store: store})

	grabbed := &models.Edition{ForeignID: "OL1M", BookID: f.book.ID, Title: "Dune", Publisher: "Chilton", Format: "EPUB", ImageURL: ref}
	other := &models.Edition{ForeignID: "OL2M", BookID: f.book.ID, Title: "Dune", Publisher: "Ace", Format: "EPUB"}
	for _, e := range []*models.Edition{other, grabbed} {
		if err := f.editions.Upsert(f.ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(t.TempDir(), "a.epub")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(f.ctx, f.book.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatal(err)
	}
	files, _ := f.books.ListFiles(f.ctx, f.book.ID)
	if _, err := f.d.Enqueue(f.ctx, f.book.ID, files[0].ID, &grabbed.ID, path); err != nil {
		t.Fatal(err)
	}
	// Changed after the import queued the file: the delivery must see it.
	f.book.Title = "Dune (retitled)"
	if err := f.books.Update(f.ctx, f.book); err != nil {
		t.Fatal(err)
	}

	f.d.RunDeliveries(f.ctx)

	if bridge.addCount() != 1 {
		t.Fatalf("adds = %d", bridge.addCount())
	}
	meta := bridge.metas[0]
	if meta.Title != "Dune (retitled)" || len(meta.Authors) != 1 || meta.Authors[0] != "Frank Herbert" || meta.AuthorSort != "Herbert, Frank" {
		t.Errorf("metadata = %+v, want the current title and the author", meta)
	}
	if meta.Publisher != "Chilton" {
		t.Errorf("publisher = %q, want the row's edition", meta.Publisher)
	}
	want, _, _ := store.Resolve(ref)
	if meta.CoverPath != want || want == "" {
		t.Errorf("cover = %q, want the stored cover %q", meta.CoverPath, want)
	}
	if meta.Identifiers["bindery"] == "" {
		t.Errorf("identifiers = %+v, want the bindery id", meta.Identifiers)
	}
}

// Against the real plugin client: a bridge that is down is not an attempt,
// and a bridge that answers gets the book and reports the library.
func TestDeliverer_RealPluginClient(t *testing.T) {
	var mu sync.Mutex
	up := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"plugin_version":"0.6.2","calibre_version":"7.0","library":"/calibre/lib","capabilities":["book_metadata","cover","error_codes"]}`))
		case "/v1/books":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":5678}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	f := newWorkerFixture(t, ModePlugin, NewPluginClient(srv.URL, "k"))
	row := f.addFile(t, "a.epub")

	f.d.RunDeliveries(f.ctx)
	if got := f.row(t, row.ID); got.State != models.CalibreDeliveryPending || got.Attempts != 0 {
		t.Fatalf("row = %+v, want untouched while the bridge is down", got)
	}

	mu.Lock()
	up = true
	mu.Unlock()
	f.d.RunDeliveries(f.ctx)
	got := f.row(t, row.ID)
	if got.State != models.CalibreDeliveryDelivered || *got.CalibreID != 5678 || got.TargetLibrary != "/calibre/lib" {
		t.Fatalf("row = %+v, want delivered as 5678 into /calibre/lib", got)
	}
}

func TestPluginClient_AddReturnsTheErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_, _ = w.Write([]byte(`{"capabilities":["book_metadata","error_codes"]}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"cannot determine book format","code":"bad_format"}`))
	}))
	defer srv.Close()
	_, err := NewPluginClient(srv.URL, "k").Add(context.Background(), "/x/a.zzz", Metadata{Title: "T"})
	if PluginErrorCode(err) != "bad_format" {
		t.Fatalf("code = %q from %v, want bad_format", PluginErrorCode(err), err)
	}
	if err.Error() != "plugin client: server error 400: cannot determine book format (bad_format)" {
		t.Errorf("message = %q, want the old wording kept", err.Error())
	}
}

// testJPEG is the smallest byte sequence the cover store accepts as a JPEG.
func testJPEG() []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}, make([]byte, 32)...)
}
