package oidc

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
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// tokenIDP is a full fake OIDC provider: discovery, JWKS, token endpoint and
// userinfo. ID tokens are RS256 JWTs signed by hand so the test needs no
// extra module dependency.
type tokenIDP struct {
	*httptest.Server
	key *rsa.PrivateKey

	mu sync.Mutex
	// idClaims is the claim set minted into the next ID token. "iss", "aud",
	// "exp" and "iat" are filled in when absent.
	idClaims map[string]any
	// signWith overrides the signing key (to forge a bad signature).
	signWith *rsa.PrivateKey
	// tokenStatus, when non-zero, makes /token fail with that status.
	tokenStatus int
	// omitIDToken drops id_token from the token response.
	omitIDToken bool
	// userinfo is served from /userinfo; userinfoStatus non-zero fails it.
	userinfo       map[string]any
	userinfoStatus int
	// noUserinfo drops userinfo_endpoint from the discovery document.
	noUserinfo bool
	// lastTokenForm records the most recent /token request body.
	lastTokenForm url.Values
	// lastUserinfoAuth records the Authorization header on /userinfo.
	lastUserinfoAuth string
}

// Key generation dominates runtime, so the two keys the tests need are made
// once per package run.
var (
	testKeysOnce          sync.Once
	testIDPKey, testOther *rsa.PrivateKey
	testKeysErr           error
)

func testKeys(t *testing.T) (idp, other *rsa.PrivateKey) {
	t.Helper()
	testKeysOnce.Do(func() {
		if testIDPKey, testKeysErr = rsa.GenerateKey(rand.Reader, 2048); testKeysErr != nil {
			return
		}
		testOther, testKeysErr = rsa.GenerateKey(rand.Reader, 2048)
	})
	if testKeysErr != nil {
		t.Fatal(testKeysErr)
	}
	return testIDPKey, testOther
}

func newTokenIDP(t *testing.T) *tokenIDP {
	t.Helper()
	key, _ := testKeys(t)
	f := &tokenIDP{key: key}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *tokenIDP) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		doc := map[string]any{
			"issuer":                                f.URL,
			"authorization_endpoint":                f.URL + "/authorize",
			"token_endpoint":                        f.URL + "/token",
			"jwks_uri":                              f.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		}
		if !f.noUserinfo {
			doc["userinfo_endpoint"] = f.URL + "/userinfo"
		}
		writeJSON(w, doc)
	case "/jwks":
		pub := f.key.PublicKey
		writeJSON(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA",
			"kid": "k1",
			"alg": "RS256",
			"use": "sig",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	case "/token":
		_ = r.ParseForm()
		f.lastTokenForm = r.PostForm
		if f.tokenStatus != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.tokenStatus)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		resp := map[string]any{
			"access_token": "access-123",
			"token_type":   "Bearer",
			"expires_in":   3600,
		}
		if !f.omitIDToken {
			resp["id_token"] = f.mintIDToken()
		}
		writeJSON(w, resp)
	case "/userinfo":
		f.lastUserinfoAuth = r.Header.Get("Authorization")
		if f.userinfoStatus != 0 {
			http.Error(w, "nope", f.userinfoStatus)
			return
		}
		writeJSON(w, f.userinfo)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *tokenIDP) mintIDToken() string {
	claims := map[string]any{}
	for k, v := range f.idClaims {
		claims[k] = v
	}
	now := time.Now()
	if _, ok := claims["iss"]; !ok {
		claims["iss"] = f.URL
	}
	if _, ok := claims["aud"]; !ok {
		claims["aud"] = "client-1"
	}
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = now.Add(time.Hour).Unix()
	}
	if _, ok := claims["iat"]; !ok {
		claims["iat"] = now.Unix()
	}
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	body, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(body)
	key := f.key
	if f.signWith != nil {
		key = f.signWith
	}
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *tokenIDP) set(fn func(f *tokenIDP)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func loadedManager(t *testing.T, f *tokenIDP) *Manager {
	t.Helper()
	m := NewManager()
	m.Reload(context.Background(), []ProviderConfig{{
		ID: "idp", Name: "IdP", Issuer: f.URL, ClientID: "client-1", ClientSecret: "s3cret",
	}})
	if st := m.Status("idp"); st == nil || st.State != "ok" {
		t.Fatalf("provider did not load: %+v", st)
	}
	return m
}

func TestExchange_MergesUserinfoUnderIDToken(t *testing.T) {
	f := newTokenIDP(t)
	f.set(func(f *tokenIDP) {
		f.idClaims = map[string]any{
			"sub":            "user-1",
			"nonce":          "n-1",
			"email":          "id@example.com",
			"email_verified": "true", // string-shaped, as some IdPs send it
			"name":           "ID Name",
		}
		f.userinfo = map[string]any{
			"sub":                "user-1",
			"name":               "Userinfo Name", // must NOT override the ID token
			"preferred_username": "alice",
			"groups":             []string{"admins", "readers"},
		}
	})
	m := loadedManager(t, f)

	c, err := m.Exchange(context.Background(), "https://bindery.example", "idp", "code-xyz", "n-1", "verifier-1")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if c.Sub != "user-1" || c.Issuer != f.URL {
		t.Errorf("sub/issuer = %q/%q", c.Sub, c.Issuer)
	}
	if c.Email != "id@example.com" || !c.EmailVerified {
		t.Errorf("email = %q verified=%v", c.Email, c.EmailVerified)
	}
	if c.Name != "ID Name" {
		t.Errorf("name = %q, want the ID token's value to win over userinfo", c.Name)
	}
	if c.PreferredUsername != "alice" {
		t.Errorf("preferred_username = %q, want gap filled from userinfo", c.PreferredUsername)
	}
	if strings.Join(c.Groups, ",") != "admins,readers" {
		t.Errorf("groups = %v, want userinfo groups", c.Groups)
	}
	if !GroupClaimPresent(c.Raw, "groups") {
		t.Error("groups claim should be present in the merged raw set")
	}

	f.mu.Lock()
	form, auth := f.lastTokenForm, f.lastUserinfoAuth
	f.mu.Unlock()
	if form.Get("code") != "code-xyz" || form.Get("code_verifier") != "verifier-1" {
		t.Errorf("token form = %v, want code and PKCE verifier", form)
	}
	if got := form.Get("redirect_uri"); got != "https://bindery.example"+CallbackPath("idp") {
		t.Errorf("redirect_uri = %q", got)
	}
	if auth != "Bearer access-123" {
		t.Errorf("userinfo Authorization = %q", auth)
	}
}

func TestExchange_FillsEmailFromUserinfo(t *testing.T) {
	f := newTokenIDP(t)
	f.set(func(f *tokenIDP) {
		f.idClaims = map[string]any{"sub": "user-1", "nonce": "n"}
		f.userinfo = map[string]any{"sub": "user-1", "email": "ui@example.com", "name": "UI"}
	})
	m := loadedManager(t, f)
	c, err := m.Exchange(context.Background(), "https://b", "idp", "c", "n", "v")
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "ui@example.com" || c.Name != "UI" {
		t.Errorf("email/name = %q/%q, want filled from userinfo", c.Email, c.Name)
	}
	// email_verified was not asserted anywhere: fail closed.
	if c.EmailVerified {
		t.Error("EmailVerified must be false when the claim is absent")
	}
	if GroupClaimPresent(c.Raw, "groups") {
		t.Error("no groups claim was sent by either source")
	}
}

func TestExchange_UserinfoSubjectMismatchIgnored(t *testing.T) {
	f := newTokenIDP(t)
	f.set(func(f *tokenIDP) {
		f.idClaims = map[string]any{"sub": "user-1", "nonce": "n"}
		f.userinfo = map[string]any{"sub": "someone-else", "groups": []string{"admins"}, "preferred_username": "mallory"}
	})
	m := loadedManager(t, f)
	c, err := m.Exchange(context.Background(), "https://b", "idp", "c", "n", "v")
	if err != nil {
		t.Fatal(err)
	}
	if c.Groups != nil || c.PreferredUsername != "" {
		t.Errorf("groups=%v username=%q: another subject's userinfo must not be merged", c.Groups, c.PreferredUsername)
	}
}

func TestExchange_UserinfoFailureIsNonFatal(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(f *tokenIDP)
	}{
		{"userinfo 500", func(f *tokenIDP) { f.userinfoStatus = http.StatusInternalServerError }},
		{"no userinfo endpoint", func(f *tokenIDP) { f.noUserinfo = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTokenIDP(t)
			f.set(func(f *tokenIDP) {
				f.idClaims = map[string]any{"sub": "u", "nonce": "n", "groups": "a, b c"}
				tc.set(f)
			})
			m := loadedManager(t, f)
			c, err := m.Exchange(context.Background(), "https://b", "idp", "c", "n", "v")
			if err != nil {
				t.Fatalf("userinfo failure must not fail login: %v", err)
			}
			if strings.Join(c.Groups, ",") != "a,b,c" {
				t.Errorf("groups = %v, want the ID token's delimited string split", c.Groups)
			}
		})
	}
}

func TestExchange_Errors(t *testing.T) {
	_, other := testKeys(t)
	for _, tc := range []struct {
		name    string
		set     func(f *tokenIDP)
		nonce   string
		wantErr string
	}{
		{"token endpoint rejects code", func(f *tokenIDP) { f.tokenStatus = http.StatusBadRequest }, "n", "oidc token exchange"},
		{"no id_token", func(f *tokenIDP) { f.omitIDToken = true }, "n", "no id_token"},
		{"bad signature", func(f *tokenIDP) { f.signWith = other }, "n", "verification failed"},
		{"wrong audience", func(f *tokenIDP) { f.idClaims["aud"] = "someone-else" }, "n", "verification failed"},
		{"expired", func(f *tokenIDP) { f.idClaims["exp"] = time.Now().Add(-time.Hour).Unix() }, "n", "verification failed"},
		{"wrong issuer", func(f *tokenIDP) { f.idClaims["iss"] = "https://evil.example" }, "n", "verification failed"},
		{"nonce mismatch", func(*tokenIDP) {}, "other-nonce", "nonce mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTokenIDP(t)
			f.set(func(f *tokenIDP) {
				f.idClaims = map[string]any{"sub": "u", "nonce": "n"}
				tc.set(f)
			})
			m := loadedManager(t, f)
			c, err := m.Exchange(context.Background(), "https://b", "idp", "c", tc.nonce, "v")
			if err == nil {
				t.Fatalf("want error containing %q, got claims %+v", tc.wantErr, c)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestExchange_UnknownProvider(t *testing.T) {
	m := NewManager()
	if _, err := m.Exchange(context.Background(), "https://b", "nope", "c", "n", "v"); err == nil ||
		!strings.Contains(err.Error(), "unknown oidc provider") {
		t.Fatalf("err = %v", err)
	}
}

func TestExchange_CancelledContext(t *testing.T) {
	f := newTokenIDP(t)
	f.set(func(f *tokenIDP) { f.idClaims = map[string]any{"sub": "u", "nonce": "n"} })
	m := loadedManager(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Exchange(ctx, "https://b", "idp", "c", "n", "v"); err == nil {
		t.Fatal("a cancelled context must abort the token exchange")
	}
}

func TestMergeUserinfo_NilInputsReturnClaimsUnchanged(t *testing.T) {
	in := map[string]any{"sub": "u"}
	tok := &oauth2.Token{AccessToken: "a"}
	if got := mergeUserinfo(context.Background(), nil, tok, "u", in); got["sub"] != "u" || len(got) != 1 {
		t.Errorf("nil entry: %v", got)
	}
	if got := mergeUserinfo(context.Background(), &entry{}, tok, "u", in); len(got) != 1 {
		t.Errorf("nil provider: %v", got)
	}
	if got := mergeUserinfo(context.Background(), &entry{}, nil, "u", nil); got != nil {
		t.Errorf("nil token with nil claims: %v", got)
	}
}

func TestMergeUserinfo_NilIDClaimsGetsUserinfo(t *testing.T) {
	f := newTokenIDP(t)
	f.set(func(f *tokenIDP) { f.userinfo = map[string]any{"sub": "u", "groups": []string{"g"}} })
	m := loadedManager(t, f)
	got := mergeUserinfo(context.Background(), m.Get("idp"), &oauth2.Token{AccessToken: "a", TokenType: "Bearer"}, "u", nil)
	if !GroupClaimPresent(got, "groups") || got["sub"] != "u" {
		t.Errorf("merged = %v, want userinfo claims in a fresh map", got)
	}
}

func TestGroupClaimPresent(t *testing.T) {
	cases := []struct {
		name  string
		raw   map[string]any
		claim string
		want  bool
	}{
		{"nil map", nil, "groups", false},
		{"empty claim name", map[string]any{"groups": []any{"a"}}, "", false},
		{"absent", map[string]any{"sub": "u"}, "groups", false},
		{"explicit null", map[string]any{"groups": nil}, "groups", false},
		{"present but empty list", map[string]any{"groups": []any{}}, "groups", true},
		{"present string", map[string]any{"roles": "admin"}, "roles", true},
	}
	for _, tc := range cases {
		if got := GroupClaimPresent(tc.raw, tc.claim); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProviderConfigPublic_StripsSecret(t *testing.T) {
	c := ProviderConfig{
		ID: "a", Name: "A", Issuer: "https://i", ClientID: "cid", ClientSecret: "TOPSECRET",
		Scopes: []string{"openid"}, AllowedGroups: []string{"g"},
	}
	p := c.Public()
	if p.ID != "a" || p.Name != "A" || p.Issuer != "https://i" || p.ClientID != "cid" ||
		len(p.Scopes) != 1 || len(p.AllowedGroups) != 1 {
		t.Errorf("public view lost fields: %+v", p)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "TOPSECRET") || strings.Contains(string(b), "client_secret") {
		t.Errorf("public JSON leaks the secret: %s", b)
	}
}

func TestNewVerifier_IsPKCECompliant(t *testing.T) {
	a, err := NewVerifier()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewVerifier()
	if a == b {
		t.Error("verifiers must be random")
	}
	// RFC 7636: 43 to 128 characters from the unreserved set.
	if len(a) < 43 || len(a) > 128 {
		t.Errorf("verifier length %d out of RFC 7636 range", len(a))
	}
	if _, err := base64.RawURLEncoding.DecodeString(a); err != nil {
		t.Errorf("verifier is not base64url: %v", err)
	}
}

func TestAuthURL_DefaultScopesAndPKCE(t *testing.T) {
	f := newTokenIDP(t)
	m := loadedManager(t, f)
	raw, err := m.AuthURL(context.Background(), "https://b", "idp", "st", "no", "ver")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("scope") != "openid profile email" {
		t.Errorf("scope = %q, want defaults", q.Get("scope"))
	}
	if q.Get("code_challenge") != pkceChallenge("ver") || q.Get("code_challenge_method") != "S256" {
		t.Errorf("PKCE params = %v", q)
	}
	if q.Get("state") != "st" || q.Get("nonce") != "no" || q.Get("client_id") != "client-1" {
		t.Errorf("query = %v", q)
	}
	if cfg, ok := m.ProviderConfig("idp"); !ok || cfg.ClientSecret != "s3cret" {
		t.Errorf("ProviderConfig = %+v %v", cfg, ok)
	}
	if l := m.List(); len(l) != 1 || l[0].ID != "idp" {
		t.Errorf("List = %+v", l)
	}
}
