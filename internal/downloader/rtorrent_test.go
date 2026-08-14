package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/rtorrent"
	"github.com/vavallee/bindery/internal/models"
)

// --- adapter-level wiring ---------------------------------------------------

const rtorrentTestHash = "2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e"

// rtorrentStub is a minimal XML-RPC endpoint for the adapter tests: it records
// every method it was asked for, and answers d.multicall2 with one row.
type rtorrentStub struct {
	*httptest.Server
	mu      sync.Mutex
	methods []string
	bodies  []string
	// torrent is the d.multicall2 row: complete, active, labelled "books".
	complete string
	message  string
}

func newRtorrentStub(t *testing.T, complete, message string) *rtorrentStub {
	t.Helper()
	s := &rtorrentStub{complete: complete, message: message}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(body))
		for _, m := range []string{"d.multicall2", "load.start", "load.raw_start", "d.hash", "d.erase", "d.base_path", "directory.default"} {
			if strings.Contains(string(body), "<methodName>"+m+"</methodName>") {
				s.methods = append(s.methods, m)
				break
			}
		}
		s.mu.Unlock()

		w.Header().Set("Content-Type", "text/xml")
		switch {
		case strings.Contains(string(body), "d.multicall2"):
			_, _ = io.WriteString(w, fmt.Sprintf(`<?xml version="1.0"?><methodResponse><params><param><value><array><data>
<value><array><data>
<value><string>the-book</string></value>
<value><string>%s</string></value>
<value><string>/seedbox/downloads/the-book</string></value>
<value><string>/seedbox/downloads/the-book</string></value>
<value><string>books</string></value>
<value><i8>1000</i8></value>
<value><i8>250</i8></value>
<value><i8>125</i8></value>
<value><i8>%s</i8></value>
<value><i8>1</i8></value>
<value><i8>1</i8></value>
<value><string>%s</string></value>
</data></array></value>
</data></array></value></param></params></methodResponse>`, strings.ToUpper(rtorrentTestHash), s.complete, s.message))
		case strings.Contains(string(body), "d.hash"):
			_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><string>`+strings.ToUpper(rtorrentTestHash)+`</string></value></param></params></methodResponse>`)
		case strings.Contains(string(body), "d.base_path"):
			_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><string>/seedbox/downloads/the-book</string></value></param></params></methodResponse>`)
		default:
			_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><i8>0</i8></value></param></params></methodResponse>`)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *rtorrentStub) sawMethod(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.methods {
		if m == name {
			return true
		}
	}
	return false
}

func (s *rtorrentStub) allBodies() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.bodies, "\n")
}

func (s *rtorrentStub) client(t *testing.T, id int64) *models.DownloadClient {
	t.Helper()
	host, port := serverHostPort(t, s.URL)
	return &models.DownloadClient{ID: id, Type: "rtorrent", Host: host, Port: port, Category: "books"}
}

func TestGetLiveStatuses_Rtorrent(t *testing.T) {
	stub := newRtorrentStub(t, "0", "")
	statuses, usesTorrentID, err := GetLiveStatuses(context.Background(), stub.client(t, 101))
	if err != nil {
		t.Fatalf("GetLiveStatuses: %v", err)
	}
	if !usesTorrentID {
		t.Fatal("rTorrent keys statuses by torrent hash")
	}
	// The key must be the lower-cased hash rTorrent reports upper-case, or the
	// poller never matches the stored Download.TorrentID.
	st, ok := statuses[rtorrentTestHash]
	if !ok {
		t.Fatalf("status not keyed by lower-cased hash: %v", statuses)
	}
	if st.Percentage != "75.0" {
		t.Errorf("percentage: got %q, want 75.0", st.Percentage)
	}
	if st.Size != 1000 || st.SizeLeft != 250 {
		t.Errorf("sizes: got %d/%d", st.Size, st.SizeLeft)
	}
	// 250 bytes left at 125 B/s = 2s; rTorrent reports no ETA of its own.
	if st.TimeLeft != "2s" {
		t.Errorf("time left: got %q, want 2s", st.TimeLeft)
	}
	if st.Status != "downloading" {
		t.Errorf("status: got %q", st.Status)
	}
}

func TestGetStalledIDs_Rtorrent(t *testing.T) {
	t.Run("errored torrent is stalled", func(t *testing.T) {
		stub := newRtorrentStub(t, "0", "Tracker: unregistered torrent")
		ids, usesTorrentID, err := GetStalledIDs(context.Background(), stub.client(t, 102))
		if err != nil {
			t.Fatalf("GetStalledIDs: %v", err)
		}
		if !usesTorrentID {
			t.Fatal("expected hash keys")
		}
		if !ids[rtorrentTestHash] {
			t.Fatalf("expected the errored torrent to be stalled, got %v", ids)
		}
	})

	t.Run("a message on a complete torrent is not a stall", func(t *testing.T) {
		// Tracker chatter on a seeding torrent says nothing about its files.
		stub := newRtorrentStub(t, "1", "Tracker: unregistered torrent")
		ids, _, err := GetStalledIDs(context.Background(), stub.client(t, 103))
		if err != nil {
			t.Fatalf("GetStalledIDs: %v", err)
		}
		if len(ids) != 0 {
			t.Fatalf("expected no stalls, got %v", ids)
		}
	})
}

func TestSendDownload_Rtorrent(t *testing.T) {
	stub := newRtorrentStub(t, "0", "")
	client := stub.client(t, 104)
	// rTorrent writes under /seedbox/downloads; Bindery reads it at /media/books.
	client.PathRemap = "/seedbox/downloads:/media/books"

	magnet := "magnet:?xt=urn:btih:" + rtorrentTestHash
	res, err := SendDownload(context.Background(), client, magnet, "The Book", SendOptions{DownloadDir: "/media/books"})
	if err != nil {
		t.Fatalf("SendDownload: %v", err)
	}
	if res.Protocol != "torrent" || !res.UsesTorrentID {
		t.Errorf("result: %+v", res)
	}
	if res.RemoteID != rtorrentTestHash {
		t.Errorf("remote ID: got %q, want %q", res.RemoteID, rtorrentTestHash)
	}
	if !stub.sawMethod("load.start") {
		t.Fatal("magnet was not submitted with load.start")
	}
	body := stub.allBodies()
	if !strings.Contains(body, "d.custom1.set=&#34;books&#34;") {
		t.Errorf("label command missing from request: %s", body)
	}
	// The download directory must be sent in rTorrent's namespace, i.e. the
	// PathRemap applied in reverse.
	if !strings.Contains(body, "d.directory.set=&#34;/seedbox/downloads&#34;") {
		t.Errorf("download directory not inverse-remapped: %s", body)
	}
}

func TestRemoveDownload_Rtorrent(t *testing.T) {
	t.Run("erases the torrent", func(t *testing.T) {
		stub := newRtorrentStub(t, "1", "")
		hash := rtorrentTestHash
		err := RemoveDownload(context.Background(), stub.client(t, 105), &models.Download{TorrentID: &hash}, false, "")
		if err != nil {
			t.Fatalf("RemoveDownload: %v", err)
		}
		if !stub.sawMethod("d.erase") {
			t.Fatal("d.erase was not called")
		}
		if stub.sawMethod("d.base_path") {
			t.Fatal("the data path must only be resolved when deleteFiles is set")
		}
	})

	t.Run("nil torrent id is a no-op", func(t *testing.T) {
		stub := newRtorrentStub(t, "1", "")
		if err := RemoveDownload(context.Background(), stub.client(t, 106), &models.Download{}, true, ""); err != nil {
			t.Fatalf("RemoveDownload: %v", err)
		}
		if stub.sawMethod("d.erase") {
			t.Fatal("nothing should be sent for a download with no torrent id")
		}
	})

	t.Run("deletes the data when asked", func(t *testing.T) {
		// rTorrent has no delete-with-data command, so Bindery removes the
		// payload itself at the remapped local path before erasing.
		root := t.TempDir()
		payload := filepath.Join(root, "the-book")
		if err := os.MkdirAll(payload, 0o755); err != nil {
			t.Fatal(err)
		}
		stub := newRtorrentStub(t, "1", "")
		client := stub.client(t, 107)
		client.PathRemap = "/seedbox/downloads:" + root

		hash := rtorrentTestHash
		if err := RemoveDownload(context.Background(), client, &models.Download{TorrentID: &hash}, true, ""); err != nil {
			t.Fatalf("RemoveDownload: %v", err)
		}
		if _, err := os.Stat(payload); !os.IsNotExist(err) {
			t.Fatalf("payload should have been deleted, stat err = %v", err)
		}
		if !stub.sawMethod("d.erase") {
			t.Fatal("the torrent must still be erased after its data is removed")
		}
	})

	t.Run("an invisible path still erases the torrent", func(t *testing.T) {
		// A seedbox Bindery cannot see must not strand the download row.
		stub := newRtorrentStub(t, "1", "")
		hash := rtorrentTestHash
		if err := RemoveDownload(context.Background(), stub.client(t, 108), &models.Download{TorrentID: &hash}, true, ""); err != nil {
			t.Fatalf("RemoveDownload: %v", err)
		}
		if !stub.sawMethod("d.erase") {
			t.Fatal("d.erase must run even when the data could not be deleted")
		}
	})
}

func TestTestClient_Rtorrent(t *testing.T) {
	stub := newRtorrentStub(t, "1", "")
	if err := TestClient(context.Background(), stub.client(t, 109)); err != nil {
		t.Fatalf("TestClient: %v", err)
	}
	if !stub.sawMethod("d.multicall2") {
		t.Fatal("the Test action must also verify the download list is readable")
	}
}

func TestCheckCompletedPathVisibility_Rtorrent(t *testing.T) {
	root := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		if strings.Contains(string(body), "directory.default") {
			_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><string>/seedbox/downloads</string></value></param></params></methodResponse>`)
			return
		}
		_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><i8>0</i8></value></param></params></methodResponse>`)
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)

	t.Run("visible", func(t *testing.T) {
		client := &models.DownloadClient{ID: 110, Type: "rtorrent", Host: host, Port: port, PathRemap: "/seedbox/downloads:" + root}
		got := CheckCompletedPathVisibility(context.Background(), client, root, "", "")
		if got.Status != PathVisible {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("not visible", func(t *testing.T) {
		// The classic seedbox setup with no remap configured: connection fine,
		// nothing will ever import (#1182).
		client := &models.DownloadClient{ID: 111, Type: "rtorrent", Host: host, Port: port}
		got := CheckCompletedPathVisibility(context.Background(), client, root, "", "")
		if got.Status != PathNotVisible {
			t.Fatalf("got %+v", got)
		}
		if !strings.Contains(got.Message, "path remap") {
			t.Errorf("warning should name the fix, got %q", got.Message)
		}
	})
}

func TestRtorrentStatus(t *testing.T) {
	cases := map[string]struct {
		torrent rtorrent.Torrent
		want    string
		isError bool
	}{
		"active and incomplete": {rtorrent.Torrent{IsActive: true}, "downloading", false},
		"complete":              {rtorrent.Torrent{Complete: true, IsActive: true}, "seeding", false},
		"stopped":               {rtorrent.Torrent{}, "stopped", false},
		// d.message is rTorrent's error slot; it must classify as an error so the
		// queue view flags it the way it flags a Transmission errorString.
		"message":           {rtorrent.Torrent{Message: "Tracker: unregistered torrent"}, "error: Tracker: unregistered torrent", true},
		"blank message":     {rtorrent.Torrent{IsActive: true, Message: "   "}, "downloading", false},
		"complete but idle": {rtorrent.Torrent{Complete: true}, "seeding", false},
		// d.message only counts as an error while the torrent is incomplete.
		// rTorrent parks routine tracker chatter there on healthy, fully
		// downloaded torrents, and treating that as an error painted a seeding
		// row red and tripped LiveStatusIsError's "error" substring match. The
		// importer's poller and GetStalledIDs both already qualify on
		// !Complete; this is the third call site agreeing with them.
		"complete with tracker chatter": {
			rtorrent.Torrent{Complete: true, IsActive: true, Message: "Tracker: [Failure reason \"Torrent not registered with this tracker\"]"},
			"seeding", false,
		},
		"incomplete with a real failure": {
			rtorrent.Torrent{Message: "hash check failed"},
			"error: hash check failed", true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := RtorrentStatus(tc.torrent)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if LiveStatusIsError(got) != tc.isError {
				t.Fatalf("LiveStatusIsError(%q) = %v, want %v", got, !tc.isError, tc.isError)
			}
		})
	}
}

// resolveRtorrentDataPath is the only place Bindery deletes a tree at a path a
// remote service chose, so every guard gets an explicit negative case.
func TestResolveRtorrentDataPath(t *testing.T) {
	// Resolve the temp root up front: some platforms hand back a path that is
	// itself reached through a symlink (/tmp → /private/tmp), and the guard
	// under test deliberately refuses any symlinked component.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "downloads", "The Hobbit")
	if err := os.MkdirAll(payload, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("visible payload resolves", func(t *testing.T) {
		client := &models.DownloadClient{}
		got, reason := resolveRtorrentDataPath(client, payload, "")
		if got != payload {
			t.Fatalf("got %q (%s), want %q", got, reason, payload)
		}
	})

	t.Run("path remap is applied", func(t *testing.T) {
		// rTorrent writes to /seedbox/... ; Bindery mounts it under the temp root.
		client := &models.DownloadClient{PathRemap: "/seedbox:" + filepath.Join(root, "downloads")}
		got, reason := resolveRtorrentDataPath(client, "/seedbox/The Hobbit", "")
		if got != payload {
			t.Fatalf("got %q (%s), want %q", got, reason, payload)
		}
	})

	// An operator with one global BINDERY_DOWNLOAD_PATH_REMAP and no per-client
	// remap had working imports and a passing Test button, then a "not visible
	// to Bindery" refusal on remove-with-data alone, because this was the one
	// translation site that skipped the global fallback.
	t.Run("global remap is applied when the client has none", func(t *testing.T) {
		client := &models.DownloadClient{}
		got, reason := resolveRtorrentDataPath(client, "/seedbox/The Hobbit", "/seedbox:"+filepath.Join(root, "downloads"))
		if got != payload {
			t.Fatalf("got %q (%s), want %q", got, reason, payload)
		}
	})

	// Same precedence as remapClientPath and the importer: the client's own
	// remap wins, and the global remap is only consulted when it did not match.
	t.Run("client remap takes precedence over the global remap", func(t *testing.T) {
		client := &models.DownloadClient{PathRemap: "/seedbox:" + filepath.Join(root, "downloads")}
		got, reason := resolveRtorrentDataPath(client, "/seedbox/The Hobbit", "/seedbox:/somewhere/else")
		if got != payload {
			t.Fatalf("got %q (%s), want %q", got, reason, payload)
		}
	})

	// A symlinked *parent* passed the leaf Lstat and the depth guard, and
	// os.RemoveAll follows it — so a remap landing on a directory whose parent
	// points elsewhere would delete outside the download tree entirely.
	t.Run("symlinked parent directory is refused", func(t *testing.T) {
		outside := filepath.Join(root, "outside")
		if err := os.MkdirAll(filepath.Join(outside, "Book"), 0o755); err != nil {
			t.Fatal(err)
		}
		linkedParent := filepath.Join(root, "downloads", "via-link")
		if err := os.Symlink(outside, linkedParent); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		through := filepath.Join(linkedParent, "Book")
		if _, err := os.Lstat(through); err != nil {
			t.Fatalf("fixture setup: %v", err)
		}
		got, reason := resolveRtorrentDataPath(&models.DownloadClient{}, through, "")
		if got != "" {
			t.Fatalf("expected refusal for a symlinked parent, got %q", got)
		}
		if reason == "" {
			t.Fatal("a refusal must explain itself")
		}
	})

	t.Run("symlink is refused", func(t *testing.T) {
		link := filepath.Join(root, "downloads", "link")
		if err := os.Symlink(payload, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		client := &models.DownloadClient{}
		got, reason := resolveRtorrentDataPath(client, link, "")
		if got != "" {
			t.Fatalf("expected refusal, got %q", got)
		}
		if reason == "" {
			t.Fatal("a refusal must explain itself")
		}
	})

	negatives := map[string]struct {
		client      *models.DownloadClient
		basePath    string
		globalRemap string
	}{
		"empty base path": {&models.DownloadClient{}, "", ""},
		"whitespace only": {&models.DownloadClient{}, "   ", ""},
		"relative path":   {&models.DownloadClient{}, "downloads/Book", ""},
		// A remap that collapses the payload onto a mount root would delete
		// every other download on the next removal.
		"filesystem root":      {&models.DownloadClient{}, "/", ""},
		"top-level directory":  {&models.DownloadClient{}, "/downloads", ""},
		"remapped to one seg":  {&models.DownloadClient{PathRemap: "/seedbox/downloads/Book:/data"}, "/seedbox/downloads/Book", ""},
		"not visible locally":  {&models.DownloadClient{}, filepath.Join(root, "downloads", "missing"), ""},
		"parent not traversed": {&models.DownloadClient{}, filepath.Join(root, "nope", "Book"), ""},
		// The global remap must not rescue a path onto a mount root either.
		"global remap to one seg": {&models.DownloadClient{}, "/seedbox/downloads/Book", "/seedbox/downloads/Book:/data"},
	}
	for name, tc := range negatives {
		t.Run(name, func(t *testing.T) {
			got, reason := resolveRtorrentDataPath(tc.client, tc.basePath, tc.globalRemap)
			if got != "" {
				t.Fatalf("expected refusal for %q, got %q", tc.basePath, got)
			}
			if reason == "" {
				t.Fatal("a refusal must explain itself")
			}
		})
	}
}

// rTorrent must be recognised as a torrent client everywhere the protocol is
// derived, or a Torznab grab routed to it is rejected as a protocol mismatch.
func TestRtorrentIsTorrentClient(t *testing.T) {
	if !IsTorrentClient("rtorrent") {
		t.Fatal("rtorrent must be a torrent client")
	}
	if got := ProtocolForClient("rtorrent"); got != "torrent" {
		t.Fatalf("protocol: got %q, want torrent", got)
	}
	if IsNZBGetClient("rtorrent") {
		t.Fatal("rtorrent is not NZBGet")
	}
}

func TestRtorrentFor_IsCached(t *testing.T) {
	cache := NewClientCache()
	client := &models.DownloadClient{ID: 7, Type: "rtorrent", Host: "seedbox", Port: 8080, Username: "u", Password: "p"}

	first := cache.RtorrentFor(client)
	if first == nil {
		t.Fatal("expected a client")
	}
	if second := cache.RtorrentFor(client); second != first {
		t.Fatal("a repeated lookup with the same config must reuse the cached client")
	}
	if got := cache.ConstructorCount(); got != 1 {
		t.Fatalf("constructor count: got %d, want 1", got)
	}

	// A credential rotation must rebuild, or the poller keeps using the old one.
	client.Password = "rotated"
	if third := cache.RtorrentFor(client); third == first {
		t.Fatal("a changed password must invalidate the cached client")
	}
	if got := cache.ConstructorCount(); got != 2 {
		t.Fatalf("constructor count after rotation: got %d, want 2", got)
	}
}
