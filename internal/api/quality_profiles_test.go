package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func qualityProfileFixture(t *testing.T) (*QualityProfileHandler, *db.QualityProfileRepo, *sql.DB, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewQualityProfileRepo(database)
	return NewQualityProfileHandler(repo), repo, database, context.Background()
}

func validProfileBody(name string) string {
	return `{
		"name": "` + name + `",
		"upgradeAllowed": true,
		"cutoff": "epub",
		"items": [
			{"quality":"pdf","allowed":false},
			{"quality":"mobi","allowed":true},
			{"quality":"epub","allowed":true},
			{"quality":"azw3","allowed":true}
		]
	}`
}

func TestQualityProfileList_Empty(t *testing.T) {
	h, _, _, _ := qualityProfileFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/qualityprofile", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if bytes.TrimSpace(rec.Body.Bytes())[0] != '[' {
		t.Errorf("expected JSON array, got %s", rec.Body.String())
	}
}

func TestQualityProfileGet(t *testing.T) {
	h, _, _, _ := qualityProfileFixture(t)

	// Bad id
	rec := httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/qualityprofile/abc", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: expected 400, got %d", rec.Code)
	}

	// Missing
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/qualityprofile/999", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing: expected 404, got %d", rec.Code)
	}
}

func TestQualityProfileCreate_Success(t *testing.T) {
	h, _, _, _ := qualityProfileFixture(t)
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/qualityprofile",
		bytes.NewBufferString(validProfileBody("Custom"))))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var got models.QualityProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID == 0 || got.Name != "Custom" || got.Cutoff != "epub" || len(got.Items) != 4 {
		t.Errorf("unexpected create response: %+v", got)
	}
}

func TestQualityProfileCreate_BadBody(t *testing.T) {
	h, _, _, _ := qualityProfileFixture(t)
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/qualityprofile",
		bytes.NewBufferString("not-json")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestQualityProfileCreate_RequiresName(t *testing.T) {
	h, _, _, _ := qualityProfileFixture(t)
	body := `{"name":"  ","cutoff":"epub","items":[{"quality":"epub","allowed":true}]}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/qualityprofile", bytes.NewBufferString(body)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for blank name, got %d", rec.Code)
	}
}

func TestQualityProfileCreate_RequiresAllowedFormat(t *testing.T) {
	h, _, _, _ := qualityProfileFixture(t)
	body := `{"name":"X","cutoff":"epub","items":[{"quality":"epub","allowed":false}]}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/qualityprofile", bytes.NewBufferString(body)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when no format is allowed, got %d", rec.Code)
	}
}

func TestQualityProfileCreate_CutoffMustBeAllowed(t *testing.T) {
	h, _, _, _ := qualityProfileFixture(t)
	body := `{"name":"X","cutoff":"pdf","items":[{"quality":"pdf","allowed":false},{"quality":"epub","allowed":true}]}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/qualityprofile", bytes.NewBufferString(body)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when cutoff is not in allowed list, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestQualityProfileCreate_NoDuplicateFormats(t *testing.T) {
	h, _, _, _ := qualityProfileFixture(t)
	body := `{"name":"X","cutoff":"epub","items":[{"quality":"epub","allowed":true},{"quality":"epub","allowed":true}]}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/qualityprofile", bytes.NewBufferString(body)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for duplicate format, got %d", rec.Code)
	}
}

func TestQualityProfileCreate_DuplicateName(t *testing.T) {
	h, _, _, _ := qualityProfileFixture(t)
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/qualityprofile",
		bytes.NewBufferString(validProfileBody("Dupe"))))
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: expected 201, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/qualityprofile",
		bytes.NewBufferString(validProfileBody("Dupe"))))
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for duplicate name, got %d", rec.Code)
	}
}

func TestQualityProfileUpdate_RoundTrip(t *testing.T) {
	h, repo, _, ctx := qualityProfileFixture(t)
	p := &models.QualityProfile{
		Name: "Original", UpgradeAllowed: true, Cutoff: "epub",
		Items: []models.QualityItem{
			{Quality: "epub", Allowed: true},
		},
	}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	body := `{
		"name":"Renamed","upgradeAllowed":false,"cutoff":"mobi",
		"items":[{"quality":"epub","allowed":true},{"quality":"mobi","allowed":true}]
	}`
	idStr := strconv.FormatInt(p.ID, 10)
	rec := httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/qualityprofile/"+idStr, bytes.NewBufferString(body)), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := repo.GetByID(ctx, p.ID)
	if got.Name != "Renamed" || got.UpgradeAllowed || got.Cutoff != "mobi" || len(got.Items) != 2 {
		t.Errorf("update did not persist as expected: %+v", got)
	}
}

func TestQualityProfileUpdate_NotFound(t *testing.T) {
	h, _, _, _ := qualityProfileFixture(t)
	rec := httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/qualityprofile/999",
		bytes.NewBufferString(validProfileBody("X"))), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestQualityProfileUpdate_RejectsInvalid(t *testing.T) {
	h, repo, _, ctx := qualityProfileFixture(t)
	p := &models.QualityProfile{Name: "X", Cutoff: "epub", Items: []models.QualityItem{{Quality: "epub", Allowed: true}}}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	idStr := strconv.FormatInt(p.ID, 10)
	body := `{"name":"X","cutoff":"pdf","items":[{"quality":"epub","allowed":true}]}`
	rec := httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/qualityprofile/"+idStr, bytes.NewBufferString(body)), "id", idStr))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestQualityProfileDelete_Success(t *testing.T) {
	h, repo, _, ctx := qualityProfileFixture(t)
	p := &models.QualityProfile{Name: "Tmp", Cutoff: "epub", Items: []models.QualityItem{{Quality: "epub", Allowed: true}}}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	idStr := strconv.FormatInt(p.ID, 10)
	rec := httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/qualityprofile/"+idStr, nil), "id", idStr))
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := repo.GetByID(ctx, p.ID)
	if got != nil {
		t.Errorf("expected profile gone, got %+v", got)
	}
}

func TestQualityProfileDelete_NotFound(t *testing.T) {
	h, _, _, _ := qualityProfileFixture(t)
	rec := httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/qualityprofile/999", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestQualityProfileDelete_InUse — deleting a profile that an author still
// references must 409 with a useful body, not silently strand the author with
// a dangling FK-less reference.
func TestQualityProfileDelete_InUse(t *testing.T) {
	h, repo, database, ctx := qualityProfileFixture(t)
	p := &models.QualityProfile{Name: "InUse", Cutoff: "epub", Items: []models.QualityItem{{Quality: "epub", Allowed: true}}}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	// An author row referencing the profile: bypass AuthorRepo, all we need
	// is the FK column populated so the delete-in-use guard trips.
	if _, err := database.ExecContext(ctx,
		`INSERT INTO authors (foreign_id, name, sort_name, quality_profile_id) VALUES (?, ?, ?, ?)`,
		"test:author:1", "Alice", "alice", p.ID); err != nil {
		t.Fatal(err)
	}

	idStr := strconv.FormatInt(p.ID, 10)
	rec := httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/qualityprofile/"+idStr, nil), "id", idStr))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error       string   `json:"error"`
		AuthorCount int      `json:"authorCount"`
		AuthorNames []string `json:"authorNames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AuthorCount != 1 {
		t.Errorf("expected authorCount=1, got %d", body.AuthorCount)
	}
	if len(body.AuthorNames) != 1 || body.AuthorNames[0] != "Alice" {
		t.Errorf("expected authorNames=[Alice], got %v", body.AuthorNames)
	}
	if !strings.Contains(body.Error, "Alice") {
		t.Errorf("expected error message to mention Alice, got %q", body.Error)
	}
}
