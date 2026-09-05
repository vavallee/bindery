package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

func downloadClientFixture(t *testing.T) (*DownloadClientHandler, *db.DownloadClientRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	clients := db.NewDownloadClientRepo(database)
	return NewDownloadClientHandler(clients), clients
}

func TestDownloadClientList_Empty(t *testing.T) {
	h, _ := downloadClientFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/downloadclient", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out []models.DownloadClient
	json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 0 {
		t.Errorf("expected empty list, got %d items", len(out))
	}
}

func TestDownloadClientCRUD(t *testing.T) {
	h, clients := downloadClientFixture(t)
	ctx := context.Background()

	// Create — valid. Use RFC1918 IP literal so the SSRF validator's LAN
	// policy accepts it without needing DNS in the test environment.
	body := `{"name":"My SAB","host":"10.10.10.10","port":8080,"type":"sabnzbd","apiKey":"key1","pathRemap":"/remote:/local","enabled":true}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/downloadclient", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.DownloadClient
	json.NewDecoder(rec.Body).Decode(&created)
	if created.ID == 0 {
		t.Fatal("expected non-zero ID after create")
	}
	if created.PathRemap != "/remote:/local" {
		t.Fatalf("pathRemap = %q", created.PathRemap)
	}

	// List — should have one entry
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/downloadclient", nil))
	var list []models.DownloadClient
	json.NewDecoder(rec.Body).Decode(&list)
	if len(list) != 1 {
		t.Errorf("expected 1 client, got %d", len(list))
	}

	// Get by ID
	idStr := "1"
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/downloadclient/1", nil), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", rec.Code)
	}

	// Get — not found
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/downloadclient/999", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("get missing: expected 404, got %d", rec.Code)
	}

	// Update
	update := `{"name":"Updated SAB","host":"10.10.10.11","port":8080,"type":"sabnzbd","apiKey":"key2","pathRemap":"/remote2:/local2","enabled":false}`
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/downloadclient/1", bytes.NewBufferString(update)), "id", idStr))
	if rec.Code != http.StatusOK {
		t.Errorf("update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := clients.GetByID(ctx, created.ID)
	if got == nil {
		t.Fatal("expected client still exists after update")
		return
	}
	if got.PathRemap != "/remote2:/local2" {
		t.Errorf("updated pathRemap = %q", got.PathRemap)
	}

	// Delete
	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/downloadclient/1", nil), "id", idStr))
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", rec.Code)
	}
}

func TestDownloadClientCreate_Validation(t *testing.T) {
	h, _ := downloadClientFixture(t)
	for _, body := range []string{
		`{}`,
		`{"name":"x"}`,         // missing host
		`{"host":"localhost"}`, // missing name
		`not-json`,
	} {
		rec := httptest.NewRecorder()
		h.Create(rec, httptest.NewRequest(http.MethodPost, "/downloadclient", bytes.NewBufferString(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: expected 400, got %d", body, rec.Code)
		}
	}
}

func TestDownloadClientCreate_Defaults(t *testing.T) {
	h, clients := downloadClientFixture(t)
	ctx := context.Background()
	body := `{"name":"SAB","host":"10.10.10.10"}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/downloadclient", bytes.NewBufferString(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	var created models.DownloadClient
	json.NewDecoder(rec.Body).Decode(&created)
	got, _ := clients.GetByID(ctx, created.ID)
	if got.Type != "sabnzbd" {
		t.Errorf("default type: want sabnzbd, got %q", got.Type)
	}
	if got.Port != 8080 {
		t.Errorf("default port: want 8080, got %d", got.Port)
	}
	if got.Category != "books" {
		t.Errorf("default category: want books, got %q", got.Category)
	}
}

func TestDownloadClientTest_NotFound(t *testing.T) {
	h, _ := downloadClientFixture(t)
	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/999/test", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing client, got %d", rec.Code)
	}
}

func TestDownloadClientTest_SuccessMessage(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	qbit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("5.1.4"))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer qbit.Close()
	u, err := url.Parse(qbit.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	h, clients := downloadClientFixture(t)
	client := &models.DownloadClient{
		Name:     "qBit",
		Type:     "qbittorrent",
		Host:     host,
		Port:     port,
		Username: "u",
		Password: "p",
		Enabled:  true,
	}
	if err := clients.Create(context.Background(), client); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/1/test", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Message string                       `json:"message"`
		Health  *models.DownloadClientHealth `json:"health"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Message != "Connection verified" {
		t.Errorf("message: want Connection verified, got %q", out.Message)
	}
}

// TestDownloadClientTest_PathVisibility exercises #1182: after a successful
// connection, Test resolves the qBittorrent category save path, applies the
// client's PathRemap, and reports whether Bindery can read it. A reachable
// remapped path is reported "ok"; a missing one is reported "warning" with an
// actionable message, without turning the whole Test into a hard failure.
func TestDownloadClientTest_PathVisibility(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	visible := t.TempDir()

	newQbitStub := func(savePath string) (string, int, func()) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v2/auth/login":
				_, _ = w.Write([]byte("Ok."))
			case "/api/v2/app/version":
				_, _ = w.Write([]byte("5.1.4"))
			case "/api/v2/torrents/info":
				_, _ = w.Write([]byte("[]"))
			case "/api/v2/torrents/categories":
				_, _ = w.Write([]byte(`{"books":{"name":"books","savePath":"` + savePath + `"}}`))
			case "/api/v2/app/defaultSavePath":
				_, _ = w.Write([]byte(""))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		u, _ := url.Parse(srv.URL)
		host, portStr, _ := net.SplitHostPort(u.Host)
		port, _ := strconv.Atoi(portStr)
		return host, port, srv.Close
	}

	type pathVis struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Path    string `json:"path"`
	}

	t.Run("visible path reports ok", func(t *testing.T) {
		host, port, closeFn := newQbitStub("/remote/downloads")
		defer closeFn()
		h, clients := downloadClientFixture(t)
		h.WithStoragePaths(visible, "")
		client := &models.DownloadClient{
			Name: "qBit", Type: "qbittorrent", Host: host, Port: port,
			Username: "u", Password: "p", Category: "books",
			PathRemap: "/remote/downloads:" + visible, Enabled: true,
		}
		if err := clients.Create(context.Background(), client); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/1/test", nil), "id", "1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Message        string   `json:"message"`
			PathVisibility *pathVis `json:"pathVisibility"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.PathVisibility == nil {
			t.Fatal("expected pathVisibility in response")
		}
		if out.PathVisibility.Status != "ok" {
			t.Fatalf("status: want ok, got %q (%s)", out.PathVisibility.Status, out.PathVisibility.Message)
		}
	})

	t.Run("missing path reports warning", func(t *testing.T) {
		host, port, closeFn := newQbitStub("/remote/downloads")
		defer closeFn()
		h, clients := downloadClientFixture(t)
		h.WithStoragePaths(visible, "")
		client := &models.DownloadClient{
			Name: "qBit", Type: "qbittorrent", Host: host, Port: port,
			Username: "u", Password: "p", Category: "books",
			PathRemap: "/remote/downloads:/nope/not/here", Enabled: true,
		}
		if err := clients.Create(context.Background(), client); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/1/test", nil), "id", "1"))
		// Still a 200: the connection succeeded; the warning is in the body.
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Message        string   `json:"message"`
			PathVisibility *pathVis `json:"pathVisibility"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.PathVisibility == nil {
			t.Fatal("expected pathVisibility in response")
		}
		if out.PathVisibility.Status != "warning" {
			t.Fatalf("status: want warning, got %q (%s)", out.PathVisibility.Status, out.PathVisibility.Message)
		}
		if !strings.Contains(out.PathVisibility.Message, "can't read") {
			t.Errorf("warning message lacks actionable text: %q", out.PathVisibility.Message)
		}
	})
}

func TestDownloadClientTestConfig_MissingHost(t *testing.T) {
	h, _ := downloadClientFixture(t)
	rec := httptest.NewRecorder()
	h.TestConfig(rec, httptest.NewRequest(http.MethodPost, "/downloadclient/test", bytes.NewBufferString(`{"type":"qbittorrent"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing host, got %d", rec.Code)
	}
}

func TestDownloadClientTestConfig_Reachable(t *testing.T) {
	// httptest binds 127.0.0.1; allow loopback through the SSRF guard.
	defer httpsec.AllowLoopbackForTests()()
	// A reachable qBittorrent stub. The posted config is probed but never
	// persisted — the repo stays empty afterwards.
	qbit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("5.1.4"))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer qbit.Close()
	u, _ := url.Parse(qbit.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)

	h, clients := downloadClientFixture(t)
	rec := httptest.NewRecorder()
	body := `{"name":"qBit","type":"qbittorrent","host":"` + host + `","port":` + strconv.Itoa(port) + `,"username":"u","password":"p","enabled":true}`
	h.TestConfig(rec, httptest.NewRequest(http.MethodPost, "/downloadclient/test", bytes.NewBufferString(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Message != "Connection verified" {
		t.Errorf("message: want Connection verified, got %q", out.Message)
	}
	list, _ := clients.List(context.Background())
	if len(list) != 0 {
		t.Errorf("test-by-config must not persist; repo has %d clients", len(list))
	}
}

func TestDownloadClientTestConfig_Unreachable(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	// Start a stub server to grab a real local address, then close it so the
	// connection is refused immediately (fast + deterministic, no 15s timeout).
	stub := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	u, _ := url.Parse(stub.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)
	stub.Close()

	h, _ := downloadClientFixture(t)
	rec := httptest.NewRecorder()
	body := `{"name":"qBit","type":"qbittorrent","host":"` + host + `","port":` + strconv.Itoa(port) + `,"username":"u","password":"p","enabled":true}`
	h.TestConfig(rec, httptest.NewRequest(http.MethodPost, "/downloadclient/test", bytes.NewBufferString(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unreachable client, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Error == "" {
		t.Error("expected a non-empty error message")
	}
}

// TestDownloadClientHandler_LifetimeCtxFallsBackToBackground is the #846
// follow-up guard for the async health-probe goroutine spawned by Create/Update.
func TestDownloadClientHandler_LifetimeCtxFallsBackToBackground(t *testing.T) {
	h := &DownloadClientHandler{}
	if h.bgCtx() != context.Background() {
		t.Error("bgCtx without WithLifetimeCtx must return context.Background()")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.WithLifetimeCtx(ctx)
	if h.bgCtx() != ctx {
		t.Error("bgCtx with WithLifetimeCtx must return the supplied ctx")
	}
	h.WithLifetimeCtx(nil) //nolint:staticcheck // SA1012 testing nil-tolerance contract
	if h.bgCtx() != ctx {
		t.Error("WithLifetimeCtx(nil) must not clobber a previously installed ctx")
	}
}

// TestDownloadClientCreate_RejectsMalformedHost covers #2203: a Host copied
// out of a browser address bar was stored whole, interpolated into
// "http://<host>:<port>/" and then quietly reached the client's web UI
// instead of its API. Every rejection has to name the field to fix.
func TestDownloadClientCreate_RejectsMalformedHost(t *testing.T) {
	for _, tc := range []struct {
		name     string
		host     string
		contains []string
	}{
		// The exact value from the report.
		{"address bar paste", "10.1.2.3:8080/#/", []string{"a port and a path", "8080 as the port"}},
		{"embedded port", "10.1.2.3:9091", []string{"a port", "9091 as the port"}},
		{"trailing path", "10.1.2.3/qbittorrent", []string{"a path"}},
		{"fragment", "10.1.2.3#/downloads", []string{"a fragment"}},
		{"credentials", "admin:pw@10.1.2.3", []string{"Username and Password"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, clients := downloadClientFixture(t)
			body, err := json.Marshal(map[string]any{"name": "qBit", "type": "qbittorrent", "host": tc.host, "port": 8080})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			h.Create(rec, httptest.NewRequest(http.MethodPost, "/downloadclient", bytes.NewReader(body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
			for _, want := range tc.contains {
				if !strings.Contains(rec.Body.String(), want) {
					t.Errorf("response %s does not mention %q", rec.Body.String(), want)
				}
			}
			list, err := clients.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 0 {
				t.Errorf("rejected client was saved anyway: %+v", list)
			}
		})
	}
}

// TestDownloadClientCreate_AcceptsPlainHosts guards the other direction: the
// host forms a working install already holds must still save, with only the
// scheme prefix and a lone trailing slash removed.
func TestDownloadClientCreate_AcceptsPlainHosts(t *testing.T) {
	for _, tc := range []struct{ name, host, want string }{
		{"ip", "10.1.2.3", "10.1.2.3"},
		{"http prefix", "http://10.1.2.3", "10.1.2.3"},
		{"https prefix", "https://10.1.2.3", "10.1.2.3"},
		{"trailing slash", "10.1.2.3/", "10.1.2.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, clients := downloadClientFixture(t)
			body, err := json.Marshal(map[string]any{"name": "qBit", "type": "qbittorrent", "host": tc.host, "port": 8080})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			h.Create(rec, httptest.NewRequest(http.MethodPost, "/downloadclient", bytes.NewReader(body)))
			if rec.Code != http.StatusCreated {
				t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
			}
			list, err := clients.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 || list[0].Host != tc.want {
				t.Fatalf("stored host = %+v, want %q", list, tc.want)
			}
		})
	}
}

func TestDownloadClientUpdate_RejectsMalformedHost(t *testing.T) {
	h, clients := downloadClientFixture(t)
	ctx := context.Background()
	client := &models.DownloadClient{Name: "qBit", Type: "qbittorrent", Host: "10.1.2.3", Port: 8080, Enabled: true}
	if err := clients.Create(ctx, client); err != nil {
		t.Fatal(err)
	}

	body := `{"name":"qBit","type":"qbittorrent","host":"10.1.2.3:8080/#/","port":8080}`
	rec := httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/downloadclient/1", bytes.NewBufferString(body)), "id", "1"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := clients.GetByID(ctx, client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "10.1.2.3" {
		t.Errorf("host was overwritten with a rejected value: %q", got.Host)
	}
}

// TestDownloadClientTest_RejectsSavedBadHost is the reporter's other
// complaint: the client had already been saved (before this validation
// existed), so Test is the only place left that can tell them what is wrong.
// It used to report "Connection verified" because the malformed URL still
// reached qBittorrent's web UI.
func TestDownloadClientTest_RejectsSavedBadHost(t *testing.T) {
	h, clients := downloadClientFixture(t)
	// Written through the repo, not the handler, so it stands in for a row
	// saved by an earlier version.
	client := &models.DownloadClient{Name: "qBit", Type: "qbittorrent", Host: "10.1.2.3:8080/#/", Port: 8080, Enabled: true}
	if err := clients.Create(context.Background(), client); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Test(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/downloadclient/1/test", nil), "id", "1"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "8080 as the port") {
		t.Errorf("response %s does not say which field to fix", rec.Body.String())
	}
}

// TestDownloadClientTestConfig_RejectsMalformedHost covers the inline Test
// button on the add/edit form, which probes an unsaved configuration.
func TestDownloadClientTestConfig_RejectsMalformedHost(t *testing.T) {
	h, _ := downloadClientFixture(t)
	body := `{"name":"qBit","type":"qbittorrent","host":"10.1.2.3:8080/#/","port":8080}`
	rec := httptest.NewRecorder()
	h.TestConfig(rec, httptest.NewRequest(http.MethodPost, "/downloadclient/test", bytes.NewBufferString(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hostname or IP address only") {
		t.Errorf("response %s does not explain the host", rec.Body.String())
	}
}

// TestDownloadClientURL_IPv6 checks the SSRF pre-check URL is well formed for
// both spellings of an IPv6 literal the Host field accepts. Before #2203 the
// bare form produced "http://::1:8080/", which is not a URL at all.
func TestDownloadClientURL_IPv6(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"::1", "http://[::1]:8080/"},
		{"[::1]", "http://[::1]:8080/"},
		{"10.1.2.3", "http://10.1.2.3:8080/"},
	} {
		got := downloadClientURL(&models.DownloadClient{Host: tc.host, Port: 8080})
		if got != tc.want {
			t.Errorf("downloadClientURL(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

// TestDownloadClientHandlers_NonNumericID pins that a malformed {id} answers
// 400 on every handler that takes one. All four used to run
// `id, _ := strconv.ParseInt(...)`, so "abc" became id 0. Get/Update/Test then
// reported "download client not found" for a request that never named a
// client, and Delete was worse: it ran the repo delete, downloader.Evict(0) and
// the health drop against id 0, then answered 204 No Content, so a typo in a
// script reported success for a delete that deleted nothing (#2364).
func TestDownloadClientHandlers_NonNumericID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		call   func(h *DownloadClientHandler, w http.ResponseWriter, r *http.Request)
	}{
		{"Get", http.MethodGet, (*DownloadClientHandler).Get},
		{"Update", http.MethodPut, (*DownloadClientHandler).Update},
		{"Delete", http.MethodDelete, (*DownloadClientHandler).Delete},
		{"Test", http.MethodPost, (*DownloadClientHandler).Test},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, clients := downloadClientFixture(t)

			// A real client at id 1, so a 404 here would mean the handler
			// looked something up rather than rejecting the id.
			existing := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", Host: "127.0.0.1", Port: 8080, Category: "books"}
			if err := clients.Create(context.Background(), existing); err != nil {
				t.Fatalf("seed client: %v", err)
			}

			rec := httptest.NewRecorder()
			req := withURLParam(httptest.NewRequest(tc.method, "/downloadclient/abc", bytes.NewBufferString(`{}`)), "id", "abc")

			tc.call(h, rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for a non-numeric id, got %d: %s", rec.Code, rec.Body.String())
			}
			var out map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if out["error"] != "invalid id" {
				t.Errorf("error = %q, want %q", out["error"], "invalid id")
			}

			// The seeded client must still be there: a rejected id must not
			// have reached the repo at all.
			still, err := clients.GetByID(context.Background(), existing.ID)
			if err != nil || still == nil {
				t.Errorf("seeded client should survive a malformed-id request (err=%v)", err)
			}
		})
	}
}
