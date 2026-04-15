// Package smoke boots the real bindery binary and exercises the critical
// golden paths via HTTP. It catches wiring regressions — bad route
// registration, missing migration, broken frontend embed — that per-package
// unit tests can't see.
//
// Requires a pre-built binary. Set BINDERY_BINARY to override the default
// location (repo root `./bindery`). Typically invoked via `make smoke`,
// which builds the binary (including frontend assets) first.
//
// The whole suite ships as a single top-level test so binary boot/teardown
// happens once. Subtests are cheap HTTP calls against the same process.
package smoke_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	apiKey      = "smoke-test-key-0000000000000000"
	bootTimeout = 20 * time.Second
	httpTimeout = 5 * time.Second
)

func TestSmoke(t *testing.T) {
	bin := resolveBinary(t)
	tmp := t.TempDir()

	port, err := freePort()
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	cmd := exec.Command(bin)
	// Clean env: drop BINDERY_* from the outer shell so a developer's
	// personal config (e.g. BINDERY_PUID set for their homelab) can't make
	// the smoke run diverge from CI. We explicitly set every variable the
	// binary needs.
	cmd.Env = sanitizedEnv(
		"BINDERY_PORT="+fmt.Sprint(port),
		"BINDERY_DB_PATH="+filepath.Join(tmp, "bindery.db"),
		"BINDERY_DATA_DIR="+tmp,
		"BINDERY_API_KEY="+apiKey,
		"BINDERY_LIBRARY_DIR="+filepath.Join(tmp, "library"),
		"BINDERY_DOWNLOAD_DIR="+filepath.Join(tmp, "downloads"),
		"BINDERY_LOG_LEVEL=error",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		t.Fatalf("start bindery: %v", err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			t.Logf("bindery stdout/stderr:\n%s", out.String())
		}
	})

	if err := waitForHealth(base, bootTimeout); err != nil {
		t.Fatalf("health never green: %v\nbindery output:\n%s", err, out.String())
	}

	t.Run("authors list empty on fresh install", func(t *testing.T) {
		body := getJSON(t, base+"/api/v1/author")
		var arr []map[string]any
		if err := json.Unmarshal(body, &arr); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, body)
		}
		if len(arr) != 0 {
			t.Errorf("expected empty authors, got %d", len(arr))
		}
	})

	t.Run("books list returns an array", func(t *testing.T) {
		body := getJSON(t, base+"/api/v1/book")
		var arr []map[string]any
		if err := json.Unmarshal(body, &arr); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, body)
		}
	})

	t.Run("settings list returns an array", func(t *testing.T) {
		body := getJSON(t, base+"/api/v1/setting")
		var arr []map[string]any
		if err := json.Unmarshal(body, &arr); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, body)
		}
	})

	t.Run("calibre import 400s when library unconfigured", func(t *testing.T) {
		req := mustReq(t, http.MethodPost, base+"/api/v1/calibre/import", nil)
		resp := do(t, req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			b, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 400, got %d (body=%s)", resp.StatusCode, b)
		}
	})

	t.Run("api auth rejects missing key", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/author")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("embedded frontend serves index.html", func(t *testing.T) {
		resp, err := http.Get(base + "/")
		if err != nil {
			t.Fatalf("get /: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(strings.ToLower(string(body)), "<!doctype html") {
			t.Errorf("/ did not return an HTML document — was the binary built without web assets?\ngot: %s", truncate(body, 200))
		}
	})
}

// resolveBinary locates the bindery binary the test will drive. Search order:
//  1. $BINDERY_BINARY if set
//  2. repo-root ./bindery (two levels up from tests/smoke)
//  3. ./bindery relative to cwd
//
// When none of those exist the test is skipped with an actionable hint.
// `make smoke` is responsible for building it first.
func resolveBinary(t *testing.T) string {
	t.Helper()
	candidates := []string{}
	if p := os.Getenv("BINDERY_BINARY"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, "../../bindery", "./bindery")
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
			return abs
		}
	}
	t.Skip("bindery binary not found — run `make smoke` (builds first) or set BINDERY_BINARY to an existing binary path")
	return ""
}

// sanitizedEnv returns the current environment with all BINDERY_* variables
// removed, then appends the supplied overrides. This prevents a developer's
// personal env (BINDERY_PUID, BINDERY_DB_PATH pointing at their real install)
// from leaking into the smoke-test sandbox.
func sanitizedEnv(overrides ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "BINDERY_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, overrides...)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitForHealth(base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/api/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("no 200 from /api/v1/health within %s: %w", timeout, lastErr)
}

func getJSON(t *testing.T, url string) []byte {
	t.Helper()
	req := mustReq(t, http.MethodGet, url, nil)
	resp := do(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d (body=%s)", url, resp.StatusCode, body)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}

func mustReq(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Api-Key", apiKey)
	return req
}

func do(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(req.Context(), httpTimeout)
	t.Cleanup(cancel)
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	return resp
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
