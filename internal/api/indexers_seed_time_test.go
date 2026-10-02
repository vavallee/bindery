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

// TestIndexerSeedTimeLimits_API covers the #2206 fields through the handler:
// they round-trip, an update that omits them keeps them, an explicit null
// clears them, a value below one minute is rejected on both write paths, and
// writing them marks the seed time as user owned so a Prowlarr sync leaves it.
func TestIndexerSeedTimeLimits_API(t *testing.T) {
	h := indexerFixture(t)

	decode := func(t *testing.T, rec *httptest.ResponseRecorder) models.Indexer {
		t.Helper()
		var out models.Indexer
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}
	update := func(id int64, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/indexer/"+strconv.FormatInt(id, 10), bytes.NewBufferString(body))
		h.Update(rec, withURLParam(req, "id", strconv.FormatInt(id, 10)))
		return rec
	}

	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/indexer",
		bytes.NewBufferString(`{"name":"Timed","url":"http://192.168.1.50:9117/api","apiKey":"k","type":"torznab","seedTimeMinutes":4320,"inactiveSeedTimeMinutes":60}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	created := decode(t, rec)
	if created.SeedTimeMinutes == nil || *created.SeedTimeMinutes != 4320 {
		t.Fatalf("created SeedTimeMinutes = %v, want 4320", created.SeedTimeMinutes)
	}
	if created.InactiveSeedTimeMinutes == nil || *created.InactiveSeedTimeMinutes != 60 {
		t.Fatalf("created InactiveSeedTimeMinutes = %v, want 60", created.InactiveSeedTimeMinutes)
	}
	if created.SeedTimeSource != models.SeedRatioSourceUser {
		t.Errorf("created SeedTimeSource = %q, want user", created.SeedTimeSource)
	}

	// Omitted keys keep the stored values.
	rec = update(created.ID, `{"name":"Timed Renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	got := decode(t, rec)
	if got.SeedTimeMinutes == nil || *got.SeedTimeMinutes != 4320 || got.InactiveSeedTimeMinutes == nil || *got.InactiveSeedTimeMinutes != 60 {
		t.Errorf("an update that omitted the limits changed them: %v %v", got.SeedTimeMinutes, got.InactiveSeedTimeMinutes)
	}

	// An explicit null is how the form clears a field: back to the client's rule.
	rec = update(created.ID, `{"seedTimeMinutes":null,"inactiveSeedTimeMinutes":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	got = decode(t, rec)
	if got.SeedTimeMinutes != nil || got.InactiveSeedTimeMinutes != nil {
		t.Errorf("after clear: %v %v, want both nil", got.SeedTimeMinutes, got.InactiveSeedTimeMinutes)
	}
	if got.SeedTimeSource != models.SeedRatioSourceUser {
		t.Errorf("a clear must stay user owned so Prowlarr does not refill it, got %q", got.SeedTimeSource)
	}
	stored, _ := h.indexers.GetByID(t.Context(), created.ID)
	if stored.SeedTimeMinutes != nil || stored.InactiveSeedTimeMinutes != nil {
		t.Errorf("stored after clear: %v %v, want both nil", stored.SeedTimeMinutes, stored.InactiveSeedTimeMinutes)
	}

	bad := []string{
		`"seedTimeMinutes":-1`,
		`"seedTimeMinutes":0`,
		`"inactiveSeedTimeMinutes":-30`,
		`"inactiveSeedTimeMinutes":0`,
		`"seedTimeMinutes":5256001`,
	}
	for i, field := range bad {
		rec = httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/indexer",
			bytes.NewBufferString(`{"name":"Bad","url":"http://192.168.2.`+strconv.Itoa(i+1)+`:9117/api",`+field+`}`)))
		// The message must name the field, so a 400 for some other reason
		// (the URL, a duplicate) cannot pass this test.
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "SeedTimeMinutes") && !strings.Contains(rec.Body.String(), "seedTimeMinutes") {
			t.Errorf("create with %s: expected a 400 naming the field, got %d %s", field, rec.Code, rec.Body.String())
		}
		rec = update(created.ID, `{`+field+`}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "SeedTimeMinutes") && !strings.Contains(rec.Body.String(), "seedTimeMinutes") {
			t.Errorf("update with %s: expected a 400 naming the field, got %d %s", field, rec.Code, rec.Body.String())
		}
	}
	stored, _ = h.indexers.GetByID(t.Context(), created.ID)
	if stored.SeedTimeMinutes != nil || stored.InactiveSeedTimeMinutes != nil {
		t.Errorf("a rejected update was stored: %v %v", stored.SeedTimeMinutes, stored.InactiveSeedTimeMinutes)
	}
}

// TestIndexerCreate_NoSeedTimeLeavesSourceUnset: an indexer created without a
// seed time keeps the provenance unset, matching the seed ratio rule.
func TestIndexerCreate_NoSeedTimeLeavesSourceUnset(t *testing.T) {
	h := indexerFixture(t)
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/indexer",
		bytes.NewBufferString(`{"name":"Plain","url":"http://192.168.1.51:9117/api","apiKey":"k"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	var created models.Indexer
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.SeedTimeSource != models.SeedRatioSourceUnset {
		t.Errorf("SeedTimeSource = %q, want unset", created.SeedTimeSource)
	}
}
