package nzbfetch

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// endedAt fakes the final request of a fetch chain — the URL the redirects
// actually landed on (what resp.Request holds after client.Do). Responses are
// built as literals at each call site: a helper returning *http.Response
// trips bodyclose even though these fabricated responses have no body.
func endedAt(t *testing.T, finalURL string) *http.Request {
	t.Helper()
	u, err := url.Parse(finalURL)
	if err != nil {
		t.Fatalf("parse %q: %v", finalURL, err)
	}
	return &http.Request{URL: u}
}

const nzbfinder203 = `<?xml version="1.0" encoding="UTF-8"?>
<error code="203" description="This application is not allowed to download NZBs from NZBFinder."/>`

func TestError_ParsesNewznabErrorDocument(t *testing.T) {
	err := Error("https://indexer.example.com/getnzb?id=1", &http.Response{StatusCode: 400, Request: endedAt(t, "https://indexer.example.com/getnzb?id=1")}, []byte(nzbfinder203))

	msg := err.Error()
	for _, want := range []string{"fetch nzb:", "newznab error 203", "not allowed to download NZBs", "HTTP 400"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q: %s", want, msg)
		}
	}
	// Same host end to end — no redirect guidance.
	if strings.Contains(msg, "redirected") {
		t.Errorf("no cross-host hop happened, but error mentions a redirect: %s", msg)
	}
}

// The #1404 shape: the release's nzbUrl points at Prowlarr, Prowlarr 302s to
// the indexer (its per-indexer Redirect setting), the indexer rejects
// Bindery's identity. The error must name both hosts and the Prowlarr setting.
func TestError_CrossHostRedirectAddsProwlarrGuidance(t *testing.T) {
	err := Error("http://prowlarr:9696/3/download?apikey=k&link=abc", &http.Response{StatusCode: 400, Request: endedAt(t, "https://nzbfinder.ws/getnzb/abc")}, []byte(nzbfinder203))

	msg := err.Error()
	for _, want := range []string{"newznab error 203", `redirected from "prowlarr" to "nzbfinder.ws"`, "Redirect setting", "whitelisted identity"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q: %s", want, msg)
		}
	}
}

func TestError_SameHostDifferentPortIsNotAHop(t *testing.T) {
	err := Error("http://indexer.example.com:8080/getnzb", &http.Response{StatusCode: 502, Request: endedAt(t, "https://indexer.example.com/getnzb")}, []byte("bad gateway"))
	if msg := err.Error(); strings.Contains(msg, "redirected") {
		t.Errorf("scheme/port rewrite on the same host must not read as a hand-off: %s", msg)
	}
}

func TestError_NonXMLBodyFallsBackToRawSnippet(t *testing.T) {
	err := Error("https://indexer.example.com/getnzb", &http.Response{StatusCode: 503, Request: endedAt(t, "https://indexer.example.com/getnzb")}, []byte("  <html>overloaded</html>  "))
	msg := err.Error()
	if !strings.Contains(msg, "indexer returned HTTP 503: <html>overloaded</html>") {
		t.Errorf("raw fallback wrong: %s", msg)
	}
}

func TestError_RawSnippetIsCapped(t *testing.T) {
	err := Error("https://indexer.example.com/getnzb", &http.Response{StatusCode: 500, Request: endedAt(t, "https://indexer.example.com/getnzb")}, []byte(strings.Repeat("x", MaxErrorBody)))
	if msg := err.Error(); len(msg) > rawBodyCap+200 {
		t.Errorf("raw body not capped, error is %d bytes", len(msg))
	}
}

func TestError_NilFinalRequestIsSafe(t *testing.T) {
	// Defensive: a response with no Request must not panic or emit hop text.
	err := Error("https://indexer.example.com/getnzb", &http.Response{StatusCode: 404}, []byte("not found"))
	if msg := err.Error(); strings.Contains(msg, "redirected") {
		t.Errorf("unexpected redirect text: %s", msg)
	}
}
