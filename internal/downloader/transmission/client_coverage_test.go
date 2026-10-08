package transmission

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
)

// rpcStub answers each Transmission RPC method with a fixed status and body
// and records the decoded arguments, so a test can assert both the request
// Bindery sent and how it read the reply.
type rpcStub struct {
	mu      sync.Mutex
	replies map[string]stubReply
	args    map[string]map[string]any
	calls   int
}

type stubReply struct {
	status int
	body   string
}

func newRPCStub(t *testing.T, replies map[string]stubReply) (*Client, *rpcStub) {
	t.Helper()
	s := &rpcStub{replies: replies, args: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = newJSONDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.calls++
		s.args[req.Method] = req.Arguments
		reply, ok := s.replies[req.Method]
		s.mu.Unlock()
		if !ok {
			reply = stubReply{body: `{"result":"success","arguments":{}}`}
		}
		if reply.status != 0 {
			w.WriteHeader(reply.status)
		}
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(srv.Close)
	return newTestClient(srv.URL, "u", "p"), s
}

func (s *rpcStub) lastArgs(method string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.args[method]
}

func (s *rpcStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestDownloadDir(t *testing.T) {
	c, stub := newRPCStub(t, map[string]stubReply{
		"session-get": {body: `{"result":"success","arguments":{"download-dir":"  /data/complete  ","peer-port":51413}}`},
	})
	dir, err := c.DownloadDir(context.Background())
	if err != nil {
		t.Fatalf("DownloadDir: %v", err)
	}
	if dir != "/data/complete" {
		t.Errorf("dir = %q, want the trimmed download-dir", dir)
	}
	fields, _ := stub.lastArgs("session-get")["fields"].([]any)
	if len(fields) != 1 || fields[0] != "download-dir" {
		t.Errorf("session-get fields = %v, want only download-dir", fields)
	}
}

func TestDownloadDir_Failures(t *testing.T) {
	tests := []struct {
		name  string
		reply stubReply
		want  string
	}{
		{"refused", stubReply{body: `{"result":"permission denied","arguments":{}}`}, "session-get failed: permission denied"},
		{"not json", stubReply{body: `<html>`}, "decode session-get response"},
		{"http error", stubReply{status: http.StatusUnauthorized, body: "Unauthorized"}, "transmission HTTP 401: Unauthorized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newRPCStub(t, map[string]stubReply{"session-get": tt.reply})
			_, err := c.DownloadDir(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
	t.Run("unconfigured client", func(t *testing.T) {
		c := New("", 9091, "", "", "", false)
		if _, err := c.DownloadDir(context.Background()); err == nil || !strings.Contains(err.Error(), "host is empty") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRemoveTorrentByHash(t *testing.T) {
	c, stub := newRPCStub(t, nil)
	if err := c.RemoveTorrentByHash(context.Background(), "  ABCDEF0123  ", true); err != nil {
		t.Fatalf("RemoveTorrentByHash: %v", err)
	}
	args := stub.lastArgs("torrent-remove")
	ids, _ := args["ids"].([]any)
	if len(ids) != 1 || ids[0] != "abcdef0123" {
		t.Errorf("ids = %v, want the trimmed lowercase hash", ids)
	}
	if args["delete-local-data"] != true {
		t.Errorf("delete-local-data = %v, want true", args["delete-local-data"])
	}

	if err := c.RemoveTorrentByHash(context.Background(), "abc", false); err != nil {
		t.Fatalf("RemoveTorrentByHash without delete: %v", err)
	}
	if _, set := stub.lastArgs("torrent-remove")["delete-local-data"]; set {
		t.Error("delete-local-data sent although deleteFiles was false")
	}
}

func TestRemoveTorrentByHash_EmptyHashSendsNothing(t *testing.T) {
	c, stub := newRPCStub(t, nil)
	err := c.RemoveTorrentByHash(context.Background(), "   ", true)
	if err == nil || !strings.Contains(err.Error(), "empty info hash") {
		t.Fatalf("err = %v", err)
	}
	if stub.callCount() != 0 {
		t.Errorf("an empty hash reached Transmission (%d calls); an empty ids list removes every torrent", stub.callCount())
	}
}

func TestRemoveTorrent_MalformedReply(t *testing.T) {
	c, _ := newRPCStub(t, map[string]stubReply{"torrent-remove": {body: `not json`}})
	err := c.RemoveTorrent(context.Background(), 4, false)
	if err == nil || !strings.Contains(err.Error(), "decode remove torrent response") {
		t.Fatalf("err = %v", err)
	}
}

func TestTorrentRef(t *testing.T) {
	if got := torrentRef(Torrent{ID: 7, HashString: " ABC "}); got != "abc" {
		t.Errorf("with a hash: ref = %v, want the lowercase hash", got)
	}
	if got := torrentRef(Torrent{ID: 7}); got != int64(7) {
		t.Errorf("without a hash: ref = %v (%T), want int64 7", got, got)
	}
}

func TestSetSeedLimits(t *testing.T) {
	ratio := 1.5
	t.Run("zero limits send nothing", func(t *testing.T) {
		c, stub := newRPCStub(t, nil)
		if err := c.SetSeedLimits(context.Background(), "abc", SeedLimits{}); err != nil {
			t.Fatalf("SetSeedLimits: %v", err)
		}
		if stub.callCount() != 0 {
			t.Errorf("zero limits made %d calls", stub.callCount())
		}
	})
	t.Run("numeric ref", func(t *testing.T) {
		c, stub := newRPCStub(t, nil)
		if err := c.SetSeedLimits(context.Background(), int64(9), SeedLimits{Ratio: &ratio}); err != nil {
			t.Fatalf("SetSeedLimits: %v", err)
		}
		args := stub.lastArgs("torrent-set")
		ids, _ := args["ids"].([]any)
		if len(ids) != 1 || ids[0] != float64(9) {
			t.Errorf("ids = %v", ids)
		}
		if args["seedRatioLimit"] != 1.5 || args["seedRatioMode"] != float64(seedRatioModeSingle) {
			t.Errorf("ratio args = %v", args)
		}
	})
	failures := []struct {
		name  string
		reply stubReply
		want  string
	}{
		{"refused", stubReply{body: `{"result":"invalid argument"}`}, "torrent-set failed: invalid argument"},
		{"not json", stubReply{body: `{`}, "decode torrent-set response"},
		{"http error", stubReply{status: http.StatusInternalServerError, body: "oops"}, "transmission HTTP 500"},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newRPCStub(t, map[string]stubReply{"torrent-set": tt.reply})
			err := c.SetSeedLimits(context.Background(), "abc", SeedLimits{Ratio: &ratio})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
	t.Run("unconfigured client", func(t *testing.T) {
		c := New("", 9091, "", "", "", false)
		if err := c.SetSeedLimits(context.Background(), "abc", SeedLimits{Ratio: &ratio}); err == nil {
			t.Fatal("expected the init error")
		}
	})
}

func TestFiles_Failures(t *testing.T) {
	tests := []struct {
		name  string
		reply stubReply
		want  string
	}{
		{"refused", stubReply{body: `{"result":"no such method"}`}, "torrent-get files failed: no such method"},
		{"not json", stubReply{body: `[`}, "decode torrent-get files response"},
		{"http error", stubReply{status: http.StatusBadGateway, body: "bad gateway"}, "transmission HTTP 502"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newRPCStub(t, map[string]stubReply{"torrent-get": tt.reply})
			_, err := c.Files(context.Background(), 1)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
	t.Run("unconfigured client", func(t *testing.T) {
		c := New("", 9091, "", "", "", false)
		if _, err := c.Files(context.Background(), 1); err == nil {
			t.Fatal("expected the init error")
		}
	})
}

// TestAddTorrent_FetchFailures covers the indexer side of an http(s) grab:
// every failure stops before Transmission is asked to add anything.
func TestAddTorrent_FetchFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{
			name: "indexer error carries its body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(" rate limited \n"))
			},
			want: "indexer returned HTTP 403: rate limited",
		},
		{
			name:    "redirect without location",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusSeeOther) },
			want:    "redirect without location",
		},
		{
			name: "redirect to an unsupported scheme",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "file:///etc/passwd")
				w.WriteHeader(http.StatusFound)
			},
			want: `unsupported redirect scheme "file"`,
		},
		{
			name: "redirect loop",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, r.URL.Path, http.StatusTemporaryRedirect)
			},
			want: "too many redirects",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			indexer := httptest.NewServer(tt.handler)
			t.Cleanup(indexer.Close)
			c, stub := newRPCStub(t, nil)
			allowTorrentFetch(c)
			_, err := c.AddTorrent(context.Background(), indexer.URL+"/get.torrent", "", nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if stub.callCount() != 0 {
				t.Error("Transmission was called after a failed fetch")
			}
		})
	}
}

func TestAddTorrent_RelativeRedirectRevalidated(t *testing.T) {
	torrent := "d4:infod4:name4:a.epe"
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dl" {
			w.Header().Set("Location", "/files/a.torrent")
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(torrent))
	}))
	t.Cleanup(indexer.Close)
	c, stub := newRPCStub(t, map[string]stubReply{
		"torrent-add": {body: `{"result":"success","arguments":{"torrent-added":{"id":3,"hashString":"ff"}}}`},
	})
	allowTorrentFetch(c)
	var hops []string
	c.validateTorrentURL = func(u string) error {
		hops = append(hops, u)
		return nil
	}
	id, err := c.AddTorrent(context.Background(), indexer.URL+"/dl", "", nil)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if id != 3 {
		t.Errorf("id = %d, want 3", id)
	}
	if len(hops) != 2 || hops[1] != indexer.URL+"/files/a.torrent" {
		t.Errorf("validated hops = %v, want the resolved redirect re-checked", hops)
	}
	meta, _ := stub.lastArgs("torrent-add")["metainfo"].(string)
	if raw, _ := decodeBase64(meta); string(raw) != torrent {
		t.Errorf("metainfo = %q, want the fetched bytes", raw)
	}
}

func TestAddTorrent_RedirectHopRejected(t *testing.T) {
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://169.254.169.254/latest")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(indexer.Close)
	c, stub := newRPCStub(t, nil)
	allowTorrentFetch(c)
	c.validateTorrentURL = func(u string) error {
		if strings.Contains(u, "169.254") {
			return errors.New("url not allowed: link-local")
		}
		return nil
	}
	_, err := c.AddTorrent(context.Background(), indexer.URL+"/dl", "", nil)
	if err == nil || !strings.Contains(err.Error(), "url not allowed") {
		t.Fatalf("err = %v", err)
	}
	if stub.callCount() != 0 {
		t.Error("Transmission was called after a refused redirect")
	}
}

func TestAddTorrent_FetchCancelled(t *testing.T) {
	c, _ := newRPCStub(t, nil)
	allowTorrentFetch(c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.AddTorrent(ctx, "http://127.0.0.1:1/x.torrent", "", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestValidateTorrentFetchURL_DefaultPolicy(t *testing.T) {
	c := New("localhost", 9091, "", "", "", false)
	c.validateTorrentURL = nil
	if err := c.validateTorrentFetchURL("http://169.254.169.254/latest/meta-data"); err == nil {
		t.Fatal("the default policy must refuse the cloud metadata address")
	}
}

func TestReadRPCBody_ReaderError(t *testing.T) {
	boom := errors.New("connection reset")
	if _, err := readRPCBody(iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	got, err := readRPCBody(strings.NewReader(`{"result":"success"}`))
	if err != nil || string(got) != `{"result":"success"}` {
		t.Fatalf("got (%q, %v)", got, err)
	}
}

func TestDoRequest_BodyReadErrors(t *testing.T) {
	failing := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(iotest.ErrReader(errors.New("truncated"))), Header: http.Header{}}, nil
	})
	c := newTransportClient(failing)
	err := c.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "transmission: read body: truncated") {
		t.Fatalf("err = %v", err)
	}

	// The 409 retry reads its body under the same cap.
	n := 0
	retry := roundTripFunc(func(*http.Request) (*http.Response, error) {
		n++
		if n == 1 {
			h := http.Header{}
			h.Set("X-Transmission-Session-Id", "sid")
			return &http.Response{StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader("")), Header: h}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(iotest.ErrReader(errors.New("cut off"))), Header: http.Header{}}, nil
	})
	c = newTransportClient(retry)
	err = c.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "read retry body: cut off") {
		t.Fatalf("retry err = %v", err)
	}

	// A transport failure on the retry is reported as such.
	n = 0
	retryFails := roundTripFunc(func(*http.Request) (*http.Response, error) {
		n++
		if n == 1 {
			h := http.Header{}
			h.Set("X-Transmission-Session-Id", "sid")
			return &http.Response{StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader("")), Header: h}, nil
		}
		return nil, errors.New("dial refused")
	})
	c = newTransportClient(retryFails)
	err = c.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "retry request") {
		t.Fatalf("retry transport err = %v", err)
	}
}

func TestValidateRequestTargetAndRedirect(t *testing.T) {
	c := New("localhost", 9091, "", "", "", false)
	if err := c.validateRequestTarget(nil); err == nil {
		t.Error("nil target accepted")
	}
	good, _ := url.Parse(c.baseURL)
	if err := c.validateRequestTarget(good); err != nil {
		t.Errorf("the configured RPC URL was refused: %v", err)
	}

	req := &http.Request{URL: good}
	if err := c.checkRedirect(req, make([]*http.Request, 3)); err != nil {
		t.Errorf("a redirect back to the RPC URL was refused: %v", err)
	}
	if err := c.checkRedirect(req, make([]*http.Request, 10)); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("tenth redirect: err = %v", err)
	}

	unconfigured := New("", 9091, "", "", "", false)
	if err := unconfigured.validateRequestTarget(good); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("unconfigured client: err = %v", err)
	}
	unconfigured.initErr = nil
	if _, err := unconfigured.buildRequest(context.Background(), "session-get", nil); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("buildRequest without an RPC URL: err = %v", err)
	}
}

func TestCopyRequest(t *testing.T) {
	c := New("localhost", 9091, "", "", "", false)
	c.sessionID = "fresh"
	orig, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.baseURL, strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	orig.Header.Set("X-Transmission-Session-Id", "stale")
	cp, err := c.copyRequest(orig)
	if err != nil {
		t.Fatalf("copyRequest: %v", err)
	}
	body, _ := io.ReadAll(cp.Body)
	if string(body) != "payload" || cp.Header.Get("X-Transmission-Session-Id") != "fresh" {
		t.Errorf("copy = (%q, %q), want the body again with the new session id", body, cp.Header.Get("X-Transmission-Session-Id"))
	}

	noFactory := orig.Clone(context.Background())
	noFactory.GetBody = nil
	if _, err := c.copyRequest(noFactory); err == nil || !strings.Contains(err.Error(), "missing request body factory") {
		t.Errorf("err = %v", err)
	}
	badFactory := orig.Clone(context.Background())
	badFactory.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("gone") }
	if _, err := c.copyRequest(badFactory); err == nil || !strings.Contains(err.Error(), "rebuild retry body: gone") {
		t.Errorf("err = %v", err)
	}
}
