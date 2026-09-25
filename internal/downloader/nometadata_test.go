package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestGetStalledTorrents_Transmission_NoMetadata is the reporter's torrent
// (#2709): Transmission has held it for weeks with no file list, no total size
// and no connected peers, and errorString is empty because Transmission does
// not consider any of that an error. It must come back as StallNoMetadata,
// while a healthy torrent that is simply downloading must not appear at all.
func TestGetStalledTorrents_Transmission_NoMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"arguments": map[string]any{
				"torrents": []map[string]any{
					// The reported shape.
					{
						"id": 7, "status": 0, "errorString": "",
						"totalSize": 0, "percentDone": 0,
						"metadataPercentComplete": 0, "peersConnected": 0,
					},
					// The same shape, but Transmission is still actively
					// trying (status 4). Just as dead.
					{
						"id": 8, "status": 4, "errorString": "",
						"totalSize": 0, "percentDone": 0,
						"metadataPercentComplete": 0, "peersConnected": 3,
					},
					// Healthy: metadata resolved, downloading.
					{
						"id": 9, "status": 4, "errorString": "",
						"totalSize": 8192, "percentDone": 0.25,
						"metadataPercentComplete": 1, "peersConnected": 12,
					},
					// Healthy: metadata resolved, nothing downloaded yet.
					{
						"id": 10, "status": 4, "errorString": "",
						"totalSize": 8192, "percentDone": 0,
						"metadataPercentComplete": 1, "peersConnected": 1,
					},
					// A real error still reports as the client's own signal.
					{
						"id": 11, "status": 0, "errorString": "tracker error",
						"totalSize": 8192, "percentDone": 0.5,
						"metadataPercentComplete": 1,
					},
				},
			},
			"result": "success",
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}

	report, err := GetStalledTorrents(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if !report.NoMetadata["7"] {
		t.Error("stopped magnet with no metadata: want a NoMetadata entry")
	}
	if !report.NoMetadata["8"] {
		t.Error("downloading magnet with no metadata: want a NoMetadata entry")
	}
	if report.NoMetadata["9"] || report.ClientReported["9"] {
		t.Error("a healthy downloading torrent must not be reported as stalled")
	}
	if report.NoMetadata["10"] || report.ClientReported["10"] {
		t.Error("a torrent with metadata but no progress yet must not be reported as stalled")
	}
	if !report.ClientReported["11"] {
		t.Error("errored torrent: want a ClientReported entry")
	}
	if report.NoMetadata["11"] {
		t.Error("an errored torrent must not also be reported as missing metadata")
	}
	// Two of the five have finished nothing but are not stuck; all five are
	// incomplete, so 2 of 5 is under the outage threshold and these are
	// individually actionable.
	if report.Incomplete != 5 {
		t.Errorf("incomplete count: want 5, got %d", report.Incomplete)
	}
	if report.LooksLikeClientOutage() {
		t.Error("two dead magnets out of five is a release problem, not a client outage")
	}
}

// TestGetStalledTorrents_Transmission_NoMetadataFieldsRequested guards the RPC
// contract: totalSize and metadataPercentComplete have to be asked for, or
// Transmission never sends them and every torrent decodes as "no metadata".
func TestGetStalledTorrents_Transmission_NoMetadataFieldsRequested(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"arguments": map[string]any{"torrents": []map[string]any{}},
			"result":    "success",
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}
	if _, err := GetStalledTorrents(context.Background(), client); err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	for _, field := range []string{"totalSize", "percentDone", "metadataPercentComplete"} {
		if !strings.Contains(body, `"`+field+`"`) {
			t.Errorf("torrent-get must request %q, request was: %s", field, body)
		}
	}
}

// TestGetStalledTorrents_QBittorrent_MetaDL covers qBittorrent's version of the
// same torrent: it parks a magnet awaiting metadata in metaDL, which is not
// stalledDL and so was never reported.
func TestGetStalledTorrents_QBittorrent_MetaDL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"hash": "AAA111", "state": "metaDL", "size": 0, "progress": 0},
				{"hash": "BBB222", "state": "forcedMetaDL", "size": 0, "progress": 0},
				{"hash": "CCC333", "state": "downloading", "size": 4096, "progress": 0.5},
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p",
	}
	report, err := GetStalledTorrents(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if !report.NoMetadata["aaa111"] {
		t.Error("metaDL: want a NoMetadata entry")
	}
	if !report.NoMetadata["bbb222"] {
		t.Error("forcedMetaDL: want a NoMetadata entry")
	}
	if report.NoMetadata["ccc333"] || report.ClientReported["ccc333"] {
		t.Error("a healthy downloading torrent must not be reported as stalled")
	}
}

// TestGetStalledTorrents_Deluge_NoMetadata covers Deluge, which folds
// libtorrent's downloading_metadata into the plain Downloading state, so the
// zero total_size is the only thing that tells the two apart.
func TestGetStalledTorrents_Deluge_NoMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var result any
		switch req.Method {
		case "auth.login", "web.connected":
			result = true
		case "core.get_torrents_status":
			result = map[string]any{
				"aaa111": map[string]any{
					"hash": "aaa111", "state": "Downloading",
					"progress": 0.0, "total_size": 0, "total_done": 0,
				},
				"ccc333": map[string]any{
					"hash": "ccc333", "state": "Downloading",
					"progress": 25.0, "total_size": 4000, "total_done": 1000,
				},
			}
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": req.ID})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "deluge", Host: host, Port: port, Password: "pw"}
	report, err := GetStalledTorrents(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if !report.NoMetadata["aaa111"] {
		t.Error("magnet with no metadata: want a NoMetadata entry")
	}
	if report.NoMetadata["ccc333"] || report.ClientReported["ccc333"] {
		t.Error("a healthy downloading torrent must not be reported as stalled")
	}
}

// TestGetStalledTorrents_Rtorrent_NoMetadata covers rTorrent's "<hash>.meta"
// placeholder, which reports a zero d.size_bytes and no message at all.
func TestGetStalledTorrents_Rtorrent_NoMetadata(t *testing.T) {
	stub := newRtorrentSizeStub(t, 0, "0")
	report, err := GetStalledTorrents(context.Background(), stub.client(t, 210))
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if !report.NoMetadata[rtorrentTestHash] {
		t.Error("magnet placeholder: want a NoMetadata entry")
	}

	healthy := newRtorrentSizeStub(t, 1000, "0")
	report, err = GetStalledTorrents(context.Background(), healthy.client(t, 211))
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if len(report.ClientReported)+len(report.NoMetadata) != 0 {
		t.Errorf("a healthy downloading torrent must not be reported as stalled, got %+v", report)
	}
}

// newRtorrentSizeStub is newRtorrentStub with the reported d.size_bytes under
// the test's control, which is what separates a magnet placeholder from a
// torrent whose metadata has arrived.
func newRtorrentSizeStub(t *testing.T, sizeBytes int64, complete string) *rtorrentStub {
	t.Helper()
	s := &rtorrentStub{complete: complete}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		if !strings.Contains(string(body), "d.multicall2") {
			_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><i8>0</i8></value></param></params></methodResponse>`)
			return
		}
		_, _ = io.WriteString(w, fmt.Sprintf(`<?xml version="1.0"?><methodResponse><params><param><value><array><data>
<value><array><data>
<value><string>the-book</string></value>
<value><string>%s</string></value>
<value><string></string></value>
<value><string>/seedbox/downloads</string></value>
<value><string>books</string></value>
<value><i8>%d</i8></value>
<value><i8>0</i8></value>
<value><i8>0</i8></value>
<value><i8>%s</i8></value>
<value><i8>1</i8></value>
<value><i8>1</i8></value>
<value><string></string></value>
</data></array></value>
</data></array></value></param></params></methodResponse>`, strings.ToUpper(rtorrentTestHash), sizeBytes, complete))
	}))
	t.Cleanup(s.Close)
	return s
}

// TestStallReport_LooksLikeClientOutage pins the batch guard's two halves: the
// share, and the floor that stops the share from firing on a tiny queue.
func TestStallReport_LooksLikeClientOutage(t *testing.T) {
	report := func(noMeta, incomplete int) StallReport {
		r := StallReport{NoMetadata: map[string]bool{}, Incomplete: incomplete}
		for i := 0; i < noMeta; i++ {
			r.NoMetadata[string(rune('a'+i))] = true
		}
		return r
	}
	cases := []struct {
		name       string
		noMeta     int
		incomplete int
		want       bool
	}{
		{"nothing stuck", 0, 10, false},
		{"one dead magnet in a healthy queue", 1, 10, false},
		{"one dead magnet and nothing else in flight", 1, 1, false},
		{"two dead magnets and nothing else, under the floor", 2, 2, false},
		{"three dead magnets, all of the queue", 3, 3, true},
		{"three dead magnets, exactly half, not above the share", 3, 6, false},
		{"four dead magnets of seven, above the share", 4, 7, true},
		{"three dead of ten, above the floor but well under the share", 3, 10, false},
		{"no denominator reported", 5, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := report(tc.noMeta, tc.incomplete).LooksLikeClientOutage(); got != tc.want {
				t.Errorf("LooksLikeClientOutage() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestStallKind_ZeroValueIsUnusable pins that the zero value cannot be acted on
// by accident: its reason is obviously a bug report, not a plausible sentence
// to show a user, and it does not blocklist.
func TestStallKind_ZeroValueIsUnusable(t *testing.T) {
	var k StallKind
	if k != StallNone {
		t.Fatalf("the zero StallKind must be StallNone, got %v", k)
	}
	if !strings.HasPrefix(k.Reason(), "BUG:") {
		t.Errorf("StallNone reason must not read like a real stall, got %q", k.Reason())
	}
	if k.Blocklists() {
		t.Error("StallNone must never blocklist")
	}
	if StallNoMetadata.Blocklists() {
		t.Error("a no-metadata stall must not blocklist: it says nothing about the release")
	}
	if !StallClientReported.Blocklists() {
		t.Error("a client-reported stall must still blocklist, as it did before #2709")
	}
}
