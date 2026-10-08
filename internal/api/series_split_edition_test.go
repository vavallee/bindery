package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// splitEditionLibrary is the state an install that pressed Fill on
// Stormlight before #2524 is in: the whole "The Way of Kings" imported at 1,
// its two split parts created by that fill at 1.1 and 1.2, a real novella at
// 2.5, and Oathbringer's parts with no whole Oathbringer row at all.
type splitEditionLibrary struct {
	series                                    *models.Series
	whole, part1, part2, radiance, edgedancer *models.Book
	lonePart                                  *models.Book
}

func seedSplitEditionLibrary(t *testing.T, seriesRepo *db.SeriesRepo, authorRepo *db.AuthorRepo, bookRepo *db.BookRepo) splitEditionLibrary {
	t.Helper()
	ctx := context.Background()
	author := &models.Author{ForeignID: "hc:brandon-sanderson", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", MetadataProvider: "hardcover"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	series := &models.Series{ForeignID: "hc-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	released := time.Date(2010, 8, 31, 0, 0, 0, 0, time.UTC)
	add := func(title, position, status string, monitored bool) *models.Book {
		t.Helper()
		b := &models.Book{
			ForeignID: "hc:" + title, AuthorID: author.ID, Title: title, SortTitle: title,
			Status: status, Monitored: monitored, MediaType: models.MediaTypeEbook,
			Genres: []string{}, MetadataProvider: "hardcover", ReleaseDate: &released,
		}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if err := seriesRepo.LinkBook(ctx, series.ID, b.ID, position, true); err != nil {
			t.Fatal(err)
		}
		return b
	}
	return splitEditionLibrary{
		series:     series,
		whole:      add("The Way of Kings", "1", models.BookStatusImported, true),
		part1:      add("The Way of Kings, Part 1", "1.1", models.BookStatusWanted, true),
		part2:      add("The Way of Kings, Part 2", "1.2", models.BookStatusWanted, false),
		radiance:   add("Words of Radiance", "2", models.BookStatusImported, true),
		edgedancer: add("Edgedancer", "2.5", models.BookStatusWanted, false),
		lonePart:   add("Oathbringer, Part 1", "3.1", models.BookStatusWanted, false),
	}
}

// TestSeriesFillDoesNotRequeueSplitEditionPartsOfAnOwnedBook is the #3048
// regression test. Fill re-queued every series book that was not imported,
// so the split parts a pre #2524 fill created went back to Wanted and
// monitored on every later fill, and were searched straight away. A real
// novella and a part with no whole beside it are still volumes and must still
// be queued.
func TestSeriesFillDoesNotRequeueSplitEditionPartsOfAnOwnedBook(t *testing.T) {
	h, seriesRepo, authorRepo, bookRepo := seriesFixture(t)
	lib := seedSplitEditionLibrary(t, seriesRepo, authorRepo, bookRepo)
	ctx := context.Background()

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", strconv.FormatInt(lib.series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["queued"] != 2 {
		t.Errorf("queued = %d, want 2 (Edgedancer and the lone Oathbringer part only): %+v", body["queued"], body)
	}

	part2, err := bookRepo.GetByID(ctx, lib.part2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if part2.Monitored {
		t.Error("fill re-monitored a split part of a book already imported")
	}
	for _, b := range []*models.Book{lib.edgedancer, lib.lonePart} {
		got, err := bookRepo.GetByID(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Monitored || got.Status != models.BookStatusWanted {
			t.Errorf("%q was not queued: monitored=%v status=%s", b.Title, got.Monitored, got.Status)
		}
	}
}

// TestSeriesUnmonitorSplitEditionParts covers the one click cleanup (#3048):
// it unmonitors the parts that are monitored, deletes nothing, and leaves the
// whole, the novella and the lone part alone.
func TestSeriesUnmonitorSplitEditionParts(t *testing.T) {
	h, seriesRepo, authorRepo, bookRepo := seriesFixture(t)
	lib := seedSplitEditionLibrary(t, seriesRepo, authorRepo, bookRepo)
	ctx := context.Background()
	if err := bookRepo.MarkWantedMonitored(ctx, lib.edgedancer.ID); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.MarkWantedMonitored(ctx, lib.lonePart.ID); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.UnmonitorSplitEditionParts(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/split-parts/unmonitor", nil), "id", strconv.FormatInt(lib.series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["unmonitored"] != 1 {
		t.Errorf("unmonitored = %d, want 1 (part 1; part 2 already was)", body["unmonitored"])
	}
	want := map[int64]bool{
		lib.whole.ID: true, lib.part1.ID: false, lib.part2.ID: false,
		lib.radiance.ID: true, lib.edgedancer.ID: true, lib.lonePart.ID: true,
	}
	for id, monitored := range want {
		got, err := bookRepo.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatalf("book %d was deleted", id)
		}
		if got.Monitored != monitored {
			t.Errorf("%q monitored = %v, want %v", got.Title, got.Monitored, monitored)
		}
	}

	rec = httptest.NewRecorder()
	h.UnmonitorSplitEditionParts(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/999/split-parts/unmonitor", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown series: status = %d, want 404", rec.Code)
	}
}

// TestSeriesGetMarksSplitEditionParts checks the series payload names the
// part rows, which is what the series page reads to badge them, leave them
// out of the missing count and offer the cleanup.
func TestSeriesGetMarksSplitEditionParts(t *testing.T) {
	h, seriesRepo, authorRepo, bookRepo := seriesFixture(t)
	lib := seedSplitEditionLibrary(t, seriesRepo, authorRepo, bookRepo)

	rec := httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/series/1", nil), "id", strconv.FormatInt(lib.series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Series
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !sameIDs(got.SplitEditionPartBookIDs, []int64{lib.part1.ID, lib.part2.ID}) {
		t.Errorf("Get splitEditionPartBookIds = %v, want parts %d and %d", got.SplitEditionPartBookIDs, lib.part1.ID, lib.part2.ID)
	}

	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series", nil))
	var list []models.Series
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !sameIDs(list[0].SplitEditionPartBookIDs, []int64{lib.part1.ID, lib.part2.ID}) {
		t.Errorf("List splitEditionPartBookIds = %+v", list)
	}
}

func sameIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[int64]int, len(want))
	for _, id := range want {
		seen[id]++
	}
	for _, id := range got {
		if seen[id] == 0 {
			return false
		}
		seen[id]--
	}
	return true
}
