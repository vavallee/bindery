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
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

func importListFixture(t *testing.T) (*ImportListHandler, *db.ImportListRepo, *db.SettingsRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	importLists := db.NewImportListRepo(database)
	settings := db.NewSettingsRepo(database)
	return NewImportListHandler(importLists, settings, nil), importLists, settings
}

func TestImportListList_Empty(t *testing.T) {
	h, _, _ := importListFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/import-list", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// Expect [] not null so the UI can render.
	if bytes.TrimSpace(rec.Body.Bytes())[0] != '[' {
		t.Errorf("expected JSON array, got %s", rec.Body.String())
	}
}

func TestImportListMediaType(t *testing.T) {
	h, repo, _ := importListFixture(t)
	ctx := context.Background()

	// Create with a valid media type persists and round-trips.
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list",
		bytes.NewBufferString(`{"name":"Audiobooks","type":"hardcover","url":"abs","mediaType":"audiobook"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.ImportList
	json.NewDecoder(rec.Body).Decode(&created)
	if created.MediaType != models.MediaTypeAudiobook {
		t.Fatalf("created mediaType = %q, want audiobook", created.MediaType)
	}
	stored, _ := repo.GetByID(ctx, created.ID)
	if stored == nil || stored.MediaType != models.MediaTypeAudiobook {
		t.Fatalf("stored mediaType = %+v, want audiobook", stored)
	}

	// Create with an invalid media type is rejected.
	rec = httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list",
		bytes.NewBufferString(`{"name":"Bad","type":"hardcover","mediaType":"paperback"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create invalid mediaType: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// Patch updates the media type.
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/1",
			bytes.NewBufferString(`{"mediaType":"both"}`)),
		"id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch mediaType: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, _ = repo.GetByID(ctx, created.ID)
	if stored.MediaType != models.MediaTypeBoth {
		t.Fatalf("patched mediaType = %q, want both", stored.MediaType)
	}

	// Patch with an invalid media type is rejected.
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/1",
			bytes.NewBufferString(`{"mediaType":"nonsense"}`)),
		"id", "1"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patch invalid mediaType: expected 400, got %d", rec.Code)
	}
}

func TestImportListCRUD(t *testing.T) {
	h, _, _ := importListFixture(t)

	// Create (name-only; Type should default to "csv")
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list",
		bytes.NewBufferString(`{"name":"My CSV","url":""}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.ImportList
	json.NewDecoder(rec.Body).Decode(&created)
	if created.ID == 0 || created.Type != "csv" {
		t.Fatalf("unexpected created list: %+v", created)
	}

	// Get
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/import-list/1", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", rec.Code)
	}

	// Get — bad id
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/import-list/abc", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("get bad id: expected 400, got %d", rec.Code)
	}

	// Get — missing
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/import-list/999", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("get missing: expected 404, got %d", rec.Code)
	}

	// Update
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/1",
			bytes.NewBufferString(`{"name":"Renamed","type":"csv"}`)),
		"id", "1"))
	if rec.Code != http.StatusOK {
		t.Errorf("update: expected 200, got %d", rec.Code)
	}

	// Update — bad id
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/abc", bytes.NewBufferString(`{}`)),
		"id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("update bad id: expected 400, got %d", rec.Code)
	}

	// Update — missing
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/999", bytes.NewBufferString(`{"name":"x"}`)),
		"id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("update missing: expected 404, got %d", rec.Code)
	}

	// Update — invalid body on existing row
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/1", bytes.NewBufferString(`not-json`)),
		"id", "1"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("update bad body: expected 400, got %d", rec.Code)
	}

	// Delete
	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/import-list/1", nil), "id", "1"))
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", rec.Code)
	}

	// Delete — bad id
	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/import-list/abc", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("delete bad id: expected 400, got %d", rec.Code)
	}
}

func TestImportListCreate_Validation(t *testing.T) {
	h, _, _ := importListFixture(t)
	for _, tc := range []struct {
		body string
		desc string
	}{
		{`not-json`, "invalid json"},
		{`{}`, "missing name"},
	} {
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list", bytes.NewBufferString(tc.body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", tc.desc, rec.Code)
		}
	}
}

func TestImportListExclusions(t *testing.T) {
	h, _, _ := importListFixture(t)

	// List empty
	rec := httptest.NewRecorder()
	h.ListExclusions(rec, httptest.NewRequest(http.MethodGet, "/api/v1/import-list/exclusion", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", rec.Code)
	}
	if bytes.TrimSpace(rec.Body.Bytes())[0] != '[' {
		t.Errorf("expected JSON array, got %s", rec.Body.String())
	}

	// Create — validation
	rec = httptest.NewRecorder()
	h.CreateExclusion(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list/exclusion",
		bytes.NewBufferString(`not-json`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid json: expected 400, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.CreateExclusion(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list/exclusion",
		bytes.NewBufferString(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing foreignId: expected 400, got %d", rec.Code)
	}

	// Create — success
	rec = httptest.NewRecorder()
	h.CreateExclusion(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list/exclusion",
		bytes.NewBufferString(`{"foreignId":"OL1A","title":"T","authorName":"A"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Delete
	rec = httptest.NewRecorder()
	h.DeleteExclusion(rec, withURLParam(
		httptest.NewRequest(http.MethodDelete, "/api/v1/import-list/exclusion/1", nil), "id", "1"))
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", rec.Code)
	}

	// Delete — bad id
	rec = httptest.NewRecorder()
	h.DeleteExclusion(rec, withURLParam(
		httptest.NewRequest(http.MethodDelete, "/api/v1/import-list/exclusion/abc", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: expected 400, got %d", rec.Code)
	}
}

func TestImportListSecretsAreWriteOnlyAndPreservedOnUpdate(t *testing.T) {
	h, repo, _ := importListFixture(t)
	ctx := context.Background()

	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/import-list",
		bytes.NewBufferString(`{"name":"Want","type":"hardcover","url":"want-to-read","apiKey":"Bearer hc-secret","enabled":true}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.ImportList
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.APIKey != "" || !created.APIKeyConfigured {
		t.Fatalf("create response leaked or missed secret state: %+v", created)
	}
	stored, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("get stored: %v", err)
	}
	if stored == nil || stored.APIKey != "hc-secret" {
		t.Fatalf("stored token = %+v, want normalized hc-secret", stored)
	}

	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/1", bytes.NewBufferString(`{"enabled":false}`)),
		"id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated models.ImportList
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updated.APIKey != "" || !updated.APIKeyConfigured || updated.Enabled {
		t.Fatalf("update response = %+v, want hidden configured token and disabled list", updated)
	}
	stored, err = repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("get stored after update: %v", err)
	}
	if stored == nil || stored.APIKey != "hc-secret" {
		t.Fatalf("stored token after omitted apiKey = %+v, want preserved hc-secret", stored)
	}

	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/import-list/1", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("get redacted: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var full models.ImportList
	if err := json.NewDecoder(rec.Body).Decode(&full); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	full.Name = "Want Round Trip"
	body, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal full round trip: %v", err)
	}
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/1", bytes.NewReader(body)),
		"id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("full round-trip update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode full round-trip update: %v", err)
	}
	if updated.APIKey != "" || !updated.APIKeyConfigured || updated.Name != "Want Round Trip" {
		t.Fatalf("full round-trip response = %+v, want hidden configured token and renamed list", updated)
	}
	stored, err = repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("get stored after full round trip: %v", err)
	}
	if stored == nil || stored.APIKey != "hc-secret" {
		t.Fatalf("stored token after full round trip = %+v, want preserved hc-secret", stored)
	}

	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/1", bytes.NewBufferString(`{"apiKey":""}`)),
		"id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("empty token update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode empty token update: %v", err)
	}
	if updated.APIKey != "" || !updated.APIKeyConfigured {
		t.Fatalf("empty token response = %+v, want hidden configured token", updated)
	}
	stored, err = repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("get stored after empty token update: %v", err)
	}
	if stored == nil || stored.APIKey != "hc-secret" {
		t.Fatalf("stored token after empty token update = %+v, want preserved hc-secret", stored)
	}

	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/1", bytes.NewBufferString(`{"clearApiKey":true,"apiKey":"new-token"}`)),
		"id", "1"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("conflicting token update: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, err = repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("get stored after conflicting token update: %v", err)
	}
	if stored == nil || stored.APIKey != "hc-secret" {
		t.Fatalf("stored token after conflicting token update = %+v, want preserved hc-secret", stored)
	}

	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/import-list/1", bytes.NewBufferString(`{"clearApiKey":true}`)),
		"id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("clear token: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode clear: %v", err)
	}
	if updated.APIKeyConfigured {
		t.Fatalf("clear response still reports token configured: %+v", updated)
	}
	stored, err = repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("get stored after clear: %v", err)
	}
	if stored == nil || stored.APIKey != "" {
		t.Fatalf("stored token after explicit clear = %+v, want empty", stored)
	}
}

func TestHardcoverListsUsesSavedTokenForAdmins(t *testing.T) {
	h, _, settings := importListFixture(t)
	ctx := auth.WithUserRole(context.Background(), "admin")
	if err := settings.Set(ctx, SettingHardcoverAPIToken, "Bearer global-token"); err != nil {
		t.Fatalf("set token: %v", err)
	}
	var gotToken string
	h.hcListClient = func(token string) hardcoverUserListClient {
		gotToken = token
		return fakeHardcoverUserListClient{lists: []hardcover.HCList{{ID: -1, Name: "Want to Read", Slug: "want-to-read"}}}
	}

	rec := httptest.NewRecorder()
	h.HardcoverLists(rec, httptest.NewRequest(http.MethodGet, "/api/v1/importlist/hardcover/lists", nil).WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotToken != "global-token" {
		t.Fatalf("token = %q, want normalized saved global-token", gotToken)
	}
}

func TestHardcoverListsUsesSavedTokenInDisabledAuthMode(t *testing.T) {
	h, _, settings := importListFixture(t)
	ctx := context.Background()
	if err := settings.Set(ctx, SettingAuthMode, string(auth.ModeDisabled)); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settings.Set(ctx, SettingHardcoverAPIToken, "global-token"); err != nil {
		t.Fatalf("set token: %v", err)
	}
	var gotToken string
	h.hcListClient = func(token string) hardcoverUserListClient {
		gotToken = token
		return fakeHardcoverUserListClient{lists: []hardcover.HCList{{ID: -1, Name: "Want to Read", Slug: "want-to-read"}}}
	}

	rec := httptest.NewRecorder()
	h.HardcoverLists(rec, httptest.NewRequest(http.MethodGet, "/api/v1/importlist/hardcover/lists", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotToken != "global-token" {
		t.Fatalf("token = %q, want global-token", gotToken)
	}
}

func TestHardcoverListsUsesSavedTokenForLocalOnlyLocalRequests(t *testing.T) {
	h, _, settings := importListFixture(t)
	ctx := context.Background()
	if err := settings.Set(ctx, SettingAuthMode, string(auth.ModeLocalOnly)); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settings.Set(ctx, SettingHardcoverAPIToken, "global-token"); err != nil {
		t.Fatalf("set token: %v", err)
	}
	var gotToken string
	h.hcListClient = func(token string) hardcoverUserListClient {
		gotToken = token
		return fakeHardcoverUserListClient{lists: []hardcover.HCList{{ID: -1, Name: "Want to Read", Slug: "want-to-read"}}}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/importlist/hardcover/lists", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	h.HardcoverLists(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotToken != "global-token" {
		t.Fatalf("token = %q, want global-token", gotToken)
	}
}

func TestHardcoverListsSavedTokenRequiresAdmin(t *testing.T) {
	h, _, settings := importListFixture(t)
	if err := settings.Set(context.Background(), SettingHardcoverAPIToken, "global-token"); err != nil {
		t.Fatalf("set token: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/importlist/hardcover/lists", nil).
		WithContext(auth.WithUserRole(context.Background(), "user"))
	h.HardcoverLists(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHardcoverListsHeaderOverrideDoesNotRequireAdmin(t *testing.T) {
	h, _, _ := importListFixture(t)
	var gotToken string
	h.hcListClient = func(token string) hardcoverUserListClient {
		gotToken = token
		return fakeHardcoverUserListClient{lists: []hardcover.HCList{{ID: 42, Name: "Favorites", Slug: "favorites"}}}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/importlist/hardcover/lists", nil).
		WithContext(auth.WithUserRole(context.Background(), "user"))
	req.Header.Set("Authorization", "Bearer override-token")

	rec := httptest.NewRecorder()
	h.HardcoverLists(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotToken != "override-token" {
		t.Fatalf("token = %q, want override-token", gotToken)
	}
}

type fakeHardcoverUserListClient struct {
	lists []hardcover.HCList
	err   error
}

func (f fakeHardcoverUserListClient) GetUserLists(context.Context) ([]hardcover.HCList, error) {
	return f.lists, f.err
}
