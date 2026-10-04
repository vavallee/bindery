package newznab

import (
	"strings"
	"testing"
)

// With no enclosure, <link> is the download URL (parseResults signs it into
// NZBURL), and detailURL fell back to returning it raw as InfoURL, so a Jackett
// key or tracker passkey went to every client in the search response.
func TestDetailURL_DoesNotReturnCredentials(t *testing.T) {
	for name, item := range map[string]rssItem{
		"link fallback without enclosure": {
			GUID: rssGUID{IsPermaLink: "false", Value: "opaque"},
			Link: "https://jackett.example/dl/t/?jackett_apikey=REALSECRET&passkey=REALSECRET&path=x",
		},
		"guid permalink with a passkey": {
			GUID:      rssGUID{IsPermaLink: "true", Value: "https://tracker.example/details.php?id=4&passkey=REALSECRET"},
			Enclosure: rssEnclosure{URL: "https://tracker.example/download.php?id=4&passkey=REALSECRET"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := detailURL(item)
			if got == "" {
				t.Fatal("expected a detail URL")
			}
			if strings.Contains(got, "REALSECRET") {
				t.Fatalf("detail URL carries a credential: %q", got)
			}
		})
	}
}

// newznab-tmux style download links put the API key in r=, sometimes after an
// & in the path: getnzb/<guid>.nzb&i=<uid>&r=<apikey>.
func TestRedactDownloadURL_GetNZBRParam(t *testing.T) {
	for _, in := range []string{
		"https://indexer.example/getnzb/abc123.nzb&i=42&r=REALSECRET",
		"https://indexer.example/getnzb/abc123.nzb?i=42&r=REALSECRET",
		"https://indexer.example/getnzb?id=abc123&r=REALSECRET",
	} {
		got := RedactDownloadURL(in)
		if strings.Contains(got, "REALSECRET") {
			t.Errorf("RedactDownloadURL(%q) = %q, key survived", in, got)
		}
		if !strings.Contains(got, "abc123") {
			t.Errorf("RedactDownloadURL(%q) = %q, release id lost", in, got)
		}
		if got := redactAPIKey(in); strings.Contains(got, "REALSECRET") {
			t.Errorf("redactAPIKey(%q) = %q, key survived", in, got)
		}
	}
	// r= anywhere else is an ordinary parameter.
	for _, in := range []string{
		"https://tracker.example/browse?r=1&page=2",
		"https://indexer.example/api?t=search&q=dune&r=2",
	} {
		if got := RedactDownloadURL(in); got != in {
			t.Errorf("RedactDownloadURL(%q) = %q, want it unchanged", in, got)
		}
	}
}
