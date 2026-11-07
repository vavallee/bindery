package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMerge_ReportsAliasHeldByAnotherAuthor is #3565. Alias names are unique
// across the instance, so when the merged away author's name is already an
// alias on some other author, the merge cannot record it. It used to succeed
// without a word; it must now say which name was left out.
func TestMerge_ReportsAliasHeldByAnotherAuthor(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authors := NewAuthorRepo(database)
	aliases := NewAuthorAliasRepo(database)

	// One account's author holds the pen name as an alias.
	alice := seedAuthor(t, authors, "OL-ALICE", "J.K. Rowling")
	if err := aliases.Create(ctx, &models.AuthorAlias{AuthorID: alice.ID, Name: "Robert Galbraith"}); err != nil {
		t.Fatal(err)
	}
	// Another account has the pen name as its own author row and merges it.
	target := seedAuthor(t, authors, "HC-ROWLING", "J.K. Rowling")
	source := seedAuthor(t, authors, "HC-GALBRAITH", "Robert Galbraith")

	res, err := aliases.Merge(ctx, source.ID, target.ID, MergeOptions{})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.AliasesCreated != 0 {
		t.Fatalf("AliasesCreated = %d, want 0 (another author holds the name)", res.AliasesCreated)
	}
	if len(res.AliasesSkipped) != 1 || res.AliasesSkipped[0] != "Robert Galbraith" {
		t.Fatalf("AliasesSkipped = %v, want [Robert Galbraith]", res.AliasesSkipped)
	}

	// A merge with no conflict reports nothing skipped.
	t2 := seedAuthor(t, authors, "OL-T2", "Target Two")
	s2 := seedAuthor(t, authors, "OL-S2", "Source Two")
	res, err = aliases.Merge(ctx, s2.ID, t2.ID, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.AliasesCreated != 1 || len(res.AliasesSkipped) != 0 {
		t.Fatalf("clean merge = %+v, want one alias created and none skipped", res)
	}
}
