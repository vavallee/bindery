package prowlarr

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestSyncResult_String(t *testing.T) {
	r := SyncResult{Added: 2, Updated: 1, Removed: 3}
	want := "added=2 updated=1 removed=3"
	if got := r.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestSyncer_FetchError(t *testing.T) {
	srv := prowlarrStub(t, `not json`) // triggers decode error in FetchIndexers
	defer srv.Close()

	store := &fakeIndexerStore{}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	_, err := syncer.Sync(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error when Prowlarr returns bad JSON, got nil")
	}
}

func TestSyncer_ListError(t *testing.T) {
	srv := prowlarrStub(t, `[{"id":1,"name":"T","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}]}]`)
	defer srv.Close()

	store := &fakeIndexerStore{listErr: errors.New("db failure")}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	_, err := syncer.Sync(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error when list fails, got nil")
	}
}

func TestSyncer_SkipsIndexerWithNoBookCategories(t *testing.T) {
	// Issue #675: an indexer with no ebook/audiobook categories must be
	// skipped, not auto-promoted into Bindery with a fake [7020] category.
	srv := prowlarrStub(t, `[{"id":5,"name":"Tracker","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[]}]`)
	defer srv.Close()

	store := &fakeIndexerStore{}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	if _, err := syncer.Sync(context.Background(), 1); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.created) != 0 {
		t.Errorf("expected 0 created (no book/audiobook categories), got %d", len(store.created))
	}
}

func TestSyncer_SkipsDisabledIndexer(t *testing.T) {
	// Issue #675: an indexer disabled in Prowlarr must not be added to Bindery.
	srv := prowlarrStub(t, `[{"id":6,"name":"DisabledIdx","enable":false,"protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}]}]`)
	defer srv.Close()

	store := &fakeIndexerStore{}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	if _, err := syncer.Sync(context.Background(), 1); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.created) != 0 {
		t.Errorf("expected 0 created (indexer disabled), got %d", len(store.created))
	}
}

func TestSyncer_SkipsIndexerWithoutSearchSupport(t *testing.T) {
	// Issue #675: an indexer without search support is useless for book lookup
	// and must not be added to Bindery.
	srv := prowlarrStub(t, `[{"id":7,"name":"NoSearch","enable":true,"protocol":"torrent","supportsSearch":false,"categories":[{"id":7020}]}]`)
	defer srv.Close()

	store := &fakeIndexerStore{}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	if _, err := syncer.Sync(context.Background(), 1); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.created) != 0 {
		t.Errorf("expected 0 created (no search support), got %d", len(store.created))
	}
}

func TestSyncer_RemovesStaleIndexer(t *testing.T) {
	// Prowlarr returns one indexer; DB has two (one is stale).
	pID1, pID2 := 1, 2
	instID := int64(1)
	existing := []models.Indexer{
		{ID: 10, Name: "Active", Type: "torznab", URL: "http://h/1/api", ProwlarrInstanceID: &instID, ProwlarrIndexerID: &pID1},
		{ID: 11, Name: "Stale", Type: "torznab", URL: "http://h/2/api", ProwlarrInstanceID: &instID, ProwlarrIndexerID: &pID2},
	}
	srv := prowlarrStub(t, `[{"id":1,"name":"Active","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}]}]`)
	defer srv.Close()

	// Update the Active indexer URL to match what the client will compute.
	existing[0].URL = srv.URL + "/1/api"

	store := &fakeIndexerStore{existing: existing}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	result, err := syncer.Sync(context.Background(), 1)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Removed != 1 {
		t.Errorf("Removed = %d, want 1", result.Removed)
	}
	if len(store.deleted) != 1 || store.deleted[0] != 11 {
		t.Errorf("deleted IDs = %v, want [11]", store.deleted)
	}
}

func TestSyncer_CreateErrorIsLogged(t *testing.T) {
	// Create failure must be logged but not returned as an error.
	srv := prowlarrStub(t, `[{"id":3,"name":"New","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}]}]`)
	defer srv.Close()

	store := &fakeIndexerStore{createErr: errors.New("insert failed")}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	result, err := syncer.Sync(context.Background(), 1)
	if err != nil {
		t.Fatalf("expected nil error on create failure, got %v", err)
	}
	if result.Added != 0 {
		t.Errorf("Added = %d, want 0 (create failed)", result.Added)
	}
}

func TestSyncer_DeleteErrorIsLogged(t *testing.T) {
	// Delete failure must be logged but not returned as an error.
	pID := 99
	instID := int64(1)
	existing := []models.Indexer{
		{ID: 20, Name: "Gone", Type: "torznab", URL: "http://h/99/api", ProwlarrInstanceID: &instID, ProwlarrIndexerID: &pID},
	}
	// Prowlarr no longer has this indexer.
	srv := prowlarrStub(t, `[]`)
	defer srv.Close()

	store := &fakeIndexerStore{existing: existing, deleteErr: errors.New("delete failed")}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	result, err := syncer.Sync(context.Background(), 1)
	if err != nil {
		t.Fatalf("expected nil error on delete failure, got %v", err)
	}
	if result.Removed != 0 {
		t.Errorf("Removed = %d, want 0 (delete failed)", result.Removed)
	}
}

func TestSyncer_UpdateErrorIsLogged(t *testing.T) {
	// Update failure must be logged but not returned as an error.
	pID := 7
	instID := int64(1)
	existing := []models.Indexer{
		{ID: 5, Name: "OldName", Type: "torznab", URL: "http://old/7/api",
			ProwlarrInstanceID: &instID, ProwlarrIndexerID: &pID},
	}
	srv := prowlarrStub(t, `[{"id":7,"name":"NewName","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}]}]`)
	defer srv.Close()

	store := &fakeIndexerStore{existing: existing, updateErr: errors.New("update failed")}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	result, err := syncer.Sync(context.Background(), 1)
	if err != nil {
		t.Fatalf("expected nil error on update failure, got %v", err)
	}
	if result.Updated != 0 {
		t.Errorf("Updated = %d, want 0 (update failed)", result.Updated)
	}
}

func TestSyncer_MixedAddUpdateRemove(t *testing.T) {
	pID1, pID2 := 1, 2
	instID := int64(1)
	existing := []models.Indexer{
		{ID: 10, Name: "OldName", Type: "torznab", ProwlarrInstanceID: &instID, ProwlarrIndexerID: &pID1},
		{ID: 11, Name: "Stale", Type: "torznab", ProwlarrInstanceID: &instID, ProwlarrIndexerID: &pID2},
	}
	srv := prowlarrStub(t, `[
		{"id":1,"name":"NewName","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[{"id":7020}]},
		{"id":3,"name":"Added","enable":true,"protocol":"torrent","supportsSearch":true,"categories":[{"id":7000}]}
	]`)
	defer srv.Close()

	// Compute the URL the client will assign so the existing row matches.
	existing[0].URL = srv.URL + "/1/api"

	store := &fakeIndexerStore{existing: existing}
	syncer := NewSyncer(New(srv.URL, "k"), store, fakeInstanceStore{})

	result, err := syncer.Sync(context.Background(), 1)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Added != 1 {
		t.Errorf("Added = %d, want 1", result.Added)
	}
	if result.Updated != 1 {
		t.Errorf("Updated = %d, want 1 (name changed)", result.Updated)
	}
	if result.Removed != 1 {
		t.Errorf("Removed = %d, want 1", result.Removed)
	}
}
