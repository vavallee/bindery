package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestUpgradeGate_ProfilePut_KeepsNarratorsAndEdition(t *testing.T) {
	h, repo, _, ctx := qualityProfileFixture(t)
	body := `{"name":"N","upgradeAllowed":true,"cutoff":"epub","removeReplaced":true,"preferredNarrators":["Rob Inglis"],"editionPreference":"require_unabridged","items":[{"quality":"epub","allowed":true},{"quality":"m4b","allowed":true}]}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/qualityprofile", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	ps, _ := repo.List(ctx)
	id := ps[len(ps)-1].ID
	ids := strconv.FormatInt(id, 10)
	old := `{"name":"N2","items":[{"quality":"epub","allowed":true},{"quality":"m4b","allowed":true}]}`
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/qualityprofile/"+ids, bytes.NewBufferString(old)), "id", ids))
	if rec.Code != http.StatusOK {
		t.Fatalf("put %d %s", rec.Code, rec.Body.String())
	}
	got, _ := repo.GetByID(ctx, id)
	if len(got.PreferredNarrators) != 1 || got.EditionPreference != "require_unabridged" || !got.UpgradeAllowed || !got.RemoveReplaced || got.Cutoff != "epub" {
		t.Fatalf("lost: %+v", got)
	}
}
