package hardcover

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
)

// fakeEditionBook is one work on the fake Hardcover: its slug, database id and
// how many editions it has.
type fakeEditionBook struct {
	slug     string
	id       int
	editions int
}

// fakeEditionsServer answers the three edition queries (per slug, per book id,
// batched by slugs) from a fixed catalogue, honouring order_by, limit and
// offset, and records every query it was sent.
type fakeEditionsServer struct {
	t       *testing.T
	books   []fakeEditionBook
	failFor func(slugs []string) bool // fail a batched query naming these slugs

	mu      sync.Mutex
	queries []string
	vars    []map[string]any
}

func (f *fakeEditionsServer) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries)
}

func (f *fakeEditionsServer) count(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, q := range f.queries {
		if strings.Contains(q, substr) {
			n++
		}
	}
	return n
}

// row is one edition row ordered by (book_id, id). Edition ids are unique
// across the catalogue and ascend with the book id.
type fakeEditionRow struct {
	book fakeEditionBook
	id   int
}

func (f *fakeEditionsServer) rows(match func(fakeEditionBook) bool) []fakeEditionRow {
	books := slices.Clone(f.books)
	slices.SortFunc(books, func(a, b fakeEditionBook) int { return a.id - b.id })
	var out []fakeEditionRow
	for _, b := range books {
		if !match(b) {
			continue
		}
		for i := range b.editions {
			out = append(out, fakeEditionRow{book: b, id: b.id*10000 + i + 1})
		}
	}
	return out
}

func (f *fakeEditionsServer) handle(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	var req gqlRequest
	if err := json.Unmarshal(body, &req); err != nil {
		f.t.Errorf("decode request: %v", err)
	}
	f.mu.Lock()
	f.queries = append(f.queries, req.Query)
	f.vars = append(f.vars, req.Variables)
	f.mu.Unlock()

	var match func(fakeEditionBook) bool
	switch {
	case strings.Contains(req.Query, "$slugs"):
		var slugs []string
		for _, s := range req.Variables["slugs"].([]any) {
			slugs = append(slugs, s.(string))
		}
		if f.failFor != nil && f.failFor(slugs) {
			return gqlResponse(f.t, http.StatusBadRequest, `{"errors":[{"message":"boom"}]}`), nil
		}
		match = func(b fakeEditionBook) bool { return slices.Contains(slugs, b.slug) }
	case strings.Contains(req.Query, "$slug:"):
		slug := req.Variables["slug"].(string)
		match = func(b fakeEditionBook) bool { return b.slug == slug }
	case strings.Contains(req.Query, "$bookID"):
		id := int(req.Variables["bookID"].(float64))
		match = func(b fakeEditionBook) bool { return b.id == id }
	default:
		f.t.Fatalf("unexpected query: %s", req.Query)
	}
	rows := f.rows(match)
	offset, limit := int(req.Variables["offset"].(float64)), int(req.Variables["limit"].(float64))
	rows = rows[min(offset, len(rows)):]
	rows = rows[:min(limit, len(rows))]
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"id":    row.id,
			"title": fmt.Sprintf("%s edition %d", row.book.slug, row.id),
			"book":  map[string]any{"title": row.book.slug, "slug": row.book.slug},
		})
	}
	return gqlResponse(f.t, http.StatusOK, map[string]any{"editions": out}), nil
}

func fakeCatalogue(n, editions int) []fakeEditionBook {
	books := make([]fakeEditionBook, 0, n)
	for i := 1; i <= n; i++ {
		books = append(books, fakeEditionBook{slug: fmt.Sprintf("book-%02d", i), id: i, editions: editions})
	}
	return books
}

func hcIDs(books []fakeEditionBook) []string {
	ids := make([]string, 0, len(books))
	for _, b := range books {
		ids = append(ids, idPrefix+b.slug)
	}
	return ids
}

func TestGetEditionsBatchCutsRequestCount(t *testing.T) {
	books := fakeCatalogue(25, 4)

	perBook := &fakeEditionsServer{t: t, books: books}
	c := newMockClient(perBook.handle)
	for _, id := range hcIDs(books) {
		if _, err := c.GetEditions(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}

	batch := &fakeEditionsServer{t: t, books: books}
	c = newMockClient(batch.handle)
	got, err := c.GetEditionsBatch(context.Background(), hcIDs(books))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("25 books: %d requests per book, %d requests batched", perBook.requests(), batch.requests())
	if perBook.requests() != 25 {
		t.Fatalf("per book baseline = %d requests, want 25", perBook.requests())
	}
	if batch.requests() != 3 {
		t.Fatalf("batched = %d requests, want 3 (chunks of %d)", batch.requests(), editionsBatchChunk)
	}
	for _, b := range books {
		if n := len(got[idPrefix+b.slug]); n != b.editions {
			t.Errorf("%s: %d editions, want %d", b.slug, n, b.editions)
		}
	}
}

func TestGetEditionsBatchKeepsAWorkWholeAcrossAPageBoundary(t *testing.T) {
	// 60 + 60 + 150 + 5 rows in book id order: the second work straddles the
	// first page boundary (rows 61 to 120) and the third spans two more.
	books := []fakeEditionBook{
		{slug: "a", id: 1, editions: 60},
		{slug: "b", id: 2, editions: 60},
		{slug: "c", id: 3, editions: 150},
		{slug: "d", id: 4, editions: 5},
	}
	f := &fakeEditionsServer{t: t, books: books}
	c := newMockClient(f.handle)
	got, err := c.GetEditionsBatch(context.Background(), []string{"hc:a", "hc:b", "hc:c", "hc:d"})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range books {
		editions := got["hc:"+b.slug]
		if len(editions) != b.editions {
			t.Fatalf("%s: %d editions, want %d", b.slug, len(editions), b.editions)
		}
		seen := map[string]bool{}
		for _, e := range editions {
			if seen[e.ForeignID] {
				t.Fatalf("%s: edition %s twice", b.slug, e.ForeignID)
			}
			seen[e.ForeignID] = true
		}
	}
	if f.requests() != 3 { // 275 rows at 100 a page, the last page short
		t.Fatalf("requests = %d, want 3", f.requests())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(f.queries[0], "order_by: [{book_id: asc}, {id: asc}]") {
		t.Fatalf("batched query is not ordered by book_id then id: %s", f.queries[0])
	}
}

func TestGetEditionsBatchFailedChunkFallsBackPerBook(t *testing.T) {
	books := fakeCatalogue(25, 3)
	f := &fakeEditionsServer{t: t, books: books, failFor: func(slugs []string) bool {
		return slices.Contains(slugs, "book-12") // the second chunk
	}}
	c := newMockClient(f.handle)
	got, err := c.GetEditionsBatch(context.Background(), hcIDs(books))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range books {
		if n := len(got[idPrefix+b.slug]); n != b.editions {
			t.Errorf("%s: %d editions, want %d", b.slug, n, b.editions)
		}
	}
	// 3 batched queries (one failed) plus one per work of the failed chunk.
	if f.requests() != 3+editionsBatchChunk {
		t.Fatalf("requests = %d, want %d", f.requests(), 3+editionsBatchChunk)
	}
	if f.count("$slugs") != 3 || f.count("$slug:") != editionsBatchChunk {
		t.Fatalf("batched %d, per work %d", f.count("$slugs"), f.count("$slug:"))
	}
}

func TestGetEditionsBatchLeavesAFailedWorkUnanswered(t *testing.T) {
	// The batch and the per work retry both fail: the works are left out of
	// the result, not reported as having no editions.
	books := fakeCatalogue(3, 2)
	c := newMockClient(func(*http.Request) (*http.Response, error) {
		return gqlResponse(t, http.StatusBadRequest, `{"errors":[{"message":"boom"}]}`), nil
	})
	got, err := c.GetEditionsBatch(context.Background(), hcIDs(books))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("every query failed, got answers for %d works", len(got))
	}
}

func TestGetEditionsBatchNeverJoinsSlugAndID(t *testing.T) {
	// "1984" is a database id, not a slug: the slug query finds nothing and the
	// id lookup, its own query, finds the editions.
	books := []fakeEditionBook{
		{slug: "dune", id: 7, editions: 2},
		{slug: "not-numeric", id: 8, editions: 0},
		{slug: "id-only", id: 1984, editions: 3},
	}
	f := &fakeEditionsServer{t: t, books: books}
	c := newMockClient(f.handle)
	got, err := c.GetEditionsBatch(context.Background(), []string{"hc:dune", "hc:not-numeric", "hc:1984"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["hc:dune"]) != 2 || len(got["hc:not-numeric"]) != 0 || len(got["hc:1984"]) != 3 {
		t.Fatalf("got %d, %d, %d editions", len(got["hc:dune"]), len(got["hc:not-numeric"]), len(got["hc:1984"]))
	}
	if _, ok := got["hc:not-numeric"]; !ok {
		t.Fatal("a work answered with no editions must be present in the result")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var idQueries int
	for _, q := range f.queries {
		if strings.Contains(q, "_or") {
			t.Errorf("query uses _or: %s", q)
		}
		slugPredicate, idPredicate := strings.Contains(q, "slug: {_"), strings.Contains(q, "book_id: {_")
		if slugPredicate && idPredicate {
			t.Errorf("slug and book_id in one query: %s", q)
		}
		if idPredicate {
			idQueries++
		}
	}
	if idQueries != 1 {
		t.Fatalf("book_id queries = %d, want 1 (only the work the slug query missed)", idQueries)
	}
}

func TestAggregatorEditionsBatchSharesTheEditionCache(t *testing.T) {
	books := fakeCatalogue(12, 2)
	f := &fakeEditionsServer{t: t, books: books}
	a := metadata.NewAggregator(newMockClient(f.handle))
	ctx := context.Background()

	got := a.GetEditionsBatchFromProvider(ctx, "hardcover", hcIDs(books))
	if len(got) != 12 || f.requests() != 2 {
		t.Fatalf("answers = %d, requests = %d, want 12 and 2", len(got), f.requests())
	}
	// A batched work is cached for the per book path and for a second batch.
	if _, err := a.GetEditionsFromProvider(ctx, "hardcover", "hc:book-03"); err != nil {
		t.Fatal(err)
	}
	a.GetEditionsBatchFromProvider(ctx, "hardcover", hcIDs(books))
	if f.requests() != 2 {
		t.Fatalf("requests = %d after cache hits, want 2", f.requests())
	}
	// A provider that cannot batch answers nothing, so callers go per work.
	if got := a.GetEditionsBatchFromProvider(ctx, "openlibrary", []string{"x"}); len(got) != 0 {
		t.Fatalf("unknown provider answered %d works", len(got))
	}
}
