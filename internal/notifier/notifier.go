// Package notifier dispatches webhook notifications for grab, import,
// failure, and health events to user-configured HTTP endpoints.
package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

const (
	EventGrabbed        = "grabbed"
	EventBookImported   = "bookImported"
	EventDownloadFailed = "downloadFailed"
	EventHealth         = "health"
	EventUpgrade        = "upgrade"
)

// Notifier dispatches webhook notifications for Bindery events.
type Notifier struct {
	repo *db.NotificationRepo
	http *http.Client
	// validate is the SSRF guard applied before every send. Overridable so
	// tests can point at httptest.NewServer (which binds on loopback and
	// would otherwise be rejected by the production validator).
	validate func(url string) error
}

// New creates a Notifier backed by the given repo.
func New(repo *db.NotificationRepo) *Notifier {
	return &Notifier{
		repo: repo,
		http: &http.Client{Timeout: 10 * time.Second},
		validate: func(u string) error {
			policy := httpsec.PolicyFromEnv(httpsec.PolicyStrict, "BINDERY_NOTIFICATIONS_ALLOW_PRIVATE")
			return httpsec.ValidateOutboundURL(u, policy)
		},
	}
}

// SetValidator overrides the SSRF validator. Intended for tests that need to
// target httptest.NewServer (loopback). Pass nil to disable validation.
func (n *Notifier) SetValidator(fn func(string) error) {
	n.validate = fn
}

// Send loads all enabled notifications, filters by eventType, and fires HTTP
// webhooks for each matching notification.
func (n *Notifier) Send(ctx context.Context, eventType string, payload map[string]interface{}) {
	notifications, err := n.repo.List(ctx)
	if err != nil {
		slog.Error("notifier: failed to load notifications", "error", err)
		return
	}

	for _, notif := range notifications {
		if !notif.Enabled {
			continue
		}
		if !n.matchesEvent(&notif, eventType) {
			continue
		}
		if err := n.send(ctx, &notif, payload); err != nil {
			slog.Error("notifier: failed to send notification",
				"id", notif.ID,
				"name", notif.Name,
				"event", eventType,
				"error", err)
		}
	}
}

// Test sends a test payload to a single notification without persisting it.
func (n *Notifier) Test(ctx context.Context, notification *models.Notification) error {
	payload := map[string]interface{}{
		"eventType": "test",
		"message":   "Bindery notification test",
	}
	return n.send(ctx, notification, payload)
}

// matchesEvent returns true if the notification is configured to fire for eventType.
func (n *Notifier) matchesEvent(notif *models.Notification, eventType string) bool {
	switch eventType {
	case EventGrabbed:
		return notif.OnGrab
	case EventBookImported:
		return notif.OnImport
	case EventDownloadFailed:
		return notif.OnFailure
	case EventHealth:
		return notif.OnHealth
	case EventUpgrade:
		return notif.OnUpgrade
	}
	return false
}

// send performs the actual HTTP request for a single notification.
func (n *Notifier) send(ctx context.Context, notif *models.Notification, payload map[string]interface{}) error {
	if notif.URL == "" {
		return fmt.Errorf("notification %d has no URL configured", notif.ID)
	}

	// Revalidate URL at send time. A bad URL should never have reached here
	// (handlers validate on create/update), but a persisted row could predate
	// the validator, and DNS can shift after a row was saved.
	if n.validate != nil {
		if err := n.validate(notif.URL); err != nil {
			return fmt.Errorf("url not allowed: %w", err)
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	method := strings.ToUpper(notif.Method)
	if method == "" {
		method = http.MethodPost
	}

	req, err := http.NewRequestWithContext(ctx, method, notif.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Bindery/1.0")

	// Parse and apply extra headers stored as a JSON object.
	if notif.Headers != "" && notif.Headers != "{}" {
		var extraHeaders map[string]string
		if err := json.Unmarshal([]byte(notif.Headers), &extraHeaders); err == nil {
			for k, v := range extraHeaders {
				req.Header.Set(k, v)
			}
		}
	}

	resp, err := n.http.Do(req)
	if err != nil {
		return fmt.Errorf("send webhook: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}
