package auth

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// AllowedHostsEnv names the operator allowlist of extra host names that may
// receive the login free grant of the local-only and disabled auth modes.
const AllowedHostsEnv = "BINDERY_ALLOWED_HOSTS"

// oidcRedirectBaseEnv is the one external URL of Bindery itself that the
// process already knows. Its host is accepted without being listed again.
const oidcRedirectBaseEnv = "BINDERY_OIDC_REDIRECT_BASE_URL" //nolint:gosec // env var name, not a credential

// localHostSuffixes are name suffixes that resolve only on the local network
// (or the machine itself) and that nobody can register publicly, so a page on
// another site cannot be served from them.
var localHostSuffixes = []string{
	".local",
	".lan",
	".home.arpa",
	".internal",
	".localdomain",
	".localhost",
}

// HostAllowedForModeGrant reports whether the name the browser used to reach
// Bindery may receive the admin grant the local-only and disabled modes hand
// out without a login.
//
// Those modes decide from the network peer alone. A browser on the LAN that
// loads a page from another site can be made to talk to Bindery's address
// while still treating the page's own name as the origin (DNS rebinding), and
// the peer is then the victim's own LAN address. The one thing that tells that
// request apart from the operator's own tab is the Host header, which carries
// the attacker's name. So the grant is only honoured for names an outside
// site cannot have: IP literals, localhost, single label names, the local
// only suffixes above, the host of BINDERY_OIDC_REDIRECT_BASE_URL, and the
// operator's BINDERY_ALLOWED_HOSTS list (where a bare "*" switches the check
// off; see parseAllowedHosts).
//
// When a trusted proxy forwards X-Forwarded-Host (it is stripped from every
// other peer before this runs) each name in it must pass as well, since that
// is the name the browser actually used.
//
// A request carrying a session cookie or the API key never reaches this
// check: a page on another site cannot obtain either.
func HostAllowedForModeGrant(r *http.Request) bool {
	_, ok := rejectedModeGrantHost(r)
	return ok
}

// rejectedModeGrantHost returns the first host name on r that fails the
// check, and false; or "", true when every name passes.
func rejectedModeGrantHost(r *http.Request) (string, bool) {
	if !hostAllowed(r.Host) {
		return r.Host, false
	}
	for _, v := range r.Header.Values("X-Forwarded-Host") {
		for _, h := range strings.Split(v, ",") {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			if !hostAllowed(h) {
				return h, false
			}
		}
	}
	return "", true
}

// normalizeHost lowercases a Host value and drops its port, IPv6 brackets and
// a single trailing dot ("example.com." is the same name as "example.com").
func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	h = strings.TrimSuffix(h, ".")
	return strings.ToLower(h)
}

func hostAllowed(raw string) bool {
	h := normalizeHost(raw)
	if h == "" {
		// No Host at all (HTTP/1.0). Browsers always send one, so this is
		// never a page on another site.
		return true
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	if h == "localhost" || !strings.Contains(h, ".") {
		return true
	}
	for _, s := range localHostSuffixes {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	if base := hostFromURL(os.Getenv(oidcRedirectBaseEnv)); base != "" && h == base {
		return true
	}
	return matchAllowedHosts(h, os.Getenv(AllowedHostsEnv))
}

// allowedHostList is BINDERY_ALLOWED_HOSTS parsed into what it permits.
type allowedHostList struct {
	any      bool     // a bare "*": the Host check is switched off
	exact    []string // normalised names
	suffixes []string // ".example.com" for an entry "*.example.com"
	rejected []string // entries ignored as unsafe or malformed
}

// parseAllowedHosts parses the comma separated allowlist raw. An entry
// "*.example.com" matches any name under example.com but not example.com
// itself, and the part after "*." must have at least two labels: "*.com"
// would admit every site under a TLD, which is the hole the check closes.
// A bare "*" switches the check off. Entries may carry a port or be a full
// URL; only the host is kept. Any other entry carrying a "*" is rejected.
func parseAllowedHosts(raw string) allowedHostList {
	var out allowedHostList
	for _, e := range strings.Split(raw, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if e == "*" {
			out.any = true
			continue
		}
		if strings.HasPrefix(e, "*.") {
			suffix := normalizeHost(e[2:])
			if !wildcardSuffixOK(suffix) {
				out.rejected = append(out.rejected, e)
				continue
			}
			out.suffixes = append(out.suffixes, "."+suffix)
			continue
		}
		if strings.Contains(e, "*") {
			out.rejected = append(out.rejected, e)
			continue
		}
		var n string
		if strings.Contains(e, "://") {
			n = hostFromURL(e)
		} else {
			n = normalizeHost(e)
		}
		if n == "" {
			out.rejected = append(out.rejected, e)
			continue
		}
		out.exact = append(out.exact, n)
	}
	return out
}

// wildcardSuffixOK requires at least two non empty labels and no further
// wildcard. It cannot tell a shared domain such as duckdns.org from one the
// operator owns (that needs the public suffix list, which Bindery does not
// ship), so the docs say to list exact names under a shared domain instead.
func wildcardSuffixOK(suffix string) bool {
	if strings.Contains(suffix, "*") {
		return false
	}
	labels := strings.Split(suffix, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" {
			return false
		}
	}
	return true
}

func (a allowedHostList) allows(h string) bool {
	if a.any {
		return true
	}
	for _, n := range a.exact {
		if n == h {
			return true
		}
	}
	for _, s := range a.suffixes {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

// matchAllowedHosts reports whether the normalised host h is permitted by
// the allowlist raw. Read on every call so the list is whatever the
// environment says now, and so tests can vary it.
func matchAllowedHosts(h, raw string) bool {
	return parseAllowedHosts(raw).allows(h)
}

// WarnAllowedHostsConfig logs, at startup, each BINDERY_ALLOWED_HOSTS entry
// that is ignored, and a warning when a bare "*" switches the Host check off.
// It reports whether the check is off.
func WarnAllowedHostsConfig() bool {
	a := parseAllowedHosts(os.Getenv(AllowedHostsEnv))
	for _, e := range a.rejected {
		slog.Warn("auth: ignoring "+AllowedHostsEnv+" entry; a wildcard needs at least two labels after *. (for example *.home.example.com)",
			"entry", e, "env", AllowedHostsEnv)
	}
	if a.any {
		slog.Warn("auth: "+AllowedHostsEnv+" contains *, so DNS rebinding protection is off; "+
			"in local-only and disabled mode a web page opened by anyone on your network can act as the admin. "+
			"List the host names you use instead", "env", AllowedHostsEnv)
	}
	return a.any
}

func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return normalizeHost(u.Hostname())
}

// writeHostRejected answers a request the mode would have admitted as the
// admin but whose Host failed HostAllowedForModeGrant. It is a 403 naming the
// variable to set, never a bare 401: an operator reaching Bindery by a name
// of their own has to be able to tell why the UI stopped working.
func writeHostRejected(w http.ResponseWriter, host string) {
	host = clipHost(host)
	hostRejectLog.note(host)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	if _, err := w.Write(hostRejectedBody(host)); err != nil {
		slog.Warn("failed to write host rejected response", "error", err)
	}
}

// hostRejectedBody is the JSON error body for a request refused the mode
// grant because of its host name. It names BINDERY_ALLOWED_HOSTS so the
// operator knows what to set. The host is JSON encoded and clipped.
func hostRejectedBody(host string) []byte {
	body, _ := json.Marshal(map[string]string{
		"error": "host " + clipHost(host) + " is not allowed to use this auth mode without a login; " +
			"sign in, or add the name to " + AllowedHostsEnv + " if this is how you reach Bindery",
	})
	return body
}

// clipHost bounds a caller supplied Host before it is echoed or logged.
func clipHost(h string) string {
	const maxLen = 255
	if len(h) > maxLen {
		return h[:maxLen]
	}
	return h
}

// hostRejectLog logs each rejected host once per window, and at most
// hostRejectLogMax distinct hosts per window, so a page cycling through
// random names cannot fill the log.
var hostRejectLog = &rejectLogger{}

const (
	hostRejectLogWindow = time.Hour
	hostRejectLogMax    = 20
)

type rejectLogger struct {
	mu         sync.Mutex
	start      time.Time
	seen       map[string]struct{}
	suppressed bool
}

func (l *rejectLogger) note(host string) {
	key := normalizeHost(host)
	l.mu.Lock()
	now := time.Now()
	if l.seen == nil || now.Sub(l.start) > hostRejectLogWindow {
		l.start = now
		l.seen = make(map[string]struct{})
		l.suppressed = false
	}
	if _, ok := l.seen[key]; ok {
		l.mu.Unlock()
		return
	}
	if len(l.seen) >= hostRejectLogMax {
		first := !l.suppressed
		l.suppressed = true
		l.mu.Unlock()
		if first {
			slog.Warn("auth: more hosts refused the login free grant; not logging further ones this hour",
				"env", AllowedHostsEnv)
		}
		return
	}
	l.seen[key] = struct{}{}
	l.mu.Unlock()
	slog.Warn("auth: refused the login free grant for a request under an unrecognised host name; "+
		"if this is how you reach Bindery, add it to "+AllowedHostsEnv,
		"host", host, "env", AllowedHostsEnv)
}
