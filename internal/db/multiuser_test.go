package db_test

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestMultiUser_DataIsolation verifies that per-user scoped queries return
// only the rows owned by the requesting user. Two users are created; each
// gets a distinct author + book. ListByUser for each must return only their
// own records.
func TestMultiUser_DataIsolation(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	users := db.NewUserRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)

	// Create two users.
	u1, err := users.Create(ctx, "alice", "hash1")
	if err != nil {
		t.Fatalf("create user alice: %v", err)
	}
	u2, err := users.Create(ctx, "bob", "hash2")
	if err != nil {
		t.Fatalf("create user bob: %v", err)
	}

	// Promote alice to admin so we can verify role.
	if err := users.SetRole(ctx, u1.ID, "admin"); err != nil {
		t.Fatalf("set role: %v", err)
	}
	u1, _ = users.GetByID(ctx, u1.ID)
	if !u1.IsAdmin() {
		t.Errorf("alice should be admin after SetRole")
	}

	// Create one author per user.
	a1 := &models.Author{ForeignID: "ol-alice-1", Name: "Alice Author", SortName: "Author, Alice"}
	if err := authors.CreateForUser(ctx, a1, u1.ID); err != nil {
		t.Fatalf("create author for alice: %v", err)
	}
	a2 := &models.Author{ForeignID: "ol-bob-1", Name: "Bob Author", SortName: "Author, Bob"}
	if err := authors.CreateForUser(ctx, a2, u2.ID); err != nil {
		t.Fatalf("create author for bob: %v", err)
	}

	// Create one book per author (books inherit owner via author, but we explicitly set ownerUserID).
	b1 := &models.Book{ForeignID: "book-alice-1", AuthorID: a1.ID, Title: "Alice Book", SortTitle: "Alice Book"}
	if err := books.Create(ctx, b1); err != nil {
		t.Fatalf("create book for alice: %v", err)
	}
	// Manually update owner_user_id for alice's book (Create uses no user scope yet).
	if _, err := database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", u1.ID, b1.ID); err != nil {
		t.Fatalf("set book owner: %v", err)
	}

	b2 := &models.Book{ForeignID: "book-bob-1", AuthorID: a2.ID, Title: "Bob Book", SortTitle: "Bob Book"}
	if err := books.Create(ctx, b2); err != nil {
		t.Fatalf("create book for bob: %v", err)
	}
	if _, err := database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", u2.ID, b2.ID); err != nil {
		t.Fatalf("set book owner: %v", err)
	}

	// --- Author isolation ---
	aliceAuthors, err := authors.ListByUser(ctx, u1.ID)
	if err != nil {
		t.Fatalf("list authors for alice: %v", err)
	}
	if len(aliceAuthors) != 1 || aliceAuthors[0].Name != "Alice Author" {
		t.Errorf("alice should see 1 author (Alice Author); got %v", aliceAuthors)
	}

	bobAuthors, err := authors.ListByUser(ctx, u2.ID)
	if err != nil {
		t.Fatalf("list authors for bob: %v", err)
	}
	if len(bobAuthors) != 1 || bobAuthors[0].Name != "Bob Author" {
		t.Errorf("bob should see 1 author (Bob Author); got %v", bobAuthors)
	}

	// Global list (userID=0) should see both.
	allAuthors, err := authors.List(ctx)
	if err != nil {
		t.Fatalf("list all authors: %v", err)
	}
	if len(allAuthors) != 2 {
		t.Errorf("global list should return 2 authors; got %d", len(allAuthors))
	}

	// --- Book isolation ---
	aliceBooks, err := books.ListByUser(ctx, u1.ID)
	if err != nil {
		t.Fatalf("list books for alice: %v", err)
	}
	if len(aliceBooks) != 1 || aliceBooks[0].Title != "Alice Book" {
		t.Errorf("alice should see 1 book; got %v", aliceBooks)
	}

	bobBooks, err := books.ListByUser(ctx, u2.ID)
	if err != nil {
		t.Fatalf("list books for bob: %v", err)
	}
	if len(bobBooks) != 1 || bobBooks[0].Title != "Bob Book" {
		t.Errorf("bob should see 1 book; got %v", bobBooks)
	}

	// --- User management ---
	all, err := users.List(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 users; got %d", len(all))
	}

	// Cannot delete last admin.
	if err := users.Delete(ctx, u1.ID); err == nil {
		t.Error("should not be able to delete the last admin user")
	}

	// Demote alice, then we should be able to delete.
	if err := users.SetRole(ctx, u1.ID, "user"); err == nil {
		// This should fail — she's the only admin.
		t.Error("should not be able to demote the last admin")
	}
}
