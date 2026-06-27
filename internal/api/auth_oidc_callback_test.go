package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/auth/oidc"
	"github.com/vavallee/bindery/internal/db"
)

// fakeIDP is a minimal OIDC identity provider for tests: it serves a discovery
// document, a JWKS, and a token endpoint that returns an RS256-signed ID token
// whose claims the test controls. It lets the OIDC callback path be exercised
// end to end without a real IdP.
type fakeIDP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	kid    string
	// claims is the claim set baked into the ID token returned by /token.
	claims map[string]any
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	idp := &fakeIDP{key: key, kid: "test-key-1"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 base,
			"authorization_endpoint": base + "/authorize",
			"token_endpoint":         base + "/token",
			"jwks_uri":               base + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{idp.jwk()},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		idToken := idp.signIDToken(t, base)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"id_token":     idToken,
		})
	})

	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// jwk renders the public key in JWK form for the JWKS endpoint.
func (f *fakeIDP) jwk() map[string]any {
	pub := f.key.PublicKey
	return map[string]any{
		"kty": "RSA",
		"kid": f.kid,
		"alg": "RS256",
		"use": "sig",
		"n":   b64u(pub.N.Bytes()),
		"e":   b64u(big.NewInt(int64(pub.E)).Bytes()),
	}
}

// signIDToken builds and RS256-signs an ID token with f.claims, filling in the
// standard registered claims (iss/aud/exp/iat) so the go-oidc verifier accepts
// it. The "aud" claim is set to the test client ID.
func (f *fakeIDP) signIDToken(t *testing.T, issuer string) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": f.kid}
	claims := map[string]any{
		"iss": issuer,
		"aud": "test-client-id",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
		"iat": time.Now().Unix(),
	}
	for k, v := range f.claims {
		claims[k] = v
	}
	hb, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signingInput := b64u(hb) + "." + b64u(cb)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign id token: %v", err)
	}
	return signingInput + "." + b64u(sig)
}

// newCallbackTestHandler wires an OIDCHandler against a real in-memory DB and a
// loaded provider pointing at the given fake IdP. allowedGroups and emailLink
// configure the two policies under test.
func newCallbackTestHandler(t *testing.T, idp *fakeIDP, allowedGroups []string, emailLink bool) (*OIDCHandler, *db.UserRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()

	users := db.NewUserRepo(database)
	settings := db.NewSettingsRepo(database)
	// Seed a session secret so issueSession can sign a cookie.
	if err := settings.Set(ctx, SettingAuthSessionSecret, strings.Repeat("x", 32)); err != nil {
		t.Fatalf("seed session secret: %v", err)
	}

	authH := NewAuthHandler(users, settings, auth.NewLoginLimiter(10, time.Minute))

	mgr := oidc.NewManager()
	cfg := oidc.ProviderConfig{
		ID:            "test",
		Name:          "Test IdP",
		Issuer:        idp.server.URL,
		ClientID:      "test-client-id",
		ClientSecret:  "test-secret",
		AllowedGroups: allowedGroups,
	}
	mgr.Reload(ctx, []oidc.ProviderConfig{cfg})
	if mgr.Get("test") == nil {
		t.Fatalf("provider failed to load against fake IdP at %s", idp.server.URL)
	}

	h := NewOIDCHandler(mgr, users, settings, authH, func(_ *http.Request) string {
		return "https://bindery.example.com"
	}).WithOIDCEmailLink(emailLink)
	return h, users, ctx
}

// doCallback drives the Callback handler with a valid flow cookie + state/code.
func doCallback(t *testing.T, h *OIDCHandler) *httptest.ResponseRecorder {
	t.Helper()
	state := "test-state"
	nonce := "test-nonce"
	verifier := "test-verifier-aaaaaaaaaaaaaaaaaaaaaaaa"
	flowVal, err := oidc.EncodeFlowState(state, nonce, verifier, "https://bindery.example.com")
	if err != nil {
		t.Fatalf("encode flow state: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/auth/oidc/test/callback?state="+url.QueryEscape(state)+"&code=test-code", nil)
	req.AddCookie(&http.Cookie{Name: oidcFlowCookie, Value: flowVal})
	// Inject the chi URL param the handler reads via chi.URLParam.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "test")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.Callback(rec, req)
	return rec
}

// --- Finding 2: AllowedGroups enforcement -----------------------------------

// TestCallback_AllowedGroups_InGroup verifies a user whose `groups` claim
// intersects AllowedGroups is admitted (auto-provisioned, session issued).
func TestCallback_AllowedGroups_InGroup(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub":            "user-in-group",
		"nonce":          "test-nonce",
		"email":          "ingroup@example.com",
		"email_verified": true,
		"groups":         []string{"staff", "bindery-users"},
	}
	h, _, _ := newCallbackTestHandler(t, idp, []string{"bindery-users"}, false)

	rec := doCallback(t, h)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302 (in-group user should be admitted); body=%s", rec.Code, rec.Body.String())
	}
	if !hasSessionCookie(rec) {
		t.Fatal("expected a session cookie to be issued for the admitted user")
	}
}

// TestCallback_AllowedGroups_OutOfGroup verifies a user whose `groups` claim
// does not intersect AllowedGroups is rejected with 403.
func TestCallback_AllowedGroups_OutOfGroup(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub":            "user-out-of-group",
		"nonce":          "test-nonce",
		"email":          "outgroup@example.com",
		"email_verified": true,
		"groups":         []string{"staff", "contractors"},
	}
	h, _, _ := newCallbackTestHandler(t, idp, []string{"bindery-users"}, false)

	rec := doCallback(t, h)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 (out-of-group user must be rejected); body=%s", rec.Code, rec.Body.String())
	}
	if hasSessionCookie(rec) {
		t.Fatal("a rejected login must not receive a session cookie")
	}
}

// TestCallback_AllowedGroups_CustomGroupClaim verifies the AllowedGroups policy
// is evaluated against the operator-configured BINDERY_OIDC_GROUP_CLAIM, not the
// literal "groups" claim. Here the IdP sends groups only under "bindery_groups";
// before the fix this compared against an always-empty slice and locked the
// in-group user out with a 403.
func TestCallback_AllowedGroups_CustomGroupClaim(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub":            "user-custom-claim",
		"nonce":          "test-nonce",
		"email":          "custom@example.com",
		"email_verified": true,
		"bindery_groups": []string{"staff", "bindery-users"},
		// deliberately no default "groups" claim
	}
	h, _, _ := newCallbackTestHandler(t, idp, []string{"bindery-users"}, false)
	h.WithOIDCGroupClaim("bindery_groups")

	rec := doCallback(t, h)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302: AllowedGroups must honor BINDERY_OIDC_GROUP_CLAIM; body=%s", rec.Code, rec.Body.String())
	}
	if !hasSessionCookie(rec) {
		t.Fatal("expected a session cookie for the in-group user under the configured claim")
	}
}

// TestCallback_AllowedGroups_CustomGroupClaim_OutOfGroup verifies the negative
// side of the configured-claim check still fails closed.
func TestCallback_AllowedGroups_CustomGroupClaim_OutOfGroup(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub":            "user-custom-claim-out",
		"nonce":          "test-nonce",
		"email":          "customout@example.com",
		"email_verified": true,
		"bindery_groups": []string{"staff", "contractors"},
	}
	h, _, _ := newCallbackTestHandler(t, idp, []string{"bindery-users"}, false)
	h.WithOIDCGroupClaim("bindery_groups")

	rec := doCallback(t, h)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 (out-of-group under configured claim must be rejected); body=%s", rec.Code, rec.Body.String())
	}
	if hasSessionCookie(rec) {
		t.Fatal("a rejected login must not receive a session cookie")
	}
}

// TestCallback_AllowedGroups_NoGroupsClaim verifies that when AllowedGroups is
// configured but the IdP sends no `groups` claim at all, the login is rejected
// (fail-closed) — the admin must fix the IdP scope mapping.
func TestCallback_AllowedGroups_NoGroupsClaim(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub":            "user-no-groups",
		"nonce":          "test-nonce",
		"email":          "nogroups@example.com",
		"email_verified": true,
		// no "groups" claim
	}
	h, _, _ := newCallbackTestHandler(t, idp, []string{"bindery-users"}, false)

	rec := doCallback(t, h)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 (missing groups claim must fail closed); body=%s", rec.Code, rec.Body.String())
	}
}

// TestCallback_AllowedGroups_Empty verifies that with no AllowedGroups
// configured, group membership is not checked and any user is admitted.
func TestCallback_AllowedGroups_Empty(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub":            "user-any",
		"nonce":          "test-nonce",
		"email":          "any@example.com",
		"email_verified": true,
		"groups":         []string{"whatever"},
	}
	h, _, _ := newCallbackTestHandler(t, idp, nil, false)

	rec := doCallback(t, h)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302 (empty AllowedGroups means allow all); body=%s", rec.Code, rec.Body.String())
	}
}

// --- Finding 1: verified-email requirement for OIDC linking -----------------

// TestCallback_EmailLink_VerifiedEmail verifies that with oidcEmailLink enabled
// and a verified email, an unknown OIDC subject is linked to the pre-existing
// Bindery account that owns that email.
func TestCallback_EmailLink_VerifiedEmail(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub":            "attacker-or-legit-sub",
		"nonce":          "test-nonce",
		"email":          "victim@example.com",
		"email_verified": true,
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, true)

	// Pre-create the account that owns victim@example.com via a *different*
	// OIDC issuer/sub so GetByOIDC for the callback's (issuer,sub) returns nil.
	existing, err := users.GetOrCreateByOIDC(ctx, "https://other-issuer.example", "other-sub",
		"victim", "victim@example.com", "Victim", "user")
	if err != nil {
		t.Fatalf("seed existing user: %v", err)
	}

	rec := doCallback(t, h)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302 (verified email should link); body=%s", rec.Code, rec.Body.String())
	}

	// The callback's (issuer, sub) must now resolve to the existing account.
	linked, err := users.GetByOIDC(ctx, idp.server.URL, "attacker-or-legit-sub")
	if err != nil {
		t.Fatalf("GetByOIDC after link: %v", err)
	}
	if linked == nil || linked.ID != existing.ID {
		t.Fatalf("expected the OIDC subject to be linked to user %d, got %+v", existing.ID, linked)
	}
}

// TestCallback_EmailLink_UnverifiedEmail is the security regression test for
// finding 1: an unverified `email` claim must NOT link to an existing account.
// Instead the login falls through to subject-based provisioning, creating a
// brand-new account distinct from the victim's.
func TestCallback_EmailLink_UnverifiedEmail(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub":            "attacker-sub",
		"nonce":          "test-nonce",
		"email":          "victim@example.com",
		"email_verified": false, // attacker-controlled, unverified
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, true)

	victim, err := users.GetOrCreateByOIDC(ctx, "https://other-issuer.example", "victim-sub",
		"victim", "victim@example.com", "Victim", "user")
	if err != nil {
		t.Fatalf("seed victim: %v", err)
	}

	rec := doCallback(t, h)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302 (login falls through to provisioning); body=%s", rec.Code, rec.Body.String())
	}

	// The attacker's (issuer, sub) must resolve to a NEW account, not the victim.
	got, err := users.GetByOIDC(ctx, idp.server.URL, "attacker-sub")
	if err != nil {
		t.Fatalf("GetByOIDC after callback: %v", err)
	}
	if got == nil {
		t.Fatal("expected a new account to be provisioned for the attacker's subject")
		return
	}
	if got.ID == victim.ID {
		t.Fatal("SECURITY: unverified email claim was linked to the victim's account — account takeover")
	}
}

// TestCallback_EmailLink_MissingEmailVerifiedClaim verifies that an IdP which
// omits `email_verified` entirely is treated as unverified (fail-closed): no
// linking, fall through to provisioning a separate account.
func TestCallback_EmailLink_MissingEmailVerifiedClaim(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub":   "no-emailverified-sub",
		"nonce": "test-nonce",
		"email": "victim@example.com",
		// no "email_verified" claim at all
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, true)

	victim, err := users.GetOrCreateByOIDC(ctx, "https://other-issuer.example", "victim-sub-2",
		"victim2", "victim@example.com", "Victim", "user")
	if err != nil {
		t.Fatalf("seed victim: %v", err)
	}

	rec := doCallback(t, h)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	got, err := users.GetByOIDC(ctx, idp.server.URL, "no-emailverified-sub")
	if err != nil {
		t.Fatalf("GetByOIDC: %v", err)
	}
	if got == nil || got.ID == victim.ID {
		t.Fatal("absent email_verified claim must not link to the victim's account")
	}
}

func hasSessionCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			return true
		}
	}
	return false
}
