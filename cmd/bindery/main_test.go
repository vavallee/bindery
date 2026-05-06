package main

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

func TestGoogleBooksAPIKeyPrefersUISetting(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	settings := db.NewSettingsRepo(database)
	ctx := context.Background()

	if err := settings.Set(ctx, legacySettingGoogleBooksAPIKey, "legacy-key"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, settingGoogleBooksAPIKey, " ui-key "); err != nil {
		t.Fatal(err)
	}

	if got := googleBooksAPIKey(ctx, settings); got != "ui-key" {
		t.Fatalf("googleBooksAPIKey = %q, want ui-key", got)
	}
}

func TestGoogleBooksAPIKeyFallsBackToLegacySetting(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	settings := db.NewSettingsRepo(database)
	ctx := context.Background()

	if err := settings.Set(ctx, legacySettingGoogleBooksAPIKey, " legacy-key "); err != nil {
		t.Fatal(err)
	}

	if got := googleBooksAPIKey(ctx, settings); got != "legacy-key" {
		t.Fatalf("googleBooksAPIKey = %q, want legacy-key", got)
	}
}
