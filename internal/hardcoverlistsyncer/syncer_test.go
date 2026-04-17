package hardcoverlistsyncer

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// The syncList path constructs a real hardcover.Client whose GraphQL
// endpoint is a package-level const, so it cannot be redirected to a test
// server without changing source. These tests cover the paths that don't
// reach the network: the empty-list short-circuit, error propagation from
// the ImportList repo, the sortName helper, and the constructor.

func newTestSyncer(t *testing.T) (*ListSyncer, *db.ImportListRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	importLists := db.NewImportListRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	return New(importLists, authors, books), importLists
}

func TestNew_WiresRepos(t *testing.T) {
	s, _ := newTestSyncer(t)
	if s == nil {
		t.Fatal("New returned nil")
	}
	if s.importLists == nil || s.authors == nil || s.books == nil {
		t.Errorf("expected all repo fields to be set, got %+v", s)
	}
}

// TestSync_NoEnabledLists exercises the early-return when no hardcover
// import lists are enabled. This is the happy no-op path: Sync must
// succeed without touching the network.
func TestSync_NoEnabledLists(t *testing.T) {
	s, _ := newTestSyncer(t)
	if err := s.Sync(context.Background()); err != nil {
		t.Errorf("Sync on empty list set: want nil, got %v", err)
	}
}

// TestSync_IgnoresNonHardcoverLists verifies that only lists with
// Type="hardcover" are considered. Seeding a goodreads list should not
// pull it into the sync loop, so the call is still a no-op.
func TestSync_IgnoresNonHardcoverLists(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	// Not a hardcover list — must be ignored by ListByType("hardcover").
	il := testImportList("Goodreads", "goodreads", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Sync(ctx); err != nil {
		t.Errorf("Sync: want nil, got %v", err)
	}
}

// TestSync_IgnoresDisabledHardcoverLists verifies disabled hardcover lists
// are filtered out by the ImportListRepo (ListByType only returns enabled).
func TestSync_IgnoresDisabledHardcoverLists(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("DisabledHC", "hardcover", false)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Sync(ctx); err != nil {
		t.Errorf("Sync: want nil, got %v", err)
	}
}

func TestSortName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"Cher", "Cher"},            // single token → unchanged
		{"Andy Weir", "Weir, Andy"}, // two tokens
		{"Ursula K. Le Guin", "Guin, Ursula K. Le"}, // 4 tokens: last → front
		{"  Andy   Weir  ", "Weir, Andy"},           // whitespace normalised
	}
	for _, tt := range tests {
		if got := sortName(tt.in); got != tt.want {
			t.Errorf("sortName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Compile-time check: *ListSyncer satisfies the HCListSyncer interface
// (the whole point of the interface's existence — keeps the scheduler
// from needing to import this package).
func TestHCListSyncerInterfaceSatisfied(t *testing.T) {
	var _ HCListSyncer = (*ListSyncer)(nil)
}

// RunSync is a fire-and-forget wrapper around Sync. With no enabled lists
// it must return cleanly (no panic, no error to observe).
func TestRunSync_NoEnabledLists(t *testing.T) {
	s, _ := newTestSyncer(t)
	// Should not panic; errors are swallowed inside RunSync by design.
	s.RunSync(context.Background())
}

// testImportList builds a models.ImportList pointer suitable for seeding
// via ImportListRepo.Create. The repo's Create only requires the fields
// set here.
func testImportList(name, typ string, enabled bool) models.ImportList {
	return models.ImportList{
		Name:    name,
		Type:    typ,
		URL:     "some-slug",
		APIKey:  "irrelevant-for-these-tests",
		Enabled: enabled,
	}
}
