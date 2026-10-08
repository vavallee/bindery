package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestMetadataProfileUpdateCoverage covers id, existence, ownership and body
// checks on update, and the unknown-language normalisation on a valid write.
func TestMetadataProfileUpdateCoverage(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := t.Context()
	repo := db.NewMetadataProfileRepo(database)
	users := db.NewUserRepo(database)
	h := NewMetadataProfileHandler(repo)
	alice, err := users.Create(ctx, "alice", "x")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "x")
	if err != nil {
		t.Fatal(err)
	}
	p := &models.MetadataProfile{Name: "Alice Profile", AllowedLanguages: "eng"}
	if err := repo.CreateForUser(ctx, p, alice.ID); err != nil {
		t.Fatal(err)
	}
	idStr := strconv.FormatInt(p.ID, 10)
	update := func(uid int64, id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/metadataprofile/"+id, bytes.NewBufferString(body))
		req = req.WithContext(auth.WithUserRole(auth.WithUserID(req.Context(), uid), auth.RoleUser))
		rec := httptest.NewRecorder()
		h.Update(rec, withURLParam(req, "id", id))
		return rec
	}
	unchanged := func(t *testing.T) {
		t.Helper()
		got, _ := repo.GetByID(ctx, p.ID)
		if got.Name != "Alice Profile" {
			t.Fatalf("a refused update changed the profile: %+v", got)
		}
	}

	auth.SetEnforceTenancyForTests(t, true)
	for _, c := range []struct {
		name string
		uid  int64
		id   string
		body string
		want int
	}{
		{"bad id", alice.ID, "x", `{"name":"N"}`, http.StatusBadRequest},
		{"missing", alice.ID, "9999", `{"name":"N"}`, http.StatusNotFound},
		{"other user", bob.ID, idStr, `{"name":"Hijacked"}`, http.StatusNotFound},
		{"bad body", alice.ID, idStr, `{"name":`, http.StatusBadRequest},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := update(c.uid, c.id, c.body)
			if rec.Code != c.want {
				t.Fatalf("%d %s, want %d", rec.Code, rec.Body.String(), c.want)
			}
			unchanged(t)
		})
	}

	rec := update(alice.ID, idStr, `{"name":"Renamed","allowedLanguages":"eng","unknownLanguageBehavior":"bogus"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner update: %d %s", rec.Code, rec.Body.String())
	}
	var got models.MetadataProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Renamed" || got.UnknownLanguageBehavior != models.UnknownLanguagePass {
		t.Fatalf("response = %+v, want renamed with unknown language pass", got)
	}
	stored, _ := repo.GetByID(ctx, p.ID)
	if stored.Name != "Renamed" || stored.UnknownLanguageBehavior != models.UnknownLanguagePass {
		t.Fatalf("stored = %+v", stored)
	}
}

// TestIndexerUpdateCoverageRefusals covers the refusals on indexer update; a
// refused update must not change the stored row or its key.
func TestIndexerUpdateCoverageRefusals(t *testing.T) {
	h := indexerFixture(t)
	idx := &models.Indexer{Name: "Keep", Type: "newznab", URL: "http://10.0.0.9/api", APIKey: "secret-key", Enabled: true}
	if err := h.indexers.Create(t.Context(), idx); err != nil {
		t.Fatal(err)
	}
	idStr := strconv.FormatInt(idx.ID, 10)
	for _, c := range []struct {
		name, id, body string
		want           int
		wantErr        string
	}{
		{"bad id", "x", `{}`, http.StatusBadRequest, ""},
		{"missing", "9999", `{}`, http.StatusNotFound, "indexer not found"},
		{"bad body", idStr, `{"name":`, http.StatusBadRequest, "invalid request body"},
		{"key and clear", idStr, `{"apiKey":"k2","clearApiKey":true}`, http.StatusBadRequest, "apiKey and clearApiKey cannot both be set"},
		{"bad url", idStr, `{"url":"gopher://10.0.0.9/"}`, http.StatusBadRequest, "scheme must be http or https"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/indexer/"+c.id, bytes.NewBufferString(c.body)), "id", c.id))
			if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.wantErr) {
				t.Fatalf("%d %s, want %d %q", rec.Code, rec.Body.String(), c.want, c.wantErr)
			}
			got, _ := h.indexers.GetByID(t.Context(), idx.ID)
			if got.Name != "Keep" || got.APIKey != "secret-key" || got.URL != "http://10.0.0.9/api" {
				t.Fatalf("a refused update changed the indexer: %+v", got)
			}
		})
	}
}
