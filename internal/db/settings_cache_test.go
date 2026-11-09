package db

import (
	"context"
	"testing"
)

// A warm auth.* read must be served from memory: closing the database after
// the first read proves the second one never touches it.
func TestSettingsRepo_AuthKeyServedFromCache(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	repo := NewSettingsRepo(database)
	ctx := context.Background()
	if err := repo.Set(ctx, "auth.mode", "enabled"); err != nil {
		t.Fatal(err)
	}
	if s, err := repo.Get(ctx, "auth.mode"); err != nil || s == nil || s.Value != "enabled" {
		t.Fatalf("first read = %+v, %v", s, err)
	}
	_ = database.Close()
	s, err := repo.Get(ctx, "auth.mode")
	if err != nil || s == nil || s.Value != "enabled" {
		t.Fatalf("cached read after close = %+v, %v; want enabled from memory", s, err)
	}
	// A returned value is a copy: mutating it must not poison the cache.
	s.Value = "disabled"
	if s2, _ := repo.Get(ctx, "auth.mode"); s2 == nil || s2.Value != "enabled" {
		t.Fatalf("cache was mutated through a returned value: %+v", s2)
	}
}

// Non-auth keys stay uncached, so a write that bypasses the repo (backfill
// markers, tests) is still seen.
func TestSettingsRepo_NonAuthKeyNotCached(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewSettingsRepo(database)
	ctx := context.Background()
	if err := repo.Set(ctx, "other.key", "a"); err != nil {
		t.Fatal(err)
	}
	_, _ = repo.Get(ctx, "other.key")
	if _, err := database.Exec(`UPDATE settings SET value='b' WHERE key='other.key'`); err != nil {
		t.Fatal(err)
	}
	if s, _ := repo.Get(ctx, "other.key"); s == nil || s.Value != "b" {
		t.Fatalf("non-auth key was cached: %+v", s)
	}
}

// Every writer must be visible on the very next read, including a miss that
// was cached as absent.
func TestSettingsRepo_AuthWritesInvalidate(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewSettingsRepo(database)
	ctx := context.Background()
	get := func(key string) string {
		t.Helper()
		s, err := repo.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if s == nil {
			return "<nil>"
		}
		return s.Value
	}

	// Mode change.
	_ = repo.Set(ctx, "auth.mode", "enabled")
	if got := get("auth.mode"); got != "enabled" {
		t.Fatal(got)
	}
	_ = repo.Set(ctx, "auth.mode", "disabled")
	if got := get("auth.mode"); got != "disabled" {
		t.Fatalf("Set not visible: %s", got)
	}

	// API key rotation through SetMany.
	_ = repo.Set(ctx, "auth.api_key", "old")
	if got := get("auth.api_key"); got != "old" {
		t.Fatal(got)
	}
	if err := repo.SetMany(ctx, []SettingKV{{Key: "auth.api_key", Value: "new"}, {Key: "auth.session_secret", Value: "s1"}}); err != nil {
		t.Fatal(err)
	}
	if got := get("auth.api_key"); got != "new" {
		t.Fatalf("SetMany not visible: %s", got)
	}

	// Absent key cached as absent, then SetIfAbsent wins.
	if got := get("auth.session_secret_previous"); got != "<nil>" {
		t.Fatal(got)
	}
	if won, err := repo.SetIfAbsent(ctx, "auth.session_secret_previous", "p"); err != nil || !won {
		t.Fatalf("SetIfAbsent = %v, %v", won, err)
	}
	if got := get("auth.session_secret_previous"); got != "p" {
		t.Fatalf("SetIfAbsent not visible: %s", got)
	}

	// Delete.
	if err := repo.Delete(ctx, "auth.session_secret_previous"); err != nil {
		t.Fatal(err)
	}
	if got := get("auth.session_secret_previous"); got != "<nil>" {
		t.Fatalf("Delete not visible: %s", got)
	}
}
