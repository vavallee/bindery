package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// TestQualityProfileUpdateCoverageRefusals covers the id, body, validation
// and name collision refusals on update; none of them may change the row.
func TestQualityProfileUpdateCoverageRefusals(t *testing.T) {
	h, repo, database, ctx := qualityProfileFixture(t)
	mk := func(name string) *models.QualityProfile {
		p := &models.QualityProfile{Name: name, Cutoff: "epub", Items: []models.QualityItem{{Quality: "epub", Allowed: true}}}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	target := mk("Target")
	mk("Taken")
	idStr := strconv.FormatInt(target.ID, 10)
	update := func(id, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/qualityprofile/"+id, bytes.NewBufferString(body)), "id", id))
		return rec
	}

	cases := []struct {
		name, id, body string
		want           int
		wantErr        string
	}{
		{"bad id", "abc", validProfileBody("X"), http.StatusBadRequest, "invalid id"},
		{"bad body", idStr, `{"name":`, http.StatusBadRequest, "invalid request body"},
		{"blank name", idStr, `{"name":"  ","items":[{"quality":"epub","allowed":true}]}`, http.StatusBadRequest, "name required"},
		{"no items", idStr, `{"name":"Y","items":[]}`, http.StatusBadRequest, "at least one format is required"},
		{"empty format", idStr, `{"name":"Y","items":[{"quality":" ","allowed":true}]}`, http.StatusBadRequest, "format name cannot be empty"},
		{"duplicate format", idStr, `{"name":"Y","items":[{"quality":"EPUB","allowed":true},{"quality":"epub","allowed":true}]}`, http.StatusBadRequest, "duplicate format in preference order: epub"},
		{"name taken", idStr, validProfileBody("Taken"), http.StatusConflict, "a quality profile with that name already exists"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := update(c.id, c.body)
			if rec.Code != c.want {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), c.want)
			}
			var body map[string]string
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if body["error"] != c.wantErr {
				t.Fatalf("error = %q, want %q", body["error"], c.wantErr)
			}
			got, _ := repo.GetByID(ctx, target.ID)
			if got.Name != "Target" || len(got.Items) != 1 {
				t.Fatalf("a refused update changed the profile: %+v", got)
			}
		})
	}

	// Renaming to its own current name is not a collision.
	if rec := update(idStr, validProfileBody("Target")); rec.Code != http.StatusOK {
		t.Fatalf("same-name update: %d %s", rec.Code, rec.Body.String())
	}

	database.Close()
	if rec := update(idStr, validProfileBody("Target")); rec.Code != http.StatusInternalServerError {
		t.Fatalf("repo error: %d, want 500", rec.Code)
	}
}

// TestIndexerTestCoverageSavedIndexer drives the saved-indexer probe: id and
// existence checks, the outbound URL guard, a passing probe and a failing one
// reported inline with 200.
func TestIndexerTestCoverageSavedIndexer(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	var hits atomic.Int32
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><caps><categories><category id="7020" name="Ebook"/></categories></caps>`))
	}))
	defer okSrv.Close()
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer badSrv.Close()

	h := indexerFixture(t)
	save := func(url string) string {
		idx := &models.Indexer{Name: "idx " + url, Type: "newznab", URL: url, APIKey: "k", Enabled: true}
		if err := h.indexers.Create(t.Context(), idx); err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(idx.ID, 10)
	}
	probe := func(id string) (*httptest.ResponseRecorder, IndexerTestResponse) {
		rec := httptest.NewRecorder()
		h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/indexer/"+id+"/test", nil), "id", id))
		var out IndexerTestResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}

	if rec, _ := probe("x"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d, want 400", rec.Code)
	}
	if rec, _ := probe("4242"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing indexer: %d, want 404", rec.Code)
	}

	blocked := save("ftp://indexer.invalid/api")
	if rec, _ := probe(blocked); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "scheme must be http or https") {
		t.Fatalf("blocked url: %d %s, want 400", rec.Code, rec.Body.String())
	}

	rec, out := probe(save(okSrv.URL))
	if rec.Code != http.StatusOK || !out.OK || out.Message != "ok" || out.Error != "" {
		t.Fatalf("good indexer: %d %+v", rec.Code, out)
	}
	if hits.Load() == 0 {
		t.Fatal("probe never reached the indexer")
	}

	rec, out = probe(save(badSrv.URL))
	if rec.Code != http.StatusOK || out.OK || out.Error == "" {
		t.Fatalf("failing indexer: %d %+v, want 200 with inline error", rec.Code, out)
	}
}

// TestUserManagementResetPasswordCoverage covers the admin reset: id, body
// and length refusals leave the hash alone, a valid reset replaces it.
func TestUserManagementResetPasswordCoverage(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := t.Context()
	oldHash, err := auth.HashPassword("old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	u, err := users.Create(ctx, "carol", oldHash)
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(u.ID, 10)
	reset := func(id, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ResetPassword(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/auth/users/"+id+"/reset-password", strings.NewReader(body)), "id", id))
		return rec
	}

	for _, c := range []struct {
		name, id, body string
	}{
		{"bad id", "zz", `{"password":"long-enough-pw"}`},
		{"bad body", id, `{"password":`},
		{"short password", id, `{"password":"short"}`},
	} {
		if rec := reset(c.id, c.body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s, want 400", c.name, rec.Code, rec.Body.String())
		}
	}
	if got, _ := users.GetByID(ctx, u.ID); got.PasswordHash != oldHash {
		t.Fatal("a refused reset changed the hash")
	}

	if rec := reset(id, `{"password":"a-new-password-9"}`); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := users.GetByID(ctx, u.ID)
	if !auth.VerifyPassword("a-new-password-9", got.PasswordHash) || auth.VerifyPassword("old-password-1", got.PasswordHash) {
		t.Fatal("reset did not replace the password")
	}
}
