package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func newIndexerForQuota(t *testing.T, limit *int) (*IndexerRepo, int64) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	repo := NewIndexerRepo(database)
	idx := &models.Indexer{
		Name: "Budgeted Tracker", Type: "newznab", URL: "https://example.invalid",
		APIKey: "k", Categories: []int{7020}, Enabled: true, DailyQueryLimit: limit,
	}
	if err := repo.Create(context.Background(), idx); err != nil {
		t.Fatalf("create indexer: %v", err)
	}
	return repo, idx.ID
}

func intp(n int) *int { return &n }

// TestIndexerDailyQueryLimit_RoundTrip covers the #2312 column on every path a
// row can take: created with a cap, read back, updated, and cleared.
func TestIndexerDailyQueryLimit_RoundTrip(t *testing.T) {
	repo, id := newIndexerForQuota(t, intp(500))
	ctx := context.Background()

	got, err := repo.GetByID(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("get indexer: %v", err)
	}
	if got.DailyQueryLimit == nil || *got.DailyQueryLimit != 500 {
		t.Fatalf("DailyQueryLimit = %v, want 500", got.DailyQueryLimit)
	}

	got.DailyQueryLimit = intp(1200)
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("update indexer: %v", err)
	}
	got, _ = repo.GetByID(ctx, id)
	if got.DailyQueryLimit == nil || *got.DailyQueryLimit != 1200 {
		t.Fatalf("after update DailyQueryLimit = %v, want 1200", got.DailyQueryLimit)
	}

	got.DailyQueryLimit = nil
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("clear limit: %v", err)
	}
	got, _ = repo.GetByID(ctx, id)
	if got.DailyQueryLimit != nil {
		t.Fatalf("cleared limit came back as %v", *got.DailyQueryLimit)
	}

	// List reads its own column set, and a column missing from any one of the
	// three SELECTs is how a cap silently stops being enforced.
	got.DailyQueryLimit = intp(42)
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("reset limit: %v", err)
	}
	all, err := repo.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list indexers: %v (%d rows)", err, len(all))
	}
	if all[0].DailyQueryLimit == nil || *all[0].DailyQueryLimit != 42 {
		t.Errorf("List dropped the daily query limit: %v", all[0].DailyQueryLimit)
	}

	// ListByProwlarrInstance is the third SELECT, and the one the Prowlarr
	// syncer reads before writing the row back. A cap missing from it would be
	// wiped on the next sync.
	now := time.Now().UTC()
	res, err := repo.db.ExecContext(ctx,
		"INSERT INTO prowlarr_instances (name, url, api_key, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		"Prowlarr", "https://prowlarr.invalid", "k", now, now)
	if err != nil {
		t.Fatalf("create prowlarr instance: %v", err)
	}
	instID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("prowlarr instance id: %v", err)
	}
	// Update deliberately does not write the prowlarr columns, so set them
	// directly rather than through the repo.
	if _, err := repo.db.ExecContext(ctx,
		"UPDATE indexers SET prowlarr_instance_id=?, prowlarr_indexer_id=? WHERE id=?", instID, 7, id); err != nil {
		t.Fatalf("set prowlarr ids: %v", err)
	}
	byInstance, err := repo.ListByProwlarrInstance(ctx, instID)
	if err != nil || len(byInstance) != 1 {
		t.Fatalf("list by prowlarr instance: %v (%d rows)", err, len(byInstance))
	}
	if byInstance[0].DailyQueryLimit == nil || *byInstance[0].DailyQueryLimit != 42 {
		t.Errorf("ListByProwlarrInstance dropped the daily query limit: %v", byInstance[0].DailyQueryLimit)
	}
}

// TestIndexerQueryCounts_RoundTrip covers the bucket store: an upsert replaces
// rather than duplicates, and the window sums only what is inside it.
func TestIndexerQueryCounts_RoundTrip(t *testing.T) {
	repo, id := newIndexerForQuota(t, intp(100))
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 12, 30, 0, 0, time.UTC)

	if err := repo.AddQueryCount(ctx, id, now, 5); err != nil {
		t.Fatalf("add count: %v", err)
	}
	if err := repo.AddQueryCount(ctx, id, now.Add(20*time.Minute), 9); err != nil {
		t.Fatalf("add to the same bucket: %v", err)
	}
	if err := repo.AddQueryCount(ctx, id, now.Add(-3*time.Hour), 4); err != nil {
		t.Fatalf("add to an earlier bucket: %v", err)
	}
	// A zero or negative delta is a no-op rather than a row.
	if err := repo.AddQueryCount(ctx, id, now.Add(-6*time.Hour), 0); err != nil {
		t.Fatalf("zero delta: %v", err)
	}

	usage, err := repo.QueryUsage(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("query usage: %v", err)
	}
	// The two writes 20 minutes apart land in the same 12:00 bucket and add up,
	// which is what makes a flush safe for a process that never managed to read
	// the stored value first. Plus 4 in the 09:00 bucket.
	if usage[id] != 18 {
		t.Errorf("usage = %d, want 18", usage[id])
	}

	loaded, err := repo.LoadQueryCounts(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("load counts: %v", err)
	}
	if len(loaded[id]) != 2 {
		t.Errorf("loaded %d buckets, want 2 (the zero delta must not have created one)", len(loaded[id]))
	}
	if loaded[id][TruncateQueryHour(now)] != 14 {
		t.Errorf("current bucket = %d, want 14", loaded[id][TruncateQueryHour(now)])
	}
}

// TestIndexerQueryCounts_Prune keeps the table from growing one row per indexer
// per hour forever.
func TestIndexerQueryCounts_Prune(t *testing.T) {
	repo, id := newIndexerForQuota(t, intp(100))
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	if err := repo.AddQueryCount(ctx, id, now.Add(-30*time.Hour), 7); err != nil {
		t.Fatalf("add old bucket: %v", err)
	}
	if err := repo.AddQueryCount(ctx, id, now, 3); err != nil {
		t.Fatalf("add current bucket: %v", err)
	}
	if err := repo.PruneQueryCounts(ctx, now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("prune: %v", err)
	}

	loaded, err := repo.LoadQueryCounts(ctx, now.Add(-72*time.Hour))
	if err != nil {
		t.Fatalf("load counts: %v", err)
	}
	if len(loaded[id]) != 1 {
		t.Fatalf("prune left %d buckets, want 1", len(loaded[id]))
	}
	if loaded[id][TruncateQueryHour(now)] != 3 {
		t.Errorf("prune removed the wrong bucket: %v", loaded[id])
	}
}

// TestIndexerQueryCounts_CascadeOnDelete: deleting an indexer must not leave
// orphan buckets that a recycled row id would inherit as a spent budget.
func TestIndexerQueryCounts_CascadeOnDelete(t *testing.T) {
	repo, id := newIndexerForQuota(t, intp(100))
	ctx := context.Background()
	now := time.Now().UTC()

	if err := repo.AddQueryCount(ctx, id, now, 5); err != nil {
		t.Fatalf("add count: %v", err)
	}
	if err := repo.Delete(ctx, id); err != nil {
		t.Fatalf("delete indexer: %v", err)
	}
	usage, err := repo.QueryUsage(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("query usage: %v", err)
	}
	if _, ok := usage[id]; ok {
		t.Errorf("query counts outlived their indexer: %v", usage)
	}
}

// TestIndexerQueryCount_DoesNotBumpUpdatedAt mirrors
// TestIndexerHealth_DoesNotBumpUpdatedAt and exists for the same reason: the
// #1934 cooldown reads a newer updated_at as "the user edited this row, try
// again", so a counter written on a sweep's schedule would keep cancelling
// cooldowns. This is the pin that catches anyone moving the counts onto the
// indexers row.
func TestIndexerQueryCount_DoesNotBumpUpdatedAt(t *testing.T) {
	repo, id := newIndexerForQuota(t, intp(100))
	ctx := context.Background()

	before, err := repo.GetByID(ctx, id)
	if err != nil || before == nil {
		t.Fatalf("get indexer: %v", err)
	}
	now := time.Now().UTC().Add(time.Hour)
	if err := repo.AddQueryCount(ctx, id, now, 12); err != nil {
		t.Fatalf("add count: %v", err)
	}
	if err := repo.PruneQueryCounts(ctx, now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("prune: %v", err)
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
