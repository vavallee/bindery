package api

import (
	"net/http"
	"testing"
)

// This file covers OIDC role mapping (issue #688): the BINDERY_OIDC_DEFAULT_ROLE
// provisioning role, the BINDERY_OIDC_ADMIN_GROUP group-claim promotion/demotion
// (both claim shapes), and the promote-first-OIDC-user lockout-trap fallback.
//
// It reuses the fakeIDP / newCallbackTestHandler / doCallback harness from
// auth_oidc_callback_test.go.

// --- Part 1: BINDERY_OIDC_DEFAULT_ROLE -------------------------------------

// TestCallback_DefaultRole_User confirms the historical default: a provisioned
// OIDC user gets role "user".
func TestCallback_DefaultRole_User(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "u-default", "nonce": "test-nonce",
		"preferred_username": "alice",
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	// default role is "user" out of the box

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByOIDC(ctx, idp.server.URL, "u-default")
	if err != nil || u == nil {
		t.Fatalf("provisioned user lookup: u=%v err=%v", u, err)
	}
	if u.Role != "user" {
		t.Errorf("Role=%q, want user", u.Role)
	}
}

// TestCallback_DefaultRole_Admin confirms BINDERY_OIDC_DEFAULT_ROLE=admin
// provisions the new user as admin.
func TestCallback_DefaultRole_Admin(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "u-admin", "nonce": "test-nonce",
		"preferred_username": "admin1",
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCDefaultRole("admin")

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByOIDC(ctx, idp.server.URL, "u-admin")
	if err != nil || u == nil {
		t.Fatalf("provisioned user lookup: u=%v err=%v", u, err)
	}
	if u.Role != "admin" {
		t.Errorf("Role=%q, want admin", u.Role)
	}
}

// TestCallback_DefaultRole_InvalidCoerced confirms an invalid configured role
// is coerced to "user".
func TestCallback_DefaultRole_InvalidCoerced(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "u-bad", "nonce": "test-nonce", "preferred_username": "bad",
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCDefaultRole("superuser")

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByOIDC(ctx, idp.server.URL, "u-bad")
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	if u.Role != "user" {
		t.Errorf("Role=%q, want user (invalid configured role must coerce)", u.Role)
	}
}

// --- Part 2: BINDERY_OIDC_ADMIN_GROUP group-claim sync ----------------------

// TestCallback_GroupClaim_PromoteArrayShape verifies promotion to admin when the
// admin group is present and the group claim is a JSON array of strings.
func TestCallback_GroupClaim_PromoteArrayShape(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "g-array", "nonce": "test-nonce", "preferred_username": "ga",
		"groups": []string{"staff", "bindery-admin"},
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCAdminGroup("bindery-admin")

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByOIDC(ctx, idp.server.URL, "g-array")
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	if u.Role != "admin" {
		t.Errorf("Role=%q, want admin (in admin group, array shape)", u.Role)
	}
}

// TestCallback_GroupClaim_PromoteStringShape verifies promotion when the group
// claim is a single space/comma-delimited string rather than an array.
func TestCallback_GroupClaim_PromoteStringShape(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "g-string", "nonce": "test-nonce", "preferred_username": "gs",
		"groups": "staff bindery-admin,readers",
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCAdminGroup("bindery-admin")

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByOIDC(ctx, idp.server.URL, "g-string")
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	if u.Role != "admin" {
		t.Errorf("Role=%q, want admin (in admin group, delimited-string shape)", u.Role)
	}
}

// TestCallback_GroupClaim_DemoteOnAbsence verifies that an existing admin OIDC
// user is demoted to "user" when the admin group is absent from the claim — the
// IdP is authoritative.
func TestCallback_GroupClaim_DemoteOnAbsence(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "g-demote", "nonce": "test-nonce", "preferred_username": "gd",
		"groups": []string{"staff", "readers"}, // no bindery-admin
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCAdminGroup("bindery-admin")

	// Pre-seed the OIDC user as admin (e.g. promoted earlier or default-role).
	seeded, err := users.GetOrCreateByOIDC(ctx, idp.server.URL, "g-demote", "gd", "", "", "admin")
	if err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if seeded.Role != "admin" {
		t.Fatalf("setup: seeded role=%q, want admin", seeded.Role)
	}

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByID(ctx, seeded.ID)
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	if u.Role != "user" {
		t.Errorf("Role=%q, want user (admin group absent → demote)", u.Role)
	}
}

// TestCallback_GroupClaim_DemoteWhenClaimMissingEntirely verifies that when the
// group claim is absent altogether, an existing admin is demoted (fail-safe:
// absence of the group, not presence of a deny signal).
func TestCallback_GroupClaim_DemoteWhenClaimMissingEntirely(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "g-noclaim", "nonce": "test-nonce", "preferred_username": "gn",
		// no "groups" claim at all
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCAdminGroup("bindery-admin")

	seeded, err := users.GetOrCreateByOIDC(ctx, idp.server.URL, "g-noclaim", "gn", "", "", "admin")
	if err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByID(ctx, seeded.ID)
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	if u.Role != "user" {
		t.Errorf("Role=%q, want user (no group claim → demote)", u.Role)
	}
}

// TestCallback_GroupClaim_CustomClaimName verifies BINDERY_OIDC_GROUP_CLAIM
// selects a non-standard claim path.
func TestCallback_GroupClaim_CustomClaimName(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "g-custom", "nonce": "test-nonce", "preferred_username": "gc",
		"groups":         []string{"ignored"}, // wrong claim — must be ignored
		"bindery_groups": []string{"bindery-admin"},
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCAdminGroup("bindery-admin").WithOIDCGroupClaim("bindery_groups")

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByOIDC(ctx, idp.server.URL, "g-custom")
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	if u.Role != "admin" {
		t.Errorf("Role=%q, want admin (custom group claim path)", u.Role)
	}
}

// TestCallback_GroupClaim_Disabled verifies that with no admin group configured
// the group claim does not affect roles — a default-role user stays "user".
func TestCallback_GroupClaim_Disabled(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "g-off", "nonce": "test-nonce", "preferred_username": "go",
		"groups": []string{"bindery-admin"},
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	// no WithOIDCAdminGroup → group mapping disabled

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByOIDC(ctx, idp.server.URL, "g-off")
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	if u.Role != "user" {
		t.Errorf("Role=%q, want user (group mapping disabled)", u.Role)
	}
}

// --- Part 3: promote-first-OIDC-user fallback ------------------------------

// TestCallback_PromoteFirstOIDC_LocalAuthDisabled verifies the lockout-trap fix:
// with local auth disabled and zero admins, the first provisioned OIDC user is
// promoted to admin and the one-shot guard flag is set.
func TestCallback_PromoteFirstOIDC_LocalAuthDisabled(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "first-oidc", "nonce": "test-nonce", "preferred_username": "first",
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	h.WithLocalAuthEnabled(false)

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByOIDC(ctx, idp.server.URL, "first-oidc")
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	if u.Role != "admin" {
		t.Errorf("Role=%q, want admin (first OIDC user, no admins, local auth off)", u.Role)
	}
	// The one-shot guard must now be set.
	s, err := h.settings.Get(ctx, SettingOIDCFirstAdminPromoted)
	if err != nil {
		t.Fatalf("read guard: %v", err)
	}
	if s == nil || s.Value != "true" {
		t.Errorf("first-admin-promoted guard = %v, want set to true", s)
	}
}

// TestCallback_PromoteFirstOIDC_GuardPreventsRepromotion verifies that once the
// guard is set, a later OIDC login does NOT get auto-promoted even if every
// admin was deleted in the meantime.
func TestCallback_PromoteFirstOIDC_GuardPreventsRepromotion(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "second-oidc", "nonce": "test-nonce", "preferred_username": "second",
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	h.WithLocalAuthEnabled(false)

	// Simulate: the fallback already fired once.
	if err := h.settings.Set(ctx, SettingOIDCFirstAdminPromoted, "true"); err != nil {
		t.Fatalf("seed guard: %v", err)
	}

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByOIDC(ctx, idp.server.URL, "second-oidc")
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	if u.Role != "user" {
		t.Errorf("Role=%q, want user (guard set → no re-promotion)", u.Role)
	}
}

// TestCallback_PromoteFirstOIDC_SkippedWhenLocalAuthEnabled verifies the
// fallback does not fire while local auth is enabled (a recovery path exists).
func TestCallback_PromoteFirstOIDC_SkippedWhenLocalAuthEnabled(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "la-on", "nonce": "test-nonce", "preferred_username": "laon",
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	// localAuthEnabled defaults to true

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByOIDC(ctx, idp.server.URL, "la-on")
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	if u.Role != "user" {
		t.Errorf("Role=%q, want user (local auth on → no fallback)", u.Role)
	}
}

// TestCallback_PromoteFirstOIDC_SkippedWhenAdminExists verifies the fallback
// does not fire when at least one admin already exists.
func TestCallback_PromoteFirstOIDC_SkippedWhenAdminExists(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "has-admin", "nonce": "test-nonce", "preferred_username": "hasadmin",
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	h.WithLocalAuthEnabled(false)

	// Pre-existing admin via a different identity.
	if _, err := users.GetOrCreateByOIDC(ctx, "https://other.example", "existing-admin",
		"existingadmin", "", "", "admin"); err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByOIDC(ctx, idp.server.URL, "has-admin")
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	if u.Role != "user" {
		t.Errorf("Role=%q, want user (admin already exists → no fallback)", u.Role)
	}
}
