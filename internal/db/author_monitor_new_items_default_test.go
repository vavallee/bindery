package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestResolveAuthorMonitorNewItemsDefault(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	settings := NewSettingsRepo(database)
	ctx := context.Background()

	if got := ResolveAuthorMonitorNewItemsDefault(ctx, nil); got != models.AuthorMonitorNewItemsAll {
		t.Errorf("nil repo = %q, want all", got)
	}
	if got := ResolveAuthorMonitorNewItemsDefault(ctx, settings); got != models.AuthorMonitorNewItemsAll {
		t.Errorf("unset = %q, want all", got)
	}
	for _, tc := range []struct{ stored, want string }{
		{"none", "none"},
		{"all", "all"},
		{"", "all"},
		{"sometimes", "all"},
		{"NONE", "all"},
	} {
		if err := settings.Set(ctx, SettingAuthorDefaultMonitorNewItems, tc.stored); err != nil {
			t.Fatal(err)
		}
		if got := ResolveAuthorMonitorNewItemsDefault(ctx, settings); got != tc.want {
			t.Errorf("stored %q resolved to %q, want %q", tc.stored, got, tc.want)
		}
	}
}

// ApplyAuthorCreateDefaults is the one call every importer and implicit create
// path makes (#3958). An author with no policy gets the install default, and
// one that arrives with a valid policy, which is how the ABS and Calibre
// importers and Hardcover list sync pin "none", keeps it whatever the default.
func TestApplyAuthorCreateDefaults_MonitorNewItems(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	settings := NewSettingsRepo(database)
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		setting string
		preset  string
		want    string
	}{
		{"unset, no policy", "", "", "all"},
		{"default none, no policy", "none", "", "none"},
		{"default all, no policy", "all", "", "all"},
		{"default all keeps a pinned none", "all", "none", "none"},
		{"unset keeps a pinned none", "", "none", "none"},
		{"default none keeps a pinned all", "none", "all", "all"},
		{"garbage default falls back to all", "bogus", "", "all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := settings.Set(ctx, SettingAuthorDefaultMonitorNewItems, tc.setting); err != nil {
				t.Fatal(err)
			}
			a := &models.Author{ForeignID: "OL1A", Name: "A", MonitorNewItems: tc.preset}
			ApplyAuthorCreateDefaults(ctx, settings, a)
			if a.MonitorNewItems != tc.want {
				t.Errorf("MonitorNewItems = %q, want %q", a.MonitorNewItems, tc.want)
			}
		})
	}
}
