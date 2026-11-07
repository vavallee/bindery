package auth

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestLoginAddressGroup(t *testing.T) {
	cases := map[string]string{
		"203.0.113.9":                 "203.0.113.9",
		"::ffff:203.0.113.9":          "203.0.113.9",
		"2001:db8:1:2::1":             "2001:db8:1:2::/64",
		"[2001:db8:1:2:ffff:ffff::9]": "2001:db8:1:2::/64",
		"not-an-address":              "not-an-address",
	}
	for in, want := range cases {
		if got := LoginAddressGroup(in); got != want {
			t.Errorf("LoginAddressGroup(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestLoginBucketsAreIndependentPerAccount(t *testing.T) {
	l := NewLoginLimiter(3, time.Minute)
	for i := 0; i < 2; i++ {
		l.AcquireLogin("203.0.113.9", "admin")
	}
	l.AcquireLogin("203.0.113.9", "reader")
	l.LoginSucceeded("203.0.113.9", "reader")
	if !l.AcquireLogin("203.0.113.9", "admin") {
		t.Fatal("third attempt should be allowed")
	}
	if l.AcquireLogin("203.0.113.9", "Admin ") {
		t.Error("fourth attempt at the same account from the same address must be refused")
	}
}

func TestReleaseLoginRefundsReservation(t *testing.T) {
	l := NewLoginLimiter(1, time.Minute)
	if !l.AcquireLogin("203.0.113.9", "admin") {
		t.Fatal("first attempt refused")
	}
	l.ReleaseLogin("203.0.113.9", "admin")
	if !l.AcquireLogin("203.0.113.9", "admin") {
		t.Error("refunded attempt must not count")
	}
}

func TestManyWrongGuessesDoNotBlockCorrectPasswordFromFreshAddress(t *testing.T) {
	l := NewLoginLimiter(5, time.Minute)
	for i := 0; i < 20; i++ {
		ip := fmt.Sprintf("198.51.100.%d", i+1)
		for j := 0; j < 5; j++ {
			l.AcquireLogin(ip, "admin")
		}
	}
	const fresh = "203.0.113.77"
	if !l.AllowLogin(fresh, "admin") {
		t.Fatal("AllowLogin refused a fresh address")
	}
	if !l.AcquireLogin(fresh, "admin") {
		t.Fatal("a fresh address was refused because other addresses guessed wrong")
	}
	l.LoginSucceeded(fresh, "admin")
}

func TestAddressBucketStillBoundsAccountsTriedFromOneAddress(t *testing.T) {
	l := NewLoginLimiter(1, time.Minute)
	for _, u := range []string{"x1", "x2", "x3", "x4"} {
		if !l.AcquireLogin("203.0.113.9", u) {
			t.Fatalf("attempt for %s refused", u)
		}
	}
	if l.AcquireLogin("203.0.113.9", "admin") {
		t.Error("address bucket must refuse once the address total is spent")
	}
}

func TestKeyCapRefusesNewKeysOnly(t *testing.T) {
	l := NewLoginLimiter(5, time.Minute)
	l.maxKeys = 4
	if !l.AcquireLogin("192.0.2.1", "a") || !l.AcquireLogin("192.0.2.1", "b") || !l.AcquireLogin("192.0.2.1", "c") {
		t.Fatal("attempts under the cap refused")
	}
	if len(l.events) != 4 {
		t.Fatalf("key count %d; want 4", len(l.events))
	}
	if !l.AcquireLogin("192.0.2.1", "d") {
		t.Error("an attempt needing new keys at the cap must make room, not be refused")
	}
	if !l.AcquireLogin("192.0.2.1", "d") {
		t.Error("an already tracked bucket must keep working at the cap")
	}
	if len(l.events) > 4 {
		t.Errorf("key count %d exceeds the cap", len(l.events))
	}
}

func TestKeyCapPurgesExpiredBucketsFirst(t *testing.T) {
	l := NewLoginLimiter(5, 30*time.Millisecond)
	l.maxKeys = 4
	l.AcquireLogin("192.0.2.1", "a")
	l.AcquireLogin("192.0.2.1", "b")
	time.Sleep(60 * time.Millisecond)
	if !l.AcquireLogin("192.0.2.2", "c") {
		t.Error("expired buckets must be reclaimed before refusing")
	}
	if len(l.events) != 2 {
		t.Errorf("key count %d; want 2 after the expired buckets were purged", len(l.events))
	}
}

// floodKeys creates buckets for n distinct addresses with one account each.
func floodKeys(l *LoginLimiter, n int) {
	for i := 0; i < n; i++ {
		l.AcquireLogin(fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256), fmt.Sprintf("flood%d", i))
	}
}

func TestFullKeyMapStillServesNewAddresses(t *testing.T) {
	l := NewLoginLimiter(5, time.Hour)
	l.maxKeys = 300
	floodKeys(l, 400)
	if len(l.events) > 300 {
		t.Fatalf("key count %d exceeds the cap", len(l.events))
	}
	for i := 0; i < 50; i++ {
		if !l.AcquireLogin(fmt.Sprintf("203.0.113.%d", i), fmt.Sprintf("user%d", i)) {
			t.Fatalf("fresh address %d refused once the key map was full", i)
		}
	}
	if len(l.events) > 300 {
		t.Errorf("key count %d exceeds the cap", len(l.events))
	}
}

func TestOneAddressGroupCannotCreateUnboundedAccountKeys(t *testing.T) {
	l := NewLoginLimiter(100, time.Hour) // address allowance 400, above the key bound
	granted := 0
	for i := 0; i < 200; i++ {
		if l.AcquireLogin("192.0.2.7", fmt.Sprintf("name%d", i)) {
			granted++
		}
	}
	if granted != maxPairKeysPerGroup {
		t.Errorf("one address group created %d account keys; want %d", granted, maxPairKeysPerGroup)
	}
	if !l.AcquireLogin("192.0.2.7", "name0") {
		t.Error("an account that is already tracked must keep working")
	}
	if !l.AcquireLogin("192.0.2.8", "fresh") {
		t.Error("another address group must be unaffected")
	}
}

func TestPurgeIsThrottledWhenKeyMapIsFull(t *testing.T) {
	l := NewLoginLimiter(5, time.Hour)
	l.maxKeys = 300
	floodKeys(l, 400)
	before := l.purges
	for i := 0; i < 200; i++ {
		l.AcquireLogin(fmt.Sprintf("198.18.%d.%d", i/256, i%256), "someone")
	}
	if got := l.purges - before; got > 1 {
		t.Errorf("full map walked %d times for 200 attempts; want at most 1", got)
	}
}

func TestHugeUsernameIsTruncatedCheaply(t *testing.T) {
	big := strings.Repeat("É", 512*1024) // 1 MiB
	prefix := strings.Repeat("é", 64)    // 128 bytes
	l := NewLoginLimiter(5, time.Minute)
	if l.loginBuckets("192.0.2.1", big)[0].key != l.loginBuckets("192.0.2.1", prefix)[0].key {
		t.Error("a huge name must share the bucket of its 128 byte prefix")
	}
	three := strings.Repeat("€", 100) // 3 byte runes, 128 is mid rune
	for _, b := range l.loginBuckets("192.0.2.1", three) {
		if !utf8.ValidString(b.key) {
			t.Fatalf("key %q is not valid UTF-8", b.key)
		}
	}
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	for i := 0; i < 50; i++ {
		l.AllowLogin("192.0.2.1", big)
	}
	runtime.ReadMemStats(&m1)
	if d := m1.TotalAlloc - m0.TotalAlloc; d > 1<<20 {
		t.Errorf("50 checks of a 1 MiB name allocated %d bytes; the name must be cut before it is processed", d)
	}
}

func TestInvalidUTF8NamesDoNotCollapseToEmptyName(t *testing.T) {
	l := NewLoginLimiter(5, time.Minute)
	empty := l.loginBuckets("192.0.2.1", "")[0].key
	for _, name := range []string{strings.Repeat("\x80", 200), "a" + strings.Repeat("\x80", 200)} {
		if got := l.loginBuckets("192.0.2.1", name)[0].key; got == empty {
			t.Errorf("%.8q... shares the empty name bucket", name)
		}
	}
	a := l.loginBuckets("192.0.2.1", strings.Repeat("\x80", 200))[0].key
	b := l.loginBuckets("192.0.2.1", "a"+strings.Repeat("\x80", 200))[0].key
	if a == b {
		t.Error("different invalid names should not share a bucket")
	}
}

func TestValidMultiByteNamesStillCutOnRuneBoundary(t *testing.T) {
	l := NewLoginLimiter(5, time.Minute)
	for _, r := range []string{"é", "€", "😀"} {
		key := l.loginBuckets("192.0.2.1", strings.Repeat(r, 100))[0].key
		name := key[strings.Index(key, "\x00")+1:]
		if !utf8.ValidString(name) || len(name) > maxLoginNameLen || len(name) < maxLoginNameLen-3 {
			t.Errorf("%q: cut gave %d bytes, valid=%v", r, len(name), utf8.ValidString(name))
		}
	}
}

func TestSuccessDoesNotRefundMoreThanItsOwnAttempt(t *testing.T) {
	l := NewLoginLimiter(5, time.Minute)
	l.AcquireLogin("203.0.113.9", "reader")
	l.AcquireLogin("203.0.113.9", "admin")
	l.AcquireLogin("203.0.113.9", "admin")
	l.LoginSucceeded("203.0.113.9", "admin")
	addr := l.loginBuckets("203.0.113.9", "admin")[1].key
	if got := len(l.events[addr]); got != 2 {
		t.Errorf("address bucket holds %d; want 2 (3 reserved, exactly one refunded)", got)
	}
}

func TestLoginTrackingIsRaceFree(t *testing.T) {
	l := NewLoginLimiter(5, time.Minute)
	var wg sync.WaitGroup
	var granted atomic.Int32
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			ip := fmt.Sprintf("192.0.2.%d", g%20)
			user := fmt.Sprintf("u%d", g%3)
			for i := 0; i < 20; i++ {
				if l.AllowLogin(ip, user) && l.AcquireLogin(ip, user) {
					granted.Add(1)
					switch i % 3 {
					case 0:
						l.LoginSucceeded(ip, user)
					case 1:
						l.ReleaseLogin(ip, user)
					}
				}
			}
		}(g)
	}
	wg.Wait()
	if granted.Load() == 0 {
		t.Error("nothing was granted")
	}
}

func TestConcurrentAcquireGrantsExactlyTheLimit(t *testing.T) {
	l := NewLoginLimiter(5, time.Minute)
	var wg sync.WaitGroup
	var granted atomic.Int32
	for g := 0; g < 200; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.AcquireLogin("203.0.113.9", "admin") {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	if granted.Load() != 5 {
		t.Errorf("granted %d; want 5", granted.Load())
	}
}

func TestSuccessfulLoginsDoNotUseUpTheAccountCap(t *testing.T) {
	l := NewLoginLimiter(5, time.Hour)
	for i := 0; i < maxPairKeysPerGroup*2; i++ {
		if !l.AcquireLogin("192.0.2.30", "admin") {
			t.Fatalf("sign in %d refused for one account from one address", i+1)
		}
		l.LoginSucceeded("192.0.2.30", "admin")
	}
	for i := 0; i < maxPairKeysPerGroup*2; i++ {
		if !l.AcquireLogin("192.0.2.31", "admin") {
			t.Fatalf("released attempt %d refused", i+1)
		}
		l.ReleaseLogin("192.0.2.31", "admin")
	}
}

func TestAccountCapStillBindsForFailingNames(t *testing.T) {
	l := NewLoginLimiter(100, time.Hour)
	granted := 0
	for i := 0; i < 100; i++ {
		if l.AcquireLogin("192.0.2.32", fmt.Sprintf("guess%d", i)) {
			granted++
		}
	}
	if granted != maxPairKeysPerGroup {
		t.Errorf("granted %d failing names; want %d", granted, maxPairKeysPerGroup)
	}
}
