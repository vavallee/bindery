package qbittorrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func init() {
	// Keep the tests that exercise the untagged diff fallback quick.
	tagGrace = 50 * time.Millisecond
}

// fakeQbit is a qBittorrent stand in for the tag tests. Its list is the
// snapshot before the add, then whatever after() returns once an add happened.
type fakeQbit struct {
	mu          sync.Mutex
	srv         *httptest.Server
	added       bool
	infoCalls   int
	failInfoNth int // 1 based info call that returns 500, 0 for none
	before      string
	after       func(tag string) string
	addTags     string
	catHashes   []string
	autoHashes  []string
	deletedTags []string
	addStatus   int
	addBody     string
	uploadSeen  bool
}

func newFakeQbit(t *testing.T) *fakeQbit {
	t.Helper()
	f := &fakeQbit{addStatus: http.StatusOK, addBody: "Ok."}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
				_ = r.ParseMultipartForm(1 << 20)
				f.uploadSeen = true
				f.addTags = r.FormValue("tags")
			} else {
				_ = r.ParseForm()
				f.addTags = r.Form.Get("tags")
			}
			f.added = true
			w.WriteHeader(f.addStatus)
			_, _ = w.Write([]byte(f.addBody))
		case "/api/v2/torrents/info":
			f.infoCalls++
			if f.failInfoNth != 0 && f.infoCalls == f.failInfoNth {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if !f.added {
				_, _ = w.Write([]byte(f.before))
				return
			}
			_, _ = w.Write([]byte(f.after(f.addTags)))
		case "/api/v2/torrents/setCategory":
			_ = r.ParseForm()
			f.catHashes = append(f.catHashes, r.Form.Get("hashes"))
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/setAutoManagement":
			_ = r.ParseForm()
			f.autoHashes = append(f.autoHashes, r.Form.Get("hashes"))
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/deleteTags":
			_ = r.ParseForm()
			f.deletedTags = append(f.deletedTags, r.Form.Get("tags"))
			_, _ = w.Write([]byte("Ok."))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

const strangerHash = "ffffffffffffffffffffffffffffffffffffffff"
const ourHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// torrentURLClient returns a client plus a URL whose body is not a parseable
// torrent, so no infohash is derivable from the payload and the recovery has to
// lean on the tag or the diff.
func torrentURLClient(t *testing.T, f *fakeQbit) (*Client, string) {
	t.Helper()
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("d3:foo3:bare"))
	}))
	t.Cleanup(indexer.Close)
	c := newTestClient(f.srv.URL, "u", "p")
	allowTorrentFetch(c)
	return c, indexer.URL + "/t"
}

func (f *fakeQbit) assertOnlyOursTouched(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range append(append([]string{}, f.catHashes...), f.autoHashes...) {
		if h != ourHash {
			t.Fatalf("settings were applied to %q, only %q is ours", h, ourHash)
		}
	}
}

// A stranger lands in the same second as our torrent and is listed first. The
// old newest-AddedOn tie-break kept the first row, so the stranger won.
func TestAddTorrent_Tag_SameSecondStrangerListedFirstIsNotAdopted(t *testing.T) {
	f := newFakeQbit(t)
	f.before = `[{"hash":"1111111111111111111111111111111111111111","added_on":900}]`
	f.after = func(tag string) string {
		return fmt.Sprintf(`[{"hash":%q,"name":"video","added_on":1000,"tags":""},{"hash":%q,"name":"book","added_on":1000,"tags":%q}]`,
			strangerHash, ourHash, "other,"+tag)
	}
	c, u := torrentURLClient(t, f)
	got, err := c.AddTorrent(context.Background(), u, "books", "")
	if err != nil || got != ourHash {
		t.Fatalf("AddTorrent = %q, %v; want %q", got, err, ourHash)
	}
	f.assertOnlyOursTouched(t)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(f.addTags, "bindery-add-") {
		t.Errorf("add carried tags %q, want a bindery-add- marker", f.addTags)
	}
	if len(f.deletedTags) != 1 || f.deletedTags[0] != f.addTags {
		t.Errorf("marker tag cleanup = %v, want [%q]", f.deletedTags, f.addTags)
	}
	if len(f.catHashes) != 1 {
		t.Errorf("setCategory calls = %v, want exactly one", f.catHashes)
	}
}

// A stranger shows up first and ours is not visible yet. With a tag in play the
// stranger must not be adopted while ours is still arriving.
func TestAddTorrent_Tag_StrangerAloneThenOursAppears(t *testing.T) {
	f := newFakeQbit(t)
	f.before = `[]`
	polls := 0
	f.after = func(tag string) string {
		polls++
		stranger := fmt.Sprintf(`{"hash":%q,"added_on":1000,"tags":""}`, strangerHash)
		if polls < 3 {
			return "[" + stranger + "]"
		}
		return fmt.Sprintf(`[%s,{"hash":%q,"added_on":1000,"tags":%q}]`, stranger, ourHash, tag)
	}
	c, u := torrentURLClient(t, f)
	tagGrace = 10 * time.Second
	t.Cleanup(func() { tagGrace = 50 * time.Millisecond })
	got, err := c.AddTorrent(context.Background(), u, "books", "")
	if err != nil || got != ourHash {
		t.Fatalf("AddTorrent = %q, %v; want %q", got, err, ourHash)
	}
	f.assertOnlyOursTouched(t)
}

// The before-snapshot fails. The tag still identifies the torrent, even though
// every torrent in the client is unseen.
func TestAddTorrent_Tag_SnapshotFailureStillResolvesByTag(t *testing.T) {
	f := newFakeQbit(t)
	f.failInfoNth = 1
	f.after = func(tag string) string {
		return fmt.Sprintf(`[{"hash":%q,"added_on":1000,"tags":""},{"hash":%q,"added_on":1000,"tags":%q}]`, strangerHash, ourHash, tag)
	}
	c, u := torrentURLClient(t, f)
	got, err := c.AddTorrent(context.Background(), u, "books", "")
	if err != nil || got != ourHash {
		t.Fatalf("AddTorrent = %q, %v; want %q", got, err, ourHash)
	}
	f.assertOnlyOursTouched(t)
}

// Snapshot failed and the client ignores tags: nothing can identify our torrent,
// so the call fails and touches nothing, instead of treating every torrent as a
// candidate.
func TestAddTorrent_SnapshotFailureWithoutTagSupportFailsClosed(t *testing.T) {
	f := newFakeQbit(t)
	f.failInfoNth = 1
	// One lone row, which could just as well be a stranger's: adopting it
	// would be a guess.
	f.after = func(string) string {
		return fmt.Sprintf(`[{"hash":%q,"added_on":1000}]`, strangerHash)
	}
	c, u := torrentURLClient(t, f)
	origTimeout := hashPollTimeout
	hashPollTimeout = 200 * time.Millisecond
	t.Cleanup(func() { hashPollTimeout = origTimeout })
	got, err := c.AddTorrent(context.Background(), u, "books", "")
	if err == nil || got != "" {
		t.Fatalf("AddTorrent = %q, %v; want an error and no hash", got, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.catHashes)+len(f.autoHashes) != 0 {
		t.Fatalf("settings applied to %v %v despite no identification", f.catHashes, f.autoHashes)
	}
}

// Client that ignores tags, snapshot fine, exactly one new torrent: the plain
// diff still works (older clients, API emulators).
func TestAddTorrent_NoTagSupport_SingleNewTorrentAdopted(t *testing.T) {
	f := newFakeQbit(t)
	f.before = `[{"hash":"1111111111111111111111111111111111111111","added_on":900}]`
	f.after = func(string) string {
		return fmt.Sprintf(`[{"hash":"1111111111111111111111111111111111111111","added_on":900},{"hash":%q,"added_on":1000}]`, ourHash)
	}
	c, u := torrentURLClient(t, f)
	got, err := c.AddTorrent(context.Background(), u, "books", "")
	if err != nil || got != ourHash {
		t.Fatalf("AddTorrent = %q, %v; want %q", got, err, ourHash)
	}
	f.assertOnlyOursTouched(t)
}

// Client that ignores tags and two torrents are new in the same second: refuse
// rather than pick one.
func TestAddTorrent_NoTagSupport_AmbiguousDiffRefuses(t *testing.T) {
	f := newFakeQbit(t)
	f.before = `[]`
	f.after = func(string) string {
		return fmt.Sprintf(`[{"hash":%q,"added_on":1000},{"hash":%q,"added_on":1000}]`, strangerHash, ourHash)
	}
	c, u := torrentURLClient(t, f)
	got, err := c.AddTorrent(context.Background(), u, "books", "")
	if err == nil || got != "" || !strings.Contains(err.Error(), "none can be tied") {
		t.Fatalf("AddTorrent = %q, %v; want an ambiguity error", got, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.catHashes)+len(f.autoHashes) != 0 {
		t.Fatalf("settings applied to %v %v", f.catHashes, f.autoHashes)
	}
}

// A valid .torrent upload: the infohash derived from the bytes is authoritative,
// a stranger in the list is ignored, and the upload carries the tag.
func TestAddTorrent_TorrentFileUpload_ExpectedHashWins(t *testing.T) {
	payload := []byte("d8:announce3:foo4:infod6:lengthi1e4:name1:a12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")
	want := infoHashFromTorrentFile(payload)
	if want == "" {
		t.Fatal("test setup: payload has no derivable infohash")
	}
	f := newFakeQbit(t)
	f.before = `[]`
	f.after = func(string) string {
		return fmt.Sprintf(`[{"hash":%q,"added_on":1000},{"hash":%q,"added_on":1000}]`, strangerHash, want)
	}
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer indexer.Close()
	c := newTestClient(f.srv.URL, "u", "p")
	allowTorrentFetch(c)
	got, err := c.AddTorrent(context.Background(), indexer.URL+"/t", "books", "")
	if err != nil || got != want {
		t.Fatalf("AddTorrent = %q, %v; want %q", got, err, want)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.uploadSeen || !strings.HasPrefix(f.addTags, "bindery-add-") {
		t.Errorf("upload seen=%v tags=%q; want a tagged multipart upload", f.uploadSeen, f.addTags)
	}
	for _, h := range f.catHashes {
		if h == strangerHash {
			t.Fatal("stranger recategorised")
		}
	}
}

// The derived v1 hash is a preferred match, not an exclusive one. A v2 only (or
// hybrid) torrent never shows that hash in the list, and must still resolve by
// its tag while a stranger listed first is left alone.
func TestAddTorrent_TorrentFileUpload_DerivedHashAbsentResolvesByTag(t *testing.T) {
	payload := []byte("d8:announce3:foo4:infod6:lengthi1e4:name1:a12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")
	if infoHashFromTorrentFile(payload) == "" {
		t.Fatal("test setup: payload has no derivable infohash")
	}
	f := newFakeQbit(t)
	f.before = `[]`
	f.after = func(tag string) string {
		// The client reports a different (v2 truncated) hash for our torrent.
		return fmt.Sprintf(`[{"hash":%q,"added_on":1000,"tags":""},{"hash":%q,"added_on":1000,"tags":%q}]`, strangerHash, ourHash, tag)
	}
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer indexer.Close()
	c := newTestClient(f.srv.URL, "u", "p")
	allowTorrentFetch(c)
	got, err := c.AddTorrent(context.Background(), indexer.URL+"/t", "books", "")
	if err != nil || got != ourHash {
		t.Fatalf("AddTorrent = %q, %v; want %q", got, err, ourHash)
	}
	f.assertOnlyOursTouched(t)
}

// The urlencoded add (a magnet without a btih here) must carry the tags field.
func TestAddTorrent_URLEncodedAddCarriesTag(t *testing.T) {
	f := newFakeQbit(t)
	f.before = `[]`
	f.after = func(tag string) string {
		return fmt.Sprintf(`[{"hash":%q,"added_on":1000,"tags":%q}]`, ourHash, tag)
	}
	c := newTestClient(f.srv.URL, "u", "p")
	got, err := c.AddTorrent(context.Background(), "magnet:?dn=no-btih-here", "books", "")
	if err != nil || got != ourHash {
		t.Fatalf("AddTorrent = %q, %v; want %q", got, err, ourHash)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(f.addTags, "bindery-add-") {
		t.Errorf("urlencoded add carried tags %q, want a bindery-add- marker", f.addTags)
	}
}

// 409 duplicate keeps working and needs no list lookup.
func TestAddTorrent_Duplicate409_StillRecoversHashFromMagnet(t *testing.T) {
	f := newFakeQbit(t)
	f.addStatus = http.StatusConflict
	f.addBody = "Fails."
	f.before = `[]`
	f.after = func(string) string { return `[]` }
	c := newTestClient(f.srv.URL, "u", "p")
	magnet := "magnet:?xt=urn:btih:" + strings.Repeat("b", 40)
	got, err := c.AddTorrent(context.Background(), magnet, "books", "")
	if err != nil || got != strings.Repeat("b", 40) {
		t.Fatalf("AddTorrent = %q, %v", got, err)
	}
}

func TestAddTorrent_Magnet_NotTagged(t *testing.T) {
	f := newFakeQbit(t)
	f.before = `[]`
	f.after = func(string) string { return `[]` }
	c := newTestClient(f.srv.URL, "u", "p")
	if _, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:"+strings.Repeat("c", 40), "books", ""); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addTags != "" || len(f.deletedTags) != 0 {
		t.Errorf("magnet with btih was tagged: %q %v", f.addTags, f.deletedTags)
	}
}
