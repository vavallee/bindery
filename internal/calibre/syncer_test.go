package calibre

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// waitUntil polls the predicate every millisecond until it returns true or
// the deadline elapses. Used to wait for the Syncer's background goroutine
// to reach/leave a state without busy-spinning without yielding.
func waitUntil(t *testing.T, timeout time.Duration, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

// fakeBookLister is a test double for the syncer's BookLister dependency.
// Captures SetCalibreID calls so we can assert that both fresh pushes and
// 409-conflict responses persist the id when one is returned.
type fakeBookLister struct {
	books []models.Book
	mu    sync.Mutex
	set   map[int64]int64 // bookID → calibreID
}

func (f *fakeBookLister) ListByStatus(_ context.Context, _ string) ([]models.Book, error) {
	return f.books, nil
}

func (f *fakeBookLister) SetCalibreID(_ context.Context, id, calibreID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.set == nil {
		f.set = map[int64]int64{}
	}
	f.set[id] = calibreID
	return nil
}

type fakeAuthorGetter struct {
	authors map[int64]*models.Author
}

func (f fakeAuthorGetter) GetByID(_ context.Context, id int64) (*models.Author, error) {
	return f.authors[id], nil
}

type fakeEditionLister struct {
	editions map[int64][]models.Edition
}

func (f fakeEditionLister) ListByBook(_ context.Context, bookID int64) ([]models.Edition, error) {
	return f.editions[bookID], nil
}

// fakePusher is an inline pluginPusher that dispatches per-path behaviour
// so we can verify pushed / already_in_calibre / failed all get counted
// correctly inside one sync run.
type fakePusher struct {
	calls      map[string]func() (int64, error)
	library    string
	libraryErr error
	mu         sync.Mutex
	metas      map[string]Metadata
}

func (f *fakePusher) Add(_ context.Context, path string, meta Metadata) (int64, error) {
	f.mu.Lock()
	if f.metas == nil {
		f.metas = map[string]Metadata{}
	}
	f.metas[path] = meta
	f.mu.Unlock()
	fn, ok := f.calls[path]
	if !ok {
		return 0, errors.New("unexpected path")
	}
	return fn()
}

func (f *fakePusher) Library(_ context.Context) (string, error) {
	return f.library, f.libraryErr
}

func (f *fakePusher) meta(path string) Metadata {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.metas[path]
}

func TestSyncer_Start_MixedOutcomesCountedCorrectly(t *testing.T) {
	books := &fakeBookLister{
		books: []models.Book{
			{ID: 1, Title: "Fresh", FilePath: "/l/fresh.epub"},
			{ID: 2, Title: "Duplicate", FilePath: "/l/dup.epub"},
			{ID: 3, Title: "Broken", FilePath: "/l/broken.epub"},
			{ID: 4, Title: "NoFile"}, // must be filtered out by the syncer
		},
	}
	pusher := &fakePusher{calls: map[string]func() (int64, error){
		"/l/fresh.epub":  func() (int64, error) { return 101, nil },
		"/l/dup.epub":    func() (int64, error) { return 202, ErrAlreadyInCalibre },
		"/l/broken.epub": func() (int64, error) { return 0, errors.New("boom") },
	}}

	s := NewSyncer(books)
	// Inject the fake pusher in place of the real HTTP client factory.
	s.newClient = func(_ Config) pluginPusher { return pusher }

	if err := s.Start(context.Background(), Config{}, ModePlugin); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitUntil(t, 2*time.Second, func() bool { return !s.Running() })
	p := s.Progress()
	if p.Stats.Total != 3 { // book 4 has no file path; excluded
		t.Errorf("Total: got %d, want 3", p.Stats.Total)
	}
	if p.Stats.Pushed != 1 {
		t.Errorf("Pushed: got %d, want 1", p.Stats.Pushed)
	}
	if p.Stats.AlreadyInCalibre != 1 {
		t.Errorf("AlreadyInCalibre: got %d, want 1", p.Stats.AlreadyInCalibre)
	}
	if p.Stats.Failed != 1 {
		t.Errorf("Failed: got %d, want 1", p.Stats.Failed)
	}
	if len(p.Errors) != 1 || p.Errors[0].BookID != 3 {
		t.Errorf("Errors: got %+v, want one entry for book 3", p.Errors)
	}
	if books.set[1] != 101 {
		t.Errorf("expected fresh book to persist calibre_id=101, got %d", books.set[1])
	}
	if books.set[2] != 202 {
		t.Errorf("expected duplicate book to persist calibre_id=202 from 409 response, got %d", books.set[2])
	}
}

func TestSyncer_Start_PassesPresentBookEditionAndAuthorMetadata(t *testing.T) {
	editionASIN := "B000FC1BN8"
	isbn := "9780441172719"
	books := &fakeBookLister{
		books: []models.Book{{
			ID:               42,
			AuthorID:         7,
			Title:            "Dune",
			FilePath:         "/l/dune.epub",
			ForeignID:        "gb:zyTCAlFPjgYC",
			MetadataProvider: "googlebooks",
			ASIN:             "BOOKASIN",
		}},
	}
	pusher := &fakePusher{calls: map[string]func() (int64, error){
		"/l/dune.epub": func() (int64, error) { return 101, nil },
	}}

	s := NewSyncer(books)
	s.WithMetadata(
		fakeAuthorGetter{authors: map[int64]*models.Author{
			7: {ID: 7, Name: "Frank Herbert", SortName: "Herbert, Frank"},
		}},
		fakeEditionLister{editions: map[int64][]models.Edition{
			42: {{
				ID:        99,
				BookID:    42,
				ForeignID: "/books/OL999M",
				Format:    "EPUB",
				ISBN13:    &isbn,
				ASIN:      &editionASIN,
			}},
		}},
	)
	s.newClient = func(_ Config) pluginPusher { return pusher }

	if err := s.Start(context.Background(), Config{}, ModePlugin); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return !s.Running() })

	meta := pusher.meta("/l/dune.epub")
	if meta.Identifiers["bindery"] != "42" {
		t.Fatalf("bindery identifier = %q, want 42", meta.Identifiers["bindery"])
	}
	if meta.Identifiers["google"] != "zyTCAlFPjgYC" {
		t.Fatalf("google identifier = %q, want zyTCAlFPjgYC", meta.Identifiers["google"])
	}
	if meta.Identifiers["asin"] != "B000FC1BN8" {
		t.Fatalf("asin identifier = %q, want edition ASIN", meta.Identifiers["asin"])
	}
	if meta.Identifiers["isbn"] != "9780441172719" {
		t.Fatalf("isbn identifier = %q, want edition ISBN", meta.Identifiers["isbn"])
	}
	if meta.Identifiers["openlibrary_edition"] != "OL999M" {
		t.Fatalf("openlibrary_edition identifier = %q, want OL999M", meta.Identifiers["openlibrary_edition"])
	}
	if len(meta.Authors) != 1 || meta.Authors[0] != "Frank Herbert" {
		t.Fatalf("authors = %#v, want Frank Herbert", meta.Authors)
	}
	if meta.AuthorSort != "Herbert, Frank" {
		t.Fatalf("author sort = %q, want Herbert, Frank", meta.AuthorSort)
	}
}

func TestSyncer_Start_DifferentiatesSameTitleByAuthorAndISBN(t *testing.T) {
	blakeISBN := "9780140422153"
	sextonISBN := "9781504034364"
	books := &fakeBookLister{
		books: []models.Book{
			{ID: 1, AuthorID: 10, Title: "The Complete Poems", FilePath: "/l/blake.epub"},
			{ID: 2, AuthorID: 20, Title: "The Complete Poems", FilePath: "/l/sexton.azw3"},
		},
	}
	pusher := &fakePusher{calls: map[string]func() (int64, error){
		"/l/blake.epub":  func() (int64, error) { return 301, nil },
		"/l/sexton.azw3": func() (int64, error) { return 302, nil },
	}}

	s := NewSyncer(books).WithMetadata(
		fakeAuthorGetter{authors: map[int64]*models.Author{
			10: {ID: 10, Name: "William Blake", SortName: "Blake, William"},
			20: {ID: 20, Name: "Anne Sexton", SortName: "Sexton, Anne"},
		}},
		fakeEditionLister{editions: map[int64][]models.Edition{
			1: {{ID: 101, BookID: 1, Title: "The Complete Poems", Format: "EPUB", ISBN13: &blakeISBN}},
			2: {{ID: 201, BookID: 2, Title: "The Complete Poems", Format: "AZW3", ISBN13: &sextonISBN}},
		}},
	)
	s.newClient = func(_ Config) pluginPusher { return pusher }

	if err := s.Start(context.Background(), Config{}, ModePlugin); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return !s.Running() })

	blake := pusher.meta("/l/blake.epub")
	sexton := pusher.meta("/l/sexton.azw3")
	if got := blake.Identifiers["isbn"]; got != blakeISBN {
		t.Fatalf("Blake isbn = %q, want %s", got, blakeISBN)
	}
	if got := sexton.Identifiers["isbn"]; got != sextonISBN {
		t.Fatalf("Sexton isbn = %q, want %s", got, sextonISBN)
	}
	if len(blake.Authors) != 1 || blake.Authors[0] != "William Blake" {
		t.Fatalf("Blake authors = %#v", blake.Authors)
	}
	if len(sexton.Authors) != 1 || sexton.Authors[0] != "Anne Sexton" {
		t.Fatalf("Sexton authors = %#v", sexton.Authors)
	}
	if blake.Identifiers["isbn"] == sexton.Identifiers["isbn"] {
		t.Fatalf("same-title books exported with identical ISBN metadata: Blake=%+v Sexton=%+v", blake, sexton)
	}
}

func TestSyncer_Start_DoesNotReuseSourceCalibreIDForDifferentTargetLibrary(t *testing.T) {
	isbn := "9781504034364"
	books := &fakeBookLister{
		books: []models.Book{{
			ID:               1,
			AuthorID:         20,
			Title:            "The Complete Poems",
			FilePath:         "/source/Anne Sexton/The Complete Poems.azw3",
			ForeignID:        "calibre:book:2767",
			MetadataProvider: "calibre",
			CalibreID:        int64Ptr(2767),
		}},
	}
	pusher := &fakePusher{
		library: "/target-calibre",
		calls: map[string]func() (int64, error){
			"/source/Anne Sexton/The Complete Poems.azw3": func() (int64, error) { return 101, nil },
		},
	}

	s := NewSyncer(books).WithMetadata(
		fakeAuthorGetter{authors: map[int64]*models.Author{
			20: {ID: 20, Name: "Anne Sexton", SortName: "Sexton, Anne"},
		}},
		fakeEditionLister{editions: map[int64][]models.Edition{
			1: {{ID: 201, BookID: 1, Title: "The Complete Poems", Format: "AZW3", ISBN13: &isbn}},
		}},
	)
	s.newClient = func(_ Config) pluginPusher { return pusher }

	if err := s.Start(context.Background(), Config{LibraryPath: "/source-calibre"}, ModePlugin); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return !s.Running() })

	if _, ok := books.set[1]; ok {
		t.Fatalf("source calibre_id was overwritten for a different target library: %+v", books.set)
	}
	meta := pusher.meta("/source/Anne Sexton/The Complete Poems.azw3")
	if _, ok := meta.Identifiers["calibre"]; ok {
		t.Fatalf("source calibre identifier leaked into different target library payload: %+v", meta.Identifiers)
	}
	if meta.Identifiers["isbn"] != isbn {
		t.Fatalf("isbn identifier = %q, want %s", meta.Identifiers["isbn"], isbn)
	}
	if meta.Identifiers["bindery"] != "1" {
		t.Fatalf("bindery identifier = %q, want 1", meta.Identifiers["bindery"])
	}
}

func TestSyncer_Start_PersistsCalibreIDForSameTargetLibrary(t *testing.T) {
	books := &fakeBookLister{
		books: []models.Book{{
			ID:               1,
			Title:            "Existing",
			FilePath:         "/library/existing.epub",
			ForeignID:        "calibre:book:2767",
			MetadataProvider: "calibre",
			CalibreID:        int64Ptr(2767),
		}},
	}
	pusher := &fakePusher{
		library: "/library",
		calls: map[string]func() (int64, error){
			"/library/existing.epub": func() (int64, error) { return 2767, ErrAlreadyInCalibre },
		},
	}

	s := NewSyncer(books)
	s.newClient = func(_ Config) pluginPusher { return pusher }

	if err := s.Start(context.Background(), Config{LibraryPath: "/library"}, ModePlugin); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return !s.Running() })

	if books.set[1] != 2767 {
		t.Fatalf("same-library calibre_id should be persisted, got %+v", books.set)
	}
	meta := pusher.meta("/library/existing.epub")
	if meta.Identifiers["calibre"] != "calibre:book:2767" {
		t.Fatalf("same-library calibre identifier = %q", meta.Identifiers["calibre"])
	}
}

func TestSyncer_Start_RejectsNonPluginMode(t *testing.T) {
	s := NewSyncer(&fakeBookLister{})
	if err := s.Start(context.Background(), Config{}, ModeCalibredb); !errors.Is(err, ErrSyncModeNotPlugin) {
		t.Errorf("want ErrSyncModeNotPlugin, got %v", err)
	}
}

func TestSyncer_Start_RejectsConcurrentRuns(t *testing.T) {
	// Feed the first run one book whose push blocks, so the second Start
	// observes running=true. Use a channel instead of a sleep to keep the
	// test deterministic.
	release := make(chan struct{})
	books := &fakeBookLister{books: []models.Book{{ID: 1, Title: "A", FilePath: "/x"}}}
	pusher := &fakePusher{calls: map[string]func() (int64, error){
		"/x": func() (int64, error) { <-release; return 1, nil },
	}}
	s := NewSyncer(books)
	s.newClient = func(_ Config) pluginPusher { return pusher }

	if err := s.Start(context.Background(), Config{}, ModePlugin); err != nil {
		t.Fatalf("first start: %v", err)
	}
	waitUntil(t, time.Second, s.Running)
	if err := s.Start(context.Background(), Config{}, ModePlugin); !errors.Is(err, ErrSyncAlreadyRunning) {
		t.Errorf("second start: want ErrSyncAlreadyRunning, got %v", err)
	}
	close(release)
	waitUntil(t, 2*time.Second, func() bool { return !s.Running() })
}

func int64Ptr(v int64) *int64 { return &v }
