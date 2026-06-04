package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/indexer"
)

// TestBookRepo_DedupKeyPersistedOnCreate proves Create writes the canonical
// dedup key derived from the title, so every book-creation path (Calibre, ABS,
// CWA, manual) is keyed identically (#940).
func TestBookRepo_DedupKeyPersistedOnCreate(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := mkAuthor(t, authorRepo, ctx, "OL-DK-A")

	b := mkBook(t, bookRepo, ctx, a.ID, "OL-DK-B", "Mistborn: The Final Empire", "wanted")
	want := indexer.CanonicalDedupKey("Mistborn: The Final Empire")
	if b.DedupKey != want {
		t.Fatalf("Create did not set DedupKey: got %q want %q", b.DedupKey, want)
	}

	got, _ := bookRepo.GetByID(ctx, b.ID)
	if got.DedupKey != want {
		t.Fatalf("stored DedupKey: got %q want %q", got.DedupKey, want)
	}
}

// TestBookRepo_DedupKeyRecomputedOnUpdate proves a title change refreshes the
// stored key so it can never go stale and silently break future binds.
func TestBookRepo_DedupKeyRecomputedOnUpdate(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := mkAuthor(t, authorRepo, ctx, "OL-DK-U")

	b := mkBook(t, bookRepo, ctx, a.ID, "OL-DK-UB", "Old Title", "wanted")
	b.Title = "New Title: A Subtitle"
	if err := bookRepo.Update(ctx, b); err != nil {
		t.Fatalf("Update: %v", err)
	}
	want := indexer.CanonicalDedupKey("New Title: A Subtitle")
	got, _ := bookRepo.GetByID(ctx, b.ID)
	if got.DedupKey != want {
		t.Fatalf("Update did not recompute DedupKey: got %q want %q", got.DedupKey, want)
	}
}

// TestBookRepo_FindByAuthorAndDedupKey_Asymmetry is the core #940 regression.
// A book stored under one source's raw title must be found by the OTHER
// source's differing-but-equivalent title via the canonical key, in BOTH
// directions, for titles differing by subtitle, bracket/paren qualifier, case,
// and umlaut form. The pre-fix FindByAuthorAndTitle (raw LOWER match) failed
// every one of these.
func TestBookRepo_FindByAuthorAndDedupKey_Asymmetry(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)

	cases := []struct {
		name   string
		stored string // title as one source persists it
		lookup string // title the other source presents
	}{
		{"subtitle vs none", "Mistborn: The Final Empire", "Mistborn"},
		{"none vs subtitle", "Mistborn", "Mistborn: The Final Empire"},
		{"bracket qualifier", "The Eye of the World", "The Eye of the World [Unabridged]"},
		{"case differs", "DUNE", "dune"},
		{"umlaut form differs", "Die Straße", "Die Strasse"},
		{"paren edition", "Dune", "Dune (Unabridged)"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := mkAuthor(t, authorRepo, ctx, "OL-AS-A"+string(rune('a'+i)))
			b := mkBook(t, bookRepo, ctx, a.ID, "OL-AS-B"+string(rune('a'+i)), tc.stored, "wanted")

			got, err := bookRepo.FindByAuthorAndDedupKey(ctx, a.ID, tc.lookup)
			if err != nil {
				t.Fatalf("FindByAuthorAndDedupKey: %v", err)
			}
			if got == nil || got.ID != b.ID {
				t.Fatalf("stored %q not found via lookup %q (got %+v)", tc.stored, tc.lookup, got)
			}
		})
	}
}

// TestBookRepo_FindByAuthorAndDedupKey_EmptyKeyAndWrongAuthor guards against
// the two ways the lookup could over-match: a blank title (which must never
// collapse untitled rows) and a different author.
func TestBookRepo_FindByAuthorAndDedupKey_EmptyKeyAndWrongAuthor(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := mkAuthor(t, authorRepo, ctx, "OL-EK-A")
	_ = mkBook(t, bookRepo, ctx, a.ID, "OL-EK-B", "Some Title", "wanted")

	// Blank lookup title -> empty key -> never match.
	got, err := bookRepo.FindByAuthorAndDedupKey(ctx, a.ID, "   ")
	if err != nil {
		t.Fatalf("blank lookup: %v", err)
	}
	if got != nil {
		t.Fatalf("blank title must not match, got %+v", got)
	}

	// Right key, wrong author.
	got, err = bookRepo.FindByAuthorAndDedupKey(ctx, 999, "Some Title")
	if err != nil {
		t.Fatalf("wrong author lookup: %v", err)
	}
	if got != nil {
		t.Fatalf("wrong author must not match, got %+v", got)
	}
}

// TestBookRepo_FindAllByAuthorAndDedupKey_Ambiguous proves the ABS importer's
// ambiguity signal: two rows under one author sharing a canonical key are both
// returned so the caller can route to review instead of guessing.
func TestBookRepo_FindAllByAuthorAndDedupKey_Ambiguous(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := mkAuthor(t, authorRepo, ctx, "OL-AM-A")

	_ = mkBook(t, bookRepo, ctx, a.ID, "OL-AM-1", "Foundation", "wanted")
	_ = mkBook(t, bookRepo, ctx, a.ID, "OL-AM-2", "Foundation: The Empire", "wanted")

	all, err := bookRepo.FindAllByAuthorAndDedupKey(ctx, a.ID, "foundation")
	if err != nil {
		t.Fatalf("FindAllByAuthorAndDedupKey: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 ambiguous rows, got %d", len(all))
	}
}

// TestBackfillBookDedupKeys recomputes the exact key for rows whose stored
// value is blank or only the coarse SQL approximation. Simulates a legacy row
// by clearing dedup_key directly, then asserts the startup backfill fixes it.
func TestBackfillBookDedupKeys(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := mkAuthor(t, authorRepo, ctx, "OL-BF-A")
	b := mkBook(t, bookRepo, ctx, a.ID, "OL-BF-B", "Die Straße: Ein Roman", "wanted")

	// Simulate a pre-051 / coarse-SQL row: wrong (un-folded) key.
	if _, err := database.ExecContext(ctx, "UPDATE books SET dedup_key = ? WHERE id = ?", "die straße", b.ID); err != nil {
		t.Fatalf("seed stale key: %v", err)
	}

	if err := backfillBookDedupKeys(database); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	want := indexer.CanonicalDedupKey("Die Straße: Ein Roman")
	got, _ := bookRepo.GetByID(ctx, b.ID)
	if got.DedupKey != want {
		t.Fatalf("backfill did not canonicalize: got %q want %q", got.DedupKey, want)
	}
	// Now the umlaut-transliterated lookup must bind.
	found, _ := bookRepo.FindByAuthorAndDedupKey(ctx, a.ID, "Die Strasse")
	if found == nil || found.ID != b.ID {
		t.Fatalf("post-backfill umlaut lookup failed: %+v", found)
	}
}
