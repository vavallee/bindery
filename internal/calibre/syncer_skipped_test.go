package calibre

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type fakeSeriesGetter struct {
	series map[int64][2]string
}

func (f fakeSeriesGetter) GetPrimarySeriesForBook(_ context.Context, bookID int64) (string, string, error) {
	v := f.series[bookID]
	return v[0], v[1], nil
}

// TestSyncer_SkippedBooksAreCountedAndExplained is discussion #1592. A book
// that the bulk push silently dropped appeared in no counter, no error row and
// no log line, so the modal's honest state was indistinguishable from
// "nothing to do".
func TestSyncer_SkippedBooksAreCountedAndExplained(t *testing.T) {
	books := &fakeBookLister{
		all: []models.Book{
			{ID: 1, Title: "Pushable", Status: models.BookStatusImported, EbookFilePath: "/l/a.epub"},
			{ID: 2, Title: "Wanted", Status: models.BookStatusWanted},
			{ID: 3, Title: "Audio Only", Status: models.BookStatusImported, AudiobookFilePath: "/l/b/"},
			{ID: 4, Title: "Ghost", Status: models.BookStatusImported},
			{ID: 5, Title: "Unmonitored", Status: models.BookStatusImported, EbookFilePath: "/l/e.epub"},
		},
		books: []models.Book{
			{ID: 1, Title: "Pushable", Status: models.BookStatusImported, EbookFilePath: "/l/a.epub"},
			{ID: 3, Title: "Audio Only", Status: models.BookStatusImported, AudiobookFilePath: "/l/b/"},
			{ID: 4, Title: "Ghost", Status: models.BookStatusImported},
		},
	}
	pusher := &fakePusher{calls: map[string]func() (int64, error){
		"/l/a.epub": func() (int64, error) { return 1, nil },
	}}

	s := NewSyncer(books)
	s.newClient = func(_ Config) pluginPusher { return pusher }
	if err := s.Start(context.Background(), Config{}, ModePlugin); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return !s.Running() })

	p := s.Progress()
	if p.Stats.Total != 1 {
		t.Errorf("Total = %d, want 1", p.Stats.Total)
	}
	if p.Stats.Skipped != 3 {
		t.Errorf("Skipped = %d, want 3", p.Stats.Skipped)
	}
	got := map[int64]string{}
	for _, s := range p.Skips {
		got[s.BookID] = s.Reason
	}
	if reason, ok := got[2]; ok {
		t.Errorf("wanted book listed as skipped with %q; a book with no file was never a candidate", reason)
	}
	want := map[int64]string{
		3: SkipReasonAudiobookOnly,
		4: SkipReasonNoFile,
		5: SkipReasonNotMonitored,
	}
	for id, reason := range want {
		if got[id] != reason {
			t.Errorf("book %d skip reason = %q, want %q", id, got[id], reason)
		}
	}
}

// TestSyncer_ZeroCaseNamesTheReason replaces the generic "no imported books
// with files to push" with something the reporter in #1592 could have acted on.
func TestSyncer_ZeroCaseNamesTheReason(t *testing.T) {
	books := &fakeBookLister{
		all: []models.Book{
			{ID: 1, Title: "Wanted", Status: models.BookStatusWanted},
			{ID: 2, Title: "Wanted Too", Status: models.BookStatusWanted},
			{ID: 3, Title: "Audio Only", Status: models.BookStatusImported, AudiobookFilePath: "/l/b/"},
		},
		books: []models.Book{
			{ID: 3, Title: "Audio Only", Status: models.BookStatusImported, AudiobookFilePath: "/l/b/"},
		},
	}
	s := NewSyncer(books)
	s.newClient = func(_ Config) pluginPusher { return &fakePusher{} }
	if err := s.Start(context.Background(), Config{}, ModePlugin); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return !s.Running() })

	p := s.Progress()
	if p.Stats.Total != 0 {
		t.Fatalf("Total = %d, want 0", p.Stats.Total)
	}
	if !strings.Contains(p.Message, "1 "+SkipReasonAudiobookOnly) {
		t.Errorf("message = %q, want it to name the skip reason with its count", p.Message)
	}
	if strings.Contains(p.Message, "not imported") {
		t.Errorf("message = %q, wanted books have no file and are not a skip reason", p.Message)
	}
}

// TestSyncer_WantedBooksDoNotCrowdOutActionableSkips: the skip list is a
// 50 row sample, and a large wanted list used to fill it with "not imported"
// rows before any unmonitored or missing file book was reached, so the
// reasons a user could act on never showed.
func TestSyncer_WantedBooksDoNotCrowdOutActionableSkips(t *testing.T) {
	books := &fakeBookLister{}
	for i := int64(1); i <= 3*maxSyncErrors; i++ {
		books.all = append(books.all, models.Book{ID: i, Title: "Wanted", Status: models.BookStatusWanted})
	}
	books.all = append(books.all, models.Book{ID: 999, Title: "Unmonitored", Status: models.BookStatusImported, EbookFilePath: "/l/u.epub"})
	s := NewSyncer(books)
	s.newClient = func(_ Config) pluginPusher { return &fakePusher{} }
	if err := s.Start(context.Background(), Config{}, ModePlugin); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return !s.Running() })

	p := s.Progress()
	if p.Stats.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (only the unmonitored book)", p.Stats.Skipped)
	}
	if len(p.Skips) != 1 || p.Skips[0].BookID != 999 || p.Skips[0].Reason != SkipReasonNotMonitored {
		t.Errorf("skips = %+v, want only book 999 as %q", p.Skips, SkipReasonNotMonitored)
	}
}

// TestSyncer_BulkPushSendsTheSameMetadataAsAnImport is review item 3. The bulk
// job built six fields where a live import built fifteen, so every book pushed
// this way landed in Calibre with no series, description, publisher, published
// date or rating.
func TestSyncer_BulkPushSendsTheSameMetadataAsAnImport(t *testing.T) {
	published := dateOf("1965-08-01")
	books := &fakeBookLister{
		books: []models.Book{{
			ID:            1,
			Title:         "Dune",
			Description:   "Desert planet.",
			Genres:        []string{"Science Fiction"},
			Language:      "eng",
			ReleaseDate:   published,
			AverageRating: 4.5,
			Status:        models.BookStatusImported,
			EbookFilePath: "/l/dune.epub",
			Author:        &models.Author{Name: "Frank Herbert", SortName: "Herbert, Frank"},
		}},
	}
	pusher := &fakePusher{calls: map[string]func() (int64, error){
		"/l/dune.epub": func() (int64, error) { return 1, nil },
	}}

	s := NewSyncer(books).
		WithMetadata(fakeAuthorGetter{}, fakeEditionLister{editions: map[int64][]models.Edition{
			1: {{Format: "EPUB", Publisher: "Ace"}},
		}}).
		WithSeries(fakeSeriesGetter{series: map[int64][2]string{1: {"Dune Chronicles", "1"}}})
	s.newClient = func(_ Config) pluginPusher { return pusher }
	if err := s.Start(context.Background(), Config{}, ModePlugin); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return !s.Running() })

	meta := pusher.meta("/l/dune.epub")
	if meta.Series != "Dune Chronicles" || meta.SeriesIndex != "1" {
		t.Errorf("series = %q/%q, want the book's series", meta.Series, meta.SeriesIndex)
	}
	if meta.Description != "Desert planet." {
		t.Errorf("description = %q", meta.Description)
	}
	if meta.Publisher != "Ace" {
		t.Errorf("publisher = %q", meta.Publisher)
	}
	if meta.PublishedDate != "1965-08-01" {
		t.Errorf("publishedDate = %q", meta.PublishedDate)
	}
	if meta.Rating != 4.5 {
		t.Errorf("rating = %v", meta.Rating)
	}
	if meta.AuthorSort != "Herbert, Frank" {
		t.Errorf("authorSort = %q", meta.AuthorSort)
	}
}
