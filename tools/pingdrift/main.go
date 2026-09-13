// Command pingdrift compares the build api.getbindery.dev reports against the
// newest commit here that would have produced a bindery-ping image.
//
// The deployment lives in a private infrastructure repo and is synced by
// ArgoCD with selfHeal, so this repo cannot see, read or change what is
// deployed. It can see two things: what the running server says it is, and
// what the newest build from this tree would be. When those part company,
// someone has to bump the image over there.
//
// It exists because that gap ran to 41 days once. The service served an end
// of life base with two High advisories for weeks after the fix landed here,
// and the only reason anyone noticed was a log line missing from a pod.
//
// It is a Go tool rather than shell in a workflow step so the comparison and
// the HTTP read are unit-testable, matching tools/licensegen.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

// compareSHA reports whether the running build matches the one expected.
//
// The comparison is on the shorter of the two, so a short sha and a full one
// still compare equal.
func compareSHA(expected, live string) error {
	// A build from before the stamping change reports the "unknown" default,
	// or nothing at all on a build from before the field existed. Both mean
	// the running image is older than the change that added the field, so
	// both are drift rather than an inconclusive result.
	switch live {
	case "":
		return errors.New("the running server reports no build sha at all, so it predates the build_sha field entirely")
	case "unknown":
		return errors.New("the running server reports build sha \"unknown\", so it was built without BUILD_SHA and predates the stamping")
	}

	n := len(expected)
	if len(live) < n {
		n = len(live)
	}
	if expected[:n] != live[:n] {
		return fmt.Errorf("running %s but this tree's newest bindery-ping build is %s", live, expected)
	}
	return nil
}

// checkStatsURL constrains where the bearer token may be sent.
//
// The URL is overridable through PING_STATS_URL so the tests can point at a
// stub, which makes it a taint source: without this, anyone who can set the
// environment of this job redirects a live credential to a host of their
// choosing. https everywhere, except a loopback host, which is the only
// shape the test seam needs and cannot leave the machine.
func checkStatsURL(raw string) error {
	u, err := parseURL(raw)
	if err != nil {
		return fmt.Errorf("PING_STATS_URL is not a valid URL: %w", err)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && isLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("PING_STATS_URL must be https, or http on loopback for tests, got %q", raw)
}

func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, errors.New("no host")
	}
	return u, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// liveBuildSHA reads build_sha from the token-gated stats endpoint. The sha
// is not on public /health deliberately: an exact build identifier tells a
// stranger which advisories apply, and this caller already holds the token.
func liveBuildSHA(ctx context.Context, statsURL, token string) (string, error) {
	if err := checkStatsURL(statsURL); err != nil {
		return "", err
	}

	// #nosec G704 -- statsURL is checked by checkStatsURL above: https, or
	// http on loopback for the test stub. Nothing here reaches an arbitrary
	// host with the token.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req) // #nosec G704 -- see above
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned HTTP %d", statsURL, resp.StatusCode)
	}

	var body struct {
		BuildSHA string `json:"build_sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode %s: %w", statsURL, err)
	}
	return body.BuildSHA, nil
}

// run does the work and returns an error rather than exiting, so the whole
// path is reachable from a test. main is the only part that cannot be, and
// it is now three lines.
func run(ctx context.Context, out io.Writer) error {
	// The workflow computes this with a single git command, because "the
	// newest commit touching cmd/telemetry-server" is git's question, not
	// this tool's. Everything worth testing stays here.
	expected := os.Getenv("PING_EXPECTED_SHA")
	if expected == "" {
		return errors.New("PING_EXPECTED_SHA is not set, so there is nothing to compare the running build against")
	}
	statsURL := env("PING_STATS_URL", "https://api.getbindery.dev/api/stats")
	token := os.Getenv("TELEMETRY_STATS_TOKEN")
	if token == "" {
		return errors.New("TELEMETRY_STATS_TOKEN is not set, cannot read the running build")
	}

	live, err := liveBuildSHA(ctx, statsURL, token)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "newest bindery-ping build in this tree: %s\n", expected)
	_, _ = fmt.Fprintf(out, "running on api.getbindery.dev:           %s\n", orPlaceholder(live))

	if err := compareSHA(expected, live); err != nil {
		return fmt.Errorf("bindery-ping drift: %w. The deployment is in the homelab repo at kubernetes/apps/default/bindery-ping/deployment.yaml; bump the image there and ArgoCD syncs it", err)
	}
	_, _ = fmt.Fprintln(out, "in step")
	return nil
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		// A GitHub Actions error annotation, so the reason lands on the run
		// summary rather than only in the log.
		fmt.Printf("::error::%v\n", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func orPlaceholder(s string) string {
	if s == "" {
		return "<absent>"
	}
	return s
}
