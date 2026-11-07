package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate122TurnsUpgradesOffForExistingProfiles: upgrade_allowed defaulted
// to 1 and was never read, so every existing profile says "on" without anyone
// having chosen it. The ebook upgrade sweep reads it now (#2420), so the
// migration clears it, and the new remove_replaced column starts at 0.
func TestMigrate122TurnsUpgradesOffForExistingProfiles(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	mustExec(t, database, `ALTER TABLE quality_profiles DROP COLUMN remove_replaced`)
	mustExec(t, database, `INSERT INTO quality_profiles (name, upgrade_allowed, cutoff, items) VALUES ('legacy', 1, 'epub', '[{"quality":"epub","allowed":true}]')`)

	v122 := migrationVersionForTest(t, "122_quality_profile_upgrade.sql")
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v122); err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 122: %v", err)
	}

	profiles, err := NewQualityProfileRepo(database).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) == 0 {
		t.Fatal("no profiles")
	}
	for _, p := range profiles {
		if p.UpgradeAllowed || p.RemoveReplaced {
			t.Errorf("profile %q: UpgradeAllowed=%v RemoveReplaced=%v, want both off", p.Name, p.UpgradeAllowed, p.RemoveReplaced)
		}
	}

	// A profile saved afterwards keeps what it is given.
	p := &models.QualityProfile{Name: "new", UpgradeAllowed: true, RemoveReplaced: true, Cutoff: "epub",
		Items: []models.QualityItem{{Quality: "epub", Allowed: true}}}
	if err := NewQualityProfileRepo(database).Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := NewQualityProfileRepo(database).GetByID(ctx, p.ID)
	if err != nil || got == nil || !got.UpgradeAllowed || !got.RemoveReplaced {
		t.Errorf("round trip = %+v, err %v", got, err)
	}
}
