package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// covAuSearchProvider answers SearchAuthors with a fixed list (or error) for
// every query and GetAuthor from a map (or error), so the relink and create
// paths can be driven without a real provider.
type covAuSearchProvider struct {
	stubMetaProvider
	results   []models.Author
	searchErr error
	byID      map[string]*models.Author
	getErr    error
}

func (p *covAuSearchProvider) SearchAuthors(context.Context, string) ([]models.Author, error) {
	if p.searchErr != nil {
		return nil, p.searchErr
	}
	return p.results, nil
}

func (p *covAuSearchProvider) GetAuthor(_ context.Context, foreignID string) (*models.Author, error) {
	if p.getErr != nil {
		return nil, p.getErr
	}
	if a, ok := p.byID[foreignID]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, nil
}

func covAuHandler(env *covAuEnv, meta *metadata.Aggregator) *AuthorHandler {
	return NewAuthorHandler(env.authors, env.aliases, env.books, env.series, meta, nil, nil, nil)
}

func covAuDecodeList(t *testing.T, rec *httptest.ResponseRecorder) authorListResponse {
	t.Helper()
	var out authorListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestAuthorListCoverage(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	env := covAuSetup(t)
	unmonitored := &models.Author{ForeignID: "OL-ALICE-B", Name: "Quiet Alice", SortName: "Alice, Quiet", Monitored: false}
	if err := env.authors.CreateForUser(env.ctx, unmonitored, env.alice); err != nil {
		t.Fatal(err)
	}
	h := covAuHandler(env, nil)

	names := func(resp authorListResponse) []string {
		out := make([]string, 0, len(resp.Items))
		for _, a := range resp.Items {
			out = append(out, a.Name)
		}
		return out
	}

	t.Run("non-admin sees only own authors", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/api/v1/author", "", nil, env.bob, "user"))
		resp := covAuDecodeList(t, rec)
		if rec.Code != http.StatusOK || resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].ID != env.bobA.ID {
			t.Fatalf("bob list: %d %v total=%d", rec.Code, names(resp), resp.Total)
		}
	})
	t.Run("admin sees everyone", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/api/v1/author", "", nil, env.bob, "admin"))
		if resp := covAuDecodeList(t, rec); resp.Total != 3 {
			t.Fatalf("admin list: %v total=%d", names(resp), resp.Total)
		}
	})
	t.Run("monitored=true filter", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/api/v1/author?monitored=true", "", nil, env.alice, "user"))
		resp := covAuDecodeList(t, rec)
		if resp.Total != 1 || resp.Items[0].ID != env.aliceA.ID {
			t.Fatalf("monitored=true: %v", names(resp))
		}
	})
	t.Run("monitored=false filter", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/api/v1/author?monitored=false", "", nil, env.alice, "user"))
		resp := covAuDecodeList(t, rec)
		if resp.Total != 1 || resp.Items[0].ID != unmonitored.ID {
			t.Fatalf("monitored=false: %v", names(resp))
		}
	})
	t.Run("search with no hits is an empty array", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/api/v1/author?search=zzzz", "", nil, env.alice, "user"))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
			t.Fatalf("empty search: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("repo error is 500", func(t *testing.T) {
		_ = env.database.Close()
		rec := httptest.NewRecorder()
		h.List(rec, covAuRequest(http.MethodGet, "/api/v1/author", "", nil, env.alice, "user"))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestAuthorGetCoverage(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	env := covAuSetup(t)
	h := covAuHandler(env, nil)
	if err := env.aliases.Create(env.ctx, &models.AuthorAlias{AuthorID: env.aliceA.ID, Name: "A. Writer"}); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-ALICE-S1", AuthorID: env.aliceA.ID, Title: "Saga One", Status: models.BookStatusWanted}
	if err := env.books.Create(env.ctx, book); err != nil {
		t.Fatal(err)
	}
	s, err := env.series.CreateManual(env.ctx, "Saga")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.series.UpsertBookLink(env.ctx, s.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	env.aliceA.MonitorMode = models.AuthorMonitorModeSeries
	if err := env.authors.Update(env.ctx, env.aliceA); err != nil {
		t.Fatal(err)
	}
	if err := env.authors.SetMonitoredSeriesIDs(env.ctx, env.aliceA.ID, []int64{s.ID}); err != nil {
		t.Fatal(err)
	}
	id := covAuID(env.aliceA.ID)

	rec := httptest.NewRecorder()
	h.Get(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": "x"}, env.alice, "user"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.Get(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": id}, env.bob, "user"))
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "Alice Writer") {
		t.Fatalf("cross-user get: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.Get(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": id}, env.alice, "user"))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner get: %d %s", rec.Code, rec.Body.String())
	}
	var got models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Aliases) != 1 || got.Aliases[0].Name != "A. Writer" {
		t.Fatalf("aliases not attached: %+v", got.Aliases)
	}
	if len(got.MonitoredSeriesIDs) != 1 || got.MonitoredSeriesIDs[0] != s.ID {
		t.Fatalf("series pins not attached: %+v", got.MonitoredSeriesIDs)
	}
	if len(got.Books) != 1 || got.Books[0].ID != book.ID {
		t.Fatalf("books not attached: %+v", got.Books)
	}

	_ = env.database.Close()
	rec = httptest.NewRecorder()
	h.Get(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": id}, env.alice, "user"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closed db: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuthorListSeriesCoverage(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	env := covAuSetup(t)

	noSeries := NewAuthorHandler(env.authors, env.aliases, env.books, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	noSeries.ListSeries(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": covAuID(env.aliceA.ID)}, env.alice, "user"))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("nil series repo: %d %q", rec.Code, rec.Body.String())
	}

	h := covAuHandler(env, nil)
	rec = httptest.NewRecorder()
	h.ListSeries(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": covAuID(env.aliceA.ID)}, env.bob, "user"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuthorUpdateCoverage(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	env := covAuSetup(t)
	h := covAuHandler(env, nil)
	id := covAuID(env.aliceA.ID)
	update := func(user int64, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.Update(rec, covAuRequest(http.MethodPut, "/", body, map[string]string{"id": id}, user, "user"))
		return rec
	}

	for _, tc := range []struct {
		name, body, errMsg string
	}{
		{"bad body", `{`, "invalid request body"},
		{"bad monitor mode", `{"monitorMode":"sometimes"}`, "monitorMode must be one of: all, future, latest, none, series"},
		{"bad monitorNewItems", `{"monitorNewItems":"some"}`, "monitorNewItems must be one of: all, none"},
		{"zero latest count", `{"monitorLatestCount":0}`, "monitorLatestCount must be a positive integer"},
	} {
		rec := update(env.alice, tc.body)
		if rec.Code != http.StatusBadRequest || covAuErrorBody(t, rec) != tc.errMsg {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}

	rec := update(env.bob, `{"monitored":false}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user update: %d %s", rec.Code, rec.Body.String())
	}
	if a, _ := env.authors.GetByID(env.ctx, env.aliceA.ID); a == nil || !a.Monitored {
		t.Fatalf("bob's rejected update changed alice's author: %+v", a)
	}

	rec = update(env.alice, `{"monitored":false,"monitorNewItems":"none","monitorLatestCount":3,"qualityProfileId":1,"metadataProfileId":1,"rootFolderId":7,"audiobookRootFolderId":8}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("full update: %d %s", rec.Code, rec.Body.String())
	}
	a, _ := env.authors.GetByID(env.ctx, env.aliceA.ID)
	if a == nil || a.Monitored || a.MonitorNewItems != "none" || a.MonitorLatestCount != 3 ||
		a.QualityProfileID == nil || *a.QualityProfileID != 1 || a.MetadataProfileID == nil || *a.MetadataProfileID != 1 ||
		a.RootFolderID == nil || *a.RootFolderID != 7 || a.AudiobookRootFolderID == nil || *a.AudiobookRootFolderID != 8 {
		t.Fatalf("update not persisted: %+v", a)
	}

	rec = update(env.alice, `{"clearAudiobookRootFolder":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear audiobook root: %d %s", rec.Code, rec.Body.String())
	}
	if a, _ := env.authors.GetByID(env.ctx, env.aliceA.ID); a == nil || a.AudiobookRootFolderID != nil || a.RootFolderID == nil {
		t.Fatalf("audiobook root not cleared alone: %+v", a)
	}

	// Series mode without a pin set in the request surfaces the stored pins.
	book := &models.Book{ForeignID: "OL-ALICE-S1", AuthorID: env.aliceA.ID, Title: "Saga One", Status: models.BookStatusWanted}
	if err := env.books.Create(env.ctx, book); err != nil {
		t.Fatal(err)
	}
	s, err := env.series.CreateManual(env.ctx, "Saga")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.series.UpsertBookLink(env.ctx, s.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if err := env.authors.SetMonitoredSeriesIDs(env.ctx, env.aliceA.ID, []int64{s.ID}); err != nil {
		t.Fatal(err)
	}
	rec = update(env.alice, `{"monitorMode":"series"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("series mode: %d %s", rec.Code, rec.Body.String())
	}
	var got models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.MonitoredSeriesIDs) != 1 || got.MonitoredSeriesIDs[0] != s.ID {
		t.Fatalf("stored pins not surfaced: %+v", got.MonitoredSeriesIDs)
	}

	// An explicit empty pin set clears the selection.
	rec = update(env.alice, `{"monitoredSeriesIds":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear pins: %d %s", rec.Code, rec.Body.String())
	}
	if ids, _ := env.authors.ListMonitoredSeriesIDs(env.ctx, env.aliceA.ID); len(ids) != 0 {
		t.Fatalf("pins not cleared: %v", ids)
	}

	_ = env.database.Close()
	rec = update(env.alice, `{"monitored":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closed db: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuthorDeleteCoverage(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	env := covAuSetup(t)
	h := covAuHandler(env, nil)

	rec := httptest.NewRecorder()
	h.Delete(rec, covAuRequest(http.MethodDelete, "/?deleteFiles=true", "", map[string]string{"id": covAuID(env.aliceA.ID)}, env.bob, "user"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user delete: %d %s", rec.Code, rec.Body.String())
	}
	if a, _ := env.authors.GetByID(env.ctx, env.aliceA.ID); a == nil {
		t.Fatal("bob deleted alice's author")
	}

	rec = httptest.NewRecorder()
	h.Delete(rec, covAuRequest(http.MethodDelete, "/", "", map[string]string{"id": covAuID(env.aliceA.ID)}, env.alice, "user"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("owner delete: %d %s", rec.Code, rec.Body.String())
	}
	if a, _ := env.authors.GetByID(env.ctx, env.aliceA.ID); a != nil {
		t.Fatal("author survived delete")
	}
}

func TestAuthorCreateCoverageValidation(t *testing.T) {
	env := covAuSetup(t)
	h := covAuHandler(env, nil)
	for _, tc := range []struct {
		name, body, errMsg string
	}{
		{"bad body", `{`, "invalid request body"},
		{"missing fields", `{"foreignAuthorId":"OL-NEW"}`, ""},
		{"bad monitor mode", `{"foreignAuthorId":"OL-NEW","authorName":"New","monitorMode":"sometimes"}`, "monitorMode must be one of: all, future, latest, none"},
		{"zero latest count", `{"foreignAuthorId":"OL-NEW","authorName":"New","monitorLatestCount":0}`, "monitorLatestCount must be a positive integer"},
		{"bad monitorNewItems", `{"foreignAuthorId":"OL-NEW","authorName":"New","monitorNewItems":"some"}`, ""},
	} {
		rec := httptest.NewRecorder()
		h.Create(rec, covAuRequest(http.MethodPost, "/", tc.body, nil, env.alice, "user"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
		if tc.errMsg != "" && covAuErrorBody(t, rec) != tc.errMsg {
			t.Fatalf("%s: error %q", tc.name, covAuErrorBody(t, rec))
		}
	}
	if a, _ := env.authors.GetByForeignID(env.ctx, "OL-NEW"); a != nil {
		t.Fatalf("a rejected create wrote an author: %+v", a)
	}
}

// TestAuthorCreateCoverageNameConflicts drives the name-resolution conflicts
// in createAuthorCore: the upstream fetch failing, returning nothing, or
// returning a nameless record all fall back to the requested name, which then
// resolves to an existing author (by name, alias, or normalised alias) and
// must answer 409 instead of creating a duplicate.
func TestAuthorCreateCoverageNameConflicts(t *testing.T) {
	type check struct {
		name      string
		provider  *covAuSearchProvider
		nilMeta   bool
		reqName   string
		wantMsg   string
		wantCanon bool
	}
	nameless := &covAuSearchProvider{byID: map[string]*models.Author{"OL-NEW": {ForeignID: "OL-NEW"}}}
	checks := []check{
		{name: "provider error falls back to name", provider: &covAuSearchProvider{getErr: errors.New("upstream down")}, reqName: "Bob Writer", wantMsg: "author name already resolves to an existing author — confirm merge", wantCanon: true},
		{name: "provider nil falls back to name", provider: &covAuSearchProvider{}, reqName: "bob writer", wantMsg: "author name already resolves to an existing author — confirm merge", wantCanon: true},
		{name: "nameless upstream record", provider: nameless, reqName: "Bob Writer", wantMsg: "author name already resolves to an existing author — confirm merge", wantCanon: true},
		{name: "no aggregator", nilMeta: true, reqName: "Pen Name", wantMsg: "author name already resolves to an existing author — confirm merge", wantCanon: true},
		{name: "normalised alias", nilMeta: true, reqName: "Pen-Name.", wantMsg: "author name already resolves to an existing author — confirm merge", wantCanon: true},
		{name: "ambiguous name", nilMeta: true, reqName: "Twin Name", wantMsg: "author name resolves ambiguously — merge manually"},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			env := covAuSetup(t)
			if err := env.aliases.Create(env.ctx, &models.AuthorAlias{AuthorID: env.bobA.ID, Name: "Pen Name"}); err != nil {
				t.Fatal(err)
			}
			for i, fid := range []string{"OL-TWIN-1", "OL-TWIN-2"} {
				twin := &models.Author{ForeignID: fid, Name: "Twin Name", SortName: "Name, Twin"}
				if i == 1 {
					twin.Name = "twin name"
				}
				if err := env.authors.Create(env.ctx, twin); err != nil {
					t.Fatal(err)
				}
			}
			var meta *metadata.Aggregator
			if !c.nilMeta {
				meta = metadata.NewAggregator(c.provider)
			}
			h := covAuHandler(env, meta)
			body, _ := json.Marshal(map[string]any{"foreignAuthorId": "OL-NEW", "authorName": c.reqName})
			rec := httptest.NewRecorder()
			h.Create(rec, covAuRequest(http.MethodPost, "/", string(body), nil, 0, ""))
			if rec.Code != http.StatusConflict || covAuErrorBody(t, rec) != c.wantMsg {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
			var resp map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			if c.wantCanon {
				if id, _ := resp["canonicalAuthorId"].(float64); int64(id) != env.bobA.ID {
					t.Fatalf("canonicalAuthorId = %v, want %d", resp["canonicalAuthorId"], env.bobA.ID)
				}
			} else if _, ok := resp["canonicalAuthorId"]; ok {
				t.Fatalf("ambiguous conflict must not name a canonical author: %s", rec.Body.String())
			}
			if a, _ := env.authors.GetByForeignID(env.ctx, "OL-NEW"); a != nil {
				t.Fatalf("conflict still created the author: %+v", a)
			}
		})
	}
}

func TestAuthorRelinkUpstreamCoverage(t *testing.T) {
	absAuthor := func() *models.Author {
		return &models.Author{ForeignID: "abs:author:jay", Name: "Jay Dee", SortName: "Dee, Jay", MetadataProvider: "audiobookshelf", Monitored: true}
	}
	jay := models.Author{ForeignID: "OL-JAY", Name: "Jay Dee", SortName: "Dee, Jay", MetadataProvider: "openlibrary"}

	type tc struct {
		name     string
		meta     func() *metadata.Aggregator
		seed     func(t *testing.T, env *covAuEnv)
		author   func() *models.Author
		body     string
		urlID    string
		want     int
		wantMsg  string
		unlinked bool
	}
	primary := func(p *covAuSearchProvider) func() *metadata.Aggregator {
		return func() *metadata.Aggregator {
			p.name = "openlibrary"
			return metadata.NewAggregator(p)
		}
	}
	cases := []tc{
		{name: "bad id", urlID: "x", want: http.StatusBadRequest, wantMsg: "invalid id"},
		{name: "missing author", urlID: "9999", want: http.StatusNotFound, wantMsg: "author not found"},
		{name: "already linked", author: func() *models.Author {
			return &models.Author{ForeignID: "OL-LINKED", Name: "Linked", SortName: "Linked", MetadataProvider: "openlibrary"}
		}, want: http.StatusConflict, wantMsg: "author is already linked to upstream metadata"},
		{name: "no aggregator", want: http.StatusFailedDependency},
		{name: "no match", meta: primary(&covAuSearchProvider{}), want: http.StatusConflict, wantMsg: "no confident upstream author match found"},
		{name: "provider error", meta: primary(&covAuSearchProvider{searchErr: errors.New("boom")}), want: http.StatusBadGateway, wantMsg: "boom"},
		{name: "primary down, enricher match", meta: func() *metadata.Aggregator {
			return metadata.NewAggregator(
				&covAuSearchProvider{stubMetaProvider: stubMetaProvider{name: "openlibrary"}, searchErr: errors.New("ol down")},
				&covAuSearchProvider{stubMetaProvider: stubMetaProvider{name: "hardcover"}, results: []models.Author{{ForeignID: "hc:jay", Name: "Jay Dee", MetadataProvider: "hardcover"}}},
			)
		}, want: http.StatusServiceUnavailable},
		{name: "matched but upstream record gone", meta: primary(&covAuSearchProvider{results: []models.Author{jay}}), want: http.StatusConflict, wantMsg: "no confident upstream author match found"},
		{name: "explicit id not found", meta: primary(&covAuSearchProvider{}), body: `{"foreignAuthorId":"OL-NOPE"}`, want: http.StatusConflict, wantMsg: "no confident upstream author match found"},
		{name: "explicit id lookup error", meta: primary(&covAuSearchProvider{getErr: errors.New("get failed")}), body: `{"foreignAuthorId":"OL-NOPE"}`, want: http.StatusBadGateway, wantMsg: "get failed"},
		{name: "upstream name already a local author", meta: primary(&covAuSearchProvider{byID: map[string]*models.Author{"OL-BOB": {ForeignID: "OL-BOB", Name: "Bob Writer"}}}),
			body: `{"foreignAuthorId":"OL-BOB"}`, want: http.StatusConflict, wantMsg: "author name already resolves to an existing author — confirm merge"},
		{name: "upstream id already local", meta: primary(&covAuSearchProvider{byID: map[string]*models.Author{"OL-BOB-A": {ForeignID: "OL-BOB-A", Name: "Robert Writer"}}}),
			body: `{"foreignAuthorId":"OL-BOB-A"}`, want: http.StatusConflict, wantMsg: "upstream author already exists locally"},
		{name: "ambiguous local name", meta: primary(&covAuSearchProvider{byID: map[string]*models.Author{"OL-TWIN": {ForeignID: "OL-TWIN", Name: "Twin Name"}}}),
			seed: func(t *testing.T, env *covAuEnv) {
				for _, fid := range []string{"OL-TWIN-1", "OL-TWIN-2"} {
					if err := env.authors.Create(env.ctx, &models.Author{ForeignID: fid, Name: "Twin Name", SortName: "Name, Twin"}); err != nil {
						t.Fatal(err)
					}
				}
			},
			body: `{"foreignAuthorId":"OL-TWIN"}`, want: http.StatusConflict, wantMsg: "author name resolves ambiguously — merge manually"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := covAuSetup(t)
			var meta *metadata.Aggregator
			if c.meta != nil {
				meta = c.meta()
			}
			if c.seed != nil {
				c.seed(t, env)
			}
			mk := absAuthor
			if c.author != nil {
				mk = c.author
			}
			target := mk()
			if err := env.authors.Create(env.ctx, target); err != nil {
				t.Fatal(err)
			}
			urlID := c.urlID
			if urlID == "" {
				urlID = covAuID(target.ID)
			}
			h := covAuHandler(env, meta)
			rec := httptest.NewRecorder()
			h.RelinkUpstream(rec, covAuRequest(http.MethodPost, "/", c.body, map[string]string{"id": urlID}, 0, ""))
			if rec.Code != c.want {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body.String(), c.want)
			}
			if c.wantMsg != "" && covAuErrorBody(t, rec) != c.wantMsg {
				t.Fatalf("error = %q, want %q", covAuErrorBody(t, rec), c.wantMsg)
			}
			after, _ := env.authors.GetByID(env.ctx, target.ID)
			if after == nil || after.ForeignID != target.ForeignID || after.Name != target.Name {
				t.Fatalf("a rejected relink changed the author: %+v", after)
			}
		})
	}

	t.Run("repo error is 500", func(t *testing.T) {
		env := covAuSetup(t)
		h := covAuHandler(env, nil)
		_ = env.database.Close()
		rec := httptest.NewRecorder()
		h.RelinkUpstream(rec, covAuRequest(http.MethodPost, "/", "", map[string]string{"id": "1"}, 0, ""))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestAuthorRelinkCandidatesCoverage(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	call := func(h *AuthorHandler, id string, user int64) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.RelinkCandidates(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": id}, user, "user"))
		return rec
	}

	env := covAuSetup(t)
	if rec := call(covAuHandler(env, nil), "x", env.alice); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := call(covAuHandler(env, nil), "9999", env.alice); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", rec.Code)
	}
	if rec := call(covAuHandler(env, nil), covAuID(env.aliceA.ID), env.bob); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(covAuHandler(env, nil), covAuID(env.aliceA.ID), env.alice); rec.Code != http.StatusFailedDependency {
		t.Fatalf("no aggregator: %d %s", rec.Code, rec.Body.String())
	}
	failing := metadata.NewAggregator(&covAuSearchProvider{searchErr: errors.New("search down")})
	if rec := call(covAuHandler(env, failing), covAuID(env.aliceA.ID), env.alice); rec.Code != http.StatusBadGateway || !strings.Contains(covAuErrorBody(t, rec), "search down") {
		t.Fatalf("search error: %d %s", rec.Code, rec.Body.String())
	}
	empty := metadata.NewAggregator(&covAuSearchProvider{})
	rec := call(covAuHandler(env, empty), covAuID(env.aliceA.ID), env.alice)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("no candidates: %d %q", rec.Code, rec.Body.String())
	}

	_ = env.database.Close()
	if rec := call(covAuHandler(env, empty), covAuID(env.aliceA.ID), env.alice); rec.Code != http.StatusInternalServerError {
		t.Fatalf("closed db: %d %s", rec.Code, rec.Body.String())
	}
}
