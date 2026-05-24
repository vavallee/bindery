package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestCheckDownloadClientHealth_QBittorrentCategoryPath(t *testing.T) {
	tests := []struct {
		name       string
		categories string
		pathRemap  string
		wantStatus string
		wantText   string
	}{
		{
			name:       "remapped category path matches",
			categories: `{"books":{"name":"books","savePath":"/media/downloads"}}`,
			wantStatus: HealthOK,
		},
		{
			name:       "qbit v5 boolean download_path still remaps category path",
			categories: `{"books":{"download_path":false,"name":"books","savePath":"/media/books/downloads"}}`,
			pathRemap:  "/media/books:/books",
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
			wantText:   `expected a path at or under "/books/downloads"`,
		},
		{
			name:       "category path is a subdirectory of download dir",
			categories: `{"books":{"name":"books","savePath":"/media/downloads/Torrents/books"}}`,
			wantStatus: HealthOK,
		},
		{
			name:       "category path under sibling dir is still rejected",
			categories: `{"books":{"name":"books","savePath":"/media/downloads-extra/books"}}`,
			wantStatus: HealthError,
			wantText:   `expected a path at or under "/books/downloads"`,
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
			pathRemap := tc.pathRemap
			if pathRemap == "" {
				pathRemap = "/media:/books"
			}
			client := &models.DownloadClient{
				Type:      "qbittorrent",
				Host:      host,
				Port:      port,
				Username:  "u",
				Password:  "p",
				Category:  "books",
				PathRemap: pathRemap,
			}
			got := CheckDownloadClientHealth(context.Background(), client, "/books/downloads", "")
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q; message=%s", got.Status, tc.wantStatus, got.Message)
			}
			if tc.wantText != "" && !strings.Contains(got.Message, tc.wantText) {
				t.Fatalf("message %q does not contain %q", got.Message, tc.wantText)
			}
		})
	}
}

func TestExpectedDownloadDirForClient_AudiobookCategory(t *testing.T) {
	client := &models.DownloadClient{Category: "audiobooks"}
	if got := ExpectedDownloadDirForClient(client, "/books/downloads", "/books/audio-downloads"); got != "/books/audio-downloads" {
		t.Fatalf("expected audiobook download dir, got %q", got)
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
