package db

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
)

// Copies made under tenancy keep each other's ids as secondaries: alice's A
// has primary OL1 and secondary HC1, bob's B the reverse. With tenancy off,
// making A's primary HC1 must still be refused. A already holds HC1 as an
// identifier row, and skipping the check for held rows let the update through,
// leaving two authors with primary HC1 instance wide (#3555 review).
func TestAuthorRepo_TenancyOffRefusesPrimaryThatCollidesViaHeldSecondary(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	ctx := context.Background()
	a := f.author(t, f.alice, "OL1", "HC1")
	b := f.author(t, f.bob, "HC1", "OL1")

	auth.SetEnforceTenancyForTests(t, false)

	got, err := f.authors.GetByID(ctx, a.ID)
	if err != nil || got == nil {
		t.Fatalf("read A: %v", err)
	}
	got.ForeignID = "HC1"
	var conflict *AuthorIdentifierConflictError
	if err := f.authors.Update(ctx, got); !errors.As(err, &conflict) {
		t.Fatalf("update A to primary HC1: err = %v, want a conflict", err)
	}
	if again, _ := f.authors.GetByID(ctx, a.ID); again == nil || again.ForeignID != "OL1" {
		t.Errorf("A's primary = %+v, want it unchanged after the refused update", again)
	}

	// Every copy still saves with the primary it has.
	f.mustUpdate(t, a.ID)
	f.mustUpdate(t, b.ID)
}

// Folding an author on account delete keeps its name as an alias on the kept
// row, so a bad provider mapping can be undone by hand.
func TestUserDelete_FoldKeepsFoldedAuthorsNameAsAlias(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	ctx := context.Background()
	f.author(t, f.alice, "OL-A", "OL1")
	b := f.author(t, f.bob, "OL1")

	to := f.bob
	if err := f.users.Delete(ctx, f.alice, UserDeletePlan{Strategy: ReassignOwnedRows, ReassignTo: &to}); err != nil {
		t.Fatalf("delete alice reassigning to bob: %v", err)
	}
	aliases, err := NewAuthorAliasRepo(f.authors.db).ListByAuthor(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, al := range aliases {
		if al.Name == "OL-A" && al.SourceOLID == "OL-A" {
			found = true
		}
	}
	if !found {
		t.Errorf("aliases of the kept author = %+v, want the folded author's name OL-A", aliases)
	}
}
