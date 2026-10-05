package deluge_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

const labelTestMagnet = "magnet:?xt=urn:btih:aabbccddeeff00112233445566778899aabbccdd&dn=Test+Book"
const labelTestHash = "aabbccddeeff00112233445566778899aabbccdd"

// lockedBuffer is a bytes.Buffer safe for the slog handler and the test to
// share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureWarnings(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return buf
}

// TestAddTorrent_CapitalCategoryIsLabelled: the Label plugin stores every
// label lowercased and set_torrent only accepts the stored id, so a category
// typed as "Books" has to be sent as "books" or the torrent is left
// unlabelled (#2665).
func TestAddTorrent_CapitalCategoryIsLabelled(t *testing.T) {
	srv, ds := newTestServer(t, "pw")
	// Created in the Deluge UI as "Books", which the plugin keeps as "books".
	ds.labels = map[string]bool{"books": true}
	c := clientFromServer(srv, "pw")

	hash, err := c.AddTorrent(context.Background(), labelTestMagnet, "Books", nil)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if got := ds.torrentLabels[hash]; got != "books" {
		t.Errorf("torrent label = %q, want %q", got, "books")
	}
}

// TestAddTorrent_UnknownLabelWarns: a label Deluge does not have still adds
// the torrent, but the failure is logged at WARN instead of discarded.
func TestAddTorrent_UnknownLabelWarns(t *testing.T) {
	logs := captureWarnings(t)
	srv, ds := newTestServer(t, "pw")
	ds.labels = map[string]bool{"tv": true}
	c := clientFromServer(srv, "pw")

	hash, err := c.AddTorrent(context.Background(), labelTestMagnet, "books", nil)
	if err != nil {
		t.Fatalf("AddTorrent must not fail over a label: %v", err)
	}
	if hash != labelTestHash {
		t.Errorf("hash = %q", hash)
	}
	if _, labelled := ds.torrentLabels[hash]; labelled {
		t.Errorf("torrent was labelled %q, want none", ds.torrentLabels[hash])
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "Unknown Label") || !strings.Contains(out, "label=books") {
		t.Errorf("expected a WARN naming the label and Deluge's error, got:\n%s", out)
	}
}
