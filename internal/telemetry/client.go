// Package telemetry sends an anonymous once-daily ping to api.getbindery.dev so
// the project maintainer can count active installs. The ping carries:
//
//   - install_id  — random UUID generated on first run, stored in the DB
//   - version     — the running binary's version string
//   - os / arch   — runtime.GOOS / runtime.GOARCH
//
// No personal data, no hostnames, no library contents. The setting
// "telemetry.enabled" (default "true") can be set to "false" to opt out.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/google/uuid"
	"github.com/vavallee/bindery/internal/db"
)

const (
	pingURL          = "https://api.getbindery.dev/api/ping"
	settingEnabled   = "telemetry.enabled"
	settingInstallID = "telemetry.install_id"
	timeout          = 10 * time.Second
)

// Client sends anonymous usage pings and surfaces the latest published version.
type Client struct {
	settings      *db.SettingsRepo
	version       string
	latestVersion string
}

// New creates a telemetry client. version is the running binary's version string.
func New(settings *db.SettingsRepo, version string) *Client {
	return &Client{settings: settings, version: version}
}

// LatestVersion returns the most recently received latest-version string from
// the ping server. Empty string means no ping has succeeded yet.
func (c *Client) LatestVersion() string {
	return c.latestVersion
}

// Ping sends one anonymous ping if telemetry is enabled. It is safe to call
// from a goroutine; failures are logged at debug level and never returned.
func (c *Client) Ping(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if !c.isEnabled(ctx) {
		return
	}

	id, err := c.installID(ctx)
	if err != nil {
		slog.Debug("telemetry: could not resolve install ID", "error", err)
		return
	}

	payload, _ := json.Marshal(map[string]string{
		"install_id": id,
		"version":    c.version,
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pingURL, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Debug("telemetry: ping failed", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Debug("telemetry: ping returned non-200", "status", resp.StatusCode)
		return
	}

	var body struct {
		LatestVersion string `json:"latest_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil && body.LatestVersion != "" {
		c.latestVersion = body.LatestVersion
		slog.Debug("telemetry: ping ok", "latest", body.LatestVersion)
	}
}

func (c *Client) isEnabled(ctx context.Context) bool {
	s, _ := c.settings.Get(ctx, settingEnabled)
	if s == nil {
		return true // default on
	}
	return s.Value != "false"
}

// installID returns the persistent install UUID, creating it on first call.
func (c *Client) installID(ctx context.Context) (string, error) {
	s, err := c.settings.Get(ctx, settingInstallID)
	if err != nil {
		return "", fmt.Errorf("get install_id: %w", err)
	}
	if s != nil && s.Value != "" {
		return s.Value, nil
	}
	id := uuid.New().String()
	if err := c.settings.Set(ctx, settingInstallID, id); err != nil {
		return "", fmt.Errorf("set install_id: %w", err)
	}
	return id, nil
}
