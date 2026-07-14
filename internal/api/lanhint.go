package api

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
)

// lanTimeoutHint returns err's message, appending a VPN-killswitch pointer
// when a connection test timed out against a LAN-looking host (#1474). The
// recurring case: Bindery shares a VPN container's network namespace
// (network_mode: service:gluetun) and the killswitch silently drops
// LAN-bound packets, so "Test connection" to an on-LAN Audiobookshelf or
// Calibre dies with a bare Client.Timeout while the same service loads fine
// in a browser. The raw timeout gives the operator nothing to go on; naming
// the likely cause and the fix turns a support thread into a one-liner.
//
// Only fires for a genuine timeout AND a private/LAN-shaped host, so real
// upstream errors (auth failures, 404s, refused connections) and public
// hosts keep their unmodified message. Host shape is judged without DNS
// lookups — the error path must never block on resolution.
func lanTimeoutHint(rawURL string, err error) string {
	if err == nil {
		return ""
	}
	if !isTimeoutErr(err) {
		return err.Error()
	}
	u, perr := url.Parse(rawURL)
	if perr != nil || !looksLANHost(u.Hostname()) {
		return err.Error()
	}
	return err.Error() + " — the host looks LAN-local; if Bindery runs inside a VPN container's network (network_mode: service:gluetun), the VPN killswitch blocks LAN traffic by default — allow your LAN CIDR via FIREWALL_OUTBOUND_SUBNETS on the VPN container (see the deployment docs, \"Running Bindery behind a VPN\")"
}

// isTimeoutErr reports whether err is a deadline/timeout failure, from either
// the context layer or the net stack (http.Client timeouts satisfy net.Error).
func isTimeoutErr(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// looksLANHost reports whether host is plausibly on the operator's LAN:
// a private/loopback/link-local IP literal, a bare single-label hostname
// (Docker service names, mDNS-less LAN hosts), or a conventional local
// suffix. Deliberately lookup-free.
func looksLANHost(host string) bool {
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
	}
	if !strings.Contains(host, ".") {
		return true
	}
	lower := strings.ToLower(host)
	for _, suffix := range []string{".local", ".lan", ".internal", ".home", ".home.arpa"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}
