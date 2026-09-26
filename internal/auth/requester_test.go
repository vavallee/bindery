package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// requesterDeniedRoutes is every route a requester must not reach, named from
// the router in cmd/bindery/main.go and its register* helpers. Each one is
// also tried in the variant spellings from security review item S6.
var requesterDeniedRoutes = []struct{ method, path string }{
	// Grabbing and the download queue.
	{http.MethodPost, "/api/v1/queue/grab"},
	{http.MethodGet, "/api/v1/queue"},
	{http.MethodDelete, "/api/v1/queue/7"},
	{http.MethodPost, "/api/v1/queue/bulk-delete"},
	{http.MethodPost, "/api/v1/queue/7/retry-import"},
	{http.MethodPost, "/api/v1/queue/7/retry"},
	{http.MethodPost, "/api/v1/queue/bulk-retry"},
	{http.MethodGet, "/api/queue"}, // the Arr compatible tree
	{http.MethodGet, "/api/v1/pending"},
	{http.MethodPost, "/api/v1/pending/7/grab"},
	{http.MethodDelete, "/api/v1/pending/7"},
	// Direct adds and library mutations.
	{http.MethodPost, "/api/v1/author"},
	{http.MethodPost, "/api/v1/author/book"},
	{http.MethodPost, "/api/v1/author/bulk"},
	{http.MethodDelete, "/api/v1/author/7"},
	{http.MethodPut, "/api/v1/author/7"},
	{http.MethodPost, "/api/v1/author/7/refresh"},
	{http.MethodPost, "/api/v1/author/7/merge"},
	{http.MethodDelete, "/api/v1/book/7"},
	{http.MethodDelete, "/api/v1/book/7/file"},
	{http.MethodPut, "/api/v1/book/7"},
	{http.MethodPost, "/api/v1/book/bulk"},
	{http.MethodPost, "/api/v1/book/7/rebind"},
	{http.MethodPost, "/api/v1/wanted/bulk"},
	// Reads that carry more than the projection, and the file download.
	{http.MethodGet, "/api/v1/author"},
	{http.MethodGet, "/api/v1/author/7"},
	{http.MethodGet, "/api/v1/book"},
	{http.MethodGet, "/api/v1/book/7"},
	{http.MethodGet, "/api/v1/book/7/file"},
	{http.MethodGet, "/api/v1/series"},
	{http.MethodGet, "/api/v1/wanted/missing"},
	{http.MethodGet, "/api/v1/search/library"},
	{http.MethodGet, "/api/v1/calendar"},
	// Searches that grab or hit indexers.
	{http.MethodPost, "/api/v1/book/7/search"},
	{http.MethodGet, "/api/v1/indexer/search"},
	{http.MethodGet, "/api/v1/search/last-debug"},
	// Library scan, adoption and bulk refresh.
	{http.MethodPost, "/api/v1/library/scan"},
	{http.MethodGet, "/api/v1/library/unmatched"},
	{http.MethodGet, "/api/v1/library/unmatched/summary"},
	{http.MethodPost, "/api/v1/library/unmatched/7/adopt"},
	{http.MethodPost, "/api/v1/library/unmatched/7/ignore"},
	{http.MethodPost, "/api/v1/downloadclient/7/diagnose"},
	{http.MethodGet, "/api/v1/library/scan/status"},
	{http.MethodPost, "/api/v1/authors/refresh-all"},
	// History and blocklist.
	{http.MethodGet, "/api/v1/history"},
	{http.MethodDelete, "/api/v1/history/7"},
	{http.MethodPost, "/api/v1/history/7/blocklist"},
	{http.MethodGet, "/api/v1/blocklist"},
	{http.MethodDelete, "/api/v1/blocklist/bulk"},
	// Profiles, settings, system.
	{http.MethodGet, "/api/v1/metadataprofile"},
	{http.MethodPost, "/api/v1/metadataprofile"},
	{http.MethodGet, "/api/v1/qualityprofile"},
	{http.MethodGet, "/api/v1/rootfolder"},
	{http.MethodGet, "/api/v1/setting"},
	{http.MethodGet, "/api/v1/setting/metadata.primary_provider"},
	{http.MethodPut, "/api/v1/setting/requests.max_pending_per_user"},
	{http.MethodGet, "/api/v1/system/status"},
	{http.MethodGet, "/api/v1/system/setup-state"},
	{http.MethodGet, "/api/v1/system/logs"},
	{http.MethodGet, "/api/v1/notification"},
	{http.MethodGet, "/api/v1/downloadclient"},
	{http.MethodGet, "/api/v1/indexer"},
	// Recommendations, imports, migrations, integrations.
	{http.MethodGet, "/api/v1/recommendations"},
	{http.MethodPost, "/api/v1/recommendations/7/add"},
	{http.MethodPost, "/api/v1/recommendations/refresh"},
	{http.MethodPost, "/api/v1/queue/manual-import"},
	{http.MethodPost, "/api/v1/migrate/csv"},
	{http.MethodPost, "/api/v1/calibre/import"},
	{http.MethodPost, "/api/v1/abs/import"},
	{http.MethodGet, "/api/v1/backup"},
	// Auth administration and first run setup.
	{http.MethodGet, "/api/v1/auth/users"},
	{http.MethodPut, "/api/v1/auth/users/7/role"},
	{http.MethodPost, "/api/v1/auth/apikey/regenerate"},
	{http.MethodPut, "/api/v1/auth/mode"},
	{http.MethodPost, "/api/v1/auth/setup"},
	{http.MethodPost, "/api/v1/auth/oidc/test-discovery"},
	// The admin half of the requests API.
	{http.MethodGet, "/api/v1/requests/queue"},
	{http.MethodGet, "/api/v1/requests/pending-count"},
	{http.MethodPost, "/api/v1/requests/7/approve"},
	{http.MethodPost, "/api/v1/requests/7/decline"},
	// Methods the allow list does not grant on allowed paths.
	{http.MethodPut, "/api/v1/requests"},
	{http.MethodPost, "/api/v1/requests/library"},
	{http.MethodPatch, "/api/v1/requests/7"},
	{http.MethodDelete, "/api/v1/images"},
	{http.MethodOptions, "/api/v1/requests"},
	// Unknown paths.
	{http.MethodGet, "/api/v1/does-not-exist"},
	{http.MethodGet, "/api/v1"},
	{http.MethodGet, "/"},
}

// variants spells one denied route every way S6 lists: HEAD, a trailing
// slash, a doubled slash, a dot segment, mixed case, an encoded slash and an
// escaped letter. None may reach a handler.
func variants(method, path string) []struct{ method, target string } {
	out := []struct{ method, target string }{{method, path}}
	if method == http.MethodGet {
		out = append(out, struct{ method, target string }{http.MethodHead, path})
	}
	add := func(target string) { out = append(out, struct{ method, target string }{method, target}) }
	add(path + "/")
	add(strings.Replace(path, "/api/v1/", "/api/v1//", 1))
	add(strings.Replace(path, "/api/", "/api/./", 1))
	add(strings.Replace(path, "/api/", "/api/x/../", 1))
	add(strings.ToUpper(path))
	if i := strings.LastIndex(path, "/"); i > 0 {
		add(path[:i] + "%2F" + path[i+1:])
		add(path[:i] + "%2f" + path[i+1:])
	}
	if strings.Contains(path, "/v1/") {
		add(strings.Replace(path, "/v1/", "/v%31/", 1))
	}
	return out
}

func requesterRequest(method, target string) *http.Request {
	u, err := url.ParseRequestURI(target)
	if err != nil {
		panic(err)
	}
	r := (&http.Request{Method: method, URL: u, Header: http.Header{}}).WithContext(context.Background())
	ctx := WithUserRole(WithUserID(r.Context(), 42), RoleRequester)
	return r.WithContext(ctx)
}

// serveGuard runs req through the guard in front of a handler that records
// whether it ran.
func serveGuard(t *testing.T, req *http.Request) (int, bool) {
	t.Helper()
	reached := false
	h := restrictRequester(defaultRequesterMatcher, NewRequesterLimiter(1000, 1000, time.Minute, 16), NewRequesterLimiter(1000, 1000, time.Minute, 16))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusNoContent)
		}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, reached
}

func TestRestrictRequester_DeniesEveryListedRoute(t *testing.T) {
	if len(requesterDeniedRoutes) < 40 {
		t.Fatalf("denied table has %d routes, want at least 40", len(requesterDeniedRoutes))
	}
	for _, d := range requesterDeniedRoutes {
		for _, v := range variants(d.method, d.path) {
			code, reached := serveGuard(t, requesterRequest(v.method, v.target))
			if reached || code != http.StatusForbidden {
				t.Errorf("%s %s: status %d, handler reached %v; want 403 before the handler", v.method, v.target, code, reached)
			}
		}
	}
}

// TestRestrictRequester_AllowListVariantsDenied: even an allowed route is
// refused when spelled any way but its canonical form, so a matcher and a
// router can never disagree about which route a spelling means.
func TestRestrictRequester_AllowListVariantsDenied(t *testing.T) {
	for _, rt := range RequesterAllowList {
		p := concretePath(rt.Pattern)
		for _, v := range variants(rt.Method, p)[1:] {
			if v.method == http.MethodHead {
				continue // HEAD of an allowed GET is allowed; covered below
			}
			code, reached := serveGuard(t, requesterRequest(v.method, v.target))
			if reached || code != http.StatusForbidden {
				t.Errorf("%s %s: status %d, reached %v; want 403 for a non canonical spelling", v.method, v.target, code, reached)
			}
		}
	}
}

func concretePath(pattern string) string {
	r := strings.NewReplacer("{id}", "7", "{provider}", "authentik")
	return r.Replace(pattern)
}

func TestRestrictRequester_AllowsEveryAllowListEntry(t *testing.T) {
	for _, rt := range RequesterAllowList {
		p := concretePath(rt.Pattern)
		code, reached := serveGuard(t, requesterRequest(rt.Method, p))
		if !reached || code == http.StatusForbidden {
			t.Errorf("%s %s: status %d, reached %v; want the handler to run", rt.Method, p, code, reached)
		}
		if rt.Method == http.MethodGet {
			code, reached = serveGuard(t, requesterRequest(http.MethodHead, p))
			if !reached {
				t.Errorf("HEAD %s: status %d; want HEAD of an allowed GET to pass", p, code)
			}
		}
	}
}

func TestRestrictRequester_QueryStringDoesNotChangeTheRoute(t *testing.T) {
	code, reached := serveGuard(t, requesterRequest(http.MethodGet, "/api/v1/search/book?term=%2Fapi%2Fv1%2Fqueue&x=../../queue"))
	if !reached {
		t.Fatalf("search with an odd query string: status %d, want allowed", code)
	}
}

// TestRestrictRequester_IgnoresMethodOverride: the guard reads r.Method only.
func TestRestrictRequester_IgnoresMethodOverride(t *testing.T) {
	for _, h := range []string{"X-HTTP-Method-Override", "X-HTTP-Method", "X-Method-Override"} {
		req := requesterRequest(http.MethodPost, "/api/v1/queue/grab")
		req.Header.Set(h, http.MethodGet)
		if code, reached := serveGuard(t, req); reached || code != http.StatusForbidden {
			t.Errorf("POST /queue/grab with %s: GET got %d, reached %v", h, code, reached)
		}
		req = requesterRequest(http.MethodGet, "/api/v1/requests")
		req.Header.Set(h, http.MethodDelete)
		if _, reached := serveGuard(t, req); !reached {
			t.Errorf("GET /requests with %s: DELETE was refused, so the header was read", h)
		}
	}
}

func TestRestrictRequester_OtherRolesUnaffected(t *testing.T) {
	for _, role := range []string{RoleAdmin, RoleUser} {
		req := requesterRequest(http.MethodPost, "/api/v1/queue/grab")
		req = req.WithContext(WithUserRole(req.Context(), role))
		if _, reached := serveGuard(t, req); !reached {
			t.Errorf("role %s refused on POST /queue/grab", role)
		}
	}
	// No identity at all: only AllowUnauthPath requests reach the guard this
	// way, and Middleware already constrained them.
	u, _ := url.ParseRequestURI("/api/v1/auth/setup")
	anon := &http.Request{Method: http.MethodPost, URL: u, Header: http.Header{}}
	if _, reached := serveGuard(t, anon.WithContext(context.Background())); !reached {
		t.Error("anonymous first run setup refused")
	}
}

// TestRestrictRequester_FailsClosedOnUnknownRole: a signed in user whose role
// could not be read, or who carries a role this build does not know, gets the
// requester allow list, not full access.
func TestRestrictRequester_FailsClosedOnUnknownRole(t *testing.T) {
	for _, role := range []string{"", "superuser", "Admin"} {
		req := requesterRequest(http.MethodPost, "/api/v1/queue/grab")
		req = req.WithContext(WithUserRole(req.Context(), role))
		if code, reached := serveGuard(t, req); reached || code != http.StatusForbidden {
			t.Errorf("role %q: status %d reached %v, want 403", role, code, reached)
		}
	}
}

func TestRestrictRequester_ChiRouteMethodMismatchDenied(t *testing.T) {
	req := requesterRequest(http.MethodGet, "/api/v1/requests")
	rctx := chi.NewRouteContext()
	rctx.RouteMethod = http.MethodDelete
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	if code, reached := serveGuard(t, req); reached || code != http.StatusForbidden {
		t.Fatalf("routing method differs from r.Method: status %d reached %v, want 403", code, reached)
	}
}

func TestRestrictRequester_LimitsSearches(t *testing.T) {
	limiter := NewRequesterLimiter(3, 0.5, time.Minute, 16)
	now := time.Unix(1_700_000_000, 0)
	limiter.now = func() time.Time { return now }
	h := restrictRequester(defaultRequesterMatcher, limiter, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	serve := func(target string, uid int64) *httptest.ResponseRecorder {
		req := requesterRequest(http.MethodGet, target)
		req = req.WithContext(WithUserID(req.Context(), uid))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < 3; i++ {
		if rec := serve("/api/v1/search/book?term=x", 1); rec.Code != http.StatusOK {
			t.Fatalf("search %d: status %d, want 200 inside the burst", i, rec.Code)
		}
	}
	rec := serve("/api/v1/book/lookup?isbn=1", 1)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("fourth search: status %d Retry-After %q, want 429 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := serve("/api/v1/requests", 1); rec.Code != http.StatusOK {
		t.Fatalf("unlimited route: status %d, want 200 while searches are limited", rec.Code)
	}
	if rec := serve("/api/v1/search/author?term=x", 2); rec.Code != http.StatusOK {
		t.Fatalf("another user: status %d, want their own bucket", rec.Code)
	}
	now = now.Add(2 * time.Second)
	if rec := serve("/api/v1/search/author?term=x", 1); rec.Code != http.StatusOK {
		t.Fatalf("after refill: status %d, want 200", rec.Code)
	}
	// Admins and users are never limited.
	for i := 0; i < 10; i++ {
		req := requesterRequest(http.MethodGet, "/api/v1/search/book?term=x")
		req = req.WithContext(WithUserRole(req.Context(), RoleUser))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("role user search %d limited: %d", i, rec.Code)
		}
	}
}

func TestRequesterLimiter_BoundedWithIdleEviction(t *testing.T) {
	limiter := NewRequesterLimiter(5, 1, time.Minute, 4)
	now := time.Unix(1_700_000_000, 0)
	limiter.now = func() time.Time { return now }
	for id := int64(1); id <= 50; id++ {
		limiter.allow(id)
		if n := limiter.size(); n > 4 {
			t.Fatalf("after user %d the limiter holds %d buckets, cap is 4", id, n)
		}
	}
	now = now.Add(2 * time.Minute)
	limiter.allow(999)
	if n := limiter.size(); n != 1 {
		t.Fatalf("after the idle period the limiter holds %d buckets, want only the new one", n)
	}
}

// TestRestrictRequester_CreateAndImagesAreLimited: POST /requests spends the
// provider bucket (its lookup reaches the provider) and the image proxy spends
// its own bucket, so neither is an unlimited way to make Bindery fetch.
func TestRestrictRequester_CreateAndImagesAreLimited(t *testing.T) {
	provider := NewRequesterLimiter(2, 0.001, time.Minute, 16)
	images := NewRequesterLimiter(3, 0.001, time.Minute, 16)
	var charged []bool
	h := restrictRequester(defaultRequesterMatcher, provider, images)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		charged = append(charged, RequesterProviderCharged(r.Context()))
		w.WriteHeader(http.StatusOK)
	}))
	serve := func(method, target string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, requesterRequest(method, target))
		return rec.Code
	}
	for i := 0; i < 2; i++ {
		if code := serve(http.MethodPost, "/api/v1/requests"); code != http.StatusOK {
			t.Fatalf("create %d: %d", i, code)
		}
	}
	if code := serve(http.MethodPost, "/api/v1/requests"); code != http.StatusTooManyRequests {
		t.Fatalf("third create: %d, want 429", code)
	}
	if code := serve(http.MethodGet, "/api/v1/search/book?term=x"); code != http.StatusTooManyRequests {
		t.Fatalf("search after creates drained the provider bucket: %d, want 429", code)
	}
	if len(charged) != 2 || !charged[0] || !charged[1] {
		t.Fatalf("handler saw provider charged = %v, want true for both creates", charged)
	}
	for i := 0; i < 3; i++ {
		if code := serve(http.MethodGet, "/api/v1/images?url=https://x/"+strconv.Itoa(i)); code != http.StatusOK {
			t.Fatalf("image %d: %d", i, code)
		}
	}
	if code := serve(http.MethodGet, "/api/v1/images?url=https://x/9"); code != http.StatusTooManyRequests {
		t.Fatalf("fourth image: %d, want 429", code)
	}
}

// TestRequesterLimiter_AllowRequester: a handler spending the bucket itself
// charges a restricted caller once, never charges one the guard already
// charged, and never limits admins or users.
func TestRequesterLimiter_AllowRequester(t *testing.T) {
	l := NewRequesterLimiter(1, 0.001, time.Minute, 16)
	ctx := WithUserRole(WithUserID(context.Background(), 5), RoleRequester)
	if ok, _ := l.AllowRequester(ctx); !ok {
		t.Fatal("first call refused")
	}
	if ok, retry := l.AllowRequester(ctx); ok || retry < 1 {
		t.Fatalf("second call ok=%v retry=%d, want refused with a wait", ok, retry)
	}
	charged := context.WithValue(ctx, providerChargedCtxKey{}, true)
	if ok, _ := l.AllowRequester(charged); !ok {
		t.Fatal("a request the guard already charged was charged again")
	}
	for _, role := range []string{RoleAdmin, RoleUser} {
		c := WithUserRole(WithUserID(context.Background(), 5), role)
		for i := 0; i < 5; i++ {
			if ok, _ := l.AllowRequester(c); !ok {
				t.Fatalf("role %s limited", role)
			}
		}
	}
}
