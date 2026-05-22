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
	existing  []models.Indexer
	created   []models.Indexer
	updated   []models.Indexer
	deleted   []int64
	nextID    int64
	listErr   error
	createErr error
	updateErr error
	deleteErr error
}

func (f *fakeIndexerStore) ListByProwlarrInstance(_ context.Context, _ int64) ([]models.Indexer, error) {
	return f.existing, f.listErr
}

func (f *fakeIndexerStore) Create(_ context.Context, idx *models.Indexer) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.nextID++
	idx.ID = f.nextID
	f.created = append(f.created, *idx)
	return nil
}

func (f *fakeIndexerStore) Update(_ context.Context, idx *models.Indexer) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated = append(f.updated, *idx)
	return nil
}

func (f *fakeIndexerStore) Delete(_ context.Context, id int64) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
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

func prowlarrStubWithApplications(t *testing.T, indexerBody, applicationsBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/indexer":
			_, _ = w.Write([]byte(indexerBody))
		case "/api/v1/applications":
			_, _ = w.Write([]byte(applicationsBody))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestFilterCategoriesForMedia(t *testing.T) {
	cases := []struct {
		name string
		in   []int
		want []int
	}{
		{"empty stays empty", []int{}, []int{}},
		{"non-parent passes through", []int{7020, 2000, 5030}, []int{7020, 2000, 5030}},
		{"only 7000 widens to 7020", []int{7000}, []int{7020}},
		{"only 3000 widens to 3030", []int{3000}, []int{3030}},
		{"both parents only widen both", []int{7000, 3000}, []int{7020, 3030}},
		{"7000 with child drops parent", []int{7000, 7020}, []int{7020}},
		{"3000 with child drops parent", []int{3000, 3010}, []int{3010}},
		{"7000 with multiple children drops parent", []int{7000, 7020, 7030}, []int{7020, 7030}},
		{"both parents with children drop both", []int{7000, 7020, 3000, 3010}, []int{7020, 3010}},
		{"7000 with child and 3000 alone widen 3000", []int{7000, 7020, 3000}, []int{7020, 3030}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterCategoriesForMedia(tc.in)
			if len(got) != len(tc.want) {
				t.Errorf("filterCategoriesForMedia(%v) = %v, want %v", tc.in, got, tc.want)
				return
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("filterCategoriesForMedia(%v)[%d] = %d, want %d", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
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
		{"id":1,"name":"NZBHydra","enable":true,"protocol":"usenet","supportsSearch":true,"categories":[{"id":7020}]},
		{"id":2,"name":"PrivateTracker","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}]}
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
	srv := prowlarrStub(t, `[{"id":42,"name":"NZBHydra","enable":true,"protocol":"usenet","supportsSearch":true,"categories":[{"id":7020}]}]`)
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

func TestSyncer_WidensParentOnlyCategory(t *testing.T) {
	// Prowlarr reports [7000] only — syncer must store [7020], not [7000] or [].
	srv := prowlarrStub(t, `[{"id":9,"name":"GenericBooks","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[{"id":7000}]}]`)
	defer srv.Close()

	store := &fakeIndexerStore{}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	if _, err := syncer.Sync(context.Background(), 1); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("created = %d, want 1", len(store.created))
	}
	cats := store.created[0].Categories
	if len(cats) != 1 || cats[0] != 7020 {
		t.Errorf("Categories = %v, want [7020] (7000 must be widened to 7020, never stored raw)", cats)
	}
}

func TestSyncer_PropagatesChangedCategories(t *testing.T) {
	// Existing indexer was stored with the old broad category set [7000, 7020].
	// Prowlarr now reports [7020] only. Re-sync must update Categories to [7020].
	pID := 10
	instID := int64(1)
	existing := []models.Indexer{{
		ID:                 10,
		Name:               "IndexerA",
		Type:               "torznab",
		Categories:         []int{7000, 7020},
		ProwlarrInstanceID: &instID,
		ProwlarrIndexerID:  &pID,
	}}
	srv := prowlarrStub(t, `[{"id":10,"name":"IndexerA","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}]}]`)
	defer srv.Close()
	existing[0].URL = srv.URL + "/10/api"

	store := &fakeIndexerStore{existing: existing}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	if _, err := syncer.Sync(context.Background(), 1); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.updated) != 1 {
		t.Fatalf("expected 1 update (categories changed), got %d updates", len(store.updated))
	}
	want := []int{7020}
	got := store.updated[0].Categories
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("Categories = %v, want %v", got, want)
	}
}

func TestSyncer_NoUpdateWhenNothingChanged(t *testing.T) {
	pID := 7
	instID := int64(1)
	existing := []models.Indexer{{
		ID:         11,
		Name:       "TrackerX",
		Type:       "torznab",
		Categories: []int{7020},
		// URL is computed as {base}/{id}/api by the client; match it below.
		ProwlarrInstanceID: &instID,
		ProwlarrIndexerID:  &pID,
	}}
	srv := prowlarrStub(t, `[{"id":7,"name":"TrackerX","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}]}]`)
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

func TestSyncer_KeepsExistingIndexersWhenSyncMatchesNothing(t *testing.T) {
	// Issue #763: a sync that matches zero indexers (a category-filter
	// regression, a partial Prowlarr response) must not delete the user's
	// existing synced indexers.
	pID := 7
	instID := int64(1)
	existing := []models.Indexer{{
		ID:                 11,
		Name:               "TrackerX",
		Type:               "torznab",
		Categories:         []int{7020},
		ProwlarrInstanceID: &instID,
		ProwlarrIndexerID:  &pID,
	}}
	// Prowlarr returns an indexer the filter rejects (no categories, no
	// capabilities, no applications) — nothing matches.
	srv := prowlarrStub(t, `[{"id":99,"name":"Torrenting","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[]}]`)
	defer srv.Close()

	store := &fakeIndexerStore{existing: existing}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	res, err := syncer.Sync(context.Background(), 1)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.deleted) != 0 {
		t.Errorf("deleted = %v, want none (a zero-match sync must not wipe indexers)", store.deleted)
	}
	if res.Removed != 0 {
		t.Errorf("Removed = %d, want 0", res.Removed)
	}
}

func TestSyncer_RemovesStaleIndexerWhenOthersStillMatch(t *testing.T) {
	// The zero-match guard must not block legitimate removals: when at least
	// one indexer still matches, indexers gone from Prowlarr are deleted.
	matchID, staleID := 7, 8
	instID := int64(1)
	existing := []models.Indexer{
		{ID: 11, Name: "Keep", Type: "torznab", Categories: []int{7020},
			ProwlarrInstanceID: &instID, ProwlarrIndexerID: &matchID},
		{ID: 12, Name: "Gone", Type: "torznab", Categories: []int{7020},
			ProwlarrInstanceID: &instID, ProwlarrIndexerID: &staleID},
	}
	srv := prowlarrStub(t, `[{"id":7,"name":"Keep","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}]}]`)
	defer srv.Close()
	existing[0].URL = srv.URL + "/7/api"

	store := &fakeIndexerStore{existing: existing}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	res, err := syncer.Sync(context.Background(), 1)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != 12 {
		t.Errorf("deleted = %v, want [12]", store.deleted)
	}
	if res.Removed != 1 {
		t.Errorf("Removed = %d, want 1", res.Removed)
	}
}
