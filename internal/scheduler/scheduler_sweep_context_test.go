package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// sweepContextScheduler seeds one indexer, one blocklist entry, one delay
// profile and the preferred-language setting, and returns a Scheduler wired to
// those repos.
func sweepContextScheduler(t *testing.T) *Scheduler {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()

	indexers := db.NewIndexerRepo(database)
	if err := indexers.Create(ctx, &models.Indexer{
		Name: "mock", Type: "newznab", URL: "http://x", APIKey: "k", Enabled: true, Priority: 25,
	}); err != nil {
		t.Fatalf("create indexer: %v", err)
	}

	blocklist := db.NewBlocklistRepo(database)
	if err := blocklist.Create(ctx, &models.BlocklistEntry{
		GUID: "blocked-guid", Title: "Blocked Release", Reason: "test",
	}); err != nil {
		t.Fatalf("create blocklist entry: %v", err)
	}

	// The migrations seed one delay profile, which is what the sweep reads.
	delayProfiles := db.NewDelayProfileRepo(database)
	if profiles, err := delayProfiles.List(ctx); err != nil || len(profiles) == 0 {
		t.Fatalf("expected a seeded delay profile: got %d, err %v", len(profiles), err)
	}

	settings := db.NewSettingsRepo(database)
	if err := settings.Set(ctx, "search.preferredLanguage", "eng"); err != nil {
		t.Fatalf("seed search.preferredLanguage: %v", err)
	}

	return &Scheduler{
		indexers:      indexers,
		blocklist:     blocklist,
		delayProfiles: delayProfiles,
		settings:      settings,
	}
}

// detachRepos removes every repo the sweep snapshot covers. An accessor that
// still answers correctly afterwards can only be reading the snapshot, which is
// what "loaded once per sweep, not once per book" means in practice (#2370).
func detachRepos(s *Scheduler) {
	s.indexers = nil
	s.blocklist = nil
	s.delayProfiles = nil
	s.settings = nil
}

// TestSweepContext_ServesEveryBookFromOneLoad asserts the snapshot answers all
// four reads with the repos detached, so nothing in the per-book path goes back
// to the database once a sweep has loaded them.
func TestSweepContext_ServesEveryBookFromOneLoad(t *testing.T) {
	ctx := context.Background()
	s := sweepContextScheduler(t)

	sweep := s.newSweepContext(ctx)
	if sweep == nil {
		t.Fatal("newSweepContext returned nil with every repo readable")
	}
	detachRepos(s)

	if idxs, err := s.sweepIndexers(ctx, sweep); err != nil || len(idxs) != 1 {
		t.Errorf("sweepIndexers from snapshot: got %d indexers, err %v; want 1, nil", len(idxs), err)
	}
	entries, ok := s.sweepBlocklist(ctx, sweep)
	if !ok || len(entries) != 1 || entries[0].GUID != "blocked-guid" {
		t.Errorf("sweepBlocklist from snapshot: got %v (ok=%v), want the one seeded entry", entries, ok)
	}
	if profiles := s.sweepDelayProfiles(ctx, sweep); len(profiles) != 1 {
		t.Errorf("sweepDelayProfiles from snapshot: got %d, want 1", len(profiles))
	}
	if lang := s.sweepPreferredLanguage(ctx, sweep); lang != "eng" {
		t.Errorf("sweepPreferredLanguage from snapshot: got %q, want %q", lang, "eng")
	}
}

// TestSweepContext_NilLoadsPerCall pins the other half of the contract: a
// one-off search (on-add hook, stall re-search, API) passes no snapshot and
// still reads each value from its repo, exactly as before.
func TestSweepContext_NilLoadsPerCall(t *testing.T) {
	ctx := context.Background()
	s := sweepContextScheduler(t)

	if idxs, err := s.sweepIndexers(ctx, nil); err != nil || len(idxs) != 1 {
		t.Errorf("sweepIndexers with no snapshot: got %d indexers, err %v; want 1, nil", len(idxs), err)
	}
	if entries, ok := s.sweepBlocklist(ctx, nil); !ok || len(entries) != 1 {
		t.Errorf("sweepBlocklist with no snapshot: got %d (ok=%v), want 1, true", len(entries), ok)
	}
	if profiles := s.sweepDelayProfiles(ctx, nil); len(profiles) != 1 {
		t.Errorf("sweepDelayProfiles with no snapshot: got %d, want 1", len(profiles))
	}
	if lang := s.sweepPreferredLanguage(ctx, nil); lang != "eng" {
		t.Errorf("sweepPreferredLanguage with no snapshot: got %q, want %q", lang, "eng")
	}

	// With the repos gone the same calls degrade to empty rather than panicking,
	// which is what the many scheduler tests that build a bare Scheduler rely on.
	detachRepos(s)
	if idxs, err := s.sweepIndexers(ctx, nil); err != nil || len(idxs) != 0 {
		t.Errorf("sweepIndexers with no repo: got %d, err %v; want 0, nil", len(idxs), err)
	}
	if _, ok := s.sweepBlocklist(ctx, nil); ok {
		t.Error("sweepBlocklist with no repo reported the list as readable; the spec must not be built")
	}
	if profiles := s.sweepDelayProfiles(ctx, nil); len(profiles) != 0 {
		t.Errorf("sweepDelayProfiles with no repo: got %d, want 0", len(profiles))
	}
	if lang := s.sweepPreferredLanguage(ctx, nil); lang != "" {
		t.Errorf("sweepPreferredLanguage with no repo: got %q, want empty", lang)
	}
}

// TestSweepContext_UnreadableBlocklistSkipsTheSpec pins the distinction the
// snapshot has to preserve: an empty blocklist still builds the spec, but a
// blocklist that could not be read must not, because an empty spec would read
// as "nothing is blocked".
func TestSweepContext_UnreadableBlocklistSkipsTheSpec(t *testing.T) {
	s := &Scheduler{}
	if _, ok := s.sweepBlocklist(context.Background(), &sweepContext{}); ok {
		t.Error("an unloaded blocklist reported as readable")
	}
	if _, ok := s.sweepBlocklist(context.Background(), &sweepContext{blocklistLoaded: true}); !ok {
		t.Error("an empty but readable blocklist reported as unreadable")
	}
}
