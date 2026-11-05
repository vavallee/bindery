package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/config"
	"github.com/vavallee/bindery/internal/db"
)

// Only the import roots may include admin configured import sources; the
// delete roots must not (#2168).
func TestImportSourcesReachOnlyImportRoots(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	settings := db.NewSettingsRepo(database)
	source := t.TempDir()
	if err := settings.Set(context.Background(), "import.source_folders", source); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LibraryDir: t.TempDir(), AudiobookDir: t.TempDir(), DownloadDir: t.TempDir(), AudiobookDownloadDir: t.TempDir()}
	file := filepath.Join(source, "a.epub")

	if !newImportRoots(cfg, nil, settings).Contains(context.Background(), file) {
		t.Error("import roots do not include the configured source")
	}
	if newLibraryRoots(cfg, nil).Contains(context.Background(), file) {
		t.Error("delete roots include the configured source")
	}
}
