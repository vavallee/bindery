// Package predeploy runs HTTP-level smoke checks against an already-running
// bindery instance (typically the cluster deployment just promoted by
// ArgoCD). It is pointed at a live URL via BINDERY_URL and authenticates
// with BINDERY_API_KEY — both required. When either is missing the whole
// suite is skipped so local `go test ./...` runs stay green.
//
// Unlike tests/smoke, this package boots no binary and touches no disk: it
// is purely a post-rollout sanity check for wiring that unit tests can't
// see (routes, auth, frontend embed, migrations applied).
package predeploy_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const httpTimeout = 10 * time.Second

func TestPreDeploy(t *testing.T) {
	base := strings.TrimRight(os.Getenv("BINDERY_URL"), "/")
	apiKey := os.Getenv("BINDERY_API_KEY")
	if base == "" || apiKey == "" {
		t.Skip("BINDERY_URL and BINDERY_API_KEY must be set for pre-deploy smoke")
	}

	t.Run("health endpoint returns ok", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/health")
		if err != nil {
			t.Fatalf("get health: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("auth status reports not setup-required", func(t *testing.T) {
		body := getJSON(t, base+"/api/v1/auth/status", apiKey)
		var status struct {
			SetupRequired bool `json:"setupRequired"`
		}
		if err := json.Unmarshal(body, &status); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, body)
		}
		if status.SetupRequired {
			t.Errorf("live instance reports setupRequired=true — deployment is uninitialised")
		}
	})

	t.Run("unauthenticated request returns 401", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/author")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("author list returns array", func(t *testing.T) {
		body := getJSON(t, base+"/api/v1/author", apiKey)
		var arr []map[string]any
		if err := json.Unmarshal(body, &arr); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, body)
		}
	})

	t.Run("book list returns array", func(t *testing.T) {
		body := getJSON(t, base+"/api/v1/book", apiKey)
		var arr []map[string]any
		if err := json.Unmarshal(body, &arr); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, body)
		}
	})

	t.Run("settings list returns array", func(t *testing.T) {
		body := getJSON(t, base+"/api/v1/setting", apiKey)
		var arr []map[string]any
		if err := json.Unmarshal(body, &arr); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, body)
		}
	})

	t.Run("search author endpoint is reachable", func(t *testing.T) {
		req := mustReq(t, http.MethodGet, base+"/api/v1/search/author?term=test", apiKey)
		resp := do(t, req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 200, got %d (body=%s)", resp.StatusCode, b)
		}
	})

	t.Run("queue endpoint returns array", func(t *testing.T) {
		body := getJSON(t, base+"/api/v1/queue", apiKey)
		var arr []map[string]any
		if err := json.Unmarshal(body, &arr); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, body)
		}
	})

	t.Run("frontend serves HTML", func(t *testing.T) {
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
			t.Errorf("/ did not return an HTML document\ngot: %s", truncate(body, 200))
		}
	})
}

func getJSON(t *testing.T, url, apiKey string) []byte {
	t.Helper()
	req := mustReq(t, http.MethodGet, url, apiKey)
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

func mustReq(t *testing.T, method, url, apiKey string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
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
