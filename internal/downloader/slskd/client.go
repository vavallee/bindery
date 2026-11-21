// Package slskd provides a client for the slskd (Soulseek daemon) REST API.
//
// It covers reachability, version, login state, the API key's role and the
// downloads folder, and interactive search (search.go). Downloading is built on
// top of it later.
//
// Privacy note that belongs next to the code: every searched title leaves as
// search text to the Soulseek network, and a download exposes the host's IP
// address to the peer. Soulseek downloads should also run with the content
// check in block mode, because peers are anonymous; that is a later change and
// is only recorded here.
package slskd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/downloader/clienthost"
	"github.com/vavallee/bindery/internal/downloader/nethint"
	"github.com/vavallee/bindery/internal/downloader/urlbase"
	"github.com/vavallee/bindery/internal/httpsec"
)

// MinVersion is the oldest slskd Bindery supports. The batch download
// endpoint that grabs use first appears in 0.26.0.
const MinVersion = "0.26.0"

// Response size caps. slskd answers these endpoints with small documents; a
// reply past the cap is a wrong service or a hostile one, not a bigger answer.
const (
	maxApplicationBytes = 1 << 20
	maxOptionsBytes     = 4 << 20
)

// Client talks to one slskd instance.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client

	// searchSem admits one search at a time, because slskd answers a second
	// concurrent POST /searches with 429. Capacity one, so it is a mutex that
	// a waiting caller can give up on.
	searchSem chan struct{}
	// Search tuning. Zero values use the package defaults; tests shorten them.
	searchTimeout time.Duration
	searchWait    time.Duration
	pollInterval  time.Duration
	searchGrace   time.Duration
}

// New creates a slskd client. urlBase is the optional reverse-proxy subpath
// placed between host:port and /api.
func New(host string, port int, apiKey, urlBase string, useSSL bool) *Client {
	scheme := "http"
	if useSSL {
		scheme = "https"
	}
	return &Client{
		baseURL:   fmt.Sprintf("%s://%s%s", scheme, clienthost.Authority(host, port), urlbase.Normalize(urlBase)),
		apiKey:    apiKey,
		searchSem: make(chan struct{}, 1),
		// Dialled directly, never via BINDERY_OUTBOUND_PROXY, with the dial and
		// every redirect hop checked against the service policy.
		http: &http.Client{
			Timeout:       15 * time.Second,
			Transport:     httpsec.DirectGuardedTransport(httpsec.PolicyService),
			CheckRedirect: httpsec.RedirectGuard(httpsec.PolicyService),
		},
	}
}

// WrapTransport replaces the client's transport with wrap applied to it. The
// client cache uses it to count and time requests.
func (c *Client) WrapTransport(wrap func(http.RoundTripper) http.RoundTripper) {
	c.http.Transport = wrap(c.http.Transport)
}

// Application is the part of GET /api/v0/application Bindery reads.
type Application struct {
	Version struct {
		Current string `json:"current"`
	} `json:"version"`
	Server struct {
		IsConnected bool `json:"isConnected"`
		IsLoggedIn  bool `json:"isLoggedIn"`
	} `json:"server"`
	Vpn struct {
		ForwardedPort *int `json:"forwardedPort"`
	} `json:"vpn"`
}

// StatusError is a non-2xx answer from slskd.
type StatusError struct {
	Status int
}

func (e *StatusError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return "slskd rejected the API key (HTTP 401)"
	case http.StatusForbidden:
		return "slskd refused the request (HTTP 403)"
	}
	return "slskd answered HTTP " + strconv.Itoa(e.Status)
}

// do sends one request. The key travels only in the X-API-Key header, never in
// a query string. On success it decodes at most max bytes of JSON into target
// (nil skips the body). Errors never carry the response body, which is peer
// influenced, and every URL is redacted.
func (c *Client) do(ctx context.Context, method, path string, max int64, target any) (int, error) {
	return c.doBody(ctx, method, path, nil, max, target)
}

// doBody is do with an optional JSON request body.
func (c *Client) doBody(ctx context.Context, method, path string, body any, max int64, target any) (int, error) {
	u := c.baseURL + path
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("encode slskd request: %w", err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return 0, fmt.Errorf("build request for %s: %w", httpsec.RedactSecrets(u), httpsec.RedactURLError(err))
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, httpsec.RedactSecrets(u), httpsec.RedactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, &StatusError{Status: resp.StatusCode}
	}
	if target == nil {
		return resp.StatusCode, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read slskd response: %w", httpsec.RedactURLError(err))
	}
	if int64(len(raw)) > max {
		return resp.StatusCode, fmt.Errorf("slskd response is larger than %d bytes, so this is probably not slskd", max)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return resp.StatusCode, errors.New("slskd answered with something that is not JSON, so this is probably not slskd")
	}
	return resp.StatusCode, nil
}

// Application reads the application state. It proves the address and API key
// work and reports the version, the Soulseek login and the VPN forwarded port.
func (c *Client) Application(ctx context.Context) (*Application, error) {
	var app Application
	if _, err := c.do(ctx, http.MethodGet, "/api/v0/application", maxApplicationBytes, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

// Ping checks that slskd answers with the stored key and nothing more.
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.Application(ctx); err != nil {
		return fmt.Errorf("could not reach slskd at %s: %w%s", c.baseURL, err, nethint.ForErr(err))
	}
	return nil
}

// Test is the save time check: slskd answers, is new enough, and is logged in
// to the Soulseek network.
func (c *Client) Test(ctx context.Context) error {
	app, err := c.Application(ctx)
	if err != nil {
		return fmt.Errorf("could not reach slskd at %s: %w%s", c.baseURL, err, nethint.ForErr(err))
	}
	if err := CheckVersion(app.Version.Current); err != nil {
		return err
	}
	if !app.Server.IsLoggedIn {
		return errors.New("slskd is running but is not logged in to the Soulseek network. Check the Soulseek username and password in slskd")
	}
	return nil
}

// CheckVersion reports an error when version is older than MinVersion or
// cannot be read.
func CheckVersion(version string) error {
	have, ok := parseVersion(version)
	if !ok {
		return fmt.Errorf("slskd reported a version Bindery cannot read (%q). Version %s or newer is required", truncate(version, 40), MinVersion)
	}
	want, _ := parseVersion(MinVersion)
	if compareVersion(have, want) < 0 {
		return fmt.Errorf("slskd %s is too old. Bindery needs %s or newer, so update slskd", truncate(version, 40), MinVersion)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

type semver [3]int

// parseVersion reads "0.26.0", "v0.26.0" or "0.26.0-canary+abc". Anything
// after the third number's digits is ignored.
func parseVersion(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+ "); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return semver{}, false
	}
	var v semver
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return semver{}, false
		}
		v[i] = n
	}
	return v, true
}

func compareVersion(a, b semver) int {
	for i := range a {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// Role is what the API key may do.
type Role int

const (
	// RoleUnknown means the probe got an answer it cannot interpret.
	RoleUnknown Role = iota
	// RoleWrite means the key can search and download.
	RoleWrite
	// RoleReadOnly means the key can only read. slskd's default keys are
	// read only, so a key made without the role is the usual mistake.
	RoleReadOnly
)

// ProbeRole finds out whether the key has a write role without changing
// anything. It cancels a search that does not exist: the cancel action is
// restricted to ReadWriteOrAdministrator, which ASP.NET enforces before the
// action runs, so a read only key is refused with 403 and a write key reaches
// the lookup and gets 404 (SearchesController, Cancel). Any other answer is
// RoleUnknown with the status in the error, never a guess.
func (c *Client) ProbeRole(ctx context.Context) (Role, error) {
	id, err := randomGUID()
	if err != nil {
		return RoleUnknown, err
	}
	status, err := c.do(ctx, http.MethodPut, "/api/v0/searches/"+id, 1<<10, nil)
	switch {
	case status == http.StatusForbidden:
		return RoleReadOnly, nil
	case status == http.StatusNotFound:
		return RoleWrite, nil
	case err != nil:
		return RoleUnknown, err
	default:
		return RoleUnknown, fmt.Errorf("slskd answered the role probe with HTTP %d", status)
	}
}

func randomGUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// DownloadsDir reads directories.downloads from GET /api/v0/options, the
// folder slskd moves finished files into. Empty with no error means slskd did
// not report one.
func (c *Client) DownloadsDir(ctx context.Context) (string, error) {
	var opts struct {
		Directories struct {
			Downloads string `json:"downloads"`
		} `json:"directories"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/api/v0/options", maxOptionsBytes, &opts); err != nil {
		return "", err
	}
	return strings.TrimSpace(opts.Directories.Downloads), nil
}
