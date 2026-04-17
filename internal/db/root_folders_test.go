package db

import (
	"context"
	"testing"
)

func TestRootFolderRepo_CRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewRootFolderRepo(database)

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("want 0 folders, got %d", len(list))
	}

	// GetByID — not found returns (nil, nil) per contract.
	missing, err := repo.GetByID(ctx, 9999)
	if err != nil {
		t.Fatalf("GetByID missing: %v", err)
	}
	if missing != nil {
		t.Errorf("want nil for missing folder, got %+v", missing)
	}

	// Create
	f, err := repo.Create(ctx, "/library/books")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if f.ID == 0 {
		t.Fatal("expected non-zero ID")
	}
	if f.Path != "/library/books" {
		t.Errorf("Path: want /library/books, got %q", f.Path)
	}
	if f.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}

	// GetByID round-trips.
	got, err := repo.GetByID(ctx, f.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil || got.Path != f.Path {
		t.Errorf("GetByID mismatch: %+v", got)
	}

	// List contains the new folder.
	list, err = repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("want 1 folder, got %d", len(list))
	}

	// UpdateFreeSpace
	if err := repo.UpdateFreeSpace(ctx, f.ID, 1024*1024*1024); err != nil {
		t.Fatalf("UpdateFreeSpace: %v", err)
	}
	got, _ = repo.GetByID(ctx, f.ID)
	if got.FreeSpace != 1024*1024*1024 {
		t.Errorf("FreeSpace: want %d, got %d", 1024*1024*1024, got.FreeSpace)
	}

	// Delete
	if err := repo.Delete(ctx, f.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ = repo.List(ctx)
	if len(list) != 0 {
		t.Errorf("want 0 after delete, got %d", len(list))
	}
}
