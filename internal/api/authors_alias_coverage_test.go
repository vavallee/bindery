package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// covAuEnv is a small in-memory fixture for the alias and genre handler
// tests: two users, an author owned by each, and the repos the handlers need.
type covAuEnv struct {
	ctx      context.Context
	database *sql.DB
	authors  *db.AuthorRepo
	aliases  *db.AuthorAliasRepo
	books    *db.BookRepo
	series   *db.SeriesRepo
	alice    int64
	bob      int64
	aliceA   *models.Author
	bobA     *models.Author
}

func covAuSetup(t *testing.T) *covAuEnv {
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
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}
	env := &covAuEnv{
		ctx:      ctx,
		database: database,
		authors:  db.NewAuthorRepo(database),
		aliases:  db.NewAuthorAliasRepo(database),
		books:    db.NewBookRepo(database),
		series:   db.NewSeriesRepo(database),
		alice:    alice.ID,
		bob:      bob.ID,
	}
	env.aliceA = &models.Author{ForeignID: "OL-ALICE-A", Name: "Alice Writer", SortName: "Writer, Alice", Monitored: true}
	if err := env.authors.CreateForUser(ctx, env.aliceA, alice.ID); err != nil {
		t.Fatal(err)
	}
	env.bobA = &models.Author{ForeignID: "OL-BOB-A", Name: "Bob Writer", SortName: "Writer, Bob", Monitored: true}
	if err := env.authors.CreateForUser(ctx, env.bobA, bob.ID); err != nil {
		t.Fatal(err)
	}
	return env
}

// covAuRequest builds a request with chi URL params and an optional caller
// identity. userID 0 leaves the context anonymous; role "" leaves it unset.
func covAuRequest(method, path, body string, params map[string]string, userID int64, role string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	if userID != 0 {
		ctx = auth.WithUserID(ctx, userID)
	}
	if role != "" {
		ctx = auth.WithUserRole(ctx, role)
	}
	return req.WithContext(ctx)
}

func covAuID(id int64) string { return strconv.FormatInt(id, 10) }

func covAuErrorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	s, _ := body["error"].(string)
	return s
}

func TestAuthorAliasListCoverage(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	env := covAuSetup(t)
	h := NewAuthorAliasHandler(env.authors, env.aliases)
	if err := env.aliases.Create(env.ctx, &models.AuthorAlias{AuthorID: env.aliceA.ID, Name: "A. Writer"}); err != nil {
		t.Fatal(err)
	}

	t.Run("bad id is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": "abc"}, env.alice, "user"))
		if rec.Code != http.StatusBadRequest || covAuErrorBody(t, rec) != "invalid id" {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("missing author is 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": "9999"}, env.alice, "user"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("another user's author is 404 not its aliases", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": covAuID(env.aliceA.ID)}, env.bob, "user"))
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "A. Writer") {
			t.Fatalf("cross-user alias list: got %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("owner sees aliases", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": covAuID(env.aliceA.ID)}, env.alice, "user"))
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		var got []models.AuthorAlias
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != "A. Writer" {
			t.Fatalf("aliases = %+v", got)
		}
	})
	t.Run("admin sees another user's aliases", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": covAuID(env.aliceA.ID)}, env.bob, "admin"))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "A. Writer") {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("no aliases is an empty array", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": covAuID(env.bobA.ID)}, env.bob, "user"))
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("got %d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("repo error is 500", func(t *testing.T) {
		_ = env.database.Close()
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": covAuID(env.aliceA.ID)}, env.alice, "user"))
		if rec.Code != http.StatusInternalServerError || covAuErrorBody(t, rec) != "internal server error" {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestAuthorAliasDeleteCoverage(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	env := covAuSetup(t)
	h := NewAuthorAliasHandler(env.authors, env.aliases)
	alias := &models.AuthorAlias{AuthorID: env.aliceA.ID, Name: "A. Writer"}
	if err := env.aliases.Create(env.ctx, alias); err != nil {
		t.Fatal(err)
	}
	params := func(author, aliasID string) map[string]string {
		return map[string]string{"id": author, "aliasID": aliasID}
	}

	cases := []struct {
		name   string
		params map[string]string
		user   int64
		want   int
		errMsg string
	}{
		{"bad author id", params("x", "1"), env.alice, http.StatusBadRequest, "invalid id"},
		{"bad alias id", params(covAuID(env.aliceA.ID), "x"), env.alice, http.StatusBadRequest, "invalid alias id"},
		{"missing author", params("9999", covAuID(alias.ID)), env.alice, http.StatusNotFound, "author not found"},
		{"another user's author", params(covAuID(env.aliceA.ID), covAuID(alias.ID)), env.bob, http.StatusNotFound, "author not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.Delete(rec, covAuRequest(http.MethodDelete, "/", "", tc.params, tc.user, "user"))
			if rec.Code != tc.want || covAuErrorBody(t, rec) != tc.errMsg {
				t.Fatalf("got %d %s, want %d %q", rec.Code, rec.Body.String(), tc.want, tc.errMsg)
			}
		})
	}
	// None of the rejected calls may have removed the alias.
	got, err := env.aliases.ListByAuthor(env.ctx, env.aliceA.ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("alias removed by a rejected call: %+v err=%v", got, err)
	}

	_ = env.database.Close()
	rec := httptest.NewRecorder()
	h.Delete(rec, covAuRequest(http.MethodDelete, "/", "", params(covAuID(env.aliceA.ID), covAuID(alias.ID)), env.alice, "user"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closed db: got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuthorAliasMergeCoverage(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)

	t.Run("validation", func(t *testing.T) {
		env := covAuSetup(t)
		h := NewAuthorAliasHandler(env.authors, env.aliases)
		target := covAuID(env.aliceA.ID)
		cases := []struct {
			name, id, body, errMsg string
		}{
			{"bad id", "x", `{"sourceId":1}`, "invalid id"},
			{"bad body", target, `{`, "invalid request body"},
			{"missing source", target, `{}`, "sourceId required"},
			{"self merge", target, `{"sourceId":` + target + `}`, "source and target must differ"},
		}
		for _, tc := range cases {
			rec := httptest.NewRecorder()
			h.Merge(rec, covAuRequest(http.MethodPost, "/", tc.body, map[string]string{"id": tc.id}, env.alice, "user"))
			if rec.Code != http.StatusBadRequest || covAuErrorBody(t, rec) != tc.errMsg {
				t.Fatalf("%s: got %d %s", tc.name, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("cannot absorb another user's author", func(t *testing.T) {
		env := covAuSetup(t)
		h := NewAuthorAliasHandler(env.authors, env.aliases)
		body := `{"sourceId":` + covAuID(env.aliceA.ID) + `}`
		rec := httptest.NewRecorder()
		h.Merge(rec, covAuRequest(http.MethodPost, "/", body, map[string]string{"id": covAuID(env.bobA.ID)}, env.bob, "user"))
		if rec.Code != http.StatusNotFound || covAuErrorBody(t, rec) != "author not found: "+covAuID(env.aliceA.ID) {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		if a, _ := env.authors.GetByID(env.ctx, env.aliceA.ID); a == nil {
			t.Fatal("alice's author was deleted by bob's merge")
		}
	})

	t.Run("cannot merge into another user's author", func(t *testing.T) {
		env := covAuSetup(t)
		h := NewAuthorAliasHandler(env.authors, env.aliases)
		body := `{"sourceId":` + covAuID(env.bobA.ID) + `}`
		rec := httptest.NewRecorder()
		h.Merge(rec, covAuRequest(http.MethodPost, "/", body, map[string]string{"id": covAuID(env.aliceA.ID)}, env.bob, "user"))
		if rec.Code != http.StatusNotFound || covAuErrorBody(t, rec) != "author not found: "+covAuID(env.aliceA.ID) {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		if a, _ := env.authors.GetByID(env.ctx, env.bobA.ID); a == nil {
			t.Fatal("bob's author was deleted by a rejected merge")
		}
	})

	t.Run("overwriteDefaults false still merges", func(t *testing.T) {
		env := covAuSetup(t)
		h := NewAuthorAliasHandler(env.authors, env.aliases)
		book := &models.Book{ForeignID: "OL-BOB-BOOK", AuthorID: env.bobA.ID, Title: "Bob's Book", Status: models.BookStatusWanted}
		if err := env.books.Create(env.ctx, book); err != nil {
			t.Fatal(err)
		}
		body := `{"sourceId":` + covAuID(env.bobA.ID) + `,"overwriteDefaults":false}`
		rec := httptest.NewRecorder()
		h.Merge(rec, covAuRequest(http.MethodPost, "/", body, map[string]string{"id": covAuID(env.aliceA.ID)}, env.alice, "admin"))
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		var res db.MergeResult
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res.BooksReparented != 1 {
			t.Fatalf("result = %+v", res)
		}
		if a, _ := env.authors.GetByID(env.ctx, env.bobA.ID); a != nil {
			t.Fatal("source author survived the merge")
		}
		moved, _ := env.books.GetByID(env.ctx, book.ID)
		if moved == nil || moved.AuthorID != env.aliceA.ID {
			t.Fatalf("book not reparented: %+v", moved)
		}
	})

	t.Run("repo error is 500", func(t *testing.T) {
		env := covAuSetup(t)
		h := NewAuthorAliasHandler(env.authors, env.aliases)
		_ = env.database.Close()
		body := `{"sourceId":` + covAuID(env.bobA.ID) + `}`
		rec := httptest.NewRecorder()
		h.Merge(rec, covAuRequest(http.MethodPost, "/", body, map[string]string{"id": covAuID(env.aliceA.ID)}, env.alice, "admin"))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestApplyGenresAuthorCoverage(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	env := covAuSetup(t)
	h := NewAuthorHandler(env.authors, env.aliases, env.books, env.series, nil, nil, nil, nil)
	book := &models.Book{ForeignID: "OL-ALICE-BOOK", AuthorID: env.aliceA.ID, Title: "Alice's Book", Status: models.BookStatusWanted, Genres: []string{"Old"}}
	if err := env.books.Create(env.ctx, book); err != nil {
		t.Fatal(err)
	}
	id := covAuID(env.aliceA.ID)

	cases := []struct {
		name, id, body string
		user           int64
		want           int
		errMsg         string
	}{
		{"bad id", "x", `{"genres":["A"]}`, env.alice, http.StatusBadRequest, "invalid id"},
		{"missing author", "9999", `{"genres":["A"]}`, env.alice, http.StatusNotFound, "author not found"},
		{"another user's author", id, `{"genres":["Hijacked"]}`, env.bob, http.StatusNotFound, "author not found"},
		{"bad body", id, `{`, env.alice, http.StatusBadRequest, "invalid request body"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ApplyGenres(rec, covAuRequest(http.MethodPut, "/", tc.body, map[string]string{"id": tc.id}, tc.user, "user"))
		if rec.Code != tc.want || covAuErrorBody(t, rec) != tc.errMsg {
			t.Fatalf("%s: got %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	got, _ := env.books.GetByID(env.ctx, book.ID)
	if got == nil || len(got.Genres) != 1 || got.Genres[0] != "Old" || got.IsFieldLocked(models.BookFieldGenres) {
		t.Fatalf("a rejected call changed the book: %+v", got)
	}

	rec := httptest.NewRecorder()
	h.ApplyGenres(rec, covAuRequest(http.MethodPut, "/", `{"genres":[" Fantasy ",""]}`, map[string]string{"id": id}, env.alice, "user"))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"updated":1}` {
		t.Fatalf("owner apply: got %d %s", rec.Code, rec.Body.String())
	}
	got, _ = env.books.GetByID(env.ctx, book.ID)
	if got == nil || len(got.Genres) != 1 || got.Genres[0] != "Fantasy" || !got.IsFieldLocked(models.BookFieldGenres) {
		t.Fatalf("genres not applied and locked: %+v", got)
	}
}

func TestApplyGenresSeriesCoverage(t *testing.T) {
	env := covAuSetup(t)
	h := NewSeriesHandler(env.series, env.books, env.authors, nil, nil)
	s, err := env.series.CreateManual(env.ctx, "Saga")
	if err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-SAGA-1", AuthorID: env.aliceA.ID, Title: "Saga One", Status: models.BookStatusWanted}
	if err := env.books.Create(env.ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := env.series.UpsertBookLink(env.ctx, s.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	id := covAuID(s.ID)

	for _, tc := range []struct {
		name, id, body string
		want           int
		errMsg         string
	}{
		{"bad id", "x", `{"genres":[]}`, http.StatusBadRequest, "invalid id"},
		{"missing series", "9999", `{"genres":[]}`, http.StatusNotFound, "series not found"},
		{"bad body", id, `{`, http.StatusBadRequest, "invalid request body"},
	} {
		rec := httptest.NewRecorder()
		h.ApplyGenres(rec, covAuRequest(http.MethodPut, "/", tc.body, map[string]string{"id": tc.id}, 0, ""))
		if rec.Code != tc.want || covAuErrorBody(t, rec) != tc.errMsg {
			t.Fatalf("apply %s: got %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	for _, tc := range []struct {
		name, id string
		want     int
	}{
		{"bad id", "x", http.StatusBadRequest},
		{"missing series", "9999", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		h.ClearGenres(rec, covAuRequest(http.MethodDelete, "/", "", map[string]string{"id": tc.id}, 0, ""))
		if rec.Code != tc.want {
			t.Fatalf("clear %s: got %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	h.ApplyGenres(rec, covAuRequest(http.MethodPut, "/", `{"genres":["Space Opera"]}`, map[string]string{"id": id}, 0, ""))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"updated":1}` {
		t.Fatalf("apply: got %d %s", rec.Code, rec.Body.String())
	}
	saved, _ := env.series.GetByID(env.ctx, s.ID)
	if saved == nil || !saved.GenreOverrideSet || len(saved.GenreOverride) != 1 || saved.GenreOverride[0] != "Space Opera" {
		t.Fatalf("override not stored: %+v", saved)
	}

	rec = httptest.NewRecorder()
	h.ClearGenres(rec, covAuRequest(http.MethodDelete, "/", "", map[string]string{"id": id}, 0, ""))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"cleared":true}` {
		t.Fatalf("clear: got %d %s", rec.Code, rec.Body.String())
	}
	saved, _ = env.series.GetByID(env.ctx, s.ID)
	if saved == nil || saved.GenreOverrideSet {
		t.Fatalf("override not cleared: %+v", saved)
	}
	// Clearing the policy leaves the book's applied genres alone (#1709).
	b, _ := env.books.GetByID(env.ctx, book.ID)
	if b == nil || len(b.Genres) != 1 || b.Genres[0] != "Space Opera" || !b.IsFieldLocked(models.BookFieldGenres) {
		t.Fatalf("book genres changed by clear: %+v", b)
	}

	_ = env.database.Close()
	rec = httptest.NewRecorder()
	h.ApplyGenres(rec, covAuRequest(http.MethodPut, "/", `{"genres":[]}`, map[string]string{"id": id}, 0, ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("closed db apply: got %d %s", rec.Code, rec.Body.String())
	}
}
