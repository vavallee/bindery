package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/deluge"
	"github.com/vavallee/bindery/internal/downloader/rtorrent"
	"github.com/vavallee/bindery/internal/models"
)

// These run only on a Windows Bindery, where path/filepath uses `\` and the
// bug in #2902 is real: qBittorrent in Docker reports POSIX paths, and joining
// them with filepath.Join before the remap produced "\downloads\..." that the
// POSIX remap rule never matched, so every file was dropped and the import
// failed with "no book files found".

// windowsDockerFixture lays out fearvector's download folder under a temp dir
// standing in for H:\Utorrent Downloads, and returns the remap a user would
// write for it.
func windowsDockerFixture(t *testing.T) (hostRoot, remap, book string) {
	t.Helper()
	hostRoot = t.TempDir()
	dir := filepath.Join(hostRoot, ".Completed", "Mushoku Tensei")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	book = filepath.Join(dir, "Vol 17.epub")
	if err := os.WriteFile(book, []byte("epub"), 0o644); err != nil {
		t.Fatal(err)
	}
	return hostRoot, "/downloads:" + hostRoot, book
}

func TestWindowsResolveTorrentFiles_DockerClientPOSIXRemap(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	_, remap, book := windowsDockerFixture(t)
	client := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", PathRemap: remap}

	got := s.resolveTorrentFiles(client, "/downloads/.Completed", []torrentFile{
		{Name: "Mushoku Tensei/Vol 17.epub", Size: 4},
	})
	if len(got) != 1 || got[0] != book {
		t.Fatalf("resolveTorrentFiles = %q, want [%q]", got, book)
	}
	if kept := filterImportableFiles(got); len(kept) != 1 {
		t.Fatalf("filterImportableFiles dropped the book: %q", got)
	}
}

// The #2878 content path fallback, for a qBittorrent emulator whose file
// names omit the root folder, must work from the same Docker client paths.
func TestWindowsResolveTorrentFiles_ContentFallbackDockerClient(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	_, remap, book := windowsDockerFixture(t)
	client := &models.DownloadClient{Name: "rdt", Type: "qbittorrent", PathRemap: remap}

	got := s.resolveTorrentFilesWithContentFallback(client, "/downloads/.Completed",
		"/downloads/.Completed/Mushoku Tensei", []torrentFile{{Name: "Vol 17.epub", Size: 4}})
	if len(got) != 1 || got[0] != book {
		t.Fatalf("content fallback = %q, want [%q]", got, book)
	}
}

func TestWindowsDelugeAndRtorrentDownloadPath_DockerClient(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	_, remap, book := windowsDockerFixture(t)
	want := filepath.Dir(book)
	host, port := deadRPC(t)

	dc := &models.DownloadClient{Name: "deluge", Type: "deluge", PathRemap: remap}
	got, _ := s.delugeImportSources(context.Background(), deluge.New(host, port, "", "", false), dc,
		deluge.TorrentStatus{Name: "Mushoku Tensei", Hash: "abc", DownloadLocation: "/downloads/.Completed"})
	if filepath.Clean(got) != want {
		t.Errorf("deluge download path = %q, want %q", got, want)
	}

	rc := &models.DownloadClient{Name: "rtorrent", Type: "rtorrent", PathRemap: remap}
	got, _ = s.rtorrentImportSources(context.Background(), rtorrent.New(host, port, "", "", "", false), rc,
		rtorrent.Torrent{Name: "Mushoku Tensei", Hash: "abc", Directory: "/downloads/.Completed"})
	if filepath.Clean(got) != want {
		t.Errorf("rtorrent download path = %q, want %q", got, want)
	}
}

// A client on the same Windows machine reports Windows paths and needs no
// remap; that worked before #2902 and must keep working.
func TestWindowsResolveTorrentFiles_NativeWindowsClient(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	hostRoot, _, book := windowsDockerFixture(t)
	client := &models.DownloadClient{Name: "qbit", Type: "qbittorrent"}

	got := s.resolveTorrentFiles(client, filepath.Join(hostRoot, ".Completed"), []torrentFile{
		{Name: "Mushoku Tensei/Vol 17.epub", Size: 4},
	})
	if len(got) != 1 || got[0] != book {
		t.Fatalf("resolveTorrentFiles = %q, want [%q]", got, book)
	}
}
