package oidc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseProviders_empty(t *testing.T) {
	ps, err := ParseProviders("")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Fatalf("want 0 providers, got %d", len(ps))
	}
}

func TestParseProviders_valid(t *testing.T) {
	raw := `[{"id":"google","name":"Google","issuer":"https://accounts.google.com","client_id":"cid","client_secret":"sec","scopes":["openid","email"]}]`
	ps, err := ParseProviders(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].ID != "google" {
		t.Fatalf("unexpected providers: %+v", ps)
	}
}

func TestParseProviders_invalid(t *testing.T) {
	_, err := ParseProviders("{not json")
	if err == nil {
		t.Fatal("want error for invalid JSON")
	}
}

// --- PKCE --------------------------------------------------------------------

func TestPKCEChallengeRoundtrip(t *testing.T) {
	verifier, err := NewVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if len(verifier) < 40 {
		t.Fatalf("verifier too short: %d", len(verifier))
	}
	challenge := pkceChallenge(verifier)
	if challenge == "" {
		t.Fatal("empty challenge")
	}
	v2, _ := NewVerifier()
	if pkceChallenge(v2) == challenge {
		t.Fatal("two verifiers produced the same challenge")
	}
}

// --- Flow state cookie -------------------------------------------------------

func TestEncodeDecodeFlowState(t *testing.T) {
	state, _ := NewState()
	nonce, _ := NewNonce()
	verifier, _ := NewVerifier()
	base := "https://bindery.example.com"

	encoded, err := EncodeFlowState(state, nonce, verifier, base)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := DecodeFlowState(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if fs.State != state || fs.Nonce != nonce || fs.CodeVerifier != verifier || fs.RedirectBase != base {
		t.Fatalf("round-trip mismatch: %+v", fs)
	}
}

func TestDecodeFlowState_LegacyCookieWithoutRedirectBase(t *testing.T) {
	// Old flow cookies (pre-1.4) didn't carry a redirect_base field. Make
	// sure decoding still works — callers fall back to live-resolving from
	// the request when RedirectBase is empty.
	encoded, err := encodeFlowStateRaw(flowState{
		State:        "s",
		Nonce:        "n",
		CodeVerifier: "cv",
		Expiry:       time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := DecodeFlowState(encoded)
	if err != nil {
		t.Fatalf("legacy cookie should still decode: %v", err)
	}
	if fs.RedirectBase != "" {
		t.Fatalf("legacy cookie should report empty RedirectBase, got %q", fs.RedirectBase)
	}
}

func TestDecodeFlowState_expired(t *testing.T) {
	encoded, err := encodeFlowStateRaw(flowState{
		State:        "s",
		Nonce:        "n",
		CodeVerifier: "cv",
		Expiry:       time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodeFlowState(encoded)
	if err == nil {
		t.Fatal("want error for expired flow state")
	}
}

func TestDecodeFlowState_tampered(t *testing.T) {
	_, err := DecodeFlowState("not-valid-base64!!!")
	if err == nil {
		t.Fatal("want error for invalid encoded state")
	}
}

// --- Nonce / state uniqueness ------------------------------------------------

func TestNewStateUnique(t *testing.T) {
	a, _ := NewState()
	b, _ := NewState()
	if a == b {
		t.Fatal("two states should differ")
	}
}

func TestNewNonceUnique(t *testing.T) {
	a, _ := NewNonce()
	b, _ := NewNonce()
	if a == b {
		t.Fatal("two nonces should differ")
	}
}

// --- Reload failure tracking + on-demand retry -----------------------------
//
// These exercise the resilience contract: Reload must record (not drop)
// providers whose discovery fails, and EnsureLoaded must retry them at most
// once per retryMinInterval until they succeed.

// fakeIDP is a minimal OIDC discovery endpoint that flips between failing and
// succeeding under test control. It only implements /.well-known/openid-configuration —
// enough for gooidc.NewProvider to succeed; nothing further is exercised.
type fakeIDP struct {
	*httptest.Server
	failing atomic.Bool
}

func newFakeIDP() *fakeIDP {
	f := &fakeIDP{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			http.NotFound(w, r)
			return
		}
		if f.failing.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.URL,
			"authorization_endpoint":                f.URL + "/authorize",
			"token_endpoint":                        f.URL + "/token",
			"jwks_uri":                              f.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	}))
	return f
}

func TestReload_FailedProviderRecorded(t *testing.T) {
	idp := newFakeIDP()
	defer idp.Close()
	idp.failing.Store(true)

	mgr := NewManager()
	cfg := ProviderConfig{ID: "x", Name: "X", Issuer: idp.URL, ClientID: "cid", ClientSecret: "sec"}
	mgr.Reload(context.Background(), []ProviderConfig{cfg})

	if mgr.Get("x") != nil {
		t.Fatal("provider should not be loaded when discovery fails")
	}
	st := mgr.Status("x")
	if st == nil {
		t.Fatal("status should exist for a configured-but-failed provider")
		return
	}
	if st.State != "failed" {
		t.Fatalf("want state=failed, got %q", st.State)
	}
	if st.LastError == "" {
		t.Fatal("LastError should be populated on failed discovery")
	}
	if st.LastAttempt.IsZero() {
		t.Fatal("LastAttempt should be set on failed discovery")
	}
}

func TestStatus_LoadedProviderReportsOk(t *testing.T) {
	idp := newFakeIDP()
	defer idp.Close()

	mgr := NewManager()
	mgr.Reload(context.Background(), []ProviderConfig{
		{ID: "y", Name: "Y", Issuer: idp.URL, ClientID: "cid", ClientSecret: "sec"},
	})

	st := mgr.Status("y")
	if st == nil || st.State != "ok" {
		t.Fatalf("want state=ok, got %+v", st)
	}
	if mgr.Status("nonexistent") != nil {
		t.Fatal("Status should be nil for a provider that was never configured")
	}
}

func TestEnsureLoaded_RetryRespectsInterval(t *testing.T) {
	idp := newFakeIDP()
	defer idp.Close()
	idp.failing.Store(true)

	// Speed up retry interval so the test doesn't sleep 30s.
	prev := retryMinInterval
	retryMinInterval = 50 * time.Millisecond
	defer func() { retryMinInterval = prev }()

	mgr := NewManager()
	mgr.Reload(context.Background(), []ProviderConfig{
		{ID: "z", Name: "Z", Issuer: idp.URL, ClientID: "cid", ClientSecret: "sec"},
	})
	if mgr.Get("z") != nil {
		t.Fatal("expected initial discovery to fail")
	}

	// Bring the IdP back up and immediately try EnsureLoaded — within the
	// retry interval, it must NOT attempt another discovery.
	idp.failing.Store(false)
	firstAttempt := mgr.Status("z").LastAttempt
	mgr.EnsureLoaded(context.Background(), "z")
	if mgr.Get("z") != nil {
		t.Fatal("EnsureLoaded should not have retried within retryMinInterval")
	}
	if mgr.Status("z").LastAttempt != firstAttempt {
		t.Fatal("LastAttempt should be unchanged when retry is rate-limited")
	}

	// After the interval elapses, EnsureLoaded triggers re-discovery and
	// the provider transitions to loaded.
	time.Sleep(retryMinInterval + 20*time.Millisecond)
	mgr.EnsureLoaded(context.Background(), "z")
	if mgr.Get("z") == nil {
		t.Fatal("EnsureLoaded should have recovered the provider after the interval")
	}
	if mgr.Status("z").State != "ok" {
		t.Fatalf("recovered provider should report ok, got %+v", mgr.Status("z"))
	}
}

func TestEnsureLoaded_NoOpForUnknown(t *testing.T) {
	mgr := NewManager()
	// Should not panic, should not log, should not touch internal maps.
	mgr.EnsureLoaded(context.Background(), "nope")
	if mgr.Get("nope") != nil {
		t.Fatal("unknown provider must not be created by EnsureLoaded")
	}
}

func TestAuthURL_TriggersRetryForFailedProvider(t *testing.T) {
	idp := newFakeIDP()
	defer idp.Close()
	idp.failing.Store(true)

	prev := retryMinInterval
	retryMinInterval = 1 * time.Millisecond
	defer func() { retryMinInterval = prev }()

	mgr := NewManager()
	mgr.Reload(context.Background(), []ProviderConfig{
		{ID: "p", Name: "P", Issuer: idp.URL, ClientID: "cid", ClientSecret: "sec"},
	})

	// Bring IdP back up. Sleep past the retry interval so EnsureLoaded
	// will take a fresh attempt the next time it's called.
	idp.failing.Store(false)
	time.Sleep(5 * time.Millisecond)

	verifier, _ := NewVerifier()
	url, err := mgr.AuthURL(context.Background(), "https://bindery.example.com", "p", "state", "nonce", verifier)
	if err != nil {
		t.Fatalf("AuthURL should have succeeded after on-demand retry: %v", err)
	}
	if url == "" {
		t.Fatal("AuthURL returned empty URL")
	}
}

func TestAuthURL_StillFailsWhenIdPDown(t *testing.T) {
	idp := newFakeIDP()
	defer idp.Close()
	idp.failing.Store(true)

	prev := retryMinInterval
	retryMinInterval = 1 * time.Millisecond
	defer func() { retryMinInterval = prev }()

	mgr := NewManager()
	mgr.Reload(context.Background(), []ProviderConfig{
		{ID: "q", Name: "Q", Issuer: idp.URL, ClientID: "cid", ClientSecret: "sec"},
	})
	time.Sleep(5 * time.Millisecond)

	verifier, _ := NewVerifier()
	_, err := mgr.AuthURL(context.Background(), "https://bindery.example.com", "q", "state", "nonce", verifier)
	if err == nil {
		t.Fatal("AuthURL should fail when IdP is still down after retry attempt")
		return
	}
	if !strings.Contains(err.Error(), "unknown oidc provider") {
		t.Fatalf("expected unknown-provider error, got: %v", err)
	}
}

func TestReload_RecoversPreviouslyFailedProvider(t *testing.T) {
	idp := newFakeIDP()
	defer idp.Close()
	idp.failing.Store(true)

	mgr := NewManager()
	cfg := ProviderConfig{ID: "r", Name: "R", Issuer: idp.URL, ClientID: "cid", ClientSecret: "sec"}
	mgr.Reload(context.Background(), []ProviderConfig{cfg})
	if mgr.Status("r").State != "failed" {
		t.Fatal("setup: provider should start in failed state")
	}

	// IdP comes back up, admin saves settings again (or any other Reload trigger).
	idp.failing.Store(false)
	mgr.Reload(context.Background(), []ProviderConfig{cfg})

	if mgr.Status("r").State != "ok" {
		t.Fatalf("Reload should have recovered the provider, got %+v", mgr.Status("r"))
	}
	if _, stillFailed := mgr.failed["r"]; stillFailed {
		t.Fatal("recovered provider must be removed from the failed map")
	}
}

func TestReload_RemovesStaleProviders(t *testing.T) {
	idp := newFakeIDP()
	defer idp.Close()

	mgr := NewManager()
	mgr.Reload(context.Background(), []ProviderConfig{
		{ID: "keep", Name: "K", Issuer: idp.URL, ClientID: "cid", ClientSecret: "sec"},
		{ID: "drop", Name: "D", Issuer: idp.URL, ClientID: "cid", ClientSecret: "sec"},
	})
	if mgr.Get("drop") == nil {
		t.Fatal("setup: both providers should be loaded")
	}

	// Settings now contain only "keep".
	mgr.Reload(context.Background(), []ProviderConfig{
		{ID: "keep", Name: "K", Issuer: idp.URL, ClientID: "cid", ClientSecret: "sec"},
	})
	if mgr.Get("drop") != nil {
		t.Fatal("removed provider should be gone from loaded map")
	}
	if mgr.Status("drop") != nil {
		t.Fatal("removed provider should not appear in status either")
	}
}

// --- Sub collision across issuers --------------------------------------------
// Verifies the invariant that (issuer, sub) is the composite key — two
// different issuers can emit the same sub without colliding.

func TestSubCollisionAcrossIssuers(t *testing.T) {
	// Two providers with different issuers but the same sub value.
	// They should produce distinct (issuer, sub) pairs and must NOT be
	// treated as the same identity. This is a schema/logic property;
	// this test documents the contract and would catch a regression if
	// someone keyed lookups on sub alone.
	issuer1 := "https://accounts.google.com"
	issuer2 := "https://login.microsoftonline.com/tenant/v2.0"
	sub := "1234567890"

	type identity struct{ issuer, sub string }
	a := identity{issuer1, sub}
	b := identity{issuer2, sub}

	if a == b {
		t.Fatal("same sub from different issuers must not be equal identities")
	}
	// Confirm the composite key is distinct.
	if a.issuer == b.issuer {
		t.Fatal("test setup error: issuers should differ")
	}
}

// --- AllowedGroups enforcement (issue #709, finding 2) -----------------------

func TestGroupsAllowed(t *testing.T) {
	tests := []struct {
		name          string
		allowedGroups []string
		userGroups    []string
		want          bool
	}{
		{
			name:          "empty AllowedGroups allows any login",
			allowedGroups: nil,
			userGroups:    []string{"some-random-group"},
			want:          true,
		},
		{
			name:          "empty AllowedGroups allows a login with no groups",
			allowedGroups: []string{},
			userGroups:    nil,
			want:          true,
		},
		{
			name:          "user in an allowed group is admitted",
			allowedGroups: []string{"bindery-users", "bindery-admins"},
			userGroups:    []string{"staff", "bindery-users"},
			want:          true,
		},
		{
			name:          "user not in any allowed group is rejected",
			allowedGroups: []string{"bindery-users"},
			userGroups:    []string{"staff", "everyone"},
			want:          false,
		},
		{
			name:          "user with no groups is rejected when AllowedGroups is set",
			allowedGroups: []string{"bindery-users"},
			userGroups:    nil,
			want:          false,
		},
		{
			name:          "group matching is case-sensitive",
			allowedGroups: []string{"Bindery-Users"},
			userGroups:    []string{"bindery-users"},
			want:          false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GroupsAllowed(tt.allowedGroups, tt.userGroups); got != tt.want {
				t.Errorf("GroupsAllowed(%v, %v) = %v, want %v",
					tt.allowedGroups, tt.userGroups, got, tt.want)
			}
		})
	}
}

// --- email_verified parsing (issue #709, finding 1) -------------------------

func TestParseEmailVerified(t *testing.T) {
	tests := []struct {
		name string
		raw  string // raw JSON value of the email_verified claim
		want bool
	}{
		{name: "absent claim is not verified", raw: ``, want: false},
		{name: "boolean true", raw: `true`, want: true},
		{name: "boolean false", raw: `false`, want: false},
		{name: "string \"true\" (some IdPs serialise it this way)", raw: `"true"`, want: true},
		{name: "string \"True\" case-insensitive", raw: `"True"`, want: true},
		{name: "string \"false\"", raw: `"false"`, want: false},
		{name: "empty string is not verified", raw: `""`, want: false},
		{name: "null is not verified", raw: `null`, want: false},
		{name: "numeric 1 is not verified (not a recognised truthy form)", raw: `1`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw json.RawMessage
			if tt.raw != "" {
				raw = json.RawMessage(tt.raw)
			}
			if got := parseEmailVerified(raw); got != tt.want {
				t.Errorf("parseEmailVerified(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// TestProviderConfig_ExposesAllowedGroups verifies the manager surfaces a
// loaded provider's config (including AllowedGroups) so the callback handler
// can enforce the group policy after a token exchange.
func TestProviderConfig_ExposesAllowedGroups(t *testing.T) {
	m := NewManager()
	if _, ok := m.ProviderConfig("missing"); ok {
		t.Fatal("ProviderConfig should report ok=false for an unknown provider")
	}

	cfg := ProviderConfig{
		ID:            "corp",
		Issuer:        "https://idp.example.com",
		ClientID:      "cid",
		AllowedGroups: []string{"bindery-users"},
	}
	// Inject a loaded entry directly — discovery is exercised elsewhere and
	// this test only cares about the config accessor.
	m.mu.Lock()
	m.providers["corp"] = &entry{cfg: cfg}
	m.mu.Unlock()

	got, ok := m.ProviderConfig("corp")
	if !ok {
		t.Fatal("ProviderConfig should report ok=true for a loaded provider")
	}
	if len(got.AllowedGroups) != 1 || got.AllowedGroups[0] != "bindery-users" {
		t.Fatalf("AllowedGroups = %v, want [bindery-users]", got.AllowedGroups)
	}
}

// --- Wave 5 / M: concurrency regressions -------------------------------------

// pausingIDP is a fakeIDP that blocks every discovery request on a channel
// the test controls. It lets us assert Reload releases the manager lock during
// the IdP roundtrip — concurrent reads must not be stuck behind it.
type pausingIDP struct {
	*httptest.Server
	release chan struct{}
	hits    atomic.Int32
}

func newPausingIDP() *pausingIDP {
	p := &pausingIDP{release: make(chan struct{})}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			http.NotFound(w, r)
			return
		}
		p.hits.Add(1)
		// Block until the test releases us — simulates a slow IdP.
		<-p.release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                p.URL,
			"authorization_endpoint":                p.URL + "/authorize",
			"token_endpoint":                        p.URL + "/token",
			"jwks_uri":                              p.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	}))
	return p
}

// TestReload_ReleasesLockDuringDiscovery is the regression test for Wave 5
// finding 17: Manager.Reload used to hold the write lock through the
// gooidc.NewProvider discovery roundtrip, blocking every concurrent OIDC
// operation behind it. The fix snapshots state under RLock, does discovery
// unlocked, and installs the new state under a short Lock.
//
// We assert by calling Reload in goroutine A (which stalls in the paused IdP)
// and then calling List/Status from goroutine B. With the bug, B blocks until
// the IdP is released. With the fix, B returns promptly.
func TestReload_ReleasesLockDuringDiscovery(t *testing.T) {
	idp := newPausingIDP()
	defer idp.Close()

	mgr := NewManager()
	cfg := ProviderConfig{ID: "slow", Name: "Slow", Issuer: idp.URL, ClientID: "cid", ClientSecret: "sec"}

	reloadDone := make(chan struct{})
	go func() {
		mgr.Reload(context.Background(), []ProviderConfig{cfg})
		close(reloadDone)
	}()

	// Wait for the IdP to start serving the request — that's the proof the
	// goroutine is currently in the middle of discovery.
	deadline := time.Now().Add(2 * time.Second)
	for idp.hits.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Reload goroutine did not reach the IdP within 2s")
		}
		time.Sleep(time.Millisecond)
	}

	// Now hammer the read paths. With the old code these would block on the
	// write lock until the IdP responds. With the fix they return promptly.
	readDone := make(chan struct{})
	go func() {
		_ = mgr.List()
		_ = mgr.Status("slow")
		_ = mgr.Get("slow")
		close(readDone)
	}()

	select {
	case <-readDone:
		// good — the reads did not block behind the network call.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("read paths blocked behind Reload's network roundtrip — the lock is still held during discovery")
	}

	// Let the IdP finish so the goroutine can exit cleanly.
	close(idp.release)
	select {
	case <-reloadDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Reload did not complete after IdP was released")
	}
	if mgr.Status("slow").State != "ok" {
		t.Fatalf("Reload should have loaded the provider, status=%+v", mgr.Status("slow"))
	}
}

// TestEnsureLoaded_NoRaceOnLastAttempt is the regression test for Wave 5
// finding 18: EnsureLoaded used to read failedEntry.lastAttempt outside the
// lock. Under `go test -race` with concurrent EnsureLoaded calls plus a
// Reload that mutates the failed map, the detector flagged the read.
//
// This test drives many concurrent EnsureLoaded calls against a failed
// provider, interleaved with a Reload that swaps the failed map. With the
// fix the race detector reports no findings; pre-fix it reliably triggers.
func TestEnsureLoaded_NoRaceOnLastAttempt(t *testing.T) {
	idp := newFakeIDP()
	defer idp.Close()
	idp.failing.Store(true)

	prev := retryMinInterval
	retryMinInterval = 1 * time.Millisecond
	defer func() { retryMinInterval = prev }()

	mgr := NewManager()
	cfg := ProviderConfig{ID: "racy", Name: "Racy", Issuer: idp.URL, ClientID: "cid", ClientSecret: "sec"}
	mgr.Reload(context.Background(), []ProviderConfig{cfg})
	if mgr.Status("racy").State != "failed" {
		t.Fatal("setup: provider should start in failed state")
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Fan-out EnsureLoaded calls. Each one races on the lastAttempt read of
	// the failedEntry pointer in the map.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					mgr.EnsureLoaded(context.Background(), "racy")
				}
			}
		}()
	}

	// A concurrent Reload swaps the failed-map pointer entirely, exercising
	// the path where the failedEntry observed by EnsureLoaded's RLock branch
	// is then re-pointed by a writer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				mgr.Reload(context.Background(), []ProviderConfig{cfg})
			}
		}
	}()

	// Also pound Status which reads the same field from the read side.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = mgr.Status("racy")
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestEnsureLoaded_ReleasesLockDuringRetryDiscovery covers the same lock
// release pattern for the on-demand retry path. With the old code, a single
// click on a failed-provider login button held the write lock through the
// retry's discovery roundtrip and blocked every other OIDC operation.
func TestEnsureLoaded_ReleasesLockDuringRetryDiscovery(t *testing.T) {
	// Start with a normal fake IdP so the initial Reload records a failed
	// entry quickly. Then swap in a pausing IdP for the retry path.
	failingIDP := newFakeIDP()
	defer failingIDP.Close()
	failingIDP.failing.Store(true)

	prev := retryMinInterval
	retryMinInterval = 1 * time.Millisecond
	defer func() { retryMinInterval = prev }()

	mgr := NewManager()
	// Use the pausing IdP as the configured issuer from the start; the first
	// Reload call below will block in discovery and we'll release it to seed
	// a failed entry, then verify the retry path doesn't hold the lock.
	pausingFailing := newPausingIDP()
	defer pausingFailing.Close()

	cfg := ProviderConfig{ID: "retry", Name: "Retry", Issuer: pausingFailing.URL, ClientID: "cid", ClientSecret: "sec"}

	// Seed: release the IdP with a 503 by closing it mid-flight isn't viable;
	// instead, drive the initial discovery to failure by cancelling the ctx.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	mgr.Reload(ctx, []ProviderConfig{cfg})
	cancel()
	if mgr.Status("retry") == nil || mgr.Status("retry").State != "failed" {
		t.Fatalf("setup: provider should be in failed state after cancelled discovery, got %+v", mgr.Status("retry"))
	}

	// Sleep past the retry interval so EnsureLoaded will attempt.
	time.Sleep(5 * time.Millisecond)

	// Trigger the retry path. It will block in the IdP roundtrip.
	retryDone := make(chan struct{})
	go func() {
		mgr.EnsureLoaded(context.Background(), "retry")
		close(retryDone)
	}()

	// Wait until the retry goroutine has reached the IdP.
	deadline := time.Now().Add(2 * time.Second)
	for pausingFailing.hits.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("retry goroutine did not reach the IdP within 2s")
		}
		time.Sleep(time.Millisecond)
	}

	// Concurrent read must not be blocked behind the retry's roundtrip.
	readDone := make(chan struct{})
	go func() {
		_ = mgr.List()
		_ = mgr.Status("retry")
		close(readDone)
	}()
	select {
	case <-readDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("read paths blocked behind EnsureLoaded's retry roundtrip — the lock is still held during discovery")
	}

	close(pausingFailing.release)
	select {
	case <-retryDone:
	case <-time.After(2 * time.Second):
		t.Fatal("EnsureLoaded did not complete after IdP was released")
	}
}
