// Package rtorrent provides a client for rTorrent's XML-RPC API, used to
// submit magnet/torrent URLs and poll status for torrent downloads.
//
// Two transports, because rTorrent is deployed two ways:
//
//   - HTTP(S), the default. ruTorrent, Flood, the linuxserver/rutorrent image
//     and every seedbox panel front rTorrent's socket with nginx or lighttpd
//     and expose an XML-RPC endpoint (/RPC2, or /plugins/rpc/rpc.php under
//     ruTorrent). This path reuses Bindery's existing download-client
//     plumbing: base URL, basic auth, TLS, and guarded redirects. The endpoint
//     itself is SSRF-validated when the client is saved
//     (httpsec.ValidateOutboundURL in the API handler); per-call, the guard is
//     validateRequestTarget, which pins every request and every redirect hop to
//     the exact configured scheme/host/path. Same posture as Transmission.
//   - SCGI, rTorrent's native listener (see scgi.go). For a plain rTorrent
//     with no web server in front of it. It carries no credentials and no TLS
//     — that is the protocol, not a gap here — so the listener must not be
//     reachable from outside its host.
//
// Field mapping for DownloadClient storage:
//   - Username/Password → HTTP basic auth on the RPC endpoint (HTTP only)
//   - URLBase           → the RPC path, or an scgi:// address; see New
//   - Category          → the ruTorrent label, stored in d.custom1
package rtorrent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vavallee/bindery/internal/downloader/infohash"
	"github.com/vavallee/bindery/internal/downloader/nethint"
	"github.com/vavallee/bindery/internal/downloader/urlbase"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/useragent"
)

// DefaultRPCPath is used when the client has no URL base configured. It is
// what a stock nginx/lighttpd SCGI passthrough exposes.
const DefaultRPCPath = "/RPC2"

// maxTorrentFileBytes caps the .torrent payload Bindery will fetch from an
// indexer before submitting it. Matches the qBittorrent/Deluge/Transmission cap.
var maxTorrentFileBytes int64 = 50 << 20

// maxRPCResponseBytes caps an rTorrent RPC reply. d.multicall2 returns every
// torrent in one document and XML is verbose, so this is generous; readRPCBody
// turns an over-limit reply into a clear error instead of a truncated parse.
const maxRPCResponseBytes = 64 << 20

// addPollAttempts / addPollInterval bound the wait for a freshly-loaded torrent
// to appear in rTorrent's download list. load.start and load.raw_start return 0
// the moment the command is queued, not when the torrent exists, so AddTorrent
// has to confirm separately.
const (
	addPollAttempts = 10
	addPollInterval = 300 * time.Millisecond
)

// Client interacts with rTorrent's XML-RPC API.
type Client struct {
	baseURL  string
	rpcURL   *url.URL
	initErr  error
	username string
	password string
	http     *http.Client // HTTP RPC transport (validated target)
	// scgi is non-nil when the client was configured with an scgi:// URL base,
	// in which case it replaces the HTTP transport entirely.
	scgi *scgiDialer
	// fetchTransport pulls indexer-controlled .torrent URLs under the
	// download-fetch SSRF policy, separately from the RPC transport which
	// targets the admin-configured client.
	fetchTransport http.RoundTripper
	// validateTorrentURL is injectable for tests; nil uses httpsec.ValidateOutboundURL.
	validateTorrentURL func(string) error
	// pollInterval is the sleep between add-confirmation attempts. Overridden
	// in tests so they do not spend three seconds confirming a torrent.
	pollInterval time.Duration
}

// rpcTimeout bounds a single RPC round trip on either transport. Generous
// because d.multicall2 over a session with thousands of torrents is a large
// document that rTorrent assembles synchronously.
const rpcTimeout = 30 * time.Second

// New creates an rTorrent client.
//
// urlBase selects both the transport and the endpoint:
//
//	""                  HTTP(S) POST to DefaultRPCPath
//	"/some/path"        HTTP(S) POST to that exact path
//	"scgi://"           rTorrent's SCGI listener on the given host:port
//	"scgi://host:port"  the same, at an explicit address
//	"scgi:///path"      the SCGI unix socket at /path (host and port unused)
//
// An HTTP urlBase is the full path of the XML-RPC endpoint, not a prefix:
// rTorrent deployments expose one specific path and nothing else lives under
// it.
//
// useSSL applies to the HTTP transport only; SCGI has neither TLS nor
// authentication, which is why it must never be exposed off-host.
func New(host string, port int, username, password, urlBase string, useSSL bool) *Client {
	scheme := "http"
	if useSSL {
		scheme = "https"
	}

	c := &Client{
		username:       username,
		password:       password,
		http:           &http.Client{Timeout: rpcTimeout},
		fetchTransport: httpsec.GuardedTransport(httpsec.DownloadFetchPolicy()),
		pollInterval:   addPollInterval,
	}
	c.http.CheckRedirect = c.checkRedirect

	scgi, err := parseSCGIURLBase(urlBase, host, port)
	if err != nil {
		c.initErr = err
		return c
	}
	if scgi != nil {
		scgi.timeout = rpcTimeout
		c.scgi = scgi
		c.baseURL = scgi.String()
		return c
	}

	rpcURL, err := buildRPCURL(scheme, host, port, urlBase)
	if err != nil {
		c.initErr = err
	} else {
		c.rpcURL = rpcURL
		c.baseURL = rpcURL.String()
	}
	return c
}

func buildRPCURL(scheme, host string, port int, urlBase string) (*url.URL, error) {
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("rTorrent host is empty")
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#@") {
		return nil, fmt.Errorf("rTorrent host must be a bare hostname or IP address")
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid rTorrent port %d", port)
	}
	path := urlbase.Normalize(urlBase)
	if path == "" {
		path = DefaultRPCPath
	}
	return &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   path,
	}, nil
}

// Test verifies connectivity and that the endpoint really is rTorrent's
// XML-RPC socket rather than, say, ruTorrent's HTML UI.
//
// Two probes, because they fail differently and users hit both: a wrong
// host/port/credential fails system.client_version, while a correct host
// pointed at the wrong path (the ruTorrent web root instead of /RPC2) returns
// HTTP 200 with HTML that only fails once something tries to parse it.
func (c *Client) Test(ctx context.Context) error {
	version, err := c.Version(ctx)
	if err != nil {
		if hint := nethint.ForErr(err); hint != "" {
			return fmt.Errorf("could not reach rTorrent at %s — %w%s", c.baseURL, err, hint)
		}
		return fmt.Errorf("could not reach rTorrent at %s — %w", c.baseURL, err)
	}
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("rTorrent at %s answered but reported no version — check that the URL path points at the XML-RPC endpoint (usually /RPC2, or /plugins/rpc/rpc.php under ruTorrent)", c.baseURL)
	}
	if _, err := c.GetTorrents(ctx, ""); err != nil {
		return fmt.Errorf("rTorrent at %s reported version %s but the download list could not be read: %w", c.baseURL, version, err)
	}
	return nil
}

// Version returns rTorrent's reported client version (system.client_version).
func (c *Client) Version(ctx context.Context) (string, error) {
	v, err := c.call(ctx, "system.client_version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(v.stringValue()), nil
}

// DefaultDirectory returns rTorrent's configured default download directory
// (directory.default). Used by the Test action's path-visibility check so a
// remote rTorrent whose completed files Bindery cannot see is reported at save
// time rather than silently failing every import (#1182).
func (c *Client) DefaultDirectory(ctx context.Context) (string, error) {
	v, err := c.call(ctx, "directory.default")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(v.stringValue()), nil
}

// multicallFields is the column list for GetTorrents. Order is load-bearing:
// it must match the field assignment in torrentFromRow.
var multicallFields = []string{
	"d.name=",
	"d.hash=",
	"d.base_path=",
	"d.directory=",
	"d.custom1=",
	"d.size_bytes=",
	"d.left_bytes=",
	"d.down.rate=",
	"d.complete=",
	"d.is_active=",
	"d.is_open=",
	"d.message=",
}

// GetTorrents returns every torrent in rTorrent's "main" view, optionally
// filtered to those carrying one of the given ruTorrent labels (d.custom1).
// Blank labels are ignored; if no non-blank label is supplied, every torrent is
// returned.
//
// Multiple labels are accepted because a client can grab ebooks and audiobooks
// under different labels (#700). rTorrent has no server-side label filter — the
// multicall returns everything regardless — so filtering locally over one call
// is strictly cheaper than qBittorrent's per-category fetches.
//
// "main" is rTorrent's built-in view holding every download; the alternative
// views (started, complete, seeding, …) are subsets that would hide a paused
// or errored torrent Bindery is still tracking.
func (c *Client) GetTorrents(ctx context.Context, labels ...string) ([]Torrent, error) {
	args := []arg{"", "main"}
	for _, f := range multicallFields {
		args = append(args, f)
	}
	v, err := c.call(ctx, "d.multicall2", args...)
	if err != nil {
		return nil, fmt.Errorf("d.multicall2: %w", err)
	}

	want := make([]string, 0, len(labels))
	for _, l := range labels {
		if l = strings.TrimSpace(l); l != "" {
			want = append(want, l)
		}
	}

	rows, droppedShape := v.rows()
	out := make([]Torrent, 0, len(rows))
	droppedRows := 0
	for i := range rows {
		t, ok := torrentFromRow(rows[i])
		if !ok {
			droppedRows++
			continue
		}
		if !matchesLabel(t.Label, want) {
			continue
		}
		out = append(out, t)
	}
	// Dropping rows is the correct behaviour — a short or hash-less row means
	// the column order no longer matches multicallFields, and decoding it
	// anyway would attribute one torrent's size to another's hash. Dropping
	// them *silently* is not: if a future rTorrent changes the reply shape,
	// every torrent vanishes from the poll, the importer's
	// blockStaleImportFailures treats the session as authoritative and
	// terminally blocks every import_failed download, and nothing anywhere
	// says why.
	if droppedShape > 0 || droppedRows > 0 {
		slog.Warn("rtorrent: ignored unusable rows in the d.multicall2 reply — Bindery will not see those torrents; if this is every torrent, rTorrent's reply shape has changed",
			"not_an_array", droppedShape, "short_or_hashless", droppedRows, "usable", len(rows)-droppedRows)
	}
	return out, nil
}

// matchesLabel reports whether a torrent's label is one the caller asked for.
// An empty want list means "no filter" rather than "match nothing" — that is
// how a client configured without a category polls everything.
func matchesLabel(label string, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if strings.EqualFold(label, w) {
			return true
		}
	}
	return false
}

// torrentFromRow maps one d.multicall2 row onto a Torrent. A row with fewer
// columns than requested is dropped rather than partially decoded: a short row
// means the field order no longer matches multicallFields, and silently
// shifting columns would attribute one torrent's size to another's hash.
func torrentFromRow(row []xmlValue) (Torrent, bool) {
	if len(row) < len(multicallFields) {
		return Torrent{}, false
	}
	sizeBytes, _ := row[5].int64Value()
	leftBytes, _ := row[6].int64Value()
	downRate, _ := row[7].int64Value()
	t := Torrent{
		Name:      row[0].stringValue(),
		Hash:      strings.ToLower(strings.TrimSpace(row[1].stringValue())),
		BasePath:  strings.TrimSpace(row[2].stringValue()),
		Directory: strings.TrimSpace(row[3].stringValue()),
		Label:     decodeLabel(row[4].stringValue()),
		SizeBytes: sizeBytes,
		LeftBytes: leftBytes,
		DownRate:  downRate,
		Complete:  row[8].boolValue(),
		IsActive:  row[9].boolValue(),
		IsOpen:    row[10].boolValue(),
		Message:   strings.TrimSpace(row[11].stringValue()),
	}
	if t.Hash == "" {
		return Torrent{}, false
	}
	return t, true
}

// encodeLabel / decodeLabel bridge Bindery's plain label strings and
// ruTorrent's convention of percent-encoding the value it stores in d.custom1.
// Encoding on write keeps a label with a space ("sci fi") readable in
// ruTorrent's sidebar; decoding on read means a label ruTorrent wrote and a
// label Bindery wrote compare equal.
func encodeLabel(label string) string {
	return url.PathEscape(strings.TrimSpace(label))
}

func decodeLabel(raw string) string {
	decoded, err := url.PathUnescape(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return decoded
}

// quoteCommandArg renders a value for the right-hand side of a command string
// passed to load.start / load.raw_start, e.g. `d.directory.set=<value>`.
//
// Those trailing arguments are not data: rTorrent parses each one as a command
// and splits its argument list on commas, so a download directory containing a
// comma ("/media/Author, Name/") silently becomes two arguments and the
// directory is set to the fragment before the comma. Wrapping the value in
// double quotes is exactly how ruTorrent's own addtorrent.php passes a
// directory, and rTorrent's parser unescapes \" and \\ inside the quotes.
//
// Quoting unconditionally rather than only when a comma is present keeps one
// code path under test instead of two.
func quoteCommandArg(v string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(v) + `"`
}

// redactedSource renders the grabbed URL for a log line with any indexer apikey
// stripped and the length bounded — a magnet URI carries a full tracker list
// and would otherwise dominate the log.
func redactedSource(raw string) string {
	s := httpsec.RedactSecrets(strings.TrimSpace(raw))
	const maxLen = 120
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// AddTorrent submits a magnet link or torrent URL to rTorrent and returns the
// torrent's lower-case hex infohash.
//
// The hash is computed by Bindery, not reported by rTorrent: load.start and
// load.raw_start both return 0 on success and carry no identifier. For a
// magnet the infohash is the btih topic; for a .torrent it is the SHA-1 of the
// bencoded info dictionary. A torrent whose hash cannot be derived is refused
// rather than added blind — Bindery would have nothing to poll and the grab
// would sit at "downloading" forever.
//
// http(s) URLs are fetched by Bindery rather than handed to rTorrent, matching
// every other client (#873): in a split-network setup (rTorrent behind a VPN
// container) the daemon frequently cannot reach the indexer at all.
//
// directory, when non-empty, is set as the torrent's download directory
// (d.directory.set). label, when non-empty, becomes the ruTorrent label.
//
// seedRatio is accepted for interface symmetry but not applied: rTorrent has
// no per-torrent ratio limit reachable over plain XML-RPC — ratio handling
// lives in global "ratio groups" configured in .rtorrent.rc — so an override
// is logged and skipped rather than silently appearing to work.
func (c *Client) AddTorrent(ctx context.Context, magnetOrURL, label, directory string, seedRatio *float64) (string, error) {
	if seedRatio != nil {
		// Named so the line is actionable on its own: "ratio" alone left an
		// operator with several indexers and several clients no way to tell
		// which grab it referred to. A Prowlarr sync can also set a ratio the
		// user never chose, so the message says where to look.
		slog.Warn("rtorrent: per-indexer seed-ratio override ignored — rTorrent has no per-torrent ratio limit over XML-RPC; configure a ratio group in .rtorrent.rc instead, or clear the seed ratio on the indexer (Settings → Indexers) to stop this warning",
			"ratio", *seedRatio, "client", c.baseURL, "label", label, "source", redactedSource(magnetOrURL))
	}

	commands := make([]arg, 0, 2)
	if l := strings.TrimSpace(label); l != "" {
		commands = append(commands, "d.custom1.set="+quoteCommandArg(encodeLabel(l)))
	}
	if d := strings.TrimSpace(directory); d != "" {
		commands = append(commands, "d.directory.set="+quoteCommandArg(d))
	}

	var (
		hash       string
		fromMagnet bool
	)
	if isMagnetLink(magnetOrURL) {
		hash = infohash.Normalize(infohash.FromMagnet(magnetOrURL))
		if hash == "" {
			return "", fmt.Errorf("magnet link carries no usable btih infohash — rTorrent downloads are tracked by hash, so this release cannot be monitored")
		}
		fromMagnet = true
		if err := c.load(ctx, "load.start", append([]arg{"", magnetOrURL}, commands...)...); err != nil {
			return "", err
		}
	} else {
		fetched, err := c.fetchTorrentContent(ctx, magnetOrURL)
		if err != nil {
			return "", err
		}
		// An indexer http(s) link can 30x-redirect to a magnet: URI (common on
		// public trackers surfaced via Prowlarr/Jackett). There is no .torrent
		// to submit in that case — fall through to the magnet path (#1006).
		if fetched.magnetURL != "" {
			hash = infohash.Normalize(infohash.FromMagnet(fetched.magnetURL))
			if hash == "" {
				return "", fmt.Errorf("indexer redirected to a magnet link with no usable btih infohash")
			}
			fromMagnet = true
			if err := c.load(ctx, "load.start", append([]arg{"", fetched.magnetURL}, commands...)...); err != nil {
				return "", err
			}
		} else {
			hash = infohash.Normalize(infohash.FromTorrentFile(fetched.data))
			if hash == "" {
				return "", fmt.Errorf("could not compute an infohash from the .torrent the indexer returned — the response may not be a torrent file")
			}
			if err := c.load(ctx, "load.raw_start", append([]arg{"", blob(fetched.data)}, commands...)...); err != nil {
				return "", err
			}
		}
	}

	if c.waitForTorrent(ctx, hash) {
		return hash, nil
	}
	if fromMagnet {
		// A magnet is loaded as a "<hash>.meta" placeholder until rTorrent pulls
		// the metadata off the DHT, which can outlast any sane add timeout. The
		// hash is already correct, so hand it back and let the poller pick the
		// torrent up when it materialises.
		slog.Warn("rtorrent: magnet not visible in the download list yet — rTorrent is still resolving metadata",
			"hash", hash, "waited", time.Duration(addPollAttempts)*c.pollInterval)
		return hash, nil
	}
	return "", fmt.Errorf("rTorrent accepted the torrent but it did not appear in the download list within %s — check rTorrent's log for a load failure", time.Duration(addPollAttempts)*c.pollInterval)
}

// load issues a load.* command and enforces the documented "returns 0 on
// success" contract. rTorrent reports a rejected metafile as a non-zero return
// rather than a fault, so skipping this check would make a failed add look
// like a successful one.
func (c *Client) load(ctx context.Context, method string, args ...arg) error {
	v, err := c.call(ctx, method, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	code, err := v.int64Value()
	if err != nil {
		// Some proxies answer with an empty <value/>; treat an undecodable
		// reply as success only if the call itself did not fault, which it
		// did not (err was nil above).
		return nil
	}
	if code != 0 {
		return fmt.Errorf("%s: rTorrent refused the torrent (return code %d)", method, code)
	}
	return nil
}

// waitForTorrent polls until the hash shows up in rTorrent's download list.
// Necessary because load.* returns as soon as the command is queued.
func (c *Client) waitForTorrent(ctx context.Context, hash string) bool {
	for attempt := 0; attempt < addPollAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(c.pollInterval):
			}
		}
		if ok, err := c.HasTorrent(ctx, hash); err == nil && ok {
			return true
		}
	}
	return false
}

// HasTorrent reports whether rTorrent currently holds the given infohash.
// An unknown hash makes rTorrent answer with a fault, which is a normal
// negative answer here, not a transport error.
func (c *Client) HasTorrent(ctx context.Context, hash string) (bool, error) {
	v, err := c.call(ctx, "d.hash", wireHash(hash))
	if err != nil {
		if isFault(err) {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(v.stringValue()) != "", nil
}

// BasePath returns d.base_path for a torrent — the torrent's own file or
// directory on the rTorrent host. Empty means rTorrent has not resolved the
// metadata yet (a magnet still fetching) or the item is closed.
func (c *Client) BasePath(ctx context.Context, hash string) (string, error) {
	v, err := c.call(ctx, "d.base_path", wireHash(hash))
	if err != nil {
		return "", fmt.Errorf("d.base_path: %w", err)
	}
	return strings.TrimSpace(v.stringValue()), nil
}

// Files returns the per-torrent file list. Names are paths relative to the
// torrent's Directory, which is the same contract the Transmission, qBittorrent
// and Deluge clients expose so the importer can share resolveTorrentFiles.
//
// This is the authoritative list of files belonging to this torrent, used by
// the importer (#903) so a single-file torrent at a shared download root does
// not drag in every unrelated sibling.
func (c *Client) Files(ctx context.Context, hash string) ([]File, error) {
	v, err := c.call(ctx, "f.multicall", wireHash(hash), "", "f.path=", "f.size_bytes=")
	if err != nil {
		return nil, fmt.Errorf("f.multicall: %w", err)
	}
	rows, droppedShape := v.rows()
	out := make([]File, 0, len(rows))
	droppedRows := 0
	for i := range rows {
		if len(rows[i]) < 2 {
			droppedRows++
			continue
		}
		name := strings.TrimSpace(rows[i][0].stringValue())
		if name == "" {
			droppedRows++
			continue
		}
		size, _ := rows[i][1].int64Value()
		out = append(out, File{Name: name, Size: size})
	}
	if droppedShape > 0 || droppedRows > 0 {
		// An incomplete file list makes the importer fall back to a directory
		// walk rather than fail, so this is less severe than the multicall2
		// case — but it still silently changes which files get imported.
		slog.Warn("rtorrent: ignored unusable rows in the f.multicall reply — the torrent's file list may be incomplete",
			"hash", hash, "not_an_array", droppedShape, "short_or_unnamed", droppedRows, "usable", len(out))
	}
	return out, nil
}

// RemoveTorrent erases a torrent from rTorrent (d.erase).
//
// rTorrent has no delete-with-data command: d.erase closes the item, drops its
// session files and fires the erased event, but explicitly leaves the payload
// on disk. Deleting the data is therefore the caller's job — see
// downloader.RemoveDownload, which removes the resolved local path first.
func (c *Client) RemoveTorrent(ctx context.Context, hash string) error {
	v, err := c.call(ctx, "d.erase", wireHash(hash))
	if err != nil {
		if isFault(err) {
			// The torrent is already gone. Removal is idempotent from
			// Bindery's side: the desired end state is "not in the client".
			return nil
		}
		return fmt.Errorf("d.erase: %w", err)
	}
	if code, cerr := v.int64Value(); cerr == nil && code != 0 {
		return fmt.Errorf("d.erase: rTorrent returned %d", code)
	}
	return nil
}

// SetLabel writes a torrent's ruTorrent label (d.custom1).
func (c *Client) SetLabel(ctx context.Context, hash, label string) error {
	if _, err := c.call(ctx, "d.custom1.set", wireHash(hash), encodeLabel(label)); err != nil {
		return fmt.Errorf("d.custom1.set: %w", err)
	}
	return nil
}

// wireHash renders a Bindery-side (lower-case) infohash in the upper-case hex
// form rTorrent uses canonically.
func wireHash(hash string) string {
	return strings.ToUpper(strings.TrimSpace(hash))
}

// isFault reports whether err is (or wraps) an rTorrent command-level fault, as
// opposed to a transport or decode failure. The two call sites that treat a
// fault as a normal negative answer — "no such torrent" — share this.
func isFault(err error) bool {
	var fault *Fault
	return errors.As(err, &fault)
}

func isMagnetLink(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "magnet:")
}

// call performs one XML-RPC method call against the configured endpoint.
func (c *Client) call(ctx context.Context, method string, args ...arg) (*xmlValue, error) {
	if c.initErr != nil {
		return nil, c.initErr
	}

	payload, err := encodeMethodCall(method, args...)
	if err != nil {
		return nil, err
	}

	if c.scgi != nil {
		body, err := c.scgi.roundTrip(ctx, payload)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		return c.decode(method, body)
	}

	if c.rpcURL == nil {
		return nil, fmt.Errorf("rTorrent RPC URL is not configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build rTorrent request: %w", err)
	}
	req.Header.Set("Content-Type", "text/xml")
	req.Header.Set("Accept", "text/xml")
	req.Header.Set("User-Agent", useragent.Get())
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	if err := c.validateRequestTarget(req.URL); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req) // #nosec G107 G704 -- URL validated by validateRequestTarget; redirect policy enforced on client
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()

	body, err := readRPCBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("rtorrent: read body: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("rTorrent rejected the credentials (HTTP 401) — check the username and password, and that the reverse proxy in front of %s is not requiring a different login", c.baseURL)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rTorrent HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	return c.decode(method, body)
}

// decode turns a raw reply into a value, translating a parse failure into the
// advice it almost always calls for: the endpoint is not the XML-RPC handler.
// The raw parse error ("expected element type <methodResponse>") tells the user
// nothing, and this is the single most common rTorrent misconfiguration.
func (c *Client) decode(method string, body []byte) (*xmlValue, error) {
	value, err := decodeMethodResponse(body)
	if err != nil {
		if isFault(err) {
			return nil, err
		}
		hint := "it usually ends in /RPC2, or /plugins/rpc/rpc.php under ruTorrent"
		if c.scgi != nil {
			hint = "check that this is rTorrent's scgi_port / scgi_local socket and not another service"
		}
		return nil, fmt.Errorf("%s: %w — %s does not look like an rTorrent XML-RPC endpoint (%s)", method, err, c.baseURL, hint)
	}
	return value, nil
}

// readRPCBody reads an RPC response under maxRPCResponseBytes, reporting
// truncation as an explicit error rather than handing back half a document.
func readRPCBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxRPCResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxRPCResponseBytes {
		return nil, fmt.Errorf("rTorrent RPC response exceeded %d bytes — too many torrents to poll in one request", maxRPCResponseBytes)
	}
	return body, nil
}

// snippet renders the head of a non-200 body for an error message that gets
// persisted on the download row. It truncates on a rune boundary: slicing a
// UTF-8 string at a fixed byte offset can cut a multi-byte character in half,
// and the resulting replacement character then travels into the DB, the history
// entry and any webhook payload.
func snippet(body []byte) string {
	const maxSnippet = 256
	s := strings.TrimSpace(string(body))
	if len(s) <= maxSnippet {
		return s
	}
	cut := maxSnippet
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func (c *Client) validateRequestTarget(target *url.URL) error {
	if target == nil {
		return fmt.Errorf("request target is nil")
	}
	if c.rpcURL == nil {
		return fmt.Errorf("rTorrent RPC URL is not configured")
	}
	if target.Scheme != c.rpcURL.Scheme || target.Host != c.rpcURL.Host || target.Path != c.rpcURL.Path {
		return fmt.Errorf("refusing rTorrent request to unexpected target: %s", target.Redacted())
	}
	return nil
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after too many redirects")
	}
	return c.validateRequestTarget(req.URL)
}

type fetchedTorrentContent struct {
	data      []byte
	magnetURL string
}

func (c *Client) validateTorrentFetchURL(raw string) error {
	if c.validateTorrentURL != nil {
		return c.validateTorrentURL(raw)
	}
	return httpsec.ValidateOutboundURL(raw, httpsec.DownloadFetchPolicy())
}

// fetchTorrentContent resolves an indexer http(s) download link into either the
// .torrent bytes or, when the link 30x-redirects to a magnet: URI, the magnet
// itself (#1006). Every hop is re-validated against the SSRF policy so a
// redirect cannot be used to reach a blocked host.
func (c *Client) fetchTorrentContent(ctx context.Context, rawURL string) (*fetchedTorrentContent, error) {
	fetchClient := &http.Client{
		Transport: c.fetchTransport,
		Timeout:   60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	current := rawURL
	for redirects := 0; redirects <= 5; redirects++ {
		if err := c.validateTorrentFetchURL(current); err != nil {
			return nil, fmt.Errorf("fetch torrent: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return nil, fmt.Errorf("fetch torrent: %w", err)
		}
		req.Header.Set("Accept", "application/x-bittorrent")
		// Some indexers serve an anti-bot 403 to Go's default UA (#1053).
		req.Header.Set("User-Agent", useragent.Get())

		resp, err := fetchClient.Do(req)
		if err != nil {
			// Scrub the indexer apikey the *url.Error would otherwise leak into
			// the download row / history / webhook payloads.
			return nil, fmt.Errorf("fetch torrent from indexer: %w", httpsec.RedactURLError(err))
		}

		if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
			location := resp.Header.Get("Location")
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if location == "" {
				return nil, fmt.Errorf("fetch torrent: redirect without location")
			}
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(location)), "magnet:") {
				return &fetchedTorrentContent{magnetURL: location}, nil
			}
			next, err := req.URL.Parse(location)
			if err != nil {
				return nil, fmt.Errorf("fetch torrent: invalid redirect location: %w", err)
			}
			if next.Scheme != "http" && next.Scheme != "https" {
				return nil, fmt.Errorf("fetch torrent: unsupported redirect scheme %q", next.Scheme)
			}
			current = next.String()
			continue
		}

		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			return nil, fmt.Errorf("fetch torrent: indexer returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxTorrentFileBytes+1))
		if err != nil {
			return nil, fmt.Errorf("fetch torrent: %w", err)
		}
		if int64(len(data)) > maxTorrentFileBytes {
			return nil, fmt.Errorf("fetch torrent: response exceeds %d bytes", maxTorrentFileBytes)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("fetch torrent: empty response")
		}
		return &fetchedTorrentContent{data: data}, nil
	}
	return nil, fmt.Errorf("fetch torrent: too many redirects")
}
