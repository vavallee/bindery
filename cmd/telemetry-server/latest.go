package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// latestVersionInterval is how often runLatestVersionLoop re-checks GitHub for
// a newer release. A var so tests can shorten it. The unauthenticated GitHub
// API allows 60 requests/hour per IP; 30 min (2/h) leaves ample headroom.
var latestVersionInterval = 30 * time.Minute

// githubAPIBase is the GitHub REST base URL; a var so tests can point it at a
// local stub server.
var githubAPIBase = "https://api.github.com"

var latestVersionClient = &http.Client{Timeout: 15 * time.Second}

// fetchLatestRelease returns the tag_name of the newest published (non-draft,
// non-prerelease) release for repo ("owner/name"), e.g. "v1.22.1".
func fetchLatestRelease(ctx context.Context, repo string) (string, error) {
	url := githubAPIBase + "/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "bindery-telemetry-server (+https://github.com/vavallee/bindery)")
	resp, err := latestVersionClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github releases/latest: status %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	return body.TagName, nil
}

// runLatestVersionLoop keeps s.latest() in sync with the newest GitHub release
// so a new release advertises itself without anyone pushing LATEST_VERSION into
// this cluster. The previous mechanism (`kubectl set env` from the release CI
// job) silently no-opped every release because the GitHub runner cannot reach
// this cluster's API server. The LATEST_VERSION env now serves only as the
// seed/fallback: used until the first successful poll and whenever GitHub is
// unreachable. Best-effort — a failed poll keeps the last known value.
func (s *server) runLatestVersionLoop(ctx context.Context, repo string) {
	if repo == "" {
		return
	}
	check := func() {
		v, err := fetchLatestRelease(ctx, repo)
		if err != nil {
			slog.Warn("latest-version poll failed", "error", err)
			return
		}
		if !isReleaseVersion(v) {
			slog.Warn("latest-version poll: unexpected tag", "tag", v)
			return
		}
		if v != s.latest() {
			slog.Info("latest version updated", "from", s.latest(), "to", v)
			s.setLatest(v)
		}
	}
	check()
	t := time.NewTicker(latestVersionInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}
