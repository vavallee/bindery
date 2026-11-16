package db

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
)

// Repository side of #3555: authors keyed per owner.

type perOwnerFixture struct {
	authors *AuthorRepo
	books   *BookRepo
	users   *UserRepo
	alice   int64
	bob     int64
}

func newPerOwnerFixture(t *testing.T, tenancy bool) perOwnerFixture {
	t.Helper()
	auth.SetEnforceTenancyForTests(t, tenancy)
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
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
	return perOwnerFixture{authors: NewAuthorRepo(database), books: NewBookRepo(database), users: users, alice: alice.ID, bob: bob.ID}
}

func (f perOwnerFixture) herbert(t *testing.T, owner int64) *models.Author {
	t.Helper()
	a := &models.Author{ForeignID: "OL-HERBERT", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "openlibrary"}
	if err := f.authors.CreateForUser(context.Background(), a, owner); err != nil {
		t.Fatalf("create herbert for %d: %v", owner, err)
	}
	return a
}

func (f perOwnerFixture) book(t *testing.T, author *models.Author, foreignID string, owner int64) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID: foreignID, AuthorID: author.ID, Title: foreignID, SortTitle: foreignID,
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
		OwnerUserID: owner,
	}
	if err := f.books.Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAuthorRepo_TenancyOnTwoOwnersShareProviderIDs(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	ctx := context.Background()
	alices := f.herbert(t, f.alice)
	bobs := f.herbert(t, f.bob)

	// Each can carry the same alternate id too.
	for _, a := range []*models.Author{alices, bobs} {
		if err := f.authors.UpsertAuthorIdentifier(ctx, a.ID, "hc:herbert"); err != nil {
			t.Fatalf("identifier for author %d: %v", a.ID, err)
		}
	}
	for _, tc := range []struct {
		user int64
		want int64
	}{{f.alice, alices.ID}, {f.bob, bobs.ID}} {
		for _, id := range []string{"OL-HERBERT", "hc:herbert"} {
			got, err := f.authors.GetByAnyForeignIDForUser(ctx, id, tc.user)
			if err != nil || got == nil || got.ID != tc.want {
				t.Errorf("user %d, %s: got %+v (err %v), want author %d", tc.user, id, got, err, tc.want)
			}
		}
		ids, err := f.authors.LibraryIDsByAnyForeignIDsForUser(ctx, []string{"OL-HERBERT", "hc:herbert"}, tc.user)
		if err != nil || ids["OL-HERBERT"] != tc.want || ids["hc:herbert"] != tc.want {
			t.Errorf("user %d: library ids = %v (err %v), want both on %d", tc.user, ids, err, tc.want)
		}
	}

	// Within one owner the id is still taken.
	other := &models.Author{ForeignID: "OL-OTHER", Name: "Other", SortName: "Other", MetadataProvider: "openlibrary"}
	if err := f.authors.CreateForUser(ctx, other, f.alice); err != nil {
		t.Fatal(err)
	}
	err := f.authors.UpsertAuthorIdentifier(ctx, other.ID, "hc:herbert")
	var conflict *AuthorIdentifierConflictError
	if !errors.As(err, &conflict) || conflict.AuthorID != alices.ID {
		t.Fatalf("alice's second author taking her Herbert's id = %v, want a conflict naming %d", err, alices.ID)
	}
	dup := &models.Author{ForeignID: "OL-HERBERT", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "openlibrary"}
	if err := f.authors.CreateForUser(ctx, dup, f.alice); err == nil {
		t.Fatal("a second Herbert row for alice was accepted")
	}

	// Unscoped lookups are deterministic: the oldest row.
	got, err := f.authors.GetByAnyForeignID(ctx, "OL-HERBERT")
	if err != nil || got == nil || got.ID != alices.ID {
		t.Errorf("unscoped lookup = %+v (err %v), want the oldest row %d", got, err, alices.ID)
	}
	ids, err := f.authors.LibraryIDsByAnyForeignIDsForUser(ctx, []string{"OL-HERBERT"}, 0)
	if err != nil || ids["OL-HERBERT"] != alices.ID {
		t.Errorf("unscoped library ids = %v (err %v), want %d", ids, err, alices.ID)
	}
}

// A shared row wins an unscoped lookup; a user's own row wins their scoped
// one over the shared row.
func TestAuthorRepo_LookupOrderPrefersSharedUnscopedAndOwnScoped(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	ctx := context.Background()
	alices := f.herbert(t, f.alice)
	shared := f.herbert(t, 0)

	if got, _ := f.authors.GetByForeignID(ctx, "OL-HERBERT"); got == nil || got.ID != shared.ID {
		t.Errorf("unscoped = %+v, want the shared row %d", got, shared.ID)
	}
	if got, _ := f.authors.GetByForeignIDForUser(ctx, "OL-HERBERT", f.alice); got == nil || got.ID != alices.ID {
		t.Errorf("alice's scoped = %+v, want her own row %d", got, alices.ID)
	}
	if got, _ := f.authors.GetByForeignIDForUser(ctx, "OL-HERBERT", f.bob); got == nil || got.ID != shared.ID {
		t.Errorf("bob's scoped = %+v, want the shared row %d", got, shared.ID)
	}
}

func TestAuthorRepo_TenancyOffKeepsProviderIDsInstanceWide(t *testing.T) {
	f := newPerOwnerFixture(t, false)
	ctx := context.Background()
	alices := f.herbert(t, f.alice)

	bobs := &models.Author{ForeignID: "OL-HERBERT", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "openlibrary"}
	err := f.authors.CreateForUser(ctx, bobs, f.bob)
	var conflict *AuthorIdentifierConflictError
	if !errors.As(err, &conflict) || conflict.AuthorID != alices.ID {
		t.Fatalf("bob's Herbert = %v, want a conflict naming alice's %d", err, alices.ID)
	}

	// Relinking another owner's author onto the taken id is refused too.
	other := &models.Author{ForeignID: "OL-OTHER", Name: "Other", SortName: "Other", MetadataProvider: "openlibrary"}
	if err := f.authors.CreateForUser(ctx, other, f.bob); err != nil {
		t.Fatal(err)
	}
	other.ForeignID = "OL-HERBERT"
	if err := f.authors.Update(ctx, other); !errors.As(err, &conflict) {
		t.Fatalf("update onto a taken id = %v, want a conflict", err)
	}
	if err := f.authors.UpsertAuthorIdentifier(ctx, other.ID, "OL-HERBERT"); !errors.As(err, &conflict) {
		t.Fatalf("identifier for a taken id = %v, want a conflict", err)
	}
	all, _ := f.authors.List(ctx)
	if len(all) != 2 {
		t.Fatalf("got %d authors, want 2", len(all))
	}
	for _, a := range all {
		if a.ID == other.ID && a.ForeignID != "OL-OTHER" {
			t.Fatalf("refused update still landed: %+v", a)
		}
	}
}

// UpgradeSyntheticDNB rewrites the one row it was handed, never another
// account's row carrying the same synthetic id.
func TestAuthorRepo_UpgradeSyntheticDNBTouchesOneRow(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	ctx := context.Background()
	mk := func(owner int64) *models.Author {
		a := &models.Author{ForeignID: "dnb:author:kari", Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: "dnb"}
		if err := f.authors.CreateForUser(ctx, a, owner); err != nil {
			t.Fatal(err)
		}
		return a
	}
	alices, bobs := mk(f.alice), mk(f.bob)
	if err := f.authors.UpgradeSyntheticDNB(ctx, alices.ID, alices.ForeignID, &models.Author{ForeignID: "OL-KARI", MetadataProvider: "openlibrary"}); err != nil {
		t.Fatal(err)
	}
	a, _ := f.authors.GetByID(ctx, alices.ID)
	b, _ := f.authors.GetByID(ctx, bobs.ID)
	if a.ForeignID != "OL-KARI" {
		t.Errorf("alice's row = %q, want upgraded", a.ForeignID)
	}
	if b.ForeignID != "dnb:author:kari" {
		t.Errorf("bob's row = %q, want untouched", b.ForeignID)
	}
}

// Deleting an account and handing its library to someone who holds their own
// row for the same author folds the two rows, instead of failing on the
// (owner, foreign_id) index or leaving two rows for the receiving account.
func TestUserDelete_ReassignFoldsIntoReceiversOwnRow(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	ctx := context.Background()
	alices := f.herbert(t, f.alice)
	bobs := f.herbert(t, f.bob)
	f.book(t, alices, "OL-DUNE", f.alice)
	bobsBook := f.book(t, bobs, "OL-MESSIAH", f.bob)

	to := f.alice
	if err := f.users.Delete(ctx, f.bob, UserDeletePlan{Strategy: ReassignOwnedRows, ReassignTo: &to}); err != nil {
		t.Fatalf("delete bob reassigning to alice: %v", err)
	}
	all, _ := f.authors.List(ctx)
	if len(all) != 1 || all[0].ID != alices.ID || all[0].OwnerUserID != f.alice {
		t.Fatalf("authors = %+v, want only alice's Herbert", all)
	}
	moved, _ := f.books.GetByID(ctx, bobsBook.ID)
	if moved == nil || moved.AuthorID != alices.ID || moved.OwnerUserID != f.alice {
		t.Fatalf("bob's book = %+v, want it alice's, under her Herbert", moved)
	}
	if ids, _ := f.authors.ListAuthorIdentifiers(ctx, alices.ID); len(ids) != 1 {
		t.Errorf("alice's identifiers = %+v, want the one OL-HERBERT row", ids)
	}
}

// The same for a purge: bob's Herbert holds a book of alice's, and alice has
// her own Herbert, so the rehome folds into hers.
func TestUserDelete_PurgeRehomeFoldsIntoOwnersRow(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	ctx := context.Background()
	alices := f.herbert(t, f.alice)
	bobs := f.herbert(t, f.bob)
	stray := f.book(t, bobs, "OL-STRAY", f.alice)
	f.book(t, bobs, "OL-BOBS", f.bob)

	if err := f.users.Delete(ctx, f.bob, UserDeletePlan{Strategy: PurgeOwnedRows}); err != nil {
		t.Fatalf("purge bob: %v", err)
	}
	all, _ := f.authors.List(ctx)
	if len(all) != 1 || all[0].ID != alices.ID {
		t.Fatalf("authors = %+v, want only alice's Herbert", all)
	}
	kept, _ := f.books.GetByID(ctx, stray.ID)
	if kept == nil || kept.AuthorID != alices.ID {
		t.Fatalf("alice's book = %+v, want it under her Herbert", kept)
	}
	if gone, _ := f.books.GetByForeignID(ctx, "OL-BOBS"); gone != nil {
		t.Errorf("bob's own book survived the purge: %+v", gone)
	}
}

// Merging two accounts' rows for one author (an admin action) must not fail
// on the identifiers both carry.
func TestAuthorMerge_AcrossOwnersWithSharedIdentifiers(t *testing.T) {
	f := newPerOwnerFixture(t, true)
	ctx := context.Background()
	alices := f.herbert(t, f.alice)
	bobs := f.herbert(t, f.bob)
	f.book(t, bobs, "OL-MESSIAH", f.bob)

	aliases := NewAuthorAliasRepo(f.authors.db)
	if _, err := aliases.Merge(ctx, bobs.ID, alices.ID, MergeOptions{}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got, _ := f.authors.GetByID(ctx, bobs.ID); got != nil {
		t.Errorf("source survived the merge: %+v", got)
	}
	if b, _ := f.books.GetByForeignID(ctx, "OL-MESSIAH"); b == nil || b.AuthorID != alices.ID {
		t.Errorf("book = %+v, want it under %d", b, alices.ID)
	}
}
