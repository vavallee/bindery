package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// covSrRecFixture is an in-memory recommendation handler with author and book
// repos wired, plus the raw database so a test can close it to force repo
// errors.
type covSrRecFixture struct {
	database *sql.DB
	recs     *db.RecommendationRepo
	authors  *db.AuthorRepo
	books    *db.BookRepo
	searcher *mockBookSearcher
	engine   *covSrRecEngine
	h        *RecommendationHandler
}

// covSrRecEngine records each Run call's user id on a channel so the
// background refresh goroutine can be observed without sleeping.
type covSrRecEngine struct {
	calls chan int64
	err   error
}

func (e *covSrRecEngine) Run(_ context.Context, userID int64) error {
	e.calls <- userID
	return e.err
}

func covSrNewRecFixture(t *testing.T) *covSrRecFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &covSrRecFixture{
		database: database,
		recs:     db.NewRecommendationRepo(database),
		authors:  db.NewAuthorRepo(database),
		books:    db.NewBookRepo(database),
		searcher: newMockBookSearcher(),
		engine:   &covSrRecEngine{calls: make(chan int64, 1)},
	}
	f.h = NewRecommendationHandler(f.recs, f.engine, f.authors, f.books, f.searcher).
		WithAppContext(context.Background())
	return f
}

// seed replaces userID's feed with candidates and returns the stored rows in
// score order.
func (f *covSrRecFixture) seed(t *testing.T, userID int64, candidates ...models.RecommendationCandidate) []models.Recommendation {
	t.Helper()
	ctx := context.Background()
	for i := range candidates {
		if candidates[i].Genres == nil {
			candidates[i].Genres = []string{}
		}
	}
	if err := f.recs.ReplaceBatch(ctx, userID, candidates); err != nil {
		t.Fatal(err)
	}
	rows, err := f.recs.List(ctx, userID, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func covSrRecRequest(method, target, body string, userID int64, role string, params map[string]string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	ctx := req.Context()
	if userID != 0 {
		ctx = auth.WithUserID(ctx, userID)
	}
	if role != "" {
		ctx = auth.WithUserRole(ctx, role)
	}
	req = req.WithContext(ctx)
	if params != nil {
		req = withURLParams(req, params)
	}
	return req
}

func covSrDecodeError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body["error"]
}

func TestRecommendationListAppliesTypeLimitAndOffset(t *testing.T) {
	f := covSrNewRecFixture(t)
	f.seed(t, 7,
		models.RecommendationCandidate{ForeignID: "hc:a", RecType: models.RecTypeSeries, Title: "A", Score: 3},
		models.RecommendationCandidate{ForeignID: "hc:b", RecType: models.RecTypeSeries, Title: "B", Score: 2},
		models.RecommendationCandidate{ForeignID: "hc:c", RecType: models.RecTypeGenreSimilar, Title: "C", Score: 1},
	)

	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"hc:a", "hc:b", "hc:c"}},
		{"?type=series", []string{"hc:a", "hc:b"}},
		{"?limit=1&offset=1", []string{"hc:b"}},
		// Garbage and non-positive values fall back to the defaults.
		{"?limit=0&offset=-1", []string{"hc:a", "hc:b", "hc:c"}},
		{"?limit=x&offset=y", []string{"hc:a", "hc:b", "hc:c"}},
		{"?type=serendipity", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			rec := httptest.NewRecorder()
			f.h.List(rec, covSrRecRequest(http.MethodGet, "/api/v1/recommendations"+tc.query, "", 7, "", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			// An empty page must encode as [], not null.
			if len(tc.want) == 0 && strings.TrimSpace(rec.Body.String()) != "[]" {
				t.Fatalf("empty page body = %q, want []", rec.Body.String())
			}
			var got []models.Recommendation
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			ids := make([]string, 0, len(got))
			for _, r := range got {
				ids = append(ids, r.ForeignID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("foreign ids = %v, want %v", ids, tc.want)
			}
		})
	}
}

func TestRecommendationDismissOwnershipUnderTenancy(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := covSrNewRecFixture(t)
	const alice, bob int64 = 11, 12
	rows := f.seed(t, alice, models.RecommendationCandidate{ForeignID: "hc:alice", RecType: models.RecTypeSeries, Title: "Alice's", Score: 1})
	id := strconv.FormatInt(rows[0].ID, 10)
	ctx := context.Background()

	// Bob cannot dismiss alice's row: 404, not 403, and nothing changes.
	rec := httptest.NewRecorder()
	f.h.Dismiss(rec, covSrRecRequest(http.MethodPost, "/api/v1/recommendations/"+id+"/dismiss", "", bob, "user", map[string]string{"id": id}))
	if rec.Code != http.StatusNotFound || covSrDecodeError(t, rec) != "recommendation not found" {
		t.Fatalf("cross-user dismiss = %d %s, want 404 recommendation not found", rec.Code, rec.Body.String())
	}
	if dismissed, _ := f.recs.IsDismissed(ctx, bob, "hc:alice"); dismissed {
		t.Fatal("cross-user dismiss recorded a dismissal for bob")
	}
	if left, _ := f.recs.List(ctx, alice, "", 0, 0); len(left) != 1 {
		t.Fatalf("alice's feed changed after bob's refused dismiss: %+v", left)
	}

	// An unknown id is the same 404.
	rec = httptest.NewRecorder()
	f.h.Dismiss(rec, covSrRecRequest(http.MethodPost, "/", "", alice, "user", map[string]string{"id": "9999"}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id dismiss = %d, want 404", rec.Code)
	}

	// A non-numeric id is rejected before any lookup.
	rec = httptest.NewRecorder()
	f.h.Dismiss(rec, covSrRecRequest(http.MethodPost, "/", "", alice, "user", map[string]string{"id": "abc"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id dismiss = %d, want 400", rec.Code)
	}

	// The owner can dismiss: 204, the row leaves the feed and the dismissal
	// is persisted for the owner.
	rec = httptest.NewRecorder()
	f.h.Dismiss(rec, covSrRecRequest(http.MethodPost, "/", "", alice, "user", map[string]string{"id": id}))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("owner dismiss = %d %s, want 204", rec.Code, rec.Body.String())
	}
	if left, _ := f.recs.List(ctx, alice, "", 0, 0); len(left) != 0 {
		t.Fatalf("dismissed row still listed: %+v", left)
	}
	if dismissed, _ := f.recs.IsDismissed(ctx, alice, "hc:alice"); !dismissed {
		t.Fatal("owner dismissal not persisted")
	}

	// ClearDismissals forgets it again for the caller only.
	rec = httptest.NewRecorder()
	f.h.ClearDismissals(rec, covSrRecRequest(http.MethodDelete, "/", "", alice, "user", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("clear dismissals = %d, want 204", rec.Code)
	}
	if dismissed, _ := f.recs.IsDismissed(ctx, alice, "hc:alice"); dismissed {
		t.Fatal("ClearDismissals left the dismissal in place")
	}
}

func TestRecommendationAddRefusals(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := covSrNewRecFixture(t)
	ctx := context.Background()
	const alice, bob int64 = 21, 22

	author := &models.Author{ForeignID: "OL-REC-A", Name: "Known Author", SortName: "Author, Known", MetadataProvider: "openlibrary", Monitored: true}
	if err := f.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	existing := &models.Book{ForeignID: "OL-EXISTS", AuthorID: author.ID, Title: "Already Here", SortTitle: "Already Here", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := f.books.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}
	rows := f.seed(t, alice,
		models.RecommendationCandidate{ForeignID: "OL-EXISTS", RecType: models.RecTypeSeries, Title: "Already Here", AuthorID: &author.ID, Score: 3},
		models.RecommendationCandidate{ForeignID: "OL-ORPHAN", RecType: models.RecTypeSeries, Title: "No Author", AuthorName: "Nobody We Know", Score: 2},
		models.RecommendationCandidate{ForeignID: "OL-NONAME", RecType: models.RecTypeSeries, Title: "Blank Author", Score: 1},
	)
	idOf := func(foreignID string) string {
		for _, r := range rows {
			if r.ForeignID == foreignID {
				return strconv.FormatInt(r.ID, 10)
			}
		}
		t.Fatalf("no seeded row %s", foreignID)
		return ""
	}

	cases := []struct {
		name     string
		id       string
		caller   int64
		wantCode int
		wantErr  string
	}{
		{"bad id", "x", alice, http.StatusBadRequest, "invalid id"},
		{"unknown id", "424242", alice, http.StatusNotFound, "recommendation not found"},
		{"other user's row", idOf("OL-ORPHAN"), bob, http.StatusNotFound, "recommendation not found"},
		{"book already exists", idOf("OL-EXISTS"), alice, http.StatusConflict, "book already exists"},
		{"author name unknown", idOf("OL-ORPHAN"), alice, http.StatusBadRequest, "cannot resolve author for this recommendation"},
		{"no author at all", idOf("OL-NONAME"), alice, http.StatusBadRequest, "cannot resolve author for this recommendation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			f.h.Add(rec, covSrRecRequest(http.MethodPost, "/", "", tc.caller, "user", map[string]string{"id": tc.id}))
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if got := covSrDecodeError(t, rec); got != tc.wantErr {
				t.Fatalf("error = %q, want %q", got, tc.wantErr)
			}
		})
	}

	// None of the refusals created a book or queued a search.
	for _, fid := range []string{"OL-ORPHAN", "OL-NONAME"} {
		if b, _ := f.books.GetByForeignID(ctx, fid); b != nil {
			t.Fatalf("refused add created book %s", fid)
		}
	}
	select {
	case b := <-f.searcher.ch:
		t.Fatalf("refused add queued a search for %q", b.Title)
	default:
	}
	if left, _ := f.recs.List(ctx, alice, "", 0, 0); len(left) != 3 {
		t.Fatalf("refused adds dismissed rows: %d left, want 3", len(left))
	}
}

// TestRecommendationAddResolvesAuthorByName covers the branch where the
// recommendation carries only an author name: the handler must find the local
// author with that exact name, create the book as wanted under it, dismiss the
// row and queue a search.
func TestRecommendationAddResolvesAuthorByName(t *testing.T) {
	f := covSrNewRecFixture(t)
	ctx := context.Background()
	for _, name := range []string{"Someone Else", "Named Author"} {
		if err := f.authors.Create(ctx, &models.Author{ForeignID: "OL-" + strings.ReplaceAll(name, " ", ""), Name: name, SortName: name, MetadataProvider: "openlibrary", Monitored: true}); err != nil {
			t.Fatal(err)
		}
	}
	target, err := f.authors.GetByForeignID(ctx, "OL-NamedAuthor")
	if err != nil || target == nil {
		t.Fatalf("author lookup: %v %v", target, err)
	}
	fetcherCalled := false
	f.h.WithEditionFetcher(func(context.Context, string) ([]models.Edition, error) {
		fetcherCalled = true
		return nil, nil
	})
	rows := f.seed(t, 1, models.RecommendationCandidate{
		ForeignID: "OL-BYNAME", RecType: models.RecTypeAuthorNew, Title: "Found By Name",
		AuthorName: "Named Author", MediaType: models.MediaTypeEbook, Score: 1,
	})
	id := strconv.FormatInt(rows[0].ID, 10)

	rec := httptest.NewRecorder()
	f.h.Add(rec, covSrRecRequest(http.MethodPost, "/", "", 1, "", map[string]string{"id": id}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var created models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.AuthorID != target.ID || created.Status != models.BookStatusWanted || !created.Monitored {
		t.Fatalf("created book = author %d status %q monitored %v; want author %d wanted monitored", created.AuthorID, created.Status, created.Monitored, target.ID)
	}
	stored, err := f.books.GetByForeignID(ctx, "OL-BYNAME")
	if err != nil || stored == nil || stored.AuthorID != target.ID {
		t.Fatalf("stored book = %+v, %v", stored, err)
	}
	if queued := f.searcher.waitForCall(t, time.Second); queued.ForeignID != "OL-BYNAME" {
		t.Fatalf("queued search for %q, want OL-BYNAME", queued.ForeignID)
	}
	if left, _ := f.recs.List(ctx, 1, "", 0, 0); len(left) != 0 {
		t.Fatalf("added recommendation still listed: %+v", left)
	}
	// Edition hydration is not wired (no edition repo), so the fetcher must
	// not have been consulted.
	if fetcherCalled {
		t.Fatal("edition fetcher called without an edition repo")
	}
}

func TestRecommendationRefreshRunsEngineForCaller(t *testing.T) {
	for _, engineErr := range []error{nil, errors.New("engine exploded")} {
		name := "ok"
		if engineErr != nil {
			name = "engine error"
		}
		t.Run(name, func(t *testing.T) {
			f := covSrNewRecFixture(t)
			f.engine.err = engineErr
			rec := httptest.NewRecorder()
			f.h.Refresh(rec, covSrRecRequest(http.MethodPost, "/", "", 33, "", nil))
			if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "refresh started") {
				t.Fatalf("refresh = %d %s, want 202 refresh started", rec.Code, rec.Body.String())
			}
			select {
			case uid := <-f.engine.calls:
				if uid != 33 {
					t.Fatalf("engine ran for user %d, want 33", uid)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("engine was not run")
			}
		})
	}
}

func TestRecommendationAuthorExclusionsLifecycle(t *testing.T) {
	f := covSrNewRecFixture(t)
	const user int64 = 44

	list := func() []string {
		t.Helper()
		rec := httptest.NewRecorder()
		f.h.ListAuthorExclusions(rec, covSrRecRequest(http.MethodGet, "/", "", user, "", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("list exclusions = %d", rec.Code)
		}
		var names []string
		if err := json.Unmarshal(rec.Body.Bytes(), &names); err != nil {
			t.Fatal(err)
		}
		if names == nil {
			t.Fatalf("empty exclusion list encoded as %q, want []", rec.Body.String())
		}
		return names
	}

	if got := list(); len(got) != 0 {
		t.Fatalf("fresh exclusions = %v, want none", got)
	}

	for _, body := range []string{"not json", `{}`, `{"authorName":""}`} {
		rec := httptest.NewRecorder()
		f.h.ExcludeAuthor(rec, covSrRecRequest(http.MethodPost, "/", body, user, "", nil))
		if rec.Code != http.StatusBadRequest || covSrDecodeError(t, rec) != "authorName required" {
			t.Fatalf("exclude %q = %d %s, want 400 authorName required", body, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	f.h.ExcludeAuthor(rec, covSrRecRequest(http.MethodPost, "/", `{"authorName":"Prolific Person"}`, user, "", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("exclude = %d %s, want 201", rec.Code, rec.Body.String())
	}
	if got := list(); len(got) != 1 || got[0] != "Prolific Person" {
		t.Fatalf("exclusions = %v, want [Prolific Person]", got)
	}
	// Exclusions are per user.
	if other, _ := f.recs.ListAuthorExclusions(context.Background(), user+1); len(other) != 0 {
		t.Fatalf("exclusion leaked to another user: %v", other)
	}

	rec = httptest.NewRecorder()
	f.h.RemoveAuthorExclusion(rec, covSrRecRequest(http.MethodDelete, "/", "", user, "", map[string]string{"name": ""}))
	if rec.Code != http.StatusBadRequest || covSrDecodeError(t, rec) != "author name required" {
		t.Fatalf("remove blank = %d %s, want 400", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	f.h.RemoveAuthorExclusion(rec, covSrRecRequest(http.MethodDelete, "/", "", user, "", map[string]string{"name": "Prolific Person"}))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove = %d %s, want 204", rec.Code, rec.Body.String())
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("exclusions after remove = %v, want none", got)
	}
}

// TestRecommendationHandlersReturn500OnRepoFailure closes the database under
// every handler that reads or writes the recommendation repo and checks each
// answers a generic 500 rather than leaking the SQL error.
func TestRecommendationHandlersReturn500OnRepoFailure(t *testing.T) {
	f := covSrNewRecFixture(t)
	if err := f.database.Close(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request)
		body string
		args map[string]string
	}{
		{"List", f.h.List, "", nil},
		{"Dismiss", f.h.Dismiss, "", map[string]string{"id": "1"}},
		{"Add", f.h.Add, "", map[string]string{"id": "1"}},
		{"ClearDismissals", f.h.ClearDismissals, "", nil},
		{"ListAuthorExclusions", f.h.ListAuthorExclusions, "", nil},
		{"ExcludeAuthor", f.h.ExcludeAuthor, `{"authorName":"X"}`, nil},
		{"RemoveAuthorExclusion", f.h.RemoveAuthorExclusion, "", map[string]string{"name": "X"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, covSrRecRequest(http.MethodPost, "/", tc.body, 1, "", tc.args))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
			}
			if got := covSrDecodeError(t, rec); got != "internal server error" {
				t.Fatalf("error = %q, want the generic message", got)
			}
		})
	}
}
