package hardcover

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
)

// SettingDailyRequestLimit is the fallback when Hardcover does not report a policy.
const SettingDailyRequestLimit = "hardcover.daily_request_limit"

const quotaPrefix = "auth.hardcover_quota."

var errQuotaStorage = fmt.Errorf("hardcover quota storage unavailable: %w", metadata.ErrProviderDeferred)

// Quota shares persistent accounting across all Hardcover clients in an install.
// Requests are serialized so older responses cannot replenish a newer budget.
// No database transaction is held during network I/O.
type Quota struct {
	settings *db.SettingsRepo
	gate     chan struct{}
	now      func() time.Time
	pending  *quotaPending
}

// NewQuota must be shared by metadata, lists and settings clients.
func NewQuota(settings *db.SettingsRepo) *Quota {
	return &Quota{settings: settings, gate: make(chan struct{}, 1), now: time.Now}
}

// WithQuota returns a copy using the shared quota controller.
func (c *Client) WithQuota(q *Quota) *Client {
	copy := *c
	copy.quota = q
	return &copy
}

// A failed response-state write blocks further traffic until it can be saved.
type quotaPending struct {
	key   string
	state quotaState
}

func (q *Quota) flush(ctx context.Context) error {
	if q.pending == nil {
		return nil
	}
	if err := q.save(ctx, q.pending.key, &q.pending.state); err != nil {
		return err
	}
	q.pending = nil
	return nil
}

type quotaHour struct {
	Start int64 `json:"start"`
	Used  int   `json:"used"`
}
type quotaState struct {
	Hours     []quotaHour `json:"hours,omitempty"`
	Allowance int         `json:"allowance,omitempty"`
	Remaining int         `json:"remaining"`
	Reset     time.Time   `json:"reset,omitempty"`
	Hold      time.Time   `json:"hold,omitempty"`
	Pacing    quotaPacing `json:"pacing,omitzero"`
}

type quotaIdentity struct {
	Account string    `json:"account"`
	RetryAt time.Time `json:"retryAt,omitempty"`
}

// QuotaStatus reports estimates separately from upstream-reported usage.
type QuotaStatus struct {
	Source       string    `json:"source"`
	Allowance    int       `json:"allowance"`
	Remaining    int       `json:"remaining"`
	Reserve      int       `json:"interactiveReserve"`
	Deferred     bool      `json:"deferred"`
	NextEligible time.Time `json:"nextEligible,omitzero"`
}

// QuotaDeferredError retains the provider's identity and when work can be retried.
type QuotaDeferredError struct{ Status QuotaStatus }

func (e *QuotaDeferredError) Error() string {
	return fmt.Sprintf("hardcover deferred: %s quota, allowance %d, remaining %d; next eligible %s", e.Status.Source, e.Status.Allowance, e.Status.Remaining, e.Status.NextEligible.UTC().Format(time.RFC3339))
}
func (e *QuotaDeferredError) Is(target error) bool {
	return target == metadata.ErrProviderDeferred || target == ErrRateLimited
}

type backgroundQuotaKey struct{}

// WithBackgroundQuota reserves ten percent of the effective allowance for interactive work.
func WithBackgroundQuota(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundQuotaKey{}, true)
}

func quotaHash(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

func (q *Quota) load(ctx context.Context, key string, out any) error {
	s, err := q.settings.Get(ctx, quotaPrefix+key)
	if err != nil {
		return fmt.Errorf("%w: read: %w", errQuotaStorage, err)
	}
	if s != nil {
		if err := json.Unmarshal([]byte(s.Value), out); err != nil {
			return fmt.Errorf("%w: decode: %w", errQuotaStorage, err)
		}
	}
	return nil
}
func (q *Quota) save(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := q.settings.Set(ctx, quotaPrefix+key, string(raw)); err != nil {
		return fmt.Errorf("%w: save: %w", errQuotaStorage, err)
	}
	return nil
}
func (q *Quota) fallback(ctx context.Context) (int, error) {
	s, err := q.settings.Get(ctx, SettingDailyRequestLimit)
	if err != nil {
		return 0, err
	}
	if s == nil || s.Value == "" {
		return 5000, nil
	}
	n, err := strconv.Atoi(s.Value)
	if err != nil || n <= 0 || n > 1_000_000_000 {
		return 0, errors.New("invalid hardcover daily request limit")
	}
	return n, nil
}

func (q *Quota) status(s *quotaState, fallback int, background bool) QuotaStatus {
	now := q.now()
	if !s.Reset.IsZero() && !s.Reset.After(now) {
		s.Reset = time.Time{}
		s.Hours = nil
	}
	kept := s.Hours[:0]
	used := 0
	for _, h := range s.Hours {
		if time.Unix(h.Start, 0).Add(25 * time.Hour).After(now) {
			kept = append(kept, h)
			used += h.Used
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Start < kept[j].Start })
	s.Hours = nil
	for _, h := range kept {
		if len(s.Hours) > 0 && s.Hours[len(s.Hours)-1].Start == h.Start {
			s.Hours[len(s.Hours)-1].Used += h.Used
		} else {
			s.Hours = append(s.Hours, h)
		}
	}
	status := QuotaStatus{Source: "local estimate (configured fallback)", Allowance: fallback, Remaining: max(0, fallback-used)}
	if s.Allowance > 0 {
		status.Allowance = s.Allowance
		status.Remaining = max(0, s.Allowance-used)
		status.Source = "local estimate (detected allowance)"
	}
	if s.Allowance > 0 && s.Reset.After(now) {
		status.Source = "upstream"
		status.Remaining = s.Remaining
	}
	status.Reserve = (status.Allowance + 9) / 10
	threshold := 0
	if background {
		threshold = status.Reserve
	}
	if status.Remaining <= threshold {
		status.Deferred = true
		if s.Reset.After(now) {
			status.NextEligible = s.Reset
		} else {
			// Hour buckets conservatively retain attempts for between 24 and 25 hours.
			for _, h := range s.Hours {
				used -= h.Used
				status.NextEligible = time.Unix(h.Start, 0).Add(25 * time.Hour)
				if status.Allowance-used > threshold {
					break
				}
			}
		}
	}
	if s.Hold.After(now) {
		status.Deferred = true
		if s.Hold.After(status.NextEligible) {
			status.NextEligible = s.Hold
		}
	}
	return status
}

// Status reads current state without spending a request or discovering an account.
func (q *Quota) Status(ctx context.Context, token string) (QuotaStatus, error) {
	select {
	case q.gate <- struct{}{}:
		defer func() { <-q.gate }()
	case <-ctx.Done():
		return QuotaStatus{}, ctx.Err()
	}
	if err := q.flush(ctx); err != nil {
		return QuotaStatus{}, err
	}
	fallback, err := q.fallback(ctx)
	if err != nil {
		return QuotaStatus{}, err
	}
	id := quotaIdentity{}
	key := "key." + quotaHash(NormalizeAPIToken(token))
	if err := q.load(ctx, key, &id); err != nil {
		return QuotaStatus{}, err
	}
	account := id.Account
	if account == "" {
		account = key
	}
	var s quotaState
	if err := q.load(ctx, "usage."+account, &s); err != nil {
		return QuotaStatus{}, err
	}
	return q.status(&s, fallback, false), nil
}

type quotaRequest func([]byte) (int, http.Header, []byte, error)

func (q *Quota) do(ctx context.Context, token, pacedAccount string, body []byte, request quotaRequest) (int, http.Header, []byte, error) {
	held := false
	select {
	case q.gate <- struct{}{}:
		held = true
		defer func() {
			if held {
				<-q.gate
			}
		}()
	case <-ctx.Done():
		return 0, nil, nil, ctx.Err()
	}
	if err := q.flush(ctx); err != nil {
		return 0, nil, nil, err
	}
	fallback, err := q.fallback(ctx)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: %w", errQuotaStorage, err)
	}
	key := "key." + quotaHash(token)
	var id quotaIdentity
	if err := q.load(ctx, key, &id); err != nil {
		return 0, nil, nil, err
	}
	if id.Account == "" || (!id.RetryAt.IsZero() && !id.RetryAt.After(q.now())) {
		// The documented me query identifies accounts without decoding tokens. This
		// one-time discovery is itself charged and its headers update the budget.
		status, headers, raw, err := q.attempt(ctx, key, []byte(`{"query":"query BinderyQuotaIdentity { me { id } }"}`), fallback, request)
		if err != nil {
			return status, headers, raw, err
		}
		if status == http.StatusUnauthorized || status == http.StatusTooManyRequests || status >= 500 {
			return status, headers, raw, nil
		}
		// Discovery spent the reservation even if account identification fails.
		// The real query always needs its own slot.
		pacedAccount = ""
		var response struct {
			Data struct {
				Me []struct {
					ID int64 `json:"id"`
				} `json:"me"`
			} `json:"data"`
		}
		id = quotaIdentity{Account: key, RetryAt: q.now().Add(24 * time.Hour)}
		if status == http.StatusOK && json.Unmarshal(raw, &response) == nil && len(response.Data.Me) == 1 && response.Data.Me[0].ID > 0 {
			id = quotaIdentity{Account: "account." + quotaHash(strconv.FormatInt(response.Data.Me[0].ID, 10))}
			var provisional, existing quotaState
			if err := q.load(ctx, "usage."+key, &provisional); err != nil {
				return 0, nil, nil, err
			}
			if err := q.load(ctx, "usage."+id.Account, &existing); err != nil {
				return 0, nil, nil, err
			}
			// Expire the old account window before adding newly spent discovery
			// requests. Without fresh headers they also consume known remaining.
			q.status(&existing, fallback, false)
			for _, hour := range provisional.Hours {
				if existing.Reset.After(q.now()) {
					existing.Remaining = max(0, existing.Remaining-hour.Used)
				}
			}
			existing.Hours = append(existing.Hours, provisional.Hours...)
			// Adoption must not weaken either account's adaptive penalty.
			if provisional.Pacing.Next.After(existing.Pacing.Next) {
				existing.Pacing.Next = provisional.Pacing.Next
			}
			existing.Pacing.Interval = max(existing.Pacing.Interval, provisional.Pacing.Interval)
			existing.Pacing.Successes = min(existing.Pacing.Successes, provisional.Pacing.Successes)
			q.observe(&existing, headers, status)
			if provisional.Hold.After(existing.Hold) {
				existing.Hold = provisional.Hold
			}
			usageJSON, err := json.Marshal(existing)
			if err != nil {
				return 0, nil, nil, err
			}
			identityJSON, err := json.Marshal(id)
			if err != nil {
				return 0, nil, nil, err
			}
			if err := q.settings.SetMany(ctx, []db.SettingKV{{Key: quotaPrefix + "usage." + id.Account, Value: string(usageJSON)}, {Key: quotaPrefix + key, Value: string(identityJSON)}}); err != nil {
				return 0, nil, nil, fmt.Errorf("%w: %w", errQuotaStorage, err)
			}
		} else if err := q.save(ctx, key, id); err != nil {
			return 0, nil, nil, err
		}
	}
	// Every caller must validate its reservation after acquiring the gate:
	// another caller may have identified this key since it reserved pacing.
	for pacedAccount != id.Account {
		<-q.gate
		held = false
		var err error
		pacedAccount, err = q.wait(ctx, token)
		if err != nil {
			if errors.Is(err, errThrottled) {
				err = rateLimited(err)
			}
			return 0, nil, nil, err
		}
		select {
		case q.gate <- struct{}{}:
			held = true
		case <-ctx.Done():
			return 0, nil, nil, ctx.Err()
		}
		if err := q.flush(ctx); err != nil {
			return 0, nil, nil, err
		}
		if err := q.load(ctx, key, &id); err != nil {
			return 0, nil, nil, err
		}
	}
	return q.attempt(ctx, id.Account, body, fallback, request)
}

func (q *Quota) attempt(ctx context.Context, account string, body []byte, fallback int, request quotaRequest) (int, http.Header, []byte, error) {
	var s quotaState
	if err := q.load(ctx, "usage."+account, &s); err != nil {
		return 0, nil, nil, err
	}
	background, _ := ctx.Value(backgroundQuotaKey{}).(bool)
	status := q.status(&s, fallback, background)
	if status.Deferred {
		return 0, nil, nil, &QuotaDeferredError{Status: status}
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, nil, err
	}
	// Every query currently has exactly one top-level field. Each retry and
	// page passes here separately; nested fields do not consume extra quota.
	hour := q.now().Truncate(time.Hour).Unix()
	if len(s.Hours) > 0 && s.Hours[len(s.Hours)-1].Start == hour {
		s.Hours[len(s.Hours)-1].Used++
	} else {
		s.Hours = append(s.Hours, quotaHour{Start: hour, Used: 1})
	}
	if s.Reset.After(q.now()) {
		s.Remaining = max(0, s.Remaining-1)
	}
	// Persist before transmission. A crash or ambiguous transport failure may
	// overcount one attempt but cannot restore already-spent allowance.
	if err := q.save(ctx, "usage."+account, &s); err != nil {
		return 0, nil, nil, err
	}
	code, headers, raw, err := request(body)
	q.observe(&s, headers, code)
	pacer := s.Pacing.throttle(q.now)
	if isRetryableStatus(code) {
		hint, ok := parseRetryAfterHeader(headers.Get("Retry-After"))
		if !ok {
			hint, _ = parseRetryHint(classifyHTTPError(code, raw).Error())
		}
		pacer.penalize(hint)
	} else if code == http.StatusOK {
		pacer.succeed()
	}
	s.Pacing = pacingState(pacer)
	// Persist response cooldowns even if the caller cancelled while reading.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if saveErr := q.save(saveCtx, "usage."+account, &s); saveErr != nil {
		q.pending = &quotaPending{key: "usage." + account, state: s}
		return 0, nil, nil, saveErr
	}
	if code == http.StatusTooManyRequests {
		status = q.status(&s, fallback, background)
		if status.Deferred {
			return code, headers, raw, &QuotaDeferredError{Status: status}
		}
	}
	return code, headers, raw, err
}

// dailyParameters reads only the documented daily bucket, ignoring the burst
// bucket and unknown extensions. Invalid or duplicate parameters are rejected.
func dailyParameters(value string) map[string]int64 {
	for _, entry := range strings.Split(value, ",") {
		parts := strings.Split(entry, ";")
		if strings.TrimSpace(parts[0]) != `"daily"` {
			continue
		}
		result := map[string]int64{}
		for _, part := range parts[1:] {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) != 2 {
				return nil
			}
			n, err := strconv.ParseInt(kv[1], 10, 64)
			if err != nil || n < 0 {
				return nil
			}
			if _, ok := result[kv[0]]; ok {
				return nil
			}
			result[kv[0]] = n
		}
		return result
	}
	return nil
}

func (q *Quota) observe(s *quotaState, h http.Header, code int) {
	policy := dailyParameters(strings.Join(h.Values("RateLimit-Policy"), ","))
	if policy["q"] > 0 && policy["q"] <= 1_000_000_000 && policy["w"] == 86400 {
		limit := int(policy["q"])
		if s.Allowance > 0 && s.Allowance != limit {
			spent := max(0, s.Allowance-s.Remaining)
			s.Remaining = max(0, limit-spent)
		}
		s.Allowance = limit
	}
	standing := dailyParameters(strings.Join(h.Values("RateLimit"), ","))
	remaining, hasRemaining := standing["r"]
	reset, hasReset := standing["t"]
	if hasRemaining && hasReset && reset > 0 && reset <= int64((time.Duration(1<<63-1))/time.Second) && s.Allowance > 0 && remaining <= int64(s.Allowance) {
		s.Remaining = int(remaining)
		s.Reset = q.now().Add(time.Duration(reset) * time.Second)
	}
	if code == http.StatusTooManyRequests {
		if delay, ok := parseRetryAfterUncapped(h.Get("Retry-After"), q.now()); ok && delay > throttleMaxHold {
			until := q.now().Add(delay)
			if until.After(s.Hold) {
				s.Hold = until
			}
		}
	}
}

func parseRetryAfterUncapped(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		if n > 0 && n <= int64((time.Duration(1<<63-1))/time.Second) {
			return time.Duration(n) * time.Second, true
		}
		return 0, false
	}
	if until, err := http.ParseTime(value); err == nil && until.After(now) {
		return until.Sub(now), true
	}
	return 0, false
}

// Transient pacing is stored with the account so independently constructed
// clients share penalties without an unbounded in-memory token registry.
type quotaPacing struct {
	Interval  time.Duration `json:"interval,omitempty"`
	Next      time.Time     `json:"next,omitzero"`
	Successes int           `json:"successes,omitempty"`
}

func (p quotaPacing) throttle(now func() time.Time) *throttle {
	return &throttle{interval: p.Interval, next: p.Next, successes: p.Successes, now: now, sleep: sleepCtx}
}

func pacingState(t *throttle) quotaPacing {
	return quotaPacing{Interval: t.interval, Next: t.next, Successes: t.successes}
}

func (q *Quota) wait(ctx context.Context, token string) (string, error) {
	delay, account, err := q.reserve(ctx, token)
	if err != nil {
		return "", err
	}
	// Never sleep while holding the shared accounting gate: another account
	// must remain usable while this account waits out its penalty.
	return account, sleepCtx(ctx, delay)
}

func (q *Quota) reserve(ctx context.Context, token string) (time.Duration, string, error) {
	select {
	case q.gate <- struct{}{}:
		defer func() { <-q.gate }()
	case <-ctx.Done():
		return 0, "", ctx.Err()
	}
	if err := q.flush(ctx); err != nil {
		return 0, "", err
	}
	key := "key." + quotaHash(NormalizeAPIToken(token))
	var id quotaIdentity
	if err := q.load(ctx, key, &id); err != nil {
		return 0, "", err
	}
	account := id.Account
	if account == "" {
		account = key
	}
	var s quotaState
	if err := q.load(ctx, "usage."+account, &s); err != nil {
		return 0, "", err
	}
	fallback, err := q.fallback(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("%w: %w", errQuotaStorage, err)
	}
	background, _ := ctx.Value(backgroundQuotaKey{}).(bool)
	if status := q.status(&s, fallback, background); status.Deferred {
		return 0, "", &QuotaDeferredError{Status: status}
	}
	pacer := s.Pacing.throttle(q.now)
	delay, ok := pacer.reserve(ctx)
	if !ok {
		return 0, "", errThrottled
	}
	s.Pacing = pacingState(pacer)
	return delay, account, q.save(ctx, "usage."+account, &s)
}
