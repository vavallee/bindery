package downloader

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestCheckCompletedPathVisibility_Qbittorrent verifies the Test-time path
// visibility probe (#1182): a category whose remapped save path exists on
// Bindery's filesystem reports PathVisible, and one that doesn't reports
// PathNotVisible with an actionable message.
func TestCheckCompletedPathVisibility_Qbittorrent(t *testing.T) {
	visible := t.TempDir()

	tests := []struct {
		name       string
		categories string
		pathRemap  string
		wantStatus string
		wantInMsg  string
	}{
		{
			name:       "remapped path exists -> visible",
			categories: `{"books":{"name":"books","savePath":"/remote/downloads"}}`,
			pathRemap:  "/remote/downloads:" + visible,
			wantStatus: PathVisible,
			wantInMsg:  "can read",
		},
		{
			name:       "remapped path missing -> warning",
			categories: `{"books":{"name":"books","savePath":"/remote/downloads"}}`,
			pathRemap:  "/remote/downloads:" + filepath.Join(visible, "does-not-exist"),
			wantStatus: PathNotVisible,
			wantInMsg:  "can't read",
		},
		{
			name:       "no remap, client path missing -> warning",
			categories: `{"books":{"name":"books","savePath":"/totally/not/here/xyz"}}`,
			wantStatus: PathNotVisible,
			wantInMsg:  "path remap",
		},
		{
			name:       "category not found -> unknown (no false alarm)",
			categories: `{}`,
			wantStatus: PathUnknown,
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
					_, _ = w.Write([]byte(""))
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
			got := CheckCompletedPathVisibility(context.Background(), client, "/some/download/dir", "", "")
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q; message=%s", got.Status, tc.wantStatus, got.Message)
			}
			if tc.wantInMsg != "" && !strings.Contains(got.Message, tc.wantInMsg) {
				t.Fatalf("message %q does not contain %q", got.Message, tc.wantInMsg)
			}
		})
	}
}

// TestCheckCompletedPathVisibility_GlobalRemapFallback verifies that when a
// client has no per-client PathRemap, the global BINDERY_DOWNLOAD_PATH_REMAP is
// applied as a fallback so the Test verdict matches what the importer resolves
// (#1182). Without the fallback the path would false-warn.
func TestCheckCompletedPathVisibility_GlobalRemapFallback(t *testing.T) {
	visible := t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/categories":
			_, _ = w.Write([]byte(`{"books":{"name":"books","savePath":"/remote/downloads"}}`))
		case "/api/v2/app/defaultSavePath":
			_, _ = w.Write([]byte(""))
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
		Category: "books",
		// No per-client PathRemap on purpose — the global remap must cover it.
	}
	globalRemap := "/remote/downloads:" + visible
	got := CheckCompletedPathVisibility(context.Background(), client, "/some/download/dir", "", globalRemap)
	if got.Status != PathVisible {
		t.Fatalf("status = %q, want %q (global remap fallback); message=%s", got.Status, PathVisible, got.Message)
	}
}

// TestCheckCompletedPathVisibility_Nzbget verifies the probe resolves NZBGet's
// completed directory from its config RPC (expanding ${MainDir}) and stats it.
func TestCheckCompletedPathVisibility_Nzbget(t *testing.T) {
	visible := t.TempDir()

	newServer := func(destDir string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// version + config both POST to /jsonrpc; switch on the method.
			body := readBody(t, r)
			if strings.Contains(body, `"version"`) {
				_, _ = w.Write([]byte(`{"version":"1.1","result":"21.0"}`))
				return
			}
			_, _ = w.Write([]byte(`{"version":"1.1","result":[` +
				`{"Name":"MainDir","Value":"` + visible + `"},` +
				`{"Name":"DestDir","Value":"` + destDir + `"},` +
				`{"Name":"Category1.Name","Value":"books"}` +
				`]}`))
		}))
	}

	t.Run("completed dir exists -> visible", func(t *testing.T) {
		srv := newServer("${MainDir}")
		defer srv.Close()
		host, port := serverHostPort(t, srv.URL)
		client := &models.DownloadClient{Type: "nzbget", Host: host, Port: port, Category: "books"}
		got := CheckCompletedPathVisibility(context.Background(), client, "", "", "")
		if got.Status != PathVisible {
			t.Fatalf("status = %q, want %q; message=%s", got.Status, PathVisible, got.Message)
		}
	})

	t.Run("completed dir missing -> warning", func(t *testing.T) {
		srv := newServer(filepath.Join(visible, "nope"))
		defer srv.Close()
		host, port := serverHostPort(t, srv.URL)
		client := &models.DownloadClient{Type: "nzbget", Host: host, Port: port, Category: "books"}
		got := CheckCompletedPathVisibility(context.Background(), client, "", "", "")
		if got.Status != PathNotVisible {
			t.Fatalf("status = %q, want %q; message=%s", got.Status, PathNotVisible, got.Message)
		}
	})
}

// TestCheckCompletedPathVisibility_UnknownTypes confirms client types whose
// completed path isn't introspectable degrade to PathUnknown (connection-only).
func TestCheckCompletedPathVisibility_UnknownTypes(t *testing.T) {
	for _, typ := range []string{"sabnzbd", "transmission", "deluge", ""} {
		client := &models.DownloadClient{Type: typ, Host: "10.0.0.1", Port: 8080, Category: "books"}
		got := CheckCompletedPathVisibility(context.Background(), client, "/x", "", "")
		if got.Status != PathUnknown {
			t.Fatalf("type %q: status = %q, want %q", typ, got.Status, PathUnknown)
		}
	}
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}
