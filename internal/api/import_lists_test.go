package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/hardcoverlistsyncer"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

func importListFixture(t *testing.T) (*ImportListHandler, *db.ImportListRepo, *db.SettingsRepo) {
	t.Helper()
	h, repo, settings, _ := importListFixtureWithUsers(t)
	return h, repo, settings
}

func importListFixtureWithUsers(t *testing.T) (*ImportListHandler, *db.ImportListRepo, *db.SettingsRepo, *db.UserRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	importLists := db.NewImportListRepo(database)
	settings := db.NewSettingsRepo(database)
	users := db.NewUserRepo(database)
	return NewImportListHandler(importLists, settings, nil, users), importLists, settings, users
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

func TestImportListOwner_ValidUserPersists(t *testing.T) {
	h, repo, _, users := importListFixtureWithUsers(t)
	ctx := context.Background()
	owner, err := users.Create(ctx, "alice", "hash")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	rec := httptest.NewRecorder()
	body := `{"name":"Alice WTR","type":"hardcover","url":"wtr","ownerUserId":` +
		strconv.FormatInt(owner.ID, 10) + `}`
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/importlist", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.ImportList
	json.NewDecoder(rec.Body).Decode(&created)
	if created.OwnerUserID == nil || *created.OwnerUserID != owner.ID {
		t.Fatalf("created OwnerUserID = %v, want %d", created.OwnerUserID, owner.ID)
	}
	stored, _ := repo.GetByID(ctx, created.ID)
	if stored == nil || stored.OwnerUserID == nil || *stored.OwnerUserID != owner.ID {
		t.Fatalf("stored owner = %+v, want %d", stored, owner.ID)
	}
}

func TestImportListOwner_UnknownUserRejected(t *testing.T) {
	h, _, _, _ := importListFixtureWithUsers(t)
	rec := httptest.NewRecorder()
	// No users seeded, so id 999 cannot exist.
	body := `{"name":"Bad owner","type":"hardcover","url":"x","ownerUserId":999}`
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/importlist", bytes.NewBufferString(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown owner, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportListOwner_NullIsGlobal(t *testing.T) {
	h, repo, _, _ := importListFixtureWithUsers(t)
	ctx := context.Background()
	rec := httptest.NewRecorder()
	// Explicit null owner = global; must be accepted with no users present.
	body := `{"name":"Shared","type":"hardcover","url":"s","ownerUserId":null}`
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/importlist", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for global list, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.ImportList
	json.NewDecoder(rec.Body).Decode(&created)
	stored, _ := repo.GetByID(ctx, created.ID)
	if stored == nil || stored.OwnerUserID != nil {
		t.Fatalf("stored owner = %+v, want nil (global)", stored)
	}
}

func TestImportListOwner_UpdateClearsToGlobal(t *testing.T) {
	h, repo, _, users := importListFixtureWithUsers(t)
	ctx := context.Background()
	owner, err := users.Create(ctx, "bob", "hash")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	il := models.ImportList{Name: "Bob", Type: "hardcover", URL: "b", OwnerUserID: &owner.ID}
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	// PUT with explicit null must clear the owner back to global.
	rec := httptest.NewRecorder()
	idStr := strconv.FormatInt(il.ID, 10)
	h.Update(rec, withURLParam(
		httptest.NewRequest(http.MethodPut, "/api/v1/importlist/"+idStr,
			bytes.NewBufferString(`{"ownerUserId":null}`)),
		"id", idStr))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, _ := repo.GetByID(ctx, il.ID)
	if stored == nil || stored.OwnerUserID != nil {
		t.Fatalf("after clear, owner = %+v, want nil", stored)
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

// fakeHCSyncer stands in for *hardcoverlistsyncer.ListSyncer. startErr is what
// StartOne returns; when it is nil the fake behaves like the real background
// launcher — it flips progress to running and only settles once release is
// closed, so a test can prove the handler answered without waiting.
type fakeHCSyncer struct {
	startErr error
	release  chan struct{}

	mu       sync.Mutex
	progress hardcoverlistsyncer.SyncProgress
	starts   []int64
}

func (f *fakeHCSyncer) StartOne(_ context.Context, id int64) error {
	f.mu.Lock()
	f.starts = append(f.starts, id)
	if f.startErr != nil {
		err := f.startErr
		f.mu.Unlock()
		return err
	}
	f.progress = hardcoverlistsyncer.SyncProgress{
		Running: true, ListID: id, Trigger: hardcoverlistsyncer.TriggerManual,
		StartedAt: time.Now().UTC(),
	}
	f.mu.Unlock()

	if f.release != nil {
		go func() {
			<-f.release
			f.mu.Lock()
			f.progress.Running = false
			f.mu.Unlock()
		}()
	}
	return nil
}

func (f *fakeHCSyncer) Progress() hardcoverlistsyncer.SyncProgress {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.progress
}

// syncFixture wires the handler with a seeded hardcover list and the fake
// syncer, returning the list id.
func syncFixture(t *testing.T, syncer HardcoverListSyncer) (*ImportListHandler, int64) {
	t.Helper()
	h, repo, _, _ := importListFixtureWithUsers(t)
	h.hcSync = syncer
	il := models.ImportList{Name: "Want to Read", Type: "hardcover", URL: "wtr", Enabled: true}
	if err := repo.Create(context.Background(), &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	return h, il.ID
}

// syncRequest issues POST /importlist/{id}/sync against the handler.
func syncRequest(h *ImportListHandler, id int64) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/importlist/"+strconv.FormatInt(id, 10)+"/sync", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(id, 10))
	h.Sync(rec, req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))
	return rec
}

// TestImportListSync_AcceptsWithoutWaiting is the #1854 contract at the HTTP
// edge: "Sync now" answers 202 with a running snapshot while the sync is still
// in flight, instead of holding the request until the 60s timeout kills it.
func TestImportListSync_AcceptsWithoutWaiting(t *testing.T) {
	syncer := &fakeHCSyncer{release: make(chan struct{})}
	defer close(syncer.release)
	h, id := syncFixture(t, syncer)

	rec := syncRequest(h, id)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var got hardcoverlistsyncer.SyncProgress
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode progress: %v (%s)", err, rec.Body.String())
	}
	if !got.Running || got.ListID != id {
		t.Errorf("progress = %+v, want a running sync of list %d", got, id)
	}
	if len(syncer.starts) != 1 || syncer.starts[0] != id {
		t.Errorf("StartOne calls = %v, want [%d]", syncer.starts, id)
	}
}

// TestImportListSync_MapsSyncerErrors pins the status code for each sentinel so
// a rejected start stays a 4xx and never looks like a launched job.
func TestImportListSync_MapsSyncerErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not found", hardcoverlistsyncer.ErrNotFound, http.StatusNotFound},
		{"wrong type", hardcoverlistsyncer.ErrWrongType, http.StatusBadRequest},
		{"disabled", hardcoverlistsyncer.ErrDisabled, http.StatusBadRequest},
		{"missing token", hardcoverlistsyncer.ErrMissingToken, http.StatusBadRequest},
		{"already running", hardcoverlistsyncer.ErrSyncAlreadyRunning, http.StatusConflict},
		// The process was already draining, so the job was never launched.
		// Nothing is broken and nothing is running: 503, not 502.
		{"shutting down", hardcoverlistsyncer.ErrShuttingDown, http.StatusServiceUnavailable},
		{"upstream failure", errors.New("boom"), http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, id := syncFixture(t, &fakeHCSyncer{startErr: tc.err})
			if rec := syncRequest(h, id); rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// TestImportListSyncStatus_ReportsProgress covers the poll endpoint the UI uses
// to tell that a sync is running and when it finished.
func TestImportListSyncStatus_ReportsProgress(t *testing.T) {
	finished := time.Now().UTC()
	syncer := &fakeHCSyncer{}
	syncer.progress = hardcoverlistsyncer.SyncProgress{
		Running: false, ListID: 4, ListName: "Want to Read",
		Trigger: hardcoverlistsyncer.TriggerScheduled, FinishedAt: &finished,
		Stats: hardcoverlistsyncer.SyncStats{Total: 3, Processed: 3, Imported: 2, Skipped: 1},
	}
	h, _, _, _ := importListFixtureWithUsers(t)
	h.hcSync = syncer

	rec := httptest.NewRecorder()
	h.SyncStatus(rec, httptest.NewRequest(http.MethodGet, "/api/v1/importlist/sync/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got hardcoverlistsyncer.SyncProgress
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Running || got.ListID != 4 || got.Stats.Imported != 2 || got.FinishedAt == nil {
		t.Errorf("progress = %+v, want the finished scheduled run", got)
	}
}

// TestImportListSync_NoSyncerConfigured keeps both endpoints honest when the
// Hardcover syncer isn't wired.
func TestImportListSync_NoSyncerConfigured(t *testing.T) {
	h, _, _, _ := importListFixtureWithUsers(t)
	if rec := syncRequest(h, 1); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("sync status = %d, want 503", rec.Code)
	}
	rec := httptest.NewRecorder()
	h.SyncStatus(rec, httptest.NewRequest(http.MethodGet, "/api/v1/importlist/sync/status", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status endpoint = %d, want 503", rec.Code)
	}
}

type fakeHardcoverUserListClient struct {
	lists   []hardcover.HCList
	account string
	err     error
}

func (f fakeHardcoverUserListClient) GetUserLists(context.Context) ([]hardcover.HCList, error) {
	return f.lists, f.err
}

func (f fakeHardcoverUserListClient) GetUsername(context.Context) (string, error) {
	return f.account, nil
}
