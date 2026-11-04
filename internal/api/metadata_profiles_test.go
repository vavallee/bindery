package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func metaProfileFixture(t *testing.T) (*MetadataProfileHandler, *db.MetadataProfileRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewMetadataProfileRepo(database)
	return NewMetadataProfileHandler(repo), repo, context.Background()
}

// TestMetaProfileCreate_DefaultsAllowedLanguages — #14 regression guard.
// When the client omits allowedLanguages, we default to "eng" rather than
// persisting an empty string that would let non-English editions slip
// through the author-refresh filter.
func TestMetaProfileCreate_DefaultsAllowedLanguages(t *testing.T) {
	h, _, _ := metaProfileFixture(t)
	body := bytes.NewBufferString(`{"name":"Default"}`)
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/metadata-profile", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var got models.MetadataProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.AllowedLanguages != "eng" {
		t.Errorf("expected default allowedLanguages=eng, got %q", got.AllowedLanguages)
	}
}

func TestMetaProfileCreate_RequiresName(t *testing.T) {
	h, _, _ := metaProfileFixture(t)
	body := bytes.NewBufferString(`{"allowedLanguages":"eng"}`)
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/metadata-profile", body))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing name, got %d", rec.Code)
	}
}

func TestMetaProfileCreate_BadBody(t *testing.T) {
	h, _, _ := metaProfileFixture(t)
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/metadata-profile", bytes.NewBufferString("not-json")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestMetaProfileList_EmptyIsArray(t *testing.T) {
	h, _, _ := metaProfileFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/metadata-profile", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if bytes.TrimSpace(rec.Body.Bytes())[0] != '[' {
		t.Errorf("expected JSON array, got %s", rec.Body.String())
	}
}

func TestMetaProfileGet_NotFound(t *testing.T) {
	h, _, _ := metaProfileFixture(t)
	req := withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/metadata-profile/999", nil), "id", "999")
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestMetaProfileUpdate_RoundTrip(t *testing.T) {
	h, repo, ctx := metaProfileFixture(t)
	p := &models.MetadataProfile{Name: "Fiction", AllowedLanguages: "eng"}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"name":"Fiction","allowedLanguages":"eng,fre","minPages":100}`)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/metadata-profile/"+strconv.FormatInt(p.ID, 10), body), "id", strconv.FormatInt(p.ID, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := repo.GetByID(ctx, p.ID)
	if got.AllowedLanguages != "eng,fre" || got.MinPages != 100 {
		t.Errorf("update did not persist, got %+v", got)
	}
}

func TestMetaProfileUpdate_NotFound(t *testing.T) {
	h, _, _ := metaProfileFixture(t)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/metadata-profile/999", bytes.NewBufferString(`{"name":"X"}`)), "id", "999")
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestMetaProfileDelete_Success(t *testing.T) {
	h, repo, ctx := metaProfileFixture(t)
	p := &models.MetadataProfile{Name: "X", AllowedLanguages: "eng"}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/metadata-profile/"+strconv.FormatInt(p.ID, 10), nil), "id", strconv.FormatInt(p.ID, 10))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
}

func putMetaProfile(t *testing.T, h *MetadataProfileHandler, id int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	sid := strconv.FormatInt(id, 10)
	rec := httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/metadataprofile/"+sid, bytes.NewBufferString(body)), "id", sid))
	return rec
}

func TestMetaProfileUpdate_PartialKeepsOtherFields(t *testing.T) {
	h, repo, ctx := metaProfileFixture(t)
	seed := &models.MetadataProfile{
		Name: "Fiction", MinPopularity: 5, MinPages: 100, MinEditionCount: 3,
		SkipMissingDate: true, SkipMissingISBN: true, SkipPartBooks: true,
		AllowedLanguages: "eng,fre", UnknownLanguageBehavior: models.UnknownLanguageFail,
	}
	if err := repo.Create(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if rec := putMetaProfile(t, h, seed.ID, `{"name":"Renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := repo.GetByID(ctx, seed.ID)
	want := *seed
	want.Name = "Renamed"
	want.CreatedAt = got.CreatedAt
	if *got != want {
		t.Errorf("partial update changed other fields:\n got  %+v\n want %+v", *got, want)
	}
}

func TestMetaProfileUpdate_RoundTripsEveryField(t *testing.T) {
	h, repo, ctx := metaProfileFixture(t)
	seed := &models.MetadataProfile{Name: "Fiction", AllowedLanguages: "eng"}
	if err := repo.Create(ctx, seed); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"Other","minPopularity":9,"minPages":250,"minEditionCount":4,"skipMissingDate":true,
		"skipMissingIsbn":true,"skipPartBooks":true,"allowedLanguages":"deu","unknownLanguageBehavior":"fail"}`
	if rec := putMetaProfile(t, h, seed.ID, body); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := repo.GetByID(ctx, seed.ID)
	want := models.MetadataProfile{
		ID: seed.ID, Name: "Other", MinPopularity: 9, MinPages: 250, MinEditionCount: 4,
		SkipMissingDate: true, SkipMissingISBN: true, SkipPartBooks: true,
		AllowedLanguages: "deu", UnknownLanguageBehavior: models.UnknownLanguageFail, CreatedAt: got.CreatedAt,
	}
	if *got != want {
		t.Errorf("round trip mismatch:\n got  %+v\n want %+v", *got, want)
	}
}

func TestMetaProfileUpdate_EmptyNameRejected(t *testing.T) {
	h, repo, ctx := metaProfileFixture(t)
	seed := &models.MetadataProfile{Name: "Fiction", AllowedLanguages: "eng"}
	if err := repo.Create(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if rec := putMetaProfile(t, h, seed.ID, `{"name":""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rec.Code)
	}
}
