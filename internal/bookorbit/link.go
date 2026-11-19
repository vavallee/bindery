package bookorbit

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// NormalizePublicURL trims and validates the address a browser uses to reach
// BookOrbit. Only http and https are accepted, so a saved value can never turn
// into a javascript: or data: link. Trailing slashes go, a path prefix (a
// reverse proxy serving BookOrbit under /bookorbit) stays, and a query or
// fragment is dropped. An empty value is valid and means "use the server URL".
func NormalizePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("browser address %q is not a URL", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("browser address %q must start with http:// or https://", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("browser address %q is missing a host", raw)
	}
	if u.User != nil {
		return "", errors.New("browser address must not carry a username or password")
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// BookURL is the address of a book in BookOrbit's web UI, or "" when there is
// nothing safe to link to. publicURL is the browser address and wins when it is
// set and valid; otherwise serverURL, the address Bindery itself uses, is the
// fallback. A set but invalid publicURL does not fall back: it means the admin
// asked for a different address, and the server URL could point somewhere the
// browser cannot reach. bookID must be a positive integer, as BookOrbit's are.
func BookURL(publicURL, serverURL, bookID string) string {
	bookID = strings.TrimSpace(bookID)
	if n, err := strconv.ParseInt(bookID, 10, 64); err != nil || n <= 0 {
		return ""
	}
	base := ""
	if strings.TrimSpace(publicURL) != "" {
		normalized, err := NormalizePublicURL(publicURL)
		if err != nil {
			return ""
		}
		base = normalized
	} else if strings.TrimSpace(serverURL) != "" {
		normalized, err := NormalizePublicURL(serverURL)
		if err != nil {
			return ""
		}
		// People paste the API address as the server URL.
		normalized = strings.TrimSuffix(normalized, "/api/v1")
		base = strings.TrimSuffix(normalized, "/api")
	}
	if base == "" {
		return ""
	}
	return base + "/book/" + bookID
}
