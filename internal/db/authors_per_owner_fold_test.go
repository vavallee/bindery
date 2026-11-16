package db

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
)

// #3555 review: a merge or a user delete must never leave two authors of one
// owner answering to the same provider id, primary or secondary. Every later
// Update of either would fail identifierHolder.

// author creates an author with the given primary id for owner, plus any
// secondary identifiers.
func (f perOwnerFixture) author(t *testing.T, owner int64, foreignID string, secondary ...string) *models.Author {
	t.Helper()
	ctx := context.Background()
	a := &models.Author{ForeignID: foreignID, Name: foreignID, SortName: foreignID, MetadataProvider: "openlibrary"}
	if err := f.authors.CreateForUser(ctx, a, owner); err != nil {
		t.Fatalf("create %s for %d: %v", foreignID, owner, err)
	}
	for _, id := range secondary {
		if err := f.authors.UpsertAuthorIdentifier(ctx, a.ID, id); err != nil {
			t.Fatalf("add %s to %s: %v", id, foreignID, err)
		}
	}
	return a
}

// assertNoIdentifierSharedWithinOwner checks the per owner rule over the whole
// table: no provider id, primary or secondary, answers to two authors of one
// owner.
func (f perOwnerFixture) assertNoIdentifierSharedWithinOwner(t *testing.T) {
	t.Helper()
	rows, err := f.authors.db.QueryContext(context.Background(), `
		WITH ident AS (`+authorIdentitySQL+`)
		SELECT COALESCE(a.owner_user_id, 0), i.foreign_id, COUNT(DISTINCT a.id)
		FROM ident i JOIN authors a ON a.id = i.author_id
		GROUP BY 1, 2 HAVING COUNT(DISTINCT a.id) > 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var owner, n int64
		var id string
		if err := rows.Scan(&owner, &id, &n); err != nil {
			t.Fatal(err)
		}
		t.Errorf("owner %d has %d authors answering to %q", owner, n, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// mustUpdate saves a fresh read of the author, the write a monitor toggle, a
// settings save or a scheduled refresh makes.
func (f perOwnerFixture) mustUpdate(t *testing.T, id int64) {
	t.Helper()
	ctx := context.Background()
	a, err := f.authors.GetByID(ctx, id)
	if err != nil || a == nil {
		t.Fatalf("read author %d: %v", id, err)
	}
	a.Monitored = !a.Monitored
	if err := f.authors.Update(ctx, a); err != nil {
		t.Errorf("update author %d: %v", id, err)
		return
	}
	ok, err := f.authors.UpdateIfUnchanged(ctx, a, a.UpdatedAtRaw)
	if err != nil || !ok {
		t.Errorf("update author %d if unchanged = %v, %v", id, ok, err)
	}
}

// An admin merging alice's author into one of bob's must not hand bob a
// second author holding an id another of his authors already holds. Before
// the fix the merge moved OL1 onto B2 and every later Update of B1 failed
// with "author identifier OL1 already belongs to author B2".
func TestAuthorMerge_AcrossOwnersDropsIdentifierTargetsOwnerHolds(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	ctx := context.Background()
	b1 := f.author(t, f.bob, "OL1")
	b2 := f.author(t, f.bob, "OL2")
	a := f.author(t, f.alice, "OL1", "OL9")

	res, err := NewAuthorAliasRepo(f.authors.db).Merge(ctx, a.ID, b2.ID, MergeOptions{})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(res.IdentifiersDropped) != 1 || res.IdentifiersDropped[0] != "OL1" {
		t.Errorf("dropped = %v, want [OL1]", res.IdentifiersDropped)
	}
	got := map[string]bool{}
	ids, _ := f.authors.ListAuthorIdentifiers(ctx, b2.ID)
	for _, id := range ids {
		got[id.ForeignID] = true
	}
	if got["OL1"] || !got["OL2"] || !got["OL9"] {
		t.Errorf("target identifiers = %+v, want OL2 and OL9 without OL1", ids)
	}
	f.assertNoIdentifierSharedWithinOwner(t)
	f.mustUpdate(t, b1.ID)
	f.mustUpdate(t, b2.ID)
}

// Deleting alice and reassigning to bob, where her author shares only a
// secondary id with bob's: the two are the same author, so hers folds into
// his. Before the fix only a primary match folded, bob ended with two authors
// holding OL1 and updates to his own author failed.
func TestUserDelete_ReassignFoldsOnSecondaryIdentifier(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	ctx := context.Background()
	a := f.author(t, f.alice, "OL-A", "OL1")
	b := f.author(t, f.bob, "OL1")
	dune := f.book(t, a, "OL-DUNE", f.alice)

	to := f.bob
	if err := f.users.Delete(ctx, f.alice, UserDeletePlan{Strategy: ReassignOwnedRows, ReassignTo: &to}); err != nil {
		t.Fatalf("delete alice reassigning to bob: %v", err)
	}
	all, _ := f.authors.List(ctx)
	if len(all) != 1 || all[0].ID != b.ID {
		t.Fatalf("authors = %+v, want only bob's", all)
	}
	moved, _ := f.books.GetByID(ctx, dune.ID)
	if moved == nil || moved.AuthorID != b.ID || moved.OwnerUserID != f.bob {
		t.Fatalf("alice's book = %+v, want it bob's, under his author", moved)
	}
	f.assertNoIdentifierSharedWithinOwner(t)
	f.mustUpdate(t, b.ID)
}

// The purge rehome has the same gap: bob's author holds one of alice's books
// and shares only a secondary id with alice's own author.
func TestUserDelete_PurgeRehomeFoldsOnSecondaryIdentifier(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	ctx := context.Background()
	a := f.author(t, f.alice, "OL1")
	x := f.author(t, f.bob, "OL-X", "OL1")
	stray := f.book(t, x, "OL-STRAY", f.alice)

	if err := f.users.Delete(ctx, f.bob, UserDeletePlan{Strategy: PurgeOwnedRows}); err != nil {
		t.Fatalf("purge bob: %v", err)
	}
	all, _ := f.authors.List(ctx)
	if len(all) != 1 || all[0].ID != a.ID {
		t.Fatalf("authors = %+v, want only alice's", all)
	}
	kept, _ := f.books.GetByID(ctx, stray.ID)
	if kept == nil || kept.AuthorID != a.ID {
		t.Fatalf("alice's book = %+v, want it under her author", kept)
	}
	f.assertNoIdentifierSharedWithinOwner(t)
	f.mustUpdate(t, a.ID)
}

// A source sharing ids with two of the receiver's authors folds into the one
// with its primary id, and the id the other one holds is dropped, not moved.
func TestUserDelete_ReassignFoldIntoOneOfTwoMatches(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	ctx := context.Background()
	a := f.author(t, f.alice, "OL1", "OL2")
	b1 := f.author(t, f.bob, "OL2")
	b2 := f.author(t, f.bob, "OL1")
	dune := f.book(t, a, "OL-DUNE", f.alice)

	to := f.bob
	if err := f.users.Delete(ctx, f.alice, UserDeletePlan{Strategy: ReassignOwnedRows, ReassignTo: &to}); err != nil {
		t.Fatalf("delete alice reassigning to bob: %v", err)
	}
	if moved, _ := f.books.GetByID(ctx, dune.ID); moved == nil || moved.AuthorID != b2.ID {
		t.Fatalf("alice's book = %+v, want it under %d (same primary id)", moved, b2.ID)
	}
	f.assertNoIdentifierSharedWithinOwner(t)
	f.mustUpdate(t, b1.ID)
	f.mustUpdate(t, b2.ID)
}

// Copies made under tenancy stay when it is turned off. Each must still save:
// re-writing an identifier the author already holds adds no duplicate, so it
// is not refused, while a new author for that id still is.
func TestAuthorRepo_CopiesStayEditableAfterTenancyOff(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	alices := f.herbert(t, f.alice)
	bobs := f.herbert(t, f.bob)

	auth.SetEnforceTenancyForTests(t, false)
	f.mustUpdate(t, alices.ID)
	f.mustUpdate(t, bobs.ID)

	carol := &models.Author{ForeignID: "OL-HERBERT", Name: "Frank Herbert", SortName: "Herbert, Frank"}
	var conflict *AuthorIdentifierConflictError
	if err := f.authors.CreateForUser(context.Background(), carol, 0); !errors.As(err, &conflict) {
		t.Errorf("new author for a held id with tenancy off: err = %v, want a conflict", err)
	}
}
