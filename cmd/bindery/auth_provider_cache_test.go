package main

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// The provider reads its settings through the cache, so a mode change and an
// API key or session secret rotation must still be seen on the very next call.
func TestDBAuthProvider_SettingChangesVisibleImmediately(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	settings := db.NewSettingsRepo(database)
	p := &dbAuthProvider{settings: settings, users: db.NewUserRepo(database)}
	ctx := context.Background()

	set := func(k, v string) {
		t.Helper()
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	set(api.SettingAuthMode, string(auth.ModeEnabled))
	set(api.SettingAuthAPIKey, "key-one")
	set(api.SettingAuthSessionSecret, "secret-one")
	if p.Mode() != auth.ModeEnabled || p.APIKey() != "key-one" || string(p.SessionSecret()) != "secret-one" {
		t.Fatal("initial read wrong")
	}

	set(api.SettingAuthMode, string(auth.ModeDisabled))
	if p.Mode() != auth.ModeDisabled {
		t.Fatalf("mode change not visible, got %v", p.Mode())
	}
	set(api.SettingAuthAPIKey, "key-two")
	if p.APIKey() != "key-two" {
		t.Fatalf("api key rotation not visible, got %q", p.APIKey())
	}
	if got := len(p.SessionSecrets()); got != 1 {
		t.Fatalf("secrets = %d, want 1", got)
	}
	set(api.SettingAuthSessionSecretPrevious, "secret-one")
	set(api.SettingAuthSessionSecret, "secret-two")
	secrets := p.SessionSecrets()
	if len(secrets) != 2 || string(secrets[0]) != "secret-two" || string(secrets[1]) != "secret-one" {
		t.Fatalf("secret rotation not visible: %q", secrets)
	}
}
