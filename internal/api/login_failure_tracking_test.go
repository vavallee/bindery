package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
)

// loginFixture seeds an admin and a second ordinary account and returns a
// login helper that posts to the JSON login handler from a given address.
func loginFixture(t *testing.T) func(addr, user, pw string) int {
	t.Helper()
	h, users, _, ctx := newAuthFixture(t)
	adminHash, _ := auth.HashPassword("correct-horse-admin")
	if _, err := users.CreateFirstAdmin(ctx, "admin", adminHash); err != nil {
		t.Fatal(err)
	}
	userHash, _ := auth.HashPassword("second-account-password")
	if _, err := users.Create(ctx, "reader", userHash); err != nil {
		t.Fatal(err)
	}
	return func(addr, user, pw string) int {
		req := httptest.NewRequest(http.MethodPost, "/auth/login",
			jsonBody(t, map[string]any{"username": user, "password": pw}))
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		h.Login(rec, req)
		return rec.Code
	}
}

func TestLoginFailuresTrackedPerAccount_InterleavedSuccessDoesNotClear(t *testing.T) {
	login := loginFixture(t)
	verified := 0
	for i := 0; i < 32; i++ {
		if login("203.0.113.9:4444", "admin", "wrong") == http.StatusUnauthorized {
			verified++
		}
		if code := login("203.0.113.9:4444", "reader", "second-account-password"); code != http.StatusOK {
			t.Fatalf("round %d: own login returned %d", i, code)
		}
	}
	if verified > 5 {
		t.Errorf("%d wrong guesses at one account were verified from one address; want at most 5", verified)
	}
}

func TestLoginSuccessForOneAccountKeepsOtherAccountFailures(t *testing.T) {
	login := loginFixture(t)
	for i := 0; i < 4; i++ {
		login("203.0.113.9:1", "admin", "wrong")
	}
	if code := login("203.0.113.9:1", "reader", "second-account-password"); code != http.StatusOK {
		t.Fatalf("reader login: %d", code)
	}
	if code := login("203.0.113.9:1", "admin", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("fifth failure: %d", code)
	}
	if code := login("203.0.113.9:1", "admin", "correct-horse-admin"); code != http.StatusTooManyRequests {
		t.Errorf("after five failures the address must be held for that account, got %d", code)
	}
}

func TestLoginManyWrongGuessesFromOtherAddressesDoNotBlockFreshAddress(t *testing.T) {
	login := loginFixture(t)
	for i := 0; i < 30; i++ {
		login(fmt.Sprintf("198.51.100.%d:1", i+1), "admin", "wrong")
	}
	if code := login("198.51.100.200:1", "admin", "correct-horse-admin"); code != http.StatusOK {
		t.Errorf("correct password from a fresh address got %d; want 200", code)
	}
}

func TestLoginAccountNameIsNormalisedForTracking(t *testing.T) {
	login := loginFixture(t)
	names := []string{"admin", "Admin", " ADMIN ", "admin", "aDmIn"}
	for _, n := range names {
		login("203.0.113.9:1", n, "wrong")
	}
	if code := login("203.0.113.9:1", "admin", "wrong"); code != http.StatusTooManyRequests {
		t.Errorf("case and spacing variants must share one bucket, got %d", code)
	}
}

func TestLoginIPv6AddressesInOneSlash64ShareFailures(t *testing.T) {
	login := loginFixture(t)
	for i := 0; i < 5; i++ {
		login(fmt.Sprintf("[2001:db8:1:2::%x]:1", i+1), "admin", "wrong")
	}
	if code := login("[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:1", "admin", "wrong"); code != http.StatusTooManyRequests {
		t.Errorf("same /64 got %d; want 429", code)
	}
	if code := login("[2001:db8:1:3::1]:1", "admin", "wrong"); code != http.StatusUnauthorized {
		t.Errorf("a different /64 got %d; want 401", code)
	}
}

func TestLoginTwoTyposThenSuccessIsNotHeld(t *testing.T) {
	login := loginFixture(t)
	for i := 0; i < 2; i++ {
		if code := login("203.0.113.9:1", "admin", "typo"); code != http.StatusUnauthorized {
			t.Fatalf("typo %d: %d", i, code)
		}
	}
	for i := 0; i < 3; i++ {
		if code := login("203.0.113.9:1", "admin", "correct-horse-admin"); code != http.StatusOK {
			t.Fatalf("login %d after typos: %d", i, code)
		}
	}
}

func TestOPDSBasicFailuresTrackedPerAccount(t *testing.T) {
	h, users, _ := opdsBasicHarness(t)
	for name, pw := range map[string]string{"owner": "owner-password-1", "reader": "reader-password-1"} {
		hash, _ := auth.HashPassword(pw)
		if _, err := users.Create(context.Background(), name, hash); err != nil {
			t.Fatal(err)
		}
	}
	do := func(ip, user, pw string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, basicReq(user, pw, ip))
		return rec.Code
	}
	verified := 0
	for i := 0; i < 32; i++ {
		if do("203.0.113.50", "owner", fmt.Sprintf("wrong-%d", i)) == http.StatusUnauthorized {
			verified++
		}
		if code := do("203.0.113.50", "reader", "reader-password-1"); code != http.StatusOK {
			t.Fatalf("round %d: reader got %d", i, code)
		}
	}
	if verified > 5 {
		t.Errorf("%d wrong guesses verified from one address; want at most 5", verified)
	}
}

func TestOPDSBasicManyWrongGuessesDoNotBlockFreshAddress(t *testing.T) {
	h, users, _ := opdsBasicHarness(t)
	hash, _ := auth.HashPassword("owner-password-1")
	if _, err := users.Create(context.Background(), "owner", hash); err != nil {
		t.Fatal(err)
	}
	do := func(ip, pw string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, basicReq("owner", pw, ip))
		return rec.Code
	}
	for i := 0; i < 30; i++ {
		do(fmt.Sprintf("198.51.100.%d", i+1), fmt.Sprintf("wrong-%d", i))
	}
	if code := do("198.51.100.200", "owner-password-1"); code != http.StatusOK {
		t.Errorf("correct password from a fresh address got %d; want 200", code)
	}
}
