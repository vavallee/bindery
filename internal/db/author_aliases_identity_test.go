package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/models"
)

// seedLegacyAlias writes an alias row straight to the table, bypassing Create.
// Two rows whose names identify one person but point at two authors can no
// longer be created through the repo, so a test that needs that state has to
// write it the way an existing install already holds it: from before the
// check folded.
func seedLegacyAlias(t *testing.T, database *sql.DB, authorID int64, name string) {
	t.Helper()
	if _, err := database.ExecContext(context.Background(),
		"INSERT INTO author_aliases (author_id, name, created_at) VALUES (?, ?, ?)",
		authorID, name, time.Now().UTC()); err != nil {
		t.Fatalf("seed legacy alias %q: %v", name, err)
	}
}

// TestAliasLookup_FoldsAccentsAndUnicodeForm is the identity half of #1660.
// The lookups matched with `LOWER(name) = LOWER(?)`, and SQLite's LOWER folds
// the 26 ASCII letters and nothing else, so an alias stored as "östergaard"
// was unreachable from "Östergaard" — and a name typed on macOS (decomposed)
// was unreachable from the composed spelling stored by every metadata
// provider, which is the pairing that actually happens in the wild.
func TestAliasLookup_FoldsAccentsAndUnicodeForm(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	a := seedAuthor(t, authorRepo, "OL1A", "Anne Østergaard")
	stored := "östergaard"
	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: a.ID, Name: stored}); err != nil {
		t.Fatalf("create alias: %v", err)
	}

	for _, probe := range []struct {
		name string
		want string
	}{
		{"same spelling", "östergaard"},
		{"capitalised accent", "Östergaard"},
		{"upper case", "ÖSTERGAARD"},
		{"decomposed", norm.NFD.String("Östergaard")},
		{"composed", norm.NFC.String("Östergaard")},
	} {
		t.Run(probe.name, func(t *testing.T) {
			got, err := aliasRepo.LookupByName(ctx, probe.want)
			if err != nil {
				t.Fatalf("lookup %q: %v", probe.want, err)
			}
			if got == nil || *got != a.ID {
				t.Errorf("LookupByName(%q) = %v, want author %d", probe.want, got, a.ID)
			}
			row, err := aliasRepo.GetByName(ctx, probe.want)
			if err != nil {
				t.Fatalf("get %q: %v", probe.want, err)
			}
			if row == nil || row.AuthorID != a.ID {
				t.Errorf("GetByName(%q) = %+v, want author %d", probe.want, row, a.ID)
			}
		})
	}

	// The reverse direction: an alias stored composed, looked up decomposed.
	b := seedAuthor(t, authorRepo, "OL1B", "Jörg Müller")
	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: b.ID, Name: norm.NFC.String("Jörg Müller (Hörbuch)")}); err != nil {
		t.Fatalf("create composed alias: %v", err)
	}
	got, err := aliasRepo.LookupByName(ctx, norm.NFD.String("jörg müller (hörbuch)"))
	if err != nil {
		t.Fatalf("lookup decomposed: %v", err)
	}
	if got == nil || *got != b.ID {
		t.Errorf("decomposed lookup = %v, want author %d", got, b.ID)
	}

	// A name that is a different person still misses.
	miss, err := aliasRepo.LookupByName(ctx, "Anne Ostergren")
	if err != nil {
		t.Fatalf("lookup miss: %v", err)
	}
	if miss != nil {
		t.Errorf("unrelated name resolved to author %d", *miss)
	}
}

// TestAliasLookup_AmbiguousIdentityReturnsNil pins the rule that matters more
// than the fix itself: when the folded name identifies rows belonging to two
// DIFFERENT authors there is no answer, and returning whichever row the query
// planner produced first would bind an import to an author chosen by row
// order. Create refuses to build this state, so the rows are inserted here the
// way they can only have arrived — directly, before the check existed.
func TestAliasLookup_AmbiguousIdentityReturnsNil(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	a := seedAuthor(t, authorRepo, "OL1A", "Author A")
	b := seedAuthor(t, authorRepo, "OL1B", "Author B")

	seedLegacyAlias(t, database, a.ID, "Östergaard")
	seedLegacyAlias(t, database, b.ID, "ostergaard")

	for _, probe := range []string{"Östergaard", "ostergaard", "ÖSTERGAARD"} {
		got, err := aliasRepo.LookupByName(ctx, probe)
		if err != nil {
			t.Fatalf("lookup %q: %v", probe, err)
		}
		if got != nil {
			t.Errorf("LookupByName(%q) = %d, want nil — the name identifies both author %d and author %d",
				probe, *got, a.ID, b.ID)
		}
		row, err := aliasRepo.GetByName(ctx, probe)
		if err != nil {
			t.Fatalf("get %q: %v", probe, err)
		}
		if row != nil {
			t.Errorf("GetByName(%q) = %+v, want nil on an ambiguous match", probe, row)
		}
	}
}

// TestAliasCreate_RefusesFoldedReassignment guards the refusal that already
// existed for a byte-identical name, now that the check folds. Pointing one
// name at two authors is what makes a lookup ambiguous, so it must be refused
// at the door rather than discovered later.
func TestAliasCreate_RefusesFoldedReassignment(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	a := seedAuthor(t, authorRepo, "OL1A", "Author A")
	b := seedAuthor(t, authorRepo, "OL1B", "Author B")

	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: a.ID, Name: "Östergaard"}); err != nil {
		t.Fatalf("first create: %v", err)
	}

	for _, variant := range []string{
		"Östergaard",                  // the original refusal: byte-identical
		"östergaard",                  // ASCII-foldable case only
		"ÖSTERGAARD",                  // ...and the other way
		"Ostergaard",                  // accent dropped
		norm.NFD.String("Östergaard"), // decomposed
		"  Östergaard  ",              // trimmed
		"Östergaard.",                 // trailing punctuation
	} {
		err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: b.ID, Name: variant})
		if err == nil {
			t.Errorf("Create(%q) for author %d succeeded; want a refusal, the name already points at author %d",
				variant, b.ID, a.ID)
			continue
		}
		if !strings.Contains(err.Error(), "already points") {
			t.Errorf("Create(%q): want an 'already points' refusal, got: %v", variant, err)
		}
	}

	// The refusal must not have written anything: the name still resolves to A.
	got, err := aliasRepo.LookupByName(ctx, "Östergaard")
	if err != nil {
		t.Fatalf("lookup after refusals: %v", err)
	}
	if got == nil || *got != a.ID {
		t.Errorf("after refusals LookupByName = %v, want author %d", got, a.ID)
	}
}

// TestAliasCreate_KeepsDistinctSpellingsForOneAuthor documents the deliberate
// limit of the folding. Identity decides who an alias BELONGS to; it does not
// decide that two spellings are the same row. The names go out to indexers as
// MatchCriteria.AuthorAliases, and release matching expands umlauts where
// identity strips them, so "Jorg Muller" is worth its own row next to
// "Jörg Müller". Only an exact repeat is idempotent, as before.
func TestAliasCreate_KeepsDistinctSpellingsForOneAuthor(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	a := seedAuthor(t, authorRepo, "OL1A", "Jörg Müller")

	accented := &models.AuthorAlias{AuthorID: a.ID, Name: "Jörg Müller"}
	if err := aliasRepo.Create(ctx, accented); err != nil {
		t.Fatalf("create accented: %v", err)
	}
	ascii := &models.AuthorAlias{AuthorID: a.ID, Name: "Jorg Muller"}
	if err := aliasRepo.Create(ctx, ascii); err != nil {
		t.Fatalf("create ascii spelling: %v", err)
	}
	if ascii.ID == accented.ID {
		t.Errorf("second spelling reused row %d; both spellings should be on file for indexer matching", ascii.ID)
	}

	// An exact repeat is still a no-op on the existing row.
	repeat := &models.AuthorAlias{AuthorID: a.ID, Name: "Jörg Müller"}
	if err := aliasRepo.Create(ctx, repeat); err != nil {
		t.Fatalf("repeat create: %v", err)
	}
	if repeat.ID != accented.ID {
		t.Errorf("repeat create made row %d, want the existing %d", repeat.ID, accented.ID)
	}

	// Both rows point at the one author, so the lookup is not ambiguous.
	got, err := aliasRepo.LookupByName(ctx, "JORG MULLER")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got == nil || *got != a.ID {
		t.Errorf("LookupByName = %v, want author %d", got, a.ID)
	}
}

// TestMerge_SkipsAliasTargetAlreadyIdentifies covers the fourth folding site:
// the migration used to skip a source alias only when target held a
// LOWER()-equal name, so an accented variant was carried across and the pair
// then made every lookup for that person ambiguous.
func TestMerge_SkipsAliasTargetAlreadyIdentifies(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	source := seedAuthor(t, authorRepo, "OL-source", "Anne Ostergaard")
	target := seedAuthor(t, authorRepo, "OL-target", "Anne Østergaard")

	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: target.ID, Name: "Östergaard, Anne"}); err != nil {
		t.Fatalf("seed target alias: %v", err)
	}
	// Same person, different spelling, on the source author. Create refuses to
	// build this now, so it is seeded the way an existing install holds it.
	seedLegacyAlias(t, database, source.ID, "ostergaard, anne")
	// And one target does not have, which must still migrate.
	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: source.ID, Name: "A. Østergaard"}); err != nil {
		t.Fatalf("seed second source alias: %v", err)
	}

	res, err := aliasRepo.Merge(ctx, source.ID, target.ID, MergeOptions{OverwriteDefaults: true})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.AliasesMigrated != 1 {
		t.Errorf("aliases migrated = %d, want 1 — the spelling target already carries must be dropped, not moved",
			res.AliasesMigrated)
	}

	aliases, err := aliasRepo.ListByAuthor(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ostergaard int
	for _, al := range aliases {
		if strings.Contains(strings.ToLower(al.Name), "stergaard, anne") {
			ostergaard++
		}
	}
	if ostergaard != 1 {
		t.Errorf("target carries %d rows identifying 'Østergaard, Anne', want 1", ostergaard)
	}

	// The merged author is still reachable by either spelling, unambiguously.
	for _, probe := range []string{"Östergaard, Anne", "ostergaard, anne", "a. østergaard"} {
		got, err := aliasRepo.LookupByName(ctx, probe)
		if err != nil {
			t.Fatalf("lookup %q: %v", probe, err)
		}
		if got == nil || *got != target.ID {
			t.Errorf("LookupByName(%q) = %v, want target %d", probe, got, target.ID)
		}
	}
}

// BenchmarkAliasGetByName measures the table scan the identity comparison
// costs, because "not a hot path" should be a measurement rather than an
// assumption. The alias table holds one row per merged-away or variant name,
// so 10k rows is already a library nobody has; the caller that runs this most
// often (the Calibre importer, once per book) already lists every author row
// per book alongside it.
func BenchmarkAliasGetByName(b *testing.B) {
	for _, rows := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("rows=%d", rows), func(b *testing.B) {
			database, err := OpenMemory()
			if err != nil {
				b.Fatal(err)
			}
			defer database.Close()

			ctx := context.Background()
			authorRepo := NewAuthorRepo(database)
			aliasRepo := NewAuthorAliasRepo(database)
			author := &models.Author{
				ForeignID: "OL1A", Name: "Anne Østergaard", SortName: "Østergaard, Anne",
				MetadataProvider: "openlibrary", Monitored: true,
			}
			if err := authorRepo.Create(ctx, author); err != nil {
				b.Fatal(err)
			}
			for i := 0; i < rows; i++ {
				if err := aliasRepo.Create(ctx, &models.AuthorAlias{
					AuthorID: author.ID,
					Name:     fmt.Sprintf("Filler Name %d", i),
				}); err != nil {
					b.Fatal(err)
				}
			}
			if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: author.ID, Name: "östergaard, anne"}); err != nil {
				b.Fatal(err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got, err := aliasRepo.GetByName(ctx, "Östergaard, Anne")
				if err != nil || got == nil {
					b.Fatalf("lookup: %+v (err %v)", got, err)
				}
			}
		})
	}
}
