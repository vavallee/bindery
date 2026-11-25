package calibre

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vavallee/bindery/internal/httpsec"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/pathmap"
	"github.com/vavallee/bindery/internal/useragent"
)

// ErrAlreadyInCalibre is returned by PluginClient.Add when the plugin
// reports the book is already present (HTTP 409 Conflict). Callers that
// treat duplicate pushes as idempotent, e.g. the "Push all to Calibre"
// bulk sync, can errors.Is-check this sentinel instead of parsing the
// response body.
var ErrAlreadyInCalibre = errors.New("plugin client: book already in Calibre library")

// ErrCalibreBookMissing is returned by PluginClient.UpdateMetadata when the
// Calibre row Bindery recorded is gone (HTTP 404 with code "not_found").
// Callers treat it as "the linkage is stale", not as a transport failure.
var ErrCalibreBookMissing = errors.New("plugin client: book is no longer in the Calibre library")

// Capability names advertised by GET /v1/health. Anything not listed by the
// plugin must degrade to the behaviour of the version that came before it.
const (
	pluginCapabilityBookMetadata   = "book_metadata"
	pluginCapabilityCover          = "cover"
	pluginCapabilityPathProbe      = "path_probe"
	pluginCapabilityMetadataUpdate = "metadata_update"
	pluginCapabilityErrorCodes     = "error_codes"
	// pluginCapabilityAddFormat is calibre-bridge 0.7.0: POST /v1/books
	// takes addFormat and can put a second file of the same Bindery book on
	// the row the first file made, answering format_added (#2832).
	pluginCapabilityAddFormat = "add_format"
)

// Machine readable error codes from calibre-bridge 0.6.0. A plugin older than
// that sends no code at all, which is why every branch keyed on one has a
// code-absent fallback.
const (
	pluginCodeInvalidMetadata = "invalid_metadata"
	pluginCodeNotFound        = "not_found"
)

// pluginRetryBackoff is the 503 retry schedule. docs/protocol.md specifies
// "retry with exponential backoff up to ~30s"; the client used to retry once
// after a flat two seconds, which is materially weaker than the contract it
// was written against. Five steps summing to 30s. A var, not a const, so
// tests can shrink it.
var pluginRetryBackoff = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	15 * time.Second,
}

// pluginHTTPTimeout bounds one request. Combined with pluginRetryBackoff the
// worst case for a single book is six requests plus 30s of sleeping, so about
// 210s before a push gives up. In practice a 503 comes back immediately and
// the real ceiling is the 30s of backoff.
const pluginHTTPTimeout = 30 * time.Second

// pluginCapabilityTTL bounds how long a capability probe is trusted. Without
// it, upgrading the plugin underneath a running Bindery left the client
// sending the older payload shape until the process restarted. A var so tests
// can shrink it.
var pluginCapabilityTTL = 5 * time.Minute

// errnoLikeRe matches the shape of an operating system error surfaced by the
// plugin's `open(path)` failure, e.g. "[Errno 2] No such file or directory".
// Used only to classify a 400 from a plugin too old to send an error code.
var errnoLikeRe = regexp.MustCompile(`\[Errno \d+\]`)

// PluginClient calls the Bindery Bridge Calibre plugin's HTTP API
// (protocol /v1/, see bindery-plugins/docs/protocol.md). It implements the
// importer's calibreAdder interface so the scanner can swap it in when the
// operator selects mode=plugin.
type PluginClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
	// remap translates Bindery-side library paths to the prefix the plugin's
	// container sees before they go over the wire (#1346). nil = passthrough.
	remap *pathmap.Remapper

	capMu                 sync.Mutex
	capabilities          map[string]bool
	capabilitiesFetchedAt time.Time
	metadataWarningLogged bool
}

// NewPluginClient builds a client against the plugin's base URL (e.g.
// "http://calibre.default.svc:8099"). Trailing slashes are trimmed so
// callers can pass either form.
func NewPluginClient(baseURL, apiKey string) *PluginClient {
	return &PluginClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		// Dialled directly and every dial and redirect hop re-checked, so a
		// rebound hostname or a redirect cannot reach a metadata or link-local
		// address. PolicyService still allows the LAN targets a plugin needs.
		http: &http.Client{
			Timeout:       pluginHTTPTimeout,
			Transport:     httpsec.DirectGuardedTransport(httpsec.PolicyService),
			CheckRedirect: httpsec.RedirectGuard(httpsec.PolicyService),
		},
	}
}

// WithPushPathRemap installs a pathmap "from:to[,from:to]" translation applied
// to every file path sent to the plugin (#1346). Bindery hands the Bridge the
// path it stores a book at and the plugin opens that path on ITS side of the
// container boundary; when the two containers mount the library at different
// points (the recurring Unraid case) every push fails with "No such file or
// directory". The spec is parsed once here; empty or all-malformed input
// leaves the client in passthrough mode. Returns the client for chaining.
func (c *PluginClient) WithPushPathRemap(spec string) *PluginClient {
	if r := pathmap.Parse(spec); !r.Empty() {
		c.remap = r
	}
	return c
}

// PushPath returns the path the plugin will be asked to open for a given
// Bindery-side path: remapped when a translation is configured, verbatim
// otherwise. Exported so the settings "Test connection" probe can name the
// path it actually asked about.
func (c *PluginClient) PushPath(filePath string) string {
	return c.pushPathFor(filePath)
}

// pushPathFor returns the path to put on the wire for filePath: remapped when
// a translation is configured, verbatim otherwise.
func (c *PluginClient) pushPathFor(filePath string) string {
	if c.remap == nil || strings.TrimSpace(filePath) == "" {
		return filePath
	}
	return c.remap.Apply(filePath)
}

// Add POSTs the file path and Bindery metadata to the plugin and returns the
// Calibre book id. Retries a 503 (library swap in progress) on the
// pluginRetryBackoff schedule; all other non-2xx statuses surface immediately.
func (c *PluginClient) Add(ctx context.Context, filePath string, meta Metadata) (int64, error) {
	res, err := c.AddWithOptions(ctx, filePath, meta, AddOptions{})
	return res.ID, err
}

// AddOptions are the optional parts of a push.
type AddOptions struct {
	// AddFormat asks the plugin to put this file on the row an earlier push
	// of the same Bindery book made, when that row lacks this format. It is
	// only sent to a plugin that advertises add_format; to any other it is
	// dropped and the push is exactly what Add sends.
	AddFormat bool
}

// AddResult is what a push came to. ID is the Calibre book id, also set on
// ErrAlreadyInCalibre when the plugin named the existing row. FormatAdded
// is true when the file joined an existing row rather than making one.
type AddResult struct {
	ID          int64
	FormatAdded bool
}

// AddWithOptions is Add with the optional request fields.
func (c *PluginClient) AddWithOptions(ctx context.Context, filePath string, meta Metadata, opts AddOptions) (AddResult, error) {
	addFormat := false
	if opts.AddFormat {
		// Only a plugin that says it understands addFormat gets it. A probe
		// that fails drops it too: the push then behaves as it always has.
		ok, err := c.hasCapability(ctx, pluginCapabilityAddFormat)
		addFormat = err == nil && ok
	}
	legacyPayload := false
	if !meta.empty() {
		supported, err := c.hasCapability(ctx, pluginCapabilityBookMetadata)
		if err != nil {
			c.warnMetadataUnavailable("plugin client: metadata capability probe failed; sending metadata and will retry legacy payload if rejected", "error", err)
		} else if !supported {
			c.warnMetadataUnavailable("plugin client: plugin does not advertise metadata support; upgrade Bindery Bridge to export metadata")
			legacyPayload = true
		}
	}
	// A cover path is only understood by a plugin that says so. Dropping it
	// here rather than at the call site keeps the compatibility rule in one
	// place and covers every caller, including the bulk sync.
	if strings.TrimSpace(meta.CoverPath) != "" {
		if ok, err := c.hasCapability(ctx, pluginCapabilityCover); err != nil || !ok {
			meta.CoverPath = ""
		}
	}
	// Translate once, up front: addWithRetry recurses for the 503 and
	// legacy-payload retries and must carry the already-translated paths.
	meta.CoverPath = c.pushPathFor(meta.CoverPath)
	return c.addWithRetry(ctx, c.pushPathFor(filePath), meta, addFormat, 0, legacyPayload)
}

// pluginResult is the response envelope shared by every /v1/ endpoint.
type pluginResult struct {
	ID        int64 `json:"id"`
	Duplicate bool  `json:"duplicate"`
	Updated   bool  `json:"updated"`
	// Fields names what an update actually wrote. The plugin fills blanks and
	// never overwrites, so an accepted update can legitimately write nothing.
	Fields []string `json:"fields"`
	Error  string   `json:"error"`
	// Code is the machine readable error code added in calibre-bridge 0.6.0.
	// Empty from any older plugin.
	Code string `json:"code"`
	// FormatAdded is calibre-bridge 0.7.0: the file went onto a row that
	// already existed. Absent, so false, on every other response.
	FormatAdded bool `json:"format_added"`
}

// message renders the server's error for a Go error string, appending the
// code when the plugin sent one so the operator can search for it.
func (r pluginResult) message() string {
	switch {
	case r.Error != "" && r.Code != "":
		return fmt.Sprintf("%s (%s)", r.Error, r.Code)
	case r.Error != "":
		return r.Error
	case r.Code != "":
		return r.Code
	}
	return "no error detail"
}

func (c *PluginClient) addWithRetry(ctx context.Context, filePath string, meta Metadata, addFormat bool, attempt int, legacyPayload bool) (AddResult, error) {
	body, _ := json.Marshal(pluginAddRequest{Path: filePath, Metadata: &meta, AddFormat: addFormat, Legacy: legacyPayload})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/books", bytes.NewReader(body))
	if err != nil {
		return AddResult{}, err
	}
	c.setHeaders(req, true)

	resp, err := c.http.Do(req)
	if err != nil {
		// The plugin may have restarted at a different version; nothing the
		// cache holds is trustworthy after a transport failure.
		c.invalidateCapabilities()
		return AddResult{}, fmt.Errorf("plugin client: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable && attempt < len(pluginRetryBackoff) {
		select {
		case <-ctx.Done():
			return AddResult{}, ctx.Err()
		case <-time.After(pluginRetryBackoff[attempt]):
		}
		return c.addWithRetry(ctx, filePath, meta, addFormat, attempt+1, legacyPayload)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return AddResult{}, fmt.Errorf("plugin client: authentication failed, check api_key in Settings then Calibre")
	}

	var result pluginResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONResponseBytes)).Decode(&result); err != nil && resp.StatusCode < 400 {
		return AddResult{}, fmt.Errorf("plugin client: decode response: %w", err)
	}
	if !legacyPayload && !meta.empty() && shouldRetryLegacy(resp.StatusCode, result) {
		slog.Warn("plugin client: add rejected; retrying without the metadata object. Either the plugin cannot apply this metadata or it cannot open the file at the path Bindery sent, in which case set a push path remap in Settings then Calibre",
			"status", resp.StatusCode, "code", result.Code, "error", result.Error, "path", filePath)
		return c.addWithRetry(ctx, filePath, Metadata{}, addFormat, attempt, true)
	}
	if resp.StatusCode == http.StatusConflict {
		// 409 means the book is already in the Calibre library. Surface the
		// existing id (when the plugin includes it) so the caller can
		// persist the linkage, but wrap ErrAlreadyInCalibre so idempotent
		// callers can distinguish this from a real failure.
		return AddResult{ID: result.ID}, ErrAlreadyInCalibre
	}
	if resp.StatusCode >= 400 {
		return AddResult{}, &PluginError{Status: resp.StatusCode, Code: result.Code, Detail: result.message()}
	}
	return AddResult{ID: result.ID, FormatAdded: result.FormatAdded}, nil
}

// PluginError is a rejection the plugin answered with: it was reached, read
// the request and said no. Code is the machine readable code a 0.6.0 or newer
// plugin sends (bad_format, path_forbidden, path_not_found, ...) and is empty
// from an older one. The delivery worker keys its retry policy on it.
type PluginError struct {
	Status int
	Code   string
	Detail string
}

func (e *PluginError) Error() string {
	return fmt.Sprintf("plugin client: server error %d: %s", e.Status, e.Detail)
}

// PluginErrorCode returns the plugin's error code carried by err, or "" when
// err is not a plugin rejection or the plugin sent no code.
func PluginErrorCode(err error) string {
	var pe *PluginError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// shouldRetryLegacy decides whether a rejection is about the metadata object
// (worth re-sending without it) or about the file the plugin was asked to
// open (re-sending only doubles the request count and buries the real cause).
//
// 422 is unambiguous. A 400 is only a metadata rejection when the plugin says
// so, or, from a plugin too old to send a code, when the message does not read
// like an operating system error about the file.
func shouldRetryLegacy(status int, result pluginResult) bool {
	if status == http.StatusUnprocessableEntity {
		return true
	}
	if status != http.StatusBadRequest {
		return false
	}
	if result.Code != "" {
		return result.Code == pluginCodeInvalidMetadata
	}
	return !looksLikeFileError(result.Error)
}

// looksLikeFileError reports whether an error string from a plugin with no
// error codes reads like a filesystem problem rather than a metadata one.
func looksLikeFileError(msg string) bool {
	if errnoLikeRe.MatchString(msg) {
		return true
	}
	lower := strings.ToLower(msg)
	for _, needle := range []string{
		"no such file or directory",
		"permission denied",
		"is a directory",
		"not a directory",
		"cannot determine book format",
		"path traversal",
		"path outside ingest root",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

type pluginAddRequest struct {
	Path     string    `json:"path"`
	Metadata *Metadata `json:"metadata,omitempty"`
	// AddFormat is sent only when true, so a push that does not ask for it
	// is byte for byte what it was before add_format existed. It survives
	// the legacy retry: that retry drops the metadata object, not the
	// question of which row the file belongs on.
	AddFormat bool `json:"-"`
	Legacy    bool `json:"-"`
}

func (r pluginAddRequest) MarshalJSON() ([]byte, error) {
	if r.Legacy || r.Metadata == nil || r.Metadata.empty() {
		return json.Marshal(struct {
			Path      string `json:"path"`
			AddFormat bool   `json:"addFormat,omitempty"`
		}{Path: r.Path, AddFormat: r.AddFormat})
	}
	return json.Marshal(struct {
		Path      string   `json:"path"`
		Metadata  Metadata `json:"metadata"`
		AddFormat bool     `json:"addFormat,omitempty"`
	}{Path: r.Path, Metadata: *r.Metadata, AddFormat: r.AddFormat})
}

// UpdateMetadata applies meta to a Calibre row that already exists, via
// PATCH /v1/books/{id} (capability "metadata_update"). It is how a 409 stops
// being a dead end: Bindery can correct a book it pushed before its own
// metadata improved. A plugin that does not advertise the capability gets no
// request at all.
//
// Returns the Bindery field names the plugin actually wrote, which is empty
// when the Calibre row already had everything: the plugin fills blanks and
// never overwrites, so an update can legitimately change nothing. Returns
// ErrCalibreBookMissing when the row is gone, so the caller can drop the
// stale linkage instead of retrying forever.
func (c *PluginClient) UpdateMetadata(ctx context.Context, id int64, meta Metadata) ([]string, error) {
	if id <= 0 {
		return nil, errors.New("plugin client: update needs a Calibre book id")
	}
	if strings.TrimSpace(meta.CoverPath) != "" {
		if ok, err := c.hasCapability(ctx, pluginCapabilityCover); err != nil || !ok {
			meta.CoverPath = ""
		}
	}
	meta.CoverPath = c.pushPathFor(meta.CoverPath)

	// The body IS the metadata object. POST wraps it because it shares the
	// body with `path`; PATCH has nothing to share it with, and the plugin
	// reads the whole body as the metadata dict.
	body, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		c.baseURL+"/v1/books/"+strconv.FormatInt(id, 10), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req, true)

	resp, err := c.http.Do(req)
	if err != nil {
		c.invalidateCapabilities()
		return nil, fmt.Errorf("plugin client: update metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("plugin client: authentication failed, check api_key in Settings then Calibre")
	}
	var result pluginResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONResponseBytes)).Decode(&result); err != nil && resp.StatusCode < 400 {
		return nil, fmt.Errorf("plugin client: decode update response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: calibre id %d: %s", ErrCalibreBookMissing, id, result.message())
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("plugin client: update metadata: server error %d: %s", resp.StatusCode, result.message())
	}
	return result.Fields, nil
}

// PathProbe is the answer to "can the Calibre process see this path?".
// It never reads the file's contents.
type PathProbe struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Readable bool   `json:"readable"`
	IsDir    bool   `json:"isDir"`
}

// ProbePath asks the plugin whether it can see path, after putting path
// through the same remap a push would use. Without that translation the probe
// would report on a path no push ever sends, which is the opposite of useful.
// Requires the "path_probe" capability; callers check SupportsPathProbe first.
func (c *PluginClient) ProbePath(ctx context.Context, path string) (PathProbe, error) {
	wire := c.pushPathFor(path)
	endpoint := c.baseURL + "/v1/paths?path=" + url.QueryEscape(wire)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return PathProbe{}, err
	}
	c.setHeaders(req, false)

	resp, err := c.http.Do(req)
	if err != nil {
		c.invalidateCapabilities()
		return PathProbe{}, fmt.Errorf("plugin client: path probe: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return PathProbe{}, fmt.Errorf("plugin client: authentication failed, check api_key in Settings then Calibre")
	}
	var result struct {
		PathProbe
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONResponseBytes)).Decode(&result); err != nil && resp.StatusCode < 400 {
		return PathProbe{}, fmt.Errorf("plugin client: decode path probe: %w", err)
	}
	if resp.StatusCode >= 400 {
		return PathProbe{}, fmt.Errorf("plugin client: path probe: server error %d: %s",
			resp.StatusCode, pluginResult{Error: result.Error, Code: result.Code}.message())
	}
	if result.Path == "" {
		result.Path = wire
	}
	return result.PathProbe, nil
}

func (c *PluginClient) setHeaders(req *http.Request, withContentType bool) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if withContentType {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", pluginUserAgent())
}

// pluginUserAgent renders the User-Agent protocol.md asks for:
// "bindery/<semver> plugin-api/v1". The version comes from the same singleton
// every other outbound client uses, so no caller has to thread it through.
func pluginUserAgent() string {
	return "bindery/" + useragent.Version() + " plugin-api/v1"
}

type pluginHealth struct {
	PluginVersion  string   `json:"plugin_version"`
	CalibreVersion string   `json:"calibre_version"`
	Library        string   `json:"library"`
	Capabilities   []string `json:"capabilities"`
	// Status is "degraded" when the bridge refused to start and is serving a
	// stand-in handler, with Error naming why. A bridge in that state answers
	// health but rejects every write, so a client that only checked for a 200
	// would report it as healthy.
	Status string `json:"status"`
	Error  string `json:"error"`
}

// Degraded reports whether the plugin is serving its refused-to-start
// handler, and why. Bindery cannot fix the cause, but saying it out loud is
// the difference between "plugin reachable" and an operator who knows to set
// an api_key.
func (h pluginHealth) Degraded() (bool, string) {
	if !strings.EqualFold(strings.TrimSpace(h.Status), "degraded") {
		return false, ""
	}
	reason := strings.TrimSpace(h.Error)
	if reason == "" {
		reason = "the plugin refused to start and is serving a stand-in handler"
	}
	return true, reason
}

// HealthState is what Test needs to know beyond "it answered".
type HealthState struct {
	Version string
	// PluginVersion is the bridge's own version as it reported it, e.g.
	// "0.6.2". Empty when the plugin did not send one.
	PluginVersion string
	Degraded      bool
	Reason        string
	// Library is the Calibre library the plugin is serving, as the plugin
	// sees it. The delivery worker records it as the target a book went to.
	Library string
}

// RecommendedBridgeVersion is the oldest calibre-bridge Test connection stops
// warning about. 0.6.1 opens long network share paths and 0.6.2 stops a
// failed add from leaving an empty book behind (#2831).
const RecommendedBridgeVersion = "0.6.2"

// BridgeUpgradeWarning returns an operator facing warning when version is
// older than RecommendedBridgeVersion, and "" otherwise. A version that does
// not parse gets no warning: guessing would nag people running a build the
// comparison does not understand.
func BridgeUpgradeWarning(version string) string {
	have, ok := parseBridgeVersion(version)
	if !ok {
		return ""
	}
	want, _ := parseBridgeVersion(RecommendedBridgeVersion)
	if compareBridgeVersions(have, want) >= 0 {
		return ""
	}
	return fmt.Sprintf("Bindery Bridge %s is older than %s. Update the Calibre plugin: 0.6.1 fixes pushes to long network share paths, and 0.6.2 fixes the empty book a failed add can leave in Calibre.",
		strings.TrimSpace(version), RecommendedBridgeVersion)
}

// parseBridgeVersion reads "0.6.1", "v0.6.1" or "0.6.1-rc1" into its numeric
// parts. Anything from the first character that is neither a digit nor a dot
// is a pre-release or build suffix and is ignored.
func parseBridgeVersion(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexFunc(v, func(r rune) bool { return (r < '0' || r > '9') && r != '.' }); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// compareBridgeVersions compares numerically, part by part, treating a
// missing part as zero so "0.7" equals "0.7.0". A string comparison would put
// "0.10.0" before "0.6.2".
func compareBridgeVersions(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}

// HealthDetail is Health with the degraded state attached.
func (c *PluginClient) HealthDetail(ctx context.Context) (HealthState, error) {
	h, err := c.fetchHealth(ctx)
	if err != nil {
		return HealthState{}, err
	}
	c.cacheCapabilities(h)
	degraded, reason := h.Degraded()
	return HealthState{
		Version:       fmt.Sprintf("calibredb plugin v%s (Calibre %s)", h.PluginVersion, h.CalibreVersion),
		PluginVersion: strings.TrimSpace(h.PluginVersion),
		Degraded:      degraded,
		Reason:        reason,
		Library:       strings.TrimSpace(h.Library),
	}, nil
}

// SupportsCover reports whether the plugin applies metadata.coverPath.
func (c *PluginClient) SupportsCover(ctx context.Context) bool {
	ok, err := c.hasCapability(ctx, pluginCapabilityCover)
	return err == nil && ok
}

// SupportsPathProbe reports whether GET /v1/paths is available.
func (c *PluginClient) SupportsPathProbe(ctx context.Context) bool {
	ok, err := c.hasCapability(ctx, pluginCapabilityPathProbe)
	return err == nil && ok
}

// SupportsMetadataUpdate reports whether PATCH /v1/books/{id} is available.
func (c *PluginClient) SupportsMetadataUpdate(ctx context.Context) bool {
	ok, err := c.hasCapability(ctx, pluginCapabilityMetadataUpdate)
	return err == nil && ok
}

// SupportsAddFormat reports whether POST /v1/books takes addFormat, so a
// second file of a Bindery book can join the row the first one made.
func (c *PluginClient) SupportsAddFormat(ctx context.Context) bool {
	ok, err := c.hasCapability(ctx, pluginCapabilityAddFormat)
	return err == nil && ok
}

// SupportsErrorCodes reports whether the plugin sends machine readable error
// codes. Only useful for diagnostics; every code-keyed branch already has a
// code-absent fallback.
func (c *PluginClient) SupportsErrorCodes(ctx context.Context) bool {
	ok, err := c.hasCapability(ctx, pluginCapabilityErrorCodes)
	return err == nil && ok
}

// hasCapability answers from the cache when it is fresh, otherwise re-probes.
func (c *PluginClient) hasCapability(ctx context.Context, name string) (bool, error) {
	c.capMu.Lock()
	if c.capabilitiesFreshLocked() {
		ok := c.capabilities[name]
		c.capMu.Unlock()
		return ok, nil
	}
	c.capMu.Unlock()

	h, err := c.fetchHealth(ctx)
	if err != nil {
		return false, err
	}
	c.cacheCapabilities(h)

	c.capMu.Lock()
	defer c.capMu.Unlock()
	return c.capabilities[name], nil
}

func (c *PluginClient) capabilitiesFreshLocked() bool {
	return c.capabilities != nil && time.Since(c.capabilitiesFetchedAt) < pluginCapabilityTTL
}

// capabilitiesFresh is the test-visible form of capabilitiesFreshLocked.
func (c *PluginClient) capabilitiesFresh() bool {
	c.capMu.Lock()
	defer c.capMu.Unlock()
	return c.capabilitiesFreshLocked()
}

func (c *PluginClient) invalidateCapabilities() {
	c.capMu.Lock()
	defer c.capMu.Unlock()
	c.capabilities = nil
	c.capabilitiesFetchedAt = time.Time{}
}

func (c *PluginClient) warnMetadataUnavailable(msg string, args ...any) {
	c.capMu.Lock()
	defer c.capMu.Unlock()
	if c.metadataWarningLogged {
		return
	}
	c.metadataWarningLogged = true
	slog.Warn(msg, args...)
}

func (c *PluginClient) cacheCapabilities(h pluginHealth) {
	c.capMu.Lock()
	defer c.capMu.Unlock()
	caps := make(map[string]bool, len(h.Capabilities))
	for _, name := range h.Capabilities {
		caps[strings.TrimSpace(name)] = true
	}
	c.capabilities = caps
	c.capabilitiesFetchedAt = time.Now()
}

// Health probes GET /v1/health and returns a human-readable version
// string for the Settings then Test button.
func (c *PluginClient) Health(ctx context.Context) (string, error) {
	h, err := c.fetchHealth(ctx)
	if err != nil {
		return "", err
	}
	c.cacheCapabilities(h)
	return fmt.Sprintf("calibredb plugin v%s (Calibre %s)", h.PluginVersion, h.CalibreVersion), nil
}

// Library probes the plugin's active Calibre library path. The returned path is
// from the Calibre/plugin runtime, so direct path comparison is only reliable
// when Bindery and Calibre mount the same library at the same container path.
func (c *PluginClient) Library(ctx context.Context) (string, error) {
	h, err := c.fetchHealth(ctx)
	if err != nil {
		return "", err
	}
	c.cacheCapabilities(h)
	return h.Library, nil
}

func (c *PluginClient) fetchHealth(ctx context.Context) (pluginHealth, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/health", nil)
	if err != nil {
		return pluginHealth{}, err
	}
	c.setHeaders(req, false)
	resp, err := c.http.Do(req)
	if err != nil {
		c.invalidateCapabilities()
		return pluginHealth{}, fmt.Errorf("plugin client: health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return pluginHealth{}, fmt.Errorf("plugin client: authentication failed, check api_key in Settings then Calibre")
	}
	if resp.StatusCode >= 400 {
		return pluginHealth{}, fmt.Errorf("plugin client: health: server error %d", resp.StatusCode)
	}
	var h pluginHealth
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONResponseBytes)).Decode(&h); err != nil {
		return pluginHealth{}, fmt.Errorf("plugin client: decode health: %w", err)
	}
	return h, nil
}

// maxJSONResponseBytes caps a decoded response body so a hostile or broken
// endpoint cannot stream unbounded JSON into memory.
const maxJSONResponseBytes = 16 << 20
