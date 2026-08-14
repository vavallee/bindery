package rtorrent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// SCGI transport for rTorrent's native XML-RPC socket.
//
// rTorrent does not speak HTTP. Its `scgi_port` / `scgi_local` listener takes
// SCGI, a deliberately tiny protocol: a netstring-framed block of NUL-separated
// header key/value pairs, then the request body verbatim. The reply is an
// HTTP-shaped header block, a blank line, and the XML-RPC document.
//
// The HTTP transport in client.go remains the primary path because it is what
// ruTorrent, Flood and seedbox panels expose, and because it is the only one
// that can carry credentials or TLS. SCGI exists for the plain-rTorrent setup
// with no web server in front of it — which also means no authentication at
// all, so the socket must not be reachable from outside the host.

// scgiDialer opens one connection per call to an rTorrent SCGI listener.
// Connections are not pooled: rTorrent closes the socket after each response,
// so there is nothing to reuse.
type scgiDialer struct {
	// network is "tcp" or "unix"; address is host:port or a socket path.
	network string
	address string
	timeout time.Duration
	// dial is injectable for tests; nil uses a net.Dialer.
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// String renders the dialer for error messages and the Test action. Both a
// host:port and a socket path read correctly after the scheme, so there is one
// spelling for both networks.
func (d *scgiDialer) String() string {
	return "scgi://" + d.address
}

// maxSCGIResponseBytes mirrors the HTTP cap: d.multicall2 over a large session
// is genuinely multi-megabyte, but an unbounded read on a socket that never
// closes would hang the poller forever.
const maxSCGIResponseBytes = maxRPCResponseBytes

func (d *scgiDialer) roundTrip(ctx context.Context, payload []byte) ([]byte, error) {
	// A zero timeout would make the SetDeadline below land in the past and fail
	// every read instantly. parseSCGIURLBase and New both fill it in, so this
	// only catches a dialer built some other way — but "silently broken
	// transport" is a bad failure mode to leave available.
	timeout := d.timeout
	if timeout <= 0 {
		timeout = rpcTimeout
	}
	dial := d.dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: timeout}
		dial = dialer.DialContext
	}
	conn, err := dial(ctx, d.network, d.address)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", d, err)
	}
	defer func() { _ = conn.Close() }()

	// Honour both the caller's deadline and the client timeout, whichever
	// lands first; a socket read has no other cancellation path.
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set deadline on %s: %w", d, err)
	}

	if _, err := conn.Write(encodeSCGIRequest(payload)); err != nil {
		return nil, fmt.Errorf("write to %s: %w", d, err)
	}
	// rTorrent reads until the declared CONTENT_LENGTH, so a half-close is not
	// required — but closing the write side is harmless where supported and
	// makes a mis-framed request fail fast instead of hanging.
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}

	return readSCGIResponse(conn)
}

// encodeSCGIRequest frames an XML-RPC document as an SCGI request.
//
// The header block is a netstring: "<byte-length>:<headers>,". CONTENT_LENGTH
// must come first and SCGI must be "1" — rTorrent rejects the request outright
// otherwise. Every key and value is NUL-terminated, including the last.
func encodeSCGIRequest(body []byte) []byte {
	var headers bytes.Buffer
	writeHeader := func(k, v string) {
		headers.WriteString(k)
		headers.WriteByte(0)
		headers.WriteString(v)
		headers.WriteByte(0)
	}
	writeHeader("CONTENT_LENGTH", strconv.Itoa(len(body)))
	writeHeader("SCGI", "1")
	writeHeader("REQUEST_METHOD", "POST")
	writeHeader("CONTENT_TYPE", "text/xml")

	var out bytes.Buffer
	out.WriteString(strconv.Itoa(headers.Len()))
	out.WriteByte(':')
	out.Write(headers.Bytes())
	out.WriteByte(',')
	out.Write(body)
	return out.Bytes()
}

// readSCGIResponse strips rTorrent's CGI-style header block and returns the
// XML-RPC document. A reply with no blank-line separator is returned whole, on
// the theory that a body is more useful to the XML decoder than an error about
// framing — the decoder's own error names the real problem.
func readSCGIResponse(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxSCGIResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read SCGI response: %w", err)
	}
	if len(data) > maxSCGIResponseBytes {
		return nil, fmt.Errorf("rTorrent SCGI response exceeded %d bytes — too many torrents to poll in one request", maxSCGIResponseBytes)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("rTorrent closed the SCGI connection without replying — check that the listener is rTorrent's scgi_port and not another service")
	}

	// Both separators appear in the wild: rTorrent emits CRLF, but a proxy in
	// between may normalise to LF.
	if idx := bytes.Index(data, []byte("\r\n\r\n")); idx >= 0 {
		return data[idx+4:], nil
	}
	if idx := bytes.Index(data, []byte("\n\n")); idx >= 0 {
		return data[idx+2:], nil
	}
	return data, nil
}

// parseSCGIURLBase recognises the URL-base spellings that select the SCGI
// transport and returns the dialer they describe.
//
//	scgi://              → TCP to the configured host:port
//	scgi                 → the same, for operators who omit the slashes
//	scgi:///var/run/x    → the unix socket at /var/run/x (host/port unused)
//	scgi://host:port     → TCP to that explicit address
//
// Returns (nil, nil) when urlBase does not select SCGI, which is the common
// case — the HTTP endpoint stays the default.
func parseSCGIURLBase(urlBase, host string, port int) (*scgiDialer, error) {
	raw := strings.TrimSpace(urlBase)
	if raw == "" {
		return nil, nil
	}
	lower := strings.ToLower(raw)
	if lower != "scgi" && !strings.HasPrefix(lower, "scgi:") {
		return nil, nil
	}

	// Drop the scheme, however the operator spelled it, leaving either nothing,
	// an absolute socket path, or a host:port.
	rest := strings.TrimPrefix(raw[len("scgi"):], ":")
	rest = strings.TrimSpace(strings.TrimPrefix(rest, "//"))

	switch {
	case rest == "":
		// scgi:// with no address: use the client's own host and port.
		if strings.TrimSpace(host) == "" {
			return nil, fmt.Errorf("rTorrent SCGI transport needs a host")
		}
		if port <= 0 || port > 65535 {
			return nil, fmt.Errorf("invalid rTorrent SCGI port %d", port)
		}
		return &scgiDialer{network: "tcp", address: net.JoinHostPort(host, strconv.Itoa(port)), timeout: rpcTimeout}, nil
	case strings.HasPrefix(rest, "/"):
		// scgi:///path/to/socket — an absolute path is a unix socket.
		return &scgiDialer{network: "unix", address: rest, timeout: rpcTimeout}, nil
	default:
		// scgi://host:port
		addrHost, addrPort, err := net.SplitHostPort(rest)
		if err != nil {
			return nil, fmt.Errorf("rTorrent SCGI address %q must be host:port or an absolute socket path", rest)
		}
		// SplitHostPort accepts "myhost:" and ":5000" — an empty half is not a
		// usable address, but it saves cleanly and then fails at dial time with
		// an OS error instead of here with a message that says what to fix.
		if strings.TrimSpace(addrHost) == "" || strings.TrimSpace(addrPort) == "" {
			return nil, fmt.Errorf("rTorrent SCGI address %q must be host:port or an absolute socket path", rest)
		}
		if n, perr := strconv.Atoi(addrPort); perr != nil || n <= 0 || n > 65535 {
			return nil, fmt.Errorf("rTorrent SCGI address %q has an invalid port", rest)
		}
		return &scgiDialer{network: "tcp", address: rest, timeout: rpcTimeout}, nil
	}
}
