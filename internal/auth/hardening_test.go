package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLoginLimiterAcquireIsAtomic: however many goroutines race Acquire for
// one address, exactly max of them get through.
func TestLoginLimiterAcquireIsAtomic(t *testing.T) {
	lim := NewLoginLimiter(5, time.Minute)
	var granted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if lim.Acquire("198.51.100.1") {
				granted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := granted.Load(); got != 5 {
		t.Fatalf("Acquire granted %d of 100; want exactly 5", got)
	}
	if lim.Allow("198.51.100.1") {
		t.Error("Allow should refuse once Acquire has used up the window")
	}
	if !lim.Acquire("198.51.100.2") {
		t.Error("another address must have its own budget")
	}
	lim.Reset("198.51.100.1")
	if !lim.Acquire("198.51.100.1") {
		t.Error("Reset should clear the reservations")
	}
}

// TestVerifyPasswordContext_WaitsForKDFSlot: with every KDF slot taken, a
// verification waits and gives up with the context's error instead of running
// another 64 MiB KDF, and runs normally once a slot frees.
func TestVerifyPasswordContext_WaitsForKDFSlot(t *testing.T) {
	hash, err := HashPassword("right-password")
	if err != nil {
		t.Fatal(err)
	}
	var releases []func()
	for range cap(kdfSlots) {
		rel, err := acquireKDF(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, rel)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ok, err := VerifyPasswordContext(ctx, "right-password", hash)
	if ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("with all slots taken got (%v, %v); want (false, deadline exceeded)", ok, err)
	}
	if _, err := HashPasswordContext(ctx, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HashPasswordContext with all slots taken: err=%v; want deadline exceeded", err)
	}
	for _, rel := range releases {
		rel()
	}
	ok, err = VerifyPasswordContext(context.Background(), "right-password", hash)
	if !ok || err != nil {
		t.Fatalf("after release got (%v, %v); want (true, nil)", ok, err)
	}
}

func TestKDFConcurrencyBounds(t *testing.T) {
	if n := kdfConcurrency(); n < 2 || n > 4 {
		t.Fatalf("kdfConcurrency() = %d; want within [2, 4]", n)
	}
}

// TestSession_V4TokensAreDistinct: two cookies for the same user, epoch and
// expiry differ, so revoking one by hash cannot revoke the other.
func TestSession_V4TokensAreDistinct(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	a, err := SignSessionWithEpoch(testSecret32, 1, 1, exp)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SignSessionWithEpoch(testSecret32, 1, 1, exp)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || SessionTokenHash(a) == SessionTokenHash(b) {
		t.Fatal("two logins in the same second minted the same token")
	}
	uid, gotExp, err := VerifySessionMultiWithExpiry([][]byte{testSecret32}, a)
	if err != nil || uid != 1 || gotExp.Unix() != exp.Unix() {
		t.Fatalf("VerifySessionMultiWithExpiry = (%d, %v, %v); want (1, %v, nil)", uid, gotExp, err, exp)
	}
}

// TestSession_V3StillVerifies: cookies minted before v4 keep working after an
// upgrade, epoch included.
func TestSession_V3StillVerifies(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	payload := fmt.Sprintf("v3.%s.%d.%d.%d", keyID(testSecret32), int64(42), int64(3), exp)
	v3 := payload + "." + base64.RawURLEncoding.EncodeToString(hmacSum(testSecret32, payload))
	uid, epoch, err := VerifySessionWithEpoch(testSecret32, v3)
	if err != nil || uid != 42 || epoch != 3 {
		t.Fatalf("v3 verify = (%d, %d, %v); want (42, 3, nil)", uid, epoch, err)
	}
}

// TestMiddleware_RejectsRevokedSession: a correctly signed cookie with the
// right epoch is refused once its hash is on the denylist, and a failed
// denylist lookup is a 500, not a silent logout or a pass.
func TestMiddleware_RejectsRevokedSession(t *testing.T) {
	cookie, err := SignSession(testSecret32, 5, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{mode: ModeEnabled, secret: testSecret32}
	serve := func() (int, bool) {
		var reached bool
		h := Middleware(p)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = UserIDFromContext(r.Context()) == 5
		}))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/author", nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookie})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, reached
	}
	if _, reached := serve(); !reached {
		t.Fatal("valid cookie should authenticate before revocation")
	}
	p.revoked = map[string]bool{SessionTokenHash(cookie): true}
	if code, reached := serve(); reached || code != http.StatusUnauthorized {
		t.Fatalf("revoked cookie: code=%d reached=%v; want 401 and not reached", code, reached)
	}
	p.revoked = nil
	p.revokedErr = errors.New("db down")
	if code, reached := serve(); reached || code != http.StatusInternalServerError {
		t.Fatalf("lookup failure: code=%d reached=%v; want 500 and not reached", code, reached)
	}
}
