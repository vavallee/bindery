package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// burstCodes fires n copies of the request built by mk at handler h, released
// together off one barrier so they genuinely overlap, and returns the status
// code of each.
func burstCodes(n int, mk func(i int) *http.Request, h http.Handler) []int {
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, n)
	for i := range n {
		req := mk(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			<-start
			h.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}()
	}
	close(start)
	wg.Wait()
	return codes
}

func countCodes(codes []int) map[int]int {
	m := map[int]int{}
	for _, c := range codes {
		m[c]++
	}
	return m
}

// TestLogin_ConcurrentBurstBoundedByLimit pins the atomic attempt accounting:
// a burst of simultaneous wrong passwords from one address may run at most
// `limit` password verifications. The rest must be refused with 429 before
// the KDF runs. Checking the limit and recording the failure as two separate
// steps let every request in a burst pass the check before any failure landed.
func TestLogin_ConcurrentBurstBoundedByLimit(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t) // limiter allows 5 per window
	hash, _ := auth.HashPassword("correct-password")
	if _, err := users.Create(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	const burst = 40
	codes := burstCodes(burst, func(int) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/auth/login",
			jsonBody(t, loginRequest{Username: "admin", Password: "wrong"}))
		req.RemoteAddr = "203.0.113.77:4444"
		return req
	}, http.HandlerFunc(h.Login))
	got := countCodes(codes)
	if got[http.StatusUnauthorized] > 5 {
		t.Errorf("%d of %d concurrent wrong passwords were evaluated; want at most 5 (codes %v)",
			got[http.StatusUnauthorized], burst, got)
	}
	if got[http.StatusUnauthorized]+got[http.StatusTooManyRequests] != burst {
		t.Errorf("unexpected status mix %v", got)
	}
}

// TestOPDS_ConcurrentBasicBurstBoundedByLimit is the OPDS Basic auth twin of
// the login burst test: the same limiter, the same check then record gap.
// Every request guesses a different password, so none can share a
// verification and each must reserve its own attempt.
func TestOPDS_ConcurrentBasicBurstBoundedByLimit(t *testing.T) {
	r, users, _, _ := opdsFixture(t) // limiter allows 5 per window
	hash, _ := auth.HashPassword("right-password-123")
	if _, err := users.Create(context.Background(), "admin", hash); err != nil {
		t.Fatal(err)
	}
	const burst = 40
	codes := burstCodes(burst, func(i int) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/opds/", nil)
		req.SetBasicAuth("admin", "wrong-password-"+strconv.Itoa(i))
		req.RemoteAddr = "203.0.113.78:4444"
		return req
	}, r)
	got := countCodes(codes)
	if got[http.StatusUnauthorized] > 5 {
		t.Errorf("%d of %d concurrent wrong Basic credentials were evaluated; want at most 5 (codes %v)",
			got[http.StatusUnauthorized], burst, got)
	}
	if got[http.StatusUnauthorized]+got[http.StatusTooManyRequests] != burst {
		t.Errorf("unexpected status mix %v", got)
	}
}

// opdsBasicHarness wires OPDSAuth's Basic path around a trivial 200 handler
// with an observable verifier and a 5 per window limiter.
func opdsBasicHarness(t *testing.T) (http.Handler, *db.UserRepo, *opdsBasicVerifier) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	settings := db.NewSettingsRepo(database)
	if err := settings.Set(context.Background(), SettingAuthSessionSecret, "abcdefghijklmnopqrstuvwxyz012345"); err != nil {
		t.Fatal(err)
	}
	users := db.NewUserRepo(database)
	v := newOPDSBasicVerifier()
	h := opdsAuthWith(&testProvider{settings: settings}, users, auth.NewLoginLimiter(5, 15*time.Minute), v)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	return h, users, v
}

func basicReq(user, pass, ip string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/opds/images/1", nil)
	req.SetBasicAuth(user, pass)
	req.RemoteAddr = ip + ":5555"
	return req
}

// TestOPDS_ParallelCorrectBasicAllSucceed: a reader fetching covers in
// parallel with the right password must not be refused, and the burst costs
// one KDF run, not one per request.
func TestOPDS_ParallelCorrectBasicAllSucceed(t *testing.T) {
	h, users, v := opdsBasicHarness(t)
	hash, _ := auth.HashPassword("reader-password-1")
	if _, err := users.Create(context.Background(), "reader", hash); err != nil {
		t.Fatal(err)
	}
	const n = 12
	codes := burstCodes(n, func(int) *http.Request {
		return basicReq("reader", "reader-password-1", "203.0.113.90")
	}, h)
	if got := countCodes(codes); got[http.StatusOK] != n {
		t.Fatalf("codes %v; want all %d to be 200", got, n)
	}
	if runs := v.kdfRuns.Load(); runs != 1 {
		t.Errorf("KDF ran %d times for %d identical correct requests; want 1", runs, n)
	}
	// Warm: further requests are cache hits.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, basicReq("reader", "reader-password-1", "203.0.113.90"))
	if rec.Code != http.StatusOK || v.kdfRuns.Load() != 1 {
		t.Errorf("warm request: code %d, KDF runs %d; want 200 and still 1", rec.Code, v.kdfRuns.Load())
	}
}

// TestOPDS_ParallelCorrectBasicThroughFixture is the same burst through the
// production OPDSAuth constructor and real OPDS routes.
func TestOPDS_ParallelCorrectBasicThroughFixture(t *testing.T) {
	r, users, _, _ := opdsFixture(t)
	hash, _ := auth.HashPassword("reader-password-1")
	if _, err := users.Create(context.Background(), "reader", hash); err != nil {
		t.Fatal(err)
	}
	codes := burstCodes(12, func(int) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/opds/", nil)
		req.SetBasicAuth("reader", "reader-password-1")
		req.RemoteAddr = "203.0.113.91:5555"
		return req
	}, r)
	if got := countCodes(codes); got[http.StatusOK] != 12 {
		t.Fatalf("codes %v; want 12 x 200", got)
	}
}

// TestOPDS_IdenticalWrongBasicBurstBounded: 40 parallel requests with the
// same wrong password still run at most 5 verifications (they coalesce, so
// usually one), and none succeeds.
func TestOPDS_IdenticalWrongBasicBurstBounded(t *testing.T) {
	h, users, v := opdsBasicHarness(t)
	hash, _ := auth.HashPassword("reader-password-1")
	if _, err := users.Create(context.Background(), "reader", hash); err != nil {
		t.Fatal(err)
	}
	codes := burstCodes(40, func(int) *http.Request {
		return basicReq("reader", "not-the-password", "203.0.113.92")
	}, h)
	got := countCodes(codes)
	if got[http.StatusOK] != 0 || got[http.StatusUnauthorized]+got[http.StatusTooManyRequests] != 40 {
		t.Errorf("codes %v; want only 401 and 429", got)
	}
	if runs := v.kdfRuns.Load(); runs > 5 {
		t.Errorf("KDF ran %d times; want at most 5", runs)
	}
}

// TestOPDS_BasicCacheInvalidatedByPasswordChange: the cache key covers the
// stored hash, so the old password stops working the moment it changes.
func TestOPDS_BasicCacheInvalidatedByPasswordChange(t *testing.T) {
	h, users, _ := opdsBasicHarness(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("old-password-123")
	u, err := users.Create(ctx, "reader", hash)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(pass string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, basicReq("reader", pass, "203.0.113.93"))
		return rec.Code
	}
	if c := serve("old-password-123"); c != http.StatusOK {
		t.Fatalf("before change: %d; want 200", c)
	}
	newHash, _ := auth.HashPassword("new-password-456")
	if err := users.UpdatePassword(ctx, u.ID, newHash); err != nil {
		t.Fatal(err)
	}
	if c := serve("old-password-123"); c != http.StatusUnauthorized {
		t.Fatalf("old password after change: %d; want 401", c)
	}
	if c := serve("new-password-456"); c != http.StatusOK {
		t.Fatalf("new password: %d; want 200", c)
	}
}

// TestOPDS_AbandonedBasicRequestsDoNotLockOut: requests whose client went
// away before any verification ran are refunded, so they cannot use up the
// address's attempts.
func TestOPDS_AbandonedBasicRequestsDoNotLockOut(t *testing.T) {
	h, users, v := opdsBasicHarness(t)
	hash, _ := auth.HashPassword("reader-password-1")
	if _, err := users.Create(context.Background(), "reader", hash); err != nil {
		t.Fatal(err)
	}
	// Every verification gives up as if the client left while queued for a
	// KDF slot, after the attempt was reserved.
	v.verify = func(context.Context, string, string) (bool, error) { return false, context.Canceled }
	for i := range 10 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, basicReq("reader", "guess-"+strconv.Itoa(i), "203.0.113.94"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("abandoned request %d: %d; want 503", i, rec.Code)
		}
	}
	v.verify = auth.VerifyPasswordContext
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, basicReq("reader", "reader-password-1", "203.0.113.94"))
	if rec.Code != http.StatusOK {
		t.Fatalf("correct credentials after abandoned requests: %d; want 200", rec.Code)
	}
}

// TestLogin_AbandonedRequestsDoNotLockOut: the login twin. A queued request
// cancelled before its KDF ran must not count as a failed attempt.
func TestLogin_AbandonedRequestsDoNotLockOut(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t) // limiter allows 5 per window
	hash, _ := auth.HashPassword("correct-password")
	if _, err := users.Create(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	// Every verification gives up as if the client left while queued for a
	// KDF slot, after the attempt was reserved.
	h.verifyPassword = func(context.Context, string, string) (bool, error) { return false, context.Canceled }
	for i := range 10 {
		req := httptest.NewRequest(http.MethodPost, "/auth/login",
			jsonBody(t, loginRequest{Username: "admin", Password: "guess-" + strconv.Itoa(i)}))
		req.RemoteAddr = "203.0.113.95:4444"
		rec := httptest.NewRecorder()
		h.Login(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("abandoned request %d: %d; want 503", i, rec.Code)
		}
	}
	h.verifyPassword = auth.VerifyPasswordContext
	req := httptest.NewRequest(http.MethodPost, "/auth/login",
		jsonBody(t, loginRequest{Username: "admin", Password: "correct-password"}))
	req.RemoteAddr = "203.0.113.95:4444"
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct login after abandoned requests: %d; want 200", rec.Code)
	}
}

// TestSetup_ConcurrentCreatesExactlyOneUser pins first-run setup as a one-shot:
// simultaneous setup calls must create exactly one account, an admin, and
// every loser gets the same 409 a call after setup gets.
func TestSetup_ConcurrentCreatesExactlyOneUser(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t)
	const racers = 6
	codes := burstCodes(racers, func(i int) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/auth/setup",
			jsonBody(t, setupRequest{Username: "racer" + strconv.Itoa(i), Password: "long-enough-password"}))
	}, http.HandlerFunc(h.Setup))
	n, err := users.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("concurrent setup created %d users; want exactly 1 (codes %v)", n, countCodes(codes))
	}
	got := countCodes(codes)
	if got[http.StatusOK] != 1 || got[http.StatusConflict] != racers-1 {
		t.Errorf("codes %v; want one 200 and %d 409", got, racers-1)
	}
	admins, err := users.CountAdmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if admins != 1 {
		t.Errorf("admins=%d; want 1", admins)
	}
}

// revokedOPDSProvider is the OPDS testProvider with a logout denylist.
type revokedOPDSProvider struct {
	*testProvider
	revoked map[string]bool
}

func (p *revokedOPDSProvider) SessionRevoked(_ context.Context, h string) (bool, error) {
	return p.revoked[h], nil
}

// TestOPDS_RevokedSessionCookieRejected: OPDS verifies session cookies on its
// own path, so a signed out cookie must fail there too, not only in
// auth.Middleware.
func TestOPDS_RevokedSessionCookieRejected(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	settings := db.NewSettingsRepo(database)
	users := db.NewUserRepo(database)
	secret := "abcdefghijklmnopqrstuvwxyz012345"
	if err := settings.Set(ctx, SettingAuthSessionSecret, secret); err != nil {
		t.Fatal(err)
	}
	u, err := users.Create(ctx, "reader", "unused-hash")
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := auth.SignSessionWithEpoch([]byte(secret), u.ID, u.SessionEpoch, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	p := &revokedOPDSProvider{testProvider: &testProvider{settings: settings}, revoked: map[string]bool{}}
	h := OPDSAuth(p, users, auth.NewLoginLimiter(5, time.Minute))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	serve := func() int {
		req := httptest.NewRequest(http.MethodGet, "/opds/", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := serve(); code != http.StatusOK {
		t.Fatalf("valid cookie: status %d; want 200", code)
	}
	p.revoked[auth.SessionTokenHash(cookie)] = true
	if code := serve(); code != http.StatusUnauthorized {
		t.Fatalf("revoked cookie: status %d; want 401", code)
	}
}

// TestLogout_RevokesOnlyThatSession pins server side logout: a cookie captured
// before logout must stop authenticating once its holder logs out, while a
// second session for the same user (another device) keeps working. Clearing
// the cookie in the browser alone left a copied cookie valid until expiry.
func TestLogout_RevokesOnlyThatSession(t *testing.T) {
	h, users, settings, ctx := newAuthFixture(t)
	hash, _ := auth.HashPassword("alice-password-1")
	if _, err := users.Create(ctx, "alice", hash); err != nil {
		t.Fatal(err)
	}
	laptop := loginAndGetCookie(t, h, "alice", "alice-password-1")
	phone := loginAndGetCookie(t, h, "alice", "alice-password-1")

	logoutReq := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	logoutReq.AddCookie(laptop)
	logoutRec := httptest.NewRecorder()
	h.Logout(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("logout: status=%d", logoutRec.Code)
	}

	provider := &epochProvider{users: users, secret: []byte(must(settings.Get(ctx, SettingAuthSessionSecret)).Value), h: h}
	authed := func(c *http.Cookie) bool {
		var reached bool
		chain := auth.Middleware(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = auth.UserIDFromContext(r.Context()) != 0
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/author", nil)
		req.AddCookie(c)
		chain.ServeHTTP(httptest.NewRecorder(), req)
		return reached
	}
	if authed(laptop) {
		t.Error("cookie captured before logout still authenticates after logout")
	}
	if !authed(phone) {
		t.Error("logging out one session signed out the user's other session too")
	}
}
