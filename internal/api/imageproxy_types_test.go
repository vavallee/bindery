package api

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// Real magic numbers for each type the proxy serves. The bodies are not
// decodable pictures; they only need to sniff to the right type.
var (
	typesJPEG = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}, []byte("JFIF\x00payload")...)
	typesPNG  = append([]byte("\x89PNG\r\n\x1a\n"), []byte("\x00\x00\x00\rIHDRpayload")...)
	typesGIF  = []byte("GIF89a\x01\x00\x01\x00payload")
	typesWebP = append(append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), 0, 0, 0, 0), []byte("payload")...)
	typesAVIF = append([]byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf"), []byte("payload")...)
	typesHTML = []byte("<!DOCTYPE html><html><body><script>document.title='x'</script></body></html>")
)

const typesCSP = "default-src 'none'; sandbox"

func typesGet(h http.Handler, path, raw string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path+"?url="+url.QueryEscape(raw), nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// failingTransport proves a request was answered from the cache.
type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("no network in this test")
}

// A list valued upstream Content-Type must never be relayed. The body is what
// is judged, so HTML under an image label is refused on the first fetch and
// on the next request too.
func TestImageProxy_ListValuedUpstreamTypeRefused(t *testing.T) {
	for _, ct := range []string{"image/jpeg, text/html", "image/png,text/javascript", "image/jpeg;text/html"} {
		t.Run(ct, func(t *testing.T) {
			upstream := newFakeUpstream(ct, typesHTML, http.StatusOK)
			defer upstream.Close()
			h := newTestHandler(t.TempDir(), upstream)
			for i := 0; i < 2; i++ {
				rr := typesGet(http.HandlerFunc(h.Serve), "/api/v1/images", upstream.URL+"/p")
				if rr.Code != http.StatusBadGateway {
					t.Fatalf("pass %d: status = %d (Content-Type %q), want 502", i, rr.Code, rr.Header().Get("Content-Type"))
				}
			}
		})
	}
}

// Real image bytes under a lying header are served with the type the bytes
// have, not the type the upstream claimed, on the miss and on the hit.
func TestImageProxy_ServesSniffedTypeNotUpstreamHeader(t *testing.T) {
	upstream := newFakeUpstream("image/jpeg, text/html", typesPNG, http.StatusOK)
	defer upstream.Close()
	h := newTestHandler(t.TempDir(), upstream)
	for i := 0; i < 2; i++ {
		rr := typesGet(http.HandlerFunc(h.Serve), "/api/v1/images", upstream.URL+"/p")
		if rr.Code != http.StatusOK {
			t.Fatalf("pass %d: status = %d, want 200", i, rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
			t.Errorf("pass %d: Content-Type = %q, want image/png", i, ct)
		}
	}
}

// Each supported type is served with its own type and the lockdown headers,
// on both the API route and the OPDS route, miss and hit.
func TestImageProxy_SupportedTypesServedWithHeaders(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"jpeg", typesJPEG, "image/jpeg"},
		{"png", typesPNG, "image/png"},
		{"webp", typesWebP, "image/webp"},
		{"gif", typesGIF, "image/gif"},
		{"avif", typesAVIF, "image/avif"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The upstream label is deliberately wrong.
			upstream := newFakeUpstream("application/octet-stream", tc.body, http.StatusOK)
			defer upstream.Close()
			h := newTestHandler(t.TempDir(), upstream)
			srv := SecurityHeaders("")(http.HandlerFunc(h.Serve))
			for _, route := range []string{"/api/v1/images", "/opds/images"} {
				for i := 0; i < 2; i++ {
					rr := typesGet(srv, route, upstream.URL+"/"+tc.name)
					if rr.Code != http.StatusOK {
						t.Fatalf("%s pass %d: status = %d, want 200", route, i, rr.Code)
					}
					if ct := rr.Header().Get("Content-Type"); ct != tc.want {
						t.Errorf("%s pass %d: Content-Type = %q, want %q", route, i, ct, tc.want)
					}
					if got := rr.Header().Get("Content-Security-Policy"); got != typesCSP {
						t.Errorf("%s pass %d: Content-Security-Policy = %q, want %q", route, i, got, typesCSP)
					}
					if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
						t.Errorf("%s pass %d: X-Content-Type-Options = %q, want nosniff", route, i, got)
					}
					if !bytes.Equal(rr.Body.Bytes(), tc.body) {
						t.Errorf("%s pass %d: body altered", route, i)
					}
				}
			}
		})
	}
}

func TestImageProxy_NonImageBodiesRefused(t *testing.T) {
	bodies := map[string][]byte{
		"html": typesHTML,
		"svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>`),
		"bmp":  []byte("BM\x00\x00\x00\x00\x00\x00\x00\x00payload"),
		"text": []byte("plain"),
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			upstream := newFakeUpstream("image/jpeg", body, http.StatusOK)
			defer upstream.Close()
			h := newTestHandler(t.TempDir(), upstream)
			rr := typesGet(http.HandlerFunc(h.Serve), "/api/v1/images", upstream.URL+"/p")
			if rr.Code != http.StatusBadGateway {
				t.Errorf("status = %d, want 502", rr.Code)
			}
		})
	}
}

// typesPlant writes a cache entry the way an older release would have left
// it, and returns a handler pointed at that directory.
func typesPlant(t *testing.T, rawURL string, body []byte, sidecar string) *ImageProxyHandler {
	t.Helper()
	dir := t.TempDir()
	sum := sha256.Sum256([]byte(rawURL))
	key := fmt.Sprintf("%x", sum)
	shard := filepath.Join(dir, "image-cache", key[:2])
	if err := os.MkdirAll(shard, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shard, key), body, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shard, key+".ct"), []byte(sidecar), 0o640); err != nil {
		t.Fatal(err)
	}
	h := NewImageProxyHandler(dir)
	h.validateURL = func(string) error { return nil }
	return h
}

// A sidecar written by an older release can hold anything. A value that is not
// exactly one of the served types is never relayed.
func TestImageProxy_CacheRejectsMismatchedBytes(t *testing.T) {
	for _, sidecar := range []string{"image/jpeg, text/html", "text/html", "image/jpeg; charset=x", "image/svg+xml", "IMAGE/JPEG"} {
		t.Run(sidecar, func(t *testing.T) {
			const raw = "http://127.0.0.1:1/mismatched"
			h := typesPlant(t, raw, typesHTML, sidecar)
			rr := typesGet(http.HandlerFunc(h.Serve), "/api/v1/images", raw)
			if rr.Code == http.StatusOK {
				t.Fatalf("status 200, Content-Type %q, want the entry refused", rr.Header().Get("Content-Type"))
			}
		})
	}
}

// A mismatched entry is dropped and refetched, so a good upstream heals it.
func TestImageProxy_CacheMismatchedEntryIsRefetched(t *testing.T) {
	upstream := newFakeUpstream("image/jpeg", typesJPEG, http.StatusOK)
	defer upstream.Close()
	raw := upstream.URL + "/heal"
	h := typesPlant(t, raw, typesHTML, "image/jpeg, text/html")
	h.client = upstream.Client()
	rr := typesGet(http.HandlerFunc(h.Serve), "/api/v1/images", raw)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(rr.Body.Bytes(), typesJPEG) {
		t.Fatalf("status = %d, Content-Type = %q, want a refetched image/jpeg", rr.Code, rr.Header().Get("Content-Type"))
	}
	// The healed entry now passes the hit path on its own.
	h.client = &http.Client{Transport: failingTransport{}}
	rr = typesGet(http.HandlerFunc(h.Serve), "/api/v1/images", raw)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("second request: status = %d, Content-Type = %q", rr.Code, rr.Header().Get("Content-Type"))
	}
}

// A well formed sidecar does not vouch for the bytes: they must sniff to it.
func TestImageProxy_CacheHitSidecarMustMatchBytes(t *testing.T) {
	const raw = "http://127.0.0.1:1/mismatch"
	for _, tc := range []struct {
		name string
		body []byte
	}{{"html under jpeg", typesHTML}, {"png under jpeg", typesPNG}} {
		t.Run(tc.name, func(t *testing.T) {
			h := typesPlant(t, raw, tc.body, "image/jpeg")
			rr := typesGet(http.HandlerFunc(h.Serve), "/api/v1/images", raw)
			if rr.Code == http.StatusOK {
				t.Fatalf("status 200 (Content-Type %q), want the entry refused", rr.Header().Get("Content-Type"))
			}
		})
	}
}

func TestImageProxy_CacheHitGoodEntryHasCSP(t *testing.T) {
	const raw = "http://127.0.0.1:1/good"
	h := typesPlant(t, raw, typesWebP, "image/webp")
	rr := typesGet(http.HandlerFunc(h.Serve), "/api/v1/images", raw)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/webp" {
		t.Fatalf("status = %d, Content-Type = %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	if got := rr.Header().Get("Content-Security-Policy"); got != typesCSP {
		t.Errorf("Content-Security-Policy = %q, want %q", got, typesCSP)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestSniffImageType(t *testing.T) {
	cases := []struct {
		want string
		body []byte
	}{
		{"image/jpeg", typesJPEG}, {"image/png", typesPNG}, {"image/gif", typesGIF},
		{"image/webp", typesWebP}, {"image/avif", typesAVIF},
		{"", []byte("<svg/>")},
		{"", []byte("\x00\x00\x00\x1cftypmp42\x00\x00\x00\x00")},
		{"", nil},
	}
	for _, c := range cases {
		if got := sniffImageType(c.body); got != c.want {
			t.Errorf("sniffImageType(%.12q) = %q, want %q", c.body, got, c.want)
		}
	}
}
