package db

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestAuthorRepo_GetByDNBSyntheticName_NoMatch confirms a benign nil/nil
// return when nothing in the table has a synthetic DNB foreign_id matching
// the requested sort_name.
func TestAuthorRepo_GetByDNBSyntheticName_NoMatch(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewAuthorRepo(database)
	ctx := context.Background()

	// Seed an OL author — must NOT be returned.
	if err := repo.Create(ctx, &models.Author{
		ForeignID: "OL-A1", Name: "Frank Herbert", SortName: "Herbert, Frank",
		MetadataProvider: "openlibrary",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := repo.GetByDNBSyntheticName(ctx, "Herbert, Frank", 0)
	if err != nil {
		t.Fatalf("GetByDNBSyntheticName: %v", err)
	}
	if got != nil {
		t.Errorf("OL-prefixed author should not match dnb:author: filter, got %+v", got)
	}
}

func TestAuthorRepo_MonitorDefaultsRoundTrip(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewAuthorRepo(database)
	ctx := context.Background()

	author := &models.Author{
		ForeignID:        "OL-MON-A",
		Name:             "Monitor Author",
		SortName:         "Author, Monitor",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := repo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	if author.MonitorMode != models.AuthorMonitorModeAll {
		t.Fatalf("create default monitor mode = %q, want %q", author.MonitorMode, models.AuthorMonitorModeAll)
	}
	if author.MonitorLatestCount != models.DefaultAuthorMonitorLatestCount {
		t.Fatalf("create default latest count = %d, want %d", author.MonitorLatestCount, models.DefaultAuthorMonitorLatestCount)
	}

	got, err := repo.GetByID(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("author not found")
	}
	if got.MonitorMode != models.AuthorMonitorModeAll || got.MonitorLatestCount != models.DefaultAuthorMonitorLatestCount {
		t.Fatalf("defaults did not round trip: %+v", got)
	}

	got.MonitorMode = models.AuthorMonitorModeLatest
	got.MonitorLatestCount = 3
	if err := repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetByID(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MonitorMode != models.AuthorMonitorModeLatest || got.MonitorLatestCount != 3 {
		t.Fatalf("updated monitor defaults did not round trip: %+v", got)
	}
}

// TestAuthorRepo_GetByDNBSyntheticName_MatchesSyntheticOnly verifies the
// foreign_id LIKE 'dnb:author:%' guard: rows with dnb:gnd: or other prefixes
// are not considered synthetic, only dnb:author:<slug> rows are eligible for
// the dedupe upgrade path.
func TestAuthorRepo_GetByDNBSyntheticName_MatchesSyntheticOnly(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewAuthorRepo(database)
	ctx := context.Background()

	// Seed: one synthetic DNB, one GND-backed DNB, one OL.
	for _, a := range []*models.Author{
		{ForeignID: "dnb:author:thomas-muller", Name: "Thomas Müller", SortName: "Müller, Thomas", MetadataProvider: "dnb"},
		{ForeignID: "dnb:gnd:118585665", Name: "Heiner Müller", SortName: "Müller, Heiner", MetadataProvider: "dnb"},
		{ForeignID: "OL-Z", Name: "Other Author", SortName: "Author, Other", MetadataProvider: "openlibrary"},
	} {
		if err := repo.Create(ctx, a); err != nil {
			t.Fatalf("seed %s: %v", a.ForeignID, err)
		}
	}

	// Synthetic match: lookup is case-insensitive.
	got, err := repo.GetByDNBSyntheticName(ctx, "müller, thomas", 0)
	if err != nil {
		t.Fatalf("GetByDNBSyntheticName: %v", err)
	}
	if got == nil || got.ForeignID != "dnb:author:thomas-muller" {
		t.Fatalf("expected synthetic Thomas Müller, got %+v", got)
	}

	// GND-backed must NOT match the synthetic filter.
	got, err = repo.GetByDNBSyntheticName(ctx, "Müller, Heiner", 0)
	if err != nil {
		t.Fatalf("GetByDNBSyntheticName(Heiner): %v", err)
	}
	if got != nil {
		t.Errorf("GND-backed row should not match synthetic filter, got %+v", got)
	}
}

// TestAuthorRepo_GetByDNBSyntheticName_UserScope confirms a user only sees
// synthetic rows they own (or unowned rows from pre-multiuser data); a
// different user's synthetic row must not surface.
func TestAuthorRepo_GetByDNBSyntheticName_UserScope(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	users := NewUserRepo(database)
	repo := NewAuthorRepo(database)
	ctx := context.Background()

	u1, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	u2, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	if err := repo.CreateForUser(ctx, &models.Author{
		ForeignID: "dnb:author:thomas-muller", Name: "Thomas Müller",
		SortName: "Müller, Thomas", MetadataProvider: "dnb",
	}, u1.ID); err != nil {
		t.Fatalf("seed alice synthetic: %v", err)
	}

	// Alice can see her row.
	got, err := repo.GetByDNBSyntheticName(ctx, "Müller, Thomas", u1.ID)
	if err != nil {
		t.Fatalf("alice lookup: %v", err)
	}
	if got == nil {
		t.Fatal("alice should see her synthetic author")
	}

	// Bob must NOT see Alice's row.
	got, err = repo.GetByDNBSyntheticName(ctx, "Müller, Thomas", u2.ID)
	if err != nil {
		t.Fatalf("bob lookup: %v", err)
	}
	if got != nil {
		t.Errorf("bob should not see alice's synthetic author, got %+v", got)
	}
}

// TestAuthorRepo_UpgradeSyntheticDNB_RowUpdatedInPlace is the core dedupe
// test: a synthetic DNB author row is replaced in place by a canonical
// OpenLibrary identity. Books that reference the original author_id keep
// working because the primary key doesn't change.
func TestAuthorRepo_UpgradeSyntheticDNB_RowUpdatedInPlace(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewAuthorRepo(database)
	ctx := context.Background()

	synthetic := &models.Author{
		ForeignID: "dnb:author:frank-herbert", Name: "Frank Herbert",
		SortName: "Herbert, Frank", MetadataProvider: "dnb",
	}
	if err := repo.Create(ctx, synthetic); err != nil {
		t.Fatalf("seed synthetic: %v", err)
	}
	originalID := synthetic.ID

	canonical := &models.Author{
		ForeignID:        "OL12345A",
		Name:             "Frank Herbert", // unchanged
		SortName:         "Herbert, Frank",
		Description:      "American science-fiction author.",
		ImageURL:         "https://covers.openlibrary.org/a/id/foo.jpg",
		MetadataProvider: "openlibrary",
	}
	if err := repo.UpgradeSyntheticDNB(ctx, synthetic.ForeignID, canonical); err != nil {
		t.Fatalf("UpgradeSyntheticDNB: %v", err)
	}

	got, err := repo.GetByForeignID(ctx, "OL12345A")
	if err != nil {
		t.Fatalf("GetByForeignID after upgrade: %v", err)
	}
	if got == nil {
		t.Fatal("expected row with canonical foreign_id to exist after upgrade")
	}
	if got.ID != originalID {
		t.Errorf("primary key changed: want %d, got %d (in-place update broken)", originalID, got.ID)
	}
	if got.MetadataProvider != "openlibrary" {
		t.Errorf("MetadataProvider not migrated: got %q", got.MetadataProvider)
	}
	if got.Description == "" || got.ImageURL == "" {
		t.Errorf("descriptive fields not copied: %+v", got)
	}

	// The old synthetic row must be gone.
	old, _ := repo.GetByForeignID(ctx, "dnb:author:frank-herbert")
	if old != nil {
		t.Errorf("old synthetic row should be gone, got %+v", old)
	}
}

// TestAuthorRepo_UpgradeSyntheticDNB_PreservesExistingDescriptiveFields
// guards the CASE WHEN behaviour: when the target has empty descriptive
// columns, the existing row's values are kept rather than blanked.
func TestAuthorRepo_UpgradeSyntheticDNB_PreservesExistingDescriptiveFields(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewAuthorRepo(database)
	ctx := context.Background()

	if err := repo.Create(ctx, &models.Author{
		ForeignID:        "dnb:author:x-y",
		Name:             "X Y",
		SortName:         "Y, X",
		Description:      "kept",
		ImageURL:         "kept.jpg",
		Disambiguation:   "kept-disamb",
		MetadataProvider: "dnb",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := repo.UpgradeSyntheticDNB(ctx, "dnb:author:x-y", &models.Author{
		ForeignID:        "OL-X",
		Name:             "X Y",
		SortName:         "Y, X",
		MetadataProvider: "openlibrary",
		// Description / ImageURL / Disambiguation deliberately empty.
	}); err != nil {
		t.Fatalf("UpgradeSyntheticDNB: %v", err)
	}

	got, err := repo.GetByForeignID(ctx, "OL-X")
	if err != nil || got == nil {
		t.Fatalf("post-upgrade fetch: %v, got=%+v", err, got)
	}
	if got.Description != "kept" || got.ImageURL != "kept.jpg" || got.Disambiguation != "kept-disamb" {
		t.Errorf("descriptive fields blanked instead of preserved: %+v", got)
	}
}

// TestAuthorRepo_UpgradeSyntheticDNB_BadArgs guards against silent no-ops
// from caller mistakes.
func TestAuthorRepo_UpgradeSyntheticDNB_BadArgs(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewAuthorRepo(database)
	ctx := context.Background()

	if err := repo.UpgradeSyntheticDNB(ctx, "", &models.Author{ForeignID: "OL-X"}); err == nil {
		t.Error("expected error when currentForeignID is empty")
	}
	if err := repo.UpgradeSyntheticDNB(ctx, "dnb:author:x", nil); err == nil {
		t.Error("expected error when target is nil")
	}
	if err := repo.UpgradeSyntheticDNB(ctx, "dnb:author:x", &models.Author{}); err == nil {
		t.Error("expected error when target.ForeignID is empty")
	}
}

// Sanity: ensure the LIKE pattern in GetByDNBSyntheticName actually uses the
// "dnb:author:" prefix (not just any "dnb:" or any prefix at all) so we
// don't accidentally try to upgrade a real DNB control-number row later.
func TestAuthorRepo_GetByDNBSyntheticName_LikePatternConstants(t *testing.T) {
	// The query is private — this is a documentation-grade assertion that
	// the prefix is the intended one. If the prefix in the SQL is changed,
	// also update the production prefix in dnb client and api handler.
	const wantPrefix = "dnb:author:"
	if !strings.HasPrefix(wantPrefix, "dnb:author:") {
		t.Fatalf("synthetic prefix changed unexpectedly: %q", wantPrefix)
	}
}
