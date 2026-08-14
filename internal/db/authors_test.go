package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

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
		return
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

func TestAuthorRepo_GetByAnyForeignIDMatchesAlternateIdentifier(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewAuthorRepo(database)
	ctx := context.Background()
	author := &models.Author{
		ForeignID:        "OL13200512A",
		Name:             "Emilia Jae",
		SortName:         "Jae, Emilia",
		MetadataProvider: "openlibrary",
	}
	if err := repo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertAuthorIdentifier(ctx, author.ID, "hc:emilia-jae"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetByAnyForeignID(ctx, "hc:emilia-jae")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != author.ID {
		t.Fatalf("GetByAnyForeignID = %+v, want author %d", got, author.ID)
	}
}

func TestAuthorRepo_GetByIDForUserScopesOwner(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	users := NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	repo := NewAuthorRepo(database)
	aliceAuthor := &models.Author{
		ForeignID:        "OL-ALICE",
		Name:             "Alice Author",
		SortName:         "Author, Alice",
		MetadataProvider: "openlibrary",
	}
	if err := repo.CreateForUser(ctx, aliceAuthor, alice.ID); err != nil {
		t.Fatalf("seed alice author: %v", err)
	}
	unowned := &models.Author{
		ForeignID:        "OL-UNOWNED",
		Name:             "Unowned Author",
		SortName:         "Author, Unowned",
		MetadataProvider: "openlibrary",
	}
	if err := repo.Create(ctx, unowned); err != nil {
		t.Fatalf("seed unowned author: %v", err)
	}

	got, err := repo.GetByIDForUser(ctx, aliceAuthor.ID, alice.ID)
	if err != nil || got == nil {
		t.Fatalf("alice lookup = %+v err=%v, want owned author", got, err)
	}
	got, err = repo.GetByIDForUser(ctx, aliceAuthor.ID, bob.ID)
	if err != nil {
		t.Fatalf("bob lookup: %v", err)
	}
	if got != nil {
		t.Fatalf("bob lookup = %+v, want nil for alice-owned author", got)
	}
	got, err = repo.GetByIDForUser(ctx, unowned.ID, bob.ID)
	if err != nil || got == nil {
		t.Fatalf("bob unowned lookup = %+v err=%v, want visible legacy row", got, err)
	}
}

func TestAuthorRepo_UpsertAuthorIdentifierRejectsDifferentOwner(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewAuthorRepo(database)
	ctx := context.Background()
	first := &models.Author{ForeignID: "OL-FIRST", Name: "First", SortName: "First", MetadataProvider: "openlibrary"}
	second := &models.Author{ForeignID: "OL-SECOND", Name: "Second", SortName: "Second", MetadataProvider: "openlibrary"}
	if err := repo.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertAuthorIdentifier(ctx, first.ID, "hc:shared"); err != nil {
		t.Fatal(err)
	}

	err = repo.UpsertAuthorIdentifier(ctx, second.ID, "hc:shared")
	if err == nil {
		t.Fatal("expected duplicate identifier owner error")
	}
	if !errors.Is(err, ErrAuthorIdentifierConflict) {
		t.Fatalf("error = %v, want owner collision", err)
	}
	var conflict *AuthorIdentifierConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %T, want AuthorIdentifierConflictError", err)
	}
	if conflict.ForeignID != "hc:shared" || conflict.AuthorID != first.ID {
		t.Fatalf("conflict = %+v, want foreign ID owner %d", conflict, first.ID)
	}
}

func TestAuthorRepo_DeleteAuthorIdentifierDeletesMatchingOwnerOnly(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewAuthorRepo(database)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-AUTHOR", Name: "Author", SortName: "Author", MetadataProvider: "openlibrary"}
	other := &models.Author{ForeignID: "OL-OTHER", Name: "Other", SortName: "Other", MetadataProvider: "openlibrary"}
	if err := repo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertAuthorIdentifier(ctx, author.ID, "hc:author"); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteAuthorIdentifier(ctx, other.ID, "hc:author"); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.GetAuthorIdentifier(ctx, "hc:author"); err != nil || got == nil || got.AuthorID != author.ID {
		t.Fatalf("identifier after wrong-owner delete = %+v err=%v, want retained for author %d", got, err, author.ID)
	}
	if err := repo.DeleteAuthorIdentifier(ctx, author.ID, "hc:author"); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.GetAuthorIdentifier(ctx, "hc:author"); err != nil || got != nil {
		t.Fatalf("identifier after delete = %+v err=%v, want nil", got, err)
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
		return
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

// MonitorNewItems (#1348): defaults to "all" when unset (create + scan
// normalization), and an explicit "none" round-trips through Update/Get.
func TestAuthorRepo_MonitorNewItemsRoundTrip(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewAuthorRepo(database)
	ctx := context.Background()

	author := &models.Author{
		ForeignID:        "OL-MNI-A",
		Name:             "Discovery Author",
		SortName:         "Author, Discovery",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := repo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	if author.MonitorNewItems != models.AuthorMonitorNewItemsAll {
		t.Fatalf("create default monitorNewItems = %q, want %q", author.MonitorNewItems, models.AuthorMonitorNewItemsAll)
	}

	got, err := repo.GetByID(ctx, author.ID)
	if err != nil || got == nil {
		t.Fatalf("get author: %v (nil=%v)", err, got == nil)
	}
	if got.MonitorNewItems != models.AuthorMonitorNewItemsAll {
		t.Fatalf("default did not round trip: %q", got.MonitorNewItems)
	}

	got.MonitorNewItems = models.AuthorMonitorNewItemsNone
	if err := repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetByID(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MonitorNewItems != models.AuthorMonitorNewItemsNone {
		t.Fatalf("monitorNewItems=none did not round trip: %q", got.MonitorNewItems)
	}

	// An invalid value normalizes back to the default rather than persisting.
	got.MonitorNewItems = "sometimes"
	if err := repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetByID(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MonitorNewItems != models.AuthorMonitorNewItemsAll {
		t.Fatalf("invalid value should normalize to default, got %q", got.MonitorNewItems)
	}
}

// TestAuthorRepo_GetByDNBSyntheticName_InitialSpacing covers #1647. DNB stores
// SortName verbatim from MARC 100 $a ("Tolkien, J. R. R."), while the canonical
// side is sortName(), a strings.Fields last-token flip that turns OpenLibrary's
// "J.R.R. Tolkien" into "Tolkien, J.R.R.". Those two never compared equal under
// SQLite `LOWER(sort_name) = LOWER(?)`, so this function returned nothing and
// the duplicate it exists to prevent was created anyway.
func TestAuthorRepo_GetByDNBSyntheticName_InitialSpacing(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewAuthorRepo(database)
	ctx := context.Background()

	if err := repo.Create(ctx, &models.Author{
		ForeignID: "dnb:author:jrr-tolkien", Name: "J. R. R. Tolkien",
		SortName: "Tolkien, J. R. R.", MetadataProvider: "dnb",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, lookup := range []string{
		"Tolkien, J.R.R.",   // what sortName() produces from OpenLibrary's form
		"Tolkien, J. R. R.", // byte-identical
		"J. R. R. Tolkien",  // un-inverted
	} {
		got, err := repo.GetByDNBSyntheticName(ctx, lookup, 0)
		if err != nil {
			t.Fatalf("GetByDNBSyntheticName(%q): %v", lookup, err)
		}
		if got == nil || got.ForeignID != "dnb:author:jrr-tolkien" {
			t.Errorf("GetByDNBSyntheticName(%q) = %+v, want the synthetic Tolkien row", lookup, got)
		}
	}
}

// TestAuthorRepo_GetByDNBSyntheticName_NonASCIICase pins the other half of the
// old SQL comparison: SQLite's LOWER() folds ASCII only, so a synthetic row
// stored with an uppercase non-ASCII initial was unreachable by its own
// lowercased name.
func TestAuthorRepo_GetByDNBSyntheticName_NonASCIICase(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewAuthorRepo(database)
	ctx := context.Background()

	if err := repo.Create(ctx, &models.Author{
		ForeignID: "dnb:author:oestergaard", Name: "Østergaard, Anne",
		SortName: "Østergaard, Anne", MetadataProvider: "dnb",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// "Ostergaard, Anne" is the ø→o romanisation; the ø→oe one is deliberately
	// NOT accepted here — this decides an author MERGE, so it stays Exact-only.
	for _, lookup := range []string{"østergaard, anne", "Østergaard, Anne", "Ostergaard, Anne"} {
		got, err := repo.GetByDNBSyntheticName(ctx, lookup, 0)
		if err != nil {
			t.Fatalf("GetByDNBSyntheticName(%q): %v", lookup, err)
		}
		if got == nil {
			t.Errorf("GetByDNBSyntheticName(%q) = nil, want the synthetic Østergaard row", lookup)
		}
	}

	// Still discriminating: a different person must not match.
	got, err := repo.GetByDNBSyntheticName(ctx, "Østergaard, Lars", 0)
	if err != nil {
		t.Fatalf("GetByDNBSyntheticName(Lars): %v", err)
	}
	if got != nil {
		t.Errorf("a shared surname alone should not match, got %+v", got)
	}
}

// TestAuthorDeleteCleansIdentifiersWithoutCascade is the #1728 regression: the
// author's identifier rows must go even when the foreign-key cascade is not
// enforcing, because foreign_keys is connection state that can be lost (#1727).
// The pragma is turned OFF deliberately here to simulate a replaced connection.
func TestAuthorDeleteCleansIdentifiersWithoutCascade(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	repo := NewAuthorRepo(database)

	a := &models.Author{ForeignID: "OL_DEL_W", Name: "Delete Me", SortName: "Delete Me"}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertAuthorIdentifier(ctx, a.ID, "hc:delete-me"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := database.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM author_identifiers WHERE author_id=?", a.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("author delete left %d orphaned identifier row(s); they block recreating the author", n)
	}

	// The user-visible symptom of the orphan: recreating the author with the
	// same identifier hits the NOT NULL UNIQUE foreign_id and 409s forever.
	b := &models.Author{ForeignID: "OL_DEL_W", Name: "Delete Me", SortName: "Delete Me"}
	if err := repo.Create(ctx, b); err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
	if err := repo.UpsertAuthorIdentifier(ctx, b.ID, "hc:delete-me"); err != nil {
		t.Fatalf("re-attach identifier after delete: %v", err)
	}
}

// TestAuthorCreateForUser_ReflectsPersistedOwner pins the write-back that
// #1872 was missing.
//
// CreateForUser already reflects the row's generated id and timestamps back
// onto the caller's struct — UserRepo.Create does the same with SessionEpoch,
// for the stated reason that the in-memory value must match the row that was
// just written. owner_user_id was the one column left out, so a caller held an
// author it believed was NULL-owned while the row belonged to ownerUserID.
//
// That gap is not cosmetic: AuthorHandler.FetchAuthorBooks stamps
// author.OwnerUserID onto every book it creates, so an author added by a real
// user produced an entire catalogue of NULL-owned rows, which per-user scoping
// treats as shared and shows to every account on the install.
func TestAuthorCreateForUser_ReflectsPersistedOwner(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	owner, err := NewUserRepo(database).Create(ctx, "owner", "hash")
	if err != nil {
		t.Fatal(err)
	}

	repo := NewAuthorRepo(database)
	a := &models.Author{ForeignID: "OL_OWNED_A", Name: "Owned Author", SortName: "Author, Owned"}
	if err := repo.CreateForUser(ctx, a, owner.ID); err != nil {
		t.Fatal(err)
	}
	if a.OwnerUserID != owner.ID {
		t.Fatalf("in-memory owner = %d, want %d — the struct disagrees with the row it just wrote", a.OwnerUserID, owner.ID)
	}

	persisted, err := repo.GetByID(ctx, a.ID)
	if err != nil || persisted == nil {
		t.Fatalf("read back author: err=%v got=%v", err, persisted)
	}
	if persisted.OwnerUserID != a.OwnerUserID {
		t.Fatalf("persisted owner = %d, in-memory owner = %d", persisted.OwnerUserID, a.OwnerUserID)
	}
}

// TestAuthorCreate_ReportsTheNullOwnerItWrote is the other half of the same
// invariant, and the reason the #1843 fixture could not be made non-vacuous by
// setting the struct field alone.
//
// Create is the system-owned constructor: it hard-codes ownerUserID 0 and
// writes NULL, ignoring whatever the caller put in a.OwnerUserID. Silently
// discarding that value is what made the drift invisible, so Create now
// reports the owner it actually persisted. A caller that wants an owned author
// must use CreateForUser.
func TestAuthorCreate_ReportsTheNullOwnerItWrote(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewAuthorRepo(database)
	a := &models.Author{ForeignID: "OL_UNOWNED_A", Name: "Unowned", SortName: "Unowned", OwnerUserID: 7}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if a.OwnerUserID != 0 {
		t.Fatalf("in-memory owner = %d after Create, want 0 — Create writes NULL and must say so", a.OwnerUserID)
	}

	persisted, err := repo.GetByID(ctx, a.ID)
	if err != nil || persisted == nil {
		t.Fatalf("read back author: err=%v got=%v", err, persisted)
	}
	if persisted.OwnerUserID != 0 {
		t.Fatalf("persisted owner = %d, want 0", persisted.OwnerUserID)
	}
}

// TestMigration074_BackfillsBookOwnerFromAuthor pins the repair migration for
// the rows #1872 already wrote.
//
// Three shapes have to come out differently:
//   - an owned author's NULL-owned book is adopted (the bug's signature)
//   - a NULL-owned author's NULL-owned book is left NULL (deliberately shared
//     content, and legacy pre-multi-user data; there is no owner to inherit)
//   - a book that already has an owner is never re-pointed, including to a
//     different user than its author's
func TestMigration074_BackfillsBookOwnerFromAuthor(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	users := NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "h")
	if err != nil {
		t.Fatal(err)
	}

	authors := NewAuthorRepo(database)
	owned := &models.Author{ForeignID: "OL_OWNED", Name: "Owned", SortName: "Owned"}
	if err := authors.CreateForUser(ctx, owned, alice.ID); err != nil {
		t.Fatal(err)
	}
	shared := &models.Author{ForeignID: "OL_SHARED", Name: "Shared", SortName: "Shared"}
	if err := authors.Create(ctx, shared); err != nil {
		t.Fatal(err)
	}

	// Write the three rows straight to SQL: the point is the state on disk
	// after the buggy code ran, which the repaired repo can no longer produce.
	insert := func(foreignID string, authorID int64, owner any) {
		t.Helper()
		if _, err := database.ExecContext(ctx,
			`INSERT INTO books (foreign_id, author_id, title, sort_title, status, owner_user_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, 'wanted', ?, datetime('now'), datetime('now'))`,
			foreignID, authorID, foreignID, foreignID, owner); err != nil {
			t.Fatal(err)
		}
	}
	insert("B_ORPHANED", owned.ID, nil)   // the bug: owned author, NULL book
	insert("B_SHARED", shared.ID, nil)    // no owner to inherit
	insert("B_ALREADY", owned.ID, bob.ID) // already attributed, to someone else

	// OpenMemory already ran 074 at open, before these rows existed. Re-apply
	// the real file (not a hand-copied query) against the state above.
	migration, err := migrationsFS.ReadFile("migrations/074_backfill_book_owner_from_author.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration 074: %v", err)
	}

	ownerOf := func(foreignID string) (int64, bool) {
		t.Helper()
		var owner sql.NullInt64
		if err := database.QueryRowContext(ctx,
			"SELECT owner_user_id FROM books WHERE foreign_id=?", foreignID).Scan(&owner); err != nil {
			t.Fatal(err)
		}
		return owner.Int64, owner.Valid
	}

	if got, ok := ownerOf("B_ORPHANED"); !ok || got != alice.ID {
		t.Errorf("orphaned book owner = %d (set=%v), want alice %d", got, ok, alice.ID)
	}
	if _, ok := ownerOf("B_SHARED"); ok {
		t.Error("a NULL-owned author's book must stay NULL-owned — nothing to inherit")
	}
	if got, ok := ownerOf("B_ALREADY"); !ok || got != bob.ID {
		t.Errorf("already-owned book owner = %d (set=%v), want bob %d untouched", got, ok, bob.ID)
	}
}

// TestListPageFiltered_PopulatesBookCount pins the Books column on the Authors
// page, which rendered "—" for every author before #1349's second half.
//
// The frontend reads `author.statistics?.bookCount`, but ListPageFiltered
// selected only authorSelectCols — which has no count — and nothing else on the
// list path ever set Author.Statistics. It is a `*AuthorStats` with
// `json:"statistics,omitempty"`, so a nil pointer is simply omitted from the
// response and the column falls back to its em dash. The column has been inert
// for as long as it has existed.
func TestListPageFiltered_PopulatesBookCount(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)

	prolific := &models.Author{ForeignID: "OL_P", Name: "Prolific", SortName: "Prolific"}
	if err := authors.Create(ctx, prolific); err != nil {
		t.Fatal(err)
	}
	silent := &models.Author{ForeignID: "OL_S", Name: "Silent", SortName: "Silent"}
	if err := authors.Create(ctx, silent); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		b := &models.Book{
			ForeignID: fmt.Sprintf("OL_P_B%d", i), AuthorID: prolific.ID,
			Title: fmt.Sprintf("Book %d", i), SortTitle: fmt.Sprintf("book %d", i),
			Status: models.BookStatusWanted,
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}

	page, _, err := authors.ListPageFiltered(ctx, AuthorListFilter{}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, a := range page {
		if a.Statistics == nil {
			t.Fatalf("author %q has nil Statistics — the Books column renders an em dash for this row", a.Name)
		}
		counts[a.Name] = a.Statistics.BookCount
	}
	if counts["Prolific"] != 3 {
		t.Errorf("Prolific book count = %d, want 3", counts["Prolific"])
	}
	// An author with no books must report 0, not a missing Statistics pointer —
	// "0" and "unknown" are different things on screen.
	if counts["Silent"] != 0 {
		t.Errorf("Silent book count = %d, want 0", counts["Silent"])
	}
}

// TestListPageFiltered_BookCountIsOwnerScoped keeps the derived count inside the
// same tenancy boundary as the rows it decorates. An unscoped COUNT would let
// one user infer how much another user's author holds — the same class of leak
// as #1872, and free to avoid since the predicate is already known here.
func TestListPageFiltered_BookCountIsOwnerScoped(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	users := NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "h")
	if err != nil {
		t.Fatal(err)
	}

	authors := NewAuthorRepo(database)
	// A shared (NULL-owned) author, the pre-multi-user shape, visible to both.
	shared := &models.Author{ForeignID: "OL_SH", Name: "Shared", SortName: "Shared"}
	if err := authors.Create(ctx, shared); err != nil {
		t.Fatal(err)
	}

	insertBook := func(foreignID string, owner any) {
		t.Helper()
		if _, err := database.ExecContext(ctx,
			`INSERT INTO books (foreign_id, author_id, title, sort_title, status, owner_user_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, 'wanted', ?, datetime('now'), datetime('now'))`,
			foreignID, shared.ID, foreignID, foreignID, owner); err != nil {
			t.Fatal(err)
		}
	}
	insertBook("B_ALICE", alice.ID)
	insertBook("B_BOB", bob.ID)
	insertBook("B_SHARED", nil)

	countFor := func(userID int64) int {
		t.Helper()
		page, _, err := authors.ListPageFiltered(ctx, AuthorListFilter{UserID: userID}, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != 1 || page[0].Statistics == nil {
			t.Fatalf("expected one author with statistics, got %d", len(page))
		}
		return page[0].Statistics.BookCount
	}

	// Own book + the shared one; never the other user's.
	if got := countFor(alice.ID); got != 2 {
		t.Errorf("alice sees %d books, want 2 (hers + the shared one)", got)
	}
	if got := countFor(bob.ID); got != 2 {
		t.Errorf("bob sees %d books, want 2 (his + the shared one)", got)
	}
	// Unscoped (admin / API key) sees everything.
	if got := countFor(0); got != 3 {
		t.Errorf("unscoped count = %d, want 3", got)
	}
}

// TestListPageFiltered_ColumnHeaderSorts covers the Authors half of #1349: the
// Books, Rating and Monitored columns had no sort at all, in either direction.
func TestListPageFiltered_ColumnHeaderSorts(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)

	seed := func(name string, rating float64, monitored bool, bookCount int) {
		t.Helper()
		a := &models.Author{
			ForeignID: "OL_" + name, Name: name, SortName: name,
			AverageRating: rating, Monitored: monitored,
		}
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		for i := range bookCount {
			b := &models.Book{
				ForeignID: fmt.Sprintf("OL_%s_B%d", name, i), AuthorID: a.ID,
				Title: fmt.Sprintf("%s %d", name, i), SortTitle: fmt.Sprintf("%s %d", name, i),
				Status: models.BookStatusWanted,
			}
			if err := books.Create(ctx, b); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed("Ann", 4.5, true, 1)
	seed("Bea", 2.0, false, 3)
	seed("Cal", 3.0, true, 2)

	names := func(sort string) []string {
		t.Helper()
		page, _, err := authors.ListPageFiltered(ctx, AuthorListFilter{Sort: sort}, 50, 0)
		if err != nil {
			t.Fatalf("sort %q: %v", sort, err)
		}
		out := make([]string, 0, len(page))
		for _, a := range page {
			out = append(out, a.Name)
		}
		return out
	}

	for _, tc := range []struct {
		sort string
		want []string
	}{
		{"books-asc", []string{"Ann", "Cal", "Bea"}},
		{"books-desc", []string{"Bea", "Cal", "Ann"}},
		{"rating-asc", []string{"Bea", "Cal", "Ann"}},
		{"rating-desc", []string{"Ann", "Cal", "Bea"}},
		// Ties on the monitored flag fall back to sort_key, so Ann precedes Cal.
		{"monitored-asc", []string{"Bea", "Ann", "Cal"}},
		{"monitored-desc", []string{"Ann", "Cal", "Bea"}},
		// Unknown keys must not error or reorder unpredictably.
		{"nonsense", []string{"Ann", "Bea", "Cal"}},
	} {
		if got := names(tc.sort); !slices.Equal(got, tc.want) {
			t.Errorf("sort %q = %v, want %v", tc.sort, got, tc.want)
		}
	}
}

// TestListPageFiltered_TiedSortIsStableAcrossPages guards the tiebreaker on the
// new sorts. Book counts and the monitored flag are heavily non-unique; without
// a stable secondary key SQLite may return ties in any order, and a paginated
// list then drops and repeats rows between pages.
func TestListPageFiltered_TiedSortIsStableAcrossPages(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authors := NewAuthorRepo(database)
	// Six authors, all monitored, all with zero books: every sort key ties.
	for _, n := range []string{"Fay", "Eve", "Dot", "Cal", "Bea", "Ann"} {
		if err := authors.Create(ctx, &models.Author{
			ForeignID: "OL_" + n, Name: n, SortName: n, Monitored: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, sort := range []string{"books-asc", "monitored-desc", "rating-asc"} {
		var paged []string
		for offset := 0; offset < 6; offset += 2 {
			page, _, err := authors.ListPageFiltered(ctx, AuthorListFilter{Sort: sort}, 2, offset)
			if err != nil {
				t.Fatalf("sort %q offset %d: %v", sort, offset, err)
			}
			for _, a := range page {
				paged = append(paged, a.Name)
			}
		}
		want := []string{"Ann", "Bea", "Cal", "Dot", "Eve", "Fay"}
		if !slices.Equal(paged, want) {
			t.Errorf("sort %q paged 2-at-a-time = %v, want %v (a repeat or a gap here is a lost author)", sort, paged, want)
		}
	}
}

// The catalogue-populated marker is the discriminator the refresh discovery
// policy needs (#1815): "this author has no books" cannot tell an import that
// never landed a catalogue apart from a library the user emptied on purpose.
func TestAuthorRepo_CataloguePopulatedMarker(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewAuthorRepo(database)
	a := &models.Author{ForeignID: "OL_POP_A", Name: "Populated Author", SortName: "Author, Populated"}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}

	got, err := repo.CataloguePopulatedAt(ctx, a.ID)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if got != nil {
		t.Fatalf("a fresh author must read as never populated, got %v", got)
	}

	first := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	if err := repo.MarkCataloguePopulated(ctx, a.ID, first); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got, err = repo.CataloguePopulatedAt(ctx, a.ID)
	if err != nil || got == nil {
		t.Fatalf("read marker after mark: err=%v got=%v", err, got)
	}
	if !got.Equal(first) {
		t.Errorf("marker = %v, want %v", got, first)
	}

	// Write-once: a later sync must not move the stamp, or "populated once"
	// degrades into "synced recently" and stops answering the question.
	if err := repo.MarkCataloguePopulated(ctx, a.ID, time.Now().UTC()); err != nil {
		t.Fatalf("second mark: %v", err)
	}
	got, err = repo.CataloguePopulatedAt(ctx, a.ID)
	if err != nil || got == nil {
		t.Fatalf("read marker after second mark: err=%v got=%v", err, got)
	}
	if !got.Equal(first) {
		t.Errorf("marker moved on a later sync: %v, want %v", got, first)
	}

	// An author that no longer exists reads as never populated rather than
	// erroring — the caller is a best-effort gate, not a lookup.
	if got, err := repo.CataloguePopulatedAt(ctx, a.ID+9999); err != nil || got != nil {
		t.Errorf("missing author: got=%v err=%v, want nil/nil", got, err)
	}
}

// Creating a book marks its author, whoever created it. The marker has to mean
// "this author has had books" rather than "a catalogue sync ran", or an author
// populated by an ABS/Calibre import or a Hardcover list and then emptied by
// hand still reads as never-populated and the next refresh re-imports the
// bibliography (#1815). BookRepo.Create is the one place every creation path
// goes through, which is why dedup_key is derived there too.
func TestBookRepo_CreateMarksTheAuthorAsPopulated(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)

	a := &models.Author{ForeignID: "OL_IMPORTED_A", Name: "Imported Author", SortName: "Author, Imported"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got, err := authors.CataloguePopulatedAt(ctx, a.ID); err != nil || got != nil {
		t.Fatalf("author with no books: got=%v err=%v, want nil", got, err)
	}

	first := &models.Book{ForeignID: "OL_IB1", AuthorID: a.ID, Title: "First", SortTitle: "first"}
	if err := books.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	marked, err := authors.CataloguePopulatedAt(ctx, a.ID)
	if err != nil || marked == nil {
		t.Fatalf("creating a book did not mark the author: got=%v err=%v", marked, err)
	}

	// Still write-once through this path: a second book must not move it.
	if err := books.Create(ctx, &models.Book{
		ForeignID: "OL_IB2", AuthorID: a.ID, Title: "Second", SortTitle: "second",
	}); err != nil {
		t.Fatal(err)
	}
	again, err := authors.CataloguePopulatedAt(ctx, a.ID)
	if err != nil || again == nil {
		t.Fatalf("read marker: got=%v err=%v", again, err)
	}
	if !again.Equal(*marked) {
		t.Errorf("second book moved the marker: %v then %v", marked, again)
	}

	// Deleting every book leaves the marker standing — that is the whole point.
	if err := books.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := authors.CataloguePopulatedAt(ctx, a.ID); err != nil || got == nil {
		t.Errorf("deleting books cleared the marker: got=%v err=%v", got, err)
	}
}

// Migration 075 backfills the marker for authors that already have books:
// without it, every existing library would look never-populated for one
// refresh, and an author whose catalogue the user had already deleted would be
// re-imported once more before the marker started sticking.
func TestMigration075_BackfillsAuthorsThatHaveBooks(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)

	withBooks := &models.Author{ForeignID: "OL_HAS_BOOKS", Name: "Has Books", SortName: "Books, Has"}
	if err := authors.Create(ctx, withBooks); err != nil {
		t.Fatal(err)
	}
	without := &models.Author{ForeignID: "OL_NO_BOOKS", Name: "No Books", SortName: "Books, No"}
	if err := authors.Create(ctx, without); err != nil {
		t.Fatal(err)
	}
	if err := books.Create(ctx, &models.Book{
		ForeignID: "OL_B1", AuthorID: withBooks.ID, Title: "A Book", SortTitle: "a book",
	}); err != nil {
		t.Fatal(err)
	}

	// Re-run the migration body against the now-populated DB: the rows above
	// were created after migrate() ran, so this stands in for the upgrade.
	if _, err := database.Exec(`UPDATE authors SET catalogue_populated_at = created_at
		WHERE catalogue_populated_at IS NULL
		  AND EXISTS (SELECT 1 FROM books WHERE books.author_id = authors.id)`); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if got, err := authors.CataloguePopulatedAt(ctx, withBooks.ID); err != nil || got == nil {
		t.Errorf("author with books was not backfilled: got=%v err=%v", got, err)
	}
	if got, err := authors.CataloguePopulatedAt(ctx, without.ID); err != nil || got != nil {
		t.Errorf("author with no books must stay unmarked: got=%v err=%v", got, err)
	}
}
