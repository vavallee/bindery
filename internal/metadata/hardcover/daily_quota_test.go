package hardcover

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
)

func dailyFixture(t *testing.T) (*DailyQuota, *db.SettingsRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	settings := db.NewSettingsRepo(database)
	return NewDailyQuota(settings), settings
}

func dailyHeaders(remaining, reset int) http.Header {
	h := make(http.Header)
	h.Set("RateLimit-Policy", `"burst";q=60;w=60, "daily";q=5000;w=86400`)
	h.Set("RateLimit", fmt.Sprintf(`"daily";r=%d;t=%d`, remaining, reset))
	return h
}

func TestDailyQuotaPersistsIsolatesAndExpires(t *testing.T) {
	ctx := context.Background()
	q, settings := dailyFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	q.now = func() time.Time { return now }
	if err := q.observe(ctx, "example-only-token", dailyHeaders(0, 3600)); err != nil {
		t.Fatal(err)
	}
	// A late successful response must not erase an exhaustion hold.
	if err := q.observe(ctx, "example-only-token", dailyHeaders(20, 3600)); err != nil {
		t.Fatal(err)
	}
	restarted := NewDailyQuota(settings)
	restarted.now = q.now
	var daily *metadata.DailyQuotaError
	if err := restarted.Check(ctx, "Bearer example-only-token"); !errors.As(err, &daily) || !daily.ResetAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("hold = %v", err)
	}
	if errors.Is(daily, ErrRateLimited) || errors.Is(daily, metadata.ErrRateLimited) {
		t.Fatal("daily exhaustion matches short throttle")
	}
	if err := restarted.Check(ctx, "different-example-token"); err != nil {
		t.Fatal(err)
	}
	row, err := settings.Get(ctx, dailyHoldSetting)
	if err != nil || row == nil || strings.Contains(row.Value, "example-only-token") {
		t.Fatalf("unsafe/missing persistence: %v", err)
	}
	now = now.Add(time.Hour)
	if err := restarted.Check(ctx, "example-only-token"); err != nil {
		t.Fatal(err)
	}
}

func TestDailyQuotaOnlyAcceptsDailyExhaustion(t *testing.T) {
	for _, header := range []string{
		`"burst";r=0;t=60`, `"daily";r=1;t=3600`, `"daily";r=0`,
		`"daily";r=0;t=-1`, `"daily";r=0;t=9223372036854775807`,
		`"daily";r=0;t=30;t=40`, `"daily";r=no;t=3600`,
	} {
		t.Run(header, func(t *testing.T) {
			q, _ := dailyFixture(t)
			h := make(http.Header)
			h.Set("RateLimit", header)
			h.Set("Retry-After", "60")
			if err := q.observe(context.Background(), "example", h); err != nil {
				t.Fatal(err)
			}
			if err := q.Check(context.Background(), "example"); err != nil {
				t.Fatalf("unexpected hold: %v", err)
			}
		})
	}
}

type dailyTransport func(*http.Request) (*http.Response, error)

func (f dailyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDailyQuotaTransportStopsDailyButRetriesShort429(t *testing.T) {
	for _, daily := range []bool{true, false} {
		t.Run(fmt.Sprint(daily), func(t *testing.T) {
			q, _ := dailyFixture(t)
			th, clock := newFakeThrottle()
			q.now = clock.now
			calls := 0
			c := New().WithDailyQuota(q).WithTokenSource(func(context.Context) string { return "example" })
			c.throttle = th
			c.http = &http.Client{Transport: dailyTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				header := make(http.Header)
				header.Set("Retry-After", "60")
				status, body := 429, `{"error":"too many requests"}`
				if daily {
					header = dailyHeaders(0, 3600)
				} else if calls > 1 {
					status, body = 200, `{}`
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			var out any
			err := c.query(context.Background(), "query", nil, &out)
			if daily {
				var exhausted *metadata.DailyQuotaError
				if !errors.As(err, &exhausted) || calls != 1 {
					t.Fatalf("calls %d err %v", calls, err)
				}
				if err = c.WithToken("example").query(context.Background(), "query", nil, &out); !errors.As(err, &exhausted) || calls != 1 {
					t.Fatalf("repeated exhausted request: %d %v", calls, err)
				}
			} else if err != nil || calls != 2 {
				t.Fatalf("short throttle: %d %v", calls, err)
			}
		})
	}
}

func TestDailyQuotaDoesNotSerializeNetworkRequests(t *testing.T) {
	q, _ := dailyFixture(t)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := New().WithDailyQuota(q)
	c.throttle, _ = newFakeThrottle()
	c.http = &http.Client{Transport: dailyTransport(func(r *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	done := make(chan error, 2)
	for _, token := range []string{"one", "two"} {
		go func() { var out any; done <- c.WithToken(token).query(ctx, "query", nil, &out) }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			close(release)
			t.Fatal("network requests were serialized")
		}
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestDailyQuotaFinalSuccessAndTokenRotation(t *testing.T) {
	q, _ := dailyFixture(t)
	var calls atomic.Int32
	token := "first"
	c := New().WithDailyQuota(q).WithTokenSource(func(context.Context) string { return token })
	c.throttle, _ = newFakeThrottle()
	c.http = &http.Client{Transport: dailyTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		token = "second"
		return &http.Response{StatusCode: 200, Header: dailyHeaders(0, 3600), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	var out struct{ OK bool }
	if err := c.query(context.Background(), "query", nil, &out); err != nil || !out.OK {
		t.Fatalf("last successful response lost: %v", err)
	}
	if err := q.Check(context.Background(), "second"); err != nil {
		t.Fatal("rotation inherited old key hold")
	}
	if err := q.Check(context.Background(), "first"); err == nil {
		t.Fatal("sent token was not held")
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected retry")
	}
}

func TestDailyQuotaStorageFailureIsVisible(t *testing.T) {
	q, settings := dailyFixture(t)
	if err := settings.Set(context.Background(), dailyHoldSetting, `invalid`); err != nil {
		t.Fatal(err)
	}
	if err := q.Check(context.Background(), "example"); err == nil {
		t.Fatal("corrupt hold was silently ignored")
	}
	if err := settings.Set(context.Background(), dailyHoldSetting, `{}`); err != nil {
		t.Fatal(err)
	}
	if err := q.Check(context.Background(), "example"); err != nil {
		t.Fatal(err)
	}
}

func TestDailyQuotaPrunesExpiredAndNeverShortensHolds(t *testing.T) {
	q, _ := dailyFixture(t)
	ctx := context.Background()
	now := time.Now()
	q.now = func() time.Time { return now }
	for token, seconds := range map[string]int{"expired": 1, "live": 3600} {
		if err := q.observe(ctx, token, dailyHeaders(0, seconds)); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(2 * time.Second)
	if err := q.observe(ctx, "live", dailyHeaders(0, 10)); err != nil {
		t.Fatal(err)
	}
	var daily *metadata.DailyQuotaError
	if err := q.Check(ctx, "live"); !errors.As(err, &daily) || daily.ResetAt.Before(now.Add(30*time.Minute)) {
		t.Fatalf("shortened: %v", err)
	}
	if err := q.observe(ctx, "new", dailyHeaders(0, 3600)); err != nil {
		t.Fatal(err)
	}
	if _, exists := q.holds[q.tokenKey("expired")]; exists {
		t.Fatal("expired token state retained")
	}
}

func TestDailyQuotaRetriesFailedPersistenceBeforeRequests(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	settings := db.NewSettingsRepo(database)
	q := NewDailyQuota(settings)
	ctx := context.Background()
	if err := q.Check(ctx, "example"); err != nil {
		t.Fatal(err)
	}
	// Keep real SQLite but force only writes to fail.
	if _, err := database.Exec(`CREATE TRIGGER fail_hold_write BEFORE INSERT ON settings BEGIN SELECT RAISE(FAIL, 'temporary write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := q.observe(ctx, "example", dailyHeaders(0, 3600)); err == nil {
		t.Fatal("failed write was hidden")
	}
	// The held token still reports its pause; others are refused until the
	// pending hold is durable.
	var daily *metadata.DailyQuotaError
	if err := q.Check(ctx, "example"); !errors.As(err, &daily) || !strings.Contains(err.Error(), "temporary write failure") {
		t.Fatalf("held token during pending persistence: %v", err)
	}
	if err := q.Check(ctx, "other"); err == nil || errors.As(err, &daily) || !strings.Contains(err.Error(), "temporary write failure") {
		t.Fatalf("pending persistence ignored: %v", err)
	}
	if _, err := database.Exec(`DROP TRIGGER fail_hold_write`); err != nil {
		t.Fatal(err)
	}
	if err := q.Check(ctx, "other"); err != nil {
		t.Fatalf("retried persistence: %v", err)
	}
	if err := q.Check(ctx, "example"); !errors.As(err, &daily) {
		t.Fatalf("hold after retry: %v", err)
	}
	if err := NewDailyQuota(settings).Check(ctx, "example"); !errors.As(err, &daily) {
		t.Fatalf("retried hold was not durable: %v", err)
	}
}

func TestDailyQuotaFingerprintsWithInstallSecret(t *testing.T) {
	ctx := context.Background()
	q, settings := dailyFixture(t)
	if err := q.observe(ctx, "example-only-token", dailyHeaders(0, 3600)); err != nil {
		t.Fatal(err)
	}
	secretRow, err := settings.Get(ctx, dailyHoldSecretSetting)
	if err != nil || secretRow == nil {
		t.Fatalf("install secret: %v", err)
	}
	secret, err := hex.DecodeString(secretRow.Value)
	if err != nil || len(secret) != 32 {
		t.Fatalf("install secret = %q", secretRow.Value)
	}
	row, err := settings.Get(ctx, dailyHoldSetting)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("example-only-token"))
	if plain := fmt.Sprintf("%x", sha256.Sum256([]byte("example-only-token"))); strings.Contains(row.Value, plain) {
		t.Fatal("hold keyed by unsalted token hash")
	}
	if !strings.Contains(row.Value, hex.EncodeToString(mac.Sum(nil))) {
		t.Fatalf("hold not keyed by install HMAC: %s", row.Value)
	}
	// A restart reuses the stored secret rather than orphaning holds.
	restarted := NewDailyQuota(settings)
	var daily *metadata.DailyQuotaError
	if err := restarted.Check(ctx, "example-only-token"); !errors.As(err, &daily) {
		t.Fatalf("hold after restart: %v", err)
	}
	if again, _ := settings.Get(ctx, dailyHoldSecretSetting); again == nil || again.Value != secretRow.Value {
		t.Fatal("install secret regenerated")
	}
	// A different install cannot map the same token to the same fingerprint.
	other, otherSettings := dailyFixture(t)
	if err := other.Check(ctx, "example-only-token"); err != nil {
		t.Fatal(err)
	}
	if otherRow, _ := otherSettings.Get(ctx, dailyHoldSecretSetting); otherRow == nil || otherRow.Value == secretRow.Value {
		t.Fatal("install secrets are not independent")
	}
}

func TestDailyQuotaCorruptSecretIsVisible(t *testing.T) {
	q, settings := dailyFixture(t)
	if err := settings.Set(context.Background(), dailyHoldSecretSetting, "not-hex"); err != nil {
		t.Fatal(err)
	}
	if err := q.Check(context.Background(), "example"); err == nil {
		t.Fatal("corrupt secret was silently ignored")
	}
}

func TestDailyQuotaAdmissionDoesNotWaitOnSettingsWrite(t *testing.T) {
	q, _ := dailyFixture(t)
	ctx := context.Background()
	if err := q.observe(ctx, "held", dailyHeaders(0, 3600)); err != nil {
		t.Fatal(err)
	}
	// Stand in for a slow in-flight write.
	q.writeMu.Lock()
	defer q.writeMu.Unlock()
	done := make(chan error, 2)
	go func() { done <- q.Check(ctx, "held") }()
	go func() { done <- q.Check(ctx, "free") }()
	var daily *metadata.DailyQuotaError
	for range 2 {
		select {
		case err := <-done:
			if err != nil && !errors.As(err, &daily) {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("admission waited on a settings write")
		}
	}
}

func TestDailyQuotaResetBounds(t *testing.T) {
	for _, seconds := range []int{0, 1, 86400, 86401, 9223372036} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			q, _ := dailyFixture(t)
			if err := q.observe(context.Background(), "example", dailyHeaders(0, seconds)); err != nil {
				t.Fatal(err)
			}
			var daily *metadata.DailyQuotaError
			held := errors.As(q.Check(context.Background(), "example"), &daily)
			if held != (seconds > 0 && seconds <= 86400) {
				t.Fatalf("held=%v for reset=%d", held, seconds)
			}
		})
	}
	q, _ := dailyFixture(t)
	h := dailyHeaders(0, 3600)
	h.Set("RateLimit-Policy", `"daily";w=60`)
	if err := q.observe(context.Background(), "example", h); err != nil {
		t.Fatal(err)
	}
	if err := q.Check(context.Background(), "example"); err != nil {
		t.Fatalf("non-daily window held: %v", err)
	}
}

func TestDailyQuotaStopsRequestAfterPacerWait(t *testing.T) {
	q, _ := dailyFixture(t)
	th, clk := newFakeThrottle()
	q.now = clk.now
	th.penalize(0)
	th.sleep = func(ctx context.Context, d time.Duration) error {
		if err := q.observe(ctx, "example", dailyHeaders(0, 3600)); err != nil {
			return err
		}
		return clk.sleep(ctx, d)
	}
	c := New().WithToken("example").WithDailyQuota(q)
	c.throttle = th
	c.http = &http.Client{Transport: dailyTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("request sent despite hold during pacing")
		return nil, nil
	})}
	var out any
	var daily *metadata.DailyQuotaError
	if err := c.query(context.Background(), "query", nil, &out); !errors.As(err, &daily) {
		t.Fatalf("got %v", err)
	}
}

func TestDailyQuotaQueryReportsPersistenceFailure(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	q := NewDailyQuota(db.NewSettingsRepo(database))
	c := New().WithToken("example").WithDailyQuota(q)
	c.throttle, _ = newFakeThrottle()
	c.http = &http.Client{Transport: dailyTransport(func(*http.Request) (*http.Response, error) {
		if _, err := database.Exec(`CREATE TRIGGER fail_hold_write BEFORE INSERT ON settings BEGIN SELECT RAISE(FAIL, 'temporary write failure'); END`); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: dailyHeaders(0, 3600), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	var out any
	if err := c.query(context.Background(), "query", nil, &out); err == nil || !strings.Contains(err.Error(), "persist Hardcover daily hold") {
		t.Fatalf("got %v", err)
	}
}
