package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestCalibreTestCoveragePluginRefusals covers plugin mode's failure answers
// on Test connection: no plugin URL is a 400 without any outbound call, a
// health failure is a 502, and a degraded bridge is a 502 naming the reason
// rather than the false green of "reachable".
func TestCalibreTestCoveragePluginRefusals(t *testing.T) {
	t.Run("no plugin url", func(t *testing.T) {
		h, repo, ctx := calibreFixture(t)
		if err := repo.Set(ctx, SettingCalibreMode, "plugin"); err != nil {
			t.Fatal(err)
		}
		code, body := testConnection(t, h)
		if code != http.StatusBadRequest || body["error"] != "plugin_url is not configured" {
			t.Fatalf("%d %v, want 400 plugin_url is not configured", code, body)
		}
	})

	t.Run("health fails", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		h, repo, ctx := calibreFixture(t)
		for k, v := range map[string]string{SettingCalibreMode: "plugin", SettingCalibrePluginURL: srv.URL} {
			if err := repo.Set(ctx, k, v); err != nil {
				t.Fatal(err)
			}
		}
		code, body := testConnection(t, h)
		if code != http.StatusBadGateway || body["error"] == "" || body["ok"] != "" {
			t.Fatalf("%d %v, want 502 with an error", code, body)
		}
		if hits.Load() == 0 {
			t.Fatal("health was never requested")
		}
	})

	t.Run("degraded bridge", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/health" {
				t.Errorf("unexpected call to %s on a degraded bridge", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"plugin_version":"0.6.2","calibre_version":"9.8","status":"degraded","error":"api_key missing"}`))
		}))
		t.Cleanup(srv.Close)
		h, repo, ctx := calibreFixture(t)
		for k, v := range map[string]string{SettingCalibreMode: "plugin", SettingCalibrePluginURL: srv.URL} {
			if err := repo.Set(ctx, k, v); err != nil {
				t.Fatal(err)
			}
		}
		code, body := testConnection(t, h)
		if code != http.StatusBadGateway || !strings.Contains(body["error"], "not serving the API: api_key missing") {
			t.Fatalf("%d %v, want 502 naming the degraded reason", code, body)
		}
	})
}
