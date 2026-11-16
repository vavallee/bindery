package migrate

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// TestImportCSVAuthors_HonoursDefaultMonitorMode covers the severe half of
// #1666. A CSV row carries a monitored flag but never a monitor *mode*, so the
// author landed in mode "all"; the catalogue fetch that fires for every new
// author then monitors the entire upstream catalogue, everything goes Wanted,
// and the scheduler grabs the lot. One user reported ~1250 books queued this
// way (the report behind #1622). Setting the install-wide default to None did
// not help because this path never read it.
func TestImportCSVAuthors_HonoursDefaultMonitorMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setting  string
		wantMode string
	}{
		{"none", models.AuthorMonitorModeNone, models.AuthorMonitorModeNone},
		{"latest", models.AuthorMonitorModeLatest, models.AuthorMonitorModeLatest},
		{"unset keeps the historical default", "", models.AuthorMonitorModeAll},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := newTestDB(t)
			repo := db.NewAuthorRepo(database)
			settings := db.NewSettingsRepo(database)
			ctx := context.Background()

			if tc.setting != "" {
				if err := settings.Set(ctx, db.SettingAuthorDefaultMonitorMode, tc.setting); err != nil {
					t.Fatalf("set setting: %v", err)
				}
			}

			agg := metadata.NewAggregator(&stubProvider{
				searchAuthorsFn: func(_ context.Context, q string) ([]models.Author, error) {
					return []models.Author{{Name: q, SortName: q, ForeignID: "OL-" + q}}, nil
				},
				getAuthorFn: func(_ context.Context, id string) (*models.Author, error) {
					return &models.Author{Name: "Andy Weir", SortName: "Weir, Andy", ForeignID: id}, nil
				},
			})

			res, err := ImportCSVAuthors(ctx, strings.NewReader("Andy Weir,true\n"), repo, settings, agg, nil)
			if err != nil {
				t.Fatalf("ImportCSVAuthors: %v", err)
			}
			if res.Added != 1 {
				t.Fatalf("Added = %d, want 1 (%v)", res.Added, res.Failures)
			}

			authors, err := repo.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(authors) != 1 {
				t.Fatalf("authors = %d, want 1", len(authors))
			}
			if got := authors[0].MonitorMode; got != tc.wantMode {
				t.Errorf("MonitorMode = %q, want %q", got, tc.wantMode)
			}
			// The CSV's own monitored flag is a separate axis and must survive.
			if !authors[0].Monitored {
				t.Errorf("Monitored = false, want the CSV flag preserved")
			}
		})
	}
}

// A CSV import names no monitor new items policy, so it takes the install wide
// default (#3958), the same as it does for the monitor mode.
func TestImportCSVAuthors_HonoursDefaultMonitorNewItems(t *testing.T) {
	for _, tc := range []struct{ setting, want string }{
		{"none", models.AuthorMonitorNewItemsNone},
		{"all", models.AuthorMonitorNewItemsAll},
		{"", models.AuthorMonitorNewItemsAll},
	} {
		t.Run("default "+tc.setting, func(t *testing.T) {
			database := newTestDB(t)
			repo := db.NewAuthorRepo(database)
			settings := db.NewSettingsRepo(database)
			ctx := context.Background()
			if tc.setting != "" {
				if err := settings.Set(ctx, db.SettingAuthorDefaultMonitorNewItems, tc.setting); err != nil {
					t.Fatal(err)
				}
			}
			agg := metadata.NewAggregator(&stubProvider{
				searchAuthorsFn: func(_ context.Context, q string) ([]models.Author, error) {
					return []models.Author{{Name: q, SortName: q, ForeignID: "OL-" + q}}, nil
				},
				getAuthorFn: func(_ context.Context, id string) (*models.Author, error) {
					return &models.Author{Name: "Andy Weir", SortName: "Weir, Andy", ForeignID: id}, nil
				},
			})
			res, err := ImportCSVAuthors(ctx, strings.NewReader("Andy Weir,true\n"), repo, settings, agg, nil)
			if err != nil || res.Added != 1 {
				t.Fatalf("import: %v added=%d %v", err, res.Added, res.Failures)
			}
			authors, err := repo.List(ctx)
			if err != nil || len(authors) != 1 {
				t.Fatalf("authors = %d, err %v", len(authors), err)
			}
			if got := authors[0].MonitorNewItems; got != tc.want {
				t.Errorf("MonitorNewItems = %q, want %q", got, tc.want)
			}
		})
	}
}
