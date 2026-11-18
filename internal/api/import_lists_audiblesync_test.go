package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/audiblesynclistsyncer"
	"github.com/vavallee/bindery/internal/models"
)

type fakeASSyncer struct {
	startErr error
	starts   []int64
}

func (f *fakeASSyncer) StartOne(_ context.Context, id int64) error {
	f.starts = append(f.starts, id)
	return f.startErr
}

func (f *fakeASSyncer) Progress() audiblesynclistsyncer.SyncProgress {
	return audiblesynclistsyncer.SyncProgress{Running: true, ListName: "Audible wishlist", Stats: audiblesynclistsyncer.SyncStats{Owned: 2}}
}

func createList(t *testing.T, h *ImportListHandler, body string) (*httptest.ResponseRecorder, models.ImportList) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/importlist", bytes.NewBufferString(body)))
	var il models.ImportList
	_ = json.NewDecoder(rec.Body).Decode(&il)
	return rec, il
}

func TestCreateAudibleSyncListNormalises(t *testing.T) {
	h, _, _ := importListFixture(t)
	rec, il := createList(t, h, `{"name":"Wishlist","type":"audiblesync","url":"ignored","apiKey":"ignored","account":" 2 ","mediaType":"ebook","enabled":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if il.MediaType != models.MediaTypeAudiobook || il.URL != "" || il.APIKeyConfigured || il.Account != "2" {
		t.Fatalf("list = %+v", il)
	}
	if !il.SkipOwned || il.RemoveOnImport {
		t.Fatalf("defaults: skipOwned=%v removeOnImport=%v, want true and false", il.SkipOwned, il.RemoveOnImport)
	}

	rec, _ = createList(t, h, `{"name":"Bad","type":"audiblesync","account":"main"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a non-numeric account must be refused, got %d", rec.Code)
	}
}

func TestUpdateAudibleSyncListOptions(t *testing.T) {
	h, repo, _ := importListFixture(t)
	_, il := createList(t, h, `{"name":"Wishlist","type":"audiblesync","enabled":true}`)

	r := chi.NewRouter()
	r.Put("/importlist/{id}", h.Update)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/importlist/"+strconv.FormatInt(il.ID, 10),
		bytes.NewBufferString(`{"skipOwned":false,"removeOnImport":true,"mediaType":"ebook"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got, _ := repo.GetByID(context.Background(), il.ID)
	if got.SkipOwned || !got.RemoveOnImport || got.MediaType != models.MediaTypeAudiobook {
		t.Fatalf("after update = %+v", got)
	}
}

func TestSyncDispatchesAudibleSyncLists(t *testing.T) {
	h, repo, _ := importListFixture(t)
	hc := &fakeHCSyncer{}
	as := &fakeASSyncer{}
	h.hcSync = hc
	h.WithAudibleSyncSyncer(as)
	ctx := context.Background()
	wish := models.ImportList{Name: "Wishlist", Type: models.ImportListTypeAudibleSync, Enabled: true}
	shelf := models.ImportList{Name: "Shelf", Type: models.ImportListTypeHardcover, URL: "wtr", Enabled: true}
	_ = repo.Create(ctx, &wish)
	_ = repo.Create(ctx, &shelf)

	if rec := syncRequest(h, wish.ID); rec.Code != http.StatusAccepted {
		t.Fatalf("wishlist sync: %d %s", rec.Code, rec.Body.String())
	}
	if rec := syncRequest(h, shelf.ID); rec.Code != http.StatusAccepted {
		t.Fatalf("hardcover sync: %d %s", rec.Code, rec.Body.String())
	}
	if len(as.starts) != 1 || as.starts[0] != wish.ID || len(hc.starts) != 1 || hc.starts[0] != shelf.ID {
		t.Fatalf("audible sync starts %v, hardcover starts %v", as.starts, hc.starts)
	}

	as.startErr = audiblesynclistsyncer.ErrNotConfigured
	if rec := syncRequest(h, wish.ID); rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfigured: %d", rec.Code)
	}
	as.startErr = audiblesynclistsyncer.ErrSyncAlreadyRunning
	if rec := syncRequest(h, wish.ID); rec.Code != http.StatusConflict {
		t.Fatalf("already running: %d", rec.Code)
	}

	rec := httptest.NewRecorder()
	h.SyncStatus(rec, httptest.NewRequest(http.MethodGet, "/importlist/sync/status?type=audiblesync", nil))
	var p audiblesynclistsyncer.SyncProgress
	_ = json.NewDecoder(rec.Body).Decode(&p)
	if rec.Code != http.StatusOK || p.Stats.Owned != 2 {
		t.Fatalf("status: %d %+v", rec.Code, p)
	}
}
