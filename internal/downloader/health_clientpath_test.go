package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestRemapHint_ClientNamespace checks that the remap suggestion takes the
// parent of the save path in the client's namespace (#2902). A Docker
// qBittorrent's POSIX save path must come back with forward slashes so the
// suggestion is a source rule that matches, and a Windows client's save path
// must have its parent taken with Windows rules even on a Linux Bindery.
func TestRemapHint_ClientNamespace(t *testing.T) {
	cases := []struct{ save, expected, want string }{
		{"/torrents/complete/library", "/downloads", "/torrents/complete:" + filepath.Clean("/downloads")},
		{"/torrents", "/downloads", "/torrents:" + filepath.Clean("/downloads")},
		{"/downloads/.Completed/books/", filepath.Clean(`H:\Utorrent Downloads`), "/downloads/.Completed:" + filepath.Clean(`H:\Utorrent Downloads`)},
		{`C:\Torrents\books`, "/downloads", `C:\Torrents:` + filepath.Clean("/downloads")},
		{`C:\Torrents`, "/downloads", `C:\Torrents:` + filepath.Clean("/downloads")},
	}
	for _, c := range cases {
		if got := remapHint(c.save, c.expected); got != c.want {
			t.Errorf("remapHint(%q, %q) = %q, want %q", c.save, c.expected, got, c.want)
		}
	}
}

func TestPathIsAtOrUnderOn(t *testing.T) {
	cases := []struct {
		goos, candidate, base string
		want                  bool
	}{
		{"linux", "/data/downloads/x", "/data/downloads", true},
		{"linux", "/data/downloads", "/data/downloads", true},
		{"linux", "/data/downloads-extra", "/data/downloads", false},
		{"linux", "/Data/Downloads/x", "/data/downloads", false},
		{"windows", `H:\Utorrent Downloads\.Completed\x`, `h:\utorrent downloads`, true},
		{"windows", `H:\Utorrent Downloads`, `h:\UTORRENT DOWNLOADS`, true},
		{"windows", "H:/Utorrent Downloads/x", `H:\Utorrent Downloads`, true},
		{"windows", `H:\Utorrent Downloads-old\x`, `H:\Utorrent Downloads`, false},
		{"windows", `G:\Utorrent Downloads\x`, `H:\Utorrent Downloads`, false},
	}
	for _, c := range cases {
		if got := pathIsAtOrUnderOn(c.goos, c.candidate, c.base); got != c.want {
			t.Errorf("pathIsAtOrUnderOn(%q, %q, %q) = %v, want %v", c.goos, c.candidate, c.base, got, c.want)
		}
	}
}

// TestQbittorrentCategoryPath_UnsetDownloadDir covers the Windows binary,
// which has no default BINDERY_DOWNLOAD_DIR (#2902). With nothing configured
// the health check no longer errors out; it checks that the remapped save
// path exists on this host.
func TestQbittorrentCategoryPath_UnsetDownloadDir(t *testing.T) {
	host := t.TempDir()
	if err := os.MkdirAll(filepath.Join(host, ".Completed"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, savePath, wantStatus, wantText string
	}{
		{"save path exists after remap", "/downloads/.Completed", HealthOK, "saves to a folder Bindery can read"},
		{"save path missing after remap", "/downloads/.Missing", HealthError, "does not exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = w.Write([]byte("Ok."))
				case "/api/v2/torrents/categories":
					_, _ = w.Write([]byte(`{"books":{"name":"books","savePath":"` + tc.savePath + `"}}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			h, port := serverHostPort(t, srv.URL)
			client := &models.DownloadClient{
				Type: "qbittorrent", Host: h, Port: port, Username: "u", Password: "p",
				Category: "books", PathRemap: "/downloads:" + host,
			}
			got := CheckDownloadClientHealth(context.Background(), client, "", "", "")
			if got.Status != tc.wantStatus || !strings.Contains(got.Message, tc.wantText) {
				t.Fatalf("got %q %q, want %q containing %q", got.Status, got.Message, tc.wantStatus, tc.wantText)
			}
		})
	}
}
