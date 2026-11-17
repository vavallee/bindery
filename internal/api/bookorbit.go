package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/bookorbit"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/pathmap"
)

const (
	SettingBookOrbitBaseURL    = "bookorbit.base_url"
	SettingBookOrbitAPIKey     = "bookorbit.api_key" //nolint:gosec // #nosec G101 -- settings key name, not a credential value
	SettingBookOrbitLibraryIDs = "bookorbit.library_ids"
	SettingBookOrbitPathRemap  = "bookorbit.path_remap"
	// SettingBookOrbitPublicURL is the address a browser uses to reach
	// BookOrbit, for the "Open in BookOrbit" link. Empty means the base URL.
	SettingBookOrbitPublicURL = "bookorbit.public_url"
)

type bookOrbitClient interface {
	Me(ctx context.Context) (*bookorbit.Identity, error)
	ListLibraries(ctx context.Context) ([]bookorbit.Library, error)
}

type bookOrbitImporterAPI interface {
	Start(ctx context.Context, cfg bookorbit.Config) error
	Progress() abs.ImportProgress
	RunsPage(ctx context.Context, limit, offset int) ([]models.ABSImportRun, error)
}

// BookOrbitHandler serves the BookOrbit import: its settings, a connection
// test, the library list and the import itself. Every route is admin only
// (see the route table), and no response carries the API key.
type BookOrbitHandler struct {
	settings  *db.SettingsRepo
	importer  bookOrbitImporterAPI
	newClient func(baseURL, apiKey string) (bookOrbitClient, error)
	userAgent string
}

// BookOrbitStoredConfig is the saved configuration, key included. It never
// leaves the server.
type BookOrbitStoredConfig struct {
	BaseURL    string
	APIKey     string
	LibraryIDs []int64
	PathRemap  string
	PublicURL  string
}

type bookOrbitConfigResponse struct {
	BaseURL          string   `json:"baseUrl"`
	PublicURL        string   `json:"publicUrl"`
	LibraryIDs       []string `json:"libraryIds"`
	PathRemap        string   `json:"pathRemap"`
	APIKeyConfigured bool     `json:"apiKeyConfigured"`
}

type bookOrbitConfigRequest struct {
	BaseURL    *string   `json:"baseUrl"`
	APIKey     *string   `json:"apiKey"`
	LibraryIDs *[]string `json:"libraryIds"`
	PathRemap  *string   `json:"pathRemap"`
	PublicURL  *string   `json:"publicUrl"`
}

type bookOrbitProbeRequest struct {
	// APIKey lets the form test a key before saving it. Empty means the
	// saved key.
	APIKey string `json:"apiKey"`
}

type bookOrbitLibraryResponse struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Folders []string `json:"folders"`
}

type bookOrbitImportRequest struct {
	DryRun *bool `json:"dryRun"`
	// LibraryIDs narrows the run to some of the selected libraries.
	LibraryIDs []string `json:"libraryIds"`
}

func NewBookOrbitHandler(settings *db.SettingsRepo, importer bookOrbitImporterAPI) *BookOrbitHandler {
	h := &BookOrbitHandler{settings: settings, importer: importer}
	h.newClient = func(baseURL, apiKey string) (bookOrbitClient, error) {
		client, err := bookorbit.NewClient(baseURL, apiKey)
		if err != nil {
			return nil, err
		}
		return client.WithUserAgent(h.userAgent), nil
	}
	return h
}

func (h *BookOrbitHandler) WithUserAgent(userAgent string) *BookOrbitHandler {
	h.userAgent = strings.TrimSpace(userAgent)
	return h
}

// LoadBookOrbitConfig reads the saved BookOrbit configuration.
func LoadBookOrbitConfig(ctx context.Context, settings *db.SettingsRepo) BookOrbitStoredConfig {
	get := func(key string) string {
		s, _ := settings.Get(ctx, key)
		if s == nil {
			return ""
		}
		return strings.TrimSpace(s.Value)
	}
	var rawIDs []string
	_ = json.Unmarshal([]byte(get(SettingBookOrbitLibraryIDs)), &rawIDs)
	ids, _ := parseBookOrbitLibraryIDs(rawIDs)
	return BookOrbitStoredConfig{
		BaseURL:    get(SettingBookOrbitBaseURL),
		APIKey:     get(SettingBookOrbitAPIKey),
		LibraryIDs: ids,
		PathRemap:  get(SettingBookOrbitPathRemap),
		PublicURL:  get(SettingBookOrbitPublicURL),
	}
}

// parseBookOrbitLibraryIDs reads library ids as the API carries them, which
// is as strings like every other id in a Bindery response. BookOrbit's are
// positive integers. Duplicates are dropped and the order is kept.
func parseBookOrbitLibraryIDs(raw []string) ([]int64, error) {
	out := make([]int64, 0, len(raw))
	seen := make(map[int64]struct{}, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("library id " + strconv.Quote(value) + " is not a BookOrbit library id")
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func formatBookOrbitLibraryIDs(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out
}

func bookOrbitConfigResponseFor(cfg BookOrbitStoredConfig) bookOrbitConfigResponse {
	return bookOrbitConfigResponse{
		BaseURL:          cfg.BaseURL,
		PublicURL:        cfg.PublicURL,
		LibraryIDs:       formatBookOrbitLibraryIDs(cfg.LibraryIDs),
		PathRemap:        cfg.PathRemap,
		APIKeyConfigured: cfg.APIKey != "",
	}
}

func (h *BookOrbitHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, bookOrbitConfigResponseFor(LoadBookOrbitConfig(r.Context(), h.settings)))
}

func (h *BookOrbitHandler) SetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := LoadBookOrbitConfig(r.Context(), h.settings)

	var req bookOrbitConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.BaseURL != nil {
		cfg.BaseURL = strings.TrimSpace(*req.BaseURL)
		if cfg.BaseURL != "" {
			normalized, err := bookorbit.ValidateBaseURL(cfg.BaseURL)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			cfg.BaseURL = normalized
		}
	}
	// An absent or empty key keeps the saved one, so the form can be saved
	// without retyping a key it was never shown.
	newKey := ""
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" {
		key, err := bookorbit.NormalizeAPIKey(*req.APIKey)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		newKey = key
		cfg.APIKey = key
	}
	if req.LibraryIDs != nil {
		ids, err := parseBookOrbitLibraryIDs(*req.LibraryIDs)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		cfg.LibraryIDs = ids
	}
	if req.PathRemap != nil {
		cfg.PathRemap = strings.TrimSpace(*req.PathRemap)
		if err := pathmap.Validate(cfg.PathRemap); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path remap: " + err.Error()})
			return
		}
	}

	if req.PublicURL != nil {
		normalized, err := bookorbit.NormalizePublicURL(*req.PublicURL)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		cfg.PublicURL = normalized
	}

	idsJSON, err := json.Marshal(formatBookOrbitLibraryIDs(cfg.LibraryIDs))
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	// One transaction, so a failure cannot leave a new URL beside an old
	// library selection.
	kvs := []db.SettingKV{
		{Key: SettingBookOrbitBaseURL, Value: cfg.BaseURL},
		{Key: SettingBookOrbitLibraryIDs, Value: string(idsJSON)},
		{Key: SettingBookOrbitPathRemap, Value: cfg.PathRemap},
		{Key: SettingBookOrbitPublicURL, Value: cfg.PublicURL},
	}
	if newKey != "" {
		kvs = append(kvs, db.SettingKV{Key: SettingBookOrbitAPIKey, Value: newKey})
	}
	if err := h.settings.SetMany(r.Context(), kvs); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bookOrbitConfigResponseFor(cfg))
}

// clientFromProbe builds a client for the saved base URL. The URL always comes
// from settings, never from the request, so a probe cannot be pointed at an
// address that did not go through SetConfig's validation.
func (h *BookOrbitHandler) clientFromProbe(r *http.Request) (bookOrbitClient, string, error) {
	cfg := LoadBookOrbitConfig(r.Context(), h.settings)
	var req bookOrbitProbeRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			return nil, "", errors.New("invalid request body")
		}
	}
	if cfg.BaseURL == "" {
		return nil, "", errors.New("save the BookOrbit base URL before testing")
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		apiKey = cfg.APIKey
	}
	if apiKey == "" {
		return nil, "", errors.New("an API key is required")
	}
	client, err := h.newClient(cfg.BaseURL, apiKey)
	return client, cfg.BaseURL, err
}

func (h *BookOrbitHandler) Test(w http.ResponseWriter, r *http.Request) {
	client, baseURL, err := h.clientFromProbe(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	me, err := client.Me(r.Context())
	if err != nil {
		h.writeProbeError(w, "bookorbit test failed", baseURL, err)
		return
	}
	libraries, err := client.ListLibraries(r.Context())
	if err != nil {
		h.writeProbeError(w, "bookorbit test failed", baseURL, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message":              "connected",
		"username":             me.Username,
		"authenticationMethod": me.AuthenticationMethod,
		"libraryCount":         len(libraries),
	})
}

func (h *BookOrbitHandler) Libraries(w http.ResponseWriter, r *http.Request) {
	client, baseURL, err := h.clientFromProbe(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	libraries, err := client.ListLibraries(r.Context())
	if err != nil {
		h.writeProbeError(w, "bookorbit list libraries failed", baseURL, err)
		return
	}
	out := make([]bookOrbitLibraryResponse, 0, len(libraries))
	for _, lib := range libraries {
		folders := lib.Folders
		if folders == nil {
			folders = []string{}
		}
		out = append(out, bookOrbitLibraryResponse{ID: strconv.FormatInt(lib.ID, 10), Name: lib.Name, Folders: folders})
	}
	sort.SliceStable(out, func(a, b int) bool { return strings.ToLower(out[a].Name) < strings.ToLower(out[b].Name) })
	writeJSON(w, http.StatusOK, out)
}

func (h *BookOrbitHandler) writeProbeError(w http.ResponseWriter, logMsg, baseURL string, err error) {
	var apiErr *bookorbit.APIError
	var shapeErr *bookorbit.ShapeError
	switch {
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "BookOrbit rejected the API key. It may be revoked or expired, or API keys may be turned off on the server."})
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "BookOrbit refused the request for this API key."})
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "BookOrbit has no API at this address. Check the base URL, and that the server is recent enough to support API keys."})
	case errors.As(err, &apiErr):
		// BookOrbit's own message goes to the log only. It is upstream text
		// and never carries our key, but the browser gets a plain sentence.
		slog.Warn(logMsg, "status", apiErr.StatusCode, "host", redactABSHost(baseURL), "code", apiErr.Code, "message", apiErr.Message)
		if apiErr.PlainHTTPToHTTPS {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "The server at this address speaks https, but the base URL starts with http://. Try https:// instead."})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": bookOrbitStatusMessage(apiErr.StatusCode)})
	case errors.As(err, &shapeErr):
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": shapeErr.Error()})
	default:
		slog.Warn(logMsg, "host", redactABSHost(baseURL), "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": bookOrbitTransportMessage(baseURL, err)})
	}
}

func bookOrbitStatusMessage(status int) string {
	return "BookOrbit answered with an unexpected HTTP status (" + strconv.Itoa(status) + "). Check the server's own logs."
}

// bookOrbitTransportMessage turns a transport failure into a sentence an
// admin can act on. It names only the host the admin typed, never the request
// URL, a resolver address or anything the Go error carries. The full error
// stays in the server log.
func bookOrbitTransportMessage(baseURL string, err error) string {
	host := ""
	if u, perr := url.Parse(baseURL); perr == nil {
		host = u.Host
	}
	if host == "" {
		host = "the configured address"
	}
	var dnsErr *net.DNSError
	var unknownAuth x509.UnknownAuthorityError
	var certInvalid x509.CertificateInvalidError
	var hostnameErr x509.HostnameError
	var verifyErr *tls.CertificateVerificationError
	var recordErr tls.RecordHeaderError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return "Host not found: " + host + " does not resolve. Check the base URL."
	case errors.Is(err, http.ErrSchemeMismatch):
		return host + " speaks plain http, but the base URL starts with https://. Use an http:// URL instead."
	case isConnRefused(err):
		return "Connection refused by " + host + ". Check that BookOrbit is running and that the port is right."
	case isTimeoutErr(err):
		msg := host + " did not answer in time."
		if u, perr := url.Parse(baseURL); perr == nil && looksLANHost(u.Hostname()) {
			msg += " The host looks LAN local. If Bindery runs behind a VPN container, allow your LAN in its firewall settings."
		}
		return msg
	case errors.As(err, &unknownAuth), errors.As(err, &certInvalid), errors.As(err, &hostnameErr), errors.As(err, &verifyErr):
		return "TLS problem with " + host + ": its certificate is not trusted or does not match the address. Use a certificate Bindery trusts, or plain http on a trusted network."
	case errors.As(err, &dnsErr):
		return "Could not look up " + host + ". Check the base URL and Bindery's DNS settings."
	case errors.As(err, &recordErr):
		return "TLS problem with " + host + ": it did not answer as an https server. Check the scheme in the base URL."
	default:
		return "Could not reach BookOrbit at " + host + ". Check the base URL and the server's logs."
	}
}

// wsaeconnrefused is Windows' WSAECONNREFUSED. syscall.ECONNREFUSED on
// Windows is a Go invented value that the socket layer never returns, so the
// errno is compared by number. No other platform uses 10061.
const wsaeconnrefused = 10061

func isConnRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && uintptr(errno) == wsaeconnrefused
}

func (h *BookOrbitHandler) StartImport(w http.ResponseWriter, r *http.Request) {
	cfg := LoadBookOrbitConfig(r.Context(), h.settings)
	var req bookOrbitImportRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}
	runCfg := bookorbit.Config{
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		PathRemap:  cfg.PathRemap,
		LibraryIDs: cfg.LibraryIDs,
	}
	// A run is a dry run unless the caller says otherwise, so a bare POST
	// can never write.
	runCfg.DryRun = req.DryRun == nil || *req.DryRun
	if len(req.LibraryIDs) > 0 {
		wanted, err := parseBookOrbitLibraryIDs(req.LibraryIDs)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// Only libraries the admin selected in settings can be asked for.
		selected := make(map[int64]struct{}, len(cfg.LibraryIDs))
		for _, id := range cfg.LibraryIDs {
			selected[id] = struct{}{}
		}
		for _, id := range wanted {
			if _, ok := selected[id]; !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "library " + strconv.FormatInt(id, 10) + " is not one of the selected BookOrbit libraries"})
				return
			}
		}
		runCfg.LibraryIDs = wanted
	}
	if runCfg.BaseURL == "" || runCfg.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "BookOrbit is not configured: save a base URL and an API key first"})
		return
	}
	if len(runCfg.LibraryIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "select at least one BookOrbit library first"})
		return
	}

	err := h.importer.Start(context.WithoutCancel(r.Context()), runCfg)
	switch {
	case errors.Is(err, bookorbit.ErrAlreadyRunning):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, abs.ErrOtherImportRunning):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "another library import is running; wait for it to finish"})
		return
	case errors.Is(err, abs.ErrAlreadyRunning):
		writeJSON(w, http.StatusConflict, map[string]string{"error": bookorbit.ErrAlreadyRunning.Error()})
		return
	case errors.Is(err, abs.ErrShuttingDown):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, h.importer.Progress())
}

func (h *BookOrbitHandler) ImportStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.importer.Progress())
}

// ImportRuns lists recorded BookOrbit runs newest first, one row per library
// of a run. It honours ?limit (default 10, max 200) and ?offset.
func (h *BookOrbitHandler) ImportRuns(w http.ResponseWriter, r *http.Request) {
	limit, offset := parseLimitOffset(r, absImportRunsDefaultLimit, absImportRunsMaxLimit)
	runs, err := h.importer.RunsPage(r.Context(), limit, offset)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	resp := make([]abs.PersistedImportRun, 0, len(runs))
	for _, run := range runs {
		hydrated := abs.HydrateRun(run)
		// The engine knows the library by a prefixed id. Callers get
		// BookOrbit's own.
		hydrated.LibraryID = bookorbit.DisplayLibraryID(hydrated.LibraryID)
		hydrated.Source.LibraryID = bookorbit.DisplayLibraryID(hydrated.Source.LibraryID)
		for idx, id := range hydrated.Source.LibraryIDs {
			hydrated.Source.LibraryIDs[idx] = bookorbit.DisplayLibraryID(id)
		}
		resp = append(resp, hydrated)
	}
	writeJSON(w, http.StatusOK, resp)
}
