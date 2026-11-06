package db

import (
	"context"
	"database/sql"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestUserDelete_PurgeKeepsSharedConfig is the #3558 regression. Quality
// profiles, metadata profiles and root folders are instance wide config, but
// the create handlers stamp the creating admin as owner. Purging that admin
// used to delete them out from under every other account.
func TestUserDelete_PurgeKeepsSharedConfig(t *testing.T) {
	users, authors, database, adminID, victimID, _ := newDeleteFixture(t)
	ctx := context.Background()

	// A second admin so the first one may be deleted.
	other, err := users.Create(ctx, "second", "pw")
	if err != nil {
		t.Fatalf("create second admin: %v", err)
	}
	if _, err := database.Exec("UPDATE users SET role='admin' WHERE id=?", other.ID); err != nil {
		t.Fatalf("promote second admin: %v", err)
	}

	res, err := database.Exec("INSERT INTO quality_profiles (name, owner_user_id) VALUES ('Shared Q', ?)", adminID)
	if err != nil {
		t.Fatalf("seed quality profile: %v", err)
	}
	qpID, _ := res.LastInsertId()
	res, err = database.Exec("INSERT INTO metadata_profiles (name, owner_user_id) VALUES ('Shared M', ?)", adminID)
	if err != nil {
		t.Fatalf("seed metadata profile: %v", err)
	}
	mpID, _ := res.LastInsertId()
	res, err = database.Exec("INSERT INTO root_folders (path, owner_user_id) VALUES ('/shared', ?)", adminID)
	if err != nil {
		t.Fatalf("seed root folder: %v", err)
	}
	rfID, _ := res.LastInsertId()

	a := &models.Author{
		ForeignID: "OL7A", Name: "Other Author", SortName: "Author, Other",
		QualityProfileID: &qpID, MetadataProfileID: &mpID, RootFolderID: &rfID,
	}
	if err := authors.CreateForUser(ctx, a, victimID); err != nil {
		t.Fatalf("create author: %v", err)
	}

	if err := users.Delete(ctx, adminID, UserDeletePlan{Strategy: PurgeOwnedRows}); err != nil {
		t.Fatalf("purge admin: %v", err)
	}

	for _, c := range []struct {
		table string
		id    int64
	}{
		{"quality_profiles", qpID}, {"metadata_profiles", mpID}, {"root_folders", rfID},
	} {
		var n int
		var owner sql.NullInt64
		q := "SELECT COUNT(*), MAX(owner_user_id) FROM " + c.table + " WHERE id=?"
		if err := database.QueryRow(q, c.id).Scan(&n, &owner); err != nil {
			t.Fatalf("query %s: %v", c.table, err)
		}
		if n != 1 {
			t.Errorf("%s row %d was deleted by the purge", c.table, c.id)
		}
		if owner.Valid {
			t.Errorf("%s row %d owner = %d, want NULL (instance wide)", c.table, c.id, owner.Int64)
		}
	}

	got, err := authors.GetByID(ctx, a.ID)
	if err != nil || got == nil {
		t.Fatalf("other account's author lost: %v", err)
	}
	if got.QualityProfileID == nil || *got.QualityProfileID != qpID ||
		got.MetadataProfileID == nil || *got.MetadataProfileID != mpID ||
		got.RootFolderID == nil || *got.RootFolderID != rfID {
		t.Errorf("author no longer references the shared config: %+v", got)
	}
}
