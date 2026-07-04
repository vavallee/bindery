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
	"net/url"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/useragent"
)

const (
	EventGrabbed        = "grabbed"
	EventBookImported   = "bookImported"
	EventDownloadFailed = "downloadFailed"
	EventHealth         = "health"
	EventUpgrade        = "upgrade"
)

// normalizeEventPayload gives every event a consistent, human-readable shape so
// relays that render without a template (ntfy, Apprise) read well, and so a
// template can branch on the event (#1323). It sets `title` to *what happened*
// (the event) and `message` to *the subject*, mirroring the Sonarr/Radarr shape
// — previously `title` carried the item and `message` was often absent, so the
// notification showed the same string twice and never said what occurred. The
// original item name is preserved under `item`, and `eventType` is added to
// every payload (not just `test`) so templates can key off it. All original
// fields (size, format, path, status, clientId, …) are kept for templaters.
func normalizeEventPayload(eventType string, payload map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(payload)+3)
	for k, v := range payload {
		out[k] = v
	}
	out["eventType"] = eventType

	item, _ := out["title"].(string)
	if item != "" {
		out["item"] = item
	}
	msg, _ := out["message"].(string)
	author, _ := out["author"].(string)
	format, _ := out["format"].(string)
	status, _ := out["status"].(string)

	var title, body string
	switch eventType {
	case EventGrabbed:
		title, body = "Release Grabbed", item
		if author != "" {
			body = item + " · " + author
		}
	case EventBookImported:
		title, body = "Book Imported", item
		if format != "" {
			body = item + " (" + format + ")"
		}
	case EventUpgrade:
		title, body = "Book Upgraded", item
		if format != "" {
			body = item + " (" + format + ")"
		}
	case EventDownloadFailed:
		title = "Download Failed"
		switch {
		case item != "" && msg != "":
			body = item + ": " + msg
		case item != "":
			body = item
		default:
			body = msg
		}
	case EventHealth:
		title = "Download Client Unhealthy"
		if body = msg; body == "" {
			body = status
		}
	case "test":
		title = "Bindery Test"
		if body = msg; body == "" {
			body = "Bindery notification test"
		}
	default:
		title, body = item, msg
	}
	out["title"] = title
	out["message"] = body
	return out
}

// ntfyRootURL strips a topic URL (https://ntfy.sh/mytopic) down to the server
// root (https://ntfy.sh/) so a JSON body with a "topic" field publishes
// natively. Returns raw unchanged if it can't be parsed.
func ntfyRootURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Path = "/"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

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
	policy := httpsec.PolicyFromEnv(httpsec.PolicyStrict, "BINDERY_NOTIFICATIONS_ALLOW_PRIVATE")
	return &Notifier{
		repo: repo,
		http: &http.Client{
			Timeout:   10 * time.Second,
			Transport: guardedTransport(policy),
			// Re-validate every redirect hop. The up-front validate only checks
			// the configured URL; a webhook host that passes it could otherwise
			// 302 into loopback / RFC1918 / cloud-metadata and have Bindery
			// follow it blindly.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				if err := httpsec.ValidateOutboundURL(req.URL.String(), policy); err != nil {
					return fmt.Errorf("redirect blocked: %w", err)
				}
				return nil
			},
		},
		validate: func(u string) error {
			return httpsec.ValidateOutboundURL(u, policy)
		},
	}
}

// guardedTransport mirrors the image-proxy transport. On the direct path it
// installs a per-dial SSRF-revalidating DialContext, closing the DNS-rebind
// TOCTOU between the up-front validate and the actual connect. When an outbound
// proxy is configured the dial targets the operator-trusted proxy (not the
// webhook host), so the strict per-dial recheck is skipped and the up-front
// validate + CheckRedirect carry the guard.
func guardedTransport(policy httpsec.Policy) http.RoundTripper {
	base := httpsec.DefaultProxyTransport()
	if httpsec.ProxyFunc() != nil {
		return base
	}
	if t, ok := base.(*http.Transport); ok {
		c := t.Clone()
		c.DialContext = httpsec.NewDialContext(policy)
		return c
	}
	return &http.Transport{DialContext: httpsec.NewDialContext(policy)}
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

	out := normalizeEventPayload(eventType, payload)
	for _, notif := range notifications {
		if !notif.Enabled {
			continue
		}
		if !n.matchesEvent(&notif, eventType) {
			continue
		}
		if err := n.send(ctx, &notif, out); err != nil {
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
	payload := normalizeEventPayload("test", map[string]interface{}{})
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

	// Apprise's REST API (apprise-api) requires a "body" field and rejects any
	// payload without one ("Payload lacks minimum requirements"). Enrich a copy
	// of the payload with Apprise-friendly "body"/"title" fields so those
	// endpoints work out of the box. This is purely additive: the original keys
	// (title, message, and event-specific fields) are preserved, so existing
	// ntfy / Home Assistant / Discord-proxy consumers are unaffected. We copy
	// rather than mutate because Send reuses one payload map across every
	// configured notification.
	out := make(map[string]interface{}, len(payload)+2)
	for k, v := range payload {
		out[k] = v
	}
	if _, ok := out["body"]; !ok {
		if msg, _ := out["message"].(string); msg != "" {
			out["body"] = msg
		} else if title, _ := out["title"].(string); title != "" {
			out["body"] = title
		}
	}
	if title, _ := out["title"].(string); title == "" {
		out["title"] = "Bindery"
	}

	// When a topic is configured, publish to the ntfy server root with the topic
	// in the JSON body. ntfy only parses a JSON body at the root URL; POSTed to a
	// topic URL it treats the body as plain text and prints the JSON verbatim
	// (#1323). The host is unchanged, so the URL already passed validation above.
	target := notif.URL
	if topic := strings.TrimSpace(notif.Topic); topic != "" {
		out["topic"] = topic
		target = ntfyRootURL(notif.URL)
	}

	body, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	method := strings.ToUpper(notif.Method)
	if method == "" {
		method = http.MethodPost
	}

	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", useragent.Get())

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
