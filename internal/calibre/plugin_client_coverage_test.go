package calibre

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// covPluginServer answers /v1/health with the given capabilities and hands
// every other request to books.
func covPluginServer(t *testing.T, caps []string, books http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var probes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			probes.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"plugin_version": "0.7.0", "calibre_version": "8.0", "library": "/books", "capabilities": caps,
			})
			return
		}
		books(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &probes
}

func TestPluginClient_PushPathAppliesTheRemapOnlyWhenConfigured(t *testing.T) {
	plain := NewPluginClient("http://calibre:8099", "k")
	if got := plain.PushPath("/books/a.epub"); got != "/books/a.epub" {
		t.Errorf("no remap: PushPath = %q, want the path verbatim", got)
	}
	mapped := NewPluginClient("http://calibre:8099", "k").WithPushPathRemap("/books:/mnt/library")
	if got := mapped.PushPath("/books/a.epub"); got != "/mnt/library/a.epub" {
		t.Errorf("remap: PushPath = %q, want /mnt/library/a.epub", got)
	}
	if got := mapped.PushPath("  "); got != "  " {
		t.Errorf("blank path must pass through untouched, got %q", got)
	}
	// A spec that parses to nothing leaves the client in passthrough mode.
	if got := NewPluginClient("http://x", "k").WithPushPathRemap("garbage").PushPath("/books/a.epub"); got != "/books/a.epub" {
		t.Errorf("malformed remap: PushPath = %q, want passthrough", got)
	}
}

func TestPluginResult_Message(t *testing.T) {
	cases := []struct {
		in   pluginResult
		want string
	}{
		{pluginResult{Error: "bad", Code: "bad_format"}, "bad (bad_format)"},
		{pluginResult{Error: "bad"}, "bad"},
		{pluginResult{Code: "bad_format"}, "bad_format"},
		{pluginResult{}, "no error detail"},
	}
	for _, c := range cases {
		if got := c.in.message(); got != c.want {
			t.Errorf("%+v.message() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPluginHealth_Degraded(t *testing.T) {
	if d, reason := (pluginHealth{Status: "ok"}).Degraded(); d || reason != "" {
		t.Errorf("ok status: degraded=%v reason=%q, want false and empty", d, reason)
	}
	if d, reason := (pluginHealth{Status: " DEGRADED ", Error: " no api_key set "}).Degraded(); !d || reason != "no api_key set" {
		t.Errorf("degraded with error: degraded=%v reason=%q", d, reason)
	}
	d, reason := (pluginHealth{Status: "degraded"}).Degraded()
	if !d || !strings.Contains(reason, "refused to start") {
		t.Errorf("degraded without error: degraded=%v reason=%q, want the stand-in explanation", d, reason)
	}
}

func TestPluginClient_HealthDetailReportsDegradedBridge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"plugin_version":" 0.6.2 ","calibre_version":"8.0","library":" /books ","status":"degraded","error":"api_key missing","capabilities":["error_codes"]}`))
	}))
	defer srv.Close()
	c := NewPluginClient(srv.URL, "k")
	state, err := c.HealthDetail(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Degraded || state.Reason != "api_key missing" || state.PluginVersion != "0.6.2" || state.Library != "/books" {
		t.Fatalf("state = %+v", state)
	}
	if !c.SupportsErrorCodes(context.Background()) {
		t.Error("error_codes advertised, SupportsErrorCodes = false")
	}
	if c.SupportsAddFormat(context.Background()) {
		t.Error("add_format not advertised, SupportsAddFormat = true")
	}
}

func TestPluginClient_LibraryAndCapabilityCache(t *testing.T) {
	srv, probes := covPluginServer(t, []string{"cover", "path_probe"}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	c := NewPluginClient(srv.URL, "k")
	lib, err := c.Library(context.Background())
	if err != nil || lib != "/books" {
		t.Fatalf("Library = %q, %v; want /books", lib, err)
	}
	// Library cached the capabilities, so the checks below must not probe.
	before := probes.Load()
	if !c.SupportsCover(context.Background()) || !c.SupportsPathProbe(context.Background()) {
		t.Error("advertised capabilities reported missing")
	}
	if c.SupportsErrorCodes(context.Background()) || c.SupportsMetadataUpdate(context.Background()) {
		t.Error("unadvertised capabilities reported present")
	}
	if probes.Load() != before {
		t.Errorf("capability checks probed %d more times, want the cache", probes.Load()-before)
	}
}

func TestPluginClient_FetchHealthFailures(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"unauthorized": {http.StatusUnauthorized, `{}`, "authentication failed"},
		"server error": {http.StatusInternalServerError, `{}`, "server error 500"},
		"malformed":    {http.StatusOK, `{"plugin_version":`, "decode health"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := NewPluginClient(srv.URL, "k")
			if _, err := c.Library(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Library err = %v, want %q", err, tc.want)
			}
			if _, err := c.Health(context.Background()); err == nil {
				t.Error("Health: want an error")
			}
			if _, err := c.HealthDetail(context.Background()); err == nil {
				t.Error("HealthDetail: want an error")
			}
			if c.SupportsErrorCodes(context.Background()) {
				t.Error("a failed probe must not report a capability")
			}
		})
	}
}

func TestPluginClient_UnreachableBridgeInvalidatesTheCache(t *testing.T) {
	srv, _ := covPluginServer(t, []string{"cover", "metadata_update", "path_probe"}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	c := NewPluginClient(srv.URL, "k")
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !c.capabilitiesFresh() {
		t.Fatal("capabilities should be cached after a health probe")
	}
	srv.Close()

	if _, err := c.UpdateMetadata(context.Background(), 7, Metadata{Title: "x"}); err == nil || !strings.Contains(err.Error(), "update metadata") {
		t.Errorf("UpdateMetadata err = %v, want a transport error", err)
	}
	if c.capabilitiesFresh() {
		t.Error("a transport failure must drop the capability cache")
	}
	if _, err := c.ProbePath(context.Background(), "/books/a.epub"); err == nil || !strings.Contains(err.Error(), "path probe") {
		t.Errorf("ProbePath err = %v, want a transport error", err)
	}
	if _, err := c.Add(context.Background(), "/books/a.epub", Metadata{}); err == nil {
		t.Error("Add against a closed server: want an error")
	}
	if _, err := c.Library(context.Background()); err == nil || !strings.Contains(err.Error(), "health") {
		t.Errorf("Library err = %v, want a health transport error", err)
	}
}

func TestPluginClient_BadBaseURLFailsBeforeAnyRequest(t *testing.T) {
	c := NewPluginClient("http://bad host\x7f", "k")
	ctx := context.Background()
	if _, err := c.Library(ctx); err == nil {
		t.Error("Library: want a request build error")
	}
	if _, err := c.UpdateMetadata(ctx, 1, Metadata{}); err == nil {
		t.Error("UpdateMetadata: want a request build error")
	}
	if _, err := c.ProbePath(ctx, "/a"); err == nil {
		t.Error("ProbePath: want a request build error")
	}
	if _, err := c.Add(ctx, "/a", Metadata{}); err == nil {
		t.Error("Add: want a request build error")
	}
}

func TestPluginClient_UpdateMetadataResponses(t *testing.T) {
	ctx := context.Background()
	c := NewPluginClient("http://unused", "k")
	if _, err := c.UpdateMetadata(ctx, 0, Metadata{Title: "x"}); err == nil {
		t.Error("id 0: want an error before any request")
	}

	cases := map[string]struct {
		status  int
		body    string
		wantErr string
		isGone  bool
		fields  []string
	}{
		"written":      {http.StatusOK, `{"updated":true,"fields":["title","tags"]}`, "", false, []string{"title", "tags"}},
		"unauthorized": {http.StatusUnauthorized, `{}`, "authentication failed", false, nil},
		"malformed":    {http.StatusOK, `{"fields":`, "decode update response", false, nil},
		"gone":         {http.StatusNotFound, `{"error":"no book","code":"not_found"}`, "no book (not_found)", true, nil},
		"server error": {http.StatusInternalServerError, `not json`, "server error 500: no error detail", false, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var gotBody map[string]any
			var gotMethod, gotPath string
			srv, _ := covPluginServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			c := NewPluginClient(srv.URL, "k")
			fields, err := c.UpdateMetadata(ctx, 42, Metadata{Title: "Dune", CoverPath: "/covers/a.jpg"})
			if gotMethod != http.MethodPatch || gotPath != "/v1/books/42" {
				t.Errorf("request = %s %s, want PATCH /v1/books/42", gotMethod, gotPath)
			}
			// The plugin does not advertise cover, so the path is dropped.
			if _, ok := gotBody["coverPath"]; ok && gotBody["coverPath"] != "" {
				t.Errorf("coverPath sent to a plugin without the cover capability: %v", gotBody["coverPath"])
			}
			if tc.wantErr == "" {
				if err != nil || strings.Join(fields, ",") != strings.Join(tc.fields, ",") {
					t.Fatalf("fields = %v, err = %v; want %v", fields, err, tc.fields)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if got := errors.Is(err, ErrCalibreBookMissing); got != tc.isGone {
				t.Errorf("errors.Is(ErrCalibreBookMissing) = %v, want %v", got, tc.isGone)
			}
		})
	}
}

func TestPluginClient_ProbePathResponses(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		status  int
		body    string
		wantErr string
		want    PathProbe
	}{
		"echoes no path":  {http.StatusOK, `{"exists":true,"readable":true}`, "", PathProbe{Path: "/mnt/a.epub", Exists: true, Readable: true}},
		"names a path":    {http.StatusOK, `{"path":"/other","isDir":true}`, "", PathProbe{Path: "/other", IsDir: true}},
		"unauthorized":    {http.StatusUnauthorized, `{}`, "authentication failed", PathProbe{}},
		"malformed":       {http.StatusOK, `{`, "decode path probe", PathProbe{}},
		"forbidden by ui": {http.StatusForbidden, `{"error":"outside root","code":"path_forbidden"}`, "server error 403: outside root (path_forbidden)", PathProbe{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var gotQuery string
			srv, _ := covPluginServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.Query().Get("path")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			c := NewPluginClient(srv.URL, "k").WithPushPathRemap("/books:/mnt")
			got, err := c.ProbePath(ctx, "/books/a.epub")
			if gotQuery != "/mnt/a.epub" {
				t.Errorf("probe asked about %q, want the remapped /mnt/a.epub", gotQuery)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("probe = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestPluginClient_AddResponses(t *testing.T) {
	ctx := context.Background()
	t.Run("malformed success body", func(t *testing.T) {
		srv, _ := covPluginServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"id":`))
		})
		if _, err := NewPluginClient(srv.URL, "k").Add(ctx, "/a.epub", Metadata{}); err == nil || !strings.Contains(err.Error(), "decode response") {
			t.Fatalf("err = %v, want a decode error", err)
		}
	})
	t.Run("conflict names the existing row", func(t *testing.T) {
		srv, _ := covPluginServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"id":17,"duplicate":true}`))
		})
		res, err := NewPluginClient(srv.URL, "k").AddWithOptions(ctx, "/a.epub", Metadata{}, AddOptions{})
		if !errors.Is(err, ErrAlreadyInCalibre) || res.ID != 17 {
			t.Fatalf("res = %+v, err = %v; want id 17 and ErrAlreadyInCalibre", res, err)
		}
	})
	t.Run("rejection carries the code", func(t *testing.T) {
		srv, _ := covPluginServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"[Errno 2] No such file or directory","code":"path_not_found"}`))
		})
		_, err := NewPluginClient(srv.URL, "k").Add(ctx, "/a.epub", Metadata{})
		var pe *PluginError
		if !errors.As(err, &pe) || pe.Status != http.StatusBadRequest || PluginErrorCode(err) != "path_not_found" {
			t.Fatalf("err = %#v, want a PluginError with path_not_found", err)
		}
		if PluginErrorCode(errors.New("other")) != "" {
			t.Error("PluginErrorCode on a non-plugin error must be empty")
		}
	})
	t.Run("add format only for a capable plugin", func(t *testing.T) {
		var sent []bool
		srv, _ := covPluginServer(t, []string{"add_format"}, func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				AddFormat bool `json:"addFormat"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent = append(sent, body.AddFormat)
			_, _ = w.Write([]byte(`{"id":5,"format_added":true}`))
		})
		res, err := NewPluginClient(srv.URL, "k").AddWithOptions(ctx, "/a.mobi", Metadata{}, AddOptions{AddFormat: true})
		if err != nil || res.ID != 5 || !res.FormatAdded {
			t.Fatalf("res = %+v, err = %v; want id 5 with format added", res, err)
		}
		if len(sent) != 1 || !sent[0] {
			t.Fatalf("addFormat sent = %v, want [true]", sent)
		}
	})
}

// A 503 waits on the retry schedule, and a cancelled context ends the wait at
// once instead of sleeping out the backoff.
func TestPluginClient_Add503StopsWhenTheContextIsCancelled(t *testing.T) {
	prev := pluginRetryBackoff
	pluginRetryBackoff = []time.Duration{time.Minute}
	t.Cleanup(func() { pluginRetryBackoff = prev })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var posts atomic.Int32
	srv, _ := covPluginServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	start := time.Now()
	_, err := NewPluginClient(srv.URL, "k").Add(ctx, "/a.epub", Metadata{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if posts.Load() != 1 {
		t.Errorf("posts = %d, want 1 (no retry after cancel)", posts.Load())
	}
	if time.Since(start) > 10*time.Second {
		t.Error("cancel did not cut the backoff short")
	}
}
