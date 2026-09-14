package deluge_test

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/deluge"
	"github.com/vavallee/bindery/internal/downloader/infohash"
)

const seedingHash = "aabbccddeeff00112233445566778899aabbccdd"

// seed puts hash in the fake session as a finished, seeding torrent.
func seed(ds *delugeServer, hash string) {
	ds.torrents[hash] = deluge.TorrentStatus{Hash: hash, State: "Seeding", Progress: 100}
}

// TestAddTorrent_MagnetAlreadyInSession_ReturnsExistingHash covers the Deluge
// half of #2289. Re-grabbing a release whose torrent is still seeding made
// Deluge raise AddTorrentError "Torrent already in session", and the grab
// failed. The content is there, so the add must resolve to the existing
// torrent's hash, the way qBittorrent's 409 does (#769), and the rest of the
// add (the seed ratio override here) must still apply to it.
func TestAddTorrent_MagnetAlreadyInSession_ReturnsExistingHash(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	ds.rejectDuplicates = true
	seed(ds, seedingHash)
	c := clientFromServer(srv, "pw")

	hash, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:"+seedingHash+"&dn=Old+Book", "books", ratioPtr(1.5))
	if err != nil {
		t.Fatalf("a torrent already in the session must not fail the grab: %v", err)
	}
	if hash != seedingHash {
		t.Errorf("hash = %q, want %q", hash, seedingHash)
	}
	if got := ds.stopRatio[seedingHash]; got != 1.5 {
		t.Errorf("seed ratio on the existing torrent = %v, want 1.5", got)
	}
}

// TestAddTorrent_Base32MagnetAlreadyInSession_ReturnsHexHash: older trackers
// spell the btih in base32. Deluge keys the session by hex, so the recovered
// hash must be the hex form or the presence check and every later poll miss.
func TestAddTorrent_Base32MagnetAlreadyInSession_ReturnsHexHash(t *testing.T) {
	raw, err := hex.DecodeString(seedingHash)
	if err != nil {
		t.Fatal(err)
	}
	b32 := base32.StdEncoding.EncodeToString(raw)

	srv, ds := newTestServer(t, "pw")
	ds.rejectDuplicates = true
	seed(ds, seedingHash)
	c := clientFromServer(srv, "pw")

	hash, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:"+b32, "", nil)
	if err != nil {
		t.Fatalf("AddTorrent base32 duplicate: %v", err)
	}
	if hash != seedingHash {
		t.Errorf("hash = %q, want the hex form %q", hash, seedingHash)
	}
}

// TestAddTorrent_TorrentFileAlreadyInSession_ReturnsExistingHash covers the
// .torrent path, where the hash comes from the file's info dictionary.
func TestAddTorrent_TorrentFileAlreadyInSession_ReturnsExistingHash(t *testing.T) {
	torrent := []byte("d4:infod4:name8:dup.epub6:lengthi7eee")
	want := infohash.FromTorrentFile(torrent)
	if want == "" {
		t.Fatal("fixture torrent has no computable infohash")
	}
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(torrent)
	}))
	t.Cleanup(indexer.Close)

	srv, ds := newTestServer(t, "pw")
	ds.rejectDuplicates = true
	ds.realFileHash = true
	seed(ds, want)
	c := clientFromServer(srv, "pw")
	c.SetValidateTorrentURL(func(string) error { return nil })

	hash, err := c.AddTorrent(context.Background(), indexer.URL+"/dup.torrent", "books", nil)
	if err != nil {
		t.Fatalf("a .torrent already in the session must not fail the grab: %v", err)
	}
	if hash != want {
		t.Errorf("hash = %q, want %q", hash, want)
	}
}

// TestAddTorrent_NullReplyForKnownTorrent_ReturnsHash: Deluge 1.3 does not
// raise on a duplicate; libtorrent refuses it and core.add_torrent_magnet
// answers null. When the torrent is in the session that is the same outcome.
func TestAddTorrent_NullReplyForKnownTorrent_ReturnsHash(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	ds.nullOnDuplicate = true
	seed(ds, seedingHash)
	c := clientFromServer(srv, "pw")

	hash, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:"+seedingHash, "", nil)
	if err != nil {
		t.Fatalf("a null reply for a torrent the session holds must not fail the grab: %v", err)
	}
	if hash != seedingHash {
		t.Errorf("hash = %q, want %q", hash, seedingHash)
	}
}

// TestAddTorrent_DuplicateErrorButNotInSession_Fails keeps the recovery honest:
// the refusal is only believed when the torrent is actually there. Otherwise
// the grab fails with Deluge's own reason rather than tracking a hash nothing
// will ever report on.
func TestAddTorrent_DuplicateErrorButNotInSession_Fails(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	ds.alwaysDuplicate = true
	c := clientFromServer(srv, "pw")

	_, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:"+seedingHash, "", nil)
	if err == nil {
		t.Fatal("expected an error when the torrent Deluge claims to hold is not in the session")
	}
	if !strings.Contains(err.Error(), "already in session") {
		t.Errorf("the error should carry Deluge's reason, got %q", err)
	}
}
