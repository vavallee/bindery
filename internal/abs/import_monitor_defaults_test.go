package abs

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestImporter_HonoursDefaultMonitorMode covers the Audiobookshelf half of
// #1666. Same defect as the Calibre and CSV paths: the importer created the
// author without a mode, so the column default "all" was stamped on the way in
// no matter what Settings → Metadata → Default monitor mode said.
func TestImporter_HonoursDefaultMonitorMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setting  string
		wantMode string
	}{
		{"none", models.AuthorMonitorModeNone, models.AuthorMonitorModeNone},
		{"unset keeps the historical default", "", models.AuthorMonitorModeAll},
	} {
		t.Run(tc.name, func(t *testing.T) {
			importer, authorRepo, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
			ctx := context.Background()

			if tc.setting != "" {
				if err := importer.settings.Set(ctx, db.SettingAuthorDefaultMonitorMode, tc.setting); err != nil {
					t.Fatalf("set setting: %v", err)
				}
			}

			item := sampleABSItem()
			importer.enumerateFn = func(ctx context.Context, _ string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
				if err := fn(ctx, item); err != nil {
					return EnumerationStats{}, err
				}
				return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
			}

			if _, err := importer.Run(ctx, ImportConfig{
				SourceID:  DefaultSourceID,
				BaseURL:   "https://abs.example.com",
				APIKey:    "secret",
				LibraryID: item.LibraryID,
				Label:     "Shelf",
				Enabled:   true,
			}); err != nil {
				t.Fatalf("Run: %v", err)
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
			if got := authors[0].MonitorNewItems; got != models.AuthorMonitorNewItemsNone {
				t.Errorf("MonitorNewItems = %q, want %q (#1348 policy must survive)", got, models.AuthorMonitorNewItemsNone)
			}
		})
	}
}

// The ABS importer pins monitor new items to none (partial catalogues by
// design). The install wide default (#3958) must not move that pin.
func TestImporter_PinsMonitorNewItemsWhateverTheDefault(t *testing.T) {
	for _, setting := range []string{"all", "none"} {
		t.Run("default "+setting, func(t *testing.T) {
			importer, authorRepo, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
			ctx := context.Background()
			if err := importer.settings.Set(ctx, db.SettingAuthorDefaultMonitorNewItems, setting); err != nil {
				t.Fatal(err)
			}
			item := sampleABSItem()
			importer.enumerateFn = func(ctx context.Context, _ string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
				if err := fn(ctx, item); err != nil {
					return EnumerationStats{}, err
				}
				return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
			}
			if _, err := importer.Run(ctx, ImportConfig{
				SourceID: DefaultSourceID, BaseURL: "https://abs.example.com", APIKey: "secret",
				LibraryID: item.LibraryID, Label: "Shelf", Enabled: true,
			}); err != nil {
				t.Fatalf("Run: %v", err)
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
