package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The highest-value test here: parse the manifest as actually committed.
// The parser and the file are a pair, and the failure this whole tool exists
// to catch is a silent one, so a reformat of deploy/telemetry-server.yaml
// that the regexp stops matching has to break a PR rather than turn the
// nightly check into a permanent red nobody reads.
func TestPinnedImageSHA_RealManifest(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/telemetry-server.yaml")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	sha, err := pinnedImageSHA(raw)
	if err != nil {
		t.Fatalf("pinnedImageSHA on the committed manifest: %v", err)
	}
	if len(sha) < 7 {
		t.Errorf("sha = %q, want at least 7 hex chars", sha)
	}
}

func TestPinnedImageSHA(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		want     string
		wantErr  bool
	}{
		{
			name:     "full sha, as committed",
			manifest: "        containers:\n          image: ghcr.io/vavallee/bindery-ping:sha-23586cd355e40700e2b60e8d9344ba4e2afd3cb3\n",
			want:     "23586cd355e40700e2b60e8d9344ba4e2afd3cb3",
		},
		{
			name:     "short sha",
			manifest: "          image: ghcr.io/vavallee/bindery-ping:sha-d2aee75\n",
			want:     "d2aee75",
		},
		{
			name:     "trailing whitespace is tolerated",
			manifest: "          image: ghcr.io/vavallee/bindery-ping:sha-abc1234   \n",
			want:     "abc1234",
		},
		{
			// The pin was set by hand once and never moved. Someone putting
			// it back on a floating tag would make the check meaningless, so
			// it has to be an error rather than a pass.
			name:     "floating latest tag is not a pin",
			manifest: "          image: ghcr.io/vavallee/bindery-ping:latest\n",
			wantErr:  true,
		},
		{
			name:     "commented out line does not count",
			manifest: "          # image: ghcr.io/vavallee/bindery-ping:sha-abc1234\n",
			wantErr:  true,
		},
		{
			// A second bindery-ping container (a sidecar, an initContainer)
			// would make "the pinned image" ambiguous. Refuse rather than
			// silently checking whichever came first.
			name: "two pinned images is ambiguous",
			manifest: "          image: ghcr.io/vavallee/bindery-ping:sha-abc1234\n" +
				"          image: ghcr.io/vavallee/bindery-ping:sha-def5678\n",
			wantErr: true,
		},
		{
			name:     "a different image is ignored",
			manifest: "          image: ghcr.io/vavallee/bindery:sha-abc1234\n",
			wantErr:  true,
		},
		{
			name:     "empty manifest",
			manifest: "",
			wantErr:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pinnedImageSHA([]byte(tc.manifest))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("pinnedImageSHA = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("pinnedImageSHA: %v", err)
			}
			if got != tc.want {
				t.Errorf("pinnedImageSHA = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPinnedImageSHA_NoPinIsDistinguishable(t *testing.T) {
	_, err := pinnedImageSHA([]byte("          image: ghcr.io/vavallee/bindery-ping:latest\n"))
	if !errors.Is(err, errNoPin) {
		t.Errorf("err = %v, want errNoPin so a read failure and a missing pin stay distinguishable", err)
	}
}

func TestCompareSHA(t *testing.T) {
	const full = "d2aee75fb3c9e49e8639d7016e73c4533d1e5af6"
	cases := []struct {
		name         string
		pinned, live string
		wantErr      bool
	}{
		{name: "identical full shas", pinned: full, live: full},
		{name: "pin short, live full", pinned: "d2aee75", live: full},
		{name: "pin full, live short", pinned: full, live: "d2aee75"},
		{name: "different builds", pinned: full, live: "23586cd355e40700e2b60e8d9344ba4e2afd3cb3", wantErr: true},
		// Both of these mean the running image predates the stamping, which
		// is by definition older than the pin that introduced the field.
		{name: "live reports the unstamped default", pinned: full, live: "unknown", wantErr: true},
		{name: "live reports nothing at all", pinned: full, live: "", wantErr: true},
		// The July build this tool was written for: differs from the pin in
		// the first character, so a prefix comparison must not pass it.
		{name: "the drift that started this", pinned: "d2aee75", live: "23586cd", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := compareSHA(tc.pinned, tc.live)
			if tc.wantErr && err == nil {
				t.Errorf("compareSHA(%q, %q) = nil, want an error", tc.pinned, tc.live)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("compareSHA(%q, %q) = %v, want nil", tc.pinned, tc.live, err)
			}
		})
	}
}

// compareSHA indexes both strings, so an empty pin must not panic. It cannot
// reach here through main (pinnedImageSHA rejects it first) but the function
// is exported to the test and should not be a trap.
func TestCompareSHA_EmptyPinDoesNotPanic(t *testing.T) {
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
	const pinned = "23586cd355e40700e2b60e8d9344ba4e2afd3cb3"

	// writeManifest returns a path to a manifest pinning the given sha.
	writeManifest := func(t *testing.T, sha string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "telemetry-server.yaml")
		body := "        containers:\n          - name: ping\n            image: ghcr.io/vavallee/bindery-ping:sha-" + sha + "\n"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		return p
	}
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
		t.Setenv("PING_MANIFEST", writeManifest(t, pinned))
		t.Setenv("PING_STATS_URL", stub(t, pinned))
		t.Setenv("TELEMETRY_STATS_TOKEN", "s3cret")

		var out strings.Builder
		if err := run(context.Background(), &out); err != nil {
			t.Fatalf("run: %v", err)
		}
		if !strings.Contains(out.String(), "in step") {
			t.Errorf("output = %q, want it to say the deployment is in step", out.String())
		}
	})

	t.Run("drift names the remedy", func(t *testing.T) {
		manifest := writeManifest(t, pinned)
		t.Setenv("PING_MANIFEST", manifest)
		t.Setenv("PING_STATS_URL", stub(t, "d2aee75fb3c9e49e8639d7016e73c4533d1e5af6"))
		t.Setenv("TELEMETRY_STATS_TOKEN", "s3cret")

		var out strings.Builder
		err := run(context.Background(), &out)
		if err == nil {
			t.Fatal("run = nil, want a drift error")
		}
		// The person reading a red nightly job needs the command, not just
		// the fact. This is the whole value of the check.
		if !strings.Contains(err.Error(), "kubectl apply -f "+manifest) {
			t.Errorf("err = %v, want it to name the kubectl command and the manifest", err)
		}
	})

	t.Run("an unstamped running build is drift", func(t *testing.T) {
		t.Setenv("PING_MANIFEST", writeManifest(t, pinned))
		t.Setenv("PING_STATS_URL", stub(t, "unknown"))
		t.Setenv("TELEMETRY_STATS_TOKEN", "s3cret")

		var out strings.Builder
		if err := run(context.Background(), &out); err == nil {
			t.Error("run = nil for a build reporting \"unknown\", want drift")
		}
	})

	t.Run("a missing token is an error, not a pass", func(t *testing.T) {
		t.Setenv("PING_MANIFEST", writeManifest(t, pinned))
		t.Setenv("PING_STATS_URL", stub(t, pinned))
		t.Setenv("TELEMETRY_STATS_TOKEN", "")

		var out strings.Builder
		err := run(context.Background(), &out)
		if err == nil {
			t.Fatal("run = nil with no token, want an error")
		}
		if !strings.Contains(err.Error(), "TELEMETRY_STATS_TOKEN") {
			t.Errorf("err = %v, want it to name the missing variable", err)
		}
	})

	t.Run("a missing manifest is an error", func(t *testing.T) {
		t.Setenv("PING_MANIFEST", filepath.Join(t.TempDir(), "absent.yaml"))
		t.Setenv("PING_STATS_URL", stub(t, pinned))
		t.Setenv("TELEMETRY_STATS_TOKEN", "s3cret")

		var out strings.Builder
		if err := run(context.Background(), &out); err == nil {
			t.Error("run = nil for a manifest that does not exist, want an error")
		}
	})

	// The default is the real endpoint, so a test that forgets to set
	// PING_STATS_URL must not reach out over the network.
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
