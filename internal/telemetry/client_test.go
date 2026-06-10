package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/vavallee/bindery/internal/db"
)

func TestIsReleaseVersion(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		// Release tags — both with and without the leading "v" since the
		// docker image build keeps "v1.7.0" while goreleaser strips it.
		{"v1.7.0", true},
		{"1.7.0", true},
		{"v0.0.1", true},
		{"v10.20.30", true},

		// Non-release labels CI emits today.
		{"dev", false},               // go run / go build with no -ldflags
		{"dev-abc1234", false},       // development branch interim image
		{"sha-abc1234", false},       // non-release branch interim image
		{"v1.7.0-3-gabc1234", false}, // git describe form (commits past a tag)
		{"v1.7.0-rc.1", false},       // pre-release (not currently used; safe to revisit)
		{"", false},                  // misconfigured / empty
		{"latest", false},            // image-tag style, not a real version
		{"v1.7", false},              // truncated
		{"1.7.0.1", false},           // four-component, not semver
		{"v1.7.0 ", false},           // whitespace-padded
		{"main", false},              // branch name accidentally injected
	}
	for _, tc := range cases {
		if got := isReleaseVersion(tc.version); got != tc.want {
			t.Errorf("isReleaseVersion(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

// TestPingPayloadWireShape verifies the JSON shape of a ping payload both
// with and without a features section. Two cohorts care about the shape:
// older telemetry-server versions that don't know about features (must
// accept the message without the field), and the current server which
// expects the nested features object verbatim.
func TestPingPayloadWireShape(t *testing.T) {
	t.Run("no features field when gatherer is nil", func(t *testing.T) {
		body := pingPayload{
			InstallID: "11111111-1111-1111-1111-111111111111",
			Version:   "1.15.2",
			OS:        "linux",
			Arch:      "amd64",
			Deploy:    "docker",
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, present := decoded["features"]; present {
			t.Errorf("features field must be omitted when nil; got: %s", raw)
		}
	})

	t.Run("features field embeds when gatherer is set", func(t *testing.T) {
		f := Features{
			Indexers:        2,
			DownloadClients: 1,
			Notifications:   3,
			CalibreEnabled:  true,
			MultiUser:       true,
		}
		body := pingPayload{
			InstallID: "22222222-2222-2222-2222-222222222222",
			Version:   "1.15.3",
			OS:        "linux",
			Arch:      "amd64",
			Deploy:    "docker",
			Features:  &f,
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded struct {
			Features map[string]any `json:"features"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := decoded.Features["indexers"]; got != float64(2) {
			t.Errorf("features.indexers = %v, want 2", got)
		}
		if got := decoded.Features["calibre_enabled"]; got != true {
			t.Errorf("features.calibre_enabled = %v, want true", got)
		}
		// Zero-valued fields must be omitted, otherwise the payload bloats
		// for installs that have nothing configured.
		if _, present := decoded.Features["abs_enabled"]; present {
			t.Errorf("features.abs_enabled must be omitted when false; got: %v", decoded.Features)
		}
		if _, present := decoded.Features["users"]; present {
			t.Errorf("features.users must be omitted when zero; got: %v", decoded.Features)
		}
	})
}

// TestWithGathererIsOptional verifies that calling WithGatherer is optional
// and that omitting it leaves the gatherer nil so Ping falls back to the
// legacy payload shape (covered above by the no-features-field test).
func TestWithGathererIsOptional(t *testing.T) {
	c := &Client{}
	if c.gatherer != nil {
		t.Error("zero-value Client should have nil gatherer")
	}
	c2 := (&Client{}).WithGatherer(func(context.Context) Features {
		return Features{Indexers: 1}
	})
	if c2.gatherer == nil {
		t.Error("WithGatherer should set the gatherer")
	}
	got := c2.gatherer(context.Background())
	if got.Indexers != 1 {
		t.Errorf("gatherer returned %+v, want Indexers=1", got)
	}
}

// TestPingPayloadErrorsWireShape verifies the errors section's JSON shape:
// absent when no gatherer is wired (nil pointer), and explicit zero counts
// when present so the receiver can distinguish "no errors" from "old binary".
func TestPingPayloadErrorsWireShape(t *testing.T) {
	t.Run("no errors field when gatherer is nil", func(t *testing.T) {
		body := pingPayload{InstallID: "x", Version: "1.0.0", OS: "linux", Arch: "amd64", Deploy: "binary"}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, present := decoded["errors"]; present {
			t.Errorf("errors field must be omitted when nil; got: %s", raw)
		}
	})

	t.Run("errors field embeds counts and top list", func(t *testing.T) {
		body := pingPayload{
			InstallID: "x", Version: "1.0.0", OS: "linux", Arch: "amd64", Deploy: "binary",
			Errors: &Errors{
				ErrorCount: 7,
				WarnCount:  0,
				TopErrors:  []ErrorEntry{{Msg: "indexer search failed", Count: 5}},
			},
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded struct {
			Errors map[string]any `json:"errors"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := decoded.Errors["error_count"]; got != float64(7) {
			t.Errorf("errors.error_count = %v, want 7", got)
		}
		// Zero counts must be explicit, not omitted.
		if got, present := decoded.Errors["warn_count"]; !present || got != float64(0) {
			t.Errorf("errors.warn_count = %v (present=%v), want explicit 0", got, present)
		}
		top, ok := decoded.Errors["top_errors"].([]any)
		if !ok || len(top) != 1 {
			t.Fatalf("errors.top_errors = %v, want 1 entry", decoded.Errors["top_errors"])
		}
		entry := top[0].(map[string]any)
		if entry["msg"] != "indexer search failed" || entry["count"] != float64(5) {
			t.Errorf("top_errors[0] = %v", entry)
		}
	})

	t.Run("empty top_errors is omitted", func(t *testing.T) {
		raw, err := json.Marshal(Errors{ErrorCount: 0, WarnCount: 2})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, present := decoded["top_errors"]; present {
			t.Errorf("top_errors must be omitted when empty; got: %s", raw)
		}
	})
}

func TestSanitizeErrors(t *testing.T) {
	t.Run("nil in nil out", func(t *testing.T) {
		if sanitizeErrors(nil) != nil {
			t.Error("sanitizeErrors(nil) must be nil")
		}
	})

	t.Run("truncates long messages", func(t *testing.T) {
		long := strings.Repeat("a", 500)
		e := sanitizeErrors(&Errors{TopErrors: []ErrorEntry{{Msg: long, Count: 1}}})
		if got := len(e.TopErrors[0].Msg); got != maxErrorMsgLen {
			t.Errorf("truncated msg len = %d, want %d", got, maxErrorMsgLen)
		}
	})

	t.Run("truncates on rune boundary", func(t *testing.T) {
		// 119 ASCII bytes + a 3-byte rune straddling the 120-byte cut.
		msg := strings.Repeat("a", 119) + "€€"
		e := sanitizeErrors(&Errors{TopErrors: []ErrorEntry{{Msg: msg, Count: 1}}})
		got := e.TopErrors[0].Msg
		if !utf8.ValidString(got) {
			t.Errorf("truncated msg is invalid UTF-8: %q", got)
		}
		if len(got) > maxErrorMsgLen {
			t.Errorf("truncated msg len = %d, want <= %d", len(got), maxErrorMsgLen)
		}
	})

	t.Run("caps top list at five", func(t *testing.T) {
		var entries []ErrorEntry
		for i := 0; i < 9; i++ {
			entries = append(entries, ErrorEntry{Msg: "m", Count: 9 - i})
		}
		e := sanitizeErrors(&Errors{TopErrors: entries})
		if len(e.TopErrors) != maxTopErrors {
			t.Errorf("top list len = %d, want %d", len(e.TopErrors), maxTopErrors)
		}
	})
}

// TestNewLogErrorsGatherer exercises the SQLite-backed gatherer end to end:
// empty store, level counting, top-5 ordering, the 24h window cutoff, and
// that attrs never leak into the messages.
func TestNewLogErrorsGatherer(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()
	logs := db.NewLogRepo(database)
	gather := NewLogErrorsGatherer(logs)
	ctx := context.Background()

	t.Run("empty log store", func(t *testing.T) {
		e := gather(ctx)
		if e == nil {
			t.Fatal("gatherer returned nil for healthy empty store")
		}
		if e.ErrorCount != 0 || e.WarnCount != 0 || len(e.TopErrors) != 0 {
			t.Errorf("expected zero summary, got %+v", e)
		}
	})

	now := time.Now().UTC()
	insert := func(age time.Duration, level, msg string, fields map[string]string) {
		t.Helper()
		if err := logs.Insert(ctx, db.LogEntry{TS: now.Add(-age), Level: level, Message: msg, Fields: fields}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// The attrs carry PII-shaped values; they must never appear in TopErrors.
	for i := 0; i < 3; i++ {
		insert(time.Duration(i+1)*time.Minute, "ERROR", "download fetch rejected", map[string]string{"url": "http://127.0.0.1:9696/secret", "book": "A Private Title"})
	}
	insert(time.Hour, "ERROR", "import failed", map[string]string{"path": "/books/User Name/file.epub"})
	insert(2*time.Hour, "WARN", "retrying", nil)
	insert(25*time.Hour, "ERROR", "ancient failure", nil) // outside window

	t.Run("counts, ordering, window", func(t *testing.T) {
		e := gather(ctx)
		if e == nil {
			t.Fatal("gatherer returned nil")
		}
		if e.ErrorCount != 4 {
			t.Errorf("ErrorCount = %d, want 4", e.ErrorCount)
		}
		if e.WarnCount != 1 {
			t.Errorf("WarnCount = %d, want 1", e.WarnCount)
		}
		if len(e.TopErrors) != 2 {
			t.Fatalf("TopErrors = %+v, want 2 entries", e.TopErrors)
		}
		if e.TopErrors[0].Msg != "download fetch rejected" || e.TopErrors[0].Count != 3 {
			t.Errorf("TopErrors[0] = %+v", e.TopErrors[0])
		}
		if e.TopErrors[1].Msg != "import failed" || e.TopErrors[1].Count != 1 {
			t.Errorf("TopErrors[1] = %+v", e.TopErrors[1])
		}
		for _, te := range e.TopErrors {
			for _, leak := range []string{"127.0.0.1", "A Private Title", "/books/User Name"} {
				if strings.Contains(te.Msg, leak) {
					t.Errorf("attr value %q leaked into msg %q", leak, te.Msg)
				}
			}
		}
	})
}

// TestPingRespectsOptOut proves the errors payload rides inside the existing
// privacy gates: when telemetry is disabled (via the DB setting or the env
// var) Ping sends nothing at all, and when enabled the same code path emits
// the errors section.
func TestPingRespectsOptOut(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer database.Close()
	settings := db.NewSettingsRepo(database)
	ctx := context.Background()

	var requests atomic.Int64
	var lastBody []byte
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		lastBody = b
		mu.Unlock()
		fmt.Fprint(w, `{"latest_version":"9.9.9"}`)
	}))
	defer srv.Close()
	origURL := pingURL
	pingURL = srv.URL
	t.Cleanup(func() { pingURL = origURL })

	client := New(settings, "1.2.3").WithErrorsGatherer(func(context.Context) *Errors {
		return &Errors{ErrorCount: 3, WarnCount: 1, TopErrors: []ErrorEntry{{Msg: "import failed", Count: 3}}}
	})

	t.Run("setting disabled sends nothing", func(t *testing.T) {
		if err := settings.Set(ctx, settingEnabled, "false"); err != nil {
			t.Fatalf("set: %v", err)
		}
		client.Ping(ctx)
		if n := requests.Load(); n != 0 {
			t.Fatalf("expected 0 pings while disabled, server saw %d", n)
		}
	})

	t.Run("env opt-out sends nothing even when setting is enabled", func(t *testing.T) {
		if err := settings.Set(ctx, settingEnabled, "true"); err != nil {
			t.Fatalf("set: %v", err)
		}
		t.Setenv("BINDERY_TELEMETRY_DISABLED", "true")
		client.Ping(ctx)
		if n := requests.Load(); n != 0 {
			t.Fatalf("expected 0 pings with BINDERY_TELEMETRY_DISABLED, server saw %d", n)
		}
	})

	t.Run("enabled sends one ping carrying the errors section", func(t *testing.T) {
		if err := settings.Set(ctx, settingEnabled, "true"); err != nil {
			t.Fatalf("set: %v", err)
		}
		client.Ping(ctx)
		if n := requests.Load(); n != 1 {
			t.Fatalf("expected exactly 1 ping, server saw %d", n)
		}
		mu.Lock()
		body := lastBody
		mu.Unlock()
		var decoded struct {
			Errors *Errors `json:"errors"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("unmarshal ping body: %v", err)
		}
		if decoded.Errors == nil || decoded.Errors.ErrorCount != 3 || len(decoded.Errors.TopErrors) != 1 {
			t.Errorf("errors section = %+v, want ErrorCount=3 with 1 top entry", decoded.Errors)
		}
		if client.LatestVersion() != "9.9.9" {
			t.Errorf("LatestVersion = %q, want 9.9.9", client.LatestVersion())
		}
	})
}

// TestPingPayloadFitsReceiverBodyCap guards the telemetry-server's 4096-byte
// MaxBytesReader (cmd/telemetry-server handlePing): a worst-case payload —
// every feature populated plus five maximum-length top errors — must stay
// comfortably under the cap, otherwise the receiver responds 400 and the
// entire ping (not just the errors section) is silently lost.
func TestPingPayloadFitsReceiverBodyCap(t *testing.T) {
	var top []ErrorEntry
	for i := 0; i < maxTopErrors; i++ {
		top = append(top, ErrorEntry{Msg: strings.Repeat("m", maxErrorMsgLen), Count: 1 << 30})
	}
	body := pingPayload{
		InstallID: "12345678-1234-1234-1234-123456789012",
		Version:   "10.20.30",
		OS:        "linux",
		Arch:      "amd64",
		Deploy:    "kubernetes",
		Features: &Features{
			Indexers: 99, DownloadClients: 99, Notifications: 99, Users: 99,
			CalibreEnabled: true, ABSEnabled: true, GrimmoryEnabled: true,
			HardcoverToken: true, OIDCEnabled: true, MultiUser: true,
		},
		Errors: &Errors{ErrorCount: 1 << 30, WarnCount: 1 << 30, TopErrors: top},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const receiverBodyCap = 4096
	if len(raw) >= receiverBodyCap {
		t.Errorf("worst-case payload is %d bytes, must stay under the receiver's %d-byte body cap", len(raw), receiverBodyCap)
	}
}
