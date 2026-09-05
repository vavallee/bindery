// Package clienthost validates and renders the Host field of a download-client
// connection.
package clienthost

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ErrEmpty reports a Host field that is blank once trimmed.
var ErrEmpty = errors.New("host is required")

// invalidHostChars are the characters that cannot appear in the reg-name of a
// URL authority once the scheme, userinfo, port and path have been split off.
// A denylist rather than an allowlist on purpose: the set of hostnames that
// already work in the field is wide (Docker service names carry underscores,
// internal domains carry whatever a resolver accepts) and a stricter rule
// would reject saved connections that work today.
const invalidHostChars = " \t\r\n\v\f/?#@:[]\\%\"'<>{}|^`"

// Normalize returns the value to store in a download client's Host field, or
// an error naming what is wrong with what was typed.
//
// Host is a bare hostname or IP address. The scheme comes from the Use SSL
// flag, the port from the Port field and any reverse-proxy prefix from URL
// Base, so a Host that carries those inline is rejected rather than
// reinterpreted: the Port field is visibly populated on the same form, and
// silently overriding it with a number lifted out of another field is a worse
// outcome than an error saying which box to move it to.
//
// #2203 is what nothing checking costs. "1.2.3.4:8080/#/" pasted from a
// browser address bar became "http://1.2.3.4:8080/#/:8080/", whose fragment
// swallowed the API path, so every request landed on the qBittorrent WebUI's
// index page instead. The Test button reported success and the failure only
// surfaced later, in the poller, as "invalid character '<' looking for
// beginning of value".
//
// Two inputs are normalised instead of rejected because neither is ambiguous:
// a leading "http://" or "https://", which the form already asks operators to
// leave out, and a single trailing slash, which carries nothing. An IPv6
// literal is accepted in either the bare ("::1") or bracketed ("[::1]") form
// and returned as written; Authority renders both correctly.
func Normalize(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrEmpty
	}

	rest := trimmed
	if i := strings.Index(rest, "://"); i >= 0 {
		switch scheme := strings.ToLower(rest[:i]); scheme {
		case "http", "https":
			rest = rest[i+3:]
		default:
			return "", fmt.Errorf("host must be a hostname or IP address, not a URL: %q starts with a scheme Bindery cannot use. Use the Use SSL checkbox to choose between http and https", trimmed)
		}
	}
	if strings.Contains(rest, "@") {
		return "", fmt.Errorf("host must be a hostname or IP address only: %q includes a username. Put credentials in the Username and Password fields", trimmed)
	}

	authority, extra := cutExtra(rest)
	host, port, err := splitPort(authority, trimmed)
	if err != nil {
		return "", err
	}
	if err := checkHost(host, trimmed); err != nil {
		return "", err
	}

	var problems []string
	if port != "" {
		problems = append(problems, "a port")
	}
	if extra != "" {
		problems = append(problems, describeExtra(extra))
	}
	if len(problems) == 0 {
		return host, nil
	}

	advice := ""
	if port != "" {
		advice = fmt.Sprintf(" and %s as the port", port)
	}
	return "", fmt.Errorf("host must be a hostname or IP address only, with no port and no path: %q includes %s. Use %q as the host%s",
		trimmed, strings.Join(problems, " and "), host, advice)
}

// Authority renders host and port as a URL authority, bracketing an IPv6
// literal exactly once whichever form the Host field holds. Stored hosts
// predate any single convention: the field has accepted "::1" and "[::1]"
// alike, and the two spellings broke on opposite halves of the codebase,
// because fmt.Sprintf("%s:%d") needs the brackets while net.JoinHostPort adds
// a second pair to a host that already carries them.
func Authority(host string, port int) string {
	return net.JoinHostPort(Unbracket(strings.TrimSpace(host)), strconv.Itoa(port))
}

// URL renders the base URL a download client would be reached on from the
// three fields the connection is stored as, so a caller holding a Host, a Port
// and a Use SSL flag can hand a single string to httpsec.ValidateOutboundURL.
//
// A zero port falls back to 8080. Nothing here builds a request that is
// actually sent, and the SSRF check reads only the scheme and the host, so the
// fallback exists to keep the string parseable rather than to guess a real
// port.
func URL(host string, port int, ssl bool) string {
	scheme := "http"
	if ssl {
		scheme = "https"
	}
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("%s://%s/", scheme, Authority(host, port))
}

// Unbracket strips the brackets from an IPv6 literal, leaving every other
// host untouched.
func Unbracket(host string) string {
	if len(host) > 1 && host[0] == '[' && host[len(host)-1] == ']' {
		return host[1 : len(host)-1]
	}
	return host
}

// cutExtra splits an authority from whatever path, query or fragment follows
// it. A lone trailing slash is dropped rather than reported: "10.0.0.5/" and
// "10.0.0.5" are the same address, and browsers append the slash themselves.
func cutExtra(s string) (authority, extra string) {
	i := strings.IndexAny(s, "/?#")
	if i < 0 {
		return s, ""
	}
	if s[i:] == "/" {
		return s[:i], ""
	}
	return s[:i], s[i:]
}

// splitPort separates a trailing ":port" from the host. An IPv6 literal is
// the reason this cannot be a plain LastIndex on ":": "::1" is a whole
// address, not a host of ":" and a port of "1".
func splitPort(authority, original string) (host, port string, err error) {
	if authority == "" {
		return "", "", ErrEmpty
	}
	if strings.HasPrefix(authority, "[") {
		end := strings.Index(authority, "]")
		if end < 0 || net.ParseIP(authority[1:end]) == nil {
			return "", "", fmt.Errorf("host must be a hostname or IP address: %q is not a valid bracketed IPv6 address", original)
		}
		switch tail := authority[end+1:]; {
		case tail == "":
			return authority, "", nil
		case strings.HasPrefix(tail, ":") && validPort(tail[1:]):
			return authority[:end+1], tail[1:], nil
		default:
			return "", "", fmt.Errorf("host must be a hostname or IP address: %q is not a valid bracketed IPv6 address", original)
		}
	}
	// A bare IPv6 literal, colons and all.
	if net.ParseIP(authority) != nil {
		return authority, "", nil
	}
	i := strings.LastIndex(authority, ":")
	if i < 0 {
		return authority, "", nil
	}
	if !validPort(authority[i+1:]) {
		return "", "", fmt.Errorf("host must be a hostname or IP address only: %q is neither", original)
	}
	return authority[:i], authority[i+1:], nil
}

// validPort reports whether s is a decimal port number in range.
func validPort(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && n <= 65535
}

// checkHost rejects a reg-name that cannot survive being placed in a URL
// authority.
func checkHost(host, original string) error {
	if strings.HasPrefix(host, "[") {
		return nil // already validated as a bracketed IPv6 literal
	}
	if net.ParseIP(host) != nil {
		return nil // an IP literal, colons and all
	}
	if host == "" || strings.Trim(host, ".") == "" || strings.ContainsAny(host, invalidHostChars) {
		return fmt.Errorf("host must be a hostname or IP address only: %q is neither", original)
	}
	return nil
}

// describeExtra names the trailing component in the words an operator sees in
// their own address bar.
func describeExtra(extra string) string {
	switch extra[0] {
	case '#':
		return "a fragment"
	case '?':
		return "a query string"
	default:
		return "a path"
	}
}
