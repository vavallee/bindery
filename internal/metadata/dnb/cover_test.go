package dnb

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCoverByISBN_ReturnsURLOnImageContentType(t *testing.T) {
	var capturedURL string
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				capturedURL = r.URL.String()
				if r.Method != http.MethodHead {
					t.Errorf("expected HEAD, got %s", r.Method)
				}
				h := make(http.Header)
				h.Set("Content-Type", "image/jpeg")
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     h,
				}, nil
			}),
		},
	}
	got := c.CoverByISBN(context.Background(), "9783844935776")
	if got == "" {
		t.Fatal("CoverByISBN returned empty for image/jpeg 200")
	}
	if !strings.Contains(got, "9783844935776") {
		t.Errorf("returned URL %q does not contain the queried ISBN", got)
	}
	if !strings.Contains(capturedURL, mvbCoverEndpoint) {
		t.Errorf("request URL %q does not hit MVB endpoint", capturedURL)
	}
}

func TestCoverByISBN_ReturnsEmptyOn404(t *testing.T) {
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 404,
					Body:       io.NopCloser(strings.NewReader("not found")),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}
	if got := c.CoverByISBN(context.Background(), "9783844904994"); got != "" {
		t.Errorf("expected empty on 404, got %q", got)
	}
}

func TestCoverByISBN_ReturnsEmptyOnNonImageContentType(t *testing.T) {
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				h := make(http.Header)
				h.Set("Content-Type", "text/html; charset=utf-8")
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader("<html>not an image</html>")),
					Header:     h,
				}, nil
			}),
		},
	}
	if got := c.CoverByISBN(context.Background(), "9780000000000"); got != "" {
		t.Errorf("expected empty when MVB returns text/html, got %q", got)
	}
}

// TestCoverByISBN_StripsISBNQualifier confirms that hyphenated and
// qualified ISBNs (e.g. "978-3-8449-3577-6 (audio)") get normalised to
// the bare digit run before being sent to MVB — saves round-trips and
// matches MVB's documented expectation.
func TestCoverByISBN_StripsISBNQualifier(t *testing.T) {
	var capturedURL string
	c := &Client{
		http: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				capturedURL = r.URL.String()
				h := make(http.Header)
				h.Set("Content-Type", "image/jpeg")
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     h,
				}, nil
			}),
		},
	}
	c.CoverByISBN(context.Background(), "978-3-8449-3577-6 (audio)")
	if !strings.Contains(capturedURL, "9783844935776") {
		t.Errorf("expected normalised ISBN in MVB URL, got %q", capturedURL)
	}
}
