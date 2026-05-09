// Package telemetry sends an anonymous once-daily ping to api.getbindery.dev so
// the project maintainer can count active installs. The ping carries:
//
//   - install_id  — random UUID generated on first run, stored in the DB
//   - version     — the running binary's version string
//   - os / arch   — runtime.GOOS / runtime.GOARCH
//   - deploy      — kubernetes / docker / binary (best-effort runtime detect)
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
	"os"
	"regexp"
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

// releaseVersionPattern matches semver-shaped release tags ("1.7.0", "v1.7.0").
// CI builds inject non-matching strings — the literal "dev" when the binary
// has no -ldflags, "sha-abc1234" / "dev-abc1234" for non-release branches,
// and "v1.7.0-3-gabc1234" (git describe form) for commits past a tag — and
// we want those builds to skip the ping so the active-install chart doesn't
// fill up with throwaway version buckets, one per CI commit.
var releaseVersionPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// pingClient is a dedicated HTTP client for the telemetry ping path. We avoid
// http.DefaultClient so that other code mutating DefaultClient (transport,
// timeout, jar) can't reach into our request, and so the per-request 10s
// context deadline is backstopped by an explicit transport-level timeout.
var pingClient = &http.Client{
	Timeout: timeout, // 10s — same as the context deadline; belt-and-suspenders
}

// isReleaseVersion reports whether v looks like a semver release tag.
func isReleaseVersion(v string) bool {
	return releaseVersionPattern.MatchString(v)
}

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

	// Skip pings from non-release builds. CI tags interim images with
	// strings like "sha-abc1234" (non-release branches), "dev-abc1234"
	// (development branch), and "v1.7.0-3-gabc1234" (git describe for
	// commits past a tag); local builds with no -ldflags inject the
	// literal "dev". Each unique label becomes its own row in the version
	// histogram on api.getbindery.dev/stats — and most of those installs
	// are throwaway dev/CI containers that recreate the DB (and the
	// install_id) on every run, inflating active counts.
	//
	// Set BINDERY_TELEMETRY_FORCE=1 to override (e.g. when smoke-testing
	// the ping path against a local telemetry-server).
	if !isReleaseVersion(c.version) && os.Getenv("BINDERY_TELEMETRY_FORCE") == "" {
		slog.Debug("telemetry: skipping ping for non-release build", "version", c.version)
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
		"deploy":     detectDeploy(),
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pingURL, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := pingClient.Do(req)
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
	// BINDERY_TELEMETRY_DISABLED=true lets users opt out before any DB setting
	// exists (e.g. on first boot, before the startup ping would fire).
	if os.Getenv("BINDERY_TELEMETRY_DISABLED") == "true" {
		return false
	}
	s, _ := c.settings.Get(ctx, settingEnabled)
	if s == nil {
		return true // default on
	}
	return s.Value != "false"
}

// detectDeploy returns a best-effort label for how this binary is being run:
// "kubernetes" inside a pod, "docker" inside any other container, "binary"
// otherwise. Pod detection wins over docker because every k8s pod also has
// a container runtime underneath. The Helm chart can override by setting
// BINDERY_DEPLOY_METHOD if it wants to distinguish helm from raw manifests.
func detectDeploy() string {
	if v := os.Getenv("BINDERY_DEPLOY_METHOD"); v != "" {
		return v
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return "kubernetes"
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	return "binary"
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
