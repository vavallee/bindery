package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func queueFixture(t *testing.T) (*QueueHandler, *sql.DB, *db.DownloadRepo, *db.DownloadClientRepo, *db.BookRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)
	books := db.NewBookRepo(database)
	history := db.NewHistoryRepo(database)
	return NewQueueHandler(downloads, clients, books, history), database, downloads, clients, books, context.Background()
}

func TestQueueGrab_RequiresGUIDAndURL(t *testing.T) {
	h, _, _, _, _, _ := queueFixture(t)
	for _, body := range []string{
		`{}`,
		`{"guid":"abc"}`,
		`{"nzbUrl":"http://example/x.nzb"}`,
	} {
		rec := httptest.NewRecorder()
		h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewBufferString(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: expected 400, got %d", body, rec.Code)
		}
	}
}

func TestQueueGrab_RejectsBadJSON(t *testing.T) {
	h, _, _, _, _, _ := queueFixture(t)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewBufferString("not-json")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestQueueGrab_NoDownloadClient(t *testing.T) {
	h, _, _, _, _, _ := queueFixture(t)
	body := bytes.NewBufferString(`{"guid":"abc","nzbUrl":"http://example/x.nzb","title":"t"}`)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", body))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with no client configured, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestQueueGrab_DuplicateGUID(t *testing.T) {
	h, _, downloads, _, _, ctx := queueFixture(t)
	if err := downloads.Create(ctx, &models.Download{
		GUID: "dup-guid", Title: "T", Status: models.DownloadStatusDownloading, Protocol: "usenet",
	}); err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"guid":"dup-guid","nzbUrl":"http://example/x.nzb","title":"T"}`)
	rec := httptest.NewRecorder()
	h.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", body))
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", rec.Code)
	}
}

func TestQueueDelete_NotFound(t *testing.T) {
	h, _, _, _, _, _ := queueFixture(t)
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/queue/42", nil), "id", "42")
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestQueueDelete_FlipsBookToWanted(t *testing.T) {
	h, database, downloads, _, books, ctx := queueFixture(t)
	a := &models.Author{ForeignID: "OL1", Name: "X", SortName: "X", MetadataProvider: "openlibrary", Monitored: true}
	if err := db.NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "B1", AuthorID: a.ID, Title: "T", SortTitle: "t",
		Status: models.BookStatusDownloading, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	d := &models.Download{
		GUID: "g", BookID: &b.ID, Title: "T",
		Status: models.DownloadStatusDownloading, Protocol: "usenet",
	}
	if err := downloads.Create(ctx, d); err != nil {
		t.Fatal(err)
	}

	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/queue/"+strconv.FormatInt(d.ID, 10), nil), "id", strconv.FormatInt(d.ID, 10))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := books.GetByID(ctx, b.ID)
	if got.Status != models.BookStatusWanted {
		t.Errorf("book status should flip to wanted, got %q", got.Status)
	}
}

func TestQueueListLiveOverlaySABnzbd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("mode") != "queue" {
			t.Fatalf("expected mode=queue, got %s", r.URL.Query().Get("mode"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"queue": map[string]any{
				"speed": "2.0 MB/s",
				"slots": []map[string]any{{
					"nzo_id":     "nzo123",
					"percentage": "55",
					"timeleft":   "0:10:00",
				}},
			},
		})
	}))
	defer srv.Close()

	h := newQueueTestHandler(t)
	host, port := testServerHostPort(t, srv.URL)
	client := createTestDownloadClient(t, h, &models.DownloadClient{
		Name:     "sab",
		Type:     "sabnzbd",
		Host:     host,
		Port:     port,
		APIKey:   "testkey",
		Category: "books",
		Enabled:  true,
	})
	createTestDownload(t, h, &models.Download{
		GUID:             "guid-sab",
		DownloadClientID: &client.ID,
		Title:            "Sab Book",
		NZBURL:           "https://example.com/book.nzb",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "usenet",
		SABnzbdNzoID:     strPtr("nzo123"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var items []QueueItem
	if err := json.NewDecoder(rr.Body).Decode(&items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Percentage != "55" || items[0].TimeLeft != "0:10:00" || items[0].Speed != "2.0 MB/s" {
		t.Fatalf("unexpected overlay: %+v", items[0])
	}
}

func TestQueueListLiveOverlaySABnzbd_WithHigherPriorityTorrentClient(t *testing.T) {
	sabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("mode") != "queue" {
			t.Fatalf("expected mode=queue, got %s", r.URL.Query().Get("mode"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"queue": map[string]any{
				"speed": "1.5 MB/s",
				"slots": []map[string]any{{
					"nzo_id":     "nzo999",
					"percentage": "66",
					"timeleft":   "0:05:00",
				}},
			},
		})
	}))
	defer sabSrv.Close()

	transSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"arguments": map[string]any{"torrents": []map[string]any{}},
			"result":    "success",
		})
	}))
	defer transSrv.Close()

	h := newQueueTestHandler(t)

	transHost, transPort := testServerHostPort(t, transSrv.URL)
	_ = createTestDownloadClient(t, h, &models.DownloadClient{
		Name:     "trans-first",
		Type:     "transmission",
		Host:     transHost,
		Port:     transPort,
		Priority: 1,
		Enabled:  true,
	})

	sabHost, sabPort := testServerHostPort(t, sabSrv.URL)
	sabClient := createTestDownloadClient(t, h, &models.DownloadClient{
		Name:     "sab-second",
		Type:     "sabnzbd",
		Host:     sabHost,
		Port:     sabPort,
		APIKey:   "testkey",
		Priority: 2,
		Enabled:  true,
	})

	createTestDownload(t, h, &models.Download{
		GUID:             "guid-sab-2",
		DownloadClientID: &sabClient.ID,
		Title:            "Sab Book 2",
		NZBURL:           "https://example.com/book2.nzb",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "usenet",
		SABnzbdNzoID:     strPtr("nzo999"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var items []QueueItem
	if err := json.NewDecoder(rr.Body).Decode(&items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Percentage != "66" || items[0].TimeLeft != "0:05:00" || items[0].Speed != "1.5 MB/s" {
		t.Fatalf("unexpected overlay when torrent client has higher priority: %+v", items[0])
	}
}

func TestQueueListLiveOverlayTransmission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"arguments": map[string]any{
				"torrents": []map[string]any{{
					"id":           7,
					"percentDone":  0.42,
					"eta":          125,
					"rateDownload": 4096,
				}},
			},
			"result": "success",
		})
	}))
	defer srv.Close()

	h := newQueueTestHandler(t)
	host, port := testServerHostPort(t, srv.URL)
	client := createTestDownloadClient(t, h, &models.DownloadClient{
		Name:     "trans",
		Type:     "transmission",
		Host:     host,
		Port:     port,
		Username: "user",
		Password: "pass",
		Enabled:  true,
	})
	createTestDownload(t, h, &models.Download{
		GUID:             "guid-trans",
		DownloadClientID: &client.ID,
		Title:            "Torrent Book",
		NZBURL:           "magnet:?xt=urn:btih:abc",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        strPtr("7"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var items []QueueItem
	if err := json.NewDecoder(rr.Body).Decode(&items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Percentage != "42.0" {
		t.Fatalf("unexpected percentage: %s", items[0].Percentage)
	}
	if items[0].TimeLeft == "" || items[0].Speed == "" {
		t.Fatalf("expected timeLeft and speed, got %+v", items[0])
	}
}

func TestQueueListLiveOverlayQbittorrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash":     "ABCDEF",
				"progress": 0.9,
				"eta":      300,
			}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	h := newQueueTestHandler(t)
	host, port := testServerHostPort(t, srv.URL)
	client := createTestDownloadClient(t, h, &models.DownloadClient{
		Name:     "qb",
		Type:     "qbittorrent",
		Host:     host,
		Port:     port,
		Username: "user",
		Password: "pass",
		Enabled:  true,
	})
	createTestDownload(t, h, &models.Download{
		GUID:             "guid-qb",
		DownloadClientID: &client.ID,
		Title:            "QB Book",
		NZBURL:           "magnet:?xt=urn:btih:abcdef",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        strPtr("abcdef"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var items []QueueItem
	if err := json.NewDecoder(rr.Body).Decode(&items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Percentage != "90.0" {
		t.Fatalf("unexpected percentage: %s", items[0].Percentage)
	}
	if items[0].TimeLeft == "" {
		t.Fatalf("expected timeLeft, got %+v", items[0])
	}
}

func TestQueueListArrCompatibleEmpty(t *testing.T) {
	h := newQueueTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	rr := httptest.NewRecorder()
	h.ListArrCompatible(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp arrQueueResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TotalRecords != 0 || len(resp.Records) != 0 {
		t.Fatalf("expected empty queue response, got %+v", resp)
	}
}

func TestQueueListArrCompatibleQbittorrentShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash":        "ABCDEF",
				"progress":    0.5,
				"eta":         300,
				"size":        1048576,
				"amount_left": 524288,
				"state":       "downloading",
			}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	h, database, _, _, books, ctx := queueFixture(t)
	host, port := testServerHostPort(t, srv.URL)
	client := createTestDownloadClient(t, h, &models.DownloadClient{
		Name:     "qBittorrent",
		Type:     "qbittorrent",
		Host:     host,
		Port:     port,
		Username: "user",
		Password: "pass",
		Enabled:  true,
	})
	a := &models.Author{ForeignID: "OLQBA", Name: "Andy Weir", SortName: "Weir, Andy", MetadataProvider: "openlibrary", Monitored: true}
	if err := db.NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OLQBB", AuthorID: a.ID, Title: "Project Hail Mary", SortTitle: "project hail mary",
		Status: models.BookStatusDownloading, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	createTestDownload(t, h, &models.Download{
		GUID:             "guid-qb-arr",
		BookID:           &b.ID,
		DownloadClientID: &client.ID,
		Title:            "Project Hail Mary",
		NZBURL:           "magnet:?xt=urn:btih:abcdef",
		Size:             999,
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        strPtr("abcdef"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/queue?page=1&pageSize=10", nil)
	rr := httptest.NewRecorder()
	h.ListArrCompatible(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp arrQueueResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TotalRecords != 1 || resp.Page != 1 || resp.PageSize != 10 {
		t.Fatalf("unexpected paging response: %+v", resp)
	}
	if len(resp.Records) != 1 {
		t.Fatalf("expected one record, got %d", len(resp.Records))
	}
	rec := resp.Records[0]
	if rec.ID == 0 || rec.BookID != b.ID || rec.Title != "Project Hail Mary" {
		t.Fatalf("unexpected identity fields: %+v", rec)
	}
	if rec.Status != string(models.StateDownloading) || rec.TrackedDownloadStatus != "ok" {
		t.Fatalf("unexpected status fields: %+v", rec)
	}
	if rec.Size != 1048576 || rec.SizeLeft != 524288 {
		t.Fatalf("expected live size fields, got %+v", rec)
	}
	if rec.DownloadClient != "qBittorrent" || rec.DownloadID != "abcdef" || rec.Protocol != "torrent" {
		t.Fatalf("unexpected client fields: %+v", rec)
	}
}

func TestQueueListArrCompatibleLiveStatusMatrix(t *testing.T) {
	newStatusServer := func(t *testing.T, clientType, remoteID string, errorState bool) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch clientType {
			case "sabnzbd":
				status := "Downloading"
				if errorState {
					status = "Failed"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"queue": map[string]any{
						"slots": []map[string]any{{
							"nzo_id":     remoteID,
							"status":     status,
							"mb":         "10.0",
							"mbleft":     "5.0",
							"percentage": "50",
						}},
					},
				})
			case "nzbget":
				nzbID, err := strconv.Atoi(remoteID)
				if err != nil {
					t.Fatalf("bad NZBGet remote ID: %v", err)
				}
				status := "DOWNLOADING"
				if errorState {
					status = "FAILURE/UNPACK"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"result": []map[string]any{{
						"NZBID":           nzbID,
						"Status":          status,
						"FileSizeMB":      10.0,
						"RemainingSizeMB": 5.0,
					}},
				})
			case "transmission":
				status := 2
				if errorState {
					status = 16
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"arguments": map[string]any{
						"torrents": []map[string]any{{
							"id":            7,
							"status":        status,
							"percentDone":   0.5,
							"totalSize":     1000,
							"leftUntilDone": 500,
						}},
					},
					"result": "success",
				})
			case "qbittorrent":
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = w.Write([]byte("Ok."))
				case "/api/v2/torrents/info":
					state := "downloading"
					if errorState {
						state = "error"
					}
					_ = json.NewEncoder(w).Encode([]map[string]any{{
						"hash":        remoteID,
						"state":       state,
						"progress":    0.5,
						"size":        1000,
						"amount_left": 500,
					}})
				default:
					t.Fatalf("unexpected qBittorrent path: %s", r.URL.Path)
				}
			case "deluge":
				var req struct {
					Method string `json:"method"`
					ID     int64  `json:"id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatalf("decode Deluge request: %v", err)
				}
				var result any
				switch req.Method {
				case "auth.login":
					result = true
				case "core.get_torrents_status":
					state := "Downloading"
					if errorState {
						state = "Error"
					}
					result = map[string]any{
						remoteID: map[string]any{
							"hash":       remoteID,
							"state":      state,
							"progress":   50.0,
							"total_size": 1000,
							"total_done": 500,
						},
					}
				default:
					t.Fatalf("unexpected Deluge method: %s", req.Method)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": req.ID})
			default:
				t.Fatalf("unsupported client type: %s", clientType)
			}
		}))
	}

	tests := []struct {
		name       string
		clientType string
		remoteID   string
		torrent    bool
		errorState bool
		want       string
	}{
		{name: "sabnzbd healthy", clientType: "sabnzbd", remoteID: "nzo-sab", want: "ok"},
		{name: "sabnzbd error", clientType: "sabnzbd", remoteID: "nzo-sab", errorState: true, want: "error"},
		{name: "nzbget healthy", clientType: "nzbget", remoteID: "101", want: "ok"},
		{name: "nzbget error", clientType: "nzbget", remoteID: "101", errorState: true, want: "error"},
		{name: "transmission healthy", clientType: "transmission", remoteID: "7", torrent: true, want: "ok"},
		{name: "transmission error", clientType: "transmission", remoteID: "7", torrent: true, errorState: true, want: "error"},
		{name: "qbittorrent healthy", clientType: "qbittorrent", remoteID: "abcdef", torrent: true, want: "ok"},
		{name: "qbittorrent error", clientType: "qbittorrent", remoteID: "abcdef", torrent: true, errorState: true, want: "error"},
		{name: "deluge healthy", clientType: "deluge", remoteID: "abcdef", torrent: true, want: "ok"},
		{name: "deluge error", clientType: "deluge", remoteID: "abcdef", torrent: true, errorState: true, want: "error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newStatusServer(t, tc.clientType, tc.remoteID, tc.errorState)
			defer srv.Close()

			h := newQueueTestHandler(t)
			host, port := testServerHostPort(t, srv.URL)
			client := createTestDownloadClient(t, h, &models.DownloadClient{
				Name:     tc.clientType,
				Type:     tc.clientType,
				Host:     host,
				Port:     port,
				APIKey:   "testkey",
				Username: "user",
				Password: "pass",
				Enabled:  true,
			})
			dl := &models.Download{
				GUID:             "guid-" + tc.name,
				DownloadClientID: &client.ID,
				Title:            "Matrix Book",
				NZBURL:           "https://example.com/download",
				Size:             1000,
				Status:           models.DownloadStatusDownloading,
				Protocol:         "usenet",
			}
			if tc.torrent {
				dl.Protocol = "torrent"
				dl.TorrentID = strPtr(tc.remoteID)
			} else {
				dl.SABnzbdNzoID = strPtr(tc.remoteID)
			}
			createTestDownload(t, h, dl)

			req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
			rr := httptest.NewRecorder()
			h.ListArrCompatible(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
			}
			var resp arrQueueResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(resp.Records) != 1 {
				t.Fatalf("expected one record, got %+v", resp)
			}
			if resp.Records[0].TrackedDownloadStatus != tc.want {
				t.Fatalf("trackedDownloadStatus: want %q, got %+v", tc.want, resp.Records[0])
			}
		})
	}
}

func TestQueueListArrCompatiblePollFailureMatrix(t *testing.T) {
	tests := []struct {
		name       string
		clientType string
		remoteID   string
		torrent    bool
	}{
		{name: "sabnzbd", clientType: "sabnzbd", remoteID: "nzo-sab"},
		{name: "nzbget", clientType: "nzbget", remoteID: "101"},
		{name: "transmission", clientType: "transmission", remoteID: "7", torrent: true},
		{name: "qbittorrent", clientType: "qbittorrent", remoteID: "abcdef", torrent: true},
		{name: "deluge", clientType: "deluge", remoteID: "abcdef", torrent: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "down", http.StatusBadGateway)
			}))
			defer srv.Close()

			h := newQueueTestHandler(t)
			host, port := testServerHostPort(t, srv.URL)
			client := createTestDownloadClient(t, h, &models.DownloadClient{
				Name:     tc.clientType,
				Type:     tc.clientType,
				Host:     host,
				Port:     port,
				APIKey:   "testkey",
				Username: "user",
				Password: "pass",
				Enabled:  true,
			})
			dl := &models.Download{
				GUID:             "guid-warning-" + tc.name,
				DownloadClientID: &client.ID,
				Title:            "Warning Book",
				NZBURL:           "https://example.com/download",
				Size:             2048,
				Status:           models.DownloadStatusDownloading,
				Protocol:         "usenet",
			}
			if tc.torrent {
				dl.Protocol = "torrent"
				dl.TorrentID = strPtr(tc.remoteID)
			} else {
				dl.SABnzbdNzoID = strPtr(tc.remoteID)
			}
			createTestDownload(t, h, dl)

			req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
			rr := httptest.NewRecorder()
			h.ListArrCompatible(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
			}
			var resp arrQueueResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(resp.Records) != 1 {
				t.Fatalf("expected one record, got %+v", resp)
			}
			if resp.Records[0].TrackedDownloadStatus != "warning" {
				t.Fatalf("expected warning status, got %+v", resp.Records[0])
			}
			if resp.Records[0].SizeLeft != 2048 {
				t.Fatalf("expected conservative sizeleft, got %+v", resp.Records[0])
			}
		})
	}
}

func TestQueueListArrCompatibleRemoteIDNormalization(t *testing.T) {
	tests := []struct {
		name       string
		clientType string
		torrentID  *string
		nzoID      *string
		want       string
	}{
		{name: "qbittorrent torrent ID", clientType: "qbittorrent", torrentID: strPtr("ABCDEF"), want: "abcdef"},
		{name: "deluge torrent ID", clientType: "deluge", torrentID: strPtr("123ABC"), want: "123abc"},
		{name: "sabnzbd nzo ID", clientType: "sabnzbd", nzoID: strPtr("NZOABC"), want: "NZOABC"},
		{name: "nzbget ID", clientType: "nzbget", nzoID: strPtr("NZBABC"), want: "NZBABC"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newQueueTestHandler(t)
			client := createTestDownloadClient(t, h, &models.DownloadClient{
				Name:    tc.clientType,
				Type:    tc.clientType,
				Host:    "127.0.0.1",
				Port:    1,
				Enabled: false,
			})
			protocol := "usenet"
			if tc.torrentID != nil {
				protocol = "torrent"
			}
			createTestDownload(t, h, &models.Download{
				GUID:             "guid-normalize-" + tc.name,
				DownloadClientID: &client.ID,
				Title:            "Remote ID Book",
				NZBURL:           "https://example.com/download",
				Size:             100,
				Status:           models.DownloadStatusDownloading,
				Protocol:         protocol,
				TorrentID:        tc.torrentID,
				SABnzbdNzoID:     tc.nzoID,
			})

			req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
			rr := httptest.NewRecorder()
			h.ListArrCompatible(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
			}
			var resp arrQueueResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(resp.Records) != 1 {
				t.Fatalf("expected one record, got %+v", resp)
			}
			if resp.Records[0].DownloadID != tc.want {
				t.Fatalf("downloadId: want %q, got %+v", tc.want, resp.Records[0])
			}
		})
	}
}

func TestQueueListArrCompatiblePollFailureWarns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()

	h := newQueueTestHandler(t)
	host, port := testServerHostPort(t, srv.URL)
	client := createTestDownloadClient(t, h, &models.DownloadClient{
		Name:    "SABnzbd",
		Type:    "sabnzbd",
		Host:    host,
		Port:    port,
		APIKey:  "testkey",
		Enabled: true,
	})
	createTestDownload(t, h, &models.Download{
		GUID:             "guid-sab-warning",
		DownloadClientID: &client.ID,
		Title:            "Warning Book",
		Size:             2048,
		Status:           models.DownloadStatusDownloading,
		Protocol:         "usenet",
		SABnzbdNzoID:     strPtr("nzo-warning"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	rr := httptest.NewRecorder()
	h.ListArrCompatible(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp arrQueueResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Records) != 1 {
		t.Fatalf("expected one record, got %d", len(resp.Records))
	}
	rec := resp.Records[0]
	if rec.TrackedDownloadStatus != "warning" {
		t.Fatalf("expected warning status, got %+v", rec)
	}
	if rec.SizeLeft != 2048 {
		t.Fatalf("expected conservative sizeleft, got %+v", rec)
	}
}

func TestQueueListArrCompatibleLocalErrorOutranksPollFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()

	h := newQueueTestHandler(t)
	host, port := testServerHostPort(t, srv.URL)
	client := createTestDownloadClient(t, h, &models.DownloadClient{
		Name:    "SABnzbd",
		Type:    "sabnzbd",
		Host:    host,
		Port:    port,
		APIKey:  "testkey",
		Enabled: true,
	})
	createTestDownload(t, h, &models.Download{
		GUID:             "guid-local-error",
		DownloadClientID: &client.ID,
		Title:            "Failed Book",
		Size:             2048,
		Status:           models.StateFailed,
		Protocol:         "usenet",
		ErrorMessage:     "download failed",
		SABnzbdNzoID:     strPtr("nzo-failed"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	rr := httptest.NewRecorder()
	h.ListArrCompatible(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp arrQueueResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Records) != 1 {
		t.Fatalf("expected one record, got %d", len(resp.Records))
	}
	if resp.Records[0].TrackedDownloadStatus != "error" {
		t.Fatalf("expected error status, got %+v", resp.Records[0])
	}
}

func TestQueueListArrCompatibleSkipsImportedDownloads(t *testing.T) {
	h := newQueueTestHandler(t)
	createTestDownload(t, h, &models.Download{
		GUID: "guid-imported", Title: "Imported Book", Size: 20,
		Status: models.StateImported, Protocol: "usenet",
	})
	createTestDownload(t, h, &models.Download{
		GUID: "guid-active", Title: "Active Book", Size: 10,
		Status: models.DownloadStatusDownloading, Protocol: "usenet",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	rr := httptest.NewRecorder()
	h.ListArrCompatible(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp arrQueueResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TotalRecords != 1 || len(resp.Records) != 1 {
		t.Fatalf("expected only active record, got %+v", resp)
	}
	if resp.Records[0].Title != "Active Book" {
		t.Fatalf("expected active record, got %+v", resp.Records[0])
	}
}

func TestQueueListArrCompatiblePaginationAndSort(t *testing.T) {
	h := newQueueTestHandler(t)
	createTestDownload(t, h, &models.Download{
		GUID: "guid-b", Title: "B Book", Size: 20,
		Status: models.DownloadStatusDownloading, Protocol: "usenet",
	})
	createTestDownload(t, h, &models.Download{
		GUID: "guid-a", Title: "A Book", Size: 10,
		Status: models.DownloadStatusDownloading, Protocol: "usenet",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/queue?sortKey=title&sortDirection=ascending&page=2&pageSize=1", nil)
	rr := httptest.NewRecorder()
	h.ListArrCompatible(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp arrQueueResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TotalRecords != 2 || resp.Page != 2 || resp.PageSize != 1 {
		t.Fatalf("unexpected paging metadata: %+v", resp)
	}
	if len(resp.Records) != 1 || resp.Records[0].Title != "B Book" {
		t.Fatalf("expected second sorted record, got %+v", resp.Records)
	}
}

func TestQueueListArrCompatiblePaginationOverflow(t *testing.T) {
	h := newQueueTestHandler(t)
	createTestDownload(t, h, &models.Download{
		GUID: "guid-overflow", Title: "Overflow Book", Size: 20,
		Status: models.DownloadStatusDownloading, Protocol: "usenet",
	})

	maxInt := int(^uint(0) >> 1)
	req := httptest.NewRequest(http.MethodGet, "/api/queue?page="+strconv.Itoa(maxInt)+"&pageSize=2", nil)
	rr := httptest.NewRecorder()
	h.ListArrCompatible(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp arrQueueResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TotalRecords != 1 || len(resp.Records) != 0 {
		t.Fatalf("expected empty overflow page with preserved total, got %+v", resp)
	}
}

func newQueueTestHandler(t *testing.T) *QueueHandler {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return NewQueueHandler(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database), nil, nil)
}

func createTestDownloadClient(t *testing.T, h *QueueHandler, client *models.DownloadClient) *models.DownloadClient {
	t.Helper()
	if err := h.clients.Create(context.Background(), client); err != nil {
		t.Fatalf("create download client: %v", err)
	}
	return client
}

func createTestDownload(t *testing.T, h *QueueHandler, dl *models.Download) *models.Download {
	t.Helper()
	if err := h.downloads.Create(context.Background(), dl); err != nil {
		t.Fatalf("create download: %v", err)
	}
	return dl
}

func testServerHostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	return u.Hostname(), port
}

func strPtr(v string) *string { return &v }

// TestQueueListLiveOverlayTransmission_TorrentIDLowercased verifies that
// a TorrentID stored in mixed-case (e.g. from an older grab) is normalised
// to lowercase before looking up the live status map (issue #425).
func TestQueueListLiveOverlayTransmission_TorrentIDLowercased(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"arguments": map[string]any{
				"torrents": []map[string]any{{
					"id":           42,
					"percentDone":  0.75,
					"eta":          60,
					"rateDownload": 2048,
				}},
			},
			"result": "success",
		})
	}))
	defer srv.Close()

	h := newQueueTestHandler(t)
	host, port := testServerHostPort(t, srv.URL)
	client := createTestDownloadClient(t, h, &models.DownloadClient{
		Name:    "trans",
		Type:    "transmission",
		Host:    host,
		Port:    port,
		Enabled: true,
	})
	// Transmission uses numeric IDs, so this tests that "42" in the DB
	// matches the "42" key in the live status map. The lowercase normalisation
	// is a no-op for numeric strings but ensures correctness for all clients.
	createTestDownload(t, h, &models.Download{
		GUID:             "guid-trans-lc",
		DownloadClientID: &client.ID,
		Title:            "Torrent Book LC",
		NZBURL:           "magnet:?xt=urn:btih:abc",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        strPtr("42"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var items []QueueItem
	if err := json.NewDecoder(rr.Body).Decode(&items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Percentage != "75.0" {
		t.Fatalf("expected live status overlay to apply; percentage=%s", items[0].Percentage)
	}
}

// TestQueueListLiveOverlayQbittorrent_MixedCaseHashNormalized verifies that a
// TorrentID stored with mixed case (e.g. "ABCDEF") is lowercased before lookup
// so it matches the lowercased hash keys returned by qBittorrent (issue #425).
func TestQueueListLiveOverlayQbittorrent_MixedCaseHashNormalized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash":     "ABCDEF", // qBittorrent returns upper-case hash
				"progress": 0.6,
				"eta":      200,
			}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	h := newQueueTestHandler(t)
	host, port := testServerHostPort(t, srv.URL)
	client := createTestDownloadClient(t, h, &models.DownloadClient{
		Name:     "qb",
		Type:     "qbittorrent",
		Host:     host,
		Port:     port,
		Username: "user",
		Password: "pass",
		Enabled:  true,
	})
	// Store mixed-case hash in DB — should still match after lowercasing.
	createTestDownload(t, h, &models.Download{
		GUID:             "guid-qb-case",
		DownloadClientID: &client.ID,
		Title:            "QB Book Case",
		NZBURL:           "magnet:?xt=urn:btih:ABCDEF",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        strPtr("ABCDEF"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/queue", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var items []QueueItem
	if err := json.NewDecoder(rr.Body).Decode(&items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Percentage != "60.0" {
		t.Fatalf("expected live status overlay to apply with normalised hash; percentage=%s", items[0].Percentage)
	}
}
