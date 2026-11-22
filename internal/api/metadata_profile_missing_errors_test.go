package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestSettingDefaultMetadataProfile_PutValidation(t *testing.T) {
	f := newMissingProfileFixture(t)
	sh := NewSettingsHandler(f.settings)
	put := func(value string) int {
		body, _ := json.Marshal(map[string]string{"value": value})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+SettingAuthorDefaultMetadataProfileID, bytes.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("key", SettingAuthorDefaultMetadataProfileID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		sh.Set(rec, req)
		return rec.Code
	}
	for value, want := range map[string]int{
		strconv.FormatInt(f.both.ID, 10): http.StatusOK,
		"":                               http.StatusOK,
		"9999":                           http.StatusBadRequest, // no such profile
		"1":                              http.StatusBadRequest, // the deleted seeded profile
		"0":                              http.StatusBadRequest,
		"deutsch":                        http.StatusBadRequest,
	} {
		if got := put(value); got != want {
			t.Errorf("PUT %q: status %d, want %d", value, got, want)
		}
	}
}

func TestMetadataProfileDelete_BadRequestsAndMissing(t *testing.T) {
	f := newMissingProfileFixture(t)
	h := NewMetadataProfileHandler(f.profiles)
	del := func(id, query string) int {
		rec := httptest.NewRecorder()
		h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/x"+query, nil), "id", id))
		return rec.Code
	}
	if got := del("abc", ""); got != http.StatusBadRequest {
		t.Errorf("bad id: %d, want 400", got)
	}
	if got := del("9999", ""); got != http.StatusNotFound {
		t.Errorf("missing profile: %d, want 404", got)
	}
	id := strconv.FormatInt(f.both.ID, 10)
	for _, q := range []string{"?moveAuthorsTo=abc", "?moveAuthorsTo=0", "?moveAuthorsTo=-2"} {
		if got := del(id, q); got != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, got)
		}
	}
	if p, _ := f.profiles.GetByID(context.Background(), f.both.ID); p == nil {
		t.Fatal("a rejected request deleted the profile")
	}
	// A move delete of a profile nobody uses works and names no author.
	if got := del(id, "?moveAuthorsTo="+strconv.FormatInt(f.deutsch.ID, 10)); got != http.StatusNoContent {
		t.Errorf("move delete with no users: %d, want 204", got)
	}
}

// A failing lookup is handled per consumer, and none of them substitutes a
// profile it could not read: search and the scheduler filter nothing, catalogue
// sync keeps its English fallback, reconcile reports the error, series fill
// filters nothing, and import falls through to the preferred language.
func TestMissingMetadataProfile_LookupFailurePolicy(t *testing.T) {
	f := newMissingProfileFixture(t)
	ctx := context.Background()
	if _, err := f.database.Exec("ALTER TABLE metadata_profiles RENAME TO metadata_profiles_x"); err != nil {
		t.Fatal(err)
	}

	idx := &IndexerHandler{profiles: f.profiles}
	if got := idx.resolveAllowedLanguages(ctx, f.orphan); len(got) != 0 {
		t.Errorf("search languages on a lookup failure = %v, want none", got)
	}
	if got := (&IndexerHandler{}).resolveAllowedLanguages(ctx, f.orphan); len(got) != 0 {
		t.Errorf("search languages without a repo = %v, want none", got)
	}
	ah := &AuthorHandler{profiles: f.profiles}
	if langs, fail := ah.resolveAllowedLanguages(ctx, f.orphan); len(langs) != 1 || langs[0] != "eng" || fail {
		t.Errorf("sync on a lookup failure = %v %v, want [eng] false", langs, fail)
	}
	if _, err := ah.reconciliationProfile(ctx, f.orphan); err == nil {
		t.Error("reconcile swallowed a lookup failure")
	}
	if p := effectiveMetadataProfile(ctx, f.profiles, f.orphan); p != nil {
		t.Errorf("series fill profile on a lookup failure = %+v, want nil", p)
	}
	if p := effectiveMetadataProfile(ctx, nil, f.orphan); p != nil {
		t.Errorf("series fill profile without a repo = %+v, want nil", p)
	}
}
