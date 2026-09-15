package hardcover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
)

func testQuota(t *testing.T, limit int) *Quota {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	q := NewQuota(db.NewSettingsRepo(database))
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	q.now = func() time.Time { return now }
	if err := q.settings.Set(context.Background(), SettingDailyRequestLimit, fmt.Sprint(limit)); err != nil {
		t.Fatal(err)
	}
	return q
}

func quotaHeaders(limit, remaining, reset int) http.Header {
	h := make(http.Header)
	h.Set("RateLimit-Policy", fmt.Sprintf(`"Free";q=60;w=60;burst=10, "daily";q=%d;w=86400`, limit))
	h.Set("RateLimit", fmt.Sprintf(`"Free";r=8;t=42, "daily";r=%d;t=%d`, remaining, reset))
	return h
}

func TestQuotaDetectionPlansAndReset(t *testing.T) {
	for _, limit := range []int{5000, 50000, 100000} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			q := testQuota(t, 5000)
			ctx := context.Background()
			calls := 0
			request := func([]byte) (int, http.Header, []byte, error) {
				calls++
				return 200, quotaHeaders(limit, 0, 3600), []byte(`{}`), nil
			}
			if _, _, _, err := q.attempt(ctx, "test", nil, 5000, request); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := q.attempt(ctx, "test", nil, 5000, request); !errors.Is(err, metadata.ErrProviderDeferred) {
				t.Fatalf("expected deferred: %v", err)
			}
			restarted := NewQuota(q.settings)
			now := q.now()
			restarted.now = func() time.Time { return now }
			if _, _, _, err := restarted.attempt(ctx, "test", nil, 5000, request); !errors.Is(err, ErrRateLimited) {
				t.Fatalf("restart lost hold: %v", err)
			}
			if calls != 1 {
				t.Fatalf("spent %d requests", calls)
			}
			now = now.Add(time.Hour)
			if _, _, _, err := restarted.attempt(ctx, "test", nil, 5000, request); err != nil {
				t.Fatalf("reset: %v", err)
			}
			if calls != 2 {
				t.Fatal(calls)
			}
		})
	}
}

func TestQuotaConcurrentFallbackAndPlanChange(t *testing.T) {
	q := testQuota(t, 10)
	ctx := context.Background()
	var requests atomic.Int32
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "BinderyQuotaIdentity") {
			return gqlResponse(t, 200, `{"data":{"me":[{"id":7}]}}`), nil
		}
		return gqlResponse(t, 200, `{"data":{}}`), nil
	}).WithQuota(q)
	var wg sync.WaitGroup
	for range 25 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out any
			err := client.query(ctx, "query { books { id } }", nil, &out)
			if err != nil && !errors.Is(err, metadata.ErrProviderDeferred) {
				t.Errorf("query: %v", err)
			}
		}()
	}
	wg.Wait()
	if requests.Load() != 10 {
		t.Fatalf("overspent or lost attempts: %d", requests.Load())
	}
	status, err := q.Status(ctx, mockClientToken)
	if err != nil || !status.Deferred || status.Remaining != 0 {
		t.Fatalf("status: %+v %v", status, err)
	}
	if err := q.settings.Set(ctx, SettingDailyRequestLimit, "20"); err != nil {
		t.Fatal(err)
	}
	var out any
	if err := client.query(ctx, "query { books { id } }", nil, &out); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 11 {
		t.Fatal(requests.Load())
	}
	if err := q.settings.Set(ctx, SettingDailyRequestLimit, "5"); err != nil {
		t.Fatal(err)
	}
	if err := client.query(ctx, "query { books { id } }", nil, &out); !errors.Is(err, metadata.ErrProviderDeferred) {
		t.Fatal(err)
	}
}

func TestQuotaAccountKeysAndIsolation(t *testing.T) {
	q := testQuota(t, 5)
	ctx := context.Background()
	calls := 0
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		calls++
		id := 7
		if r.Header.Get("Authorization") == "Bearer other" {
			id = 8
		}
		return gqlResponse(t, 200, fmt.Sprintf(`{"data":{"me":[{"id":%d}]}}`, id)), nil
	}).WithQuota(q)
	var out any
	for _, token := range []string{"one", "Bearer two", "one"} {
		if err := c.WithToken(token).query(ctx, "query { me { id } }", nil, &out); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 5 {
		t.Fatal(calls)
	}
	if err := c.WithToken("two").query(ctx, "query { me { id } }", nil, &out); !errors.Is(err, metadata.ErrProviderDeferred) {
		t.Fatal(err)
	}
	if calls != 5 {
		t.Fatal(calls)
	}
	if err := c.WithToken("other").query(ctx, "query { me { id } }", nil, &out); err != nil {
		t.Fatal(err)
	}
	if calls != 7 {
		t.Fatal(calls)
	}
	rows, err := q.settings.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rows {
		if strings.Contains(s.Key, "Bearer") || strings.Contains(s.Value, "Bearer") {
			t.Fatal("credential leaked")
		}
	}
}

func TestQuotaLongRetryAfterAndBackgroundReserve(t *testing.T) {
	q := testQuota(t, 10)
	ctx := context.Background()
	calls := 0
	request := func([]byte) (int, http.Header, []byte, error) {
		calls++
		h := make(http.Header)
		h.Set("Retry-After", "7200")
		return 429, h, nil, nil
	}
	_, _, _, err := q.attempt(ctx, "test", nil, 10, request)
	var deferred *QuotaDeferredError
	if !errors.As(err, &deferred) || !deferred.Status.NextEligible.Equal(q.now().Add(2*time.Hour)) {
		t.Fatalf("hold: %v", err)
	}
	if _, _, _, err := q.attempt(ctx, "test", nil, 10, request); !errors.Is(err, ErrRateLimited) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	s := quotaState{Allowance: 50000, Remaining: 5000, Reset: q.now().Add(time.Hour)}
	if got := q.status(&s, 10, true); !got.Deferred || got.Reserve != 5000 {
		t.Fatalf("reserve: %+v", got)
	}
	if got := q.status(&s, 10, false); got.Deferred {
		t.Fatalf("interactive blocked: %+v", got)
	}
}

func TestQuotaMalformedHeadersAndUpgrade(t *testing.T) {
	q := testQuota(t, 5000)
	s := quotaState{}
	q.observe(&s, quotaHeaders(5000, 4000, 3600), 200)
	q.observe(&s, quotaHeaders(50000, 49000, 3500), 200)
	if got := q.status(&s, 5000, false); got.Allowance != 50000 || got.Remaining != 49000 {
		t.Fatalf("upgrade: %+v", got)
	}
	q.observe(&s, quotaHeaders(5000, 0, 3400), 200)
	if got := q.status(&s, 5000, false); !got.Deferred {
		t.Fatalf("downgrade: %+v", got)
	}
	for _, value := range []string{`"daily";r=-1;t=50`, `"daily";r=0;t=999999999999999999999`, `"daily";r=0;r=2;t=1`, `"Free";r=0;t=10`} {
		h := make(http.Header)
		h.Set("RateLimit", value)
		before := s.Reset
		q.observe(&s, h, 200)
		if !s.Reset.Equal(before) {
			t.Fatalf("accepted %s", value)
		}
	}
	for _, value := range []string{"99999999999999999999", "-3", "0"} {
		if _, ok := parseRetryAfterUncapped(value, q.now()); ok {
			t.Fatal(value)
		}
	}
}

func TestQuotaStorageFailureStopsTraffic(t *testing.T) {
	q := testQuota(t, 10)
	ctx := context.Background()
	key := "usage.key." + quotaHash("secret")
	if err := q.settings.Set(ctx, quotaPrefix+key, "corrupt"); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := q.do(ctx, "secret", "", nil, func([]byte) (int, http.Header, []byte, error) {
		t.Fatal("sent with corrupt accounting")
		return 0, nil, nil, nil
	})
	if err == nil {
		t.Fatal("silent storage failure")
	}
}

func TestQuotaRetriesCountAndCacheHitsDoNot(t *testing.T) {
	q := testQuota(t, 20)
	ctx := context.Background()
	calls := 0
	fail := true
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "BinderyQuotaIdentity") {
			return gqlResponse(t, 200, `{"data":{"me":[{"id":1}]}}`), nil
		}
		if fail {
			fail = false
			return nil, errors.New("temporary network failure")
		}
		return gqlResponse(t, 200, `{"data":{"authors":[{"id":123,"name":"Author"}]}}`), nil
	}).WithQuota(q)
	agg := metadata.NewAggregator(c)
	for range 2 {
		if _, err := agg.GetAuthor(ctx, "hc:123"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("expected identity + failed request + retry, no cache-hit request: %d", calls)
	}
	status, err := q.Status(ctx, mockClientToken)
	if err != nil || status.Remaining != 17 {
		t.Fatalf("accounting: %+v %v", status, err)
	}
}

func TestQuotaPartialPolicyDowngradeCannotKeepOldRemaining(t *testing.T) {
	q := testQuota(t, 5000)
	s := quotaState{}
	q.observe(&s, quotaHeaders(50000, 40000, 3600), 200)
	h := make(http.Header)
	h.Set("RateLimit-Policy", `"daily";q=5000;w=86400`)
	q.observe(&s, h, 200)
	if got := q.status(&s, 5000, false); !got.Deferred || got.Remaining != 0 {
		t.Fatalf("downgrade kept stale remaining: %+v", got)
	}
}

func TestQuotaUnavailableIdentityDoesNotProbeEveryRequest(t *testing.T) {
	q := testQuota(t, 10)
	calls := 0
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "BinderyQuotaIdentity") {
			return gqlResponse(t, 403, `{"error":"insufficient_scope"}`), nil
		}
		return gqlResponse(t, 200, `{"data":{}}`), nil
	}).WithQuota(q)
	var out any
	for range 2 {
		if err := c.query(context.Background(), "query { books { id } }", nil, &out); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("identity failure caused repeated probing: %d", calls)
	}
	status, err := q.Status(context.Background(), mockClientToken)
	if err != nil || status.Remaining != 7 {
		t.Fatalf("isolated accounting: %+v %v", status, err)
	}
}

func TestQuotaFailedCooldownWriteBlocksUntilPersisted(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	q := NewQuota(db.NewSettingsRepo(database))
	ctx := context.Background()
	token := "storage-test"
	if err := q.save(ctx, "key."+quotaHash(token), quotaIdentity{Account: "test"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	request := func([]byte) (int, http.Header, []byte, error) {
		calls++
		_, err := database.Exec(`CREATE TRIGGER quota_write_failure BEFORE UPDATE ON settings BEGIN SELECT RAISE(FAIL, 'test write failure'); END`)
		if err != nil {
			t.Fatal(err)
		}
		return 429, quotaHeaders(5000, 0, 3600), nil, nil
	}
	if _, _, _, err := q.do(ctx, token, "", nil, request); !errors.Is(err, errQuotaStorage) {
		t.Fatalf("expected storage error: %v", err)
	}
	if _, _, _, err := q.do(ctx, token, "", nil, request); !errors.Is(err, errQuotaStorage) {
		t.Fatalf("expected storage hold: %v", err)
	}
	if _, err := database.Exec(`DROP TRIGGER quota_write_failure`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := q.do(ctx, token, "", nil, request); !errors.Is(err, metadata.ErrProviderDeferred) {
		t.Fatalf("lost cooldown on recovery: %v", err)
	}
	if calls != 1 {
		t.Fatalf("sent during storage failure: %d", calls)
	}
}

func TestQuotaFallbackWindowAndHeaderLines(t *testing.T) {
	q := testQuota(t, 10)
	now := q.now()
	s := quotaState{Hours: []quotaHour{{Start: now.Unix(), Used: 10}}}
	q.now = func() time.Time { return now.Add(24 * time.Hour) }
	if got := q.status(&s, 10, false); !got.Deferred {
		t.Fatal("released conservative hourly bucket early")
	}
	q.now = func() time.Time { return now.Add(25 * time.Hour) }
	if got := q.status(&s, 10, false); got.Deferred || got.Remaining != 10 {
		t.Fatalf("did not expire local count: %+v", got)
	}
	h := make(http.Header)
	h.Add("RateLimit-Policy", `"Free";q=60;w=60;burst=10`)
	h.Add("RateLimit-Policy", `"daily";q=5000;w=86400`)
	h.Add("RateLimit", `"Free";r=1;t=30`)
	h.Add("RateLimit", `"daily";r=4321;t=3600`)
	q.observe(&s, h, 200)
	if got := q.status(&s, 10, false); got.Source != "upstream" || got.Remaining != 4321 {
		t.Fatalf("multiple header lines: %+v", got)
	}
}

func TestQuotaAccountAdoptionChargesDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name             string
		limit, remaining int
		reset            time.Duration
		headers          http.Header
		wantCalls        int
	}{
		{"active window without headers", 5000, 1, time.Hour, nil, 1},
		{"expired window without headers", 1, 0, -time.Second, nil, 1},
		{"fresh headers already include discovery", 5000, 1, time.Hour, quotaHeaders(5000, 1, 3600), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := testQuota(t, tc.limit)
			ctx := context.Background()
			if err := q.save(ctx, "usage.account."+quotaHash("7"), &quotaState{Allowance: tc.limit, Remaining: tc.remaining, Reset: q.now().Add(tc.reset)}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, _, _, err := q.do(ctx, "new-key", "", nil, func([]byte) (int, http.Header, []byte, error) {
				calls++
				return 200, tc.headers, []byte(`{"data":{"me":[{"id":7}]}}`), nil
			})
			if calls != tc.wantCalls {
				t.Fatalf("spent %d attempts, want %d; err=%v", calls, tc.wantCalls, err)
			}
			if tc.wantCalls == 1 && !errors.Is(err, metadata.ErrProviderDeferred) {
				t.Fatalf("want deferral, got %v", err)
			}
		})
	}
}

func TestQuotaPacingIsAccountScoped(t *testing.T) {
	q := testQuota(t, 5000)
	ctx := context.Background()
	for token, account := range map[string]string{"key-a": "account-a", "key-a2": "account-a", "key-b": "account-b"} {
		if err := q.save(ctx, "key."+quotaHash(token), quotaIdentity{Account: account}); err != nil {
			t.Fatal(err)
		}
	}
	// A short 429 uses adaptive pacing, not the daily quota hold.
	_, _, _, err := q.do(ctx, "key-a", "", nil, func([]byte) (int, http.Header, []byte, error) {
		return 429, http.Header{"Retry-After": []string{"20"}}, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewQuota(q.settings)
	restarted.now = q.now
	if delay, _, err := restarted.reserve(ctx, "key-b"); err != nil || delay != 0 {
		t.Fatalf("unrelated account delayed: %s %v", delay, err)
	}
	if delay, _, err := restarted.reserve(ctx, "key-a2"); err != nil || delay != 20*time.Second {
		t.Fatalf("same account lost penalty: %s %v", delay, err)
	}
}

func TestQuotaAdoptedAccountHonorsPacing(t *testing.T) {
	q := testQuota(t, 5000)
	q.now = time.Now
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	account := "account." + quotaHash("7")
	if err := q.save(ctx, "usage."+account, &quotaState{Pacing: quotaPacing{Interval: time.Second, Next: q.now().Add(20 * time.Second)}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, _, _, err := q.do(ctx, "new-sibling-key", "", nil, func([]byte) (int, http.Header, []byte, error) {
		calls++
		return 200, nil, []byte(`{"data":{"me":[{"id":7}]}}`), nil
	})
	if calls != 1 || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("adopted account bypassed pacing: calls=%d err=%v", calls, err)
	}
	// The gate must be released even when the adopted account cannot wait.
	if _, err := q.Status(ctx, "unrelated-key"); err != nil {
		t.Fatalf("unrelated account blocked: %v", err)
	}
}

func TestQuotaAdoptionKeepsStricterPacing(t *testing.T) {
	q := testQuota(t, 5000)
	q.now = time.Now
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	now := q.now()
	account := "account." + quotaHash("7")
	for key, pacing := range map[string]quotaPacing{
		"usage." + account:                  {Interval: 8 * time.Second, Next: now.Add(10 * time.Second), Successes: 19},
		"usage.key." + quotaHash("sibling"): {Interval: time.Second, Next: now.Add(20 * time.Second), Successes: 1},
	} {
		if err := q.save(ctx, key, &quotaState{Pacing: pacing}); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	_, _, _, err := q.do(ctx, "sibling", "", nil, func([]byte) (int, http.Header, []byte, error) {
		calls++
		return 200, nil, []byte(`{"data":{"me":[{"id":7}]}}`), nil
	})
	if calls != 1 || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("adoption bypassed pacing: %d %v", calls, err)
	}
	var state quotaState
	if err := q.load(ctx, "usage."+account, &state); err != nil {
		t.Fatal(err)
	}
	if state.Pacing.Interval != 8*time.Second || !state.Pacing.Next.Equal(now.Add(20*time.Second)) || state.Pacing.Successes != 2 {
		t.Fatalf("adoption weakened pacing: %+v", state.Pacing)
	}
}

func TestQuotaConcurrentFirstUseRevalidatesPacing(t *testing.T) {
	q := testQuota(t, 5000)
	q.now = time.Now
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.save(ctx, "usage.account."+quotaHash("7"), &quotaState{Pacing: quotaPacing{Interval: 8 * time.Second, Next: q.now().Add(20 * time.Second)}}); err != nil {
		t.Fatal(err)
	}
	// Deterministically interleave concurrent callers: both reserve before
	// either acquires the request gate and discovers the shared account.
	accounts := make([]string, 2)
	for i := range accounts {
		delay, account, err := q.reserve(ctx, "new-key")
		if err != nil || delay != 0 {
			t.Fatalf("reservation %d: %s %v", i, delay, err)
		}
		accounts[i] = account
	}
	calls := 0
	request := func([]byte) (int, http.Header, []byte, error) {
		calls++
		return 200, nil, []byte(`{"data":{"me":[{"id":7}]}}`), nil
	}
	for _, account := range accounts {
		if _, _, _, err := q.do(ctx, "new-key", account, nil, request); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("caller bypassed account hold: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("queued callers sent %d requests; only discovery is allowed", calls)
	}
	if _, err := q.Status(ctx, "unrelated"); err != nil {
		t.Fatalf("gate retained: %v", err)
	}
}

func TestQuotaFallbackDiscoveryConsumesPacingReservation(t *testing.T) {
	q := testQuota(t, 5000)
	q.now = time.Now
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	key := "key." + quotaHash("restricted")
	if err := q.save(ctx, key, quotaIdentity{Account: key, RetryAt: q.now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := q.save(ctx, "usage."+key, &quotaState{Pacing: quotaPacing{Interval: 8 * time.Second}}); err != nil {
		t.Fatal(err)
	}
	delay, account, err := q.reserve(ctx, "restricted")
	if err != nil || delay != 0 {
		t.Fatalf("first reservation: %s %v", delay, err)
	}
	calls := 0
	_, _, _, err = q.do(ctx, "restricted", account, nil, func([]byte) (int, http.Header, []byte, error) {
		calls++
		return 200, nil, []byte(`{"data":{"me":[]}}`), nil
	})
	if calls != 1 || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("discovery reused slot: calls=%d err=%v", calls, err)
	}
}
