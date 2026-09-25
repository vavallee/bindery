package calibre

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The contract test. It drives the real *PluginClient against the real
// calibre-bridge request handler, running as a Python subprocess with Calibre
// and Qt stubbed out (testdata/bridge_harness.py, which uses the same
// mechanism as the plugin's own conftest.py).
//
// Why it exists: every divergence in the review's protocol table, the
// User-Agent, the 400 semantics, the 503 backoff, the silently ignored
// coverPath, was found by reading two codebases side by side. Nothing in
// either repository's CI ran one against the other, so nothing caught them.
// Both sides have unit tests against their own fakes, and a fake is exactly
// what drifts.
//
// Why it lives in internal/calibre rather than a separate contract package:
// the subject is *PluginClient, and a useful contract test has to reach the
// backoff schedule and the capability TTL to keep the 503 row from taking
// thirty seconds. An out of package test would need those exported purely to
// be tested, which is a worse trade than one file with a build-time skip.
//
// Skips cleanly when python3 or the plugin checkout is absent, so the normal
// `go test ./...` on a machine with neither is unaffected. Point it at a
// checkout with BINDERY_PLUGIN_SRC=/path/to/bindery-plugins.
//
// Rows that need a capability the plugin under test does not advertise are
// skipped individually, and the test asserts the degradation instead. That is
// the compatibility rule stated as an executable check: run against
// calibre-bridge 0.5.0 it proves Bindery degrades, run against 0.6.0 it
// proves the new endpoints agree.

const contractPluginSrcEnv = "BINDERY_PLUGIN_SRC"

// bridge is one running harness process.
type bridge struct {
	url    string
	cancel func()
}

func pluginSourceRoot(t *testing.T) string {
	t.Helper()
	root := strings.TrimSpace(os.Getenv(contractPluginSrcEnv))
	if root == "" {
		// A sibling checkout is the layout the two repositories are normally
		// cloned in, so try it before giving up.
		wd, err := os.Getwd()
		if err != nil {
			t.Skipf("cannot resolve working directory: %v", err)
		}
		root = filepath.Join(wd, "..", "..", "..", "bindery-plugins")
	}
	handlers := filepath.Join(root, "plugins", "calibre-bridge", "plugin", "handlers.py")
	if _, err := os.Stat(handlers); err != nil {
		t.Skipf("plugin source not found at %s; set %s to a bindery-plugins checkout", handlers, contractPluginSrcEnv)
	}
	return filepath.Join(root, "plugins", "calibre-bridge")
}

func startBridge(t *testing.T, args ...string) *bridge {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 not available: %v", err)
	}
	root := pluginSourceRoot(t)
	harness, err := filepath.Abs(filepath.Join("testdata", "bridge_harness.py"))
	if err != nil {
		t.Fatalf("resolve harness: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, python, append([]string{harness, root}, args...)...)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Skipf("cannot start the harness: %v", err)
	}
	// Registered before the first failure path below so the child is always
	// reaped, including on the too-old-checkout skip.
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	lineCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "PORT ") || strings.HasPrefix(line, "SKIP ") {
				lineCh <- line
				return
			}
		}
		lineCh <- ""
	}()

	var line string
	select {
	case line = <-lineCh:
	case <-time.After(20 * time.Second):
		t.Fatal("harness did not report a port within 20s")
	}
	// A checkout whose make_handler predates the arguments the harness passes
	// gets a skip that names them, not a traceback. Reported by magrhino in
	// #2778 after a stale sibling checkout turned into "harness exited without
	// reporting a port" with the real cause buried in stderr.
	if reason, ok := strings.CutPrefix(line, "SKIP "); ok {
		t.Skip(strings.TrimSpace(reason))
	}
	port := strings.TrimSpace(strings.TrimPrefix(line, "PORT "))
	if port == "" {
		t.Fatal("harness exited without reporting a port")
	}
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatalf("harness reported a bad port %q", port)
	}

	return &bridge{url: "http://127.0.0.1:" + port, cancel: cancel}
}

// tempBook writes a file with an extension the plugin will accept as a format.
func tempBook(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("not really an epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPluginContract(t *testing.T) {
	shrinkBackoff(t)
	const key = "contract-key"
	srv := startBridge(t, "--api-key", key, "--max-body", "4096", "--library", "/calibre-library")
	ctx := context.Background()
	c := NewPluginClient(srv.url, key)

	var caps map[string]bool
	t.Run("health advertises a capability list", func(t *testing.T) {
		h, err := c.fetchHealth(ctx)
		if err != nil {
			t.Fatalf("health: %v", err)
		}
		if h.PluginVersion == "" || h.CalibreVersion == "" {
			t.Errorf("health = %+v, want both versions populated", h)
		}
		if h.Library != "/calibre-library" {
			t.Errorf("library = %q, want the active library path", h.Library)
		}
		caps = map[string]bool{}
		for _, name := range h.Capabilities {
			caps[name] = true
		}
		if !caps[pluginCapabilityBookMetadata] {
			t.Errorf("capabilities = %v, want at least %q", h.Capabilities, pluginCapabilityBookMetadata)
		}
		t.Logf("plugin %s advertises %v", h.PluginVersion, h.Capabilities)
	})

	t.Run("201 on a fresh book", func(t *testing.T) {
		id, err := c.Add(ctx, tempBook(t, "fresh.epub"), Metadata{
			Title:       "Dune",
			Authors:     []string{"Frank Herbert"},
			Identifiers: map[string]string{"bindery": "1001"},
		})
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if id <= 0 {
			t.Errorf("id = %d, want a positive Calibre id", id)
		}
	})

	t.Run("409 on a book the library already holds", func(t *testing.T) {
		meta := Metadata{Title: "Dune", Identifiers: map[string]string{"bindery": "2002"}}
		first, err := c.Add(ctx, tempBook(t, "dup.epub"), meta)
		if err != nil {
			t.Fatalf("first Add: %v", err)
		}
		second, err := c.Add(ctx, tempBook(t, "dup2.epub"), meta)
		if !errors.Is(err, ErrAlreadyInCalibre) {
			t.Fatalf("second Add error = %v, want ErrAlreadyInCalibre", err)
		}
		if second != first {
			t.Errorf("409 returned id %d, want the existing id %d", second, first)
		}
	})

	t.Run("401 on a bad token", func(t *testing.T) {
		bad := NewPluginClient(srv.url, "wrong")
		_, err := bad.Add(ctx, tempBook(t, "x.epub"), Metadata{Title: "Dune"})
		if err == nil || !strings.Contains(err.Error(), "authentication failed") {
			t.Fatalf("error = %v, want the authentication message", err)
		}
	})

	t.Run("400 without a code does not look like a metadata rejection", func(t *testing.T) {
		// A path the Calibre side cannot open is the #1346 mount mismatch.
		// Whether the plugin names it with a code or only in prose, the client
		// must not read it as "your metadata is bad" and re-send.
		_, err := c.Add(ctx, filepath.Join(t.TempDir(), "absent.epub"), Metadata{Title: "Dune"})
		if err == nil {
			t.Fatal("expected an error for a file the plugin cannot open")
		}
		if strings.Contains(err.Error(), "400") && looksLikeFileError(err.Error()) {
			return
		}
		t.Errorf("error = %v, want a 400 whose text reads as a file problem", err)
	})

	t.Run("400 on a path with no usable extension", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "no-extension")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := c.Add(ctx, path, Metadata{Title: "Dune"})
		if err == nil {
			t.Fatal("expected an error for an extensionless path")
		}
		if !looksLikeFileError(err.Error()) {
			t.Errorf("error = %v, want it classified as a file problem rather than a metadata one", err)
		}
	})

	t.Run("413 on an oversized body", func(t *testing.T) {
		huge := strings.Repeat("x", 8192)
		_, err := c.Add(ctx, tempBook(t, "big.epub"), Metadata{Title: "Dune", Description: huge})
		if err == nil {
			t.Fatal("expected an error for a body over the plugin's limit")
		}
		if !strings.Contains(err.Error(), "413") {
			t.Errorf("error = %v, want a 413", err)
		}
	})

	t.Run("cover capability", func(t *testing.T) {
		if !caps[pluginCapabilityCover] {
			// The degradation half of the contract: no capability, no field.
			if c.SupportsCover(ctx) {
				t.Error("SupportsCover = true against a plugin that does not advertise it")
			}
			t.Skipf("plugin does not advertise %q", pluginCapabilityCover)
		}
		cover := tempBook(t, "cover.jpg")
		if _, err := c.Add(ctx, tempBook(t, "withcover.epub"), Metadata{
			Title:       "Dune",
			CoverPath:   cover,
			Identifiers: map[string]string{"bindery": "3003"},
		}); err != nil {
			t.Fatalf("Add with a cover: %v", err)
		}
	})

	t.Run("path probe", func(t *testing.T) {
		if !caps[pluginCapabilityPathProbe] {
			if c.SupportsPathProbe(ctx) {
				t.Error("SupportsPathProbe = true against a plugin that does not advertise it")
			}
			t.Skipf("plugin does not advertise %q", pluginCapabilityPathProbe)
		}
		dir := t.TempDir()
		probe, err := c.ProbePath(ctx, dir)
		if err != nil {
			t.Fatalf("ProbePath: %v", err)
		}
		if !probe.Exists || !probe.IsDir || !probe.Readable {
			t.Errorf("probe of %s = %+v, want all true", dir, probe)
		}
		missing, err := c.ProbePath(ctx, filepath.Join(dir, "nope"))
		if err != nil {
			t.Fatalf("ProbePath on a missing path: %v", err)
		}
		if missing.Exists {
			t.Error("a missing path reported exists=true")
		}
	})

	t.Run("metadata update", func(t *testing.T) {
		if !caps[pluginCapabilityMetadataUpdate] {
			if c.SupportsMetadataUpdate(ctx) {
				t.Error("SupportsMetadataUpdate = true against a plugin that does not advertise it")
			}
			t.Skipf("plugin does not advertise %q", pluginCapabilityMetadataUpdate)
		}
		id, err := c.Add(ctx, tempBook(t, "patchable.epub"), Metadata{
			Title:       "Dune",
			Identifiers: map[string]string{"bindery": "4004"},
		})
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		fields, err := c.UpdateMetadata(ctx, id, Metadata{Title: "Dune", Series: "Dune Chronicles", SeriesIndex: "1"})
		if err != nil {
			t.Fatalf("UpdateMetadata: %v", err)
		}
		// An empty list here would mean the plugin accepted the request and
		// wrote nothing, which is what a body in the wrong shape looks like
		// from the outside. This assertion is the one that catches it.
		if len(fields) == 0 {
			t.Errorf("UpdateMetadata applied nothing; the request body shape does not match what the plugin reads")
		}
		if _, err := c.UpdateMetadata(ctx, 999999, Metadata{Title: "Dune"}); !errors.Is(err, ErrCalibreBookMissing) {
			t.Errorf("UpdateMetadata on a missing id = %v, want ErrCalibreBookMissing", err)
		}
	})

	t.Run("error codes", func(t *testing.T) {
		if !caps[pluginCapabilityErrorCodes] {
			if c.SupportsErrorCodes(ctx) {
				t.Error("SupportsErrorCodes = true against a plugin that does not advertise it")
			}
			t.Skipf("plugin does not advertise %q; the degradation is asserted in TestPluginContract_DegradesWithoutCapabilities", pluginCapabilityErrorCodes)
		}
		_, err := c.Add(ctx, filepath.Join(t.TempDir(), "absent.epub"), Metadata{Title: "Dune"})
		if err == nil || !strings.Contains(err.Error(), "path_not_found") {
			t.Errorf("error = %v, want it to carry the path_not_found code", err)
		}
	})
}

// TestPluginContract_503Backoff drives the real handler's library-not-ready
// branch. protocol.md tells clients to retry with exponential backoff to about
// thirty seconds; the client used to give up after one flat two second wait.
func TestPluginContract_503Backoff(t *testing.T) {
	shrinkBackoff(t)
	srv := startBridge(t, "--unavailable", "3")
	c := NewPluginClient(srv.url, "")

	id, err := c.Add(context.Background(), tempBook(t, "slow.epub"), Metadata{
		Title:       "Dune",
		Identifiers: map[string]string{"bindery": "5005"},
	})
	if err != nil {
		t.Fatalf("Add across three 503s: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d, want a positive id once the library came back", id)
	}
}

// TestPluginContract_UserAgent pins the one header the plugin logs for
// support. protocol.md asks for "bindery/<semver> plugin-api/v1".
func TestPluginContract_UserAgent(t *testing.T) {
	if got := pluginUserAgent(); !strings.HasPrefix(got, "bindery/") || !strings.HasSuffix(got, " plugin-api/v1") {
		t.Fatalf("User-Agent = %q, want the shape protocol.md specifies", got)
	}
	if strings.Contains(pluginUserAgent(), "  ") {
		t.Errorf("User-Agent = %q, want a single space between the parts", pluginUserAgent())
	}

}

// wireRecord is one exchange as it crossed the wire between the client and the
// Python handler.
type wireRecord struct {
	Method string
	Path   string
	Body   []byte
	Status int
	Reply  []byte
}

// recordingProxy sits between the client and the harness, forwarding every
// request to the real handler untouched and keeping a copy of both halves.
//
// The degradation half of the contract is mostly a claim about what Bindery
// does NOT put on the wire: no coverPath field, no PATCH, no path probe. A
// return value cannot show the absence of a field, and the fake based tests
// that can show it are exactly the fakes this file exists to distrust.
type recordingProxy struct {
	url string
	mu  sync.Mutex
	// seen is append only for the life of one test, so reset is how a row
	// scopes its assertions to its own traffic.
	seen []wireRecord
}

func newRecordingProxy(t *testing.T, target string) *recordingProxy {
	t.Helper()
	p := &recordingProxy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out, err := http.NewRequestWithContext(r.Context(), r.Method, target+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for name, values := range r.Header {
			for _, value := range values {
				out.Header.Add(name, value)
			}
		}
		resp, err := http.DefaultClient.Do(out)
		if err != nil {
			p.record(wireRecord{Method: r.Method, Path: r.URL.RequestURI(), Body: body, Status: http.StatusBadGateway})
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		reply, _ := io.ReadAll(resp.Body)
		p.record(wireRecord{
			Method: r.Method,
			Path:   r.URL.RequestURI(),
			Body:   body,
			Status: resp.StatusCode,
			Reply:  reply,
		})
		for name, values := range resp.Header {
			// Length and framing belong to this hop, not the forwarded one.
			if name == "Content-Length" || name == "Transfer-Encoding" {
				continue
			}
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(reply)
	}))
	t.Cleanup(srv.Close)
	p.url = srv.URL
	return p
}

func (p *recordingProxy) record(r wireRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, r)
}

func (p *recordingProxy) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = nil
}

// matching returns the recorded exchanges for one method and path prefix.
func (p *recordingProxy) matching(method, prefix string) []wireRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []wireRecord
	for _, r := range p.seen {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// describe renders the recorded traffic for a failure message, because "want 1
// POST, got 2" is only actionable next to the two bodies.
func (p *recordingProxy) describe() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var b strings.Builder
	for _, r := range p.seen {
		b.WriteString("\n  ")
		b.WriteString(r.Method)
		b.WriteString(" ")
		b.WriteString(r.Path)
		b.WriteString(" -> ")
		b.WriteString(strconv.Itoa(r.Status))
		if len(r.Body) > 0 {
			b.WriteString(" sent ")
			b.Write(r.Body)
		}
		if len(r.Reply) > 0 {
			b.WriteString(" got ")
			b.Write(r.Reply)
		}
	}
	if b.Len() == 0 {
		return "\n  (no requests)"
	}
	return b.String()
}

// TestPluginContract_DegradesWithoutCapabilities is the other half of the
// compatibility rule, and the half that was only ever asserted against fakes.
//
// TestPluginContract proves the 0.6.0 endpoints agree. This proves that
// against a bridge that advertises none of them, Bindery sends nothing the
// bridge cannot answer. Each row asserts what does not reach the wire, then
// shows the bridge really would refuse it, so the capability gate is not
// decoration. Rows whose capability the plugin under test does advertise skip:
// degradation is only observable against a plugin that lacks the capability,
// which is what running this against calibre-bridge 0.5.0 is for.
func TestPluginContract_DegradesWithoutCapabilities(t *testing.T) {
	shrinkBackoff(t)
	const key = "contract-key"
	srv := startBridge(t, "--api-key", key, "--library", "/calibre-library")
	wire := newRecordingProxy(t, srv.url)
	ctx := context.Background()
	c := NewPluginClient(wire.url, key)

	health, err := c.fetchHealth(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	caps := map[string]bool{}
	for _, name := range health.Capabilities {
		caps[name] = true
	}
	t.Logf("plugin %s advertises %v", health.PluginVersion, health.Capabilities)

	t.Run("no cover capability means no coverPath on the wire", func(t *testing.T) {
		if caps[pluginCapabilityCover] {
			t.Skipf("plugin advertises %q; the positive case is TestPluginContract's cover row", pluginCapabilityCover)
		}
		if c.SupportsCover(ctx) {
			t.Error("SupportsCover = true against a plugin that does not advertise it")
		}
		wire.reset()
		id, err := c.Add(ctx, tempBook(t, "degraded.epub"), Metadata{
			Title:       "Dune",
			Authors:     []string{"Frank Herbert"},
			CoverPath:   tempBook(t, "cover.jpg"),
			Identifiers: map[string]string{"bindery": "6006"},
		})
		if err != nil {
			t.Fatalf("Add with a cover the plugin cannot take: %v", err)
		}
		if id <= 0 {
			t.Errorf("id = %d, want the add to have succeeded without the cover", id)
		}
		posts := wire.matching(http.MethodPost, "/v1/books")
		if len(posts) != 1 {
			t.Fatalf("POST count = %d, want exactly one: %s", len(posts), wire.describe())
		}
		var sent struct {
			Metadata map[string]json.RawMessage `json:"metadata"`
		}
		if err := json.Unmarshal(posts[0].Body, &sent); err != nil {
			t.Fatalf("decode the body the client sent: %v (%s)", err, posts[0].Body)
		}
		if _, ok := sent.Metadata["coverPath"]; ok {
			t.Errorf("body = %s, want no coverPath key at all", posts[0].Body)
		}
		// Without this the row would also pass on a client that dropped the
		// whole metadata object, which is a different bug wearing the same
		// green tick.
		if _, ok := sent.Metadata["title"]; !ok {
			t.Errorf("body = %s, want the metadata object minus the cover, not minus everything", posts[0].Body)
		}
	})

	t.Run("no metadata_update capability means no PATCH on the wire", func(t *testing.T) {
		if caps[pluginCapabilityMetadataUpdate] {
			t.Skipf("plugin advertises %q; the positive case is TestPluginContract's metadata update row", pluginCapabilityMetadataUpdate)
		}
		if c.SupportsMetadataUpdate(ctx) {
			t.Error("SupportsMetadataUpdate = true against a plugin that does not advertise it")
		}
		// Nothing this test has driven so far may have issued a PATCH. The
		// production gate lives in the importer (scanner_handoff.go, which
		// this package cannot import without a cycle); what is pinned here is
		// the answer that gate reads plus the traffic it governs.
		if patches := wire.matching(http.MethodPatch, "/v1/books"); len(patches) != 0 {
			t.Errorf("saw %d PATCH requests against a bridge without %q: %s",
				len(patches), pluginCapabilityMetadataUpdate, wire.describe())
		}
		// And the gate is load bearing: this bridge has no PATCH route, so a
		// client that ignored the capability would not silently succeed.
		req, err := http.NewRequestWithContext(ctx, http.MethodPatch, wire.url+"/v1/books/1",
			strings.NewReader(`{"title":"Dune"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("raw PATCH: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("raw PATCH status = %d, want a refusal from a bridge with no such route", resp.StatusCode)
		}
		t.Logf("a PATCH this bridge never advertised answers %d", resp.StatusCode)
	})

	t.Run("no path_probe capability means Test connection reports only reachability", func(t *testing.T) {
		if caps[pluginCapabilityPathProbe] {
			t.Skipf("plugin advertises %q; the positive case is TestPluginContract's path probe row", pluginCapabilityPathProbe)
		}
		if c.SupportsPathProbe(ctx) {
			t.Error("SupportsPathProbe = true against a plugin that does not advertise it")
		}
		if probes := wire.matching(http.MethodGet, "/v1/paths"); len(probes) != 0 {
			t.Errorf("saw %d path probes against a bridge without %q: %s",
				len(probes), pluginCapabilityPathProbe, wire.describe())
		}
		// What Test connection can still report is reachability and a version,
		// and nothing about a path. The sentence itself is assembled in
		// internal/api (probeLibraryRoot), which asks SupportsPathProbe first;
		// this row pins the two answers that decide which sentence it picks.
		state, err := c.HealthDetail(ctx)
		if err != nil {
			t.Fatalf("HealthDetail: %v", err)
		}
		if state.Degraded {
			t.Errorf("HealthDetail reports degraded = true (%q), want a plainly reachable bridge", state.Reason)
		}
		if !strings.Contains(state.Version, health.PluginVersion) {
			t.Errorf("version = %q, want it to name the plugin version %q", state.Version, health.PluginVersion)
		}
		// The gate is load bearing here too: asking anyway gets a 404, which
		// is what would turn a reachable bridge into a spurious failure.
		if _, err := c.ProbePath(ctx, t.TempDir()); err == nil {
			t.Error("ProbePath succeeded against a bridge that does not serve /v1/paths")
		} else {
			t.Logf("probing anyway fails with %v", err)
		}
	})

	t.Run("the legacy payload retry keys on a 400 with no code", func(t *testing.T) {
		meta := Metadata{Title: "Dune", Identifiers: map[string]string{"bindery": "7007"}}

		// A 400 about the file must not be re-sent. This is #1346, the mount
		// mismatch that doubled every request and buried the real cause.
		wire.reset()
		if _, err := c.Add(ctx, filepath.Join(t.TempDir(), "absent.epub"), meta); err == nil {
			t.Fatal("expected an error for a file the plugin cannot open")
		}
		if posts := wire.matching(http.MethodPost, "/v1/books"); len(posts) != 1 {
			t.Errorf("a file error 400 produced %d POSTs, want one and no retry: %s", len(posts), wire.describe())
		}

		// The other direction: a 400 that is not about the file. The only one
		// the 0.5.0 handler can be driven into is an empty path, which it
		// answers "path required" with no code at all, so this is the code
		// free 400 the heuristic exists for.
		wire.reset()
		if _, err := c.Add(ctx, "", meta); err == nil {
			t.Fatal("expected an error for an empty path")
		}
		posts := wire.matching(http.MethodPost, "/v1/books")
		if len(posts) != 2 {
			t.Fatalf("POST count = %d, want the first with metadata and one legacy retry: %s", len(posts), wire.describe())
		}
		if posts[0].Status != http.StatusBadRequest {
			t.Errorf("first status = %d, want 400: %s", posts[0].Status, wire.describe())
		}
		if !bytes.Contains(posts[0].Body, []byte(`"metadata"`)) {
			t.Errorf("first body = %s, want it to carry the metadata object", posts[0].Body)
		}
		if bytes.Contains(posts[1].Body, []byte(`"metadata"`)) {
			t.Errorf("retry body = %s, want the legacy shape with no metadata object", posts[1].Body)
		}
		var reply struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(posts[0].Reply, &reply); err != nil {
			t.Fatalf("decode the rejection: %v (%s)", err, posts[0].Reply)
		}
		if caps[pluginCapabilityErrorCodes] {
			if reply.Code != pluginCodeInvalidMetadata {
				t.Errorf("code = %q, want %q to be what drove the retry", reply.Code, pluginCodeInvalidMetadata)
			}
			return
		}
		if reply.Code != "" {
			t.Errorf("code = %q, want none: this row exists for the code free 400 an older bridge sends", reply.Code)
		}
	})
}
