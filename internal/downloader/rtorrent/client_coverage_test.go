package rtorrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// multicallRow renders one d.multicall2 row with the given column values as
// strings, in multicallFields order.
func multicallRow(cols ...string) string {
	var b strings.Builder
	b.WriteString(`<value><array><data>`)
	for _, c := range cols {
		b.WriteString(`<value><string>` + c + `</string></value>`)
	}
	b.WriteString(`</data></array></value>`)
	return b.String()
}

func arrayResponse(rows ...string) string {
	return `<?xml version="1.0"?><methodResponse><params><param><value><array><data>` +
		strings.Join(rows, "") + `</data></array></value></param></params></methodResponse>`
}

// ---------------------------------------------------------------- Test

func TestTest_ConnectionRefusedCarriesHint(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	dead := srv.URL
	srv.Close()
	c := newTestClient(t, dead)
	err := c.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not reach rTorrent") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("err = %v, want the network hint for a refused connection", err)
	}
}

func TestTest_EmptyVersion(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"system.client_version": func(recordedCall) string { return stringResponse("  ") },
	})
	err := newTestClient(t, f.URL).Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reported no version") {
		t.Fatalf("err = %v", err)
	}
}

func TestTest_ListUnreadable(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"system.client_version": func(recordedCall) string { return stringResponse("0.9.8") },
		"d.multicall2":          func(recordedCall) string { return faultResponse },
	})
	err := newTestClient(t, f.URL).Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reported version 0.9.8 but the download list could not be read") {
		t.Fatalf("err = %v", err)
	}
}

func TestDefaultDirectory_Fault(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"directory.default": func(recordedCall) string { return faultResponse },
	})
	if _, err := newTestClient(t, f.URL).DefaultDirectory(context.Background()); err == nil {
		t.Fatal("a fault must surface as an error")
	}
}

func TestGetTorrents_DropsRowWithoutHash(t *testing.T) {
	cols := func(hash string) string {
		return multicallRow("Book", hash, "/d/Book", "/d", "books", "100", "0", "0", "1", "0", "1", "")
	}
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.multicall2": func(recordedCall) string {
			return arrayResponse(cols(""), cols("ABCDEF"), `<value><string>not a row</string></value>`)
		},
	})
	got, err := newTestClient(t, f.URL).GetTorrents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Hash != "abcdef" || !got[0].Complete || got[0].Progress() != 100 {
		t.Fatalf("got %+v, want only the hashed row, lower-cased", got)
	}
}

// ---------------------------------------------------------------- AddTorrent error paths

func TestAddTorrent_ErrorPaths(t *testing.T) {
	magnetRedirect := func(target string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", target)
			w.WriteHeader(http.StatusFound)
		}
	}
	cases := []struct {
		name     string
		indexer  http.HandlerFunc // nil: AddTorrent is given sampleMag directly
		handlers map[string]func(recordedCall) string
		wantErr  string
	}{
		{"magnet load faults", nil,
			map[string]func(recordedCall) string{"load.start": func(recordedCall) string { return faultResponse }},
			"load.start"},
		{"magnet load refused", nil,
			map[string]func(recordedCall) string{"load.start": func(recordedCall) string { return intResponse(-1) }},
			"refused the torrent (return code -1)"},
		{"indexer 404", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "gone", http.StatusNotFound) },
			nil, "indexer returned HTTP 404: gone"},
		{"redirect to magnet without btih", magnetRedirect("magnet:?dn=nohash"),
			nil, "no usable btih infohash"},
		{"redirect to magnet then load faults", magnetRedirect(sampleMag),
			map[string]func(recordedCall) string{"load.start": func(recordedCall) string { return faultResponse }},
			"load.start"},
		{"raw load faults", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, sampleTorrent) },
			map[string]func(recordedCall) string{"load.raw_start": func(recordedCall) string { return faultResponse }},
			"load.raw_start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRtorrent(t, tc.handlers)
			c := newTestClient(t, f.URL)
			src := sampleMag
			if tc.indexer != nil {
				idx := httptest.NewServer(tc.indexer)
				defer idx.Close()
				src = idx.URL + "/dl"
			}
			hash, err := c.AddTorrent(context.Background(), src, "", "", nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("AddTorrent = %q, %v; want error containing %q", hash, err, tc.wantErr)
			}
		})
	}
}

// Some proxies answer load.* with an empty <value/>. With no fault that is
// read as success, and the add then waits for the torrent as usual.
func TestAddTorrent_UndecodableLoadReplyIsSuccess(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"load.start": func(recordedCall) string {
			return `<?xml version="1.0"?><methodResponse><params><param><value></value></param></params></methodResponse>`
		},
		"d.hash": presentAfterLoad,
	})
	hash, err := newTestClient(t, f.URL).AddTorrent(context.Background(), sampleMag, "", "", nil)
	if err != nil || hash != magnetHash {
		t.Fatalf("AddTorrent = %q, %v", hash, err)
	}
}

// A cancelled context stops the add-confirmation poll; a magnet still hands
// back its hash because the hash is already known.
func TestAddTorrent_CancelledWhilePolling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.hash": func(recordedCall) string { cancel(); return faultResponse },
	})
	c := newTestClient(t, f.URL)
	c.pollInterval = time.Hour // only ctx.Done can end the wait promptly
	hash, err := c.AddTorrent(ctx, sampleMag, "", "", nil)
	if err != nil || hash != magnetHash {
		t.Fatalf("AddTorrent = %q, %v", hash, err)
	}
	if n := len(f.callsTo("d.hash")); n != 1 {
		t.Errorf("d.hash polled %d times after cancellation, want 1", n)
	}
}

// ---------------------------------------------------------------- per-torrent calls

func TestPerTorrentCalls_TransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "proxy down", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL)
	ctx := context.Background()

	if _, err := c.HasTorrent(ctx, magnetHash); err == nil || !strings.Contains(err.Error(), "HTTP 502: proxy down") {
		t.Errorf("HasTorrent err = %v, want the transport failure, not a negative answer", err)
	}
	if _, err := c.BasePath(ctx, magnetHash); err == nil || !strings.Contains(err.Error(), "d.base_path") {
		t.Errorf("BasePath err = %v", err)
	}
	if _, err := c.Files(ctx, magnetHash); err == nil || !strings.Contains(err.Error(), "f.multicall") {
		t.Errorf("Files err = %v", err)
	}
	if err := c.SetLabel(ctx, magnetHash, "books"); err == nil || !strings.Contains(err.Error(), "d.custom1.set") {
		t.Errorf("SetLabel err = %v", err)
	}
}

func TestFiles_SkipsUnusableRows(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"f.multicall": func(recordedCall) string {
			return arrayResponse(
				multicallRow("book.epub", "1234"),
				multicallRow("only-one-column"),
				multicallRow("  ", "5"),
				`<value><i8>7</i8></value>`,
			)
		},
	})
	files, err := newTestClient(t, f.URL).Files(context.Background(), magnetHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "book.epub" || files[0].Size != 1234 {
		t.Fatalf("files = %+v", files)
	}
}

func TestRemoveTorrent_NonZeroReturn(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.erase": func(recordedCall) string { return intResponse(1) },
	})
	err := newTestClient(t, f.URL).RemoveTorrent(context.Background(), magnetHash)
	if err == nil || !strings.Contains(err.Error(), "rTorrent returned 1") {
		t.Fatalf("err = %v", err)
	}
}

// ---------------------------------------------------------------- call()

func TestCall_ConfigurationErrors(t *testing.T) {
	ctx := context.Background()

	bad := New("", 8080, "", "", "", false)
	if _, err := bad.Version(ctx); err == nil || !strings.Contains(err.Error(), "host is empty") {
		t.Errorf("init error not surfaced: %v", err)
	}

	c := New("h", 1, "", "", "", false)
	c.rpcURL = nil
	if _, err := c.Version(ctx); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("nil rpcURL: %v", err)
	}

	c = New("h", 1, "", "", "", false)
	c.baseURL = "http://bad host/RPC2"
	if _, err := c.Version(ctx); err == nil || !strings.Contains(err.Error(), "build rTorrent request") {
		t.Errorf("unbuildable request: %v", err)
	}

	// baseURL and rpcURL disagreeing must be refused before any I/O.
	c = New("h", 1, "", "", "", false)
	c.baseURL = "http://elsewhere.example/RPC2"
	if _, err := c.Version(ctx); err == nil || !strings.Contains(err.Error(), "unexpected target") {
		t.Errorf("mismatched target: %v", err)
	}

	if _, err := encodeMethodCall("m", struct{}{}); err == nil {
		t.Error("an unsupported argument type must fail to encode")
	}
	if _, err := c.call(ctx, "m", 1.5); err == nil || !strings.Contains(err.Error(), "cannot encode") {
		t.Errorf("call with unencodable arg: %v", err)
	}
}

func TestValidateRequestTarget_Nil(t *testing.T) {
	c := New("h", 1, "", "", "", false)
	if err := c.validateRequestTarget(nil); err == nil {
		t.Error("nil target must be refused")
	}
	c.rpcURL = nil
	if err := c.validateRequestTarget(&url.URL{Scheme: "http", Host: "h:1", Path: "/RPC2"}); err == nil {
		t.Error("nil rpcURL must be refused")
	}
}

func TestCheckRedirect_Cap(t *testing.T) {
	c := New("h", 1, "", "", "", false)
	req := &http.Request{URL: &url.URL{Scheme: "http", Host: "h:1", Path: "/RPC2"}}
	if err := c.checkRedirect(req, make([]*http.Request, 10)); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("err = %v", err)
	}
	if err := c.checkRedirect(req, make([]*http.Request, 1)); err != nil {
		t.Errorf("same-endpoint redirect refused: %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestCall_BodyReadError(t *testing.T) {
	c := newTestClient(t, "http://h:1/RPC2")
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(errReader{}), Request: r}, nil
	})
	if _, err := c.Version(context.Background()); err == nil || !strings.Contains(err.Error(), "read body") {
		t.Errorf("err = %v", err)
	}
	if _, err := readRPCBody(errReader{}); err == nil {
		t.Error("readRPCBody must surface a read error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ---------------------------------------------------------------- SCGI

// scriptedConn is a net.Conn whose deadline, write and read behaviour are
// scripted, standing in for an rTorrent socket that misbehaves.
type scriptedConn struct {
	net.Conn
	deadlineErr error
	writeErr    error
	reply       io.Reader
	deadline    time.Time
}

func (c *scriptedConn) SetDeadline(t time.Time) error { c.deadline = t; return c.deadlineErr }
func (c *scriptedConn) Write(p []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return len(p), nil
}
func (c *scriptedConn) Read(p []byte) (int, error) { return c.reply.Read(p) }
func (c *scriptedConn) Close() error               { return nil }

func TestSCGIRoundTrip_Failures(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		dial    func(context.Context, string, string) (net.Conn, error)
		wantErr string
	}{
		{"dial", func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("no such file")
		}, "dial scgi://sock"},
		{"deadline", func(context.Context, string, string) (net.Conn, error) {
			return &scriptedConn{deadlineErr: errors.New("bad fd")}, nil
		}, "set deadline"},
		{"write", func(context.Context, string, string) (net.Conn, error) {
			return &scriptedConn{writeErr: errors.New("broken pipe")}, nil
		}, "write to scgi://sock"},
		{"read", func(context.Context, string, string) (net.Conn, error) {
			return &scriptedConn{reply: errReader{}}, nil
		}, "read SCGI response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &scgiDialer{network: "unix", address: "sock", dial: tc.dial}
			if _, err := d.roundTrip(ctx, []byte("x")); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// A dialer built without a timeout must still get a sane deadline, and a
// caller deadline that lands sooner must win.
func TestSCGIRoundTrip_Deadlines(t *testing.T) {
	conn := &scriptedConn{reply: strings.NewReader("Status: 200\r\n\r\n" + intResponse(0))}
	d := &scgiDialer{network: "tcp", address: "h:1", dial: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}
	before := time.Now()
	body, err := d.roundTrip(context.Background(), []byte("x"))
	if err != nil || !strings.Contains(string(body), "<i8>0</i8>") {
		t.Fatalf("roundTrip = %q, %v", body, err)
	}
	if got := conn.deadline.Sub(before); got < rpcTimeout-time.Second || got > rpcTimeout+time.Second {
		t.Errorf("deadline %v from now, want the rpcTimeout default", got)
	}

	conn = &scriptedConn{reply: strings.NewReader("\r\n\r\n" + intResponse(0))}
	d.dial = func(context.Context, string, string) (net.Conn, error) { return conn, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := d.roundTrip(ctx, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if dl, _ := ctx.Deadline(); !conn.deadline.Equal(dl) {
		t.Errorf("deadline = %v, want the caller's %v", conn.deadline, dl)
	}
}

func TestSCGICall_ErrorWrapsMethod(t *testing.T) {
	c := New("", 0, "", "", "scgi:///run/rtorrent.sock", false)
	c.scgi.dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("refused") }
	if _, err := c.Version(context.Background()); err == nil || !strings.Contains(err.Error(), "system.client_version: dial") {
		t.Fatalf("err = %v", err)
	}
}

// ---------------------------------------------------------------- fetchTorrentContent

func TestFetchTorrentContent_Paths(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/relative":
			w.Header().Set("Location", "/final")
			w.WriteHeader(http.StatusFound)
		case "/final":
			if r.Header.Get("Accept") != "application/x-bittorrent" {
				http.Error(w, "accept", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, sampleTorrent)
		case "/nolocation":
			w.WriteHeader(http.StatusFound)
		case "/badlocation":
			w.Header().Set("Location", "http://[::1")
			w.WriteHeader(http.StatusFound)
		case "/ftp":
			w.Header().Set("Location", "ftp://files.example/x.torrent")
			w.WriteHeader(http.StatusFound)
		case "/loop":
			http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
		case "/empty":
			w.WriteHeader(http.StatusOK)
		case "/big":
			_, _ = io.WriteString(w, strings.Repeat("x", 64))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, "http://h:1/RPC2")
	ctx := context.Background()

	got, err := c.fetchTorrentContent(ctx, srv.URL+"/relative")
	if err != nil || string(got.data) != sampleTorrent {
		t.Fatalf("relative redirect: %v, %+v", err, got)
	}

	orig := maxTorrentFileBytes
	maxTorrentFileBytes = 32
	defer func() { maxTorrentFileBytes = orig }()

	for path, want := range map[string]string{
		"/nolocation": "redirect without location",
		// net/http rejects an unparseable Location before Bindery sees it.
		"/badlocation": "Location",
		"/ftp":         `unsupported redirect scheme "ftp"`,
		"/loop":        "too many redirects",
		"/empty":       "empty response",
		"/big":         "response exceeds 32 bytes",
	} {
		if _, err := c.fetchTorrentContent(ctx, srv.URL+path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", path, err, want)
		}
	}
}

func TestFetchTorrentContent_ValidationAndTransport(t *testing.T) {
	c := newTestClient(t, "http://h:1/RPC2")
	ctx := context.Background()

	// Every hop is validated, including the first.
	c.validateTorrentURL = func(string) error { return errors.New("url not allowed: private address") }
	if _, err := c.fetchTorrentContent(ctx, "http://10.0.0.1/x.torrent?apikey=SECRET"); err == nil ||
		!strings.Contains(err.Error(), "private address") {
		t.Errorf("validation: %v", err)
	}

	c.validateTorrentURL = func(string) error { return nil }
	if _, err := c.fetchTorrentContent(ctx, "http://bad host/x"); err == nil {
		t.Error("an unparseable URL must fail")
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	dead := srv.URL
	srv.Close()
	_, err := c.fetchTorrentContent(ctx, dead+"/x.torrent?apikey=SECRET")
	if err == nil || !strings.Contains(err.Error(), "fetch torrent from indexer") {
		t.Fatalf("transport: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("transport error leaks the indexer apikey: %v", err)
	}

	// The default validator (no override) applies the download-fetch policy.
	c.validateTorrentURL = nil
	if err := c.validateTorrentFetchURL("file:///etc/passwd"); err == nil {
		t.Error("default policy must reject a non-HTTP scheme")
	}
}

// ---------------------------------------------------------------- small helpers

func TestRedactedSource(t *testing.T) {
	if got := redactedSource("  https://idx.example/dl?apikey=SECRET  "); strings.Contains(got, "SECRET") {
		t.Errorf("apikey not redacted: %q", got)
	}
	long := "magnet:?xt=urn:btih:" + magnetHash + "&dn=" + strings.Repeat("é", 100)
	got := redactedSource(long)
	if !strings.HasSuffix(got, "…") || len(got) > 120+len("…") || !utf8.ValidString(got) {
		t.Errorf("long source = %q (len %d), want <=120 bytes plus an ellipsis on a rune boundary", got, len(got))
	}
}

func TestProgress(t *testing.T) {
	cases := []struct {
		size, left int64
		want       float64
	}{
		{0, 0, 0},
		{100, 100, 0},
		{100, 150, 0}, // a bogus left larger than size never goes negative
		{100, 0, 100},
		{100, -5, 100}, // done > size clamps at 100
		{200, 50, 75},
	}
	for _, tc := range cases {
		if got := (Torrent{SizeBytes: tc.size, LeftBytes: tc.left}).Progress(); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("Progress(size=%d,left=%d) = %v, want %v", tc.size, tc.left, got, tc.want)
		}
	}
}

func TestEncodeValue_Types(t *testing.T) {
	cases := []struct {
		in   arg
		want string
	}{
		{math.MaxInt32 + 1, "<i8>2147483648</i8>"},
		{int64(7), "<i4>7</i4>"},
		{int64(math.MinInt32) - 1, "<i8>-2147483649</i8>"},
		{true, "<boolean>1</boolean>"},
		{false, "<boolean>0</boolean>"},
		{"a<b&c", "<string>a&lt;b&amp;c</string>"},
	}
	for _, tc := range cases {
		got, err := encodeMethodCall("m", tc.in)
		if err != nil {
			t.Fatalf("%v: %v", tc.in, err)
		}
		if !strings.Contains(string(got), tc.want) {
			t.Errorf("encode(%#v) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestValueAccessors(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		name    string
		v       *xmlValue
		str     string
		n       int64
		wantErr bool
	}{
		{"i4", &xmlValue{I4: s(" 12 ")}, "12", 12, false},
		{"boolean", &xmlValue{Boolean: s("1")}, "1", 1, false},
		{"double truncates", &xmlValue{Double: s("3.9")}, "3.9", 3, false},
		{"bad double", &xmlValue{Double: s("x")}, "x", 0, true},
		{"bad base64", &xmlValue{Base64: s("!!!")}, "", 0, true},
		{"untagged digits", &xmlValue{Text: " 42 "}, "42", 42, false},
		{"non-numeric", &xmlValue{String: s("abc")}, "abc", 0, true},
		{"nil", nil, "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.stringValue(); got != tc.str {
				t.Errorf("stringValue = %q, want %q", got, tc.str)
			}
			n, err := tc.v.int64Value()
			if (err != nil) != tc.wantErr || n != tc.n {
				t.Errorf("int64Value = %d, %v; want %d, err=%v", n, err, tc.n, tc.wantErr)
			}
		})
	}
	if (&xmlValue{Double: s("2.0")}).boolValue() != true {
		t.Error("a non-zero double is true")
	}
	if fmt.Sprint((*xmlValue)(nil).rows()) != "[] 0" {
		t.Error("rows of nil should be empty")
	}
}
