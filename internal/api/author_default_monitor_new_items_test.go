package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// Tests for author.default_monitor_new_items (#3958): the install wide default
// for "Monitor newly discovered books". Every create path that names no value
// has to read it, the lesson of #1666 and #1667.

type newItemsFixture struct {
	h        *AuthorHandler
	authors  *db.AuthorRepo
	settings *db.SettingsRepo
}

func newNewItemsFixture(t *testing.T) newItemsFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	authors := db.NewAuthorRepo(database)
	settings := db.NewSettingsRepo(database)
	provider := &searchableAuthorProvider{
		stubMetaProvider: stubMetaProvider{name: "openlibrary"},
		searchAuthorsByQuery: map[string][]models.Author{
			"New Items": {{Name: "New Items", ForeignID: "OL-MNI-A"}},
		},
		authors: map[string]*models.Author{
			"OL-MNI-A": {ForeignID: "OL-MNI-A", Name: "New Items", SortName: "Items, New", MetadataProvider: "openlibrary"},
		},
	}
	h := NewAuthorHandler(authors, db.NewAuthorAliasRepo(database), db.NewBookRepo(database), nil,
		metadata.NewAggregator(provider), settings, db.NewMetadataProfileRepo(database), nil)
	return newItemsFixture{h: h, authors: authors, settings: settings}
}

func (f newItemsFixture) post(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body["foreignAuthorId"] = "OL-MNI-A"
	body["authorName"] = "New Items"
	body["monitored"] = true
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	f.h.Create(rec, req)
	return rec
}

func (f newItemsFixture) stored(t *testing.T) *models.Author {
	t.Helper()
	got, err := f.authors.GetByForeignID(context.Background(), "OL-MNI-A")
	if err != nil || got == nil {
		t.Fatalf("fetch author: %v, got=%+v", err, got)
	}
	return got
}

func TestCreateAuthor_DefaultMonitorNewItems(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting string
		request any // nil means the field is omitted
		want    string
		status  int
	}{
		{"unset keeps all", "", nil, models.AuthorMonitorNewItemsAll, http.StatusCreated},
		{"default all", "all", nil, models.AuthorMonitorNewItemsAll, http.StatusCreated},
		{"default none applies when omitted", "none", nil, models.AuthorMonitorNewItemsNone, http.StatusCreated},
		{"request all overrides default none", "none", "all", models.AuthorMonitorNewItemsAll, http.StatusCreated},
		{"request none overrides default all", "all", "none", models.AuthorMonitorNewItemsNone, http.StatusCreated},
		{"stored garbage falls back to all", "bogus", nil, models.AuthorMonitorNewItemsAll, http.StatusCreated},
		{"invalid request value is refused", "none", "sometimes", "", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNewItemsFixture(t)
			if tc.setting != "" {
				if err := f.settings.Set(context.Background(), SettingAuthorDefaultMonitorNewItems, tc.setting); err != nil {
					t.Fatal(err)
				}
			}
			body := map[string]any{}
			if tc.request != nil {
				body["monitorNewItems"] = tc.request
			}
			rec := f.post(t, body)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status != http.StatusCreated {
				return
			}
			if got := f.stored(t).MonitorNewItems; got != tc.want {
				t.Errorf("monitorNewItems = %q, want %q", got, tc.want)
			}
		})
	}
}

// Changing the default never touches an author that already exists.
func TestDefaultMonitorNewItems_LeavesExistingAuthorsAlone(t *testing.T) {
	f := newNewItemsFixture(t)
	ctx := context.Background()
	existing := &models.Author{ForeignID: "OL-OLD-A", Name: "Old Author", SortName: "Author, Old", MonitorNewItems: models.AuthorMonitorNewItemsAll}
	if err := f.authors.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Set(ctx, SettingAuthorDefaultMonitorNewItems, "none"); err != nil {
		t.Fatal(err)
	}
	if rec := f.post(t, map[string]any{}); rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got, err := f.authors.GetByID(ctx, existing.ID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.MonitorNewItems != models.AuthorMonitorNewItemsAll {
		t.Errorf("existing author monitorNewItems = %q, want it untouched (all)", got.MonitorNewItems)
	}
}

// Adding an author that matches one already in the library relinks the match.
// A request that names no value keeps the author's own policy rather than
// resetting it to the default, and an explicit value still wins.
func TestCreateAuthor_RelinkKeepsOwnMonitorNewItems(t *testing.T) {
	for _, tc := range []struct {
		name    string
		own     string
		setting string
		request any
		want    string
	}{
		{"own none survives default all", "none", "all", nil, "none"},
		{"own all survives default none", "all", "none", nil, "all"},
		{"explicit request wins", "all", "all", "none", "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNewItemsFixture(t)
			ctx := context.Background()
			existing := &models.Author{ForeignID: "abs:new-items", Name: "New Items", SortName: "Items, New", MonitorNewItems: tc.own}
			if err := f.authors.Create(ctx, existing); err != nil {
				t.Fatal(err)
			}
			if err := f.settings.Set(ctx, SettingAuthorDefaultMonitorNewItems, tc.setting); err != nil {
				t.Fatal(err)
			}
			body := map[string]any{}
			if tc.request != nil {
				body["monitorNewItems"] = tc.request
			}
			rec := f.post(t, body)
			if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			got, err := f.authors.GetByID(ctx, existing.ID)
			if err != nil || got == nil {
				t.Fatalf("author %d: %v", existing.ID, err)
			}
			if got.ForeignID != "OL-MNI-A" {
				t.Fatalf("foreign id %q, the add should have relinked the match", got.ForeignID)
			}
			if got.MonitorNewItems != tc.want {
				t.Errorf("monitorNewItems = %q, want %q", got.MonitorNewItems, tc.want)
			}
		})
	}
}

// Add book creates the author as a side effect and names no policy, so it
// takes the default (#3958).
func TestAddBookCore_NewAuthorTakesDefaultMonitorNewItems(t *testing.T) {
	for _, tc := range []struct{ setting, want string }{
		{"", "all"},
		{"none", "none"},
	} {
		t.Run("default "+tc.setting, func(t *testing.T) {
			database, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			database.SetMaxOpenConns(1)
			authors := db.NewAuthorRepo(database)
			settings := db.NewSettingsRepo(database)
			if tc.setting != "" {
				if err := settings.Set(context.Background(), SettingAuthorDefaultMonitorNewItems, tc.setting); err != nil {
					t.Fatal(err)
				}
			}
			h := NewAuthorHandler(authors, nil, db.NewBookRepo(database), nil,
				metadata.NewAggregator(addBookBackCatalogueStub(false)), settings, db.NewMetadataProfileRepo(database), nil)
			p := wellsAddBookParams
			p.SkipCatalogueSync = true
			if _, err := h.addBookCore(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			got, err := authors.GetByForeignID(context.Background(), "OL39307A")
			if err != nil || got == nil {
				t.Fatalf("author: %v %+v", err, got)
			}
			if got.MonitorNewItems != tc.want {
				t.Errorf("monitorNewItems = %q, want %q", got.MonitorNewItems, tc.want)
			}
		})
	}
}

// A non admin cannot write the setting, though they can read it for the add
// author dialog.
func TestSettings_DefaultMonitorNewItems(t *testing.T) {
	h, repo, ctx := settingsFixture(t)

	put := func(value string) *httptest.ResponseRecorder {
		req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+SettingAuthorDefaultMonitorNewItems, bytes.NewBufferString(`{"value":"`+value+`"}`)), SettingAuthorDefaultMonitorNewItems)
		rec := httptest.NewRecorder()
		h.Set(rec, req)
		return rec
	}
	for _, ok := range []string{"all", "none", ""} {
		if rec := put(ok); rec.Code != http.StatusOK {
			t.Errorf("PUT %q = %d, want 200: %s", ok, rec.Code, rec.Body.String())
		}
	}
	for _, bad := range []string{"sometimes", "future", "ALL"} {
		if rec := put(bad); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %q = %d, want 400", bad, rec.Code)
		}
	}

	if rec := put("none"); rec.Code != http.StatusOK {
		t.Fatalf("PUT none = %d", rec.Code)
	}
	gated := auth.RequireAdmin(http.HandlerFunc(h.Set))
	req := userReq(withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+SettingAuthorDefaultMonitorNewItems, bytes.NewBufferString(`{"value":"all"}`)), SettingAuthorDefaultMonitorNewItems))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PUT as non admin = %d, want 403", rec.Code)
	}
	if got, _ := repo.Get(ctx, SettingAuthorDefaultMonitorNewItems); got == nil || got.Value != "none" {
		t.Fatalf("non admin PUT changed the value: %+v", got)
	}
}
