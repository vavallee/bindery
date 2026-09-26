package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// The per-account request auto approval flag (#2718) must default off, survive
// a round trip through GetByID, and be settable back off. A new account must
// never start approving its own requests just because the column exists.
func TestUserRepo_RequestsAutoApprove(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	u, err := repo.Create(ctx, "reader", "pw")
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestsAutoApprove {
		t.Fatal("a new account defaults to auto approving its requests")
	}

	if err := repo.SetRequestsAutoApprove(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ = repo.GetByID(ctx, u.ID); !got.RequestsAutoApprove {
		t.Fatal("auto approve did not persist")
	}
	// List shares scanUser with GetByID, but it is a different query, so check
	// the flag survives there too.
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].RequestsAutoApprove {
		t.Fatalf("List did not carry auto approve: %+v", list)
	}

	if err := repo.SetRequestsAutoApprove(ctx, u.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ = repo.GetByID(ctx, u.ID); got.RequestsAutoApprove {
		t.Fatal("turning auto approve off did not persist")
	}
}

// An unknown id is an error, not a silent success: the admin route turns this
// into a 404 rather than telling an admin a typo worked.
func TestUserRepo_SetRequestsAutoApprove_UnknownUser(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewUserRepo(database)

	if err := repo.SetRequestsAutoApprove(context.Background(), 4242, true); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("error = %v, want ErrUserNotFound", err)
	}
}

// Migration 093 has to apply to an install that already has users. This
// reproduces the upgrade path by taking the column and its schema_migrations
// row back off a populated database and running the migrator again: the
// existing account must survive with the flag off, and the column must be
// writable once it is back.
func TestMigration093_AppliesToPopulatedUsers(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	u, err := repo.Create(ctx, "reader", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "ALTER TABLE users DROP COLUMN requests_auto_approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = 93"); err != nil {
		t.Fatal(err)
	}

	if err := migrate(database); err != nil {
		t.Fatalf("migrate onto a populated database: %v", err)
	}

	got, err := repo.GetByID(ctx, u.ID)
	if err != nil || got == nil {
		t.Fatalf("the existing user did not survive the migration: %v", err)
	}
	if got.RequestsAutoApprove {
		t.Fatal("the migration switched an existing account to auto approve")
	}
	if err := repo.SetRequestsAutoApprove(ctx, u.ID, true); err != nil {
		t.Fatalf("the column added by the migration is not writable: %v", err)
	}
	if got, _ = repo.GetByID(ctx, u.ID); !got.RequestsAutoApprove {
		t.Fatal("the column added by the migration did not persist a write")
	}
}

// The daily auto-approve quota counts only automatic approvals in the window,
// and ClaimAutoApprove refuses once the owner is at it, leaving the row for a
// human.
func TestRequestRepo_AutoApproveQuota(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	users := NewUserRepo(database)
	owner, err := users.Create(ctx, "reader", "pw")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := users.Create(ctx, "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRequestRepo(database)

	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	tomorrow := dayStart.Add(24 * time.Hour)

	mk := func(foreignID string) *models.LibraryRequest {
		r := &models.LibraryRequest{
			OwnerUserID: owner.ID, Kind: models.RequestKindBook,
			ForeignID: foreignID, PayloadJSON: "{}",
		}
		if err := repo.Create(ctx, r, 100); err != nil {
			t.Fatalf("create %s: %v", foreignID, err)
		}
		return r
	}

	for _, id := range []string{"OL1", "OL2"} {
		r := mk(id)
		claimed, err := repo.ClaimAutoApprove(ctx, r.ID, owner.ID, dayStart, 2)
		if err != nil {
			t.Fatalf("claim %s under the quota: %v", id, err)
		}
		if err := repo.Complete(ctx, r.ID, claimed.ClaimToken, nil, nil); err != nil {
			t.Fatalf("complete %s: %v", id, err)
		}
	}
	if n, err := repo.countAutoApprovedSince(ctx, owner.ID, dayStart); err != nil || n != 2 {
		t.Fatalf("count = %d (err %v), want 2", n, err)
	}
	if n, _ := repo.countAutoApprovedSince(ctx, owner.ID, tomorrow); n != 0 {
		t.Fatalf("next-day count = %d, want 0 (the window rolls)", n)
	}

	// A human approval is not part of the tally, so it does not spend the
	// requester's allowance.
	human := mk("OL3")
	claimed, err := repo.Claim(ctx, human.ID, admin.ID)
	if err != nil {
		t.Fatalf("admin claim: %v", err)
	}
	if err := repo.Complete(ctx, human.ID, claimed.ClaimToken, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := repo.countAutoApprovedSince(ctx, owner.ID, dayStart); n != 2 {
		t.Fatalf("count after a human approval = %d, want 2", n)
	}

	// At the quota the automatic claim is refused and the row stays pending.
	over := mk("OL4")
	if _, err := repo.ClaimAutoApprove(ctx, over.ID, owner.ID, dayStart, 2); !errors.Is(err, ErrRequestAutoApproveQuota) {
		t.Fatalf("claim at the quota error = %v, want ErrRequestAutoApproveQuota", err)
	}
	if got, _ := repo.GetByID(ctx, over.ID); got.Status != models.RequestStatusPending {
		t.Fatalf("status at the quota = %q, want pending", got.Status)
	}
	// The next window lets it through without a human.
	if _, err := repo.ClaimAutoApprove(ctx, over.ID, owner.ID, tomorrow, 2); err != nil {
		t.Fatalf("claim past the window: %v", err)
	}
}
