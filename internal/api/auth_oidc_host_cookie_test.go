package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth/oidc"
)

// The __Host- prefix, not the signature, is what closes #2362.
//
// Signing stops an attacker forging a flow cookie. The attack in the issue
// never needed a forgery: the login endpoint is unauthenticated by necessity
// and hands out a genuine signed cookie to anyone who asks, so the attacker
// starts a real flow as themselves and plants the real cookie. What breaks
// that is a cookie the victim's browser refuses to let anyone else set, which
// is what __Host- is for.

// oidcReq builds a request against the OIDC routes with the chi provider param
// injected. An https target makes httptest set req.TLS, which is what
// cookieSecure reads.
func oidcReq(target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "test")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestLogin_SecureInstallSetsHostPrefixedFlowCookie(t *testing.T) {
	idp := newFakeIDP(t)
	h, _, _ := newCallbackTestHandler(t, idp, nil, false)

	rec := httptest.NewRecorder()
	h.Login(rec, oidcReq("https://bindery.example.com/api/v1/auth/oidc/test/login"))

	if rec.Code != http.StatusFound {
		t.Fatalf("login status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	c := cookieNamed(rec, oidcFlowCookieHost)
	if c == nil {
		t.Fatalf("no %s cookie set; got %v", oidcFlowCookieHost, rec.Result().Cookies())
	}
	// A browser silently DROPS a __Host- cookie that breaks any of these, so
	// getting one wrong turns every OIDC login into "missing flow cookie"
	// rather than into a visible error. Asserted individually so a failure
	// names the attribute.
	if !c.Secure {
		t.Error("__Host- cookie is not Secure; the browser will refuse to store it")
	}
	if c.Path != "/" {
		t.Errorf("__Host- cookie Path=%q, want \"/\"; the browser will refuse to store it", c.Path)
	}
	if c.Domain != "" {
		t.Errorf("__Host- cookie carries Domain=%q; the browser will refuse to store it", c.Domain)
	}
	if !c.HttpOnly {
		t.Error("flow cookie lost HttpOnly")
	}
	if cookieNamed(rec, oidcFlowCookie) != nil {
		t.Error("the unprefixed name was set alongside the prefixed one, which re-opens the hole the prefix closes")
	}
}

// __Host- implies Secure, so a plain-HTTP install cannot use it at all. Those
// installs keep the old name and stay exposed, which is honest rather than
// silently setting a cookie the browser will drop.
func TestLogin_PlainHTTPKeepsLegacyFlowCookie(t *testing.T) {
	idp := newFakeIDP(t)
	h, _, _ := newCallbackTestHandler(t, idp, nil, false)

	rec := httptest.NewRecorder()
	h.Login(rec, oidcReq("http://bindery.example.com/api/v1/auth/oidc/test/login"))

	if rec.Code != http.StatusFound {
		t.Fatalf("login status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if cookieNamed(rec, oidcFlowCookieHost) != nil {
		t.Error("set a __Host- cookie over plain HTTP; the browser drops it and every login breaks")
	}
	c := cookieNamed(rec, oidcFlowCookie)
	if c == nil {
		t.Fatalf("no %s cookie set; got %v", oidcFlowCookie, rec.Result().Cookies())
	}
	if c.Path != "/api/v1/auth/oidc" {
		t.Errorf("legacy cookie Path=%q, want the narrow path", c.Path)
	}
}

// doCallbackNamed is doCallback with the scheme and the cookie name under the
// test's control, so a planted cookie can be presented exactly the way the
// attacker in #2362 would present it.
func doCallbackNamed(t *testing.T, h *OIDCHandler, scheme, cookieName string) *httptest.ResponseRecorder {
	t.Helper()
	state := "test-state"
	flowVal, err := oidc.EncodeFlowState(h.flowSecret(context.Background()), "test",
		state, "test-nonce", "test-verifier-aaaaaaaaaaaaaaaaaaaaaaaa", "https://bindery.example.com")
	if err != nil {
		t.Fatalf("encode flow state: %v", err)
	}
	req := oidcReq(scheme + "://bindery.example.com/api/v1/auth/oidc/test/callback?state=" +
		url.QueryEscape(state) + "&code=test-code")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: flowVal})

	rec := httptest.NewRecorder()
	h.Callback(rec, req)
	return rec
}

// The whole point. On an HTTPS install a cookie under the unprefixed name is
// refused even though it is genuine and correctly signed, because that is the
// name a sibling subdomain or a plaintext hop can still write. Honouring it as
// a fallback would leave #2362 open while the PR claimed to have closed it.
func TestCallback_SecureInstallRefusesLegacyCookieName(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "attacker", "nonce": "test-nonce",
		"email": "attacker@example.com", "email_verified": true,
	}
	h, _, _ := newCallbackTestHandler(t, idp, nil, false)

	rec := doCallbackNamed(t, h, "https", oidcFlowCookie)

	if rec.Code == http.StatusFound {
		t.Fatalf("a planted %s cookie completed the flow on an HTTPS install; #2362 is still open", oidcFlowCookie)
	}
	if hasSessionCookie(rec) {
		t.Fatal("a session was issued from a cookie under the unprefixed name")
	}
	if !strings.Contains(rec.Body.String(), "missing flow cookie") {
		t.Errorf("body=%q, want the missing-cookie refusal", rec.Body.String())
	}
}

// The prefixed name still works, or the fix would just be an outage.
func TestCallback_SecureInstallAcceptsHostPrefixedCookie(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "real-user", "nonce": "test-nonce",
		"email": "user@example.com", "email_verified": true,
	}
	h, _, _ := newCallbackTestHandler(t, idp, nil, false)

	rec := doCallbackNamed(t, h, "https", oidcFlowCookieHost)

	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if !hasSessionCookie(rec) {
		t.Fatal("no session cookie issued for a legitimate __Host- flow")
	}
}

// A plain-HTTP install has no prefixed name to use, so the legacy one must
// still complete. Without this the fix would break every HTTP deploy.
func TestCallback_PlainHTTPStillAcceptsLegacyCookieName(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "http-user", "nonce": "test-nonce",
		"email": "http@example.com", "email_verified": true,
	}
	h, _, _ := newCallbackTestHandler(t, idp, nil, false)

	rec := doCallbackNamed(t, h, "http", oidcFlowCookie)

	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if !hasSessionCookie(rec) {
		t.Fatal("no session cookie issued on a plain-HTTP install")
	}
}

// A browser upgraded mid-flow still holds the old cookie under the old name.
// Nothing reads it any more, and a credential nobody consumes is still a
// credential sitting in a browser, so the callback expires both.
func TestCallback_ClearsBothCookieNames(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "real-user", "nonce": "test-nonce",
		"email": "user@example.com", "email_verified": true,
	}
	h, _, _ := newCallbackTestHandler(t, idp, nil, false)

	rec := doCallbackNamed(t, h, "https", oidcFlowCookieHost)

	for _, name := range []string{oidcFlowCookieHost, oidcFlowCookie} {
		c := cookieNamed(rec, name)
		if c == nil {
			t.Errorf("%s was never expired", name)
			continue
		}
		if c.MaxAge >= 0 {
			t.Errorf("%s MaxAge=%d, want negative so the browser drops it", name, c.MaxAge)
		}
	}
}
