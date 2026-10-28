package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
)

func wantedGet(t *testing.T, h *BookHandler, query string) (*httptest.ResponseRecorder, wantedPageBody) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ListWanted(rec, httptest.NewRequest(http.MethodGet, "/api/v1/wanted/missing"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", query, rec.Code, rec.Body.String())
	}
	var body wantedPageBody
	if bytes.HasPrefix(bytes.TrimSpace(rec.Body.Bytes()), []byte("{")) {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
	}
	return rec, body
}

type wantedPageBody struct {
	Items  []models.Book `json:"items"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

func wantedTitles(items []models.Book) []string {
	out := make([]string, len(items))
	for i, b := range items {
		out[i] = b.Title
	}
	return out
}

// TestListWanted_NoLimitKeepsTheBareArray: Arr-compatible clients read a bare
// array and must keep getting it.
func TestListWanted_NoLimitKeepsTheBareArray(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	for _, title := range []string{"A", "B", "C"} {
		if err := books.Create(ctx, &models.Book{ForeignID: "F" + title, AuthorID: author.ID, Title: title, SortTitle: title,
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	h.ListWanted(rec, httptest.NewRequest(http.MethodGet, "/api/v1/wanted/missing?includeExcluded=false", nil))
	var arr []models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &arr); err != nil || len(arr) != 3 {
		t.Fatalf("without limit the body must be a bare array of every wanted book: err %v len %d body %s", err, len(arr), rec.Body.String())
	}
}

// TestListWanted_LimitReturnsTheEnvelope pins the paged shape and what total
// means: every wanted book the caller can see that matches the filters,
// whatever the page.
func TestListWanted_LimitReturnsTheEnvelope(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	mk := func(id, title string, mut func(*models.Book)) {
		b := &models.Book{ForeignID: id, AuthorID: author.ID, Title: title, SortTitle: title,
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
		if mut != nil {
			mut(b)
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if b.Excluded {
			if err := books.SetExcluded(ctx, b.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, title := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo"} {
		mk("F"+title, title, nil)
	}
	mk("Fimp", "Imported", func(b *models.Book) { b.Status = "imported" })
	mk("Funmon", "Unmonitored", func(b *models.Book) { b.Monitored = false })
	mk("Fexc", "Excluded Wanted", func(b *models.Book) { b.Excluded = true })

	_, page1 := wantedGet(t, h, "?limit=2")
	if want := []string{"Alpha", "Bravo"}; !slices.Equal(wantedTitles(page1.Items), want) {
		t.Errorf("page 1 = %v, want %v", wantedTitles(page1.Items), want)
	}
	if page1.Total != 5 || page1.Limit != 2 || page1.Offset != 0 {
		t.Errorf("page 1 envelope total/limit/offset = %d/%d/%d, want 5/2/0 (imported, unmonitored and excluded rows are not wanted)", page1.Total, page1.Limit, page1.Offset)
	}

	_, page3 := wantedGet(t, h, "?limit=2&offset=4")
	if want := []string{"Echo"}; !slices.Equal(wantedTitles(page3.Items), want) || page3.Total != 5 || page3.Offset != 4 {
		t.Errorf("last page = %v total %d offset %d, want [Echo] total 5 offset 4", wantedTitles(page3.Items), page3.Total, page3.Offset)
	}

	rec, past := wantedGet(t, h, "?limit=2&offset=50")
	if past.Total != 5 || len(past.Items) != 0 || !bytes.Contains(rec.Body.Bytes(), []byte(`"items":[]`)) {
		t.Errorf("past the end: total %d items %d body %s; want total 5 and an empty array, not null", past.Total, len(past.Items), rec.Body.String())
	}

	// includeExcluded widens the set and the total with it.
	_, withExcluded := wantedGet(t, h, "?limit=50&includeExcluded=true")
	if withExcluded.Total != 6 || !slices.Contains(wantedTitles(withExcluded.Items), "Excluded Wanted") {
		t.Errorf("includeExcluded: total %d titles %v, want 6 including the excluded one", withExcluded.Total, wantedTitles(withExcluded.Items))
	}

	// search narrows items and total together.
	_, found := wantedGet(t, h, "?limit=50&search=charl")
	if found.Total != 1 || !slices.Equal(wantedTitles(found.Items), []string{"Charlie"}) {
		t.Errorf("search: total %d titles %v, want [Charlie]", found.Total, wantedTitles(found.Items))
	}
	_, folded := wantedGet(t, h, "?limit=50&search=CHARLIE")
	if folded.Total != 1 {
		t.Errorf("search is case insensitive: total %d, want 1", folded.Total)
	}

	// The limit is capped like the Books list so one request cannot ask for everything.
	_, capped := wantedGet(t, h, "?limit=100000")
	if capped.Limit != bookListMaxLimit {
		t.Errorf("limit 100000 answered limit %d, want the cap %d", capped.Limit, bookListMaxLimit)
	}
}

// TestListWanted_PagedTakesSortAndMediaType: the Wanted page's filter bar
// sends the Books list's own sort and mediaType parameters.
func TestListWanted_PagedTakesSortAndMediaType(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	for _, b := range []struct {
		title, media string
		year         int
	}{
		{"Alpha", models.MediaTypeEbook, 2001},
		{"Bravo", models.MediaTypeAudiobook, 2003},
		{"Charlie", models.MediaTypeEbook, 2002},
	} {
		released := time.Date(b.year, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := books.Create(ctx, &models.Book{ForeignID: "F" + b.title, AuthorID: author.ID, Title: b.title, SortTitle: b.title,
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
			MediaType: b.media, ReleaseDate: &released}); err != nil {
			t.Fatal(err)
		}
	}
	_, byDate := wantedGet(t, h, "?limit=50&sort=date-new")
	if want := []string{"Bravo", "Charlie", "Alpha"}; !slices.Equal(wantedTitles(byDate.Items), want) {
		t.Errorf("sort=date-new = %v, want %v", wantedTitles(byDate.Items), want)
	}
	_, audio := wantedGet(t, h, "?limit=50&mediaType=audiobook")
	if audio.Total != 1 || !slices.Equal(wantedTitles(audio.Items), []string{"Bravo"}) {
		t.Errorf("mediaType=audiobook: total %d titles %v, want [Bravo]", audio.Total, wantedTitles(audio.Items))
	}
	_, unknown := wantedGet(t, h, "?limit=50&sort=drop-table")
	if want := []string{"Alpha", "Bravo", "Charlie"}; !slices.Equal(wantedTitles(unknown.Items), want) {
		t.Errorf("an unknown sort falls back to title order: %v, want %v", wantedTitles(unknown.Items), want)
	}
}

// TestListWanted_PagedFlagsUnmonitoredAuthors: the per row hint the page shows
// must survive the move to the paged form.
func TestListWanted_PagedFlagsUnmonitoredAuthors(t *testing.T) {
	h, books, authors, _, ctx := bookFixture(t)
	h = h.WithAuthors(authors)
	quiet := &models.Author{ForeignID: "OLQ", Name: "Quiet Author", SortName: "Author, Quiet", MetadataProvider: "openlibrary", Monitored: false}
	if err := authors.Create(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if err := books.Create(ctx, &models.Book{ForeignID: "WQ", AuthorID: quiet.ID, Title: "Quiet Book", SortTitle: "quiet book",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}); err != nil {
		t.Fatal(err)
	}
	_, page := wantedGet(t, h, "?limit=10")
	if len(page.Items) != 1 || !page.Items[0].AuthorUnmonitored {
		t.Fatalf("paged wanted row lost its authorUnmonitored flag: %+v", page.Items)
	}
}

// TestListScope_WantedPaged_NonAdminIsolated: the paged form keeps the tenancy
// scope, in the rows and in the total.
func TestListScope_WantedPaged_NonAdminIsolated(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newListScopeFixture(t)
	ctx := auth.WithUserRole(auth.WithUserID(context.Background(), f.bob), "user")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/wanted/missing?limit=50", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	f.booksH.ListWanted(rec, req)
	var body wantedPageBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	got := wantedTitles(body.Items)
	slices.Sort(got)
	if !slices.Equal(got, []string{"Bob Book", "Global Book"}) || body.Total != 2 {
		t.Errorf("non-admin bob saw %v total %d, want his own and the unowned book only, total 2", got, body.Total)
	}
}

// TestListWanted_PagesAreStableWhenTitlesTie: books that share a title and sort
// key (two authors' "Collected Stories", a series of "Untitled") must land on
// exactly one page each, in a fixed order. Without a unique tiebreak the order
// of tied rows is whatever the sorter happens to produce for that LIMIT and
// OFFSET, so a row can show on two pages and another on none.
func TestListWanted_PagesAreStableWhenTitlesTie(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	const n = 60
	for i := 0; i < n; i++ {
		if err := books.Create(ctx, &models.Book{ForeignID: "T" + string(rune('A'+i/26)) + string(rune('a'+i%26)), AuthorID: author.ID,
			Title: "Collected Stories", SortTitle: "Collected Stories", Status: models.BookStatusWanted,
			Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}); err != nil {
			t.Fatal(err)
		}
	}
	for _, size := range []int{1, 7, 13} {
		seen := map[int64]bool{}
		var order []int64
		for off := 0; ; off += size {
			_, page := wantedGet(t, h, "?limit="+itoa(size)+"&offset="+itoa(off))
			if len(page.Items) == 0 {
				break
			}
			for _, b := range page.Items {
				if seen[b.ID] {
					t.Fatalf("limit %d: book %d came back on two pages", size, b.ID)
				}
				seen[b.ID] = true
				order = append(order, b.ID)
			}
		}
		if len(seen) != n {
			t.Errorf("limit %d: pages covered %d of %d books", size, len(seen), n)
		}
		if !slices.IsSorted(order) {
			t.Errorf("limit %d: tied books are not in id order: %v", size, order)
		}
	}
}
