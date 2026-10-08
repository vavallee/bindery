package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestListCoveredSplitEditionParts pins which wholes count as "already in the
// library" for #3048, and that one user's whole never covers another user's
// part.
func TestListCoveredSplitEditionParts(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	series := NewSeriesRepo(database)
	users := NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}

	author := &models.Author{ForeignID: "hc:sanderson", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", MetadataProvider: "hardcover"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	type row struct {
		title, position, status string
		monitored, excluded     bool
		owner                   int64
	}
	seed := func(seriesTitle string, rows ...row) (int64, []int64) {
		t.Helper()
		s := &models.Series{ForeignID: "hc-series:" + seriesTitle, Title: seriesTitle}
		if err := series.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			b := &models.Book{
				ForeignID: "hc:" + seriesTitle + ":" + r.title, AuthorID: author.ID, Title: r.title, SortTitle: r.title,
				Status: r.status, Monitored: r.monitored, MediaType: models.MediaTypeEbook,
				Genres: []string{}, MetadataProvider: "hardcover", OwnerUserID: r.owner,
			}
			if err := books.Create(ctx, b); err != nil {
				t.Fatal(err)
			}
			if r.excluded {
				if _, err := database.ExecContext(ctx, "UPDATE books SET excluded = 1 WHERE id = ?", b.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := series.LinkBook(ctx, s.ID, b.ID, r.position, true); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, b.ID)
		}
		return s.ID, ids
	}

	// Covered: the whole is wanted but monitored, so the library is already
	// looking for it.
	wantedID, wanted := seed("wanted-whole",
		row{"The Way of Kings", "1", models.BookStatusWanted, true, false, alice.ID},
		row{"The Way of Kings, Part 1", "1.1", models.BookStatusWanted, true, false, alice.ID})
	// Not covered: the whole is excluded.
	_, excluded := seed("excluded-whole",
		row{"The Way of Kings", "1", models.BookStatusImported, true, true, alice.ID},
		row{"The Way of Kings, Part 1", "1.1", models.BookStatusWanted, true, false, alice.ID})
	// Not covered: the whole is wanted and unmonitored, so nothing is being
	// sought and the parts may be what the user wants.
	_, unwanted := seed("unwanted-whole",
		row{"The Way of Kings", "1", models.BookStatusWanted, false, false, alice.ID},
		row{"The Way of Kings, Part 1", "1.1", models.BookStatusWanted, true, false, alice.ID})
	// Not covered: alice owns the whole, the part is bob's.
	_, crossUser := seed("cross-user",
		row{"The Way of Kings", "1", models.BookStatusImported, true, false, alice.ID},
		row{"The Way of Kings, Part 1", "1.1", models.BookStatusWanted, true, false, bob.ID})
	// Not reported: the part is already imported.
	_, importedPart := seed("imported-part",
		row{"The Way of Kings", "1", models.BookStatusImported, true, false, alice.ID},
		row{"The Way of Kings, Part 1", "1.1", models.BookStatusImported, true, false, alice.ID})

	parts, err := books.ListCoveredSplitEditionParts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].BookID != wanted[1] || parts[0].WholeBookID != wanted[0] || !parts[0].Monitored {
		t.Fatalf("parts = %+v, want only book %d covered by %d", parts, wanted[1], wanted[0])
	}
	for name, ids := range map[string][]int64{"excluded": excluded, "unwanted": unwanted, "cross user": crossUser, "imported part": importedPart} {
		for _, p := range parts {
			if p.BookID == ids[1] {
				t.Errorf("%s case: part %d reported as covered", name, ids[1])
			}
		}
	}

	scoped, err := books.ListCoveredSplitEditionParts(ctx, wantedID+100)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 0 {
		t.Errorf("a series filter matched other series: %+v", scoped)
	}

	n, err := books.UnmonitorNotImported(ctx, []int64{wanted[1], importedPart[1]})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("UnmonitorNotImported changed %d rows, want 1 (the imported part is left alone)", n)
	}
}
