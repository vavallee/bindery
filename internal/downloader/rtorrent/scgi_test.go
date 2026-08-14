package rtorrent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEncodeSCGIRequest(t *testing.T) {
	got := encodeSCGIRequest([]byte("<x/>"))

	// Netstring framing: "<len>:<headers>," then the body verbatim.
	colon := bytes.IndexByte(got, ':')
	if colon < 0 {
		t.Fatalf("no netstring length prefix: %q", got)
	}
	headerLen, err := strconv.Atoi(string(got[:colon]))
	if err != nil {
		t.Fatalf("netstring length %q: %v", got[:colon], err)
	}
	headers := got[colon+1 : colon+1+headerLen]
	if got[colon+1+headerLen] != ',' {
		t.Fatalf("netstring not terminated with a comma: %q", got)
	}
	if body := string(got[colon+2+headerLen:]); body != "<x/>" {
		t.Fatalf("body: got %q", body)
	}

	// rTorrent rejects the request unless CONTENT_LENGTH is the first header
	// and SCGI is "1".
	fields := strings.Split(strings.TrimSuffix(string(headers), "\x00"), "\x00")
	if len(fields)%2 != 0 {
		t.Fatalf("headers are not NUL-separated key/value pairs: %q", headers)
	}
	if fields[0] != "CONTENT_LENGTH" || fields[1] != "4" {
		t.Fatalf("CONTENT_LENGTH must come first and match the body length, got %v", fields[:2])
	}
	pairs := map[string]string{}
	for i := 0; i < len(fields); i += 2 {
		pairs[fields[i]] = fields[i+1]
	}
	if pairs["SCGI"] != "1" {
		t.Errorf("SCGI header: got %q, want 1", pairs["SCGI"])
	}
	if pairs["REQUEST_METHOD"] != "POST" {
		t.Errorf("REQUEST_METHOD: got %q", pairs["REQUEST_METHOD"])
	}
	if pairs["CONTENT_TYPE"] != "text/xml" {
		t.Errorf("CONTENT_TYPE: got %q", pairs["CONTENT_TYPE"])
	}
}

func TestReadSCGIResponse(t *testing.T) {
	cases := map[string]struct {
		in      string
		want    string
		wantErr bool
	}{
		"crlf header block": {"Content-Type: text/xml\r\nContent-Length: 4\r\n\r\n<x/>", "<x/>", false},
		"lf header block":   {"Content-Type: text/xml\nContent-Length: 4\n\n<x/>", "<x/>", false},
		"status line first": {"Status: 200 OK\r\nContent-Type: text/xml\r\n\r\n<x/>", "<x/>", false},
		// A reply with no separator is handed to the XML decoder whole; its own
		// error is more useful than a framing complaint.
		"no header block": {"<x/>", "<x/>", false},
		"empty":           {"", "", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := readSCGIResponse(strings.NewReader(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("readSCGIResponse: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseSCGIURLBase(t *testing.T) {
	cases := map[string]struct {
		urlBase string
		host    string
		port    int
		network string
		address string
		nilOK   bool
		wantErr bool
	}{
		"empty is HTTP":        {"", "box", 8080, "", "", true, false},
		"path is HTTP":         {"/RPC2", "box", 8080, "", "", true, false},
		"ruTorrent path":       {"/plugins/rpc/rpc.php", "box", 8080, "", "", true, false},
		"scgi:// uses host":    {"scgi://", "box", 5000, "tcp", "box:5000", false, false},
		"bare scgi uses host":  {"scgi", "box", 5000, "tcp", "box:5000", false, false},
		"scgi: uses host":      {"scgi:", "box", 5000, "tcp", "box:5000", false, false},
		"case insensitive":     {"SCGI://", "box", 5000, "tcp", "box:5000", false, false},
		"explicit address":     {"scgi://127.0.0.1:5000", "box", 8080, "tcp", "127.0.0.1:5000", false, false},
		"unix socket":          {"scgi:///var/run/rtorrent/rpc.sock", "box", 8080, "unix", "/var/run/rtorrent/rpc.sock", false, false},
		"whitespace tolerated": {"  scgi://  ", "box", 5000, "tcp", "box:5000", false, false},
		"address without port": {"scgi://justahost", "box", 8080, "", "", false, true},
		// net.SplitHostPort accepts an empty half, so these used to save
		// cleanly and then fail at dial time with a bare OS error instead of
		// here with a message that says what to fix.
		"empty port":            {"scgi://myhost:", "box", 8080, "", "", false, true},
		"empty host":            {"scgi://:5000", "box", 8080, "", "", false, true},
		"non-numeric port":      {"scgi://myhost:rpc", "box", 8080, "", "", false, true},
		"out-of-range port":     {"scgi://myhost:70000", "box", 8080, "", "", false, true},
		"scgi with no host":     {"scgi://", "", 5000, "", "", false, true},
		"scgi with bad port":    {"scgi://", "box", 0, "", "", false, true},
		"scgi-ish path is HTTP": {"/scgi", "box", 8080, "", "", true, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parseSCGIURLBase(tc.urlBase, tc.host, tc.port)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSCGIURLBase: %v", err)
			}
			if tc.nilOK {
				if got != nil {
					t.Fatalf("expected the HTTP transport, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected an SCGI dialer")
			}
			if got.network != tc.network || got.address != tc.address {
				t.Fatalf("got %s://%s, want %s://%s", got.network, got.address, tc.network, tc.address)
			}
			// roundTrip computes its read deadline as time.Now().Add(timeout);
			// a zero timeout puts the deadline in the past and every read fails
			// instantly. New fills it in, so a dialer built straight from the
			// parser must carry a usable default rather than relying on that.
			if got.timeout <= 0 {
				t.Fatalf("dialer must carry a non-zero default timeout, got %v", got.timeout)
			}
		})
	}
}

// fakeSCGIServer answers one XML-RPC document per connection, recording the
// request body it was framed with.
func fakeSCGIServer(t *testing.T, reply string) (addr string, received func() string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var last string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			n, _ := conn.Read(buf)
			last = string(buf[:n])
			_, _ = io.WriteString(conn, "Content-Type: text/xml\r\nContent-Length: "+strconv.Itoa(len(reply))+"\r\n\r\n"+reply)
			_ = conn.Close()
		}
	}()
	return ln.Addr().String(), func() string { return last }
}

func TestClient_SCGITransport(t *testing.T) {
	addr, received := fakeSCGIServer(t, stringResponse("0.9.8"))
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	c := New(host, port, "", "", "scgi://", false)
	if c.initErr != nil {
		t.Fatalf("initErr: %v", c.initErr)
	}
	if c.scgi == nil {
		t.Fatal("scgi:// URL base did not select the SCGI transport")
	}

	got, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if got != "0.9.8" {
		t.Fatalf("version: got %q", got)
	}
	if !strings.Contains(received(), "SCGI\x001\x00") {
		t.Errorf("request was not SCGI-framed: %q", received())
	}
	if !strings.Contains(received(), "system.client_version") {
		t.Errorf("request body missing the method name: %q", received())
	}
}

// The whole client — not just the transport — has to work over SCGI, so drive
// a real call that parses a multicall reply.
func TestClient_SCGIGetTorrents(t *testing.T) {
	addr, _ := fakeSCGIServer(t, multicallResponse)
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	c := New(host, port, "", "", "scgi://", false)
	torrents, err := c.GetTorrents(context.Background())
	if err != nil {
		t.Fatalf("GetTorrents: %v", err)
	}
	if len(torrents) != 2 {
		t.Fatalf("got %d torrents, want 2", len(torrents))
	}
	if torrents[0].Hash != "2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e" {
		t.Fatalf("hash: got %q", torrents[0].Hash)
	}
}

func TestClient_SCGIUnixSocket(t *testing.T) {
	// A unix socket path is passed through verbatim; the dial is stubbed so the
	// test doesn't need a filesystem socket.
	c := New("unused", 8080, "", "", "scgi:///var/run/rtorrent.sock", false)
	if c.initErr != nil {
		t.Fatalf("initErr: %v", c.initErr)
	}
	if c.scgi == nil || c.scgi.network != "unix" || c.scgi.address != "/var/run/rtorrent.sock" {
		t.Fatalf("dialer: %+v", c.scgi)
	}

	var gotNetwork, gotAddress string
	c.scgi.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		gotNetwork, gotAddress = network, address
		return nil, fmt.Errorf("stubbed")
	}
	if _, err := c.Version(context.Background()); err == nil {
		t.Fatal("expected the stubbed dial failure to surface")
	}
	if gotNetwork != "unix" || gotAddress != "/var/run/rtorrent.sock" {
		t.Fatalf("dialed %s://%s", gotNetwork, gotAddress)
	}
}

// A listener that accepts and closes without replying is the classic symptom of
// pointing at the wrong port. It must error, not hang or decode empty.
func TestClient_SCGIEmptyReply(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	c := New(host, port, "", "", "scgi://", false)
	c.scgi.timeout = 2 * time.Second

	if _, err := c.Version(context.Background()); err == nil {
		t.Fatal("expected an error when the listener replies with nothing")
	}
}

func TestClient_SCGIBadURLBaseIsAnInitError(t *testing.T) {
	c := New("box", 8080, "", "", "scgi://justahost", false)
	if c.initErr == nil {
		t.Fatal("an unparseable scgi address must be rejected at construction")
	}
	if err := c.Test(context.Background()); err == nil {
		t.Fatal("expected client operations to fail after a bad init")
	}
}
