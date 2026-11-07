package main

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// With local sign in off and proxy mode on, first run setup is refused, so the
// first account the proxy provisions has to become the admin or the instance
// has none. These tests pin that rule and its limits.

func newProxyProvisioner(t *testing.T, localAuth bool) (*dbUserProvisioner, *db.UserRepo) {
	t.Helper()
	conn, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	users := db.NewUserRepo(conn)
	return &dbUserProvisioner{users: users, settings: db.NewSettingsRepo(conn), localAuthEnabled: localAuth}, users
}

func roleOf(t *testing.T, users *db.UserRepo, id int64) string {
	t.Helper()
	u, err := users.GetByID(context.Background(), id)
	if err != nil || u == nil {
		t.Fatalf("load user %d: %v", id, err)
	}
	return u.Role
}

func TestProxyProvisioning_FirstUserBecomesAdminWhenSetupIsRefused(t *testing.T) {
	p, users := newProxyProvisioner(t, false)
	ctx := context.Background()

	first, err := p.ResolveOrProvisionUser(ctx, "alice@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := roleOf(t, users, first); got != auth.RoleAdmin {
		t.Fatalf("first proxy user role = %q, want admin", got)
	}

	second, err := p.ResolveOrProvisionUser(ctx, "bob@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := roleOf(t, users, second); got != auth.RoleUser {
		t.Fatalf("second proxy user role = %q, want user", got)
	}

	// The same person signing in again is resolved, not re-promoted or recreated.
	again, err := p.ResolveOrProvisionUser(ctx, "alice@example.com", true)
	if err != nil || again != first {
		t.Fatalf("returning user resolved to %d (%v), want %d", again, err, first)
	}
}

func TestProxyProvisioning_NoPromotionWhenLocalSignInIsOn(t *testing.T) {
	p, users := newProxyProvisioner(t, true)
	id, err := p.ResolveOrProvisionUser(context.Background(), "alice@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := roleOf(t, users, id); got != auth.RoleUser {
		t.Fatalf("role = %q, want user: first run setup is the way to an admin here", got)
	}
}

func TestProxyProvisioning_PromotesOnlyOnce(t *testing.T) {
	p, users := newProxyProvisioner(t, false)
	ctx := context.Background()
	first, err := p.ResolveOrProvisionUser(ctx, "alice@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	// Every admin is later demoted; a new proxy login must not be promoted.
	if err := users.SetRoleUnguarded(ctx, first, auth.RoleUser); err != nil {
		t.Fatal(err)
	}
	later, err := p.ResolveOrProvisionUser(ctx, "mallory@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := roleOf(t, users, later); got != auth.RoleUser {
		t.Fatalf("later proxy user role = %q, want user: promotion is one shot", got)
	}
}

// An install that already has accounts but no admin (an older version let a
// proxy login beat first run setup) must not hand admin to the next new
// identity the proxy sends; the operator recovers with the API key instead.
func TestProxyProvisioning_NoPromotionOnAnInstallThatAlreadyHasUsers(t *testing.T) {
	p, users := newProxyProvisioner(t, false)
	ctx := context.Background()
	operator, err := users.Create(ctx, "operator@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := p.ResolveOrProvisionUser(ctx, "stranger@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := roleOf(t, users, stranger); got != auth.RoleUser {
		t.Fatalf("new proxy user on an existing install got %q, want user", got)
	}
	if got := roleOf(t, users, operator.ID); got != auth.RoleUser {
		t.Fatalf("operator role changed to %q", got)
	}
}

func TestProxyProvisioning_NoAutoProvisionLeavesUnknownUsersOut(t *testing.T) {
	p, users := newProxyProvisioner(t, false)
	id, err := p.ResolveOrProvisionUser(context.Background(), "alice@example.com", false)
	if err != nil || id != 0 {
		t.Fatalf("auto provisioning off resolved an unknown user to %d (%v), want 0", id, err)
	}
	if n, _ := users.Count(context.Background()); n != 0 {
		t.Fatalf("users = %d, want none created", n)
	}
}
