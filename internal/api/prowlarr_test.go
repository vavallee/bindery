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

func prowlarrFixture(t *testing.T) (*ProwlarrHandler, *db.ProwlarrRepo, *db.IndexerRepo, *db.SettingsRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	instances := db.NewProwlarrRepo(database)
	indexers := db.NewIndexerRepo(database)
	settings := db.NewSettingsRepo(database)
	h := NewProwlarrHandler(instances, indexers).WithSettings(settings)
	return h, instances, indexers, settings
}

func TestProwlarrList_Empty(t *testing.T) {
	h, _, _, _ := prowlarrFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/prowlarr", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// Must be a JSON array, never null.
	if got := rec.Body.String(); got != "[]\n" && got != "[]" {
		t.Errorf("empty list body = %q, want []", got)
	}
}

// TestProwlarrCRUD exercises the full Create -> Get/List -> Update -> Delete
// round-trip against an in-memory DB, asserting that fields actually persist
// and that the row is gone after Delete (not just that status codes are right).
func TestProwlarrCRUD(t *testing.T) {
	h, instances, _, _ := prowlarrFixture(t)
	ctx := context.Background()

	// Create. Use an RFC1918 literal so the SSRF LAN policy accepts it.
	body := `{"name":"Main","url":"http://10.10.10.10:9696","apiKey":"secret","syncOnStartup":true,"enabled":true}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/prowlarr", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.ProwlarrInstance
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 {
		t.Fatal("expected non-zero ID after create")
	}

	// Persistence check via the repo.
	stored, err := instances.GetByID(ctx, created.ID)
	if err != nil || stored == nil {
		t.Fatalf("stored row missing: %v", err)
	}
	if stored.Name != "Main" || stored.URL != "http://10.10.10.10:9696" || stored.APIKey != "secret" {
		t.Fatalf("persisted fields wrong: %+v", stored)
	}
	if !stored.SyncOnStartup || !stored.Enabled {
		t.Errorf("flags not persisted: syncOnStartup=%v enabled=%v", stored.SyncOnStartup, stored.Enabled)
	}

	// Get returns the row including the APIKey (handler logic; admin-gated at router).
	idStr := strconv.FormatInt(created.ID, 10)
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/prowlarr/"+idStr, nil), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", rec.Code)
	}
	var got models.ProwlarrInstance
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "secret" {
		t.Errorf("Get should surface APIKey at the handler level, got %q", got.APIKey)
	}

	// List has exactly the one entry.
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/prowlarr", nil))
	var list []models.ProwlarrInstance
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(list))
	}

	// Update — change name + apiKey + url.
	update := `{"name":"Renamed","url":"http://10.10.10.11:9696","apiKey":"newsecret","syncOnStartup":false,"enabled":false}`
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/prowlarr/"+idStr, bytes.NewBufferString(update)), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, _ = instances.GetByID(ctx, created.ID)
	if stored == nil {
		t.Fatal("row vanished after update")
	}
	if stored.Name != "Renamed" || stored.URL != "http://10.10.10.11:9696" || stored.APIKey != "newsecret" {
		t.Fatalf("update did not persist: %+v", stored)
	}
	if stored.SyncOnStartup || stored.Enabled {
		t.Errorf("update should have cleared flags: %+v", stored)
	}

	// Delete — row gone afterward.
	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/prowlarr/"+idStr, nil), "id", idStr))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, _ = instances.GetByID(ctx, created.ID)
	if stored != nil {
		t.Errorf("expected row gone after delete, still present: %+v", stored)
	}
}

// TestProwlarrUpdate_PropagatesAPIKeyToIndexers verifies the side effect in
// Update: when the instance APIKey changes, synced indexer rows have their
// api_key column rewritten.
func TestProwlarrUpdate_PropagatesAPIKeyToIndexers(t *testing.T) {
	h, instances, indexers, _ := prowlarrFixture(t)
	ctx := context.Background()

	inst := &models.ProwlarrInstance{Name: "P", URL: "http://10.0.0.5:9696", APIKey: "old", Enabled: true}
	if err := instances.Create(ctx, inst); err != nil {
		t.Fatal(err)
	}
	idx := &models.Indexer{
		Name:               "From Prowlarr",
		Type:               "torznab",
		URL:                "http://10.0.0.5:9696/1/api",
		APIKey:             "old",
		Enabled:            true,
		ProwlarrInstanceID: &inst.ID,
	}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatal(err)
	}

	idStr := strconv.FormatInt(inst.ID, 10)
	update := `{"name":"P","url":"http://10.0.0.5:9696","apiKey":"rotated","enabled":true}`
	rec := httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/prowlarr/"+idStr, bytes.NewBufferString(update)), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rows, err := indexers.ListByProwlarrInstance(ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 synced indexer, got %d", len(rows))
	}
	if rows[0].APIKey != "rotated" {
		t.Errorf("indexer api_key not propagated: got %q, want rotated", rows[0].APIKey)
	}
}

// TestProwlarrDelete_RemovesSyncedIndexers verifies Delete first removes the
// synced indexers (FK constraint) and then the instance.
func TestProwlarrDelete_RemovesSyncedIndexers(t *testing.T) {
	h, instances, indexers, _ := prowlarrFixture(t)
	ctx := context.Background()

	inst := &models.ProwlarrInstance{Name: "P", URL: "http://10.0.0.5:9696", APIKey: "k", Enabled: true}
	if err := instances.Create(ctx, inst); err != nil {
		t.Fatal(err)
	}
	idx := &models.Indexer{
		Name: "From Prowlarr", Type: "torznab", URL: "http://10.0.0.5:9696/1/api",
		APIKey: "k", Enabled: true, ProwlarrInstanceID: &inst.ID,
	}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatal(err)
	}

	idStr := strconv.FormatInt(inst.ID, 10)
	rec := httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/prowlarr/"+idStr, nil), "id", idStr))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	rows, _ := indexers.ListByProwlarrInstance(ctx, inst.ID)
	if len(rows) != 0 {
		t.Errorf("expected synced indexers removed, got %d", len(rows))
	}
	if stored, _ := instances.GetByID(ctx, inst.ID); stored != nil {
		t.Errorf("instance not deleted")
	}
}

func TestProwlarrCreate_Validation(t *testing.T) {
	h, _, _, _ := prowlarrFixture(t)
	for name, body := range map[string]string{
		"missing url": `{"name":"x"}`,
		"bad json":    `not-json`,
	} {
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/prowlarr", bytes.NewBufferString(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, rec.Code)
		}
	}
}

func TestProwlarrCreate_DefaultName(t *testing.T) {
	h, instances, _, _ := prowlarrFixture(t)
	body := `{"url":"http://10.10.10.10:9696","apiKey":"k"}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/prowlarr", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.ProwlarrInstance
	json.NewDecoder(rec.Body).Decode(&created)
	stored, _ := instances.GetByID(context.Background(), created.ID)
	if stored == nil || stored.Name != "Prowlarr" {
		t.Errorf("default name not applied: %+v", stored)
	}
}

func TestProwlarrGet_NotFound(t *testing.T) {
	h, _, _, _ := prowlarrFixture(t)
	rec := httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/prowlarr/999", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestProwlarrGet_InvalidID(t *testing.T) {
	h, _, _, _ := prowlarrFixture(t)
	rec := httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/prowlarr/abc", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestProwlarrUpdate_NotFound(t *testing.T) {
	h, _, _, _ := prowlarrFixture(t)
	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"url":"http://10.0.0.1:9696"}`)
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/prowlarr/999", body), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// newProwlarrStub returns an httptest server impersonating a Prowlarr instance.
// /api/v1/system/status returns the system status (used by Test); /api/v1/indexer
// returns the indexer list (used by Sync).
func newProwlarrStub(t *testing.T, version string, indexers string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/status":
			_, _ = w.Write([]byte(`{"version":"` + version + `"}`))
		case "/api/v1/indexer":
			_, _ = w.Write([]byte(indexers))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestProwlarrTest_Success points the handler's upstream client at a fake
// Prowlarr and asserts the version is surfaced.
func TestProwlarrTest_Success(t *testing.T) {
	h, instances, _, _ := prowlarrFixture(t)
	srv := newProwlarrStub(t, "1.21.2.4649", "[]")

	inst := &models.ProwlarrInstance{Name: "P", URL: srv.URL, APIKey: "k", Enabled: true}
	if err := instances.Create(context.Background(), inst); err != nil {
		t.Fatal(err)
	}

	idStr := strconv.FormatInt(inst.ID, 10)
	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/prowlarr/"+idStr+"/test", nil), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	json.NewDecoder(rec.Body).Decode(&out)
	if out["ok"] != "true" {
		t.Errorf("expected ok=true, got %v", out)
	}
	if out["version"] != "1.21.2.4649" {
		t.Errorf("version not surfaced: %v", out)
	}
}

// TestProwlarrTest_UpstreamError points the client at a server returning 500 and
// asserts the handler surfaces ok=false with an error (still HTTP 200 body).
func TestProwlarrTest_UpstreamError(t *testing.T) {
	h, instances, _, _ := prowlarrFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	inst := &models.ProwlarrInstance{Name: "P", URL: srv.URL, APIKey: "k", Enabled: true}
	if err := instances.Create(context.Background(), inst); err != nil {
		t.Fatal(err)
	}

	idStr := strconv.FormatInt(inst.ID, 10)
	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/prowlarr/"+idStr+"/test", nil), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 envelope, got %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	json.NewDecoder(rec.Body).Decode(&out)
	if out["ok"] != "false" {
		t.Errorf("expected ok=false on upstream error, got %v", out)
	}
	if out["error"] == "" {
		t.Errorf("expected a non-empty error message, got %v", out)
	}
}

func TestProwlarrTest_NotFound(t *testing.T) {
	h, _, _, _ := prowlarrFixture(t)
	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/prowlarr/999/test", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestProwlarrSync_Success drives Sync against a fake Prowlarr that advertises
// one searchable, book-categorised torrent indexer and asserts it is created
// in Bindery's indexer table (added=1).
func TestProwlarrSync_Success(t *testing.T) {
	h, instances, indexers, _ := prowlarrFixture(t)
	ctx := context.Background()

	const indexerJSON = `[{
		"id": 5,
		"name": "MyTorrentSite",
		"enable": true,
		"protocol": "torrent",
		"supportsSearch": true,
		"categories": [{"id": 7020, "name": "Ebooks"}]
	}]`
	srv := newProwlarrStub(t, "1.21.2", indexerJSON)

	inst := &models.ProwlarrInstance{Name: "P", URL: srv.URL, APIKey: "k", Enabled: true}
	if err := instances.Create(ctx, inst); err != nil {
		t.Fatal(err)
	}

	idStr := strconv.FormatInt(inst.ID, 10)
	rec := httptest.NewRecorder()
	h.Sync(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/prowlarr/"+idStr+"/sync", nil), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Added, Updated, Removed int
	}
	json.NewDecoder(rec.Body).Decode(&out)
	if out.Added != 1 {
		t.Errorf("expected added=1, got %+v", out)
	}

	rows, _ := indexers.ListByProwlarrInstance(ctx, inst.ID)
	if len(rows) != 1 {
		t.Fatalf("expected 1 indexer synced into DB, got %d", len(rows))
	}
	if rows[0].Name != "MyTorrentSite" || rows[0].Type != "torznab" {
		t.Errorf("synced indexer wrong: %+v", rows[0])
	}
}

// TestProwlarrSync_UpstreamError asserts Sync surfaces a 502 when the upstream
// Prowlarr fails.
func TestProwlarrSync_UpstreamError(t *testing.T) {
	h, instances, _, _ := prowlarrFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	inst := &models.ProwlarrInstance{Name: "P", URL: srv.URL, APIKey: "k", Enabled: true}
	if err := instances.Create(context.Background(), inst); err != nil {
		t.Fatal(err)
	}

	idStr := strconv.FormatInt(inst.ID, 10)
	rec := httptest.NewRecorder()
	h.Sync(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/prowlarr/"+idStr+"/sync", nil), "id", idStr))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 on upstream error, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestProwlarrSync_NotFound(t *testing.T) {
	h, _, _, _ := prowlarrFixture(t)
	rec := httptest.NewRecorder()
	h.Sync(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/prowlarr/999/sync", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestProwlarrClientTimeout_FromSettings confirms the configurable
// prowlarr.search_timeout_seconds setting overrides the 60s default.
func TestProwlarrClientTimeout_FromSettings(t *testing.T) {
	h, _, _, settings := prowlarrFixture(t)
	ctx := context.Background()
	if got := h.prowlarrClientTimeout(ctx); got.Seconds() != 60 {
		t.Errorf("default timeout = %v, want 60s", got)
	}
	if err := settings.Set(ctx, SettingProwlarrSearchTimeoutSeconds, "5"); err != nil {
		t.Fatal(err)
	}
	if got := h.prowlarrClientTimeout(ctx); got.Seconds() != 5 {
		t.Errorf("configured timeout = %v, want 5s", got)
	}
	if got := LoadProwlarrTimeout(ctx, settings); got.Seconds() != 5 {
		t.Errorf("LoadProwlarrTimeout = %v, want 5s", got)
	}
}
