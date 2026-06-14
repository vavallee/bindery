package auth

import (
	"context"
	"net/http"
	"testing"
)

// Regression tests for #560: /api/v1/auth/status was in the AllowUnauthPath
// list and the middleware short-circuited there before proxy identity
// resolution ran, so a valid X-Forwarded-User from a trusted CIDR never made
// it onto the request context. The status handler then reported
// authenticated:false, leaving proxy-authed users stuck on the login screen.
//
// The fix runs proxy header resolution up-front (alongside the cookie check)
// and only attaches identity when the source IP is in the trusted-proxy CIDR
// list — so an untrusted caller still cannot spoof proxy auth.

// TestProxyAuthStatusPathTrustedIPSetsContext verifies the positive case: a
// trusted CIDR (0.0.0.0/0) plus a valid X-Forwarded-User header on a request
// to /api/v1/auth/status must attach the user id to the context, so the
// status handler can report authenticated:true.
func TestProxyAuthStatusPathTrustedIPSetsContext(t *testing.T) {
	p := proxyProvider("0.0.0.0/0", true, 99)
	mw := Middleware(p)
	var gotUID int64
	var gotRole string
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		gotUID = UserIDFromContext(r.Context())
		gotRole = UserRoleFromContext(r.Context())
	}))
	req, _ := http.NewRequest("GET", "/api/v1/auth/status", nil)
	req.RemoteAddr = "203.0.113.42:54321" // any source — 0.0.0.0/0 trusts everyone
	req.Header.Set("X-Forwarded-User", "alice")
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if !called {
		t.Fatal("handler must be invoked for /auth/status")
	}
	if gotUID != 99 {
		t.Errorf("uid = %d; want 99 — proxy identity must reach the handler context", gotUID)
	}
	if gotRole != "admin" {
		t.Errorf("role = %q; want \"admin\"", gotRole)
	}
}

// TestProxyAuthStatusPathUntrustedIPIgnoresHeader confirms the spoofing
// guard: when the request source is outside the trusted-proxy CIDR, the
// header is dropped even on an AllowUnauthPath. The handler still runs (the
// path is in the unauth list) but no identity is attached, so the status
// handler will return authenticated:false.
func TestProxyAuthStatusPathUntrustedIPIgnoresHeader(t *testing.T) {
	p := proxyProvider("127.0.0.1/32", true, 99)
	mw := Middleware(p)
	var gotUID int64
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		gotUID = UserIDFromContext(r.Context())
	}))
	req, _ := http.NewRequest("GET", "/api/v1/auth/status", nil)
	req.RemoteAddr = "1.2.3.4:54321" // outside trusted CIDR
	req.Header.Set("X-Forwarded-User", "alice")
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if !called {
		t.Fatal("/auth/status is in AllowUnauthPath — handler must still run")
	}
	if gotUID != 0 {
		t.Errorf("uid = %d; want 0 — spoofed header from untrusted IP must not attach identity", gotUID)
	}
}

// TestProxyAuthStatusPathModeMismatchIgnoresHeader confirms that proxy
// identity resolution is gated by Mode == ModeProxy. In any other mode the
// proxy header is ignored even from a trusted CIDR.
func TestProxyAuthStatusPathModeMismatchIgnoresHeader(t *testing.T) {
	// Build a provider with everything proxy-ish set, but mode = enabled.
	p := proxyProvider("0.0.0.0/0", true, 99)
	p.mode = ModeEnabled
	mw := Middleware(p)
	var gotUID int64
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		gotUID = UserIDFromContext(r.Context())
	}))
	req, _ := http.NewRequest("GET", "/api/v1/auth/status", nil)
	req.RemoteAddr = "10.0.0.5:54321"
	req.Header.Set("X-Forwarded-User", "alice")
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if !called {
		t.Fatal("/auth/status must be reachable")
	}
	if gotUID != 0 {
		t.Errorf("uid = %d; want 0 — proxy header must be ignored when mode != proxy", gotUID)
	}
}

// TestProxyAuthRegularPathTrustedIPStillAuthenticates is a belt-and-braces
// check that the refactor did not break the existing proxy-mode flow for
// non-AllowUnauthPath routes: a trusted IP with a valid header on /api/v1/author
// must still reach the handler with identity attached.
func TestProxyAuthRegularPathTrustedIPStillAuthenticates(t *testing.T) {
	p := proxyProvider("10.0.0.0/8", true, 42)
	mw := Middleware(p)
	var gotUID int64
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		gotUID = UserIDFromContext(r.Context())
	}))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	req.RemoteAddr = "10.0.0.5:12345"
	req.Header.Set("X-Forwarded-User", "alice")
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if !called {
		t.Fatal("trusted-IP proxy auth must still pass for non-unauth paths")
	}
	if gotUID != 42 {
		t.Errorf("uid = %d; want 42", gotUID)
	}
}

// TestProxyAuthRegularPathUntrustedIPStill401s mirrors the existing
// TestProxyAuthUntrustedIPWithHeader test through the new code path to
// confirm the auth gate for non-AllowUnauthPath routes is unchanged.
func TestProxyAuthRegularPathUntrustedIPStill401s(t *testing.T) {
	p := proxyProvider("10.0.0.0/8", true, 42)
	mw := Middleware(p)
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	req, _ := http.NewRequest("GET", "/api/v1/author", nil)
	req.RemoteAddr = "8.8.8.8:12345"
	req.Header.Set("X-Forwarded-User", "alice")
	w := &captureWriter{}
	h.ServeHTTP(w, req)
	if called {
		t.Fatal("untrusted source must still be rejected on protected paths after the #560 refactor")
	}
	if w.status != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", w.status)
	}
}

// TestOIDCProvidersGETIsPublicPUTIsNot confirms that AllowUnauthPath is
// method-aware for /auth/oidc/providers. GET must pass unauthenticated (login
// page needs to discover providers), but PUT must go through normal auth so a
// valid X-Api-Key header is required and grants admin access.
func TestOIDCProvidersGETIsPublicPUTIsNot(t *testing.T) {
	const apiKey = "test-api-key-for-oidc-test"
	p := &fakeProvider{mode: ModeEnabled, apiKey: apiKey}
	mw := Middleware(p)

	// GET must be let through without any credentials.
	t.Run("GET_public", func(t *testing.T) {
		called := false
		h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
		req, _ := http.NewRequest("GET", "/api/v1/auth/oidc/providers", nil)
		h.ServeHTTP(nopWriter{}, req)
		if !called {
			t.Fatal("GET /auth/oidc/providers must be public (login page needs provider list)")
		}
	})

	// PUT without credentials must be rejected as 401, not silently let through
	// with no role (which would cause RequireAdmin to return a confusing 403).
	t.Run("PUT_requires_auth", func(t *testing.T) {
		called := false
		h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
		req, _ := http.NewRequest("PUT", "/api/v1/auth/oidc/providers", nil)
		w := &captureWriter{}
		h.ServeHTTP(w, req)
		if called {
			t.Fatal("PUT /auth/oidc/providers without credentials must not reach handler")
		}
		if w.status != http.StatusUnauthorized {
			t.Errorf("status = %d; want 401", w.status)
		}
	})

	// PUT with a valid API key must pass and set admin role.
	t.Run("PUT_valid_api_key_is_admin", func(t *testing.T) {
		var gotRole string
		called := false
		h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			called = true
			gotRole = UserRoleFromContext(r.Context())
		}))
		req, _ := http.NewRequest("PUT", "/api/v1/auth/oidc/providers", nil)
		req.Header.Set("X-Api-Key", apiKey)
		h.ServeHTTP(nopWriter{}, req)
		if !called {
			t.Fatal("PUT with valid API key must reach handler")
		}
		if gotRole != "admin" {
			t.Errorf("role = %q; want \"admin\"", gotRole)
		}
	})
}

// Regression tests for CheckOwnership, the helper that closes Tier-1 per-user
// IDOR (D1). The contract:
//
//   - Gate off (BINDERY_ENFORCE_TENANCY unset/false): always returns true so
//     existing single-user installs and tests see no behavior change.
//   - Gate on + admin role: passes regardless of owner.
//   - Gate on + owner match: passes.
//   - Gate on + owner mismatch: blocked.
//   - Gate on + ownerUserID == 0 (legacy / pre-migration-025 row): passes,
//     to avoid orphaning data when an operator first turns the flag on.
//
// Each test uses SetEnforceTenancyForTests with t.Cleanup so order does not
// matter and the package-level cache is restored after the test exits.

func TestCheckOwnership_GateOffAllowsAll(t *testing.T) {
	SetEnforceTenancyForTests(t, false)

	ctx := context.WithValue(context.Background(), userIDCtxKey, int64(7))
	// Caller is user 7 but the resource is owned by user 99; with the gate
	// off this must still pass so single-user installs keep working.
	if !CheckOwnership(ctx, 99) {
		t.Error("CheckOwnership must pass when BINDERY_ENFORCE_TENANCY is off, regardless of owner")
	}
}

func TestCheckOwnership_AdminAlwaysPasses(t *testing.T) {
	SetEnforceTenancyForTests(t, true)

	ctx := context.WithValue(context.Background(), userIDCtxKey, int64(7))
	ctx = WithUserRole(ctx, "admin")
	if !CheckOwnership(ctx, 99) {
		t.Error("admin role must override owner mismatch even when the gate is on")
	}
}

func TestCheckOwnership_OwnerMatchesPasses(t *testing.T) {
	SetEnforceTenancyForTests(t, true)

	ctx := context.WithValue(context.Background(), userIDCtxKey, int64(7))
	ctx = WithUserRole(ctx, "user")
	if !CheckOwnership(ctx, 7) {
		t.Error("CheckOwnership must pass when caller owns the resource")
	}
}

func TestCheckOwnership_OwnerMismatchBlocked(t *testing.T) {
	SetEnforceTenancyForTests(t, true)

	ctx := context.WithValue(context.Background(), userIDCtxKey, int64(7))
	ctx = WithUserRole(ctx, "user")
	if CheckOwnership(ctx, 99) {
		t.Error("CheckOwnership must block when caller is not owner and not admin")
	}
}

func TestCheckOwnership_OwnerZeroPassesAsLegacyRow(t *testing.T) {
	SetEnforceTenancyForTests(t, true)

	ctx := context.WithValue(context.Background(), userIDCtxKey, int64(7))
	ctx = WithUserRole(ctx, "user")
	// owner_user_id IS NULL scans as 0 in Go; those rows pre-date migration
	// 025's backfill and must remain accessible after the flag is flipped on
	// so the operator does not orphan their library.
	if !CheckOwnership(ctx, 0) {
		t.Error("CheckOwnership must pass when ownerUserID is 0 (legacy row)")
	}
}

func TestCheckOwnership_UnauthenticatedPassesAsAdminEquivalent(t *testing.T) {
	SetEnforceTenancyForTests(t, true)

	// No user id, no role: API-key, disabled-auth, and local-only requests
	// reach handlers without an identity in ctx. Treating them as
	// admin-equivalent keeps machine-to-machine integrations (the *arr-style
	// callers, Harpoon, scripted exports) working post-gate. The IDOR
	// surface is closed by RequireAuth / RequireAdmin upstream; CheckOwnership
	// is the per-row check, not the auth check.
	ctx := context.Background()
	if !CheckOwnership(ctx, 99) {
		t.Error("unauthenticated context should pass ownership (admin-equivalent for API-key/disabled-auth integrations)")
	}
}

func TestSetEnforceTenancyForTests_RestoresPreviousValue(t *testing.T) {
	// Capture the value the package currently sees.
	pre := EnforceTenancy()

	// Sub-test flips the gate; its t.Cleanup must restore pre.
	t.Run("flipped", func(t *testing.T) {
		SetEnforceTenancyForTests(t, !pre)
		if EnforceTenancy() == pre {
			t.Fatalf("flip did not stick; got %v, want %v", EnforceTenancy(), !pre)
		}
	})
	if EnforceTenancy() != pre {
		t.Errorf("cleanup did not restore; got %v, want %v", EnforceTenancy(), pre)
	}
}
