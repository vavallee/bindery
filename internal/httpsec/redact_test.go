package httpsec

import (
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
