package db

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func newProfileResolveDB(t *testing.T) (*MetadataProfileRepo, *SettingsRepo, *AuthorRepo) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return NewMetadataProfileRepo(database), NewSettingsRepo(database), NewAuthorRepo(database)
}

func TestResolveMetadataProfile(t *testing.T) {
	ctx := context.Background()
	profiles, settings, _ := newProfileResolveDB(t)
	two := &models.MetadataProfile{Name: "Two", AllowedLanguages: "ger"}
	three := &models.MetadataProfile{Name: "Three", AllowedLanguages: "fre"}
	for _, p := range []*models.MetadataProfile{two, three} {
		if err := profiles.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	id := func(n int64) *models.Author { return &models.Author{ID: 77, MetadataProfileID: &n} }

	// The author's own profile wins and is not a fallback.
	res, err := ResolveMetadataProfile(ctx, profiles, id(three.ID))
	if err != nil || res.Profile.Name != "Three" || res.FellBack {
		t.Fatalf("own profile: %+v err=%v", res, err)
	}
	// No id at all reads as "use the default" without being a fallback.
	res, err = ResolveMetadataProfile(ctx, profiles, &models.Author{})
	if err != nil || res.Profile.ID != models.DefaultMetadataProfileID || res.FellBack {
		t.Fatalf("no id: %+v err=%v", res, err)
	}
	if res, _ = ResolveMetadataProfile(ctx, profiles, nil); res.Profile == nil || res.FellBack {
		t.Fatalf("nil author: %+v", res)
	}

	// Seeded profile 1 deleted: a missing id resolves to the first existing.
	if err := profiles.Delete(ctx, models.DefaultMetadataProfileID); err != nil {
		t.Fatal(err)
	}
	res, err = ResolveMetadataProfile(ctx, profiles, id(models.DefaultMetadataProfileID))
	if err != nil || res.Profile.ID != two.ID || !res.FellBack || res.RequestedID != models.DefaultMetadataProfileID {
		t.Fatalf("first existing: %+v err=%v", res, err)
	}

	// The install default wins over the first profile, but only while it exists.
	if err := settings.Set(ctx, SettingAuthorDefaultMetadataProfileID, strconv.FormatInt(three.ID, 10)); err != nil {
		t.Fatal(err)
	}
	if res, _ = ResolveMetadataProfile(ctx, profiles, id(999)); res.Profile.ID != three.ID {
		t.Fatalf("configured default: %+v", res)
	}
	if err := settings.Set(ctx, SettingAuthorDefaultMetadataProfileID, "4242"); err != nil {
		t.Fatal(err)
	}
	if res, _ = ResolveMetadataProfile(ctx, profiles, id(999)); res.Profile.ID != two.ID {
		t.Fatalf("stale default setting: %+v", res)
	}

	// No profile at all: the built in default, which filters nothing.
	for _, p := range []*models.MetadataProfile{two, three} {
		if err := profiles.Delete(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	res, err = ResolveMetadataProfile(ctx, profiles, id(999))
	if err != nil || !res.BuiltIn || res.Profile == nil || res.Profile.AllowedLanguages != "" {
		t.Fatalf("built in: %+v err=%v", res, err)
	}

	// A nil repo is the built in default too; a broken database is an error.
	if res, err = ResolveMetadataProfile(ctx, nil, id(1)); err != nil || !res.BuiltIn {
		t.Fatalf("nil repo: %+v err=%v", res, err)
	}
	if _, err := ResolveMetadataProfile(ctx, closedProfileRepo(t), id(1)); err == nil {
		t.Fatal("a failing lookup returned no error")
	}
}

func closedProfileRepo(t *testing.T) *MetadataProfileRepo {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	return NewMetadataProfileRepo(database)
}

func TestCreateForUser_StampsAnExistingProfileWhenSeededOneIsGone(t *testing.T) {
	ctx := context.Background()
	profiles, settings, authors := newProfileResolveDB(t)
	two := &models.MetadataProfile{Name: "Two", AllowedLanguages: "ger"}
	three := &models.MetadataProfile{Name: "Three", AllowedLanguages: "fre"}
	for _, p := range []*models.MetadataProfile{two, three} {
		if err := profiles.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := profiles.Delete(ctx, models.DefaultMetadataProfileID); err != nil {
		t.Fatal(err)
	}

	a := &models.Author{ForeignID: "x1", Name: "A", SortName: "A"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if *a.MetadataProfileID != two.ID {
		t.Fatalf("stamped %d, want first existing %d", *a.MetadataProfileID, two.ID)
	}

	if err := settings.Set(ctx, SettingAuthorDefaultMetadataProfileID, strconv.FormatInt(three.ID, 10)); err != nil {
		t.Fatal(err)
	}
	b := &models.Author{ForeignID: "x2", Name: "B", SortName: "B"}
	if err := authors.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	if *b.MetadataProfileID != three.ID {
		t.Fatalf("stamped %d, want configured default %d", *b.MetadataProfileID, three.ID)
	}
}

func TestMetadataProfileDelete_GuardAndMove(t *testing.T) {
	ctx := context.Background()
	profiles, _, authors := newProfileResolveDB(t)
	other := &models.MetadataProfile{Name: "Other", AllowedLanguages: "eng"}
	if err := profiles.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	id := models.DefaultMetadataProfileID
	a := &models.Author{ForeignID: "g1", Name: "G", SortName: "G", MetadataProfileID: &id}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}

	err := profiles.Delete(ctx, id)
	var inUse *ErrMetadataProfileInUse
	if !errors.As(err, &inUse) || inUse.AuthorCount != 1 {
		t.Fatalf("delete error = %v, want in use by 1 author", err)
	}
	if !strings.Contains(inUse.Error(), "1 authors") {
		t.Errorf("message = %q", inUse.Error())
	}
	if p, _ := profiles.GetByID(ctx, id); p == nil {
		t.Fatal("refused delete removed the profile")
	}
	if err := profiles.DeleteMovingAuthors(ctx, id, id); err == nil {
		t.Error("moving to itself was allowed")
	}
	if err := profiles.DeleteMovingAuthors(ctx, id, 999); err == nil {
		t.Error("moving to a missing profile was allowed")
	}
	if p, _ := profiles.GetByID(ctx, id); p == nil {
		t.Fatal("a refused move deleted the profile")
	}
	if err := profiles.DeleteMovingAuthors(ctx, id, other.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := authors.GetByID(ctx, a.ID)
	if got == nil || *got.MetadataProfileID != other.ID {
		t.Fatalf("author after move = %+v", got)
	}
	if p, _ := profiles.GetByID(ctx, id); p != nil {
		t.Fatal("profile survived the move delete")
	}
}

// TestMigrate137RepairsOrphanedMetadataProfiles seeds the library from #4132:
// the seeded profile 1 gone, authors still naming it, one author on a profile
// that exists and one with no profile. Only the orphans move, a second run is a
// no-op, and Down leaves the data alone.
func TestMigrate137RepairsOrphanedMetadataProfiles(t *testing.T) {
	ctx := context.Background()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	profiles := NewMetadataProfileRepo(database)
	settings := NewSettingsRepo(database)
	authors := NewAuthorRepo(database)

	// A fresh database has nothing to repair: the migration ran at open.
	content, err := migrationsFS.ReadFile("migrations/137_authors_orphaned_metadata_profile.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	idx := strings.Index(s, "-- +migrate Down")
	if idx < 0 {
		t.Fatal("migration 137 has no Down section")
	}
	up := splitSQLStatements(strings.TrimPrefix(s[:idx], "-- +migrate Up"))
	down := splitSQLStatements(s[idx+len("-- +migrate Down"):])
	run := func(stmts []string) {
		for _, st := range stmts {
			mustExec(t, database, st)
		}
	}
	run(up)

	two := &models.MetadataProfile{Name: "Two", AllowedLanguages: "ger"}
	three := &models.MetadataProfile{Name: "Three", AllowedLanguages: "fre"}
	for _, p := range []*models.MetadataProfile{two, three} {
		if err := profiles.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	gone := models.DefaultMetadataProfileID
	mk := func(fid string, pid *int64) *models.Author {
		a := &models.Author{ForeignID: fid, Name: fid, SortName: fid, MetadataProfileID: pid}
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	orphan1, orphan2 := mk("o1", &gone), mk("o2", &gone)
	healthy := mk("h", &three.ID)
	mustExec(t, database, "DELETE FROM metadata_profiles WHERE id = ?", gone)
	mustExec(t, database, "UPDATE authors SET metadata_profile_id = NULL WHERE id = ?", orphan2.ID)
	orphan2Null := orphan2

	profileOf := func(a *models.Author) any {
		got, err := authors.GetByID(ctx, a.ID)
		if err != nil || got == nil {
			t.Fatalf("reload %d: %v", a.ID, err)
		}
		if got.MetadataProfileID == nil {
			return nil
		}
		return *got.MetadataProfileID
	}

	// With the default setting naming a profile that exists, that one is used.
	if err := settings.Set(ctx, SettingAuthorDefaultMetadataProfileID, strconv.FormatInt(three.ID, 10)); err != nil {
		t.Fatal(err)
	}
	run(up)
	if got := profileOf(orphan1); got != three.ID {
		t.Errorf("orphan with default setting = %v, want %d", got, three.ID)
	}

	// Without the setting, the lowest id that exists. Re-orphan and re-run,
	// twice, to prove idempotence.
	mustExec(t, database, "DELETE FROM settings WHERE key = ?", SettingAuthorDefaultMetadataProfileID)
	mustExec(t, database, "UPDATE authors SET metadata_profile_id = 999 WHERE id = ?", orphan1.ID)
	run(up)
	run(up)
	if got := profileOf(orphan1); got != two.ID {
		t.Errorf("orphan without default setting = %v, want %d", got, two.ID)
	}
	if got := profileOf(healthy); got != three.ID {
		t.Errorf("healthy author moved to %v", got)
	}
	if got := profileOf(orphan2Null); got != nil {
		t.Errorf("NULL author changed to %v", got)
	}

	// Down is harmless, and nothing happens on an install with no profiles.
	run(down)
	mustExec(t, database, "UPDATE authors SET metadata_profile_id = 999 WHERE id = ?", orphan1.ID)
	mustExec(t, database, "DELETE FROM metadata_profiles")
	run(up)
	if got := profileOf(orphan1); got != int64(999) {
		t.Errorf("author changed with no profile to point at: %v", got)
	}
}
