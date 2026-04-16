package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newFakeUpstream starts a test HTTP server that responds with the given
// content-type and body. Caller must call Close().
func newFakeUpstream(ct string, body []byte, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
}

// newTestHandler creates an ImageProxyHandler wired to the given test server.
// It bypasses the SSRF validator (which would block 127.0.0.1) while keeping
// all other production logic intact.
func newTestHandler(dir string, upstream *httptest.Server) *ImageProxyHandler {
	h := NewImageProxyHandler(dir)
	h.client = upstream.Client()
	h.validateURL = func(_ string) error { return nil } // allow loopback in tests
	return h
}

// TestImageProxy_CacheMiss fetches an image for the first time (cache miss):
// the handler must contact upstream, write the cache files, and respond 200
// with the correct Content-Type.
func TestImageProxy_CacheMiss(t *testing.T) {
	upstream := newFakeUpstream("image/jpeg", []byte("FAKEJPEG"), http.StatusOK)
	defer upstream.Close()

	dir := t.TempDir()
	h := newTestHandler(dir, upstream)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+upstream.URL+"/cover.jpg", nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	if body := rr.Body.String(); body != "FAKEJPEG" {
		t.Errorf("body = %q, want FAKEJPEG", body)
	}

	// Cache directory should now contain the key file and .ct sidecar.
	entries, _ := os.ReadDir(filepath.Join(dir, "image-cache"))
	if len(entries) < 2 {
		t.Errorf("expected at least 2 cache files (body + .ct), got %d", len(entries))
	}
}

// TestImageProxy_CacheHit serves the same URL twice; the second request must
// be served from cache (upstream server is shut down before the second call).
func TestImageProxy_CacheHit(t *testing.T) {
	upstream := newFakeUpstream("image/png", []byte("FAKEPNG"), http.StatusOK)

	dir := t.TempDir()
	h := newTestHandler(dir, upstream)

	imgURL := upstream.URL + "/img.png"
	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+imgURL, nil)
	rr1 := httptest.NewRecorder()
	h.Serve(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first request: status = %d, want 200", rr1.Code)
	}

	// Shut the upstream down — second request must be served from disk.
	upstream.Close()

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+imgURL, nil)
	rr2 := httptest.NewRecorder()
	h.Serve(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second request (cache hit): status = %d, want 200", rr2.Code)
	}
	if ct := rr2.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
}

// TestImageProxy_MissingURL ensures a missing url parameter returns 400.
func TestImageProxy_MissingURL(t *testing.T) {
	h := NewImageProxyHandler(t.TempDir())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/images", nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// TestImageProxy_SSRFRejected verifies that private/loopback URLs are blocked.
func TestImageProxy_SSRFRejected(t *testing.T) {
	h := NewImageProxyHandler(t.TempDir())

	ssrfURLs := []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/cover.jpg",
		"http://192.168.1.1/cover.jpg",
		"http://127.0.0.1/cover.jpg",
		"file:///etc/passwd",
	}
	for _, u := range ssrfURLs {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+u, nil)
		rr := httptest.NewRecorder()
		h.Serve(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("url %q: status = %d, want 400 (SSRF rejected)", u, rr.Code)
		}
	}
}

// TestImageProxy_NonImageRejected ensures non-image content-types from
// upstream are rejected with 502.
func TestImageProxy_NonImageRejected(t *testing.T) {
	upstream := newFakeUpstream("text/html", []byte("<html/>"), http.StatusOK)
	defer upstream.Close()

	h := newTestHandler(t.TempDir(), upstream)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+upstream.URL+"/not-an-image", nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 for non-image content-type", rr.Code)
	}
}

// TestImageProxy_UpstreamNon200 ensures a non-200 from upstream returns 502.
func TestImageProxy_UpstreamNon200(t *testing.T) {
	upstream := newFakeUpstream("image/jpeg", nil, http.StatusNotFound)
	defer upstream.Close()

	h := newTestHandler(t.TempDir(), upstream)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+upstream.URL+"/missing.jpg", nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 for upstream 404", rr.Code)
	}
}

// TestImageProxy_SizeLimit enforces the 10 MB cap.
func TestImageProxy_SizeLimit(t *testing.T) {
	big := make([]byte, imageMaxBytes+1)
	upstream := newFakeUpstream("image/jpeg", big, http.StatusOK)
	defer upstream.Close()

	h := newTestHandler(t.TempDir(), upstream)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+upstream.URL+"/huge.jpg", nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 for oversized image", rr.Code)
	}
}

// TestProxyImageURL covers the ProxyImageURL helper for various inputs.
func TestProxyImageURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"/already/relative", "/already/relative"},
		{"https://covers.openlibrary.org/b/id/123-L.jpg", "/api/v1/images?url=https%3A%2F%2Fcovers.openlibrary.org%2Fb%2Fid%2F123-L.jpg"},
		{"http://example.com/img.png", "/api/v1/images?url=http%3A%2F%2Fexample.com%2Fimg.png"},
	}
	for _, tc := range cases {
		got := ProxyImageURL(tc.in)
		if got != tc.want {
			t.Errorf("ProxyImageURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
		// Proxied result must start with /api/v1/images when non-trivial.
		if tc.in != "" && !strings.HasPrefix(tc.in, "/") {
			if !strings.HasPrefix(got, "/api/v1/images?url=") {
				t.Errorf("ProxyImageURL(%q): result %q should start with /api/v1/images?url=", tc.in, got)
			}
		}
	}
}
