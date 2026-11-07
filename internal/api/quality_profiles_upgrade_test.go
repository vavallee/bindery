package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// A client that predates the upgrade settings sends the whole profile back
// without them. The stored values must survive, and a value it does send wins.
func TestQualityProfileUpdate_OmittedUpgradeSettingsKeepTheStoredOnes(t *testing.T) {
	h, repo, _, ctx := qualityProfileFixture(t)
	body := `{"name":"U","upgradeAllowed":true,"cutoff":"epub","removeReplaced":true,"items":[{"quality":"epub","allowed":true},{"quality":"mobi","allowed":true}]}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/qualityprofile", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	profiles, err := repo.List(ctx)
	if err != nil || len(profiles) == 0 {
		t.Fatalf("list: %v", err)
	}
	id := profiles[len(profiles)-1].ID
	idStr := strconv.FormatInt(id, 10)

	old := `{"name":"U renamed","items":[{"quality":"epub","allowed":true},{"quality":"mobi","allowed":true}]}`
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/qualityprofile/"+idStr, bytes.NewBufferString(old)), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	got, err := repo.GetByID(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if !got.UpgradeAllowed || got.Cutoff != "epub" || !got.RemoveReplaced {
		t.Fatalf("old client cleared the upgrade settings: %+v", got)
	}

	explicit := `{"name":"U renamed","upgradeAllowed":false,"removeReplaced":false,"items":[{"quality":"epub","allowed":true}]}`
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/qualityprofile/"+idStr, bytes.NewBufferString(explicit)), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Fatalf("explicit update: %d %s", rec.Code, rec.Body.String())
	}
	got, _ = repo.GetByID(ctx, id)
	if got.UpgradeAllowed || got.RemoveReplaced {
		t.Fatalf("an explicit false was ignored: %+v", got)
	}
}

// Upgrades are the one reader of the cutoff (#2420), so with them on the
// cutoff has to be a ticked ebook format. With them off it is still free.
func TestQualityProfileCreate_UpgradeNeedsAnAllowedEbookCutoff(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"valid", `{"name":"A","upgradeAllowed":true,"cutoff":"epub","items":[{"quality":"epub","allowed":true},{"quality":"mobi","allowed":true}]}`, http.StatusCreated},
		{"no cutoff", `{"name":"B","upgradeAllowed":true,"items":[{"quality":"epub","allowed":true}]}`, http.StatusBadRequest},
		{"unticked cutoff", `{"name":"C","upgradeAllowed":true,"cutoff":"mobi","items":[{"quality":"epub","allowed":true},{"quality":"mobi","allowed":false}]}`, http.StatusBadRequest},
		{"audio cutoff", `{"name":"D","upgradeAllowed":true,"cutoff":"m4b","items":[{"quality":"epub","allowed":true},{"quality":"m4b","allowed":true}]}`, http.StatusBadRequest},
		{"off, odd cutoff", `{"name":"E","cutoff":"pdf","items":[{"quality":"epub","allowed":true}]}`, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, _ := qualityProfileFixture(t)
			rec := httptest.NewRecorder()
			h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/qualityprofile", bytes.NewBufferString(tc.body)))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// removeReplaced defaults to false, so a client that never heard of it keeps
// the old files, and the value round-trips.
func TestQualityProfileCreate_RemoveReplacedDefaultsOffAndRoundTrips(t *testing.T) {
	h, repo, _, ctx := qualityProfileFixture(t)
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"absent", `{"name":"Keep","items":[{"quality":"epub","allowed":true}]}`, false},
		{"set", `{"name":"Remove","removeReplaced":true,"items":[{"quality":"epub","allowed":true}]}`, true},
	} {
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/qualityprofile", bytes.NewBufferString(tc.body)))
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: status = %d: %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	profiles, err := repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = ctx
	got := map[string]bool{}
	for _, p := range profiles {
		got[p.Name] = p.RemoveReplaced
	}
	if got["Keep"] || !got["Remove"] {
		t.Errorf("removeReplaced = %v, want Keep=false Remove=true", got)
	}
}
