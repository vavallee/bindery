package qbittorrent

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sampleInfoDict is a realistic single-file bencoded "info" dictionary.
const sampleInfoDict = "d6:lengthi5e4:name8:test.txt12:piece lengthi16384e6:pieces20:" +
	"\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14e"

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestInfoHashFromTorrentFile(t *testing.T) {
	want := sha1Hex(sampleInfoDict)

	t.Run("info is the first key", func(t *testing.T) {
		torrent := "d4:info" + sampleInfoDict + "8:announce10:udp://t/ane"
		if got := infoHashFromTorrentFile([]byte(torrent)); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("info after other keys", func(t *testing.T) {
		torrent := "d8:announce10:udp://t/an13:creation datei1700000000e4:info" + sampleInfoDict + "e"
		if got := infoHashFromTorrentFile([]byte(torrent)); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("info followed by another key", func(t *testing.T) {
		// Verifies the value span stops at the info dict's own closing 'e'
		// rather than running into the next key.
		torrent := "d4:info" + sampleInfoDict + "7:comment5:helloe"
		if got := infoHashFromTorrentFile([]byte(torrent)); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	negatives := map[string]string{
		"empty":             "",
		"not a dictionary":  "i5e",
		"no info key":       "d8:announce10:udp://t/ane",
		"truncated":         "d4:info",
		"bad string length": "d99:infod" + sampleInfoDict,
	}
	for name, in := range negatives {
		t.Run(name, func(t *testing.T) {
			if got := infoHashFromTorrentFile([]byte(in)); got != "" {
				t.Fatalf("got %q, want empty string", got)
			}
		})
	}
}

// TestAddTorrent_Duplicate409_Magnet is a regression test for #769.
// qBittorrent returns 409 Conflict from POST /torrents/add when it already
// holds the torrent. That is not a real failure — the content is available —
// so AddTorrent recovers the existing torrent's hash from the magnet and
// succeeds instead of reporting "failed to send to downloader".
func TestAddTorrent_Duplicate409_Magnet(t *testing.T) {
	const hash = "abcdef0123456789abcdef0123456789abcdef01"
	magnet := "magnet:?xt=urn:btih:" + hash + "&dn=Book"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte("Conflict"))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	c.loggedIn = true

	got, err := c.AddTorrent(context.Background(), magnet, "", "")
	if err != nil {
		t.Fatalf("409 on a torrent qBittorrent already holds must not fail: %v", err)
	}
	if got != hash {
		t.Errorf("recovered hash: want %q, got %q", hash, got)
	}
}

// TestAddTorrent_Duplicate409_File covers the #769 duplicate path when the
// torrent is submitted as a file upload. The 409 body carries no hash, so it
// is recovered from the .torrent bytes (SHA-1 of the bencoded info dict).
func TestAddTorrent_Duplicate409_File(t *testing.T) {
	torrent := "d8:announce11:udp://t/ann4:info" + sampleInfoDict + "e"
	wantHash := sha1Hex(sampleInfoDict)

	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/torrent" {
			_, _ = w.Write([]byte(torrent))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer indexer.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			w.WriteHeader(http.StatusConflict)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	allowTorrentFetch(c)
	c.loggedIn = true

	got, err := c.AddTorrent(context.Background(), indexer.URL+"/torrent", "", "")
	if err != nil {
		t.Fatalf("409 on a file-upload add must not fail: %v", err)
	}
	if got != wantHash {
		t.Errorf("recovered hash: want %q, got %q", wantHash, got)
	}
}

// TestAddTorrent_NonConflictErrorStillFails confirms the 409 handling did not
// soften other non-200 responses — a real failure must still surface.
func TestAddTorrent_NonConflictErrorStillFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "admin", "pass")
	c.loggedIn = true

	_, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01", "", "")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected a 500 error to still fail, got %v", err)
	}
}
