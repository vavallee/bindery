package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type dupCandBook struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	Excluded bool     `json:"excluded"`
	Rules    []string `json:"rules"`
}

type dupCandGroup struct {
	Key   string        `json:"key"`
	Rules []string      `json:"rules"`
	Books []dupCandBook `json:"books"`
}

type dupCandResponse struct {
	AuthorID int64          `json:"authorId"`
	Groups   []dupCandGroup `json:"groups"`
	Count    int            `json:"count"`
}

type dupCandFixture struct {
	ctx      context.Context
	database *sql.DB
	authors  *db.AuthorRepo
	books    *db.BookRepo
	series   *db.SeriesRepo
	handler  *AuthorHandler
}

func newDupCandFixture(t *testing.T) (*dupCandFixture, *models.Author) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	series := db.NewSeriesRepo(database)
	f := &dupCandFixture{
		ctx:      ctx,
		database: database,
		authors:  authors,
		books:    books,
		series:   series,
		handler:  NewAuthorHandler(authors, nil, books, series, nil, nil, nil, nil),
	}
	author := &models.Author{Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	return f, author
}

// addBook seeds a book row under the author and returns it with its ID.
func (f *dupCandFixture) addBook(t *testing.T, authorID int64, title, foreignID string, excluded bool) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID:        foreignID,
		AuthorID:         authorID,
		Title:            title,
		SortTitle:        title,
		Monitored:        true,
		Status:           models.BookStatusWanted,
		MetadataProvider: "openlibrary",
		MediaType:        models.MediaTypeEbook,
		Excluded:         excluded,
	}
	if err := f.books.Create(f.ctx, b); err != nil {
		t.Fatalf("seed book %q: %v", title, err)
	}
	if excluded {
		if err := f.books.SetExcluded(f.ctx, b.ID, true); err != nil {
			t.Fatalf("exclude book %q: %v", title, err)
		}
	}
	return b
}

// linkSeries creates (or reuses, by foreignID) a series and links book at the
// given position — the real DB path the review asked DuplicateCandidates to
// use so the substring rule can tell a series' own sequels apart.
func (f *dupCandFixture) linkSeries(t *testing.T, foreignID, title string, bookID int64, position string) {
	t.Helper()
	s := &models.Series{ForeignID: foreignID, Title: title}
	if err := f.series.CreateOrGet(f.ctx, s); err != nil {
		t.Fatalf("create series %q: %v", title, err)
	}
	if err := f.series.LinkBook(f.ctx, s.ID, bookID, position, false); err != nil {
		t.Fatalf("link book %d to series %q at %q: %v", bookID, title, position, err)
	}
}

func (f *dupCandFixture) get(t *testing.T, authorID int64, userID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/author/"+strconv.FormatInt(authorID, 10)+"/duplicate-candidates", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(authorID, 10))
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	if userID != 0 {
		ctx = auth.WithUserID(ctx, userID)
	}
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	f.handler.DuplicateCandidates(rec, req)
	return rec
}

func parseDupCand(t *testing.T, rec *httptest.ResponseRecorder) dupCandResponse {
	t.Helper()
	var resp dupCandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v: %s", err, rec.Body.String())
	}
	return resp
}

func TestDuplicateCandidates_ReturnsGroupsWithRules(t *testing.T) {
	f, author := newDupCandFixture(t)

	f.addBook(t, author.ID, "The Martian", "OL1001W", false)
	f.addBook(t, author.ID, "The Martian", "OL1002W", false)
	m3 := f.addBook(t, author.ID, "Martian", "OL1003W", false)
	f.addBook(t, author.ID, "The Martian: A Novel", "OL1004W", false)
	d1 := f.addBook(t, author.ID, "Dune", "OL1005W", false)
	d2 := f.addBook(t, author.ID, "Dune", "OL1006W", false)
	f.addBook(t, author.ID, "Hyperion", "OL1007W", false)

	rec := f.get(t, author.ID, 0)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := parseDupCand(t, rec)

	if resp.AuthorID != author.ID {
		t.Errorf("authorId = %d, want %d", resp.AuthorID, author.ID)
	}
	if resp.Count != 2 || len(resp.Groups) != 2 {
		t.Fatalf("count = %d, groups = %d; want 2 of each", resp.Count, len(resp.Groups))
	}
	// Deterministic order: groups sorted by key.
	if resp.Groups[0].Key != "dune" || resp.Groups[1].Key != "themartian" {
		t.Fatalf("group order = %q, %q; want dune, themartian", resp.Groups[0].Key, resp.Groups[1].Key)
	}

	dune := resp.Groups[0]
	if len(dune.Books) != 2 {
		t.Fatalf("dune group has %d books, want 2", len(dune.Books))
	}
	if !rulesEqual(dune.Rules, []string{"alnum-equal"}) {
		t.Errorf("dune rules = %v, want [alnum-equal]", dune.Rules)
	}
	if dune.Books[0].ID != d1.ID || dune.Books[1].ID != d2.ID {
		t.Errorf("dune members not sorted by ID: %d, %d", dune.Books[0].ID, dune.Books[1].ID)
	}

	martian := resp.Groups[1]
	if len(martian.Books) != 4 {
		t.Fatalf("martian group has %d books, want 4", len(martian.Books))
	}
	for _, want := range []string{"article-strip", "substring"} {
		if !contains(martian.Rules, want) {
			t.Errorf("martian rules = %v, want to contain %s", martian.Rules, want)
		}
	}
	// m3 ("Martian") matched the group via article-strip against m1/m2.
	m3rules := rulesByID(martian.Books, m3.ID)
	if !contains(m3rules, "article-strip") {
		t.Errorf("member %q rules = %v, want article-strip", "Martian", m3rules)
	}
}

// TestDuplicateCandidates_SeriesPositionsSuppressSubstring is the end-to-end
// form of the #1970 review finding: "Foundation" is a folded substring of
// "Foundation and Empire", but real series data (different, known positions
// in the same series) proves they are different books, and the endpoint must
// not report them as a candidate pair. "Mistborn" / "Mistborn: The Final
// Empire" share a series position, so that pair still groups.
func TestDuplicateCandidates_SeriesPositionsSuppressSubstring(t *testing.T) {
	f, author := newDupCandFixture(t)

	foundation := f.addBook(t, author.ID, "Foundation", "OL6001W", false)
	foundationAndEmpire := f.addBook(t, author.ID, "Foundation and Empire", "OL6002W", false)
	secondFoundation := f.addBook(t, author.ID, "Second Foundation", "OL6003W", false)
	f.linkSeries(t, "OLS1S", "Foundation", foundation.ID, "1")
	f.linkSeries(t, "OLS1S", "Foundation", foundationAndEmpire.ID, "2")
	f.linkSeries(t, "OLS1S", "Foundation", secondFoundation.ID, "3")

	mistborn := f.addBook(t, author.ID, "Mistborn", "OL6004W", false)
	mistbornFull := f.addBook(t, author.ID, "Mistborn: The Final Empire", "OL6005W", false)
	f.linkSeries(t, "OLS2S", "Mistborn", mistborn.ID, "1")
	f.linkSeries(t, "OLS2S", "Mistborn", mistbornFull.ID, "1")
	// "Mistborn" is a whole separator segment of this sequel's title, so only
	// the series positions (1 against 2) keep it out of the Mistborn group.
	wellOfAscension := f.addBook(t, author.ID, "Mistborn: The Well of Ascension", "OL6006W", false)
	f.linkSeries(t, "OLS2S", "Mistborn", wellOfAscension.ID, "2")

	rec := f.get(t, author.ID, 0)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := parseDupCand(t, rec)

	if resp.Count != 1 || len(resp.Groups) != 1 {
		t.Fatalf("count = %d, groups = %d; want 1 (Foundation trio suppressed, Mistborn kept)", resp.Count, len(resp.Groups))
	}
	g := resp.Groups[0]
	if g.Key != "mistborn" {
		t.Fatalf("group key = %q, want mistborn", g.Key)
	}
	if len(g.Books) != 2 {
		t.Fatalf("mistborn group has %d books, want 2 (the position 2 sequel must stay out)", len(g.Books))
	}
}

func TestDuplicateCandidates_404UnknownAuthor(t *testing.T) {
	f, _ := newDupCandFixture(t)
	rec := f.get(t, 999999, 0)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDuplicateCandidates_EmptyCatalogue(t *testing.T) {
	f, author := newDupCandFixture(t)
	rec := f.get(t, author.ID, 0)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := parseDupCand(t, rec)
	if resp.Count != 0 || len(resp.Groups) != 0 {
		t.Errorf("empty catalogue: count = %d, groups = %d; want 0", resp.Count, len(resp.Groups))
	}
	// groups must serialize as [] not null, so the UI can iterate it.
	if body := rec.Body.String(); body == "" || !strings.Contains(body, `"groups":[]`) {
		t.Errorf("groups should be an empty array, body = %s", body)
	}
}

func TestDuplicateCandidates_SuppressesFullyExcludedGroups(t *testing.T) {
	f, author := newDupCandFixture(t)

	// Group A: both members excluded -> must be suppressed.
	f.addBook(t, author.ID, "Dune", "OL2001W", true)
	f.addBook(t, author.ID, "Dune", "OL2002W", true)
	// Group B: two active + one excluded -> returned, all three members shown.
	a1 := f.addBook(t, author.ID, "The Martian", "OL2003W", false)
	a2 := f.addBook(t, author.ID, "The Martian", "OL2004W", false)
	a3 := f.addBook(t, author.ID, "Martian", "OL2005W", true)

	rec := f.get(t, author.ID, 0)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := parseDupCand(t, rec)
	if resp.Count != 1 || len(resp.Groups) != 1 {
		t.Fatalf("count = %d, groups = %d; want only the partially-excluded group", resp.Count, len(resp.Groups))
	}
	g := resp.Groups[0]
	if g.Key != "themartian" {
		t.Fatalf("group key = %q, want themartian", g.Key)
	}
	if len(g.Books) != 3 {
		t.Fatalf("group has %d books, want 3 (excluded member still shown)", len(g.Books))
	}
	byID := map[int64]bool{}
	for _, b := range g.Books {
		byID[b.ID] = b.Excluded
	}
	if byID[a1.ID] || byID[a2.ID] || !byID[a3.ID] {
		t.Errorf("excluded flags wrong: %v (a3 must be excluded, a1/a2 active)", byID)
	}
}

func TestDuplicateCandidates_IsReadOnly(t *testing.T) {
	f, author := newDupCandFixture(t)
	f.addBook(t, author.ID, "The Martian", "OL3001W", false)
	f.addBook(t, author.ID, "The Martian", "OL3002W", false)
	f.addBook(t, author.ID, "Martian", "OL3003W", false)

	before := snapshotBooks(t, f, author.ID)
	first := f.get(t, author.ID, 0)
	if first.Code != http.StatusOK {
		t.Fatalf("first call: %d", first.Code)
	}
	second := f.get(t, author.ID, 0)
	if second.Code != http.StatusOK {
		t.Fatalf("second call: %d", second.Code)
	}
	if first.Body.String() != second.Body.String() {
		t.Errorf("responses differ across calls:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	after := snapshotBooks(t, f, author.ID)
	if before != after {
		t.Errorf("endpoint mutated the catalogue:\nbefore = %s\nafter  = %s", before, after)
	}
}

// snapshotBooks renders the author's book rows (titles + excluded flags) as
// a stable string so TestDuplicateCandidates_IsReadOnly can compare them.
func snapshotBooks(t *testing.T, f *dupCandFixture, authorID int64) string {
	t.Helper()
	books, err := f.books.ListByAuthorIncludingExcluded(f.ctx, authorID)
	if err != nil {
		t.Fatalf("list books: %v", err)
	}
	out := make([]string, len(books))
	for i, b := range books {
		ex := "in"
		if b.Excluded {
			ex = "ex"
		}
		out[i] = b.Title + "/" + ex
	}
	slices.Sort(out)
	return strings.Join(out, "|")
}

func TestDuplicateCandidates_TenancyOnRejectsCrossUserAuthor(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f, aliceAuthor, bobID := dupTenancyFixture(t)

	rec := f.get(t, aliceAuthor.ID, bobID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-user author with tenancy on, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDuplicateCandidates_TenancyOffAllowsCrossUserAuthor(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f, aliceAuthor, bobID := dupTenancyFixture(t)

	rec := f.get(t, aliceAuthor.ID, bobID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with tenancy enforcement off, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDuplicateCandidates_Scale(t *testing.T) {
	f, author := newDupCandFixture(t)
	for i := 0; i < 996; i++ {
		f.addBook(t, author.ID, "Distinct Title Number "+strconv.Itoa(i), "OL"+strconv.Itoa(4000+i)+"W", false)
	}
	f.addBook(t, author.ID, "The Martian", "OL4997W", false)
	f.addBook(t, author.ID, "The Martian", "OL4998W", false)
	f.addBook(t, author.ID, "Martian", "OL4999W", false)
	f.addBook(t, author.ID, "The Martian: A Novel", "OL5000W", false)

	start := time.Now()
	rec := f.get(t, author.ID, 0)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if elapsed > 2*time.Second {
		t.Errorf("endpoint took %s, budget 2s", elapsed)
	}
	resp := parseDupCand(t, rec)
	if resp.Count != 1 {
		t.Errorf("count = %d, want 1 (the martian group)", resp.Count)
	}
}

// dupTenancyFixture seeds two users, an author owned by alice (with two
// duplicate books), and returns the fixture plus bob's id.
func dupTenancyFixture(t *testing.T) (*dupCandFixture, *models.Author, int64) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	f := &dupCandFixture{
		ctx:     ctx,
		authors: authors,
		books:   books,
		handler: NewAuthorHandler(authors, nil, books, nil, nil, nil, nil, nil),
	}
	author := &models.Author{
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		Monitored:        true,
		MetadataProvider: "openlibrary",
	}
	if err := authors.CreateForUser(ctx, author, alice.ID); err != nil {
		t.Fatalf("seed alice author: %v", err)
	}
	f.addBook(t, author.ID, "The Martian", "OL5001W", false)
	f.addBook(t, author.ID, "The Martian", "OL5002W", false)
	return f, author, bob.ID
}

func rulesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func rulesByID(books []dupCandBook, id int64) []string {
	for _, b := range books {
		if b.ID == id {
			return b.Rules
		}
	}
	return nil
}

// dupEvidenceGroup decodes the #2999 review annotations.
type dupEvidenceGroup struct {
	Key                 string  `json:"key"`
	AuthorID            int64   `json:"authorId"`
	AuthorName          string  `json:"authorName"`
	Conflict            bool    `json:"conflict"`
	KeeperID            int64   `json:"keeperId"`
	SuggestedExcludeIDs []int64 `json:"suggestedExcludeIds"`
	Signals             []struct {
		Kind     string   `json:"kind"`
		Conflict bool     `json:"conflict"`
		BookIDs  []int64  `json:"bookIds"`
		Values   []string `json:"values"`
	} `json:"signals"`
	Books []struct {
		ID          int64  `json:"id"`
		Status      string `json:"status"`
		Excluded    bool   `json:"excluded"`
		Description string `json:"description"`
		HasFiles    bool   `json:"hasFiles"`
		Evidence    struct {
			Files []struct {
				Kind   string `json:"kind"`
				Format string `json:"format"`
			} `json:"files"`
			ISBNs  []string `json:"isbns"`
			ASINs  []string `json:"asins"`
			Series []struct {
				Title    string `json:"title"`
				Position string `json:"position"`
			} `json:"series"`
			Year int `json:"year"`
		} `json:"evidence"`
	} `json:"books"`
}

// addFile records a real on-disk file for the book, the way an import does.
func (f *dupCandFixture) addFile(t *testing.T, bookID int64, format, name string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(f.ctx, bookID, format, path); err != nil {
		t.Fatalf("add file: %v", err)
	}
}

// addEdition stores an edition carrying an ISBN for the book.
func (f *dupCandFixture) addEdition(t *testing.T, bookID int64, foreignID, isbn13, isbn10 string) {
	t.Helper()
	e := &models.Edition{ForeignID: foreignID, BookID: bookID, Title: "edition", Monitored: true}
	if isbn13 != "" {
		e.ISBN13 = &isbn13
	}
	if isbn10 != "" {
		e.ISBN10 = &isbn10
	}
	if err := db.NewEditionRepo(f.database).Upsert(f.ctx, e); err != nil {
		t.Fatalf("add edition: %v", err)
	}
}

// TestDuplicateCandidates_Evidence is the per-author half of #2999: each row
// says which one has files and what identifies it, and the group says whether
// the rows agree, with the row that has files as the keeper.
func TestDuplicateCandidates_Evidence(t *testing.T) {
	f, author := newDupCandFixture(t)
	empty := f.addBook(t, author.ID, "Nightingale", "OL7001W", false)
	owned := f.addBook(t, author.ID, "The Nightingale", "OL7002W", false)
	f.addFile(t, owned.ID, "ebook", "The Nightingale.epub")
	f.addEdition(t, owned.ID, "OL7002M", "9780553418026", "")
	f.addEdition(t, empty.ID, "OL7001M", "", "0553418025")
	f.linkSeries(t, "OLS7S", "Standalones", owned.ID, "1")
	f.linkSeries(t, "OLS7S", "Standalones", empty.ID, "1")

	rec := f.get(t, author.ID, 0)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Groups []dupEvidenceGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(resp.Groups))
	}
	g := resp.Groups[0]
	if g.AuthorID != author.ID || g.AuthorName != "Andy Weir" {
		t.Errorf("group author = %d %q", g.AuthorID, g.AuthorName)
	}
	if g.KeeperID != owned.ID {
		t.Errorf("keeperId = %d, want %d (the row with the file)", g.KeeperID, owned.ID)
	}
	if !slices.Equal(g.SuggestedExcludeIDs, []int64{empty.ID}) {
		t.Errorf("suggestedExcludeIds = %v, want [%d]", g.SuggestedExcludeIDs, empty.ID)
	}
	if g.Conflict {
		t.Error("conflict = true for agreeing rows")
	}
	kinds := map[string]bool{}
	for _, s := range g.Signals {
		kinds[s.Kind] = true
	}
	if !kinds["shared-isbn"] || !kinds["same-series-position"] {
		t.Errorf("signals = %+v, want shared-isbn (ISBN-10 vs ISBN-13 of one edition) and same-series-position", g.Signals)
	}
	for _, b := range g.Books {
		switch b.ID {
		case owned.ID:
			if !b.HasFiles || len(b.Evidence.Files) != 1 || b.Evidence.Files[0].Format != "epub" {
				t.Errorf("owned row evidence = %+v", b.Evidence)
			}
			if b.Status != models.BookStatusImported {
				t.Errorf("owned row status = %q, want imported", b.Status)
			}
		case empty.ID:
			if b.HasFiles || len(b.Evidence.Files) != 0 {
				t.Errorf("empty row reports files: %+v", b.Evidence)
			}
			if !slices.Equal(b.Evidence.ISBNs, []string{"9780553418026"}) {
				t.Errorf("empty row isbns = %v, want the ISBN-10 as ISBN-13", b.Evidence.ISBNs)
			}
			if len(b.Evidence.Series) != 1 || b.Evidence.Series[0].Position != "1" {
				t.Errorf("empty row series = %+v", b.Evidence.Series)
			}
		}
	}
}
