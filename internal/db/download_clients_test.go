package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestDownloadClientRepoHydratesLegacyCredentialStorage(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	now := time.Now().UTC()
	result, err := database.ExecContext(context.Background(), `
		INSERT INTO download_clients (
			name, type, host, port, api_key, use_ssl, url_base, username, password,
			category, priority, enabled, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"Legacy qBit", "qbittorrent", "10.10.10.10", 8080, "old-pass", 0, "old-user", "old-user", "old-pass",
		"books", 0, 1, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	client, err := NewDownloadClientRepo(database).GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if client == nil {
		t.Fatal("expected download client")
		return
	}
	if client.Username != "old-user" || client.Password != "old-pass" {
		t.Fatalf("credentials = %q/%q, want legacy values", client.Username, client.Password)
	}
	if client.URLBase != "" {
		t.Fatalf("urlBase = %q, want cleared legacy credential value", client.URLBase)
	}
}

func TestDownloadClientRepoPreservesRealURLBase(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	client := &models.DownloadClient{
		Name:     "Proxied qBit",
		Type:     "qbittorrent",
		Host:     "10.10.10.10",
		Port:     8080,
		Username: "admin",
		Password: "secret",
		URLBase:  "/qbit",
		Category: "books",
		Enabled:  true,
	}
	if err := NewDownloadClientRepo(database).Create(context.Background(), client); err != nil {
		t.Fatal(err)
	}

	got, err := NewDownloadClientRepo(database).GetByID(context.Background(), client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected download client")
		return
	}
	if got.URLBase != "/qbit" {
		t.Fatalf("urlBase = %q, want /qbit", got.URLBase)
	}
}

func TestDownloadClientRepoPreservesBareURLBaseMatchingUsername(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	client := &models.DownloadClient{
		Name:     "Proxied Transmission",
		Type:     "transmission",
		Host:     "10.10.10.10",
		Port:     9091,
		Username: "transmission",
		Password: "secret",
		URLBase:  "transmission",
		Category: "books",
		Enabled:  true,
	}
	if err := NewDownloadClientRepo(database).Create(context.Background(), client); err != nil {
		t.Fatal(err)
	}

	got, err := NewDownloadClientRepo(database).GetByID(context.Background(), client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected download client")
		return
	}
	if got.URLBase != "transmission" {
		t.Fatalf("urlBase = %q, want bare reverse-proxy path preserved", got.URLBase)
	}
	if got.Username != "transmission" || got.Password != "secret" {
		t.Fatalf("credentials = %q/%q, want configured credentials", got.Username, got.Password)
	}
}

// TestDownloadClientRepoRoundTripsCategoryAudiobook verifies the per-media-type
// category field (#700) survives Create → GetByID → List round-trips.
func TestDownloadClientRepoRoundTripsCategoryAudiobook(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	repo := NewDownloadClientRepo(database)
	client := &models.DownloadClient{
		Name:              "qBit",
		Type:              "qbittorrent",
		Host:              "10.0.0.1",
		Port:              8080,
		Username:          "admin",
		Password:          "secret",
		Category:          "books",
		CategoryAudiobook: "audiobooks",
		Enabled:           true,
	}
	if err := repo.Create(context.Background(), client); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetByID(context.Background(), client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected download client")
		return
	}
	if got.Category != "books" || got.CategoryAudiobook != "audiobooks" {
		t.Fatalf("categories = %q/%q, want books/audiobooks", got.Category, got.CategoryAudiobook)
	}

	got.CategoryAudiobook = "audio"
	if err := repo.Update(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	listed, err := repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].CategoryAudiobook != "audio" {
		t.Fatalf("after update: listed=%+v, want CategoryAudiobook=audio", listed)
	}
}

// TestDownloadClientRepoDefaultsCategoryAudiobookEmpty proves that pre-#700
// rows (where the column defaults to "") survive the new SELECT shape so
// existing deployments keep working.
func TestDownloadClientRepoDefaultsCategoryAudiobookEmpty(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	now := time.Now().UTC()
	// Insert a row through the raw schema with no category_audiobook value —
	// the migration's default '' should hold and round-trip cleanly.
	result, err := database.ExecContext(context.Background(), `
		INSERT INTO download_clients (
			name, type, host, port, api_key, use_ssl, url_base, username, password,
			category, priority, enabled, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"Legacy qBit", "qbittorrent", "10.0.0.2", 8080, "", 0, "", "admin", "pw",
		"books", 0, 1, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewDownloadClientRepo(database).GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected download client")
		return
	}
	if got.CategoryAudiobook != "" {
		t.Fatalf("CategoryAudiobook = %q, want empty fallback", got.CategoryAudiobook)
	}
}

func TestDownloadClientRepoPreservesNewBareURLBaseWithoutLegacyAPIKey(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	now := time.Now().UTC()
	result, err := database.ExecContext(context.Background(), `
		INSERT INTO download_clients (
			name, type, host, port, api_key, use_ssl, url_base, username, password,
			category, priority, enabled, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"Proxied Transmission", "transmission", "10.10.10.10", 9091, "", 0, "transmission", "", "",
		"books", 0, 1, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	client, err := NewDownloadClientRepo(database).GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if client == nil {
		t.Fatal("expected download client")
		return
	}
	if client.URLBase != "transmission" {
		t.Fatalf("urlBase = %q, want bare reverse-proxy path preserved", client.URLBase)
	}
	if client.Username != "" || client.Password != "" {
		t.Fatalf("credentials = %q/%q, want empty credentials for new URL-base-only row", client.Username, client.Password)
	}
}
