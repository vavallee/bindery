package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Each test breaks one table so exactly one lookup in the resolver fails, and
// holds the policy: a lookup failure is an error, never a quiet substitute.

func TestResolveMetadataProfile_LookupFailuresAreErrors(t *testing.T) {
	ctx := context.Background()
	gone := int64(999)

	t.Run("settings table unreadable while resolving the default", func(t *testing.T) {
		profiles, _, _ := newProfileResolveDB(t)
		mustExec(t, profiles.db, "DROP TABLE settings")
		if _, err := ResolveMetadataProfile(ctx, profiles, &models.Author{MetadataProfileID: &gone}); err == nil {
			t.Fatal("a failing settings read was swallowed")
		}
	})

	t.Run("profile table unreadable while finding the first profile", func(t *testing.T) {
		profiles, _, _ := newProfileResolveDB(t)
		mustExec(t, profiles.db, "ALTER TABLE metadata_profiles RENAME TO metadata_profiles_x")
		if _, err := ResolveMetadataProfile(ctx, profiles, nil); err == nil {
			t.Fatal("a failing profile lookup was swallowed")
		}
	})

	t.Run("the default setting is garbage", func(t *testing.T) {
		profiles, settings, _ := newProfileResolveDB(t)
		for _, v := range []string{"abc", "-3", "0", ""} {
			if err := settings.Set(ctx, SettingAuthorDefaultMetadataProfileID, v); err != nil {
				t.Fatal(err)
			}
			res, err := ResolveMetadataProfile(ctx, profiles, &models.Author{MetadataProfileID: &gone})
			if err != nil || res.Profile.ID != models.DefaultMetadataProfileID {
				t.Fatalf("setting %q: %+v err=%v, want the first profile", v, res, err)
			}
		}
	})

	t.Run("the fallback is logged once per author and missing id", func(t *testing.T) {
		metadataProfileFallbackLogged.Delete("4242/999")
		res := MetadataProfileResolution{RequestedID: 999, Profile: BuiltinMetadataProfile()}
		logMetadataProfileFallbackOnce(4242, res)
		if _, ok := metadataProfileFallbackLogged.Load("4242/999"); !ok {
			t.Fatal("first fallback was not recorded")
		}
		logMetadataProfileFallbackOnce(4242, res) // second call takes the already logged branch
	})
}

func TestDefaultMetadataProfileID_Order(t *testing.T) {
	ctx := context.Background()
	profiles, settings, _ := newProfileResolveDB(t)
	two := &models.MetadataProfile{Name: "Two"}
	if err := profiles.Create(ctx, two); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := defaultMetadataProfileID(ctx, profiles.db); err != nil || !ok || id != models.DefaultMetadataProfileID {
		t.Fatalf("no setting: %d %v %v", id, ok, err)
	}
	if err := settings.Set(ctx, SettingAuthorDefaultMetadataProfileID, " 2 "); err != nil {
		t.Fatal(err)
	}
	if id, _, _ := defaultMetadataProfileID(ctx, profiles.db); id != two.ID {
		t.Fatalf("setting with spaces: %d, want %d", id, two.ID)
	}
	mustExec(t, profiles.db, "DELETE FROM metadata_profiles")
	if _, ok, err := defaultMetadataProfileID(ctx, profiles.db); err != nil || ok {
		t.Fatalf("no profiles: ok=%v err=%v, want ok=false", ok, err)
	}
	mustExec(t, profiles.db, "ALTER TABLE metadata_profiles RENAME TO metadata_profiles_x")
	if _, _, err := defaultMetadataProfileID(ctx, profiles.db); err == nil {
		t.Fatal("a failing existence check was swallowed")
	}
}

func TestValidateDefaultAuthorMetadataProfileID(t *testing.T) {
	ctx := context.Background()
	_, settings, _ := newProfileResolveDB(t)
	if err := ValidateDefaultAuthorMetadataProfileID(ctx, settings, "1"); err != nil {
		t.Errorf("seeded profile refused: %v", err)
	}
	if err := ValidateDefaultAuthorMetadataProfileID(ctx, settings, "77"); err == nil {
		t.Error("missing profile accepted")
	}
	if err := ValidateDefaultAuthorMetadataProfileID(ctx, nil, "77"); err != nil {
		t.Errorf("nil settings repo should skip the existence check: %v", err)
	}
	if err := ValidateDefaultAuthorMetadataProfileID(ctx, nil, "x"); err == nil {
		t.Error("garbage accepted without a repo")
	}
	mustExec(t, settings.db, "ALTER TABLE metadata_profiles RENAME TO metadata_profiles_x")
	err := ValidateDefaultAuthorMetadataProfileID(ctx, settings, "1")
	if err == nil {
		t.Error("a failing existence check was reported as fine")
	}
}

func TestMetadataProfileRepo_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	closed := closedProfileRepo(t)
	if _, err := closed.CountAuthorsUsing(ctx, 1); err == nil {
		t.Error("CountAuthorsUsing swallowed a db error")
	}
	if _, err := closed.AuthorNamesUsing(ctx, 1, 5); err == nil {
		t.Error("AuthorNamesUsing swallowed a db error")
	}
	if err := closed.Delete(ctx, 1); err == nil {
		t.Error("Delete swallowed a db error")
	}
	if err := closed.DeleteMovingAuthors(ctx, 1, 2); err == nil {
		t.Error("DeleteMovingAuthors swallowed a db error")
	}

	profiles, _, authors := newProfileResolveDB(t)
	other := &models.MetadataProfile{Name: "Other"}
	if err := profiles.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	// Deleting a profile that does not exist is a no-op, as before.
	if err := profiles.Delete(ctx, 4040); err != nil {
		t.Errorf("deleting a missing profile: %v", err)
	}
	// Moving off a profile that does not exist reports sql.ErrNoRows-like
	// not found and leaves the target alone.
	if err := profiles.DeleteMovingAuthors(ctx, 4040, other.ID); err == nil {
		t.Error("move delete of a missing profile reported success")
	}
	if p, _ := profiles.GetByID(ctx, other.ID); p == nil {
		t.Error("target profile disappeared")
	}
	// Names come back sorted and limited.
	id := other.ID
	for _, n := range []string{"Zed", "Abe", "Moe"} {
		a := &models.Author{ForeignID: "n-" + n, Name: n, SortName: n, MetadataProfileID: &id}
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	names, err := profiles.AuthorNamesUsing(ctx, id, 2)
	if err != nil || len(names) != 2 || names[0] != "Abe" || names[1] != "Moe" {
		t.Fatalf("names = %v err=%v, want [Abe Moe]", names, err)
	}
	if n, _ := profiles.CountAuthorsUsing(ctx, id); n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
	// A failing author update aborts the move before the profile is deleted.
	mustExec(t, profiles.db, "ALTER TABLE authors RENAME TO authors_x")
	if err := profiles.DeleteMovingAuthors(ctx, 1, id); err == nil {
		t.Error("move delete swallowed an author update failure")
	}
}
