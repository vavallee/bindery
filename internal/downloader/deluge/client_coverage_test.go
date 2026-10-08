package deluge_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/deluge"
)

// rpcStub is a per-method Deluge JSON-RPC stub for the paths the stateful
// delugeServer does not model: config reads, label options, HTTP-level
// failures and the 401 re-login retry. A handler returns either a result or
// an RPC error message; status, when set, overrides the HTTP status for a
// method before any RPC body is written.
type rpcStub struct {
	mu       sync.Mutex
	handlers map[string]func(params []json.RawMessage) (any, string)
	status   func(method string, n int) int
	calls    map[string]int
	params   map[string][]json.RawMessage
}

func newRPCStub(t *testing.T) (*rpcStub, *deluge.Client) {
	t.Helper()
	s := &rpcStub{
		handlers: map[string]func([]json.RawMessage) (any, string){
			"auth.login":    func([]json.RawMessage) (any, string) { return true, "" },
			"web.connected": func([]json.RawMessage) (any, string) { return true, "" },
		},
		calls:  map[string]int{},
		params: map[string][]json.RawMessage{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
			ID     int64             `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.calls[req.Method]++
		n := s.calls[req.Method]
		s.params[req.Method] = req.Params
		h := s.handlers[req.Method]
		status := s.status
		s.mu.Unlock()
		if status != nil {
			if code := status(req.Method, n); code != 0 {
				w.WriteHeader(code)
				_, _ = w.Write([]byte("stub says no"))
				return
			}
		}
		if h == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"code": -1, "message": "Unknown method"}, "id": req.ID})
			return
		}
		result, errMsg := h(req.Params)
		if errMsg != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"code": -1, "message": errMsg}, "id": req.ID})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": req.ID})
	}))
	t.Cleanup(srv.Close)
	return s, clientFromServer(srv, "pw")
}

func (s *rpcStub) on(method string, h func([]json.RawMessage) (any, string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

func (s *rpcStub) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

func (s *rpcStub) lastParams(method string) []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.params[method]
}

func result(v any) func([]json.RawMessage) (any, string) {
	return func([]json.RawMessage) (any, string) { return v, "" }
}

func rpcFail(msg string) func([]json.RawMessage) (any, string) {
	return func([]json.RawMessage) (any, string) { return nil, msg }
}

func TestDownloadLocation_Precedence(t *testing.T) {
	cfg := func(moveCompleted bool, movePath string) map[string]any {
		return map[string]any{
			"download_location":   " /downloads/incoming ",
			"move_completed":      moveCompleted,
			"move_completed_path": movePath,
		}
	}
	tests := []struct {
		name       string
		config     map[string]any
		label      string
		options    func([]json.RawMessage) (any, string)
		wantPath   string
		wantSource string
		wantNote   bool
	}{
		{
			name:       "download location when move completed is off",
			config:     cfg(false, "/downloads/done"),
			wantPath:   "/downloads/incoming",
			wantSource: "the client default download location",
		},
		{
			name:       "global move completed path when on",
			config:     cfg(true, "/downloads/done"),
			wantPath:   "/downloads/done",
			wantSource: "the client default move completed path",
		},
		{
			name:       "move completed on but blank path keeps download location",
			config:     cfg(true, "  "),
			wantPath:   "/downloads/incoming",
			wantSource: "the client default download location",
		},
		{
			name:   "label move path wins",
			config: cfg(true, "/downloads/done"),
			label:  "Books",
			options: result(map[string]any{
				"apply_move_completed": true, "move_completed": true, "move_completed_path": "/books/done",
			}),
			wantPath:   "/books/done",
			wantSource: `the move completed path of the label "books"`,
		},
		{
			name:   "label without apply_move_completed is ignored",
			config: cfg(false, ""),
			label:  "books",
			options: result(map[string]any{
				"apply_move_completed": false, "move_completed": true, "move_completed_path": "/books/done",
			}),
			wantPath:   "/downloads/incoming",
			wantSource: "the client default download location",
		},
		{
			name:       "unreadable label options add a note",
			config:     cfg(false, ""),
			label:      "books",
			options:    rpcFail("Unknown Label"),
			wantPath:   "/downloads/incoming",
			wantSource: "the client default download location",
			wantNote:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub, c := newRPCStub(t)
			stub.on("core.get_config_values", result(tt.config))
			if tt.options != nil {
				stub.on("label.get_options", tt.options)
			}
			loc, err := c.DownloadLocation(context.Background(), tt.label)
			if err != nil {
				t.Fatalf("DownloadLocation: %v", err)
			}
			if loc.Path != tt.wantPath || loc.Source != tt.wantSource {
				t.Errorf("got (%q, %q), want (%q, %q)", loc.Path, loc.Source, tt.wantPath, tt.wantSource)
			}
			if (loc.Note != "") != tt.wantNote || (loc.NoteFix != "") != tt.wantNote {
				t.Errorf("note = %q / %q, want note=%v", loc.Note, loc.NoteFix, tt.wantNote)
			}
			if tt.label == "" && stub.count("label.get_options") != 0 {
				t.Error("label.get_options must not be asked without a label")
			}
			if tt.label != "" {
				p := stub.lastParams("label.get_options")
				var sent string
				if len(p) == 1 {
					_ = json.Unmarshal(p[0], &sent)
				}
				if sent != deluge.LabelID(tt.label) {
					t.Errorf("label.get_options sent %q, want the lowercased id %q", sent, deluge.LabelID(tt.label))
				}
			}
			// Only the three keys are requested, never the whole config.
			p := stub.lastParams("core.get_config_values")
			var keys []string
			if len(p) == 1 {
				_ = json.Unmarshal(p[0], &keys)
			}
			if strings.Join(keys, ",") != "download_location,move_completed,move_completed_path" {
				t.Errorf("requested keys = %v", keys)
			}
		})
	}
}

func TestDownloadLocation_ConfigErrorIsReturned(t *testing.T) {
	stub, c := newRPCStub(t)
	stub.on("core.get_config_values", rpcFail("daemon gone"))
	_, err := c.DownloadLocation(context.Background(), "books")
	if err == nil || !strings.Contains(err.Error(), "read deluge download location") || !strings.Contains(err.Error(), "daemon gone") {
		t.Fatalf("err = %v", err)
	}
}

func TestLabels_NullReplyIsAnError(t *testing.T) {
	stub, c := newRPCStub(t)
	stub.on("label.get_labels", result(nil))
	if _, err := c.Labels(context.Background()); err == nil || !strings.Contains(err.Error(), "no label list") {
		t.Fatalf("err = %v, want a no-label-list error", err)
	}
}

// TestConnectDaemon_HostLabels covers how ambiguous hosts are named: the
// address and port when known, the address alone without a port, the opaque
// id when there is no address. Rows with no usable id are skipped.
func TestConnectDaemon_HostLabels(t *testing.T) {
	stub, c := newRPCStub(t)
	stub.on("web.connected", result(false))
	stub.on("web.get_hosts", result([]any{
		[]any{},
		[]any{""},
		[]any{"id-only"},
		[]any{"id-b", "host-b"},
		[]any{"id-c", "host-c", 0},
		[]any{"id-d", "host-d", 58846, "user", "extra"},
	}))
	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected an ambiguity error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "4 hosts are configured (id-only, host-b, host-c, host-d:58846)") {
		t.Errorf("unexpected host listing: %q", msg)
	}
	if stub.count("web.connect") != 0 {
		t.Error("web.connect fired despite the ambiguity")
	}
}

func TestConnectDaemon_RPCFailures(t *testing.T) {
	t.Run("web.connected fails", func(t *testing.T) {
		stub, c := newRPCStub(t)
		stub.on("web.connected", rpcFail("boom"))
		err := c.Login(context.Background())
		if err == nil || !strings.Contains(err.Error(), "could not check whether the Web UI has a daemon") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("web.get_hosts fails", func(t *testing.T) {
		stub, c := newRPCStub(t)
		stub.on("web.connected", result(false))
		stub.on("web.get_hosts", rpcFail("boom"))
		err := c.Login(context.Background())
		if err == nil || !strings.Contains(err.Error(), "could not list the configured daemon hosts") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("login rpc error", func(t *testing.T) {
		stub, c := newRPCStub(t)
		stub.on("auth.login", rpcFail("auth backend down"))
		err := c.Test(context.Background())
		if err == nil || !strings.Contains(err.Error(), "could not reach Deluge") || !strings.Contains(err.Error(), "auth backend down") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestCall_ReloginOn401 pins the session-expiry recovery: a 401 on an
// authenticated call logs in again and retries once.
func TestCall_ReloginOn401(t *testing.T) {
	stub, c := newRPCStub(t)
	stub.on("core.get_torrents_status", result(map[string]any{
		"ABCDEF": map[string]any{"name": "Book", "state": "Seeding"},
	}))
	stub.status = func(method string, n int) int {
		if method == "core.get_torrents_status" && n == 1 {
			return http.StatusUnauthorized
		}
		return 0
	}
	got, err := c.GetTorrents(context.Background())
	if err != nil {
		t.Fatalf("GetTorrents after a 401: %v", err)
	}
	if got["abcdef"].Name != "Book" {
		t.Errorf("torrents = %+v, want the lower-cased hash key", got)
	}
	if n := stub.count("auth.login"); n != 2 {
		t.Errorf("auth.login calls = %d, want 2 (initial + re-login)", n)
	}
	if n := stub.count("core.get_torrents_status"); n != 2 {
		t.Errorf("status calls = %d, want 2 (failed + retried)", n)
	}
}

func TestCall_401Failures(t *testing.T) {
	t.Run("relogin fails", func(t *testing.T) {
		stub, c := newRPCStub(t)
		stub.status = func(method string, n int) int {
			if method == "core.get_torrents_status" {
				return http.StatusUnauthorized
			}
			if method == "auth.login" && n > 1 {
				return http.StatusInternalServerError
			}
			return 0
		}
		_, err := c.GetTorrents(context.Background())
		if err == nil || !strings.Contains(err.Error(), "deluge login") {
			t.Fatalf("err = %v, want the re-login failure", err)
		}
	})
	t.Run("retry also 401", func(t *testing.T) {
		stub, c := newRPCStub(t)
		stub.status = func(method string, _ int) int {
			if method == "core.get_torrents_status" {
				return http.StatusUnauthorized
			}
			return 0
		}
		_, err := c.GetTorrents(context.Background())
		if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
			t.Fatalf("err = %v, want HTTP 401", err)
		}
		if n := stub.count("core.get_torrents_status"); n != 2 {
			t.Errorf("status calls = %d, want exactly one retry", n)
		}
	})
	t.Run("unauthenticated call is not retried", func(t *testing.T) {
		stub, c := newRPCStub(t)
		stub.status = func(method string, _ int) int {
			if method == "auth.login" {
				return http.StatusUnauthorized
			}
			return 0
		}
		err := c.Login(context.Background())
		if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
			t.Fatalf("err = %v", err)
		}
		if n := stub.count("auth.login"); n != 1 {
			t.Errorf("auth.login calls = %d, want 1", n)
		}
	})
}

func TestDoCall_MalformedReplies(t *testing.T) {
	t.Run("non-200 carries the body", func(t *testing.T) {
		stub, c := newRPCStub(t)
		stub.status = func(method string, _ int) int {
			if method == "core.get_torrents_status" {
				return http.StatusBadGateway
			}
			return 0
		}
		_, err := c.GetTorrents(context.Background())
		if err == nil || !strings.Contains(err.Error(), "HTTP 502") || !strings.Contains(err.Error(), "stub says no") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("result of the wrong shape", func(t *testing.T) {
		stub, c := newRPCStub(t)
		stub.on("core.get_torrents_status", result([]int{1, 2}))
		_, err := c.GetTorrents(context.Background())
		if err == nil || !strings.Contains(err.Error(), "decode result for core.get_torrents_status") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("body that is not JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html>login page</html>"))
		}))
		t.Cleanup(srv.Close)
		c := clientFromServer(srv, "pw")
		err := c.Login(context.Background())
		if err == nil || !strings.Contains(err.Error(), "decode rpc response") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		_, c := newRPCStub(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := c.Login(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

func TestRemoveTorrent_LoginFailureStopsBeforeRemoval(t *testing.T) {
	stub, c := newRPCStub(t)
	stub.on("auth.login", result(false))
	err := c.RemoveTorrent(context.Background(), "abc", true)
	if err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Fatalf("err = %v", err)
	}
	if stub.count("core.remove_torrent") != 0 {
		t.Error("core.remove_torrent was called without a session")
	}
}

func TestRemoveTorrent_RPCErrorIsWrapped(t *testing.T) {
	stub, c := newRPCStub(t)
	stub.on("core.remove_torrent", rpcFail("KeyError"))
	err := c.RemoveTorrent(context.Background(), "abc", false)
	if err == nil || !strings.Contains(err.Error(), "remove torrent: rpc error -1: KeyError") {
		t.Fatalf("err = %v", err)
	}
}

// TestAddTorrent_FetchFailures covers the indexer side of a .torrent grab:
// every failure stops before Deluge is asked to add anything.
func TestAddTorrent_FetchFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		path    string
		want    string
	}{
		{
			name:    "indexer error status",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
			want:    "indexer returned HTTP 404",
		},
		{
			name:    "empty body",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
			want:    "empty torrent response",
		},
		{
			name:    "redirect without location",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusFound) },
			want:    "redirect without location",
		},
		{
			name: "redirect to an unsupported scheme",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "ftp://example.invalid/x.torrent")
				w.WriteHeader(http.StatusFound)
			},
			want: `unsupported redirect scheme "ftp"`,
		},
		{
			name: "redirect loop",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, r.URL.Path, http.StatusFound)
			},
			want: "too many redirects",
		},
		{
			// net/http parses Location before handing the response back, so
			// an unparseable one surfaces as the client's own error.
			name: "unparseable redirect location",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "http://[::1")
				w.WriteHeader(http.StatusFound)
			},
			want: "failed to parse Location header",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			indexer := httptest.NewServer(tt.handler)
			t.Cleanup(indexer.Close)
			stub, c := newRPCStub(t)
			c.SetValidateTorrentURL(func(string) error { return nil })
			_, err := c.AddTorrent(context.Background(), indexer.URL+"/get.torrent", "", nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
			if !strings.HasPrefix(err.Error(), "fetch torrent:") {
				t.Errorf("err = %q, want the fetch torrent prefix", err)
			}
			if stub.count("core.add_torrent_file") != 0 {
				t.Error("core.add_torrent_file was called after a failed fetch")
			}
		})
	}
}

// TestAddTorrent_RelativeRedirectFollowed: a relative Location is resolved
// against the request URL and every hop is validated.
func TestAddTorrent_RelativeRedirectFollowed(t *testing.T) {
	torrent := []byte("d4:infod4:name4:a.epe")
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			w.Header().Set("Location", "/final.torrent")
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		_, _ = w.Write(torrent)
	}))
	t.Cleanup(indexer.Close)
	stub, c := newRPCStub(t)
	stub.on("core.add_torrent_file", result("ABCDEF0123"))
	var validated []string
	c.SetValidateTorrentURL(func(u string) error {
		validated = append(validated, u)
		return nil
	})
	hash, err := c.AddTorrent(context.Background(), indexer.URL+"/start", "", nil)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if hash != "abcdef0123" {
		t.Errorf("hash = %q, want the lower-cased reply", hash)
	}
	if len(validated) != 2 || !strings.HasSuffix(validated[1], "/final.torrent") {
		t.Errorf("validated hops = %v, want the start URL and the resolved redirect", validated)
	}
}

func TestAddTorrent_ValidatorRejection(t *testing.T) {
	t.Run("injected validator", func(t *testing.T) {
		stub, c := newRPCStub(t)
		c.SetValidateTorrentURL(func(string) error { return errors.New("blocked by policy") })
		_, err := c.AddTorrent(context.Background(), "http://indexer.example/x.torrent?apikey=secret", "", nil)
		if err == nil || !strings.Contains(err.Error(), "blocked by policy") {
			t.Fatalf("err = %v", err)
		}
		if stub.count("core.add_torrent_file") != 0 {
			t.Error("Deluge was asked to add a torrent whose URL was refused")
		}
	})
	t.Run("default policy refuses a link-local target", func(t *testing.T) {
		stub, c := newRPCStub(t)
		_, err := c.AddTorrent(context.Background(), "http://169.254.169.254/latest/meta-data", "", nil)
		if err == nil || !strings.Contains(err.Error(), "fetch torrent") {
			t.Fatalf("err = %v, want the SSRF policy to refuse the metadata address", err)
		}
		if stub.count("core.add_torrent_file") != 0 {
			t.Error("Deluge was asked to add a torrent whose URL was refused")
		}
	})
}

func TestAddTorrent_TorrentFileRPCError(t *testing.T) {
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("d4:infod4:name4:a.epe"))
	}))
	t.Cleanup(indexer.Close)
	stub, c := newRPCStub(t)
	stub.on("core.add_torrent_file", rpcFail("invalid torrent"))
	c.SetValidateTorrentURL(func(string) error { return nil })
	_, err := c.AddTorrent(context.Background(), indexer.URL+"/a.torrent", "", nil)
	if err == nil || !strings.Contains(err.Error(), "core.add_torrent_file: rpc error -1: invalid torrent") {
		t.Fatalf("err = %v", err)
	}
	if stub.count("core.get_torrents_status") != 0 {
		t.Error("a non-duplicate refusal must not look for an existing torrent")
	}
}
