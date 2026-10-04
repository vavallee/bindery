package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionRevocations_RevokeAndExpire(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()

	clock := time.Unix(1_800_000_000, 0)
	s := NewSessionRevocations(database)
	s.now = func() time.Time { return clock }

	if err := s.Revoke(ctx, "aaa", clock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Already expired: nothing to record.
	if err := s.Revoke(ctx, "old", clock.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if r, err := s.IsRevoked(ctx, "aaa"); err != nil || !r {
		t.Fatalf("IsRevoked(aaa) = %v, %v; want true", r, err)
	}
	if r, _ := s.IsRevoked(ctx, "bbb"); r {
		t.Fatal("unrevoked hash reported revoked")
	}

	// A second instance reads the persisted row, so a revocation survives a
	// restart.
	fresh := NewSessionRevocations(database)
	fresh.now = func() time.Time { return clock }
	if r, err := fresh.IsRevoked(ctx, "aaa"); err != nil || !r {
		t.Fatalf("after reload IsRevoked(aaa) = %v, %v; want true", r, err)
	}

	// Once the token itself has expired the entry is moot, and the next
	// revocation purges it from both the cache and the table.
	clock = clock.Add(2 * time.Hour)
	if r, _ := s.IsRevoked(ctx, "aaa"); r {
		t.Fatal("entry should lapse with the token's expiry")
	}
	if err := s.Revoke(ctx, "ccc", clock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := database.QueryRow("SELECT COUNT(*) FROM session_revocations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("table holds %d rows after purge; want 1", n)
	}
}

func TestCreateFirstAdmin_OnlyIntoEmptyTable(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	users := NewUserRepo(database)

	u, err := users.CreateFirstAdmin(ctx, "first", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != "admin" {
		t.Errorf("role = %q; want admin", u.Role)
	}
	got, err := users.GetByID(ctx, u.ID)
	if err != nil || got == nil || got.Role != "admin" || got.SessionEpoch != u.SessionEpoch {
		t.Fatalf("stored row = %+v, %v; want admin with epoch %d", got, err, u.SessionEpoch)
	}
	if _, err := users.CreateFirstAdmin(ctx, "second", "hash"); !errors.Is(err, ErrSetupComplete) {
		t.Fatalf("second CreateFirstAdmin err = %v; want ErrSetupComplete", err)
	}
	if n, _ := users.Count(ctx); n != 1 {
		t.Fatalf("users = %d; want 1", n)
	}
}
