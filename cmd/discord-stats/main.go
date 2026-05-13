// discord-stats is a tiny one-shot binary that updates three Discord voice
// channel names with live Bindery telemetry numbers and GitHub stars.
//
// It is intended to run as a Kubernetes CronJob every 10 minutes — Discord
// allows at most 2 channel renames per 10-minute window per channel, so the
// cron schedule matches the rate limit by construction. Each tick:
//
//  1. Fetches active install count + latest version from BINDERY_STATS_URL.
//  2. Fetches GitHub stargazer count from the public repo API.
//  3. For each channel, compares the desired name to the current name and
//     PATCHes only when they differ — every idempotent tick costs 3 GETs and
//     0 PATCHes against the renames-per-10min budget.
//
// The binary uses only the standard library on purpose; we don't need a
// gateway connection or anything beyond a few REST calls.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	discordAPI    = "https://discord.com/api/v10"
	githubAPI     = "https://api.github.com"
	httpTimeout   = 15 * time.Second
	defaultStats  = "https://api.getbindery.dev/stats.json"
	defaultRepo   = "vavallee/bindery"
	userAgent     = "bindery-discord-stats (+https://github.com/vavallee/bindery)"
	maxRetryAfter = 30 * time.Second // cap the sleep before giving up on a 429
)

type statsJSON struct {
	Active int    `json:"active"`
	Total  int    `json:"total"`
	Latest string `json:"latest"`
}

type ghRepo struct {
	Stargazers int `json:"stargazers_count"`
}

type discordChannel struct {
	Name string `json:"name"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	token := mustEnv("DISCORD_BOT_TOKEN")
	activeID := mustEnv("DISCORD_ACTIVE_CHANNEL_ID")
	latestID := mustEnv("DISCORD_LATEST_CHANNEL_ID")
	starsID := mustEnv("DISCORD_STARS_CHANNEL_ID")
	statsURL := envOr("BINDERY_STATS_URL", defaultStats)
	repo := envOr("GITHUB_REPO", defaultRepo)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client := &http.Client{Timeout: httpTimeout}

	stats, statsErr := fetchStats(ctx, client, statsURL)
	if statsErr != nil {
		slog.Error("fetch stats", "url", statsURL, "error", statsErr)
		// Continue: stars-only update is still worthwhile.
	} else {
		slog.Info("stats", "active", stats.Active, "latest", stats.Latest)
	}

	stars, starsErr := fetchStars(ctx, client, repo)
	if starsErr != nil {
		slog.Error("fetch stars", "repo", repo, "error", starsErr)
	} else {
		slog.Info("stars", "count", stars)
	}

	type update struct {
		label string
		id    string
		name  string
		ok    bool
	}
	var updates []update
	if stats != nil {
		updates = append(updates,
			update{"active", activeID, fmt.Sprintf("📊 Active: %d", stats.Active), true},
			update{"latest", latestID, fmt.Sprintf("📦 Latest: %s", stats.Latest), true},
		)
	} else {
		updates = append(updates,
			update{"active", activeID, "", false},
			update{"latest", latestID, "", false},
		)
	}
	updates = append(updates,
		update{"stars", starsID, fmt.Sprintf("⭐ Stars: %d", stars), starsErr == nil},
	)

	for _, u := range updates {
		if !u.ok {
			slog.Warn("skip channel: upstream fetch failed", "channel", u.label)
			continue
		}
		if err := renameChannel(ctx, client, token, u.id, u.name); err != nil {
			slog.Error("rename channel", "channel", u.label, "id", u.id, "name", u.name, "error", err)
			continue
		}
	}
	// Exit 0 even on partial failure — the next cron tick will catch up, and we
	// don't want a transient Discord 5xx to alert on the CronJob itself.
}

// fetchStats GETs the Bindery telemetry /stats.json payload.
func fetchStats(ctx context.Context, c *http.Client, url string) (*statsJSON, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stats: HTTP %d", resp.StatusCode)
	}
	var s statsJSON
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("stats: decode: %w", err)
	}
	return &s, nil
}

// fetchStars GETs the GitHub repo metadata and returns the star count.
func fetchStars(ctx context.Context, c *http.Client, repo string) (int, error) {
	url := githubAPI + "/repos/" + repo
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("github: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var r ghRepo
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, fmt.Errorf("github: decode: %w", err)
	}
	return r.Stargazers, nil
}

// renameChannel reads the current channel name and PATCHes it only when the
// desired name differs. Handles Discord 429 once via Retry-After.
func renameChannel(ctx context.Context, c *http.Client, token, id, want string) error {
	current, err := getChannelName(ctx, c, token, id)
	if err != nil {
		return fmt.Errorf("read current name: %w", err)
	}
	if current == want {
		slog.Info("channel already up-to-date", "id", id, "name", want)
		return nil
	}
	slog.Info("renaming channel", "id", id, "from", current, "to", want)

	body, _ := json.Marshal(discordChannel{Name: want})
	for attempt := 0; attempt < 2; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, discordAPI+"/channels/"+id, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bot "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		resp, err := c.Do(req)
		if err != nil {
			return err
		}
		// Drain & close so the connection can be reused on retry.
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return nil
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := parseRetryAfter(resp.Header.Get("Retry-After"))
			if wait > maxRetryAfter {
				return fmt.Errorf("discord 429: Retry-After=%s exceeds cap", wait)
			}
			slog.Warn("discord 429, sleeping", "wait", wait, "id", id)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			return fmt.Errorf("discord PATCH HTTP %d: %s", resp.StatusCode, string(respBody))
		}
	}
	return errors.New("discord 429: gave up after one retry")
}

// getChannelName fetches just the current channel's name. We rely on Discord
// to return a JSON object; we only decode the one field we need.
func getChannelName(ctx context.Context, c *http.Client, token, id string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, discordAPI+"/channels/"+id, nil)
	req.Header.Set("Authorization", "Bot "+token)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("discord GET HTTP %d: %s", resp.StatusCode, string(body))
	}
	var ch discordChannel
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return "", fmt.Errorf("discord GET decode: %w", err)
	}
	return ch.Name, nil
}

// parseRetryAfter accepts either an integer seconds value (the form Discord
// uses) or a float; fall back to 5s on any parse failure.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 5 * time.Second
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
		return time.Duration(f * float64(time.Second))
	}
	return 5 * time.Second
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required env var missing", "key", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
