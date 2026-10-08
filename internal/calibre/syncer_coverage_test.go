package calibre

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/models"
)

func TestEditionForFile(t *testing.T) {
	sel := int64(3)
	editions := []models.Edition{
		{ID: 1, Format: "PDF"},
		{ID: 2, Format: " epub "},
		{ID: 3, Format: "EPUB"},
		{ID: 4, Format: "MOBI"},
	}
	plain := &models.Book{}
	selected := &models.Book{SelectedEditionID: &sel}
	selMissing := int64(99)
	staleSel := &models.Book{SelectedEditionID: &selMissing}

	cases := []struct {
		name     string
		editions []models.Edition
		book     *models.Book
		path     string
		want     int64
	}{
		{"no editions", nil, plain, "/a.epub", 0},
		{"first format match", editions, plain, "/a.epub", 2},
		{"selected edition among format matches", editions, selected, "/a.EPUB", 3},
		{"format match beats a selected edition of another format", editions, selected, "/a.mobi", 4},
		{"no format match falls back to the selected edition", editions, selected, "/a.azw3", 3},
		{"no extension falls back to the selected edition", editions, selected, "/a", 3},
		{"stale selection falls back to the first edition", editions, staleSel, "/a.azw3", 1},
		{"nothing matches: first edition", editions, plain, "/a.cbz", 1},
	}
	for _, tc := range cases {
		got := editionForFile(tc.editions, tc.book, tc.path)
		var id int64
		if got != nil {
			id = got.ID
		}
		if id != tc.want {
			t.Errorf("%s: edition %d, want %d", tc.name, id, tc.want)
		}
	}
}

// covErrLister fails whichever call the test names.
type covErrLister struct {
	fakeBookLister
	statusErr, listErr, filesErr error
}

func (c *covErrLister) ListByStatus(ctx context.Context, s string) ([]models.Book, error) {
	if c.statusErr != nil {
		return nil, c.statusErr
	}
	return c.fakeBookLister.ListByStatus(ctx, s)
}

func (c *covErrLister) List(ctx context.Context) ([]models.Book, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	return c.fakeBookLister.List(ctx)
}

func (c *covErrLister) ListFiles(ctx context.Context, id int64) ([]models.BookFile, error) {
	if c.filesErr != nil {
		return nil, c.filesErr
	}
	return c.fakeBookLister.ListFiles(ctx, id)
}

type covErrLedger struct{ err error }

func (l covErrLedger) ListAll(context.Context) ([]models.CalibreDelivery, error) { return nil, l.err }

type covErrQueue struct{ kicks int }

func (q *covErrQueue) Enqueue(context.Context, int64, int64, *int64, string) (bool, error) {
	return false, errors.New("queue is closed")
}
func (q *covErrQueue) Kick() { q.kicks++ }

func covWaitDone(t *testing.T, s *Syncer) SyncProgress {
	t.Helper()
	var p SyncProgress
	waitUntil(t, 2*time.Second, func() bool {
		var err error
		p, err = s.Progress(context.Background())
		return err == nil && !p.Queueing
	})
	return p
}

func TestSyncer_QueueingFailures(t *testing.T) {
	book := models.Book{ID: 1, Title: "Dune", Status: models.BookStatusImported}
	files := map[int64][]models.BookFile{1: {ebook(10, 1, "/b/dune.epub")}}

	t.Run("listing books fails", func(t *testing.T) {
		s := NewSyncer(&covErrLister{statusErr: errors.New("db gone")}, &fakeLedger{}, &fakeQueue{ledger: &fakeLedger{}})
		if err := s.Start(ModePlugin); err != nil {
			t.Fatal(err)
		}
		p := covWaitDone(t, s)
		if !strings.Contains(p.Error, "list imported books: db gone") || p.Message != "failed" || p.FinishedAt == nil {
			t.Fatalf("progress = %+v, want the listing failure", p)
		}
	})
	t.Run("reading the ledger fails", func(t *testing.T) {
		s := NewSyncer(&fakeBookLister{books: []models.Book{book}, files: files}, covErrLedger{err: errors.New("locked")}, &fakeQueue{ledger: &fakeLedger{}})
		if err := s.Start(ModePlugin); err != nil {
			t.Fatal(err)
		}
		p := covWaitDone(t, s)
		if !strings.Contains(p.Error, "read the delivery queue: locked") {
			t.Fatalf("progress = %+v, want the ledger failure", p)
		}
	})
	t.Run("files and queue fail per book", func(t *testing.T) {
		second := models.Book{ID: 2, Title: "Emma", Status: models.BookStatusImported}
		lister := &covErrLister{fakeBookLister: fakeBookLister{books: []models.Book{book}, files: files}, listErr: errors.New("no catalogue")}
		q := &covErrQueue{}
		s := NewSyncer(lister, &fakeLedger{}, q)
		if err := s.Start(ModePlugin); err != nil {
			t.Fatal(err)
		}
		p := covWaitDone(t, s)
		if p.Stats.Failed != 1 || len(p.Errors) != 1 || !strings.Contains(p.Errors[0].Reason, "queueing failed: queue is closed") {
			t.Fatalf("progress = %+v, want one queueing failure", p)
		}
		if q.kicks != 0 {
			t.Errorf("kicked the worker %d times with nothing queued", q.kicks)
		}

		lister = &covErrLister{fakeBookLister: fakeBookLister{books: []models.Book{book, second}, files: files}, filesErr: errors.New("io")}
		s = NewSyncer(lister, &fakeLedger{}, &fakeQueue{ledger: &fakeLedger{}})
		if err := s.Start(ModePlugin); err != nil {
			t.Fatal(err)
		}
		p = covWaitDone(t, s)
		if p.Stats.Failed != 2 || !strings.Contains(p.Errors[0].Reason, "listing the book's files failed: io") {
			t.Fatalf("progress = %+v, want both books failed on their files", p)
		}
	})
	t.Run("progress surfaces a ledger error once queued", func(t *testing.T) {
		ledger := &fakeLedger{}
		s := NewSyncer(&fakeBookLister{books: []models.Book{book}, files: files}, ledger, &fakeQueue{ledger: ledger})
		p := runPushAll(t, s)
		if p.Stats.Total != 1 {
			t.Fatalf("progress = %+v, want one book", p)
		}
		s.ledger = covErrLedger{err: errors.New("gone")}
		if _, err := s.Progress(context.Background()); err == nil {
			t.Fatal("Progress: want the ledger error")
		}
	})
}

func TestSyncer_StartRefusals(t *testing.T) {
	s, _, _ := newTestSyncer(&fakeBookLister{})
	if err := s.Start(ModeCalibredb); !errors.Is(err, ErrSyncModeNotPlugin) {
		t.Fatalf("calibredb mode: %v, want ErrSyncModeNotPlugin", err)
	}
	if p, err := s.Progress(context.Background()); err != nil || p.Errors == nil || p.Skips == nil || p.Running {
		t.Fatalf("progress before any run = %+v, %v; want empty, non-nil lists", p, err)
	}

	gate := make(chan struct{})
	blocked, _, _ := newTestSyncer(&fakeBookLister{gate: gate})
	if err := blocked.Start(ModePlugin); err != nil {
		t.Fatal(err)
	}
	if err := blocked.Start(ModePlugin); !errors.Is(err, ErrSyncAlreadyRunning) {
		t.Fatalf("second start: %v, want ErrSyncAlreadyRunning", err)
	}
	if p, _ := blocked.Progress(context.Background()); !p.Queueing || !p.Running {
		t.Fatalf("progress while queueing = %+v", p)
	}
	close(gate)
	covWaitDone(t, blocked)
}

// A jobs group that is shutting down refuses the job, and the run is closed
// out with a message rather than left queueing forever.
func TestSyncer_WithJobsShuttingDown(t *testing.T) {
	g := jobs.NewGroup(context.Background())
	g.Shutdown(time.Second)
	s, _, _ := newTestSyncer(&fakeBookLister{})
	s.WithJobs(g)
	if err := s.Start(ModePlugin); err != nil {
		t.Fatal(err)
	}
	p, err := s.Progress(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Queueing || p.Error != "Bindery is shutting down" || p.FinishedAt == nil {
		t.Fatalf("progress = %+v, want the run closed out as shutting down", p)
	}
}

func TestSyncer_WithJobsRunsInTheGroup(t *testing.T) {
	g := jobs.NewGroup(context.Background())
	defer g.Shutdown(time.Second)
	ledger := &fakeLedger{}
	q := &fakeQueue{ledger: ledger}
	s := NewSyncer(&fakeBookLister{
		books: []models.Book{{ID: 1, Title: "Dune", Status: models.BookStatusImported}},
		files: map[int64][]models.BookFile{1: {ebook(10, 1, "/b/dune.epub")}},
	}, ledger, q).WithJobs(g)
	p := runPushAll(t, s)
	if enq, kicks := q.calls(); len(enq) != 1 || kicks != 1 || p.Stats.Total != 1 {
		t.Fatalf("enqueued %v kicks %d progress %+v; want one file queued and one kick", enq, kicks, p)
	}
}

// A cancelled jobs group stops the walk before the first book.
func TestSyncer_CancelledWalk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, _, q := newTestSyncer(&fakeBookLister{
		books: []models.Book{{ID: 1, Title: "Dune", Status: models.BookStatusImported}},
		files: map[int64][]models.BookFile{1: {ebook(10, 1, "/b/dune.epub")}},
	})
	run := &syncRun{queueing: true, tracked: map[int64]trackedFile{}}
	s.run = run
	s.queueAll(ctx, run)
	if !strings.HasPrefix(run.err, "cancelled:") || run.queueing {
		t.Fatalf("run = %+v, want it closed out as cancelled", run)
	}
	if enq, _ := q.calls(); len(enq) != 0 {
		t.Fatalf("queued %v after cancel", enq)
	}
}

func TestImporter_DefaultReaderAndRunSyncEdges(t *testing.T) {
	imp, fr, _, books, _, _, settings := newImporterFixture(t)
	ctx := context.Background()

	// The default opener reads metadata.db from disk; a directory without
	// one is an import failure, not a panic.
	imp.openReader = NewImporter(nil, nil, nil, nil, nil).openReader
	if _, err := imp.Run(ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "metadata.db") {
		t.Fatalf("Run on an empty dir: %v, want the missing metadata.db error", err)
	}
	if p := imp.Progress(); p.Error == "" || p.Running || p.Message != "failed" {
		t.Fatalf("progress = %+v, want a finished failure", p)
	}
	root := buildFixtureLibrary(t)
	r, err := imp.openReader(root)
	if err != nil {
		t.Fatal(err)
	}
	if rr, ok := r.(*Reader); !ok || rr.LibraryPath() == "" || !filepath.IsAbs(rr.LibraryPath()) {
		t.Fatalf("default reader = %#v, want a *Reader with an absolute library path", r)
	}
	_ = r.Close()

	// RunSync with import enabled but no library path does nothing.
	imp.openReader = func(string) (readerIface, error) { return fr, nil }
	fr.books = []CalibreBook{sampleCalibreBook(1, "Never Imported", "Nobody")}
	if err := settings.Set(ctx, "calibre.library_import_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	imp.RunSync(ctx)
	if got, _ := books.List(ctx); len(got) != 0 {
		t.Fatalf("RunSync without a library path imported %d books", len(got))
	}
	// A failing library is logged, not fatal.
	if err := settings.Set(ctx, "calibre.library_path", "/lib"); err != nil {
		t.Fatal(err)
	}
	fr.err = errors.New("library locked")
	imp.RunSync(ctx)
	if p := imp.Progress(); p.Error != "library locked" {
		t.Fatalf("progress = %+v, want the reader error", p)
	}
}

// A book created by an earlier run that crashed before calibre_id was
// written is found by its synthetic foreign id and linked, not duplicated.
func TestImporter_UpsertBookAdoptsAForeignIDMatch(t *testing.T) {
	imp, fr, authors, books, _, _, _ := newImporterFixture(t)
	ctx := context.Background()
	a := &models.Author{ForeignID: "OLA9", Name: "Jane Austen", SortName: "Austen, Jane", Monitored: true}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	orphan := &models.Book{ForeignID: "calibre:book:7", AuthorID: a.ID, Title: "Old Title", SortTitle: "Old Title",
		Status: models.BookStatusWanted, Monitored: true, MetadataProvider: "calibre"}
	if err := books.Create(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	fr.books = []CalibreBook{sampleCalibreBook(7, "Persuasion", "Jane Austen")}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatal(err)
	}
	all, _ := books.List(ctx)
	if len(all) != 1 {
		t.Fatalf("books = %d, want the orphan adopted rather than a duplicate", len(all))
	}
	got, _ := books.GetByID(ctx, orphan.ID)
	if got.CalibreID == nil || *got.CalibreID != 7 || got.Title != "Persuasion" {
		t.Fatalf("adopted book = %+v, want calibre id 7 and the library title", got)
	}
}

func TestDefaultRunnerRunsTheCommand(t *testing.T) {
	if _, err := exec.LookPath("echo"); err != nil {
		t.Skip("no echo on PATH")
	}
	out, err := defaultRunner(context.Background(), "echo", "bindery")
	if err != nil || strings.TrimSpace(string(out)) != "bindery" {
		t.Fatalf("defaultRunner = %q, %v", out, err)
	}
	if _, err := defaultRunner(context.Background(), filepath.Join(t.TempDir(), "missing-binary")); err == nil {
		t.Fatal("a missing binary must fail")
	}
}
