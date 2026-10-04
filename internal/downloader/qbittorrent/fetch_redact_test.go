package qbittorrent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A download URL or redirect Location that does not parse comes back as a
// *url.Error quoting the whole URL, passkey included, and the error is stored
// on the download row and sent in failure webhooks.
func TestFetchTorrentContent_UnparseableURLDoesNotLeakCredential(t *testing.T) {
	c := newTestClient("http://127.0.0.1:1", "", "")
	allowTorrentFetch(c)
	_, err := c.fetchTorrentContent(context.Background(), "http://tracker.example/dl/%zz?passkey=S3CRETVALUE")
	if err == nil {
		t.Fatal("expected an error for an unparseable URL")
	}
	if strings.Contains(err.Error(), "S3CRETVALUE") {
		t.Fatalf("error leaks the passkey: %v", err)
	}

	// The production validator parses first, so its error must be clean too.
	c.validateTorrentURL = nil
	_, err = c.fetchTorrentContent(context.Background(), "http://tracker.example/dl/%zz?passkey=S3CRETVALUE")
	if err == nil || strings.Contains(err.Error(), "S3CRETVALUE") {
		t.Fatalf("validator error leaks the passkey: %v", err)
	}
}

func TestFetchTorrentContent_BadRedirectLocationDoesNotLeakCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://tracker.example/dl/%zz?passkey=S3CRETVALUE")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, "", "")
	allowTorrentFetch(c)
	_, err := c.fetchTorrentContent(context.Background(), srv.URL+"/dl")
	if err == nil {
		t.Fatal("expected an error for an unparseable redirect")
	}
	if strings.Contains(err.Error(), "S3CRETVALUE") {
		t.Fatalf("error leaks the passkey: %v", err)
	}
}
