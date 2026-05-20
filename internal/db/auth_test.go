package db

import (
	"context"
	"strings"
	"testing"
)

func TestGetByEmail_Found(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	// Create a user with an email by using the OIDC path (which stores email).
	if _, err := repo.GetOrCreateByOIDC(ctx, "https://idp.example", "sub-abc", "alice", "alice@example.com", "Alice"); err != nil {
		t.Fatalf("GetOrCreateByOIDC: %v", err)
	}

	got, err := repo.GetByEmail(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.Username != "alice" {
		t.Errorf("Username=%q, want alice", got.Username)
	}
}

func TestGetByEmail_NotFound(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	got, err := repo.GetByEmail(ctx, "unknown@x.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for unknown email, got %+v", got)
	}
}

func TestGetByEmail_Empty(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	got, err := repo.GetByEmail(ctx, "")
	if err != nil {
		t.Fatalf("GetByEmail empty: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty email, got %+v", got)
	}
}

// TestDeleteLastAdmin verifies that deleting the sole admin is rejected.
func TestDeleteLastAdmin(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	admin, err := repo.Create(ctx, "root", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}

	err = repo.Delete(ctx, admin.ID)
	if err == nil {
		t.Fatal("expected error when deleting last admin, got nil")
	}
	if !strings.Contains(err.Error(), "last admin") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestDeleteLastAdmin_TwoAdmins verifies that deleting one of two admins succeeds.
func TestDeleteLastAdmin_TwoAdmins(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	a, err := repo.Create(ctx, "alice", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := repo.Create(ctx, "bob", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetRole(ctx, b.ID, "admin"); err != nil {
		t.Fatal(err)
	}

	if err := repo.Delete(ctx, a.ID); err != nil {
		t.Errorf("expected success deleting one of two admins: %v", err)
	}
}

// TestSetRoleLastAdmin verifies that demoting the sole admin is rejected.
func TestSetRoleLastAdmin(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	admin, err := repo.Create(ctx, "root", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}

	err = repo.SetRole(ctx, admin.ID, "user")
	if err == nil {
		t.Fatal("expected error when demoting last admin, got nil")
	}
	if !strings.Contains(err.Error(), "last admin") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestSetRoleLastAdmin_TwoAdmins verifies that demoting one of two admins succeeds.
func TestSetRoleLastAdmin_TwoAdmins(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	a, err := repo.Create(ctx, "alice", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := repo.Create(ctx, "bob", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetRole(ctx, b.ID, "admin"); err != nil {
		t.Fatal(err)
	}

	if err := repo.SetRole(ctx, a.ID, "user"); err != nil {
		t.Errorf("expected success demoting one of two admins: %v", err)
	}
}

func TestLinkOIDCSubject(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	// Create a local user (no OIDC identity yet).
	u, err := repo.Create(ctx, "bob", "hashed-pw")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Before linking, GetByOIDC should return nil.
	before, err := repo.GetByOIDC(ctx, "https://idp.example", "sub-bob")
	if err != nil {
		t.Fatalf("GetByOIDC before link: %v", err)
	}
	if before != nil {
		t.Fatal("expected nil before link")
	}

	// Link the OIDC identity.
	if err := repo.LinkOIDCSubject(ctx, u.ID, "https://idp.example", "sub-bob"); err != nil {
		t.Fatalf("LinkOIDCSubject: %v", err)
	}

	// After linking, GetByOIDC should return the user.
	after, err := repo.GetByOIDC(ctx, "https://idp.example", "sub-bob")
	if err != nil {
		t.Fatalf("GetByOIDC after link: %v", err)
	}
	if after == nil {
		t.Fatal("expected user after link, got nil")
	}
	if after.ID != u.ID {
		t.Errorf("user ID after link: got %d, want %d", after.ID, u.ID)
	}
	if after.Username != "bob" {
		t.Errorf("Username after link: got %q, want bob", after.Username)
	}
}
