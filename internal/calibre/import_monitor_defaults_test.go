package calibre

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestImporter_HonoursDefaultMonitorMode covers #1666, reported against a
// scratch Calibre import in external mode: with Settings → Metadata → Default
// monitor mode set to None, imported authors still landed in mode "all". The
// importer never read the setting, so normalizeAuthorMonitorDefaults stamped
// the column default on the way in.
func TestImporter_HonoursDefaultMonitorMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setting  string
		wantMode string
	}{
		{"none", models.AuthorMonitorModeNone, models.AuthorMonitorModeNone},
		{"future", models.AuthorMonitorModeFuture, models.AuthorMonitorModeFuture},
		{"unset keeps the historical default", "", models.AuthorMonitorModeAll},
	} {
		t.Run(tc.name, func(t *testing.T) {
			imp, fr, authorRepo, _, _, _, settingsRepo := newImporterFixture(t)
			ctx := context.Background()

			if tc.setting != "" {
				if err := settingsRepo.Set(ctx, db.SettingAuthorDefaultMonitorMode, tc.setting); err != nil {
					t.Fatalf("set setting: %v", err)
				}
			}

			fr.books = []CalibreBook{sampleCalibreBook(1, "Book One", "Alice Author")}
			if _, err := imp.Run(ctx, "/lib"); err != nil {
				t.Fatalf("run: %v", err)
			}

			authors, err := authorRepo.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(authors) != 1 {
				t.Fatalf("authors = %d, want 1", len(authors))
			}
			if got := authors[0].MonitorMode; got != tc.wantMode {
				t.Errorf("MonitorMode = %q, want %q", got, tc.wantMode)
			}
			// MonitorNewItems is a separate #1348 policy the importer pins to
			// "none" regardless of the global default — assert it survived.
			if got := authors[0].MonitorNewItems; got != models.AuthorMonitorNewItemsNone {
				t.Errorf("MonitorNewItems = %q, want %q", got, models.AuthorMonitorNewItemsNone)
			}
		})
	}
}

// The latest-count default has to travel with the mode, or "latest" authors
// silently fall back to 1 book instead of the configured N.
func TestImporter_HonoursDefaultMonitorLatestCount(t *testing.T) {
	imp, fr, authorRepo, _, _, _, settingsRepo := newImporterFixture(t)
	ctx := context.Background()

	if err := settingsRepo.Set(ctx, db.SettingAuthorDefaultMonitorMode, models.AuthorMonitorModeLatest); err != nil {
		t.Fatal(err)
	}
	if err := settingsRepo.Set(ctx, db.SettingAuthorDefaultMonitorLatestCount, "4"); err != nil {
		t.Fatal(err)
	}

	fr.books = []CalibreBook{sampleCalibreBook(1, "Book One", "Alice Author")}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("run: %v", err)
	}

	authors, err := authorRepo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want 1", len(authors))
	}
	if authors[0].MonitorMode != models.AuthorMonitorModeLatest {
		t.Errorf("MonitorMode = %q, want %q", authors[0].MonitorMode, models.AuthorMonitorModeLatest)
	}
	if authors[0].MonitorLatestCount != 4 {
		t.Errorf("MonitorLatestCount = %d, want 4", authors[0].MonitorLatestCount)
	}
}

// The Calibre importer pins monitor new items to none because a library is a
// partial catalogue by design. The install wide default (#3958) must not move
// that pin, whichever way it is set.
func TestImporter_PinsMonitorNewItemsWhateverTheDefault(t *testing.T) {
	for _, setting := range []string{"all", "none"} {
		t.Run("default "+setting, func(t *testing.T) {
			imp, fr, authorRepo, _, _, _, settingsRepo := newImporterFixture(t)
			ctx := context.Background()
			if err := settingsRepo.Set(ctx, db.SettingAuthorDefaultMonitorNewItems, setting); err != nil {
				t.Fatal(err)
			}
			fr.books = []CalibreBook{sampleCalibreBook(1, "Book One", "Alice Author")}
			if _, err := imp.Run(ctx, "/lib"); err != nil {
				t.Fatalf("run: %v", err)
			}
			authors, err := authorRepo.List(ctx)
			if err != nil || len(authors) != 1 {
				t.Fatalf("authors = %d, err %v", len(authors), err)
			}
			if got := authors[0].MonitorNewItems; got != models.AuthorMonitorNewItemsNone {
				t.Errorf("MonitorNewItems = %q, want the importer's pin (none)", got)
			}
		})
	}
}
