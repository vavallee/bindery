package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestQualityProfileUpdate_NarratorPreferences(t *testing.T) {
	h, repo, _, ctx := qualityProfileFixture(t)
	p := &models.QualityProfile{
		Name: "Audio", Cutoff: "m4b",
		Items:              []models.QualityItem{{Quality: "m4b", Allowed: true}},
		PreferredNarrators: []string{"Scott Brick"},
		EditionPreference:  models.EditionPrefPreferUnabridged,
	}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	idStr := strconv.FormatInt(p.ID, 10)
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/qualityprofile/"+idStr, bytes.NewBufferString(body)), "id", idStr))
		return rec
	}
	// A client that predates the fields must not clear them.
	if rec := put(`{"name":"Audio","cutoff":"m4b","items":[{"quality":"m4b","allowed":true}]}`); rec.Code != http.StatusOK {
		t.Fatalf("legacy PUT: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := repo.GetByID(ctx, p.ID)
	if len(got.PreferredNarrators) != 1 || got.EditionPreference != models.EditionPrefPreferUnabridged {
		t.Fatalf("legacy PUT cleared the preferences: %+v", got)
	}
	// An explicit change applies.
	if rec := put(`{"name":"Audio","cutoff":"m4b","items":[{"quality":"m4b","allowed":true}],"preferredNarrators":["A B"],"editionPreference":"require_unabridged"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d", rec.Code)
	}
	got, _ = repo.GetByID(ctx, p.ID)
	if got.EditionPreference != models.EditionPrefRequireUnabridged || got.PreferredNarrators[0] != "A B" {
		t.Fatalf("not applied: %+v", got)
	}
	// An unknown preference is a 400.
	if rec := put(`{"name":"Audio","cutoff":"m4b","items":[{"quality":"m4b","allowed":true}],"editionPreference":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown preference: %d", rec.Code)
	}
}
