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

func TestCheckDownloadClientHealth_QBittorrentCategoryPath(t *testing.T) {
	// Build real on-disk paths so the post-pathIsAtOrUnder stat() check
	// passes on the happy paths. Cases that should fail before stat (missing
	// category, empty save path, pathIsAtOrUnder mismatch) are unaffected.
	tmp := t.TempDir()
	expected := filepath.Join(tmp, "books", "downloads")
	if err := os.MkdirAll(filepath.Join(expected, "books"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(expected, "Torrents", "books"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		categories string
		pathRemap  string
		wantStatus string
		wantText   string
	}{
		{
			name:       "remapped category path matches",
			categories: `{"books":{"name":"books","savePath":"/media/downloads/books"}}`,
			pathRemap:  "/media/downloads:" + expected,
			wantStatus: HealthOK,
		},
		{
			name:       "qbit v5 boolean download_path still remaps category path",
			categories: `{"books":{"download_path":false,"name":"books","savePath":"/media/books/downloads/books"}}`,
			pathRemap:  "/media/books/downloads:" + expected,
			wantStatus: HealthOK,
		},
		{
			name:       "missing category",
			categories: `{}`,
			wantStatus: HealthError,
			wantText:   "was not found",
		},
		{
			name:       "empty category path reports default",
			categories: `{"books":{"name":"books","savePath":""}}`,
			wantStatus: HealthError,
			wantText:   "qBittorrent default is",
		},
		{
			name:       "mismatched category path",
			categories: `{"books":{"name":"books","savePath":"/media/other"}}`,
			wantStatus: HealthError,
			wantText:   `expected a path at or under`,
		},
		{
			name:       "category path is a subdirectory of download dir",
			categories: `{"books":{"name":"books","savePath":"/media/downloads/Torrents/books"}}`,
			pathRemap:  "/media/downloads:" + expected,
			wantStatus: HealthOK,
		},
		{
			name:       "category path under sibling dir is still rejected",
			categories: `{"books":{"name":"books","savePath":"/media/downloads-extra/books"}}`,
			pathRemap:  "/media/downloads:" + expected,
			wantStatus: HealthError,
			wantText:   `expected a path at or under`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = w.Write([]byte("Ok."))
				case "/api/v2/torrents/categories":
					_, _ = w.Write([]byte(tc.categories))
				case "/api/v2/app/defaultSavePath":
					_, _ = w.Write([]byte("/media/default"))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			host, port := serverHostPort(t, srv.URL)
			client := &models.DownloadClient{
				Type:      "qbittorrent",
				Host:      host,
				Port:      port,
				Username:  "u",
				Password:  "p",
				Category:  "books",
				PathRemap: tc.pathRemap,
			}
			got := CheckDownloadClientHealth(context.Background(), client, expected, "")
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q; message=%s", got.Status, tc.wantStatus, got.Message)
			}
			if tc.wantText != "" && !strings.Contains(got.Message, tc.wantText) {
				t.Fatalf("message %q does not contain %q", got.Message, tc.wantText)
			}
		})
	}
}

func TestExpectedDownloadDirForClient_AudiobookMediaType(t *testing.T) {
	client := &models.DownloadClient{Category: "books", CategoryAudiobook: "audiobooks"}
	if got := ExpectedDownloadDirForClient(client, models.MediaTypeAudiobook, "/books/downloads", "/books/audio-downloads"); got != "/books/audio-downloads" {
		t.Fatalf("audiobook mediaType: expected audiobook download dir, got %q", got)
	}
	if got := ExpectedDownloadDirForClient(client, models.MediaTypeEbook, "/books/downloads", "/books/audio-downloads"); got != "/books/downloads" {
		t.Fatalf("ebook mediaType: expected ebook download dir, got %q", got)
	}
}

// TestCheckDownloadClientHealth_QBittorrentAudiobookCategory exercises the
// per-media-type validation added in #700: when CategoryAudiobook is set, the
// health check must validate the audiobook category's save path against the
// audiobook download dir as well, and only return OK when both pass. Uses
// real on-disk t.TempDir paths so the post-pathIsAtOrUnder stat() check
// (#800 case-mismatch follow-up) doesn't fire on the happy paths.
func TestCheckDownloadClientHealth_QBittorrentAudiobookCategory(t *testing.T) {
	tmp := t.TempDir()
	ebookDir := filepath.Join(tmp, "books", "downloads")
	audioDir := filepath.Join(tmp, "books", "audio-downloads")
	if err := os.MkdirAll(filepath.Join(ebookDir, "books"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(audioDir, "audiobooks"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		categories string
		wantStatus string
		wantText   string
	}{
		{
			name:       "both categories valid",
			categories: `{"books":{"name":"books","savePath":"/media/downloads/books"},"audiobooks":{"name":"audiobooks","savePath":"/media/audio-downloads/audiobooks"}}`,
			wantStatus: HealthOK,
			wantText:   "audiobooks",
		},
		{
			name:       "audiobook category missing",
			categories: `{"books":{"name":"books","savePath":"/media/downloads/books"}}`,
			wantStatus: HealthError,
			wantText:   `qBittorrent category "audiobooks" was not found`,
		},
		{
			name:       "audiobook category points outside audiobook dir",
			categories: `{"books":{"name":"books","savePath":"/media/downloads/books"},"audiobooks":{"name":"audiobooks","savePath":"/media/downloads/books"}}`,
			wantStatus: HealthError,
			wantText:   `expected a path at or under`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = w.Write([]byte("Ok."))
				case "/api/v2/torrents/categories":
					_, _ = w.Write([]byte(tc.categories))
				case "/api/v2/app/defaultSavePath":
					_, _ = w.Write([]byte("/media/default"))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			host, port := serverHostPort(t, srv.URL)
			client := &models.DownloadClient{
				Type:              "qbittorrent",
				Host:              host,
				Port:              port,
				Username:          "u",
				Password:          "p",
				Category:          "books",
				CategoryAudiobook: "audiobooks",
				PathRemap:         "/media/downloads:" + ebookDir + ",/media/audio-downloads:" + audioDir,
			}
			got := CheckDownloadClientHealth(context.Background(), client, ebookDir, audioDir)
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q; message=%s", got.Status, tc.wantStatus, got.Message)
			}
			if tc.wantText != "" && !strings.Contains(got.Message, tc.wantText) {
				t.Fatalf("message %q does not contain %q", got.Message, tc.wantText)
			}
		})
	}
}

// TestQbittorrentCategoryPath_DetectsCaseMismatch covers the PixieApples
// follow-up to #800: when the path remap is textually correct (pathIsAtOrUnder
// passes against BINDERY_DOWNLOAD_DIR) but the resolved path does not exist on
// Bindery's filesystem because of case differences — common in WSL + Docker on
// Windows — the health-check must surface the divergent segment by name
// rather than silently reporting OK while later imports fail to find files.
func TestQbittorrentCategoryPath_DetectsCaseMismatch(t *testing.T) {
	tmp := t.TempDir()
	// Skip on case-insensitive filesystems (macOS default, Windows) since
	// the detection is a no-op when the OS itself smooths case over.
	probe := filepath.Join(tmp, "casetest")
	if err := os.MkdirAll(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "CASETEST")); err == nil {
		t.Skip("filesystem is case-insensitive; case-mismatch detection is a no-op here")
	}

	// Real on-disk path uses capital D; the user's configuration in qBit and
	// BINDERY_DOWNLOAD_DIR both use lowercase d. pathIsAtOrUnder passes
	// (strings agree), but the path does not exist on disk.
	realDir := filepath.Join(tmp, "Downloads", "books")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configuredDir := filepath.Join(tmp, "downloads")       // BINDERY_DOWNLOAD_DIR (wrong case)
	qbSavePath := filepath.Join(tmp, "downloads", "books") // qBit reports lowercase

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/categories":
			_, _ = w.Write([]byte(`{"books":{"name":"books","savePath":"` + qbSavePath + `"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p",
		Category: "books",
		// No PathRemap — qbSavePath already sits under configuredDir
		// textually, so pathIsAtOrUnder passes and the stat check fires.
	}
	got := CheckDownloadClientHealth(context.Background(), client, configuredDir, "")
	if got.Status != HealthError {
		t.Fatalf("status = %q, want %q; message=%s", got.Status, HealthError, got.Message)
	}
	for _, want := range []string{"does not exist", "case-sensitive", realDir, "Downloads"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message missing %q\nfull: %s", want, got.Message)
		}
	}
}

// TestQbittorrentCategoryPath_MismatchMessageGuidesUserToPathRemap is the #800
// regression: when the category save path doesn't fall under Bindery's
// download dir, the error message must name the fix (PathRemap) and include a
// concrete src:dst suggestion the user can copy. The previous wording said
// what was wrong but not how to fix it; reporter at #800 and #704 both bounced
// off it.
func TestQbittorrentCategoryPath_MismatchMessageGuidesUserToPathRemap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/categories":
			_, _ = w.Write([]byte(`{"library":{"name":"library","savePath":"/torrents/complete/library"}}`))
		case "/api/v2/app/defaultSavePath":
			_, _ = w.Write([]byte("/torrents"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Type:     "qbittorrent",
		Host:     host,
		Port:     port,
		Username: "u",
		Password: "p",
		Category: "library",
	}
	got := CheckDownloadClientHealth(context.Background(), client, "/downloads", "")
	if got.Status != HealthError {
		t.Fatalf("status = %q, want %q; message=%s", got.Status, HealthError, got.Message)
	}
	wants := []string{
		`expected a path at or under "/downloads"`,
		"path remap",
		`"/torrents/complete:/downloads"`,
	}
	for _, w := range wants {
		if !strings.Contains(got.Message, w) {
			t.Errorf("message missing %q\nfull: %s", w, got.Message)
		}
	}
}
