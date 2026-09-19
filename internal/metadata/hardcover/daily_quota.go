package hardcover

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
)

const dailyHoldSetting = "auth.hardcover_daily_holds"

// DailyQuota remembers only server-reported exhaustion, not request usage.
// All clients in a process share this store. Its lock never covers network I/O.
// Tokens are hashed; expired holds are pruned whenever a hold is persisted.
type DailyQuota struct {
	mu       sync.Mutex
	settings *db.SettingsRepo
	holds    map[string]time.Time
	dirty    bool
	now      func() time.Time
}

// NewDailyQuota creates a shared hold store backed by private settings.
func NewDailyQuota(settings *db.SettingsRepo) *DailyQuota {
	return &DailyQuota{settings: settings, now: time.Now}
}

func (q *DailyQuota) load(ctx context.Context) error {
	if q.holds != nil {
		return nil
	}
	row, err := q.settings.Get(ctx, dailyHoldSetting)
	if err != nil {
		return fmt.Errorf("read Hardcover daily hold: %w", err)
	}
	holds := make(map[string]time.Time)
	if row != nil {
		if err := json.Unmarshal([]byte(row.Value), &holds); err != nil {
			return fmt.Errorf("decode Hardcover daily hold: %w", err)
		}
	}
	if holds == nil {
		holds = make(map[string]time.Time)
	}
	q.holds = holds
	return nil
}

func dailyTokenKey(token string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(NormalizeAPIToken(token))))
}

// Check returns a distinct error containing the reset time while a hold is live.
func (q *DailyQuota) Check(ctx context.Context, token string) error {
	if q == nil || NormalizeAPIToken(token) == "" {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.load(ctx); err != nil {
		return err
	}
	if err := q.persist(ctx); err != nil {
		return err
	}
	if until := q.holds[dailyTokenKey(token)]; until.After(q.now()) {
		return &metadata.DailyQuotaError{ResetAt: until}
	}
	return nil
}

// observe accepts only an explicitly exhausted daily bucket. Retry-After alone
// cannot distinguish a short throttle from daily exhaustion and is left to the
// existing pacer. A successful final request is still returned to its caller.
func (q *DailyQuota) observe(ctx context.Context, token string, headers http.Header) error {
	if q == nil {
		return nil
	}
	daily := dailyParameters(strings.Join(headers.Values("RateLimit"), ","))
	remaining, hasRemaining := daily["r"]
	reset := daily["t"]
	if !hasRemaining || remaining != 0 || reset <= 0 || reset > 86400 {
		return nil
	}
	policy := dailyParameters(strings.Join(headers.Values("RateLimit-Policy"), ","))
	if window, ok := policy["w"]; ok && window != 86400 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	// Save even when cancellation interrupted response-body reading.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := q.load(saveCtx); err != nil {
		return err
	}
	now := q.now()
	until := now.Add(time.Duration(reset) * time.Second)
	key := dailyTokenKey(token)
	if !until.After(q.holds[key]) {
		return nil
	}
	for k, hold := range q.holds {
		if !hold.After(now) {
			delete(q.holds, k)
		}
	}
	q.holds[key] = until
	q.dirty = true
	return q.persist(saveCtx)
}

// A failed write retains a pending hold. Check retries it before admitting any
// request; transient storage failure must not turn into lost state at restart.
func (q *DailyQuota) persist(ctx context.Context) error {
	if !q.dirty {
		return nil
	}
	data, err := json.Marshal(q.holds)
	if err != nil {
		return fmt.Errorf("encode Hardcover daily hold: %w", err)
	}
	if err := q.settings.Set(ctx, dailyHoldSetting, string(data)); err != nil {
		return fmt.Errorf("persist Hardcover daily hold: %w", err)
	}
	q.dirty = false
	return nil
}

// dailyParameters ignores burst buckets and rejects malformed/duplicate values.
func dailyParameters(value string) map[string]int64 {
	for _, entry := range strings.Split(value, ",") {
		parts := strings.Split(entry, ";")
		if strings.TrimSpace(parts[0]) != `"daily"` {
			continue
		}
		result := make(map[string]int64)
		for _, part := range parts[1:] {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) != 2 {
				return nil
			}
			n, err := strconv.ParseInt(kv[1], 10, 64)
			if err != nil || n < 0 {
				return nil
			}
			if _, duplicate := result[kv[0]]; duplicate {
				return nil
			}
			result[kv[0]] = n
		}
		return result
	}
	return nil
}

// WithDailyQuota carries the shared durable hold into token-bound client copies.
func (c *Client) WithDailyQuota(q *DailyQuota) *Client {
	clone := *c
	clone.dailyQuota = q
	return &clone
}

// CheckQuota reports the hold for this client's current token without network I/O.
func (c *Client) CheckQuota(ctx context.Context) error {
	return c.dailyQuota.Check(ctx, c.authorizationToken(ctx))
}
