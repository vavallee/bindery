package abs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func covCliClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL+"/", "key")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAPIError_Error(t *testing.T) {
	if got := (&APIError{StatusCode: 502}).Error(); got != "abs api error (502)" {
		t.Errorf("empty message Error() = %q", got)
	}
	if got := (&APIError{StatusCode: 401, Message: "nope"}).Error(); got != "nope" {
		t.Errorf("Error() = %q, want message", got)
	}
}

func TestNormalizeBaseURL_Cases(t *testing.T) {
	ok := map[string]string{
		" http://abs.lan:13378/ ":          "http://abs.lan:13378",
		"https://abs.lan/sub/path//?q=1#f": "https://abs.lan/sub/path",
		"http://abs.lan/":                  "http://abs.lan",
	}
	for in, want := range ok {
		got, err := NormalizeBaseURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]string{
		"":                  "required",
		"   ":               "required",
		"ftp://abs.lan":     "must use http or https",
		"abs:x1":            "must use http or https",
		"http://":           "missing a host",
		"http://%zz":        "base_url",
		"audiobookshelf":    "missing a scheme",
		"audiobookshelf:80": "missing a scheme",
	}
	for in, want := range bad {
		_, err := NormalizeBaseURL(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("NormalizeBaseURL(%q) err = %v, want containing %q", in, err, want)
		}
	}
}

func TestIsAllDigits(t *testing.T) {
	for in, want := range map[string]bool{"": false, "123": true, "12a": false, "-1": false} {
		if got := isAllDigits(in); got != want {
			t.Errorf("isAllDigits(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestValidateBaseURLSecure(t *testing.T) {
	for _, in := range []string{"http://127.0.0.1:13378", "http://192.168.1.10:13378/", "https://10.0.0.5"} {
		if _, err := ValidateBaseURLSecure(in); err != nil {
			t.Errorf("ValidateBaseURLSecure(%q) = %v, want allowed", in, err)
		}
	}
	// Link-local and cloud metadata are refused even though they are
	// well-formed http URLs.
	for _, in := range []string{"http://169.254.169.254/latest/meta-data", "http://[fe80::1]:13378"} {
		if _, err := ValidateBaseURLSecure(in); err == nil {
			t.Errorf("ValidateBaseURLSecure(%q) allowed, want SSRF refusal", in)
		}
	}
	// Normalisation errors surface before the policy check.
	if _, err := ValidateBaseURLSecure("ftp://abs.lan"); err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Errorf("ftp err = %v", err)
	}
}

func TestNewClient_Validation(t *testing.T) {
	if _, err := NewClient("", "k"); err == nil {
		t.Error("empty base URL accepted")
	}
	if _, err := NewClient("http://abs.lan", "  "); err == nil || !strings.Contains(err.Error(), "api_key is required") {
		t.Errorf("blank key err = %v", err)
	}
}

func TestClient_UserAgentOverrides(t *testing.T) {
	var got atomic.Value
	c := covCliClient(t, func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("User-Agent"))
		_, _ = io.WriteString(w, `{"libraries":[]}`)
	})
	ctx := context.Background()

	c.WithUserAgent("  custom-agent/1  ")
	if _, err := c.ListLibraries(ctx); err != nil {
		t.Fatal(err)
	}
	if got.Load() != "custom-agent/1" {
		t.Errorf("UA = %v, want trimmed custom agent", got.Load())
	}

	c.WithVersion("9.9.9")
	if _, err := c.ListLibraries(ctx); err != nil {
		t.Fatal(err)
	}
	if ua, _ := got.Load().(string); ua != UserAgent("9.9.9") {
		t.Errorf("UA = %q, want %q", ua, UserAgent("9.9.9"))
	}

	// Blank falls back to the process default rather than sending no UA.
	c.WithUserAgent("   ")
	if _, err := c.ListLibraries(ctx); err != nil {
		t.Fatal(err)
	}
	if ua, _ := got.Load().(string); ua == "" || ua == "custom-agent/1" {
		t.Errorf("UA after blank override = %q, want the default", ua)
	}
}

func TestClient_ListAndGetLibrary(t *testing.T) {
	var paths []string
	c := covCliClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		switch {
		case r.URL.Path == "/api/libraries":
			_, _ = io.WriteString(w, `{"libraries":[{"id":"lib1","name":"Audiobooks","mediaType":"book"}]}`)
		case strings.HasPrefix(r.URL.Path, "/api/libraries/"):
			_, _ = io.WriteString(w, `{"id":"lib 2","name":"Podcasts","mediaType":"podcast"}`)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()

	libs, err := c.ListLibraries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 1 || libs[0].ID != "lib1" || libs[0].Name != "Audiobooks" {
		t.Fatalf("libraries = %+v", libs)
	}

	lib, err := c.GetLibrary(ctx, " lib 2 ")
	if err != nil {
		t.Fatal(err)
	}
	if lib.ID != "lib 2" || lib.Name != "Podcasts" {
		t.Errorf("library = %+v", lib)
	}
	if paths[len(paths)-1] != "/api/libraries/lib%202" {
		t.Errorf("GetLibrary path = %q, want escaped id", paths[len(paths)-1])
	}

	if _, err := c.GetLibrary(ctx, "  "); err == nil {
		t.Error("GetLibrary with blank id should fail without a request")
	}
	if n := len(paths); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestClient_EmptyIDsRejected(t *testing.T) {
	c := covCliClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request expected")
		w.WriteHeader(http.StatusTeapot)
	})
	ctx := context.Background()
	if _, err := c.ListLibraryItems(ctx, " ", 0, 10); err == nil {
		t.Error("ListLibraryItems blank id accepted")
	}
	if _, err := c.GetLibraryItem(ctx, ""); err == nil {
		t.Error("GetLibraryItem blank id accepted")
	}
}

func TestClient_ListLibraryItems_OmitsNegativePageAndZeroLimit(t *testing.T) {
	var rawQuery string
	c := covCliClient(t, func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"results":[],"total":0}`)
	})
	if _, err := c.ListLibraryItems(context.Background(), "lib", -1, 0); err != nil {
		t.Fatal(err)
	}
	if rawQuery != "minified=1" {
		t.Errorf("query = %q, want only minified=1", rawQuery)
	}
}

func TestClient_MalformedJSON(t *testing.T) {
	c := covCliClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"libraries": [`)
	})
	_, err := c.ListLibraries(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decode GET /api/libraries response") {
		t.Fatalf("err = %v, want decode error naming the request", err)
	}
}

func TestClient_DecodeAPIErrorVariants(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"message field", http.StatusForbidden, `{"message":"forbidden here"}`, "forbidden here"},
		{"error wins over message", http.StatusBadRequest, `{"error":"bad","message":"ignored"}`, "bad"},
		{"non-string error falls back to body", http.StatusBadRequest, `{"error":42}`, `{"error":42}`},
		{"plain text body", http.StatusNotFound, "no such library\n", "no such library"},
		{"empty body uses status", http.StatusConflict, "", "409 Conflict"},
		{"3xx is an error too", http.StatusNotModified, "", "304 Not Modified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := covCliClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := c.ListLibraries(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v (%T), want *APIError", err, err)
			}
			if apiErr.StatusCode != tc.status || apiErr.Message != tc.want {
				t.Errorf("APIError = %d %q, want %d %q", apiErr.StatusCode, apiErr.Message, tc.status, tc.want)
			}
		})
	}
}

// A 5xx is retried after a backoff. Cancelling the context during the
// backoff must stop the retry loop at once rather than waiting the delay out
// and sending another request.
func TestClient_ServerErrorRetryStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var hits atomic.Int32
	c := covCliClient(t, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		cancel()
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "upstream down")
	})
	start := time.Now()
	_, err := c.ListLibraries(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if hits.Load() != 1 {
		t.Errorf("server hits = %d, want 1", hits.Load())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancelled retry took %v; backoff ignored the context", elapsed)
	}
}

// On the final attempt a 5xx is no longer retried: it is decoded and
// returned as an APIError.
func TestClient_ServerErrorOnLastAttemptIsReturned(t *testing.T) {
	var hits atomic.Int32
	c := covCliClient(t, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"maintenance"}`)
	})
	// Make every backoff return immediately by handing doJSON an already
	// cancelled context only for the sleep: a transport that ignores the
	// request context lets the attempts proceed.
	c.httpClient = &http.Client{Transport: covCliIgnoreCtxTransport{base: http.DefaultTransport}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.ListLibraries(ctx)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable || apiErr.Message != "maintenance" {
		t.Fatalf("err = %v, want APIError 503 maintenance", err)
	}
	if hits.Load() != maxAttempts {
		t.Errorf("server hits = %d, want %d", hits.Load(), maxAttempts)
	}
}

// covCliIgnoreCtxTransport strips the request context so an already
// cancelled context only short-circuits sleepBackoff, not the request.
type covCliIgnoreCtxTransport struct{ base http.RoundTripper }

func (t covCliIgnoreCtxTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(r.WithContext(context.Background()))
}

// covCliScriptTransport returns the scripted results in order.
type covCliScriptTransport struct {
	calls   atomic.Int32
	results []func(*http.Request) (*http.Response, error)
}

func (t *covCliScriptTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	i := int(t.calls.Add(1)) - 1
	if i >= len(t.results) {
		i = len(t.results) - 1
	}
	return t.results[i](r)
}

func covCliJSONResponse(r *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}
}

func TestClient_TimeoutIsRetriedThenSucceeds(t *testing.T) {
	c, err := NewClient("http://abs.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	tr := &covCliScriptTransport{results: []func(*http.Request) (*http.Response, error){
		func(*http.Request) (*http.Response, error) { return nil, retryNetError{timeout: true} },
		func(r *http.Request) (*http.Response, error) {
			return covCliJSONResponse(r, `{"libraries":[{"id":"after-retry"}]}`), nil
		},
	}}
	c.httpClient = &http.Client{Transport: tr}
	// The first backoff (150ms) is cut short by a context that is already
	// done; the scripted transport does not consult it.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	libs, err := c.ListLibraries(ctx)
	if err != nil {
		t.Fatalf("err = %v, want success on the retry", err)
	}
	if len(libs) != 1 || libs[0].ID != "after-retry" {
		t.Errorf("libs = %+v", libs)
	}
	if tr.calls.Load() != 2 {
		t.Errorf("transport calls = %d, want 2", tr.calls.Load())
	}
}

func TestClient_PermanentTransportErrorIsNotRetried(t *testing.T) {
	c, err := NewClient("http://abs.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	permanent := errors.New("connection refused")
	tr := &covCliScriptTransport{results: []func(*http.Request) (*http.Response, error){
		func(*http.Request) (*http.Response, error) { return nil, permanent },
	}}
	c.httpClient = &http.Client{Transport: tr}
	if _, err := c.ListLibraries(context.Background()); !errors.Is(err, permanent) {
		t.Fatalf("err = %v, want the transport error", err)
	}
	if tr.calls.Load() != 1 {
		t.Errorf("transport calls = %d, want 1", tr.calls.Load())
	}
}

func TestClient_TimeoutOnEveryAttemptReturnsLastError(t *testing.T) {
	c, err := NewClient("http://abs.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	tr := &covCliScriptTransport{results: []func(*http.Request) (*http.Response, error){
		func(*http.Request) (*http.Response, error) { return nil, retryNetError{timeout: true} },
	}}
	c.httpClient = &http.Client{Transport: tr}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.ListLibraries(ctx)
	if err == nil {
		t.Fatal("expected an error")
	}
	var ne retryNetError
	if !errors.As(err, &ne) || !ne.timeout {
		t.Errorf("err = %v, want the timeout error from the last attempt", err)
	}
	if tr.calls.Load() != maxAttempts {
		t.Errorf("transport calls = %d, want %d", tr.calls.Load(), maxAttempts)
	}
}

func TestClient_BadMethodFailsBeforeSending(t *testing.T) {
	c, err := NewClient("http://abs.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.doJSON(context.Background(), "BAD METHOD", "/api/x", nil, nil); err == nil {
		t.Error("invalid method should fail building the request")
	}
}

func TestClient_DoJSONSendsBodyContentType(t *testing.T) {
	var ct, auth string
	c := covCliClient(t, func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.doJSON(context.Background(), http.MethodPost, "/api/x", strings.NewReader(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if ct != "application/json" || auth != "Bearer key" {
		t.Errorf("Content-Type = %q, Authorization = %q", ct, auth)
	}
}

func TestSleepBackoff_ReturnsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	sleepBackoff(ctx, 5) // nominally 4.8s
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("sleepBackoff ignored a cancelled context for %v", elapsed)
	}
}

func TestDrainAndClose(t *testing.T) {
	drainAndClose(nil) // must not panic
	rc := &covCliTrackingBody{Reader: strings.NewReader("leftover")}
	drainAndClose(rc)
	if !rc.closed || rc.Len() != 0 {
		t.Errorf("closed=%v remaining=%d, want drained and closed", rc.closed, rc.Len())
	}
}

type covCliTrackingBody struct {
	*strings.Reader
	closed bool
}

func (b *covCliTrackingBody) Close() error { b.closed = true; return nil }

// --- ScanNotifier ---------------------------------------------------------------

// One in-memory DB for the whole lifecycle: running every migration costs
// about a second, so the stages share it and end by closing it.
func TestScanNotifier_Lifecycle(t *testing.T) {
	ctx := context.Background()
	var hits atomic.Int32
	var method, path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	settings := db.NewSettingsRepo(database)
	n := NewScanNotifier(settings)

	// Nothing configured: a silent no-op.
	if err := n.ScanLibrary(ctx, "lib"); err != nil {
		t.Fatalf("unconfigured ScanLibrary = %v, want nil", err)
	}
	// Base URL without a key is still unconfigured.
	if err := settings.Set(ctx, "abs.base_url", srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := n.ScanLibrary(ctx, "lib"); err != nil {
		t.Fatalf("half-configured ScanLibrary = %v, want nil", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("unconfigured notifier reached the server %d times", hits.Load())
	}

	// Fully configured: credentials are read at call time and the scan is
	// posted to the right library.
	if err := settings.Set(ctx, "abs.api_key", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := n.ScanLibrary(ctx, "lib-1"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/api/libraries/lib-1/scan" || auth != "Bearer secret" {
		t.Errorf("request = %s %s auth=%q", method, path, auth)
	}

	// An invalid stored base URL surfaces as an error, not a silent skip.
	if err := settings.Set(ctx, "abs.base_url", "ftp://abs.lan"); err != nil {
		t.Fatal(err)
	}
	if err := n.ScanLibrary(ctx, "lib-1"); err == nil {
		t.Error("invalid base URL should surface an error")
	}

	// A settings read failure is treated as unconfigured.
	_ = database.Close()
	if err := n.ScanLibrary(ctx, "lib-1"); err != nil {
		t.Errorf("settings read failure should act unconfigured, got %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("server hits = %d, want 1", hits.Load())
	}
}

// --- metadata conflicts --------------------------------------------------------

func TestConflictFieldLabel(t *testing.T) {
	if got := ConflictFieldLabel("image_url"); got != "Cover" {
		t.Errorf("label = %q", got)
	}
	if got := ConflictFieldLabel("mystery"); got != "mystery" {
		t.Errorf("unknown label = %q, want the field name", got)
	}
}

func TestAuthorConflictValues_RoundTrip(t *testing.T) {
	if SerializeAuthorConflictValue(nil, "description") != "" {
		t.Error("nil author serialised to non-empty")
	}
	if err := ApplyAuthorConflictValue(nil, "description", "x"); err == nil {
		t.Error("nil author apply should fail")
	}
	a := &models.Author{}
	for field, value := range map[string]string{
		"image_url":      "https://img/x.jpg",
		"disambiguation": "the poet",
		"sort_name":      "Doe, Jane",
	} {
		if err := ApplyAuthorConflictValue(a, field, "  "+value+"  "); err != nil {
			t.Fatalf("apply %s: %v", field, err)
		}
		if got := SerializeAuthorConflictValue(a, field); got != value {
			t.Errorf("%s round trip = %q, want %q", field, got, value)
		}
	}
	if err := ApplyAuthorConflictValue(a, "description", "A writer."); err != nil || a.Description == "" {
		t.Errorf("description apply err=%v value=%q", err, a.Description)
	}
	if err := ApplyAuthorConflictValue(a, "bogus", "x"); err == nil {
		t.Error("unsupported field accepted")
	}
	if SerializeAuthorConflictValue(a, "bogus") != "" {
		t.Error("unknown field serialised to non-empty")
	}
}

func TestBookConflictValues_RoundTrip(t *testing.T) {
	if SerializeBookConflictValue(nil, "language") != "" {
		t.Error("nil book serialised to non-empty")
	}
	if err := ApplyBookConflictValue(nil, "language", "en"); err == nil {
		t.Error("nil book apply should fail")
	}
	b := &models.Book{}
	cases := []struct{ field, in, want string }{
		{"image_url", " https://img/c.png ", "https://img/c.png"},
		{"original_title", " Orig ", "Orig"},
		{"release_date", "2021-03-04", "2021-03-04"},
		{"release_date", "2020-01-02T23:30:00-05:00", "2020-01-03"},
		{"language", " ENG ", "eng"},
		{"average_rating", " 4.25 ", "4.25"},
		{"ratings_count", " 120 ", "120"},
		{"description", "Some <b>text</b>", SerializeBookConflictValue(&models.Book{Description: "Some <b>text</b>"}, "description")},
	}
	for _, tc := range cases {
		if err := ApplyBookConflictValue(b, tc.field, tc.in); err != nil {
			t.Fatalf("apply %s: %v", tc.field, err)
		}
		if got := SerializeBookConflictValue(b, tc.field); got != tc.want {
			t.Errorf("%s(%q) round trip = %q, want %q", tc.field, tc.in, got, tc.want)
		}
	}
	// Unparseable or blank numeric/date values clear the field.
	for _, tc := range []struct{ field, in string }{
		{"release_date", "next spring"},
		{"release_date", "  "},
		{"average_rating", "lots"},
		{"average_rating", ""},
		{"ratings_count", "1.5"},
		{"ratings_count", " "},
	} {
		if err := ApplyBookConflictValue(b, tc.field, tc.in); err != nil {
			t.Fatalf("apply %s: %v", tc.field, err)
		}
		if got := SerializeBookConflictValue(b, tc.field); got != "" {
			t.Errorf("%s(%q) = %q, want cleared", tc.field, tc.in, got)
		}
	}
	if err := ApplyBookConflictValue(b, "bogus", "x"); err == nil {
		t.Error("unsupported field accepted")
	}
	if SerializeBookConflictValue(b, "bogus") != "" {
		t.Error("unknown field serialised to non-empty")
	}
}
