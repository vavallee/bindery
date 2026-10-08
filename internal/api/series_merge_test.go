package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func seriesMergeEnv(t *testing.T) (*SeriesHandler, *db.SeriesRepo, int64, int64) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewSeriesRepo(database)
	ctx := context.Background()
	target := &models.Series{ForeignID: "nb-series:1:fjellserien", Title: "Fjellserien"}
	source := &models.Series{ForeignID: "nb-series:1:serien-om-fjellet", Title: "Serien om fjellet"}
	for _, s := range []*models.Series{target, source} {
		if err := repo.CreateOrGet(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	return NewSeriesHandler(repo, db.NewBookRepo(database), db.NewAuthorRepo(database), nil, nil), repo, target.ID, source.ID
}

func postSeriesMerge(h *SeriesHandler, id int64, body string) *httptest.ResponseRecorder {
	sid := strconv.FormatInt(id, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/"+sid+"/merge", strings.NewReader(body)), "id", sid)
	rec := httptest.NewRecorder()
	h.Merge(rec, req)
	return rec
}

// A dry run returns the plan and leaves both series; applying it merges.
func TestSeriesMergeHandler(t *testing.T) {
	h, repo, target, source := seriesMergeEnv(t)
	ctx := context.Background()

	rec := postSeriesMerge(h, target, `{"sourceIds":[`+strconv.FormatInt(source, 10)+`],"title":" Fjell-serien ","dryRun":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body.String())
	}
	var plan db.SeriesMergePlan
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Title != "Fjell-serien" || len(plan.Sources) != 1 || plan.Sources[0].ID != source {
		t.Errorf("plan = %+v", plan)
	}
	if s, _ := repo.GetByID(ctx, source); s == nil {
		t.Fatal("dry run deleted the source")
	}

	rec = postSeriesMerge(h, target, `{"sourceIds":[`+strconv.FormatInt(source, 10)+`]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", rec.Code, rec.Body.String())
	}
	if s, _ := repo.GetByID(ctx, source); s != nil {
		t.Error("merge left the source")
	}
	if s, _ := repo.GetByForeignID(ctx, "nb-series:1:serien-om-fjellet"); s == nil || s.ID != target {
		t.Errorf("source id resolves to %+v, want the target", s)
	}
	if s, _ := repo.GetByID(ctx, target); s == nil || s.Title != "Fjellserien" {
		t.Errorf("target = %+v, want the title unchanged without a new one", s)
	}
}

func TestSeriesMergeHandlerErrors(t *testing.T) {
	h, _, target, source := seriesMergeEnv(t)
	src := strconv.FormatInt(source, 10)
	for name, c := range map[string]struct {
		id   int64
		body string
		want int
	}{
		"bad body":       {target, `{`, http.StatusBadRequest},
		"no sources":     {target, `{"sourceIds":[]}`, http.StatusBadRequest},
		"into itself":    {target, `{"sourceIds":[` + strconv.FormatInt(target, 10) + `]}`, http.StatusBadRequest},
		"missing source": {target, `{"sourceIds":[999]}`, http.StatusBadRequest},
		"title too long": {target, `{"sourceIds":[` + src + `],"title":"` + strings.Repeat("x", seriesTitleMaxLength+1) + `"}`, http.StatusBadRequest},
		"missing target": {999, `{"sourceIds":[` + src + `]}`, http.StatusNotFound},
	} {
		if rec := postSeriesMerge(h, c.id, c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body.String(), c.want)
		}
	}
}

// A storage failure is a 500, not a 400 or an empty plan.
func TestSeriesMergeHandlerServerError(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	h := NewSeriesHandler(db.NewSeriesRepo(database), db.NewBookRepo(database), db.NewAuthorRepo(database), nil, nil)
	database.Close()
	if rec := postSeriesMerge(h, 1, `{"sourceIds":[2],"dryRun":true}`); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d %s, want 500", rec.Code, rec.Body.String())
	}
}

// The series id comes from the URL; anything but a number is a 400.
func TestSeriesMergeHandlerBadID(t *testing.T) {
	h, _, _, _ := seriesMergeEnv(t)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/abc/merge", strings.NewReader(`{"sourceIds":[2]}`)), "id", "abc")
	rec := httptest.NewRecorder()
	h.Merge(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
