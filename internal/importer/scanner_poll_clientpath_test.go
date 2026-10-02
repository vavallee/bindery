package importer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/deluge"
	"github.com/vavallee/bindery/internal/downloader/rtorrent"
	"github.com/vavallee/bindery/internal/models"
)

// fearvector's setup from #2902: qBittorrent in Docker sees its downloads at
// /downloads, Bindery runs natively on Windows and sees the same folder at
// H:\Utorrent Downloads.
const issue2902Remap = `/downloads:H:\Utorrent Downloads`

// TestRemapClientJoin_PosixClientToWindowsHost checks the join the torrent
// pollers now share: the client's save path and the file name are joined in
// the client's (POSIX) namespace before the remap, so the POSIX source rule
// matches and the result is the Windows path the file really has.
//
// On Linux filepath.Join and path.Join agree, so this cannot show the old
// bug by itself; scanner_poll_windows_test.go runs the same resolution with
// real Windows path semantics.
func TestRemapClientJoin_PosixClientToWindowsHost(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	client := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", PathRemap: issue2902Remap}

	got := s.remapClientJoin(client, "/downloads/.Completed", "Mushoku Tensei/Vol 17.epub")
	want := `H:\Utorrent Downloads\.Completed\Mushoku Tensei\Vol 17.epub`
	if got != want {
		t.Fatalf("remapClientJoin = %q, want %q", got, want)
	}
}

// TestResolveTorrentFiles_PosixClientToWindowsRemap runs the file list
// resolver qBittorrent, Transmission, Deluge and rTorrent all use.
func TestResolveTorrentFiles_PosixClientToWindowsRemap(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	client := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", PathRemap: issue2902Remap}

	got := s.resolveTorrentFiles(client, "/downloads/.Completed", []torrentFile{
		{Name: "Mushoku Tensei/Vol 17.epub", Size: 1},
		{Name: "Mushoku Tensei/cover.jpg", Size: 1},
	})
	want := filepath.Clean(`H:\Utorrent Downloads\.Completed\Mushoku Tensei\Vol 17.epub`)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("resolveTorrentFiles = %q, want [%q]", got, want)
	}
}

// TestResolveTorrentFiles_RejectsBackslashTraversalAndDriveNames covers the
// name guard in both namespaces. A Windows client's save path is joined with
// `\` as a separator, so "..\" has to be caught on every host, and a drive
// path is as absolute as "/etc/passwd".
func TestResolveTorrentFiles_RejectsBackslashTraversalAndDriveNames(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	client := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", PathRemap: `C:\Downloads:/downloads`}
	got := s.resolveTorrentFiles(client, `C:\Downloads`, []torrentFile{
		{Name: `Series\..\..\escape.epub`},
		{Name: `C:\Windows\escape.epub`},
		{Name: `\escape.epub`},
		{Name: "Series/good.epub", Size: 1},
	})
	want := filepath.Clean("/downloads/Series/good.epub")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("resolveTorrentFiles = %q, want only %q", got, want)
	}
}

// deadRPC is a download client endpoint that fails every call, so the
// *ImportSources helpers fall back to a directory walk and only their download
// path is under test.
func deadRPC(t *testing.T) (string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname(), port
}

// TestDelugeAndRtorrentDownloadPath_PosixClientToWindowsRemap covers the two
// pollers that build the torrent's own folder from save path + name.
func TestDelugeAndRtorrentDownloadPath_PosixClientToWindowsRemap(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	host, port := deadRPC(t)
	want := `H:\Utorrent Downloads\.Completed\Mushoku Tensei`

	dc := &models.DownloadClient{Name: "deluge", Type: "deluge", PathRemap: issue2902Remap}
	got, _ := s.delugeImportSources(context.Background(), deluge.New(host, port, "", "", false), dc,
		deluge.TorrentStatus{Name: "Mushoku Tensei", Hash: "abc", DownloadLocation: "/downloads/.Completed"})
	if got != want {
		t.Errorf("deluge download path = %q, want %q", got, want)
	}

	rc := &models.DownloadClient{Name: "rtorrent", Type: "rtorrent", PathRemap: issue2902Remap}
	got, _ = s.rtorrentImportSources(context.Background(), rtorrent.New(host, port, "", "", "", false), rc,
		rtorrent.Torrent{Name: "Mushoku Tensei", Hash: "abc", Directory: "/downloads/.Completed"})
	if got != want {
		t.Errorf("rtorrent download path = %q, want %q", got, want)
	}
}
