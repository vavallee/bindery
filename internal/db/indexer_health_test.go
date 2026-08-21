package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func newIndexerForHealth(t *testing.T) (*IndexerRepo, int64) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	repo := NewIndexerRepo(database)
	idx := &models.Indexer{
		Name: "Tracker", Type: "newznab", URL: "https://example.invalid",
		APIKey: "k", Categories: []int{7020}, Enabled: true,
	}
	if err := repo.Create(context.Background(), idx); err != nil {
		t.Fatalf("create indexer: %v", err)
	}
	return repo, idx.ID
}

// TestIndexerHealth_RoundTrip covers the #1935 columns: a stored failure comes
// back on the row, and a later success clears it rather than leaving a badge
// describing a problem that is over.
func TestIndexerHealth_RoundTrip(t *testing.T) {
	repo, id := newIndexerForHealth(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	fresh, err := repo.GetByID(ctx, id)
	if err != nil || fresh == nil {
		t.Fatalf("get indexer: %v", err)
	}
	if fresh.LastError != nil || fresh.LastSuccessAt != nil {
		t.Errorf("a new indexer already carries health: %+v", fresh)
	}

	if err := repo.RecordSearchFailure(ctx, id, 101, "indexer error 101: Account suspended", now); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	failed, err := repo.GetByID(ctx, id)
	if err != nil || failed == nil {
		t.Fatalf("get indexer: %v", err)
	}
	if failed.LastError == nil || *failed.LastError == "" {
		t.Fatal("failure was not stored")
	}
	if failed.LastErrorCode == nil || *failed.LastErrorCode != 101 {
		t.Errorf("LastErrorCode = %v, want 101", failed.LastErrorCode)
	}
	if !failed.NeedsAttention() {
		t.Error("a suspended account should need attention")
	}

	if err := repo.RecordSearchSuccess(ctx, id, now.Add(time.Minute)); err != nil {
		t.Fatalf("record success: %v", err)
	}
	recovered, err := repo.GetByID(ctx, id)
	if err != nil || recovered == nil {
		t.Fatalf("get indexer: %v", err)
	}
	if recovered.LastError != nil || recovered.LastErrorCode != nil || recovered.LastFailureAt != nil {
		t.Errorf("success did not clear the failure: %+v", recovered)
	}
	if recovered.LastSuccessAt == nil {
		t.Error("success timestamp was not stored")
	}
}

// TestIndexerHealth_TransportFailureStoresNoCode: 0 means "not a Newznab
// rejection" and must land as NULL rather than as error code zero.
func TestIndexerHealth_TransportFailureStoresNoCode(t *testing.T) {
	repo, id := newIndexerForHealth(t)
	ctx := context.Background()

	if err := repo.RecordSearchFailure(ctx, id, 0, "dial tcp: connection refused", time.Now()); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	got, err := repo.GetByID(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("get indexer: %v", err)
	}
	if got.LastErrorCode != nil {
		t.Errorf("LastErrorCode = %v, want nil", got.LastErrorCode)
	}
	if got.NeedsAttention() {
		t.Error("a transport failure should not be reported as needing a human")
	}
}

// TestIndexerHealth_DoesNotBumpUpdatedAt guards the interaction with #1934: the
// cooldown treats a bumped updated_at as the user having edited the row and
// clears itself. Health is written by the searcher on nobody's schedule, so
// bumping it here would keep undoing the cooldown.
func TestIndexerHealth_DoesNotBumpUpdatedAt(t *testing.T) {
	repo, id := newIndexerForHealth(t)
	ctx := context.Background()

	before, err := repo.GetByID(ctx, id)
	if err != nil || before == nil {
		t.Fatalf("get indexer: %v", err)
	}
	if err := repo.RecordSearchFailure(ctx, id, 500, "rate limited", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	after, err := repo.GetByID(ctx, id)
	if err != nil || after == nil {
		t.Fatalf("get indexer: %v", err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("updated_at moved from %v to %v; that would clear the #1934 cooldown",
			before.UpdatedAt, after.UpdatedAt)
	}
}
