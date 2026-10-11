package hardcover

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
)

const (
	dailyHoldSetting = "auth.hardcover_daily_holds"
	// dailyHoldSecretSetting keys the token fingerprints. It is install-local
	// and separate from the session secret so rotating sessions keeps holds.
	dailyHoldSecretSetting = "auth.hardcover_daily_hold_secret" //nolint:gosec // setting key name, not a credential
)

// DailyQuota remembers only server-reported exhaustion, not request usage.
// All clients in a process share this store. Its lock never covers network I/O,
// and after the first load it never covers a settings write either.
// Tokens are keyed by HMAC; expired holds are pruned whenever a hold is persisted.
type DailyQuota struct {
	mu       sync.Mutex
	settings *db.SettingsRepo
	secret   []byte
	holds    map[string]time.Time
	// version counts hold changes; saved is the newest version on disk.
	version uint64
	saved   uint64
	// writeMu orders settings writes so an older snapshot never lands last.
	writeMu sync.Mutex
	now     func() time.Time
}

// NewDailyQuota creates a shared hold store backed by private settings.
func NewDailyQuota(settings *db.SettingsRepo) *DailyQuota {
	return &DailyQuota{settings: settings, now: time.Now}
}

func (q *DailyQuota) load(ctx context.Context) error {
	if q.holds != nil {
		return nil
	}
	secret, err := q.loadSecret(ctx)
	if err != nil {
		return err
	}
	row, err := q.settings.Get(ctx, dailyHoldSetting)
	if err != nil {
		return fmt.Errorf("read Hardcover daily hold: %w", err)
	}
	holds := make(map[string]time.Time)
	if row != nil {
		if err := json.Unmarshal([]byte(row.Value), &holds); err != nil {
			// Failing closed here would block every token for good: the API
			// cannot write this row. Start empty; the next hold rewrites it.
			slog.Warn("Hardcover daily hold is corrupt; starting with no holds", "error", err)
			holds = nil
		}
	}
	if holds == nil {
		holds = make(map[string]time.Time)
	}
	q.secret = secret
	q.holds = holds
	return nil
}

// loadSecret reads the fingerprint key, creating it on first use and
// replacing it when corrupt.
func (q *DailyQuota) loadSecret(ctx context.Context) ([]byte, error) {
	row, err := q.settings.Get(ctx, dailyHoldSecretSetting)
	if err != nil {
		return nil, fmt.Errorf("read Hardcover daily hold key: %w", err)
	}
	if row != nil {
		if secret, err := hex.DecodeString(row.Value); err == nil && len(secret) > 0 {
			return secret, nil
		}
		// A corrupt key would block every token for good, and the API cannot
		// write it. Replace it; holds keyed by the old one expire on their own.
		slog.Warn("Hardcover daily hold key is corrupt; generating a new one")
		secret, err := newDailyHoldSecret()
		if err != nil {
			return nil, err
		}
		if err := q.settings.Set(ctx, dailyHoldSecretSetting, hex.EncodeToString(secret)); err != nil {
			return nil, fmt.Errorf("persist Hardcover daily hold key: %w", err)
		}
		return secret, nil
	}
	secret, err := newDailyHoldSecret()
	if err != nil {
		return nil, err
	}
	if _, err := q.settings.SetIfAbsent(ctx, dailyHoldSecretSetting, hex.EncodeToString(secret)); err != nil {
		return nil, fmt.Errorf("persist Hardcover daily hold key: %w", err)
	}
	// Re-read so a concurrent creator's key wins consistently.
	if row, err = q.settings.Get(ctx, dailyHoldSecretSetting); err != nil {
		return nil, fmt.Errorf("read Hardcover daily hold key: %w", err)
	}
	if row == nil {
		return nil, errors.New("read Hardcover daily hold key: missing after create")
	}
	if secret, err = hex.DecodeString(row.Value); err != nil || len(secret) == 0 {
		return nil, errors.New("decode Hardcover daily hold key: invalid value")
	}
	return secret, nil
}

func newDailyHoldSecret() ([]byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate Hardcover daily hold key: %w", err)
	}
	return secret, nil
}

// tokenKey requires a loaded store.
func (q *DailyQuota) tokenKey(token string) string {
	mac := hmac.New(sha256.New, q.secret)
	mac.Write([]byte(NormalizeAPIToken(token)))
	return hex.EncodeToString(mac.Sum(nil))
}

// Check returns a distinct error containing the reset time while a hold is live.
func (q *DailyQuota) Check(ctx context.Context, token string) error {
	if q == nil || NormalizeAPIToken(token) == "" {
		return nil
	}
	q.mu.Lock()
	if err := q.load(ctx); err != nil {
		q.mu.Unlock()
		return err
	}
	until := q.holds[q.tokenKey(token)]
	pending := q.saved != q.version
	now := q.now()
	q.mu.Unlock()

	var held error
	if until.After(now) {
		held = &metadata.DailyQuotaError{ResetAt: until}
	}
	if !pending {
		return held
	}
	// A live hold stays the primary answer so callers still classify the pause.
	if err := q.persist(ctx); err != nil {
		return errors.Join(held, err)
	}
	return held
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
	// Save even when cancellation interrupted response-body reading.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	q.mu.Lock()
	if err := q.load(saveCtx); err != nil {
		q.mu.Unlock()
		return err
	}
	now := q.now()
	until := now.Add(time.Duration(reset) * time.Second)
	key := q.tokenKey(token)
	if !until.After(q.holds[key]) {
		q.mu.Unlock()
		return nil
	}
	for k, hold := range q.holds {
		if !hold.After(now) {
			delete(q.holds, k)
		}
	}
	q.holds[key] = until
	q.version++
	q.mu.Unlock()
	return q.persist(saveCtx)
}

// A failed write retains a pending hold. Check retries it before admitting any
// request; transient storage failure must not turn into lost state at restart.
// The write runs outside q.mu; admissions wait on it only while a hold is pending.
func (q *DailyQuota) persist(ctx context.Context) error {
	q.writeMu.Lock()
	defer q.writeMu.Unlock()
	q.mu.Lock()
	if q.saved == q.version {
		q.mu.Unlock()
		return nil
	}
	version := q.version
	data, err := json.Marshal(q.holds)
	q.mu.Unlock()
	if err != nil {
		return fmt.Errorf("encode Hardcover daily hold: %w", err)
	}
	if err := q.settings.Set(ctx, dailyHoldSetting, string(data)); err != nil {
		return fmt.Errorf("persist Hardcover daily hold: %w", err)
	}
	q.mu.Lock()
	q.saved = version
	q.mu.Unlock()
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
