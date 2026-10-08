package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Fakes for the diagnose-action helpers (CompletedPath, GrabSavePath,
// TestConnection, CheckCategories). Each answers only the calls those helpers
// make, and a nil or failing field turns the matching endpoint into an error.

var coverageClientID atomic.Int64

// fakeClient points a DownloadClient of clientType at srv, with a fresh ID so
// the shared client cache never hands back another test's client.
func fakeClient(t *testing.T, srv *httptest.Server, clientType string) *models.DownloadClient {
	t.Helper()
	host, port := serverHostPort(t, srv.URL)
	return &models.DownloadClient{
		ID:   1_000_000 + coverageClientID.Add(1),
		Type: clientType, Host: host, Port: port,
		Username: "u", Password: "p", APIKey: "k",
	}
}

type qbitFake struct {
	categories  string // JSON for /torrents/categories; "" answers 500
	defaultPath string // body for /app/defaultSavePath
	defaultFail bool
}

func (f qbitFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v4.6.0"))
		case "/api/v2/torrents/categories":
			if f.categories == "" {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(f.categories))
		case "/api/v2/app/defaultSavePath":
			if f.defaultFail {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(f.defaultPath))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// nzbgetFake answers version and config; config entries are Name/Value pairs.
func nzbgetFake(t *testing.T, config map[string]string, fail bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"version"`) && !strings.Contains(string(body), `"config"`) {
			_, _ = w.Write([]byte(`{"version":"1.1","result":"21.0"}`))
			return
		}
		entries := make([]map[string]string, 0, len(config))
		for k, v := range config {
			entries = append(entries, map[string]string{"Name": k, "Value": v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.1", "result": entries})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// rtorrentDirFake answers directory.default with dir, or a 500 when fail.
func rtorrentDirFake(t *testing.T, dir string, fail bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><string>%s</string></value></param></params></methodResponse>`, dir)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// transmissionDirFake answers session-get with download-dir, or a 500.
func transmissionDirFake(t *testing.T, dir string, fail bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{"download-dir": dir}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// delugeFake answers the Web UI JSON-RPC calls behind DownloadLocation and
// Labels. labels nil makes label.get_labels fail, as with the plugin off.
type delugeFake struct {
	config     map[string]any
	options    map[string]any
	labels     []string
	configFail bool
}

func (f delugeFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := func(result any, errMsg string) {
			var e any
			if errMsg != "" {
				e = map[string]any{"code": -1, "message": errMsg}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": e, "id": req.ID})
		}
		switch req.Method {
		case "auth.login", "web.connected":
			reply(true, "")
		case "core.get_config_values":
			if f.configFail {
				reply(nil, "daemon gone")
				return
			}
			reply(f.config, "")
		case "label.get_options":
			if f.options == nil {
				reply(nil, "Unknown Label")
				return
			}
			reply(f.options, "")
		case "label.get_labels":
			if f.labels == nil {
				reply(nil, "Unknown method")
				return
			}
			reply(f.labels, "")
		default:
			reply(nil, "unknown method "+req.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sabFake answers get_config (misc and categories) and get_cats.
type sabFake struct {
	completeDir string
	catDirs     map[string]string
	cats        []string
	refuse      bool // get_config answers status false (NZB-only key)
	fail        bool // every call answers 403
}

func (f sabFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.fail {
			http.Error(w, "API Key Incorrect", http.StatusForbidden)
			return
		}
		q := r.URL.Query()
		switch q.Get("mode") {
		case "get_cats":
			_ = json.NewEncoder(w).Encode(map[string]any{"categories": f.cats})
		case "get_config":
			if f.refuse {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": false, "error": "API Key Required"})
				return
			}
			if q.Get("section") == "categories" {
				dir, ok := f.catDirs[q.Get("keyword")]
				if !ok {
					_ = json.NewEncoder(w).Encode(map[string]any{"status": false})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"categories": []map[string]string{{"name": q.Get("keyword"), "dir": dir}}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"misc": map[string]any{"complete_dir": f.completeDir}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClientTypeName(t *testing.T) {
	for in, want := range map[string]string{
		"qbittorrent": "qBittorrent", "transmission": "Transmission", "deluge": "Deluge",
		"rtorrent": "rTorrent", "nzbget": "NZBGet", "sabnzbd": "SABnzbd", "": "SABnzbd",
	} {
		if got := ClientTypeName(in); got != want {
			t.Errorf("ClientTypeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompletedPath(t *testing.T) {
	ctx := context.Background()
	if info, err := CompletedPath(ctx, nil); err != nil || info != (ClientPathInfo{}) {
		t.Fatalf("nil client = (%+v, %v)", info, err)
	}

	qbitCats := `{"books":{"name":"books","savePath":" /data/books "},"plain":{"name":"plain","savePath":""}}`
	tests := []struct {
		name       string
		client     func(t *testing.T) *models.DownloadClient
		wantPath   string
		wantSource string
		wantErr    bool
	}{
		{
			name: "qbittorrent without a category answers nothing",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, qbitFake{categories: qbitCats}.server(t), "qbittorrent")
			},
		},
		{
			name: "qbittorrent category save path",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, qbitFake{categories: qbitCats}.server(t), "qbittorrent")
				c.Category = " books "
				return c
			},
			wantPath: "/data/books", wantSource: "the category save path",
		},
		{
			name: "qbittorrent category without a save path falls back to the default",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, qbitFake{categories: qbitCats, defaultPath: "/data/default\n"}.server(t), "qbittorrent")
				c.Category = "plain"
				return c
			},
			wantPath: "/data/default", wantSource: "the client default",
		},
		{
			name: "qbittorrent default unreadable answers nothing",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, qbitFake{categories: qbitCats, defaultFail: true}.server(t), "qbittorrent")
				c.Category = "plain"
				return c
			},
		},
		{
			name: "qbittorrent unknown category answers nothing",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, qbitFake{categories: qbitCats}.server(t), "qbittorrent")
				c.Category = "missing"
				return c
			},
		},
		{
			name: "qbittorrent categories unreadable is an error",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, qbitFake{}.server(t), "qbittorrent")
				c.Category = "books"
				return c
			},
			wantErr: true,
		},
		{
			name: "nzbget DestDir",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, nzbgetFake(t, map[string]string{"MainDir": "/nzb", "DestDir": "${MainDir}/done"}, false), "nzbget")
			},
			wantPath: "/nzb/done", wantSource: "DestDir",
		},
		{
			name: "nzbget unreachable",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, nzbgetFake(t, nil, true), "nzbget")
			},
			wantErr: true,
		},
		{
			name: "rtorrent directory.default",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, rtorrentDirFake(t, " /rt/downloads ", false), "rtorrent")
			},
			wantPath: "/rt/downloads", wantSource: "the client default",
		},
		{
			name: "rtorrent unreachable",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, rtorrentDirFake(t, "", true), "rtorrent")
			},
			wantErr: true,
		},
		{
			name: "other types answer nothing without asking",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, transmissionDirFake(t, "", true), "transmission")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := CompletedPath(ctx, tt.client(t))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if info.Path != tt.wantPath || info.Source != tt.wantSource {
				t.Errorf("got (%q, %q), want (%q, %q)", info.Path, info.Source, tt.wantPath, tt.wantSource)
			}
		})
	}
}

func TestGrabSavePath_Qbittorrent(t *testing.T) {
	ctx := context.Background()
	cats := `{"abs":{"savePath":"/data/abs"},"rel":{"savePath":"sub/dir"},"bare":{"savePath":""},"audio":{"savePath":"/data/audio"}}`
	tests := []struct {
		name                 string
		fake                 qbitFake
		category, audioCat   string
		mediaType, dlDir     string
		globalRemap          string
		wantPath, wantSource string
		wantCategory         string
		wantNote, wantErr    bool
	}{
		{
			name: "absolute category save path", fake: qbitFake{categories: cats},
			category: "abs", mediaType: models.MediaTypeEbook,
			wantPath: "/data/abs", wantSource: "the category save path", wantCategory: "abs",
		},
		{
			name: "audiobook category is the one resolved", fake: qbitFake{categories: cats},
			category: "abs", audioCat: "audio", mediaType: models.MediaTypeAudiobook,
			wantPath: "/data/audio", wantSource: "the category save path", wantCategory: "audio",
		},
		{
			name: "relative category save path sits under the default", fake: qbitFake{categories: cats, defaultPath: "/data/qb"},
			category: "rel",
			wantPath: "/data/qb/sub/dir", wantSource: "the category save path, under the client default save path", wantCategory: "rel",
		},
		{
			name: "empty category save path is the default plus the category", fake: qbitFake{categories: cats, defaultPath: "/data/qb"},
			category: "bare",
			wantPath: "/data/qb/bare", wantSource: "the client default save path plus the category name", wantCategory: "bare",
		},
		{
			name: "relative path with an empty default answers nothing", fake: qbitFake{categories: cats, defaultPath: "  "},
			category: "rel", wantCategory: "rel",
		},
		{
			name: "relative path with an unreadable default is an error", fake: qbitFake{categories: cats, defaultFail: true},
			category: "rel", wantCategory: "rel", wantErr: true,
		},
		{
			name: "category the client lacks answers nothing", fake: qbitFake{categories: cats},
			category: "nope", wantCategory: "nope",
		},
		{
			name: "categories unreadable is an error", fake: qbitFake{},
			category: "abs", wantCategory: "abs", wantErr: true,
		},
		{
			name: "no category and no download dir uses the default", fake: qbitFake{defaultPath: " /data/qb "},
			wantPath: "/data/qb", wantSource: "the client default",
		},
		{
			name: "no category, no download dir, unreadable default", fake: qbitFake{defaultFail: true},
			wantErr: true,
		},
		{
			name: "no category sends the download dir", fake: qbitFake{defaultPath: "/elsewhere"},
			dlDir:    "/downloads",
			wantPath: "/downloads", wantSource: "the save path Bindery sends",
		},
		{
			name: "sent path still answers when the default is unreadable", fake: qbitFake{defaultFail: true},
			dlDir:    "/downloads",
			wantPath: "/downloads", wantSource: "the save path Bindery sends",
		},
		{
			// #2665: the global remap rewrote the folder, but qBittorrent's own
			// default sits on Bindery's path, so the remap is probably for a
			// different client.
			name: "global remap meant for another client is noted", fake: qbitFake{defaultPath: "/downloads/qb"},
			dlDir: "/downloads", globalRemap: "/sab/complete:/downloads",
			wantPath: "/sab/complete", wantSource: "the save path Bindery sends", wantNote: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fakeClient(t, tt.fake.server(t), "qbittorrent")
			client.Category, client.CategoryAudiobook = tt.category, tt.audioCat
			info, err := GrabSavePath(ctx, client, tt.mediaType, tt.dlDir, "", tt.globalRemap)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if info.Path != tt.wantPath || info.Source != tt.wantSource || info.Category != tt.wantCategory {
				t.Errorf("got (%q, %q, cat %q), want (%q, %q, cat %q)", info.Path, info.Source, info.Category, tt.wantPath, tt.wantSource, tt.wantCategory)
			}
			if (info.Note != "") != tt.wantNote {
				t.Errorf("note = %q, want note=%v", info.Note, tt.wantNote)
			}
			if tt.wantNote && !strings.Contains(info.NoteFix, `"/downloads:/downloads"`) {
				t.Errorf("fix = %q, want it to suggest an identity remap on this client", info.NoteFix)
			}
		})
	}
}

func TestGrabSavePath_OtherClients(t *testing.T) {
	ctx := context.Background()
	if info, err := GrabSavePath(ctx, nil, "", "/d", "", ""); err != nil || info != (ClientPathInfo{}) {
		t.Fatalf("nil client = (%+v, %v)", info, err)
	}

	tests := []struct {
		name                 string
		client               func(t *testing.T) *models.DownloadClient
		dlDir                string
		wantPath, wantSource string
		wantNote, wantErr    bool
	}{
		{
			name: "rtorrent sends the download dir",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, rtorrentDirFake(t, "/rt", false), "rtorrent")
			},
			dlDir:    "/downloads",
			wantPath: "/downloads", wantSource: "the save path Bindery sends",
		},
		{
			name: "rtorrent sent path survives an unreachable client",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, rtorrentDirFake(t, "", true), "rtorrent")
			},
			dlDir:    "/downloads",
			wantPath: "/downloads", wantSource: "the save path Bindery sends",
		},
		{
			name: "rtorrent without a download dir uses directory.default",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, rtorrentDirFake(t, " /rt/dl ", false), "rtorrent")
			},
			wantPath: "/rt/dl", wantSource: "the client default",
		},
		{
			name: "rtorrent without a download dir, unreachable",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, rtorrentDirFake(t, "", true), "rtorrent")
			},
			wantErr: true,
		},
		{
			name: "transmission absolute category is the download-dir sent",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, transmissionDirFake(t, "", true), "transmission")
				c.Category = "/tr/books"
				return c
			},
			wantPath: "/tr/books", wantSource: "the save path Bindery sends",
		},
		{
			name: "transmission label category uses the session download-dir",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, transmissionDirFake(t, "/tr/default", false), "transmission")
				c.Category = "books"
				return c
			},
			wantPath: "/tr/default", wantSource: "the client default",
		},
		{
			name: "transmission unreachable",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, transmissionDirFake(t, "", true), "transmission")
			},
			wantErr: true,
		},
		{
			name: "deluge label move path with its source",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, delugeFake{
					config:  map[string]any{"download_location": "/dl/incoming"},
					options: map[string]any{"apply_move_completed": true, "move_completed": true, "move_completed_path": "/dl/books"},
				}.server(t), "deluge")
				c.Category = "Books"
				return c
			},
			wantPath: "/dl/books", wantSource: `the move completed path of the label "books"`,
		},
		{
			name: "deluge unreadable label options carry a note",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, delugeFake{config: map[string]any{"download_location": "/dl/incoming"}}.server(t), "deluge")
				c.Category = "books"
				return c
			},
			wantPath: "/dl/incoming", wantSource: "the client default download location", wantNote: true,
		},
		{
			name: "deluge config unreadable",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, delugeFake{configFail: true}.server(t), "deluge")
			},
			wantErr: true,
		},
		{
			name: "nzbget category DestDir",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, nzbgetFake(t, map[string]string{
					"DestDir": "/nzb/done", "Category1.Name": "Books", "Category1.DestDir": "/nzb/books",
				}, false), "nzbget")
				c.Category = "books"
				return c
			},
			wantPath: "/nzb/books", wantSource: "the category DestDir",
		},
		{
			name:    "nzbget unreachable",
			client:  func(t *testing.T) *models.DownloadClient { return fakeClient(t, nzbgetFake(t, nil, true), "nzbget") },
			wantErr: true,
		},
		{
			name: "sabnzbd category folder",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, sabFake{completeDir: "/sab/complete", catDirs: map[string]string{"books": "ebooks"}}.server(t), "sabnzbd")
				c.Category = "books"
				return c
			},
			wantPath: "/sab/complete/ebooks", wantSource: "the category folder",
		},
		{
			name: "sabnzbd complete folder",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, sabFake{completeDir: "/sab/complete"}.server(t), "sabnzbd")
			},
			wantPath: "/sab/complete", wantSource: "the client completed folder",
		},
		{
			name: "sabnzbd NZB-only key is an error",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, sabFake{refuse: true}.server(t), "sabnzbd")
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := GrabSavePath(ctx, tt.client(t), models.MediaTypeEbook, tt.dlDir, "", "")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if info.Path != tt.wantPath || info.Source != tt.wantSource {
				t.Errorf("got (%q, %q), want (%q, %q)", info.Path, info.Source, tt.wantPath, tt.wantSource)
			}
			if (info.Note != "") != tt.wantNote {
				t.Errorf("note = %q, want note=%v", info.Note, tt.wantNote)
			}
		})
	}
}

func TestTestConnection(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		client  func(t *testing.T) *models.DownloadClient
		wantErr bool
	}{
		{"nzbget answers its version", func(t *testing.T) *models.DownloadClient {
			return fakeClient(t, nzbgetFake(t, nil, false), "nzbget")
		}, false},
		{"nzbget refuses", func(t *testing.T) *models.DownloadClient {
			return fakeClient(t, nzbgetFake(t, nil, true), "nzbget")
		}, true},
		{"transmission goes through TestClient", func(t *testing.T) *models.DownloadClient {
			return fakeClient(t, transmissionDirFake(t, "/x", false), "transmission")
		}, false},
		{"transmission down", func(t *testing.T) *models.DownloadClient {
			return fakeClient(t, transmissionDirFake(t, "", true), "transmission")
		}, true},
		{"sabnzbd connection ignores a missing category", func(t *testing.T) *models.DownloadClient {
			c := fakeClient(t, sabFake{cats: []string{"*", "tv"}}.server(t), "sabnzbd")
			c.Category = "books"
			return c
		}, false},
		{"sabnzbd bad key", func(t *testing.T) *models.DownloadClient {
			return fakeClient(t, sabFake{fail: true}.server(t), "sabnzbd")
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := TestConnection(ctx, tt.client(t)); (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckCategories(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name                          string
		client                        func(t *testing.T) *models.DownloadClient
		wantChecked                   bool
		wantWanted, wantMissing, want string // comma-joined; want is Existing
		wantErr                       bool
	}{
		{
			name: "qbittorrent reports the missing one, sorted existing",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, qbitFake{categories: `{"tv":{},"books":{},"books/ebooks":{}}`}.server(t), "qbittorrent")
				c.Category, c.CategoryAudiobook = "books/ebooks", "audio"
				return c
			},
			wantChecked: true, wantWanted: "books/ebooks,audio", wantMissing: "audio", want: "books,books/ebooks,tv",
		},
		{
			name: "duplicate and blank categories are wanted once",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, qbitFake{categories: `{"books":{}}`}.server(t), "qbittorrent")
				c.Category, c.CategoryAudiobook = "books", "books"
				return c
			},
			wantChecked: true, wantWanted: "books", want: "books",
		},
		{
			name: "a category with surrounding spaces does not match",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, qbitFake{categories: `{"books":{}}`}.server(t), "qbittorrent")
				c.Category, c.CategoryAudiobook = " books", "  "
				return c
			},
			wantChecked: true, wantWanted: " books", wantMissing: " books", want: "books",
		},
		{
			name: "qbittorrent unreachable",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, qbitFake{}.server(t), "qbittorrent")
				c.Category = "books"
				return c
			},
			wantWanted: "books", wantErr: true,
		},
		{
			name: "nzbget",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, nzbgetFake(t, map[string]string{"Category1.Name": "books", "Category2.Name": "audio"}, false), "nzbget")
				c.Category = "books"
				return c
			},
			wantChecked: true, wantWanted: "books", want: "audio,books",
		},
		{
			name: "nzbget unreachable",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, nzbgetFake(t, nil, true), "nzbget")
			},
			wantErr: true,
		},
		{
			name: "deluge compares the lowercase label id",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, delugeFake{labels: []string{"books"}}.server(t), "deluge")
				c.Category, c.CategoryAudiobook = "Books", "Audio"
				return c
			},
			wantChecked: true, wantWanted: "books,audio", wantMissing: "audio", want: "books",
		},
		{
			name: "deluge with the label plugin off is unchecked, not an error",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, delugeFake{}.server(t), "deluge")
				c.Category = "books"
				return c
			},
			wantWanted: "books",
		},
		{
			name: "transmission has no category list",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, transmissionDirFake(t, "", true), "transmission")
				c.Category = "books"
				return c
			},
			wantWanted: "books",
		},
		{
			name: "rtorrent has no category list",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, rtorrentDirFake(t, "", true), "rtorrent")
			},
		},
		{
			name: "sabnzbd",
			client: func(t *testing.T) *models.DownloadClient {
				c := fakeClient(t, sabFake{cats: []string{"tv", "*", "books"}}.server(t), "sabnzbd")
				c.Category = "books"
				return c
			},
			wantChecked: true, wantWanted: "books", want: "*,books,tv",
		},
		{
			name: "sabnzbd unreachable",
			client: func(t *testing.T) *models.DownloadClient {
				return fakeClient(t, sabFake{fail: true}.server(t), "sabnzbd")
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := CheckCategories(ctx, tt.client(t))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if r.Checked != tt.wantChecked {
				t.Errorf("Checked = %v, want %v", r.Checked, tt.wantChecked)
			}
			if got := strings.Join(r.Wanted, ","); got != tt.wantWanted {
				t.Errorf("Wanted = %q, want %q", got, tt.wantWanted)
			}
			if got := strings.Join(r.Missing, ","); got != tt.wantMissing {
				t.Errorf("Missing = %q, want %q", got, tt.wantMissing)
			}
			if got := strings.Join(r.Existing, ","); got != tt.want {
				t.Errorf("Existing = %q, want %q", got, tt.want)
			}
		})
	}
}
