package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompareSHA(t *testing.T) {
	const full = "d2aee75fb3c9e49e8639d7016e73c4533d1e5af6"
	cases := []struct {
		name           string
		expected, live string
		wantErr        bool
	}{
		{name: "identical full shas", expected: full, live: full},
		{name: "expected short, live full", expected: "d2aee75", live: full},
		{name: "expected full, live short", expected: full, live: "d2aee75"},
		{name: "different builds", expected: full, live: "23586cd355e40700e2b60e8d9344ba4e2afd3cb3", wantErr: true},
		// Both of these mean the running image predates the stamping, which
		// is by definition older than the pin that introduced the field.
		{name: "live reports the unstamped default", expected: full, live: "unknown", wantErr: true},
		{name: "live reports nothing at all", expected: full, live: "", wantErr: true},
		// The July build this tool was written for: differs from the pin in
		// the first character, so a prefix comparison must not pass it.
		{name: "the drift that started this", expected: "d2aee75", live: "23586cd", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := compareSHA(tc.expected, tc.live)
			if tc.wantErr && err == nil {
				t.Errorf("compareSHA(%q, %q) = nil, want an error", tc.expected, tc.live)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("compareSHA(%q, %q) = %v, want nil", tc.expected, tc.live, err)
			}
		})
	}
}

// compareSHA indexes both strings, so an empty expected sha must not panic.
// run() refuses before it can get here, but the function should not be a trap.
func TestCompareSHA_EmptyExpectedDoesNotPanic(t *testing.T) {
	if err := compareSHA("", "abc1234"); err != nil {
		t.Logf("compareSHA(\"\", ...) = %v", err)
	}
}

func TestLiveBuildSHA(t *testing.T) {
	t.Run("reads build_sha and sends the token", func(t *testing.T) {
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{"build_sha": "abc1234", "total": 7})
		}))
		defer srv.Close()

		got, err := liveBuildSHA(context.Background(), srv.URL, "s3cret")
		if err != nil {
			t.Fatalf("liveBuildSHA: %v", err)
		}
		if got != "abc1234" {
			t.Errorf("build_sha = %q, want %q", got, "abc1234")
		}
		if gotAuth != "Bearer s3cret" {
			t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer s3cret")
		}
	})

	t.Run("a server without the field yields empty, not an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"total": 7})
		}))
		defer srv.Close()

		got, err := liveBuildSHA(context.Background(), srv.URL, "s3cret")
		if err != nil {
			t.Fatalf("liveBuildSHA: %v", err)
		}
		if got != "" {
			t.Errorf("build_sha = %q, want empty so compareSHA reports it as drift", got)
		}
	})

	t.Run("a rejected token is an error, not silent drift", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}))
		defer srv.Close()

		if _, err := liveBuildSHA(context.Background(), srv.URL, "wrong"); err == nil {
			t.Error("liveBuildSHA = nil error on HTTP 401, want an error")
		} else if !strings.Contains(err.Error(), "401") {
			t.Errorf("err = %v, want it to name the status", err)
		}
	})
}

// PING_STATS_URL is a test seam, which makes it a taint source: it decides
// where a live bearer token gets sent. These are the cases that matter.
func TestCheckStatsURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "the real endpoint", url: "https://api.getbindery.dev/api/stats"},
		{name: "https anywhere is allowed", url: "https://example.test/api/stats"},
		{name: "loopback over http, the test stub", url: "http://127.0.0.1:18711/api/stats"},
		{name: "localhost over http", url: "http://localhost:18711/api/stats"},
		{name: "ipv6 loopback", url: "http://[::1]:18711/api/stats"},
		// The token must not leave the machine in the clear, and must not go
		// to a host someone picked by setting an environment variable.
		{name: "plain http to a remote host", url: "http://example.test/api/stats", wantErr: true},
		{name: "http to a non-loopback ip", url: "http://10.0.0.5/api/stats", wantErr: true},
		{name: "a scheme that is not http", url: "file:///etc/passwd", wantErr: true},
		{name: "no host", url: "https:///api/stats", wantErr: true},
		{name: "empty", url: "", wantErr: true},
		{name: "not a url", url: "://nope", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkStatsURL(tc.url)
			if tc.wantErr && err == nil {
				t.Errorf("checkStatsURL(%q) = nil, want an error", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("checkStatsURL(%q) = %v, want nil", tc.url, err)
			}
		})
	}
}

// liveBuildSHA must refuse before it sends anything, not after.
func TestLiveBuildSHA_RefusesABadURLWithoutDialling(t *testing.T) {
	if _, err := liveBuildSHA(context.Background(), "http://example.test/api/stats", "s3cret"); err == nil {
		t.Error("liveBuildSHA accepted plain http to a remote host")
	}
}

// run is the whole path the workflow takes, so it is worth exercising end to
// end against a stub rather than leaving it as untested glue.
func TestRun(t *testing.T) {
	const expected = "f1d124a2058468314a3bd0565e6aa9d8a38b32f6"

	// stub serves the given build_sha on any authorised request.
	stub := func(t *testing.T, sha string) string {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"build_sha": sha})
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}

	t.Run("in step", func(t *testing.T) {
		t.Setenv("PING_EXPECTED_SHA", expected)
		t.Setenv("PING_STATS_URL", stub(t, expected))
		t.Setenv("TELEMETRY_STATS_TOKEN", "s3cret")

		var out strings.Builder
		if err := run(context.Background(), &out); err != nil {
			t.Fatalf("run: %v", err)
		}
		if !strings.Contains(out.String(), "in step") {
			t.Errorf("output = %q, want it to say the deployment is in step", out.String())
		}
	})

	t.Run("drift names where the deployment actually lives", func(t *testing.T) {
		t.Setenv("PING_EXPECTED_SHA", expected)
		t.Setenv("PING_STATS_URL", stub(t, "e0eb8ac497fcd3228ee9050a3a6d5cf8905ac366"))
		t.Setenv("TELEMETRY_STATS_TOKEN", "s3cret")

		var out strings.Builder
		err := run(context.Background(), &out)
		if err == nil {
			t.Fatal("run = nil, want a drift error")
		}
		// Whoever reads a red nightly job cannot fix it from this repo. The
		// error has to say which repo and which file, or it is a dead end.
		for _, want := range []string{"homelab", "kubernetes/apps/default/bindery-ping/deployment.yaml"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("an unstamped running build is drift", func(t *testing.T) {
		t.Setenv("PING_EXPECTED_SHA", expected)
		t.Setenv("PING_STATS_URL", stub(t, "unknown"))
		t.Setenv("TELEMETRY_STATS_TOKEN", "s3cret")

		var out strings.Builder
		if err := run(context.Background(), &out); err == nil {
			t.Error("run = nil for a build reporting \"unknown\", want drift")
		}
	})

	t.Run("a missing token is an error, not a pass", func(t *testing.T) {
		t.Setenv("PING_EXPECTED_SHA", expected)
		t.Setenv("PING_STATS_URL", stub(t, expected))
		t.Setenv("TELEMETRY_STATS_TOKEN", "")

		var out strings.Builder
		err := run(context.Background(), &out)
		if err == nil || !strings.Contains(err.Error(), "TELEMETRY_STATS_TOKEN") {
			t.Errorf("err = %v, want it to name the missing variable", err)
		}
	})

	// Without this the tool would compare against the empty string and pass
	// everything, which is the silent green this whole check exists to avoid.
	t.Run("a missing expected sha is an error, not a pass", func(t *testing.T) {
		t.Setenv("PING_EXPECTED_SHA", "")
		t.Setenv("PING_STATS_URL", stub(t, expected))
		t.Setenv("TELEMETRY_STATS_TOKEN", "s3cret")

		var out strings.Builder
		err := run(context.Background(), &out)
		if err == nil || !strings.Contains(err.Error(), "PING_EXPECTED_SHA") {
			t.Errorf("err = %v, want it to name the missing variable", err)
		}
	})

	t.Run("the default stats URL is the production endpoint", func(t *testing.T) {
		t.Setenv("PING_STATS_URL", "")
		if got := env("PING_STATS_URL", "https://api.getbindery.dev/api/stats"); got != "https://api.getbindery.dev/api/stats" {
			t.Errorf("default = %q", got)
		}
	})
}

func TestOrPlaceholder(t *testing.T) {
	if got := orPlaceholder(""); got != "<absent>" {
		t.Errorf("orPlaceholder(\"\") = %q, want %q", got, "<absent>")
	}
	if got := orPlaceholder("abc1234"); got != "abc1234" {
		t.Errorf("orPlaceholder(%q) = %q, want it unchanged", "abc1234", got)
	}
}
