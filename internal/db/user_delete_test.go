package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// newDeleteFixture builds an install with an admin plus a second user who owns
// one author, one book, one quality profile and one root folder. That is the
// shape #1899 could not delete: before the fix a single author was enough for
// SQLite to reject the DELETE, so the button worked only on unused accounts.
//
// It returns the repos and the admin id, victim id and author id.
func newDeleteFixture(t *testing.T) (*UserRepo, *AuthorRepo, *sql.DB, int64, int64, int64) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	users := NewUserRepo(database)
	authors := NewAuthorRepo(database)

	admin, err := users.Create(ctx, "root", "pw")
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := users.PromoteFirstUser(ctx); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	victim, err := users.Create(ctx, "victim", "pw")
	if err != nil {
		t.Fatalf("create victim: %v", err)
	}

	a := &models.Author{ForeignID: "OL1A", Name: "Owned Author", SortName: "Author, Owned"}
	if err := authors.CreateForUser(ctx, a, victim.ID); err != nil {
		t.Fatalf("create author: %v", err)
	}
	seed := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO books (foreign_id, author_id, title, sort_title, owner_user_id) VALUES ('OL1W', ?, 'Owned Book', 'Owned Book', ?)", []any{a.ID, victim.ID}},
		{"INSERT INTO quality_profiles (name, owner_user_id) VALUES ('Owned Profile', ?)", []any{victim.ID}},
		{"INSERT INTO root_folders (path, owner_user_id) VALUES ('/owned', ?)", []any{victim.ID}},
	}
	for _, s := range seed {
		if _, err := database.Exec(s.query, s.args...); err != nil {
			t.Fatalf("seed %q: %v", s.query, err)
		}
	}
	return users, authors, database, admin.ID, victim.ID, a.ID
}

// TestUserOwnedRows_CountsEveryOwnerTable is the precondition the API relies on
// to put real numbers in front of the admin.
func TestUserOwnedRows_CountsEveryOwnerTable(t *testing.T) {
	users, _, _, adminID, victimID, _ := newDeleteFixture(t)
	ctx := context.Background()

	counts, err := users.OwnedRows(ctx, victimID)
	if err != nil {
		t.Fatalf("owned rows: %v", err)
	}
	if counts.Authors != 1 || counts.Books != 1 || counts.QualityProfiles != 1 || counts.RootFolders != 1 {
		t.Errorf("unexpected counts: %+v", counts)
	}
	if counts.Total() != 4 {
		t.Errorf("total = %d, want 4", counts.Total())
	}

	adminCounts, err := users.OwnedRows(ctx, adminID)
	if err != nil {
		t.Fatalf("owned rows for admin: %v", err)
	}
	if adminCounts.Total() != 0 {
		t.Errorf("admin owns nothing but Total() = %d", adminCounts.Total())
	}
}

// TestUserDelete_ReassignToUser is the #1899 regression: an account that owns an
// author used to be undeletable. The rows must survive under the inheritor.
func TestUserDelete_ReassignToUser(t *testing.T) {
	users, authors, _, adminID, victimID, authorID := newDeleteFixture(t)
	ctx := context.Background()

	if err := users.Delete(ctx, victimID, UserDeletePlan{
		Strategy:   ReassignOwnedRows,
		ReassignTo: &adminID,
	}); err != nil {
		t.Fatalf("delete with reassign: %v", err)
	}

	if u, _ := users.GetByID(ctx, victimID); u != nil {
		t.Error("user still present after delete")
	}
	a, err := authors.GetByID(ctx, authorID)
	if err != nil || a == nil {
		t.Fatalf("author should survive reassignment: %v", err)
	}
	if a.OwnerUserID != adminID {
		t.Errorf("author owner = %d, want %d", a.OwnerUserID, adminID)
	}
	counts, err := users.OwnedRows(ctx, adminID)
	if err != nil {
		t.Fatalf("owned rows: %v", err)
	}
	if counts.Total() != 4 {
		t.Errorf("inheritor should hold all 4 rows; got %+v", counts)
	}
}

// TestUserDelete_ReassignGlobal checks the nil inheritor, which means NULL owner
// and therefore visible to every user, the meaning migration 039 established.
func TestUserDelete_ReassignGlobal(t *testing.T) {
	users, authors, _, _, victimID, authorID := newDeleteFixture(t)
	ctx := context.Background()

	if err := users.Delete(ctx, victimID, UserDeletePlan{Strategy: ReassignOwnedRows}); err != nil {
		t.Fatalf("delete with global reassign: %v", err)
	}
	a, err := authors.GetByID(ctx, authorID)
	if err != nil || a == nil {
		t.Fatalf("author should survive: %v", err)
	}
	// AuthorRepo scans owner_user_id through COALESCE(..., 0), so a NULL owner
	// reads back as 0, which every per-user query in this package treats as
	// shared.
	if a.OwnerUserID != 0 {
		t.Errorf("author owner = %d, want 0 (global)", a.OwnerUserID)
	}
}

// TestUserDelete_Purge removes the library with the user.
func TestUserDelete_Purge(t *testing.T) {
	users, authors, _, _, victimID, authorID := newDeleteFixture(t)
	ctx := context.Background()

	if err := users.Delete(ctx, victimID, UserDeletePlan{Strategy: PurgeOwnedRows}); err != nil {
		t.Fatalf("delete with purge: %v", err)
	}
	if u, _ := users.GetByID(ctx, victimID); u != nil {
		t.Error("user still present after purge")
	}
	if a, _ := authors.GetByID(ctx, authorID); a != nil {
		t.Error("author should be gone after purge")
	}
}

// TestUserDelete_PlanValidation covers the ways a caller can get the plan wrong.
// Each must fail before anything is written.
func TestUserDelete_PlanValidation(t *testing.T) {
	users, authors, _, _, victimID, authorID := newDeleteFixture(t)
	ctx := context.Background()
	missing := int64(9999)

	cases := []struct {
		name string
		plan UserDeletePlan
		want error
	}{
		{"no strategy", UserDeletePlan{}, ErrUnknownDeleteStrategy},
		{"bogus strategy", UserDeletePlan{Strategy: "evaporate"}, ErrUnknownDeleteStrategy},
		{"inherit to self", UserDeletePlan{Strategy: ReassignOwnedRows, ReassignTo: &victimID}, ErrReassignToSelf},
		{"inheritor missing", UserDeletePlan{Strategy: ReassignOwnedRows, ReassignTo: &missing}, ErrReassignTargetMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := users.Delete(ctx, victimID, tc.plan); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if u, _ := users.GetByID(ctx, victimID); u == nil {
				t.Error("user was deleted despite an invalid plan")
			}
			if a, _ := authors.GetByID(ctx, authorID); a == nil {
				t.Error("author was touched despite an invalid plan")
			}
		})
	}
}

// TestUserDelete_LastAdminStillGuarded makes sure resolving ownership did not
// open a way past the last-admin guard.
func TestUserDelete_LastAdminStillGuarded(t *testing.T) {
	users, _, _, adminID, _, _ := newDeleteFixture(t)
	ctx := context.Background()

	err := users.Delete(ctx, adminID, UserDeletePlan{Strategy: PurgeOwnedRows})
	if !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("err = %v, want ErrLastAdmin", err)
	}
	if u, _ := users.GetByID(ctx, adminID); u == nil {
		t.Error("last admin was deleted")
	}
}

// TestUserDelete_ClearsPerUserState covers the tables carrying a user_id with
// no foreign key. They never blocked the delete; they just accumulated rows no
// session could reach afterwards, which is the stranded state migration 069 had
// to repair. They are per-user judgements, so they are cleared under either
// strategy rather than handed to the inheritor.
func TestUserDelete_ClearsPerUserState(t *testing.T) {
	users, _, database, adminID, victimID, _ := newDeleteFixture(t)
	ctx := context.Background()

	seed := []string{
		"INSERT INTO recommendations (user_id, foreign_id, rec_type, title) VALUES (?, 'OL9W', 'author', 'Suggested')",
		"INSERT INTO recommendation_dismissals (user_id, foreign_id) VALUES (?, 'OL9W')",
		"INSERT INTO recommendation_author_exclusions (user_id, author_name) VALUES (?, 'Nope')",
	}
	for _, q := range seed {
		if _, err := database.Exec(q, victimID); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	if err := users.Delete(ctx, victimID, UserDeletePlan{
		Strategy:   ReassignOwnedRows,
		ReassignTo: &adminID,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	for _, table := range perUserStateTables {
		var n int
		if err := database.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE user_id = ?", victimID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s still holds %d row(s) for the deleted user", table, n)
		}
	}
}

// TestUserDelete_KeepsBlocklistEntriesButDropsAttribution: the blocklist is
// global by design (migration 050), so a release that was broken for one user
// is still broken for everyone. Only the audit attribution goes.
func TestUserDelete_KeepsBlocklistEntriesButDropsAttribution(t *testing.T) {
	users, _, database, adminID, victimID, _ := newDeleteFixture(t)
	ctx := context.Background()

	if _, err := database.Exec(
		"INSERT INTO blocklist (guid, title, created_by_user_id) VALUES ('guid-1', 'Bad Release', ?)", victimID,
	); err != nil {
		t.Fatalf("seed blocklist: %v", err)
	}

	if err := users.Delete(ctx, victimID, UserDeletePlan{
		Strategy:   ReassignOwnedRows,
		ReassignTo: &adminID,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var total, attributed int
	if err := database.QueryRow("SELECT COUNT(*) FROM blocklist").Scan(&total); err != nil {
		t.Fatalf("count blocklist: %v", err)
	}
	if total != 1 {
		t.Errorf("blocklist entry count = %d, want 1 (entries are global)", total)
	}
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM blocklist WHERE created_by_user_id IS NOT NULL",
	).Scan(&attributed); err != nil {
		t.Fatalf("count attributed: %v", err)
	}
	if attributed != 0 {
		t.Errorf("%d blocklist row(s) still attributed to the deleted user", attributed)
	}
}
