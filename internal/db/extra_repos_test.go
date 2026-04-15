package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestNotificationRepoCRUD covers the full CRUD cycle for NotificationRepo.
func TestNotificationRepoCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewNotificationRepo(database)

	// List empty
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 notifications, got %d", len(list))
	}

	// Create
	n := &models.Notification{
		Name:    "Pushover",
		Type:    "webhook",
		URL:     "https://example.com/webhook",
		Method:  "POST",
		Headers: `{}`,
		OnGrab:  true,
		Enabled: true,
	}
	if err := repo.Create(ctx, n); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n.ID == 0 {
		t.Error("expected non-zero ID after create")
	}

	// GetByID
	got, err := repo.GetByID(ctx, n.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result from GetByID")
		return
	}
	if got.Name != "Pushover" {
		t.Errorf("Name: want 'Pushover', got %q", got.Name)
	}
	if !got.OnGrab {
		t.Error("expected OnGrab=true")
	}
	if !got.Enabled {
		t.Error("expected Enabled=true")
	}

	// GetByID — missing
	missing, err := repo.GetByID(ctx, 9999)
	if err != nil {
		t.Fatalf("GetByID missing: %v", err)
	}
	if missing != nil {
		t.Errorf("expected nil for missing ID, got %+v", missing)
	}

	// Update
	n.OnImport = true
	n.Enabled = false
	if err := repo.Update(ctx, n); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = repo.GetByID(ctx, n.ID)
	if !got.OnImport {
		t.Error("expected OnImport=true after update")
	}
	if got.Enabled {
		t.Error("expected Enabled=false after update")
	}

	// List after create
	list, err = repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 notification, got %d", len(list))
	}

	// Delete
	if err := repo.Delete(ctx, n.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ = repo.List(ctx)
	if len(list) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(list))
	}
}

// TestTagRepoCRUD covers the tag and author-tag relationship methods.
func TestTagRepoCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewTagRepo(database)

	// Create
	tag := &models.Tag{Name: "fantasy"}
	if err := repo.Create(ctx, tag); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tag.ID == 0 {
		t.Error("expected non-zero tag ID")
	}

	// List
	tags, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tags) == 0 {
		t.Error("expected at least one tag")
	}

	// GetByID
	got, err := repo.GetByID(ctx, tag.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil || got.Name != "fantasy" {
		t.Errorf("GetByID: got %v", got)
	}

	// Author tags
	authorRepo := NewAuthorRepo(database)
	a := &models.Author{
		ForeignID: "OL-TAG-A", Name: "Tag Author", SortName: "Author, Tag",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatalf("create author: %v", err)
	}

	// SetAuthorTags
	if err := repo.SetAuthorTags(ctx, a.ID, []int64{tag.ID}); err != nil {
		t.Fatalf("SetAuthorTags: %v", err)
	}

	// GetAuthorTags
	authorTags, err := repo.GetAuthorTags(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetAuthorTags: %v", err)
	}
	if len(authorTags) != 1 || authorTags[0].ID != tag.ID {
		t.Errorf("GetAuthorTags: got %v", authorTags)
	}

	// Delete tag
	if err := repo.Delete(ctx, tag.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	tags, _ = repo.List(ctx)
	found := false
	for _, tg := range tags {
		if tg.ID == tag.ID {
			found = true
		}
	}
	if found {
		t.Error("tag should be deleted")
	}
}

// TestDelayProfileRepoCRUD covers all delay profile CRUD operations.
func TestDelayProfileRepoCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDelayProfileRepo(database)

	// List initial (there may be defaults)
	initial, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("initial List: %v", err)
	}

	// Create
	dp := &models.DelayProfile{
		UsenetDelay:       60,
		TorrentDelay:      30,
		PreferredProtocol: "usenet",
		EnableUsenet:      true,
		EnableTorrent:     false,
		Order:             1,
	}
	if err := repo.Create(ctx, dp); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if dp.ID == 0 {
		t.Error("expected non-zero ID")
	}

	// GetByID
	got, err := repo.GetByID(ctx, dp.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil delay profile")
	}
	if got.UsenetDelay != 60 {
		t.Errorf("UsenetDelay: want 60, got %d", got.UsenetDelay)
	}

	// GetByID — missing
	missing, _ := repo.GetByID(ctx, 99999)
	if missing != nil {
		t.Error("expected nil for missing ID")
	}

	// List now has one more
	all, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != len(initial)+1 {
		t.Errorf("expected %d profiles, got %d", len(initial)+1, len(all))
	}

	// Update
	dp.UsenetDelay = 120
	dp.EnableTorrent = true
	if err := repo.Update(ctx, dp); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = repo.GetByID(ctx, dp.ID)
	if got.UsenetDelay != 120 {
		t.Errorf("UsenetDelay after update: want 120, got %d", got.UsenetDelay)
	}

	// Delete
	if err := repo.Delete(ctx, dp.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ = repo.GetByID(ctx, dp.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

// TestCustomFormatRepoCRUD covers the custom_formats CRUD.
func TestCustomFormatRepoCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewCustomFormatRepo(database)

	// Create
	cf := &models.CustomFormat{
		Name: "Epub Only",
		Conditions: []models.CustomCondition{
			{Type: "releaseTitle", Pattern: "epub", Required: true},
		},
	}
	if err := repo.Create(ctx, cf); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if cf.ID == 0 {
		t.Error("expected non-zero ID")
	}

	// List
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) == 0 {
		t.Error("expected at least 1 custom format")
	}

	// GetByID
	got, err := repo.GetByID(ctx, cf.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil || got.Name != "Epub Only" {
		t.Errorf("GetByID: got %v", got)
	}

	// GetByID — missing
	missing, _ := repo.GetByID(ctx, 99999)
	if missing != nil {
		t.Error("expected nil for missing ID")
	}

	// Update
	cf.Name = "Epub Preferred"
	if err := repo.Update(ctx, cf); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = repo.GetByID(ctx, cf.ID)
	if got.Name != "Epub Preferred" {
		t.Errorf("Name after update: got %q", got.Name)
	}

	// Delete
	if err := repo.Delete(ctx, cf.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ = repo.List(ctx)
	for _, item := range list {
		if item.ID == cf.ID {
			t.Error("expected custom format to be deleted")
		}
	}
}

// TestImportListRepoCRUD covers import list CRUD and exclusions.
func TestImportListRepoCRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewImportListRepo(database)

	// Create
	il := &models.ImportList{
		Name:    "Goodreads List",
		Type:    "goodreads",
		URL:     "https://goodreads.com/list/1",
		Enabled: true,
	}
	if err := repo.Create(ctx, il); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if il.ID == 0 {
		t.Error("expected non-zero ID")
	}

	// List
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) == 0 {
		t.Error("expected at least 1 import list")
	}

	// GetByID
	got, err := repo.GetByID(ctx, il.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil || got.Name != "Goodreads List" {
		t.Errorf("GetByID: got %v", got)
	}

	// GetByID — missing
	missing, _ := repo.GetByID(ctx, 99999)
	if missing != nil {
		t.Error("expected nil for missing ID")
	}

	// Update
	il.Name = "Updated List"
	il.Enabled = false
	if err := repo.Update(ctx, il); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = repo.GetByID(ctx, il.ID)
	if got.Name != "Updated List" {
		t.Errorf("Name after update: got %q", got.Name)
	}

	// Exclusions
	excl := &models.ImportListExclusion{
		ForeignID:  "OL123W",
		Title:      "Excluded Book",
		AuthorName: "Some Author",
	}
	if err := repo.CreateExclusion(ctx, excl); err != nil {
		t.Fatalf("CreateExclusion: %v", err)
	}

	exclusions, err := repo.ListExclusions(ctx)
	if err != nil {
		t.Fatalf("ListExclusions: %v", err)
	}
	if len(exclusions) != 1 || exclusions[0].ForeignID != "OL123W" {
		t.Errorf("ListExclusions: got %v", exclusions)
	}

	if err := repo.DeleteExclusion(ctx, excl.ID); err != nil {
		t.Fatalf("DeleteExclusion: %v", err)
	}
	exclusions, _ = repo.ListExclusions(ctx)
	if len(exclusions) != 0 {
		t.Error("expected 0 exclusions after delete")
	}

	// Delete list
	if err := repo.Delete(ctx, il.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// TestQualityProfileRepoCRUD covers quality profile read operations
// (write ops handled by db_test.go default profile setup).
func TestQualityProfileRepo(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewQualityProfileRepo(database)

	// List — the 3 default profiles should exist
	profiles, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(profiles) == 0 {
		t.Fatal("expected default quality profiles")
	}

	// GetByID for the first profile
	p, err := repo.GetByID(ctx, profiles[0].ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil profile")
	}

	// GetByID — missing
	missing, _ := repo.GetByID(ctx, 99999)
	if missing != nil {
		t.Error("expected nil for missing ID")
	}
}

// TestDownloadClientRepo_ExtraMethods covers List, Update, Delete, GetFirstEnabled.
func TestDownloadClientRepo_ExtraMethods(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDownloadClientRepo(database)

	// GetFirstEnabled — empty
	client, err := repo.GetFirstEnabled(ctx)
	if err != nil {
		t.Fatalf("GetFirstEnabled empty: %v", err)
	}
	if client != nil {
		t.Error("expected nil when no clients")
	}

	// List — empty
	all, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0, got %d", len(all))
	}

	// Create a client
	dc := &models.DownloadClient{
		Name:    "My SAB",
		Type:    "sabnzbd",
		Host:    "localhost",
		Port:    8080,
		APIKey:  "k",
		Enabled: true,
	}
	if err := repo.Create(ctx, dc); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// List — one
	all, err = repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 client, got %d", len(all))
	}

	// GetFirstEnabled
	first, err := repo.GetFirstEnabled(ctx)
	if err != nil {
		t.Fatalf("GetFirstEnabled: %v", err)
	}
	if first == nil || first.Name != "My SAB" {
		t.Errorf("GetFirstEnabled: got %v", first)
	}

	// Update
	dc.Name = "Updated SAB"
	if err := repo.Update(ctx, dc); err != nil {
		t.Fatalf("Update: %v", err)
	}
	all, _ = repo.List(ctx)
	if all[0].Name != "Updated SAB" {
		t.Errorf("Name after update: got %q", all[0].Name)
	}

	// Delete
	if err := repo.Delete(ctx, dc.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	all, _ = repo.List(ctx)
	if len(all) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(all))
	}
}

// TestBookRepo_ExtraMethods covers GetByForeignID and SetFilePath.
func TestBookRepo_ExtraMethods(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)

	a := &models.Author{
		ForeignID: "OL-EX-A", Name: "Extra Author", SortName: "Author, Extra",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}

	b := &models.Book{
		ForeignID:        "OL-EX-W",
		AuthorID:         a.ID,
		Title:            "Extra Book",
		SortTitle:        "Extra Book",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	// GetByForeignID
	got, err := bookRepo.GetByForeignID(ctx, "OL-EX-W")
	if err != nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	if got == nil || got.Title != "Extra Book" {
		t.Errorf("GetByForeignID: got %v", got)
	}

	// GetByForeignID — missing
	missing, _ := bookRepo.GetByForeignID(ctx, "nonexistent")
	if missing != nil {
		t.Error("expected nil for missing foreign ID")
	}

	// SetFilePath
	if err := bookRepo.SetFilePath(ctx, b.ID, "/books/extra.epub"); err != nil {
		t.Fatalf("SetFilePath: %v", err)
	}
	got, _ = bookRepo.GetByForeignID(ctx, "OL-EX-W")
	if got.FilePath != "/books/extra.epub" {
		t.Errorf("FilePath after set: want '/books/extra.epub', got %q", got.FilePath)
	}
}

// TestSettingsRepo_Delete covers the Delete method.
func TestSettingsRepo_Delete(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewSettingsRepo(database)

	// Set a value, then delete it.
	if err := repo.Set(ctx, "to_delete", "value"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := repo.Delete(ctx, "to_delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := repo.Get(ctx, "to_delete")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %v", got)
	}
}

// TestIndexerRepo_Update covers the Update method.
func TestIndexerRepo_Update(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewIndexerRepo(database)

	idx := &models.Indexer{
		Name:           "Test Indexer",
		Type:           "newznab",
		URL:            "https://example.com/api",
		APIKey:         "testkey",
		Categories:     []int{7000},
		Priority:       10,
		Enabled:        true,
		SupportsSearch: true,
	}
	if err := repo.Create(ctx, idx); err != nil {
		t.Fatalf("Create: %v", err)
	}

	idx.Name = "Updated Indexer"
	idx.Priority = 20
	idx.Enabled = false
	if err := repo.Update(ctx, idx); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.GetByID(ctx, idx.ID)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if got.Name != "Updated Indexer" {
		t.Errorf("Name: want 'Updated Indexer', got %q", got.Name)
	}
	if got.Priority != 20 {
		t.Errorf("Priority: want 20, got %d", got.Priority)
	}
	if got.Enabled {
		t.Error("expected Enabled=false after update")
	}
}
