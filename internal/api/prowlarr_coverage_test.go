package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestProwlarrUpdateCoverage covers the refusals on update (none may write)
// and the write-only key contract: a blank key keeps the stored one and leaves
// synced indexers alone, clearApiKey wipes it and cascades, and the response
// never carries the key.
func TestProwlarrUpdateCoverage(t *testing.T) {
	h, instances, indexers, _ := prowlarrFixture(t)
	ctx := t.Context()
	inst := &models.ProwlarrInstance{Name: "P", URL: "http://10.0.0.5:9696", APIKey: "stored-key", Enabled: true}
	if err := instances.Create(ctx, inst); err != nil {
		t.Fatal(err)
	}
	idx := &models.Indexer{Name: "Synced", Type: "torznab", URL: "http://10.0.0.5:9696/1/api", APIKey: "stored-key", Enabled: true, ProwlarrInstanceID: &inst.ID}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatal(err)
	}
	idStr := strconv.FormatInt(inst.ID, 10)
	update := func(id, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/prowlarr/"+id, bytes.NewBufferString(body)), "id", id))
		return rec
	}
	assertStored := func(t *testing.T, wantName, wantKey string) {
		t.Helper()
		got, _ := instances.GetByID(ctx, inst.ID)
		if got.Name != wantName || got.APIKey != wantKey {
			t.Fatalf("instance = name %q key %q, want %q %q", got.Name, got.APIKey, wantName, wantKey)
		}
		rows, _ := indexers.ListByProwlarrInstance(ctx, inst.ID)
		if len(rows) != 1 || rows[0].APIKey != wantKey {
			t.Fatalf("synced indexer key = %+v, want %q", rows, wantKey)
		}
	}

	for _, c := range []struct {
		name, id, body, wantErr string
	}{
		{"bad id", "x", `{}`, "invalid id"},
		{"bad json", idStr, `{"name":`, "invalid JSON"},
		{"wrong type", idStr, `{"apiKey":5}`, "invalid JSON"},
		{"key and clear", idStr, `{"name":"Q","apiKey":"new","clearApiKey":true}`, "apiKey and clearApiKey cannot both be set"},
		{"bad url", idStr, `{"name":"Q","url":"file:///etc/passwd"}`, "invalid indexer URL"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := update(c.id, c.body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.wantErr) {
				t.Fatalf("%d %s, want 400 %q", rec.Code, rec.Body.String(), c.wantErr)
			}
			assertStored(t, "P", "stored-key")
		})
	}

	// The web app sends the redacted (blank) key back on every save.
	rec := update(idStr, `{"name":"Renamed","url":"http://10.0.0.5:9696","apiKey":"","enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "stored-key") {
		t.Fatalf("response leaked the api key: %s", rec.Body.String())
	}
	var resp models.ProwlarrInstance
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.APIKeyConfigured || resp.Name != "Renamed" {
		t.Fatalf("response = %+v, want renamed with key configured", resp)
	}
	assertStored(t, "Renamed", "stored-key")

	rec = update(idStr, `{"name":"Renamed","url":"http://10.0.0.5:9696","clearApiKey":true,"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	assertStored(t, "Renamed", "")
}
