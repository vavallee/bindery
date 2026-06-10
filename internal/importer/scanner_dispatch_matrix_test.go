package importer

// Per-download-client dispatch/completion integration matrix (issue #1019).
//
// Bug #1019: a Deluge download reached seeding but CheckDownloads had no
// Deluge case; the client fell through the switch's default into the SABnzbd
// poller, errored against the Deluge host at Debug level, and the download
// sat at "downloading" forever. Nothing exercised the polling pipeline per
// client type, so it shipped.
//
// Each leg here drives the REAL CheckDownloads dispatch (not the typed
// check* helpers, which the per-client scanner tests call directly) against
// an httptest server speaking just enough of that client's wire protocol —
// including its auth handshake — and asserts:
//
//	(a) routing — the configured type reached ITS typed poller: the stub
//	    records every request, so the test can require the client-specific
//	    endpoints (SAB mode=history, NZBGet JSON-RPC "history", Transmission
//	    RPC behind the X-Transmission-Session-Id 409 handshake, qBittorrent
//	    cookie login + /torrents/info, Deluge auth.login + JSON-RPC) and
//	    require that NOTHING ELSE was hit. The unknown-type leg asserts the
//	    #1019 safety net: a loud WARN, zero HTTP traffic, zero handoffs.
//	(b) completion — the single completed/seeding item reported by the stub
//	    advances the tracked download to StateCompleted.
//	(c) handoff — the importer boundary (tryImportInternal, intercepted via
//	    the Scanner.testImportHook seam) is called once, by the right typed
//	    poller, with the client-reported save path (and, for torrent
//	    clients, the authoritative per-torrent file list — issue #903).
//
// The matrix is mock-only (in-memory SQLite + httptest, no sleeps or poll
// timers — CheckDownloads is invoked directly), so it runs in normal CI.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader/nzbget"
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/models"
)

// importHandoff records one call across the importer boundary.
type importHandoff struct {
	guid       string
	path       string
	clientType string
	files      []string
}

// requestLog records every request a protocol stub receives so a leg can
// assert which endpoints were (and were not) exercised.
type requestLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *requestLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
}

func (l *requestLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

func (l *requestLog) contains(substr string) bool {
	for _, e := range l.all() {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

// recording wraps inner so every request is logged as
// "METHOD <request-uri> [rpc=<json-rpc method>]" before being handled. The
// body is teed back so inner still sees it; the rpc= suffix is added when the
// body is a JSON-RPC envelope (NZBGet, Transmission, Deluge).
func recording(rec *requestLog, inner http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entry := r.Method + " " + r.URL.RequestURI()
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var rpc struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(body, &rpc) == nil && rpc.Method != "" {
				entry += " rpc=" + rpc.Method
			}
		}
		rec.add(entry)
		inner.ServeHTTP(w, r)
	}
}

// dispatchFixture is the shared scaffolding for one matrix leg: an in-memory
// DB, a Scanner whose importer boundary is replaced by a recording hook, and
// the repos needed to seed a client + tracked download.
type dispatchFixture struct {
	ctx       context.Context
	scanner   *Scanner
	clients   *db.DownloadClientRepo
	downloads *db.DownloadRepo

	mu       sync.Mutex
	handoffs []importHandoff
}

func newDispatchFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	s := NewScanner(dlRepo, clientRepo, db.NewBookRepo(database), db.NewAuthorRepo(database),
		db.NewHistoryRepo(database), t.TempDir(), "", "", "", "")

	f := &dispatchFixture{
		ctx:       context.Background(),
		scanner:   s,
		clients:   clientRepo,
		downloads: dlRepo,
	}
	s.testImportHook = func(dl *models.Download, downloadPath, clientType string, explicitFiles []string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.handoffs = append(f.handoffs, importHandoff{
			guid:       dl.GUID,
			path:       downloadPath,
			clientType: clientType,
			files:      append([]string(nil), explicitFiles...),
		})
	}
	return f
}

func (f *dispatchFixture) createClient(t *testing.T, c *models.DownloadClient, srvURL string) *models.DownloadClient {
	t.Helper()
	c.Host, c.Port = scannerTestHostPort(t, srvURL)
	c.Enabled = true
	if err := f.clients.Create(f.ctx, c); err != nil {
		t.Fatalf("create client: %v", err)
	}
	return c
}

func (f *dispatchFixture) createDownload(t *testing.T, dl *models.Download) *models.Download {
	t.Helper()
	if err := f.downloads.Create(f.ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}
	return dl
}

func (f *dispatchFixture) allHandoffs() []importHandoff {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]importHandoff(nil), f.handoffs...)
}

// singleHandoff asserts exactly one importer-boundary call happened, by the
// expected typed poller, with the expected download path, and returns it.
func (f *dispatchFixture) singleHandoff(t *testing.T, wantClientType, wantPath string) importHandoff {
	t.Helper()
	handoffs := f.allHandoffs()
	if len(handoffs) != 1 {
		t.Fatalf("importer boundary called %d times, want exactly 1: %+v", len(handoffs), handoffs)
	}
	h := handoffs[0]
	if h.clientType != wantClientType {
		t.Errorf("import handoff came from the %q poller, want %q — wrong typed poller handled the client (#1019 regression)",
			h.clientType, wantClientType)
	}
	if h.path != wantPath {
		t.Errorf("import handoff path = %q, want %q", h.path, wantPath)
	}
	return h
}

func (f *dispatchFixture) assertStatus(t *testing.T, guid string, want models.DownloadState) {
	t.Helper()
	got, err := f.downloads.GetByGUID(f.ctx, guid)
	if err != nil {
		t.Fatalf("get download %q: %v", guid, err)
	}
	if got.Status != want {
		t.Errorf("download %q status = %q, want %q", guid, got.Status, want)
	}
}

// assertOnlyEndpoints fails if the stub saw any request whose path does not
// start with one of the allowed prefixes — i.e. some other client's poller
// touched this host.
func assertOnlyEndpoints(t *testing.T, rec *requestLog, allowed ...string) {
	t.Helper()
	for _, e := range rec.all() {
		parts := strings.SplitN(e, " ", 3)
		if len(parts) < 2 {
			t.Fatalf("malformed request log entry %q", e)
		}
		ok := false
		for _, prefix := range allowed {
			if strings.HasPrefix(parts[1], prefix) {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("stub received request outside this client's protocol surface: %q (allowed prefixes %v)", e, allowed)
		}
	}
}

// --- SABnzbd ---------------------------------------------------------------

func TestCheckDownloads_DispatchMatrix_SABnzbd(t *testing.T) {
	f := newDispatchFixture(t)
	const (
		nzoID       = "SABnzbd_nzo_matrix"
		downloadDir = "/data/usenet/complete/the-book"
	)

	rec := &requestLog{}
	srv := httptest.NewServer(recording(rec, sabHistoryHandler(t, []sabnzbd.HistorySlot{{
		NzoID:  nzoID,
		Name:   "the-book",
		Status: "Completed",
		Path:   downloadDir,
	}}, nil)))
	t.Cleanup(srv.Close)

	client := f.createClient(t, &models.DownloadClient{Name: "sab", Type: "sabnzbd", APIKey: "matrix-key"}, srv.URL)
	nzo := nzoID
	f.createDownload(t, &models.Download{
		GUID: "guid-matrix-sab", Title: "the-book", Status: models.StateDownloading,
		Protocol: "usenet", SABnzbdNzoID: &nzo, DownloadClientID: &client.ID,
	})

	f.scanner.CheckDownloads(f.ctx)

	f.singleHandoff(t, "sabnzbd", downloadDir)
	f.assertStatus(t, "guid-matrix-sab", models.StateCompleted)
	if !rec.contains("mode=history") {
		t.Errorf("SABnzbd stub never received the history poll; requests: %v", rec.all())
	}
	if !rec.contains("apikey=matrix-key") {
		t.Errorf("SABnzbd history poll did not carry the API key; requests: %v", rec.all())
	}
	assertOnlyEndpoints(t, rec, "/api")
}

// --- NZBGet ----------------------------------------------------------------

func TestCheckDownloads_DispatchMatrix_NZBGet(t *testing.T) {
	f := newDispatchFixture(t)
	const downloadDir = "/data/usenet/nzbget/the-book"

	rec := &requestLog{}
	srv := httptest.NewServer(recording(rec, nzbgetHandler(t, []nzbget.HistoryItem{{
		NZBID:   7,
		NZBName: "the-book",
		Status:  "SUCCESS/ALL",
		DestDir: downloadDir,
	}}, nil)))
	t.Cleanup(srv.Close)

	client := f.createClient(t, &models.DownloadClient{Name: "nzbget", Type: "nzbget"}, srv.URL)
	nzbID := "7" // NZBGet NZBIDs are stored in the SABnzbd NZO column.
	f.createDownload(t, &models.Download{
		GUID: "guid-matrix-nzbget", Title: "the-book", Status: models.StateDownloading,
		Protocol: "usenet", SABnzbdNzoID: &nzbID, DownloadClientID: &client.ID,
	})

	f.scanner.CheckDownloads(f.ctx)

	f.singleHandoff(t, "nzbget", downloadDir)
	f.assertStatus(t, "guid-matrix-nzbget", models.StateCompleted)
	if !rec.contains("rpc=history") {
		t.Errorf("NZBGet stub never received the JSON-RPC history call; requests: %v", rec.all())
	}
	assertOnlyEndpoints(t, rec, "/jsonrpc")
}

// --- Transmission ----------------------------------------------------------

const transmissionMatrixSessionID = "matrix-session-1019"

// transmissionMatrixHandler speaks the Transmission RPC protocol including
// the CSRF session-id handshake: any request without the expected
// X-Transmission-Session-Id header is answered 409 + the id, and the client
// must retry. conflicts counts those 409s so the test can assert the
// handshake actually ran.
func transmissionMatrixHandler(t *testing.T, conflicts *atomic.Int32, downloadDir string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != transmissionMatrixSessionID {
			conflicts.Add(1)
			w.Header().Set("X-Transmission-Session-Id", transmissionMatrixSessionID)
			w.WriteHeader(http.StatusConflict)
			return
		}
		var body struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Method != "torrent-get" {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{}})
			return
		}
		fields, _ := body.Arguments["fields"].([]any)
		for _, field := range fields {
			if field == "files" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"result": "success",
					"arguments": map[string]any{
						"torrents": []map[string]any{{
							"id":    42,
							"files": []map[string]any{{"name": "the-book.epub", "length": 12, "bytesCompleted": 12}},
						}},
					},
				})
				return
			}
		}
		// Listing call (GetTorrents): one seeding, fully-downloaded torrent.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": "success",
			"arguments": map[string]any{
				"torrents": []map[string]any{{
					"id":          42,
					"hashString":  "feedfacefeedfacefeedfacefeedfacefeedface",
					"name":        "the-book",
					"percentDone": 1.0,
					"status":      3, // seeding
					"downloadDir": downloadDir,
				}},
			},
		})
	}
}

func TestCheckDownloads_DispatchMatrix_Transmission(t *testing.T) {
	f := newDispatchFixture(t)
	const downloadDir = "/data/torrents/transmission"

	rec := &requestLog{}
	var conflicts atomic.Int32
	srv := httptest.NewServer(recording(rec, transmissionMatrixHandler(t, &conflicts, downloadDir)))
	t.Cleanup(srv.Close)

	client := f.createClient(t, &models.DownloadClient{Name: "transmission", Type: "transmission"}, srv.URL)
	torrentID := "42" // Transmission downloads are matched by numeric torrent id.
	f.createDownload(t, &models.Download{
		GUID: "guid-matrix-transmission", Title: "the-book", Status: models.StateDownloading,
		Protocol: "torrent", TorrentID: &torrentID, DownloadClientID: &client.ID,
	})

	f.scanner.CheckDownloads(f.ctx)

	h := f.singleHandoff(t, "transmission", downloadDir)
	f.assertStatus(t, "guid-matrix-transmission", models.StateCompleted)
	wantFiles := []string{filepath.Join(downloadDir, "the-book.epub")}
	if len(h.files) != 1 || h.files[0] != wantFiles[0] {
		t.Errorf("import handoff files = %v, want %v (issue #903 per-torrent file list)", h.files, wantFiles)
	}
	if conflicts.Load() == 0 {
		t.Error("Transmission session-id handshake never ran: no request was answered 409")
	}
	if !rec.contains("rpc=torrent-get") {
		t.Errorf("Transmission stub never received torrent-get; requests: %v", rec.all())
	}
	assertOnlyEndpoints(t, rec, "/transmission/rpc")
}

// --- qBittorrent -----------------------------------------------------------

// qbittorrentMatrixHandler speaks the qBittorrent WebUI API including
// cookie-based auth: /api/v2/auth/login validates the credentials and sets
// the SID cookie; every other endpoint requires it (403 otherwise), exactly
// like a real qBittorrent.
func qbittorrentMatrixHandler(t *testing.T, torrent map[string]any, files []map[string]any) http.HandlerFunc {
	t.Helper()
	const sid = "matrix-sid-1019"
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			if err := r.ParseForm(); err != nil ||
				r.PostForm.Get("username") != "admin" || r.PostForm.Get("password") != "secret" {
				_, _ = io.WriteString(w, "Fails.")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: sid, Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
			return
		}
		cookie, err := r.Cookie("SID")
		if err != nil || cookie.Value != sid {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{torrent})
		case "/api/v2/torrents/files":
			_ = json.NewEncoder(w).Encode(files)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestCheckDownloads_DispatchMatrix_QBittorrent(t *testing.T) {
	f := newDispatchFixture(t)
	const (
		hash        = "cafebabecafebabecafebabecafebabecafebabe"
		savePath    = "/data/torrents/qbit"
		contentPath = savePath + "/the-book"
	)

	rec := &requestLog{}
	srv := httptest.NewServer(recording(rec, qbittorrentMatrixHandler(t,
		map[string]any{
			"hash":         hash,
			"name":         "the-book",
			"progress":     1.0,
			"state":        "uploading", // seeding
			"category":     "books",
			"size":         12,
			"amount_left":  0,
			"save_path":    savePath,
			"content_path": contentPath,
		},
		[]map[string]any{{"name": "the-book/the-book.epub", "size": 12}},
	)))
	t.Cleanup(srv.Close)

	client := f.createClient(t, &models.DownloadClient{
		Name: "qbit", Type: "qbittorrent",
		Username: "admin", Password: "secret", Category: "books",
	}, srv.URL)
	torrentHash := hash
	f.createDownload(t, &models.Download{
		GUID: "guid-matrix-qbit", Title: "the-book", Status: models.StateDownloading,
		Protocol: "torrent", TorrentID: &torrentHash, DownloadClientID: &client.ID,
	})

	f.scanner.CheckDownloads(f.ctx)

	h := f.singleHandoff(t, "qbittorrent", contentPath)
	f.assertStatus(t, "guid-matrix-qbit", models.StateCompleted)
	wantFile := filepath.Join(savePath, "the-book", "the-book.epub")
	if len(h.files) != 1 || h.files[0] != wantFile {
		t.Errorf("import handoff files = %v, want [%s] (issue #903 per-torrent file list)", h.files, wantFile)
	}
	if !rec.contains("POST /api/v2/auth/login") {
		t.Errorf("qBittorrent cookie login never happened; requests: %v", rec.all())
	}
	if !rec.contains("GET /api/v2/torrents/info") {
		t.Errorf("qBittorrent stub never received the torrents/info poll; requests: %v", rec.all())
	}
	assertOnlyEndpoints(t, rec, "/api/v2/")
}

// --- Deluge ----------------------------------------------------------------

func TestCheckDownloads_DispatchMatrix_Deluge(t *testing.T) {
	f := newDispatchFixture(t)
	const (
		hash     = "deadbeef1234567890deadbeef1234567890dead"
		savePath = "/data/torrents/deluge"
	)

	rec := &requestLog{}
	// Reuses the Deluge JSON-RPC fixture from scanner_deluge_test.go: it
	// reports one seeding torrent named "testbook" with file "testbook.epub"
	// under savePath, and answers the auth.login handshake.
	srv := httptest.NewServer(recording(rec, delugeHandler(t, hash, savePath)))
	t.Cleanup(srv.Close)

	client := f.createClient(t, &models.DownloadClient{
		Name: "deluge", Type: "deluge", Password: "deluge",
	}, srv.URL)
	torrentHash := hash
	f.createDownload(t, &models.Download{
		GUID: "guid-matrix-deluge", Title: "testbook", Status: models.StateDownloading,
		Protocol: "torrent", TorrentID: &torrentHash, DownloadClientID: &client.ID,
	})

	f.scanner.CheckDownloads(f.ctx)

	h := f.singleHandoff(t, "deluge", filepath.Join(savePath, "testbook"))
	f.assertStatus(t, "guid-matrix-deluge", models.StateCompleted)
	wantFile := filepath.Join(savePath, "testbook.epub")
	if len(h.files) != 1 || h.files[0] != wantFile {
		t.Errorf("import handoff files = %v, want [%s] (issue #903 per-torrent file list)", h.files, wantFile)
	}
	if !rec.contains("rpc=auth.login") {
		t.Errorf("Deluge auth handshake never happened; requests: %v", rec.all())
	}
	if !rec.contains("rpc=core.get_torrents_status") {
		t.Errorf("Deluge stub never received the status poll; requests: %v", rec.all())
	}
	assertOnlyEndpoints(t, rec, "/json")
}

// --- Unknown client type (the #1019 safety net) ------------------------------

// TestCheckDownloads_DispatchMatrix_UnknownType is the direct regression test
// for the footgun that hid #1019: before the fix, an unrecognised client type
// fell through CheckDownloads' default branch into the SABnzbd poller, which
// hit SAB endpoints on the foreign host, errored at Debug, and left every
// download stuck. Now an unknown type must produce a loud WARN and touch
// nothing: no HTTP traffic, no import handoff, no state change.
func TestCheckDownloads_DispatchMatrix_UnknownType(t *testing.T) {
	f := newDispatchFixture(t)

	rec := &requestLog{}
	srv := httptest.NewServer(recording(rec, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})))
	t.Cleanup(srv.Close)

	client := f.createClient(t, &models.DownloadClient{Name: "mystery", Type: "rtorrent"}, srv.URL)
	torrentHash := "feedfeedfeedfeedfeedfeedfeedfeedfeedfeed"
	f.createDownload(t, &models.Download{
		GUID: "guid-matrix-unknown", Title: "the-book", Status: models.StateDownloading,
		Protocol: "torrent", TorrentID: &torrentHash, DownloadClientID: &client.ID,
	})

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f.scanner.CheckDownloads(f.ctx)

	logs := logBuf.String()
	if !strings.Contains(logs, "unsupported download client type") || !strings.Contains(logs, "level=WARN") {
		t.Errorf("expected a WARN about the unsupported client type, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "type=rtorrent") {
		t.Errorf("safety-net warning should name the offending client type, got logs:\n%s", logs)
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("unknown client type must not fall through to another client's poller, but the host received: %v", got)
	}
	if handoffs := f.allHandoffs(); len(handoffs) != 0 {
		t.Errorf("unknown client type must never reach the importer boundary, got %+v", handoffs)
	}
	// The download is untouched — visible (stuck at downloading) but never
	// silently mis-polled. The WARN is the operator's signal.
	f.assertStatus(t, "guid-matrix-unknown", models.StateDownloading)
}
