package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// pingRecorder is an httptest server standing in for api.getbindery.dev. It
// records every ping body and replies with a configurable status and body.
type pingRecorder struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	ua     string
	status int
	reply  string
}

func newPingRecorder(t *testing.T) *pingRecorder {
	t.Helper()
	p := &pingRecorder{status: http.StatusOK, reply: `{"latest_version":"2.0.0"}`}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, b)
		p.ua = r.Header.Get("User-Agent")
		status, reply := p.status, p.reply
		p.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(p.Close)
	orig := pingURL
	pingURL = p.URL
	t.Cleanup(func() { pingURL = orig })
	return p
}

func (p *pingRecorder) payloads(t *testing.T) []pingPayload {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]pingPayload, 0, len(p.bodies))
	for _, b := range p.bodies {
		var pp pingPayload
		if err := json.Unmarshal(b, &pp); err != nil {
			t.Fatalf("ping body is not JSON: %v (%s)", err, b)
		}
		out = append(out, pp)
	}
	return out
}

// newSettings opens an in-memory DB. Migrations make each open cost most of
// a second, so tests share one per top-level test where they can.
func newSettings(t *testing.T) *db.SettingsRepo {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return db.NewSettingsRepo(database)
}

// clearDeployEnv makes detectDeploy and the ping gates deterministic
// regardless of where the test runs, leaving only the /.dockerenv probe.
func clearDeployEnv(t *testing.T) {
	t.Setenv("BINDERY_DEPLOY_METHOD", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("BINDERY_TELEMETRY_DISABLED", "")
	t.Setenv("BINDERY_TELEMETRY_FORCE", "")
}

func TestPing_NonReleaseBuildSkippedUnlessForced(t *testing.T) {
	clearDeployEnv(t)
	rec := newPingRecorder(t)
	settings := newSettings(t)
	ctx := context.Background()

	for _, v := range []string{"dev", "sha-abc1234", "v1.7.0-3-gabc1234"} {
		New(settings, v).Ping(ctx)
	}
	if n := len(rec.payloads(t)); n != 0 {
		t.Fatalf("non-release builds sent %d pings, want 0", n)
	}
	// Skipped pings must not mint an install ID either.
	if s, _ := settings.Get(ctx, settingInstallID); s != nil {
		t.Errorf("install_id created by a skipped ping: %+v", s)
	}

	t.Setenv("BINDERY_TELEMETRY_FORCE", "1")
	New(settings, "dev").Ping(ctx)
	got := rec.payloads(t)
	if len(got) != 1 || got[0].Version != "dev" {
		t.Fatalf("forced ping = %+v, want one ping carrying version dev", got)
	}
}

func TestPing_InstallIDStableAndFeaturesSent(t *testing.T) {
	clearDeployEnv(t)
	t.Setenv("BINDERY_DEPLOY_METHOD", "helm")
	rec := newPingRecorder(t)
	settings := newSettings(t)
	ctx := context.Background()

	calls := 0
	c := New(settings, "v1.2.3").WithGatherer(func(context.Context) Features {
		calls++
		return Features{Indexers: 2, DownloadClients: 1}
	})
	c.Ping(ctx)
	c.Ping(ctx)

	got := rec.payloads(t)
	if len(got) != 2 {
		t.Fatalf("got %d pings, want 2", len(got))
	}
	if got[0].InstallID == "" || got[0].InstallID != got[1].InstallID {
		t.Errorf("install IDs %q / %q: want one persistent non-empty ID", got[0].InstallID, got[1].InstallID)
	}
	if got[0].Deploy != "helm" {
		t.Errorf("deploy = %q, want the BINDERY_DEPLOY_METHOD override", got[0].Deploy)
	}
	if got[0].Features == nil || got[0].Features.Indexers != 2 || got[0].Features.DownloadClients != 1 {
		t.Errorf("features = %+v", got[0].Features)
	}
	if got[0].Errors != nil {
		t.Errorf("errors section sent without an errors gatherer: %+v", got[0].Errors)
	}
	if calls != 2 {
		t.Errorf("gatherer called %d times, want once per ping", calls)
	}
	rec.mu.Lock()
	ua := rec.ua
	rec.mu.Unlock()
	if ua == "" {
		t.Error("ping sent without a User-Agent")
	}
	if c.LatestVersion() != "2.0.0" {
		t.Errorf("LatestVersion = %q", c.LatestVersion())
	}
	// The install-created anchor is stamped alongside the install ID.
	if s, _ := settings.Get(ctx, SettingInstallCreatedAt); s == nil || s.Value == "" {
		t.Error("install_created_at anchor not stamped")
	}
}

func TestPing_ReplyHandling(t *testing.T) {
	settings := newSettings(t)
	cases := []struct {
		name   string
		status int
		reply  string
	}{
		{"non-200", http.StatusInternalServerError, `{"latest_version":"6.6.6"}`},
		{"malformed JSON", http.StatusOK, `{"latest_version":`},
		{"empty latest", http.StatusOK, `{"latest_version":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearDeployEnv(t)
			rec := newPingRecorder(t)
			rec.status, rec.reply = tc.status, tc.reply
			c := New(settings, "1.0.0")
			c.latestVersion.Store("1.0.1")
			c.Ping(context.Background())
			if n := len(rec.payloads(t)); n != 1 {
				t.Fatalf("server saw %d pings, want 1", n)
			}
			if got := c.LatestVersion(); got != "1.0.1" {
				t.Errorf("LatestVersion = %q, want previous value kept", got)
			}
		})
	}
}

func TestPing_TransportFailuresAreSilent(t *testing.T) {
	settings := newSettings(t)

	t.Run("unreachable server", func(t *testing.T) {
		clearDeployEnv(t)
		srv := httptest.NewServer(http.NotFoundHandler())
		dead := srv.URL
		srv.Close()
		orig := pingURL
		pingURL = dead
		t.Cleanup(func() { pingURL = orig })
		c := New(settings, "1.0.0")
		c.Ping(context.Background()) // must neither panic nor block
		if c.LatestVersion() != "" {
			t.Errorf("LatestVersion = %q after a failed ping", c.LatestVersion())
		}
	})

	t.Run("invalid URL", func(t *testing.T) {
		clearDeployEnv(t)
		orig := pingURL
		pingURL = "://not a url"
		t.Cleanup(func() { pingURL = orig })
		c := New(settings, "1.0.0")
		c.Ping(context.Background())
		if c.LatestVersion() != "" {
			t.Errorf("LatestVersion = %q", c.LatestVersion())
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		clearDeployEnv(t)
		rec := newPingRecorder(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		New(settings, "1.0.0").Ping(ctx)
		if n := len(rec.payloads(t)); n != 0 {
			t.Errorf("cancelled ping reached the server %d times", n)
		}
	})
}

// TestBrokenStore covers every path that reads the DB when the DB is gone:
// each must swallow or return the error rather than panic or send a ping.
func TestBrokenStore(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	settings := db.NewSettingsRepo(database)
	logs := db.NewLogRepo(database)
	_ = database.Close()
	ctx := context.Background()

	t.Run("installID errors", func(t *testing.T) {
		if _, err := (&Client{settings: settings}).installID(ctx); err == nil {
			t.Fatal("installID on a closed DB must return an error")
		}
	})
	t.Run("ping skipped", func(t *testing.T) {
		clearDeployEnv(t)
		rec := newPingRecorder(t)
		New(settings, "1.0.0").Ping(ctx)
		if n := len(rec.payloads(t)); n != 0 {
			t.Errorf("ping sent %d times with an unreadable settings store", n)
		}
	})
	t.Run("errors gatherer returns nil", func(t *testing.T) {
		if e := NewLogErrorsGatherer(logs)(ctx); e != nil {
			t.Errorf("gatherer on a broken store = %+v, want nil so the ping omits the section", e)
		}
	})
	t.Run("MarkFirst swallows", func(t *testing.T) {
		MarkFirst(ctx, nil, SettingFirstGrabAt)      // nil repo: no-op
		MarkFirst(ctx, settings, SettingFirstGrabAt) // closed DB: logged, not returned
	})
}

func TestDetectDeploy(t *testing.T) {
	clearDeployEnv(t)
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	if got := detectDeploy(); got != "kubernetes" {
		t.Errorf("in a pod: got %q, want kubernetes", got)
	}
	t.Setenv("BINDERY_DEPLOY_METHOD", "custom")
	if got := detectDeploy(); got != "custom" {
		t.Errorf("override: got %q, want custom", got)
	}
	t.Setenv("BINDERY_DEPLOY_METHOD", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	want := "binary"
	if _, err := os.Stat("/.dockerenv"); err == nil {
		want = "docker"
	}
	if got := detectDeploy(); got != want {
		t.Errorf("no env: got %q, want %q", got, want)
	}
}
