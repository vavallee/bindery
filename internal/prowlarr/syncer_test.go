package prowlarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type fakeIndexerStore struct {
	existing []models.Indexer
	created  []models.Indexer
	updated  []models.Indexer
	deleted  []int64
	nextID   int64
}

func (f *fakeIndexerStore) ListByProwlarrInstance(_ context.Context, _ int64) ([]models.Indexer, error) {
	return f.existing, nil
}

func (f *fakeIndexerStore) Create(_ context.Context, idx *models.Indexer) error {
	f.nextID++
	idx.ID = f.nextID
	f.created = append(f.created, *idx)
	return nil
}

func (f *fakeIndexerStore) Update(_ context.Context, idx *models.Indexer) error {
	f.updated = append(f.updated, *idx)
	return nil
}

func (f *fakeIndexerStore) Delete(_ context.Context, id int64) error {
	f.deleted = append(f.deleted, id)
	return nil
}

type fakeInstanceStore struct{}

func (fakeInstanceStore) SetLastSyncAt(_ context.Context, _ int64, _ time.Time) error {
	return nil
}

// prowlarrStub serves a canned /api/v1/indexer response.
func prowlarrStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/indexer" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestIndexerTypeForProtocol(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"usenet", "newznab"},
		{"torrent", "torznab"},
		{"", "torznab"},
		{"unknown", "torznab"},
	}
	for _, c := range cases {
		if got := indexerTypeForProtocol(c.in); got != c.want {
			t.Errorf("indexerTypeForProtocol(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSyncer_CreatesNewznabForUsenetIndexers(t *testing.T) {
	srv := prowlarrStub(t, `[
		{"id":1,"name":"NZBHydra","protocol":"usenet","supportsSearch":true,"categories":[{"id":7020}]},
		{"id":2,"name":"PrivateTracker","protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}]}
	]`)
	defer srv.Close()

	store := &fakeIndexerStore{}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	if _, err := syncer.Sync(context.Background(), 1); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.created) != 2 {
		t.Fatalf("created = %d, want 2", len(store.created))
	}
	gotType := map[string]string{}
	for _, c := range store.created {
		gotType[c.Name] = c.Type
	}
	if gotType["NZBHydra"] != "newznab" {
		t.Errorf("NZBHydra type = %q, want newznab (regressing #320 would set this to torznab and route NZBs to qBittorrent)", gotType["NZBHydra"])
	}
	if gotType["PrivateTracker"] != "torznab" {
		t.Errorf("PrivateTracker type = %q, want torznab", gotType["PrivateTracker"])
	}
}

func TestSyncer_CorrectsMisTypedExistingIndexer(t *testing.T) {
	// Simulate a row created by the old buggy syncer: a usenet indexer stored
	// with Type="torznab". Next sync must flip it to "newznab".
	pID := 42
	instID := int64(1)
	existing := []models.Indexer{{
		ID:                 10,
		Name:               "NZBHydra",
		Type:               "torznab",
		URL:                "http://p/42/api",
		ProwlarrInstanceID: &instID,
		ProwlarrIndexerID:  &pID,
	}}
	srv := prowlarrStub(t, `[{"id":42,"name":"NZBHydra","protocol":"usenet","supportsSearch":true,"categories":[{"id":7020}]}]`)
	defer srv.Close()

	store := &fakeIndexerStore{existing: existing}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	if _, err := syncer.Sync(context.Background(), 1); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.updated) != 1 {
		t.Fatalf("updated = %d, want 1", len(store.updated))
	}
	if store.updated[0].Type != "newznab" {
		t.Errorf("Type = %q, want newznab", store.updated[0].Type)
	}
}

func TestSyncer_NoUpdateWhenNothingChanged(t *testing.T) {
	pID := 7
	instID := int64(1)
	existing := []models.Indexer{{
		ID:   11,
		Name: "TrackerX",
		Type: "torznab",
		// URL is computed as {base}/{id}/api by the client; match it below.
		ProwlarrInstanceID: &instID,
		ProwlarrIndexerID:  &pID,
	}}
	srv := prowlarrStub(t, `[{"id":7,"name":"TrackerX","protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}]}]`)
	defer srv.Close()
	existing[0].URL = srv.URL + "/7/api"

	store := &fakeIndexerStore{existing: existing}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	if _, err := syncer.Sync(context.Background(), 1); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.updated) != 0 {
		t.Errorf("expected no updates, got %+v", store.updated)
	}
}
