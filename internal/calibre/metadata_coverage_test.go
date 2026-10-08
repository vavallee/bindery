package calibre

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseTransport(t *testing.T) {
	cases := map[string]Transport{
		"pull":           TransportPull,
		" PULL ":         TransportPull,
		"push":           TransportPush,
		"":               TransportPush,
		"carrier pigeon": TransportPush,
	}
	for in, want := range cases {
		if got := ParseTransport(in); got != want {
			t.Errorf("ParseTransport(%q) = %q, want %q", in, got, want)
		}
	}
	if !TransportPush.Valid() || !TransportPull.Valid() || Transport("smoke").Valid() {
		t.Error("Valid must accept push and pull only")
	}
	if TransportPull.String() != "pull" {
		t.Errorf("String = %q, want pull", TransportPull.String())
	}
}

func TestNormalizeLanguageForCalibre_EveryMappedCode(t *testing.T) {
	cases := map[string]string{
		"eng": "en", "fre": "fr", "fra": "fr", "ger": "de", "deu": "de", "spa": "es",
		"ita": "it", "por": "pt", "dut": "nl", "nld": "nl", "swe": "sv", "dan": "da",
		"nor": "no", "fin": "fi", "rus": "ru", "jpn": "ja", "chi": "zh", "zho": "zh",
		"kor": "ko", "pol": "pl", "tur": "tr", "ukr": "uk", "ara": "ar", "ind": "id",
		"tgl": "tl", "fil": "tl", " HEB ": "heb", "  ": "",
	}
	for in, want := range cases {
		if got := NormalizeLanguageForCalibre(in); got != want {
			t.Errorf("NormalizeLanguageForCalibre(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanList_DropsBlanksAndCaseInsensitiveDuplicates(t *testing.T) {
	if got := cleanList(nil); got != nil {
		t.Errorf("cleanList(nil) = %v, want nil", got)
	}
	got := cleanList([]string{" Fantasy ", "", "fantasy", "Sci-Fi", "  "})
	if want := []string{"Fantasy", "Sci-Fi"}; !reflect.DeepEqual(got, want) {
		t.Errorf("cleanList = %v, want %v", got, want)
	}
}

func TestCoverExt(t *testing.T) {
	cases := map[string]string{
		"image/jpeg":                ".jpg",
		"IMAGE/JPG; charset=binary": ".jpg",
		"image/png":                 ".png",
		"image/webp":                ".webp",
		"image/gif":                 ".gif",
		"text/html":                 "",
		"":                          "",
	}
	for in, want := range cases {
		if got := coverExt(in); got != want {
			t.Errorf("coverExt(%q) = %q, want %q", in, got, want)
		}
	}
}

// covRoundTripper answers every request in-process, so MaterializeCover can
// be driven past its SSRF check (which needs a public address) without
// touching the network.
type covRoundTripper struct {
	calls atomic.Int32
	fn    func(*http.Request) (*http.Response, error)
}

func (c *covRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return c.fn(r)
}

func covStubCoverClient(t *testing.T, fn func(*http.Request) (*http.Response, error)) *covRoundTripper {
	t.Helper()
	rt := &covRoundTripper{fn: fn}
	prev := coverFetchClient
	coverFetchClient = &http.Client{Transport: rt, CheckRedirect: prev.CheckRedirect}
	t.Cleanup(func() { coverFetchClient = prev })
	return rt
}

func covResponse(status int, contentType string, body []byte) *http.Response {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader(body))}
}

// A public IP literal: ValidateOutboundURL accepts it without DNS.
const covPublicCover = "https://93.184.216.34/covers/dune.png"

func TestMaterializeCover_DownloadsIntoTheCache(t *testing.T) {
	png := []byte("\x89PNG fake image bytes")
	rt := covStubCoverClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != covPublicCover {
			return nil, fmt.Errorf("unexpected url %s", r.URL)
		}
		return covResponse(http.StatusOK, "image/png", png), nil
	})
	cacheDir := filepath.Join(t.TempDir(), "nested", "cache")

	path, err := MaterializeCover(context.Background(), cacheDir, "  "+covPublicCover+"  ")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(covPublicCover))
	if want := filepath.Join(cacheDir, fmt.Sprintf("%x.png", sum)); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, png) {
		t.Fatalf("cached file = %q, %v; want the downloaded bytes", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != coverFileMode {
		t.Errorf("cover mode = %v, %v; want %v", info.Mode().Perm(), err, os.FileMode(coverFileMode))
	}
	// No temp file is left behind.
	entries, _ := os.ReadDir(cacheDir)
	if len(entries) != 1 {
		t.Errorf("cache holds %d entries, want only the cover", len(entries))
	}

	// The second call is served from the cache with no fetch.
	again, err := MaterializeCover(context.Background(), cacheDir, covPublicCover)
	if err != nil || again != path {
		t.Fatalf("second call = %q, %v; want the cached %q", again, err, path)
	}
	if rt.calls.Load() != 1 {
		t.Errorf("fetches = %d, want 1", rt.calls.Load())
	}
}

func TestMaterializeCover_Refusals(t *testing.T) {
	rt := covStubCoverClient(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("must not be called")
	})
	ctx := context.Background()
	dir := t.TempDir()

	if p, err := MaterializeCover(ctx, "", covPublicCover); p != "" || err != nil {
		t.Errorf("no cache dir: %q, %v; want nothing", p, err)
	}
	if p, err := MaterializeCover(ctx, dir, "   "); p != "" || err != nil {
		t.Errorf("blank url: %q, %v; want nothing", p, err)
	}
	for _, raw := range []string{
		"http://127.0.0.1/cover.jpg",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.5/cover.jpg",
		"file:///etc/passwd",
	} {
		if p, err := MaterializeCover(ctx, dir, raw); err == nil || p != "" {
			t.Errorf("MaterializeCover(%q) = %q, %v; want an SSRF refusal", raw, p, err)
		}
	}
	if rt.calls.Load() != 0 {
		t.Errorf("refused URLs reached the transport %d times", rt.calls.Load())
	}
}

func TestMaterializeCover_BadResponses(t *testing.T) {
	// Each case is the status, content type and body size of the answer, or
	// a transport error instead of one.
	cases := map[string]struct {
		status  int
		ctype   string
		size    int
		err     error
		wantErr string
	}{
		"status":       {status: http.StatusNotFound, ctype: "image/png", wantErr: "status 404"},
		"not an image": {status: http.StatusOK, ctype: "text/html", size: 6, wantErr: "not an image"},
		"too large":    {status: http.StatusOK, ctype: "image/jpeg", size: coverMaxBytes + 1, wantErr: "exceeds"},
		"transport":    {err: errors.New("connection reset"), wantErr: "connection reset"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			covStubCoverClient(t, func(*http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return covResponse(tc.status, tc.ctype, bytes.Repeat([]byte{1}, tc.size)), nil
			})
			dir := t.TempDir()
			p, err := MaterializeCover(context.Background(), dir, covPublicCover)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || p != "" {
				t.Fatalf("got %q, %v; want error containing %q", p, err, tc.wantErr)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("a failed fetch left %d files in the cache", len(entries))
			}
		})
	}
}

// The cache directory cannot be created when its parent is a file: the
// download is discarded with the error.
func TestMaterializeCover_UnwritableCacheDir(t *testing.T) {
	covStubCoverClient(t, func(*http.Request) (*http.Response, error) {
		return covResponse(http.StatusOK, "image/gif", []byte("GIF89a")), nil
	})
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := MaterializeCover(context.Background(), filepath.Join(blocker, "cache"), covPublicCover); err == nil || p != "" {
		t.Fatalf("got %q, %v; want an error for an uncreatable cache dir", p, err)
	}
}

func TestMaterializeCover_CancelledContext(t *testing.T) {
	covStubCoverClient(t, func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := MaterializeCover(ctx, t.TempDir(), covPublicCover); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
