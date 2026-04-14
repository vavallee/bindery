package db

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func seedAuthor(t *testing.T, repo *AuthorRepo, foreignID, name string) *models.Author {
	t.Helper()
	a := &models.Author{
		ForeignID:        foreignID,
		Name:             name,
		SortName:         name,
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := repo.Create(context.Background(), a); err != nil {
		t.Fatalf("seed author %q: %v", name, err)
	}
	return a
}

func seedBook(t *testing.T, repo *BookRepo, authorID int64, foreignID, title string) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID:        foreignID,
		AuthorID:         authorID,
		Title:            title,
		SortTitle:        title,
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := repo.Create(context.Background(), b); err != nil {
		t.Fatalf("seed book %q: %v", title, err)
	}
	return b
}

func TestAliasCreate_IdempotentOnSameAuthor(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	a := seedAuthor(t, authorRepo, "OL1A", "R.R. Haywood")

	first := &models.AuthorAlias{AuthorID: a.ID, Name: "RR Haywood"}
	if err := aliasRepo.Create(ctx, first); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if first.ID == 0 {
		t.Fatal("expected non-zero id after first create")
	}

	second := &models.AuthorAlias{AuthorID: a.ID, Name: "RR Haywood"}
	if err := aliasRepo.Create(ctx, second); err != nil {
		t.Fatalf("second create (same author): %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("expected same id %d on idempotent create, got %d", first.ID, second.ID)
	}
}

func TestAliasCreate_RejectsReassignment(t *testing.T) {
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

	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: a.ID, Name: "shared name"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err = aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: b.ID, Name: "shared name"})
	if err == nil {
		t.Fatal("expected error when reassigning alias to different author")
	}
	if !strings.Contains(err.Error(), "already points") {
		t.Errorf("expected 'already points' error, got: %v", err)
	}
}

func TestAliasLookupByName_CaseInsensitive(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	a := seedAuthor(t, authorRepo, "OL1A", "R.R. Haywood")
	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: a.ID, Name: "RR Haywood"}); err != nil {
		t.Fatalf("create alias: %v", err)
	}

	// Exact match.
	got, err := aliasRepo.LookupByName(ctx, "RR Haywood")
	if err != nil || got == nil || *got != a.ID {
		t.Errorf("exact match: want %d, got %v (err %v)", a.ID, got, err)
	}
	// Case-insensitive.
	got, err = aliasRepo.LookupByName(ctx, "rr haywood")
	if err != nil || got == nil || *got != a.ID {
		t.Errorf("case-insensitive: want %d, got %v (err %v)", a.ID, got, err)
	}
	// Trimmed.
	got, err = aliasRepo.LookupByName(ctx, "  RR Haywood  ")
	if err != nil || got == nil || *got != a.ID {
		t.Errorf("trimmed: want %d, got %v (err %v)", a.ID, got, err)
	}
	// Miss.
	got, err = aliasRepo.LookupByName(ctx, "Unknown Author")
	if err != nil {
		t.Errorf("unexpected error on miss: %v", err)
	}
	if got != nil {
		t.Errorf("miss: want nil, got %d", *got)
	}
}

func TestAliasListByAuthor(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	a := seedAuthor(t, authorRepo, "OL1A", "Author A")
	for _, n := range []string{"A Alias One", "A Alias Two"} {
		if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: a.ID, Name: n}); err != nil {
			t.Fatalf("create %q: %v", n, err)
		}
	}
	b := seedAuthor(t, authorRepo, "OL1B", "Author B")
	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: b.ID, Name: "B Alias"}); err != nil {
		t.Fatal(err)
	}

	aliases, err := aliasRepo.ListByAuthor(ctx, a.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(aliases) != 2 {
		t.Errorf("want 2 aliases for A, got %d", len(aliases))
	}
	for _, al := range aliases {
		if al.AuthorID != a.ID {
			t.Errorf("alias %q pointed at %d, want %d", al.Name, al.AuthorID, a.ID)
		}
	}
}

func TestAliasDelete(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	a := seedAuthor(t, authorRepo, "OL1A", "Author A")
	alias := &models.AuthorAlias{AuthorID: a.ID, Name: "to delete"}
	if err := aliasRepo.Create(ctx, alias); err != nil {
		t.Fatal(err)
	}
	if err := aliasRepo.Delete(ctx, alias.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := aliasRepo.LookupByName(ctx, "to delete")
	if got != nil {
		t.Errorf("expected lookup miss after delete, got %d", *got)
	}
}

func TestAliasCascadeOnAuthorDelete(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	a := seedAuthor(t, authorRepo, "OL1A", "Author A")
	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: a.ID, Name: "cascade me"}); err != nil {
		t.Fatal(err)
	}
	if err := authorRepo.Delete(ctx, a.ID); err != nil {
		t.Fatalf("delete author: %v", err)
	}
	got, _ := aliasRepo.LookupByName(ctx, "cascade me")
	if got != nil {
		t.Errorf("expected cascade delete of alias, still resolves to %d", *got)
	}
}

func TestMerge_ReparentsBooksAndRecordsAliases(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	source := seedAuthor(t, authorRepo, "OL-source", "RR Haywood")
	target := seedAuthor(t, authorRepo, "OL-target", "R.R. Haywood")

	b1 := seedBook(t, bookRepo, source.ID, "W1", "Book One")
	b2 := seedBook(t, bookRepo, source.ID, "W2", "Book Two")
	seedBook(t, bookRepo, target.ID, "W3", "Book Three")

	// Pre-existing alias on source should migrate to target.
	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: source.ID, Name: "R R Haywood"}); err != nil {
		t.Fatal(err)
	}

	res, err := aliasRepo.Merge(ctx, source.ID, target.ID, MergeOptions{OverwriteDefaults: true})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.BooksReparented != 2 {
		t.Errorf("books reparented: want 2, got %d", res.BooksReparented)
	}

	// Source author is gone.
	if got, _ := authorRepo.GetByID(ctx, source.ID); got != nil {
		t.Error("expected source author to be deleted")
	}

	// Books now belong to target.
	for _, id := range []int64{b1.ID, b2.ID} {
		book, err := bookRepo.GetByID(ctx, id)
		if err != nil || book == nil {
			t.Fatalf("get book %d: %v", id, err)
		}
		if book.AuthorID != target.ID {
			t.Errorf("book %d author: want %d, got %d", id, target.ID, book.AuthorID)
		}
	}
	targetBooks, err := bookRepo.ListByAuthor(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targetBooks) != 3 {
		t.Errorf("target should have 3 books after merge, got %d", len(targetBooks))
	}

	// Source's name + foreign id are aliases on target.
	got, err := aliasRepo.LookupByName(ctx, "RR Haywood")
	if err != nil || got == nil || *got != target.ID {
		t.Errorf("source.name alias not pointed at target: got %v (err %v)", got, err)
	}
	aliases, err := aliasRepo.ListByAuthor(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	var haveOLID, haveMigrated bool
	for _, a := range aliases {
		if a.SourceOLID == "OL-source" {
			haveOLID = true
		}
		if a.Name == "R R Haywood" {
			haveMigrated = true
		}
	}
	if !haveOLID {
		t.Error("expected source foreign id captured as alias.source_ol_id")
	}
	if !haveMigrated {
		t.Error("expected pre-existing source alias to migrate to target")
	}
}

func TestMerge_CopiesFieldsOnlyWhenTargetDefaults(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	// Source has a custom quality profile id; target's is nil (default).
	source := seedAuthor(t, authorRepo, "OL-source", "Source")
	qp := int64(42)
	source.QualityProfileID = &qp
	if err := authorRepo.Update(ctx, source); err != nil {
		t.Fatal(err)
	}

	target := seedAuthor(t, authorRepo, "OL-target", "Target")

	if _, err := aliasRepo.Merge(ctx, source.ID, target.ID, MergeOptions{OverwriteDefaults: true}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	got, err := authorRepo.GetByID(ctx, target.ID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.QualityProfileID == nil || *got.QualityProfileID != qp {
		t.Errorf("expected target.quality_profile_id=%d, got %v", qp, got.QualityProfileID)
	}
}

func TestMerge_PreservesTargetWhenNonDefault(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	// Both have profiles set. Target's must win.
	source := seedAuthor(t, authorRepo, "OL-source", "Source")
	sourceQP := int64(11)
	source.QualityProfileID = &sourceQP
	if err := authorRepo.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	target := seedAuthor(t, authorRepo, "OL-target", "Target")
	targetQP := int64(22)
	target.QualityProfileID = &targetQP
	if err := authorRepo.Update(ctx, target); err != nil {
		t.Fatal(err)
	}

	if _, err := aliasRepo.Merge(ctx, source.ID, target.ID, MergeOptions{OverwriteDefaults: true}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	got, _ := authorRepo.GetByID(ctx, target.ID)
	if got.QualityProfileID == nil || *got.QualityProfileID != targetQP {
		t.Errorf("target quality profile should be preserved (%d), got %v", targetQP, got.QualityProfileID)
	}
}

func TestMerge_RollsBackOnFailure(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	aliasRepo := NewAuthorAliasRepo(database)

	// Neither author exists — Merge must fail and leave the DB untouched.
	_, err = aliasRepo.Merge(ctx, 9991, 9992, MergeOptions{OverwriteDefaults: true})
	if err == nil {
		t.Fatal("expected error when merging non-existent authors")
	}

	// Same-id must also fail fast, before any DB mutation.
	authorRepo := NewAuthorRepo(database)
	a := seedAuthor(t, authorRepo, "OL1A", "Same")
	_, err = aliasRepo.Merge(ctx, a.ID, a.ID, MergeOptions{})
	if err == nil {
		t.Fatal("expected error on self-merge")
	}
}

func TestMerge_CollidingAliasesNotDuplicated(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)

	source := seedAuthor(t, authorRepo, "OL-source", "Collision Name")
	target := seedAuthor(t, authorRepo, "OL-target", "Target")

	// Target already has the name we'd be trying to record as an alias.
	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: target.ID, Name: "Collision Name"}); err != nil {
		t.Fatal(err)
	}
	// And source has an alias whose name is already a target alias.
	if err := aliasRepo.Create(ctx, &models.AuthorAlias{AuthorID: source.ID, Name: "Collision Name", SourceOLID: "OL-orig"}); err == nil {
		t.Fatal("expected rejection creating same alias name on second author")
	}

	if _, err := aliasRepo.Merge(ctx, source.ID, target.ID, MergeOptions{OverwriteDefaults: true}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	aliases, err := aliasRepo.ListByAuthor(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, a := range aliases {
		if strings.EqualFold(a.Name, "Collision Name") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one 'Collision Name' alias on target, got %d", count)
	}
}
