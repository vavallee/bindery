package rtorrent

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------- test rig

// recordedCall is one XML-RPC request the fake endpoint received, decoded far
// enough to assert on the method and its arguments.
type recordedCall struct {
	Method string
	Args   []string
	// Base64Args holds the decoded bytes of any <base64> parameter, which is
	// how load.raw_start carries the .torrent file.
	Base64Args []string
}

type inboundCall struct {
	XMLName xml.Name  `xml:"methodCall"`
	Method  string    `xml:"methodName"`
	Params  []xmlItem `xml:"params>param"`
}

// fakeRtorrent is an httptest server that speaks XML-RPC and answers from a
// per-method handler table, recording every call for assertions.
type fakeRtorrent struct {
	*httptest.Server
	mu       sync.Mutex
	calls    []recordedCall
	handlers map[string]func(recordedCall) string
	// fallback answers any method with no explicit handler. Defaults to i8 0.
	fallback func(recordedCall) string
}

func newFakeRtorrent(t *testing.T, handlers map[string]func(recordedCall) string) *fakeRtorrent {
	t.Helper()
	f := &fakeRtorrent{handlers: handlers}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in inboundCall
		if err := xml.Unmarshal(body, &in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		call := recordedCall{Method: in.Method}
		for i := range in.Params {
			v := in.Params[i].Value
			if v.Base64 != nil {
				call.Base64Args = append(call.Base64Args, v.stringValue())
				continue
			}
			call.Args = append(call.Args, v.stringValue())
		}
		f.mu.Lock()
		f.calls = append(f.calls, call)
		h, ok := f.handlers[in.Method]
		fallback := f.fallback
		f.mu.Unlock()

		w.Header().Set("Content-Type", "text/xml")
		switch {
		case ok:
			_, _ = io.WriteString(w, h(call))
		case fallback != nil:
			_, _ = io.WriteString(w, fallback(call))
		default:
			_, _ = io.WriteString(w, intResponse(0))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeRtorrent) recorded() []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeRtorrent) callsTo(method string) []recordedCall {
	var out []recordedCall
	for _, c := range f.recorded() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func intResponse(n int) string {
	return fmt.Sprintf(`<?xml version="1.0"?><methodResponse><params><param><value><i8>%d</i8></value></param></params></methodResponse>`, n)
}

func stringResponse(s string) string {
	return fmt.Sprintf(`<?xml version="1.0"?><methodResponse><params><param><value><string>%s</string></value></param></params></methodResponse>`, s)
}

// newTestClient points a Client at the fake endpoint and collapses the
// add-confirmation backoff so tests don't spend three seconds each.
func newTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	c := New("localhost", 8080, "user", "pass", "", false)
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	c.baseURL = serverURL
	c.rpcURL = parsed
	c.pollInterval = time.Millisecond
	// httptest servers listen on loopback, which the download-fetch SSRF policy
	// blocks by default. Drop both the pre-flight check and the dial-time guard
	// for the indexer fetches, same as the Transmission and Deluge tests.
	c.validateTorrentURL = func(string) error { return nil }
	c.fetchTransport = nil
	return c
}

// ---------------------------------------------------------------- New

func TestNew(t *testing.T) {
	cases := map[string]struct {
		host    string
		port    int
		urlBase string
		ssl     bool
		want    string
		wantErr bool
	}{
		"defaults to /RPC2":           {"seedbox.example", 8080, "", false, "http://seedbox.example:8080/RPC2", false},
		"honours an explicit path":    {"seedbox.example", 443, "/plugins/rpc/rpc.php", true, "https://seedbox.example:443/plugins/rpc/rpc.php", false},
		"normalises a bare path":      {"box", 80, "RPC2", false, "http://box:80/RPC2", false},
		"strips a pasted scheme+host": {"box", 80, "https://box/RPC2", false, "http://box:80/RPC2", false},
		"rejects a scheme in host":    {"http://box", 8080, "", false, "", true},
		"rejects a path in host":      {"box/rpc", 8080, "", false, "", true},
		"rejects an empty host":       {"", 8080, "", false, "", true},
		"rejects a bad port":          {"box", 0, "", false, "", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := New(tc.host, tc.port, "u", "p", tc.urlBase, tc.ssl)
			if tc.wantErr {
				if c.initErr == nil {
					t.Fatal("expected initErr")
				}
				if err := c.Test(context.Background()); err == nil {
					t.Fatal("expected client operations to fail after a bad init")
				}
				return
			}
			if c.initErr != nil {
				t.Fatalf("unexpected initErr: %v", c.initErr)
			}
			if c.baseURL != tc.want {
				t.Fatalf("baseURL: got %q, want %q", c.baseURL, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------- Test

func TestTest_Success(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"system.client_version": func(recordedCall) string { return stringResponse("0.9.8") },
		"d.multicall2":          func(recordedCall) string { return emptyMulticallResponse },
	})
	c := newTestClient(t, f.URL)
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	// Both probes must run: a version-only check passes against an endpoint
	// whose download list is unreadable.
	if len(f.callsTo("system.client_version")) != 1 || len(f.callsTo("d.multicall2")) != 1 {
		t.Fatalf("expected both probes, got %+v", f.recorded())
	}
}

func TestTest_SendsBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	var ok bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, ok = r.BasicAuth()
		_, _ = io.WriteString(w, stringResponse("0.9.8"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _ = c.Version(context.Background())
	if !ok || gotUser != "user" || gotPass != "pass" {
		t.Fatalf("basic auth: ok=%v user=%q pass=%q", ok, gotUser, gotPass)
	}
}

// The most common misconfiguration: the URL path points at ruTorrent's web UI
// rather than the RPC endpoint. The server answers 200 with HTML, so the error
// has to name the fix or the user has nothing to go on.
func TestTest_HTMLEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html><body>ruTorrent</body></html>")
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected an error when the endpoint serves HTML")
	}
	if !strings.Contains(err.Error(), "/RPC2") {
		t.Fatalf("error should name the expected RPC path, got: %v", err)
	}
}

func TestTest_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected an error on 401")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("401 should be reported as a credential problem, got: %v", err)
	}
}

func TestDefaultDirectory(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"directory.default": func(recordedCall) string { return stringResponse("/home/user/downloads") },
	})
	c := newTestClient(t, f.URL)
	got, err := c.DefaultDirectory(context.Background())
	if err != nil {
		t.Fatalf("DefaultDirectory: %v", err)
	}
	if got != "/home/user/downloads" {
		t.Fatalf("got %q", got)
	}
}

// ---------------------------------------------------------------- GetTorrents

func TestGetTorrents(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.multicall2": func(recordedCall) string { return multicallResponse },
	})
	c := newTestClient(t, f.URL)

	torrents, err := c.GetTorrents(context.Background(), "")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(torrents) != 2 {
		t.Fatalf("got %d torrents, want 2", len(torrents))
	}

	// The multicall must ask for the "main" view and every documented field, in
	// the order torrentFromRow assumes.
	call := f.callsTo("d.multicall2")[0]
	if len(call.Args) != 2+len(multicallFields) {
		t.Fatalf("multicall args: got %v", call.Args)
	}
	if call.Args[1] != "main" {
		t.Fatalf("view: got %q, want main", call.Args[1])
	}
	for i, field := range multicallFields {
		if call.Args[2+i] != field {
			t.Fatalf("field %d: got %q, want %q", i, call.Args[2+i], field)
		}
	}

	hobbit := torrents[0]
	if hobbit.Name != "The Hobbit" {
		t.Errorf("name: got %q", hobbit.Name)
	}
	// rTorrent reports upper-case; Bindery stores lower-case hashes everywhere.
	if hobbit.Hash != "2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e" {
		t.Errorf("hash not lower-cased: got %q", hobbit.Hash)
	}
	// ruTorrent percent-encodes the label it writes into d.custom1.
	if hobbit.Label != "sci fi" {
		t.Errorf("label: got %q, want %q", hobbit.Label, "sci fi")
	}
	if hobbit.BasePath != "/home/user/downloads/The Hobbit" {
		t.Errorf("base path: got %q", hobbit.BasePath)
	}
	if hobbit.Complete {
		t.Error("The Hobbit is still downloading; Complete should be false")
	}
	if !hobbit.IsActive {
		t.Error("IsActive should be true")
	}
	if got := hobbit.Progress(); got < 74.9 || got > 75.1 {
		t.Errorf("progress: got %v, want ~75", got)
	}
	// 262144 bytes left at 131072 B/s = 2 seconds.
	if got := hobbit.ETA(); got != 2 {
		t.Errorf("ETA: got %d, want 2", got)
	}

	dune := torrents[1]
	if !dune.Complete {
		t.Error("Dune is complete")
	}
	if dune.Message == "" {
		t.Error("Dune carries a tracker message; it must survive decoding")
	}
	if got := dune.Progress(); got != 100 {
		t.Errorf("complete torrent progress: got %v, want 100", got)
	}
	if got := dune.ETA(); got != 0 {
		t.Errorf("ETA with no rate should be 0, got %d", got)
	}
}

func TestGetTorrents_FiltersByLabel(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.multicall2": func(recordedCall) string { return multicallResponse },
	})
	c := newTestClient(t, f.URL)

	// rTorrent has no server-side label filter, so GetTorrents does it locally.
	books, err := c.GetTorrents(context.Background(), "books")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(books) != 1 || books[0].Name != "Dune" {
		t.Fatalf("label filter: got %+v", books)
	}

	// The percent-encoded label must match its decoded spelling.
	scifi, err := c.GetTorrents(context.Background(), "sci fi")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(scifi) != 1 || scifi[0].Name != "The Hobbit" {
		t.Fatalf("encoded label filter: got %+v", scifi)
	}

	none, err := c.GetTorrents(context.Background(), "movies")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("unknown label should match nothing, got %+v", none)
	}

	// #700: a client that grabs ebooks and audiobooks under different labels
	// must see both from a single poll.
	both, err := c.GetTorrents(context.Background(), "books", "sci fi")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(both) != 2 {
		t.Fatalf("two labels should match both torrents, got %+v", both)
	}
	if len(f.callsTo("d.multicall2")) != 4 {
		t.Fatalf("each GetTorrents call must be exactly one multicall, got %d", len(f.callsTo("d.multicall2")))
	}

	// A blank label in the set is "unset", not "match the empty label".
	blank, err := c.GetTorrents(context.Background(), "", "  ")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(blank) != 2 {
		t.Fatalf("blank labels must not filter anything out, got %+v", blank)
	}
}

// A row shorter than multicallFields means the column contract drifted.
// Decoding it anyway would attribute one torrent's size to another's hash.
func TestGetTorrents_DropsShortRows(t *testing.T) {
	short := `<?xml version="1.0"?><methodResponse><params><param><value><array><data>
<value><array><data><value><string>Truncated</string></value><value><string>ABCD</string></value></data></array></value>
</data></array></value></param></params></methodResponse>`
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.multicall2": func(recordedCall) string { return short },
	})
	c := newTestClient(t, f.URL)
	got, err := c.GetTorrents(context.Background(), "")
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("short row should be dropped, got %+v", got)
	}
}

func TestGetTorrents_Fault(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.multicall2": func(recordedCall) string { return faultResponse },
	})
	c := newTestClient(t, f.URL)
	if _, err := c.GetTorrents(context.Background(), ""); err == nil {
		t.Fatal("expected a fault to surface as an error")
	}
}

// TestSnippet_TruncatesOnRuneBoundary guards the non-200 body echo. The result
// is persisted on the download row and travels into history and webhook
// payloads, so a fixed byte slice that lands mid-rune plants a replacement
// character in all three.
func TestSnippet_TruncatesOnRuneBoundary(t *testing.T) {
	// 128 three-byte runes = 384 bytes; byte 256 falls inside a rune.
	body := []byte(strings.Repeat("→", 128))
	got := snippet(body)
	if len(got) > 256 {
		t.Fatalf("snippet must not exceed 256 bytes, got %d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("snippet cut mid-rune: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("snippet contains a replacement character: %q", got)
	}
	if short := snippet([]byte("  plain  ")); short != "plain" {
		t.Fatalf("short bodies pass through trimmed, got %q", short)
	}
}

// ---------------------------------------------------------------- AddTorrent

const (
	// A minimal but valid single-file .torrent and its v1 infohash.
	sampleTorrent = "d8:announce10:udp://t/an4:infod6:lengthi5e4:name8:test.txt12:piece lengthi16384e6:pieces20:" +
		"\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14ee"
	sampleTorrentHash = "9ca9aea0e4d50429f039ca828f52ec49283f36bb"

	magnetHash = "abcdef0123456789abcdef0123456789abcdef01"
	sampleMag  = "magnet:?xt=urn:btih:" + magnetHash + "&dn=Book"
)

// presentAfterLoad answers d.hash for any torrent, i.e. rTorrent picked the
// load up immediately.
func presentAfterLoad(recordedCall) string { return stringResponse("HASHPRESENT") }

func TestAddTorrent_Magnet(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"load.start": func(recordedCall) string { return intResponse(0) },
		"d.hash":     presentAfterLoad,
	})
	c := newTestClient(t, f.URL)

	got, err := c.AddTorrent(context.Background(), sampleMag, "sci fi", "/downloads/books", nil)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	// rTorrent's load.* commands return 0, never a hash — Bindery derives it.
	if got != magnetHash {
		t.Fatalf("hash: got %q, want %q", got, magnetHash)
	}

	loads := f.callsTo("load.start")
	if len(loads) != 1 {
		t.Fatalf("expected one load.start, got %d", len(loads))
	}
	args := loads[0].Args
	if len(args) != 4 {
		t.Fatalf("load.start args: got %v", args)
	}
	if args[0] != "" {
		t.Errorf("first arg must be the empty target, got %q", args[0])
	}
	if args[1] != sampleMag {
		t.Errorf("magnet arg: got %q", args[1])
	}
	// The label goes on percent-encoded, matching ruTorrent's own convention,
	// and quoted because rTorrent parses these trailing arguments as commands.
	if args[2] != `d.custom1.set="sci%20fi"` {
		t.Errorf("label command: got %q", args[2])
	}
	if args[3] != `d.directory.set="/downloads/books"` {
		t.Errorf("directory command: got %q", args[3])
	}
}

// TestAddTorrent_CommandArgsAreQuoted pins the quoting on load.*'s trailing
// arguments.
//
// They are not data: rTorrent parses each one as a command and splits its
// argument list on commas, and url.PathEscape escapes neither "," nor "=".
// Unquoted, `d.directory.set=/downloads/Doe, John/Book` set the directory to
// "/downloads/Doe" and passed " John/Book" as a second argument, so the torrent
// landed somewhere Bindery never looks. An author-named path with a comma in it
// is not exotic. ruTorrent's own addtorrent.php quotes the directory the same
// way.
func TestAddTorrent_CommandArgsAreQuoted(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"load.start": func(recordedCall) string { return intResponse(0) },
		"d.hash":     presentAfterLoad,
	})
	c := newTestClient(t, f.URL)

	if _, err := c.AddTorrent(context.Background(), sampleMag, "sci,fi", `/downloads/Doe, John\Books`, nil); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	args := f.callsTo("load.start")[0].Args
	if len(args) != 4 {
		t.Fatalf("load.start args: got %v", args)
	}
	// url.PathEscape already turns a comma into %2C, so the label was never the
	// exposed half — it is quoted for consistency with the directory.
	if args[2] != `d.custom1.set="sci%2Cfi"` {
		t.Errorf("label command: got %q, want the value wrapped in quotes", args[2])
	}
	// The backslash is escaped inside the quotes, which is what rTorrent's
	// parser unescapes back to a single character.
	if want := `d.directory.set="/downloads/Doe, John\\Books"`; args[3] != want {
		t.Errorf("directory command: got %q, want %q", args[3], want)
	}
}

func TestAddTorrent_MagnetBase32(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{"d.hash": presentAfterLoad})
	c := newTestClient(t, f.URL)

	// Older trackers emit the base32 spelling; rTorrent is addressed by hex.
	got, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:AEBAGBAFAYDQQCIKBMGA2DQPCAIREEYU", "", "", nil)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if got != "0102030405060708090a0b0c0d0e0f1011121314" {
		t.Fatalf("base32 magnet hash: got %q", got)
	}
}

func TestAddTorrent_MagnetWithoutHash(t *testing.T) {
	f := newFakeRtorrent(t, nil)
	c := newTestClient(t, f.URL)
	if _, err := c.AddTorrent(context.Background(), "magnet:?dn=Book", "", "", nil); err == nil {
		t.Fatal("a magnet with no btih topic must be refused, not added blind")
	}
	if len(f.callsTo("load.start")) != 0 {
		t.Fatal("nothing should be submitted when the hash cannot be derived")
	}
}

func TestAddTorrent_OmitsEmptyCommands(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{"d.hash": presentAfterLoad})
	c := newTestClient(t, f.URL)
	if _, err := c.AddTorrent(context.Background(), sampleMag, "", "", nil); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	args := f.callsTo("load.start")[0].Args
	if len(args) != 2 {
		t.Fatalf("blank label/directory must not add commands, got %v", args)
	}
}

func TestAddTorrent_TorrentFile(t *testing.T) {
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, sampleTorrent)
	}))
	defer indexer.Close()

	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"load.raw_start": func(recordedCall) string { return intResponse(0) },
		"d.hash":         presentAfterLoad,
	})
	c := newTestClient(t, f.URL)

	got, err := c.AddTorrent(context.Background(), indexer.URL+"/dl.torrent", "books", "", nil)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if got != sampleTorrentHash {
		t.Fatalf("hash: got %q, want %q", got, sampleTorrentHash)
	}

	loads := f.callsTo("load.raw_start")
	if len(loads) != 1 {
		t.Fatalf("expected one load.raw_start, got %d", len(loads))
	}
	// Bindery fetches the .torrent itself (#873) and submits the bytes, so a
	// download client on a different network still gets the torrent.
	if len(loads[0].Base64Args) != 1 || loads[0].Base64Args[0] != sampleTorrent {
		t.Fatalf("torrent bytes not submitted as base64: %+v", loads[0])
	}
	if len(f.callsTo("load.start")) != 0 {
		t.Fatal("a .torrent must not be handed to rTorrent as a URL")
	}
}

// #1006: an indexer link that 30x-redirects to a magnet: URI. Go's HTTP client
// cannot follow that, so the redirect must be captured and the magnet loaded.
func TestAddTorrent_RedirectToMagnet(t *testing.T) {
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sampleMag, http.StatusFound)
	}))
	defer indexer.Close()

	f := newFakeRtorrent(t, map[string]func(recordedCall) string{"d.hash": presentAfterLoad})
	c := newTestClient(t, f.URL)

	got, err := c.AddTorrent(context.Background(), indexer.URL+"/dl", "", "", nil)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if got != magnetHash {
		t.Fatalf("hash: got %q, want %q", got, magnetHash)
	}
	loads := f.callsTo("load.start")
	if len(loads) != 1 || loads[0].Args[1] != sampleMag {
		t.Fatalf("expected the magnet to be loaded, got %+v", loads)
	}
}

func TestAddTorrent_NotATorrent(t *testing.T) {
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html>login required</html>")
	}))
	defer indexer.Close()

	f := newFakeRtorrent(t, nil)
	c := newTestClient(t, f.URL)
	if _, err := c.AddTorrent(context.Background(), indexer.URL+"/dl", "", "", nil); err == nil {
		t.Fatal("expected an error when the indexer does not return a torrent")
	}
	if len(f.callsTo("load.raw_start")) != 0 {
		t.Fatal("nothing should be submitted when the payload is not a torrent")
	}
}

// rTorrent reports a rejected metafile as a non-zero return rather than a
// fault, so a decoder that ignores the return value calls a failure a success.
func TestAddTorrent_NonZeroReturn(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"load.start": func(recordedCall) string { return intResponse(1) },
		"d.hash":     presentAfterLoad,
	})
	c := newTestClient(t, f.URL)
	if _, err := c.AddTorrent(context.Background(), sampleMag, "", "", nil); err == nil {
		t.Fatal("a non-zero load.start return must fail the add")
	}
}

// A magnet may sit as a "<hash>.meta" placeholder for a long time while
// rTorrent resolves metadata. The hash is already correct, so the grab should
// succeed and let the poller pick the torrent up later.
func TestAddTorrent_MagnetNotYetVisible(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.hash": func(recordedCall) string { return faultResponse },
	})
	c := newTestClient(t, f.URL)
	got, err := c.AddTorrent(context.Background(), sampleMag, "", "", nil)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if got != magnetHash {
		t.Fatalf("hash: got %q", got)
	}
}

// A .torrent, by contrast, is loaded synchronously — if it never appears,
// rTorrent rejected it and the grab must fail rather than wedge at
// "downloading" forever.
func TestAddTorrent_FileNeverAppears(t *testing.T) {
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, sampleTorrent)
	}))
	defer indexer.Close()

	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.hash": func(recordedCall) string { return faultResponse },
	})
	c := newTestClient(t, f.URL)
	if _, err := c.AddTorrent(context.Background(), indexer.URL+"/dl.torrent", "", "", nil); err == nil {
		t.Fatal("expected an error when the torrent never appears in the list")
	}
}

// The seed-ratio override is not expressible over rTorrent's XML-RPC. It must
// be ignored rather than turned into a bogus command.
func TestAddTorrent_SeedRatioIgnored(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{"d.hash": presentAfterLoad})
	c := newTestClient(t, f.URL)
	ratio := 2.5
	if _, err := c.AddTorrent(context.Background(), sampleMag, "", "", &ratio); err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	for _, call := range f.recorded() {
		for _, a := range call.Args {
			if strings.Contains(strings.ToLower(a), "ratio") {
				t.Fatalf("no ratio command should be sent, got %q", a)
			}
		}
	}
}

// ---------------------------------------------------------------- Files

func TestFiles(t *testing.T) {
	resp := `<?xml version="1.0"?><methodResponse><params><param><value><array><data>
<value><array><data><value><string>book.epub</string></value><value><i8>2048</i8></value></data></array></value>
<value><array><data><value><string>sub/cover.jpg</string></value><value><i8>512</i8></value></data></array></value>
<value><array><data><value><string></string></value><value><i8>0</i8></value></data></array></value>
</data></array></value></param></params></methodResponse>`
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"f.multicall": func(recordedCall) string { return resp },
	})
	c := newTestClient(t, f.URL)

	files, err := c.Files(context.Background(), magnetHash)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	// The unnamed entry is dropped — it would resolve to the torrent directory
	// itself and drag unrelated siblings into the import.
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2: %+v", len(files), files)
	}
	if files[0].Name != "book.epub" || files[0].Size != 2048 {
		t.Errorf("file 0: %+v", files[0])
	}
	if files[1].Name != "sub/cover.jpg" || files[1].Size != 512 {
		t.Errorf("file 1: %+v", files[1])
	}

	// rTorrent is addressed by upper-case hex.
	call := f.callsTo("f.multicall")[0]
	if call.Args[0] != strings.ToUpper(magnetHash) {
		t.Errorf("hash argument: got %q, want upper-case hex", call.Args[0])
	}
}

// ---------------------------------------------------------------- Remove

func TestRemoveTorrent(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.erase": func(recordedCall) string { return intResponse(0) },
	})
	c := newTestClient(t, f.URL)
	if err := c.RemoveTorrent(context.Background(), magnetHash); err != nil {
		t.Fatalf("RemoveTorrent: %v", err)
	}
	calls := f.callsTo("d.erase")
	if len(calls) != 1 || calls[0].Args[0] != strings.ToUpper(magnetHash) {
		t.Fatalf("d.erase: got %+v", calls)
	}
}

// A torrent that is already gone is the desired end state, not an error —
// otherwise a re-run of the removal path wedges the download row.
func TestRemoveTorrent_AlreadyGone(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.erase": func(recordedCall) string { return faultResponse },
	})
	c := newTestClient(t, f.URL)
	if err := c.RemoveTorrent(context.Background(), magnetHash); err != nil {
		t.Fatalf("an unknown hash should be treated as removed, got %v", err)
	}
}

func TestRemoveTorrent_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL)
	if err := c.RemoveTorrent(context.Background(), magnetHash); err == nil {
		t.Fatal("a transport failure must not be swallowed like a fault")
	}
}

func TestHasTorrent(t *testing.T) {
	present := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.hash": func(recordedCall) string { return stringResponse(strings.ToUpper(magnetHash)) },
	})
	c := newTestClient(t, present.URL)
	ok, err := c.HasTorrent(context.Background(), magnetHash)
	if err != nil || !ok {
		t.Fatalf("HasTorrent: ok=%v err=%v", ok, err)
	}

	absent := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.hash": func(recordedCall) string { return faultResponse },
	})
	c2 := newTestClient(t, absent.URL)
	ok, err = c2.HasTorrent(context.Background(), magnetHash)
	if err != nil {
		t.Fatalf("an unknown hash is a negative answer, not an error: %v", err)
	}
	if ok {
		t.Fatal("expected false for an unknown hash")
	}
}

func TestBasePath(t *testing.T) {
	f := newFakeRtorrent(t, map[string]func(recordedCall) string{
		"d.base_path": func(recordedCall) string { return stringResponse("/home/user/downloads/Book") },
	})
	c := newTestClient(t, f.URL)
	got, err := c.BasePath(context.Background(), magnetHash)
	if err != nil {
		t.Fatalf("BasePath: %v", err)
	}
	if got != "/home/user/downloads/Book" {
		t.Fatalf("got %q", got)
	}
}

func TestSetLabel(t *testing.T) {
	f := newFakeRtorrent(t, nil)
	c := newTestClient(t, f.URL)
	if err := c.SetLabel(context.Background(), magnetHash, "sci fi"); err != nil {
		t.Fatalf("SetLabel: %v", err)
	}
	call := f.callsTo("d.custom1.set")[0]
	if call.Args[1] != "sci%20fi" {
		t.Fatalf("label must be percent-encoded for ruTorrent, got %q", call.Args[1])
	}
}

// ---------------------------------------------------------------- guards

// The RPC transport must refuse to talk to anything but the configured
// endpoint, so a redirect cannot walk the admin's credentials elsewhere.
func TestCall_RefusesRedirectOffEndpoint(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, stringResponse("0.9.8"))
	}))
	defer elsewhere.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if _, err := c.Version(context.Background()); err == nil {
		t.Fatal("expected the off-endpoint redirect to be refused")
	}
}

func TestLabelRoundTrip(t *testing.T) {
	for _, label := range []string{"books", "sci fi", "a+b", "ünïcode", "a/b"} {
		if got := decodeLabel(encodeLabel(label)); got != label {
			t.Errorf("round trip %q: got %q", label, got)
		}
	}
	// A label ruTorrent wrote arrives percent-encoded and must decode.
	if got := decodeLabel("sci%20fi"); got != "sci fi" {
		t.Errorf("ruTorrent label: got %q", got)
	}
	// An undecodable value is passed through rather than lost.
	if got := decodeLabel("100%"); got != "100%" {
		t.Errorf("malformed escape: got %q", got)
	}
}

func TestWireHash(t *testing.T) {
	if got := wireHash("  abcdef01  "); got != "ABCDEF01" {
		t.Fatalf("got %q", got)
	}
}
