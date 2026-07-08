package httpsec

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "google books key first param",
			in:   `Get "https://www.googleapis.com/books/v1/volumes?key=SECRET123&q=dune": dial tcp 10.0.0.53:443`,
			want: `Get "https://www.googleapis.com/books/v1/volumes?key=REDACTED&q=dune": dial tcp 10.0.0.53:443`,
		},
		{
			name: "key as trailing param",
			in:   `https://www.googleapis.com/books/v1/volumes/abc?key=SECRET123`,
			want: `https://www.googleapis.com/books/v1/volumes/abc?key=REDACTED`,
		},
		{
			name: "key mid query",
			in:   `https://host/x?q=foo&key=SECRET123&maxResults=20`,
			want: `https://host/x?q=foo&key=REDACTED&maxResults=20`,
		},
		{
			name: "case insensitive token",
			in:   `?Token=ABCDEF`,
			want: `?Token=REDACTED`,
		},
		{
			name: "access_token",
			in:   `&access_token=xyz`,
			want: `&access_token=REDACTED`,
		},
		{
			name: "no secret untouched",
			in:   `HTTP 503: service unavailable`,
			want: `HTTP 503: service unavailable`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactSecrets(tc.in)
			if got != tc.want {
				t.Fatalf("RedactSecrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.Contains(got, "SECRET123") || strings.Contains(got, "ABCDEF") || strings.Contains(got, "xyz") {
				t.Fatalf("secret value leaked through redaction: %q", got)
			}
		})
	}
}

// sentinelNetErr stands in for the underlying net error a *url.Error wraps, so
// we can assert RedactURLError leaves the chain reachable by errors.Is/As.
type sentinelNetErr struct{}

func (sentinelNetErr) Error() string { return "dial tcp 10.0.0.53:443: i/o timeout" }

func TestRedactURLError(t *testing.T) {
	inner := sentinelNetErr{}
	ue := &url.Error{
		Op:  "Get",
		URL: "https://indexer.example/download?id=42&apikey=SECRET123",
		Err: inner,
	}

	// Wrap it the way the download clients do, to prove the apikey does not
	// survive stringification even one %w-layer up.
	wrapped := fmt.Errorf("fetch torrent from indexer: %w", RedactURLError(ue))

	if strings.Contains(wrapped.Error(), "SECRET123") {
		t.Fatalf("apikey leaked through redaction: %q", wrapped.Error())
	}
	if !strings.Contains(wrapped.Error(), "apikey=REDACTED") {
		t.Fatalf("expected apikey=REDACTED, got %q", wrapped.Error())
	}
	// The chain must stay intact so nethint can still classify the net error.
	if !errors.As(wrapped, new(*url.Error)) {
		t.Fatal("*url.Error no longer reachable via errors.As")
	}
	if !errors.Is(wrapped, inner) {
		t.Fatal("underlying net error no longer reachable via errors.Is")
	}

	// Non-url.Error values pass through unchanged.
	plain := errors.New("plain failure")
	got := RedactURLError(plain)
	if got.Error() != plain.Error() {
		t.Fatalf("non-url.Error message changed: got %q, want %q", got.Error(), plain.Error())
	}
	if !errors.Is(got, plain) {
		t.Fatal("non-url.Error should pass through unchanged")
	}
}
