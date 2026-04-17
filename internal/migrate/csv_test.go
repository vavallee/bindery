package migrate

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// stubProvider is a minimal metadata.Provider used to drive the migrate
// package without reaching any real network service.
type stubProvider struct {
	searchAuthorsFn func(ctx context.Context, q string) ([]models.Author, error)
	getAuthorFn     func(ctx context.Context, id string) (*models.Author, error)
}

func (s *stubProvider) Name() string { return "stub" }
func (s *stubProvider) SearchAuthors(ctx context.Context, q string) ([]models.Author, error) {
	if s.searchAuthorsFn != nil {
		return s.searchAuthorsFn(ctx, q)
	}
	return nil, nil
}
func (s *stubProvider) SearchBooks(context.Context, string) ([]models.Book, error) {
	return nil, nil
}
func (s *stubProvider) GetAuthor(ctx context.Context, id string) (*models.Author, error) {
	if s.getAuthorFn != nil {
		return s.getAuthorFn(ctx, id)
	}
	return nil, nil
}
func (s *stubProvider) GetBook(context.Context, string) (*models.Book, error) { return nil, nil }
func (s *stubProvider) GetEditions(context.Context, string) ([]models.Edition, error) {
	return nil, nil
}
func (s *stubProvider) GetBookByISBN(context.Context, string) (*models.Book, error) {
	return nil, nil
}

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		in       string
		fallback bool
		want     bool
	}{
		{"true", false, true},
		{"TRUE", false, true},
		{"1", false, true},
		{"yes", false, true},
		{"y", false, true},
		{"t", false, true},
		{"false", true, false},
		{"0", true, false},
		{"no", true, false},
		{"n", true, false},
		{"f", true, false},
		{"  True  ", false, true},
		{"garbage", true, true},
		{"", false, false},
	}
	for _, tt := range tests {
		if got := parseBool(tt.in, tt.fallback); got != tt.want {
			t.Errorf("parseBool(%q, %v) = %v, want %v", tt.in, tt.fallback, got, tt.want)
		}
	}
}

func TestRowFromFields(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want csvRow
	}{
		{"empty", []string{}, csvRow{monitored: true, searchOnAdd: false}},
		{"name only", []string{" Andy Weir "}, csvRow{name: "Andy Weir", monitored: true, searchOnAdd: false}},
		{"name+monitored false", []string{"A", "false"}, csvRow{name: "A", monitored: false, searchOnAdd: false}},
		{"name+monitored+search", []string{"A", "true", "true"}, csvRow{name: "A", monitored: true, searchOnAdd: true}},
		{"unparseable monitored falls back", []string{"A", "gibberish"}, csvRow{name: "A", monitored: true, searchOnAdd: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rowFromFields(tt.in)
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseCSVRows(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []csvRow
		wantErr bool
	}{
		{
			name: "plain list",
			in:   "Andy Weir\nN. K. Jemisin\n\n# a comment\n  Ursula K. Le Guin  \n",
			want: []csvRow{
				{name: "Andy Weir", monitored: true},
				{name: "N. K. Jemisin", monitored: true},
				{name: "Ursula K. Le Guin", monitored: true},
			},
		},
		{
			name: "csv two cols",
			in:   "Andy Weir,true\nIsaac Asimov,false\n",
			want: []csvRow{
				{name: "Andy Weir", monitored: true, searchOnAdd: false},
				{name: "Isaac Asimov", monitored: false, searchOnAdd: false},
			},
		},
		{
			name: "csv three cols",
			in:   "Andy Weir,true,true\nIsaac Asimov,true,false\n",
			want: []csvRow{
				{name: "Andy Weir", monitored: true, searchOnAdd: true},
				{name: "Isaac Asimov", monitored: true, searchOnAdd: false},
			},
		},
		{
			name: "empty input",
			in:   "",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCSVRows(strings.NewReader(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len=%d want %d (%+v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d]: got %+v want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestImportCSVAuthors_NilReader(t *testing.T) {
	database := newTestDB(t)
	repo := db.NewAuthorRepo(database)
	agg := metadata.NewAggregator(&stubProvider{})
	res, err := ImportCSVAuthors(context.Background(), nil, repo, agg, nil)
	if err == nil {
		t.Fatal("expected error for nil reader")
	}
	if res == nil {
		t.Fatal("expected non-nil result even on error")
	}
}

func TestImportCSVAuthors_HappyPath(t *testing.T) {
	database := newTestDB(t)
	repo := db.NewAuthorRepo(database)

	provider := &stubProvider{
		searchAuthorsFn: func(_ context.Context, q string) ([]models.Author, error) {
			return []models.Author{{
				Name:      q,
				SortName:  q,
				ForeignID: "OL-" + q,
			}}, nil
		},
		getAuthorFn: func(_ context.Context, id string) (*models.Author, error) {
			return &models.Author{
				Name:        strings.TrimPrefix(id, "OL-"),
				SortName:    strings.TrimPrefix(id, "OL-"),
				ForeignID:   id,
				Description: "bio for " + id,
			}, nil
		},
	}
	agg := metadata.NewAggregator(provider)

	var wg sync.WaitGroup
	var searchCalls int32
	wg.Add(1) // one row has searchOnAdd=true
	onSearch := func(_ *models.Author) {
		atomic.AddInt32(&searchCalls, 1)
		wg.Done()
	}

	input := "Andy Weir,true,true\nIsaac Asimov,false,false\n"
	res, err := ImportCSVAuthors(context.Background(), strings.NewReader(input), repo, agg, onSearch)
	if err != nil {
		t.Fatalf("ImportCSVAuthors: %v", err)
	}
	if res.Requested != 2 {
		t.Errorf("Requested=%d want 2", res.Requested)
	}
	if res.Added != 2 {
		t.Errorf("Added=%d want 2", res.Added)
	}
	if res.Errors != 0 {
		t.Errorf("Errors=%d want 0 (%v)", res.Errors, res.Failures)
	}
	if len(res.AddedNames) != 2 {
		t.Errorf("AddedNames=%v", res.AddedNames)
	}

	// Wait for the async onSearch callback for Andy Weir.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onSearchOnAdd callback never fired")
	}
	if atomic.LoadInt32(&searchCalls) != 1 {
		t.Errorf("searchOnAdd invoked %d times, want 1", searchCalls)
	}

	// Isaac Asimov was imported with monitored=false.
	got, err := repo.GetByForeignID(context.Background(), "OL-Isaac Asimov")
	if err != nil || got == nil {
		t.Fatalf("expected Isaac Asimov, got err=%v got=%v", err, got)
	}
	if got.Monitored {
		t.Errorf("Isaac Asimov should be monitored=false")
	}
	if got.MetadataProvider != "openlibrary" {
		t.Errorf("MetadataProvider=%q want 'openlibrary'", got.MetadataProvider)
	}
}

func TestImportCSVAuthors_NoMatch(t *testing.T) {
	database := newTestDB(t)
	repo := db.NewAuthorRepo(database)
	agg := metadata.NewAggregator(&stubProvider{
		searchAuthorsFn: func(context.Context, string) ([]models.Author, error) {
			return nil, nil
		},
	})

	res, err := ImportCSVAuthors(context.Background(), strings.NewReader("Nobody\n"), repo, agg, nil)
	if err != nil {
		t.Fatalf("ImportCSVAuthors: %v", err)
	}
	if res.Added != 0 || res.Errors != 1 {
		t.Errorf("Added=%d Errors=%d; want 0/1", res.Added, res.Errors)
	}
	if msg := res.Failures["Nobody"]; !strings.Contains(msg, "no OpenLibrary match") {
		t.Errorf("failure reason = %q", msg)
	}
}

func TestImportCSVAuthors_SearchError(t *testing.T) {
	database := newTestDB(t)
	repo := db.NewAuthorRepo(database)
	agg := metadata.NewAggregator(&stubProvider{
		searchAuthorsFn: func(context.Context, string) ([]models.Author, error) {
			return nil, errors.New("network down")
		},
	})

	res, _ := ImportCSVAuthors(context.Background(), strings.NewReader("Somebody\n"), repo, agg, nil)
	if res.Errors != 1 {
		t.Errorf("Errors=%d want 1", res.Errors)
	}
	if msg := res.Failures["Somebody"]; !strings.Contains(msg, "metadata lookup failed") {
		t.Errorf("failure reason = %q", msg)
	}
}

func TestImportCSVAuthors_SkipDuplicate(t *testing.T) {
	database := newTestDB(t)
	repo := db.NewAuthorRepo(database)
	ctx := context.Background()

	// Pre-seed an author so the CSV import finds a duplicate.
	existing := &models.Author{
		ForeignID: "OL-Dup Author", Name: "Dup Author", SortName: "Dup Author",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := repo.Create(ctx, existing); err != nil {
		t.Fatalf("seed author: %v", err)
	}

	agg := metadata.NewAggregator(&stubProvider{
		searchAuthorsFn: func(_ context.Context, q string) ([]models.Author, error) {
			return []models.Author{{Name: q, SortName: q, ForeignID: "OL-" + q}}, nil
		},
	})
	res, err := ImportCSVAuthors(ctx, strings.NewReader("Dup Author\n"), repo, agg, nil)
	if err != nil {
		t.Fatalf("ImportCSVAuthors: %v", err)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped=%d want 1", res.Skipped)
	}
	if res.Added != 0 {
		t.Errorf("Added=%d want 0", res.Added)
	}
}

func TestImportCSVAuthors_GetAuthorFallback(t *testing.T) {
	// When GetAuthor errors, the top search match should still be used.
	database := newTestDB(t)
	repo := db.NewAuthorRepo(database)
	agg := metadata.NewAggregator(&stubProvider{
		searchAuthorsFn: func(_ context.Context, q string) ([]models.Author, error) {
			return []models.Author{{Name: q, SortName: q, ForeignID: "OL-" + q}}, nil
		},
		getAuthorFn: func(context.Context, string) (*models.Author, error) {
			return nil, errors.New("details fetch failed")
		},
	})
	res, err := ImportCSVAuthors(context.Background(), strings.NewReader("Fallback Person\n"), repo, agg, nil)
	if err != nil {
		t.Fatalf("ImportCSVAuthors: %v", err)
	}
	if res.Added != 1 {
		t.Fatalf("Added=%d want 1 (failures: %v)", res.Added, res.Failures)
	}
}
