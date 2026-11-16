package db

import (
	"context"
	"strings"
	"testing"
)

// TestMigrate131KeepsEveryAccountExplicit upgrades a database that is at 130,
// where requests_auto_approve is a plain 0 or 1 on every row, and checks each
// existing account keeps exactly the behaviour it had: 1 stays Always, 0 stays
// Never (not "unset", or turning the new system default on would switch it
// on), and only an account created afterwards is unset (#3900).
func TestMigrate131KeepsEveryAccountExplicit(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	on, err := repo.Create(ctx, "on", "pw")
	if err != nil {
		t.Fatal(err)
	}
	off, err := repo.Create(ctx, "off", "pw")
	if err != nil {
		t.Fatal(err)
	}

	// Put the database back at 130: run 131's own Down, then forget it ran.
	content, err := migrationsFS.ReadFile("migrations/131_requests_auto_approve_tristate.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	idx := strings.Index(s, "-- +migrate Down")
	if idx < 0 {
		t.Fatal("migration 131 has no Down section")
	}
	for _, stmt := range splitSQLStatements(s[idx+len("-- +migrate Down"):]) {
		mustExec(t, database, stmt)
	}
	mustExec(t, database, `DELETE FROM schema_migrations WHERE version = ?`,
		migrationVersionForTest(t, "131_requests_auto_approve_tristate.sql"))
	mustExec(t, database, `UPDATE users SET requests_auto_approve = 1 WHERE id = ?`, on.ID)
	mustExec(t, database, `UPDATE users SET requests_auto_approve = 0 WHERE id = ?`, off.ID)

	if err := migrate(database); err != nil {
		t.Fatalf("migrate 131 onto populated users: %v", err)
	}

	gotOn, _ := repo.GetByID(ctx, on.ID)
	if gotOn == nil || gotOn.RequestsAutoApprove == nil || !*gotOn.RequestsAutoApprove {
		t.Fatalf("an account at 1 did not stay on: %+v", gotOn)
	}
	gotOff, _ := repo.GetByID(ctx, off.ID)
	if gotOff == nil || gotOff.RequestsAutoApprove == nil || *gotOff.RequestsAutoApprove {
		t.Fatalf("an account at 0 must be an explicit off, not unset: %+v", gotOff)
	}

	fresh, err := repo.Create(ctx, "fresh", "pw")
	if err != nil {
		t.Fatal(err)
	}
	gotFresh, _ := repo.GetByID(ctx, fresh.ID)
	if gotFresh.RequestsAutoApprove != nil {
		t.Fatal("an account created after the migration should have no setting of its own")
	}
}

// Down folds the tri-state back: Always stays 1, Never and unset become 0.
func TestMigrate131DownFoldsTriStateBack(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)
	a, _ := repo.Create(ctx, "a", "pw")
	b, _ := repo.Create(ctx, "b", "pw")
	c, _ := repo.Create(ctx, "c", "pw")
	yes, no := true, false
	if err := repo.SetRequestsAutoApprove(ctx, a.ID, &yes); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetRequestsAutoApprove(ctx, b.ID, &no); err != nil {
		t.Fatal(err)
	}

	content, _ := migrationsFS.ReadFile("migrations/131_requests_auto_approve_tristate.sql")
	s := string(content)
	idx := strings.Index(s, "-- +migrate Down")
	for _, stmt := range splitSQLStatements(s[idx+len("-- +migrate Down"):]) {
		mustExec(t, database, stmt)
	}
	want := map[int64]int{a.ID: 1, b.ID: 0, c.ID: 0}
	for id, w := range want {
		var got int
		if err := database.QueryRow(`SELECT requests_auto_approve FROM users WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != w {
			t.Errorf("user %d folded to %d, want %d", id, got, w)
		}
	}
}

// The repo round trips all three states, and clearing returns to unset.
func TestUserRepo_RequestsAutoApproveTriState(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)
	u, _ := repo.Create(ctx, "reader", "pw")
	yes, no := true, false
	for _, v := range []*bool{&yes, &no, nil} {
		if err := repo.SetRequestsAutoApprove(ctx, u.ID, v); err != nil {
			t.Fatal(err)
		}
		got, _ := repo.GetByID(ctx, u.ID)
		switch {
		case v == nil && got.RequestsAutoApprove != nil:
			t.Fatalf("clearing left %v", *got.RequestsAutoApprove)
		case v != nil && (got.RequestsAutoApprove == nil || *got.RequestsAutoApprove != *v):
			t.Fatalf("set %v read back %v", *v, got.RequestsAutoApprove)
		}
	}
}
