package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// The Calibre bridge pull routes (#2833): /bridge/v1, where the Calibre
// plugin connects out to Bindery, lists the due deliveries, downloads each
// file and acknowledges it.
//
// Authentication is the plugin API key (calibre.plugin_api_key) as a Bearer
// token, and nothing else. The global API key, session cookies, the local
// only bypass and the disabled auth mode do not apply here: the routes serve
// library files to whoever holds the key, so the key is always required.

// minBridgeKeyLen is the shortest stored plugin key the bridge will accept.
// A shorter one, or none, refuses every request rather than letting a
// guessable key open the library.
const minBridgeKeyLen = 16

// bridgeErrorCode values in the {"error","code"} bodies.
const (
	bridgeCodeUnauthorized  = "unauthorized"
	bridgeCodeNotFound      = "not_found"
	bridgeCodeNotInPullMode = "not_in_pull_mode"
	bridgeCodeInvalid       = "invalid_request"
	bridgeCodeRateLimited   = "rate_limited"
	bridgeCodeNotPending    = "not_pending"
	bridgeCodePathForbidden = "path_forbidden"
	bridgeCodeInternal      = "internal"
)

// calibreBridgeWorker is the subset of *calibre.Deliverer the routes use.
type calibreBridgeWorker interface {
	PullList(ctx context.Context, caps calibre.PullCaps, cursor string, limit int) (calibre.PullPage, error)
	PullPending(ctx context.Context, id int64) (*models.CalibreDelivery, error)
	PullFileMissing(ctx context.Context, row *models.CalibreDelivery)
	PullCoverPath(ctx context.Context, row *models.CalibreDelivery) (string, error)
	PullAck(ctx context.Context, id int64, req calibre.PullAckRequest) error
	PullNack(ctx context.Context, id int64, req calibre.PullNackRequest) error
	NotePullContact(version string, caps []string, remote string)
}

// bridgePathGate is the gate the download routes use (FileHandler.openServable),
// so a delivery row can never serve a file the ordinary download route would
// refuse: outside the library roots, a symlink, or anything but a regular file.
type bridgePathGate interface {
	openServable(ctx context.Context, p string) (*servedPath, error)
}

// CalibreBridgeHandler serves /bridge/v1.
type CalibreBridgeHandler struct {
	worker     calibreBridgeWorker
	files      bridgePathGate
	key        func(context.Context) string
	mode       func(context.Context) calibre.Mode
	transport  func(context.Context) calibre.Transport
	limiter    *auth.LoginLimiter
	retryAfter time.Duration
	version    string
}

// NewCalibreBridgeHandler builds the bridge routes. limiter must be its own
// instance, not the login limiter: a plugin retrying with a stale key would
// otherwise lock the admin out of the web UI from the same address.
// retryAfter is the limiter's window, sent back on a 429.
func NewCalibreBridgeHandler(worker calibreBridgeWorker, files bridgePathGate, settings *db.SettingsRepo, limiter *auth.LoginLimiter, retryAfter time.Duration, version string) *CalibreBridgeHandler {
	return &CalibreBridgeHandler{
		worker: worker,
		files:  files,
		key: func(ctx context.Context) string {
			s, _ := settings.Get(ctx, SettingCalibrePluginAPIKey)
			if s == nil {
				return ""
			}
			return s.Value
		},
		mode:       func(ctx context.Context) calibre.Mode { return LoadCalibreMode(ctx, settings) },
		transport:  func(ctx context.Context) calibre.Transport { return LoadCalibreTransport(ctx, settings) },
		limiter:    limiter,
		retryAfter: retryAfter,
		version:    version,
	}
}

func writeBridgeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func (h *CalibreBridgeHandler) serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("calibre bridge request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeBridgeError(w, http.StatusInternalServerError, bridgeCodeInternal, "internal server error")
}

// bearerToken returns the token of an "Authorization: Bearer" header.
func bearerToken(r *http.Request) (string, bool) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, token, ok := strings.Cut(raw, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// bridgeKeyMatches compares in constant time. Both sides are hashed first so
// the comparison does not even leak the stored key's length.
func bridgeKeyMatches(presented, stored string) bool {
	a := sha256.Sum256([]byte(presented))
	b := sha256.Sum256([]byte(stored))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func (h *CalibreBridgeHandler) unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="Bindery bridge"`)
	writeBridgeError(w, http.StatusUnauthorized, bridgeCodeUnauthorized, msg)
}

// Authenticate is the /bridge/v1 middleware: the plugin key as a Bearer
// token, failures counted per client address on the bridge's own limiter.
// The key is never logged.
func (h *CalibreBridgeHandler) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := opdsClientIP(r)
		if h.limiter != nil && !h.limiter.Allow(ip) {
			secs := int(h.retryAfter / time.Second)
			if secs < 1 {
				secs = 60
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeBridgeError(w, http.StatusTooManyRequests, bridgeCodeRateLimited, "too many failed attempts")
			return
		}
		stored := strings.TrimSpace(h.key(r.Context()))
		if len(stored) < minBridgeKeyLen {
			// Not the caller's fault, so not counted against it: the
			// operator has to set a longer key in Bindery first.
			h.unauthorized(w, "the Calibre plugin API key in Bindery is unset or shorter than 16 characters")
			return
		}
		presented, ok := bearerToken(r)
		if !ok || !bridgeKeyMatches(presented, stored) {
			if h.limiter != nil {
				h.limiter.Record(ip)
			}
			slog.Debug("calibre bridge: refused a request with a missing or wrong key", "remote", ip, "path", r.URL.Path)
			h.unauthorized(w, "missing or wrong plugin API key")
			return
		}
		if h.limiter != nil {
			h.limiter.Reset(ip)
		}
		h.worker.NotePullContact(r.Header.Get("X-Bridge-Version"),
			calibre.ParseBridgeCapabilities(r.Header.Get("X-Bridge-Capabilities")), ip)
		next.ServeHTTP(w, r)
	})
}

// RequirePull gates the delivery routes: they serve only while Calibre is in
// plugin mode with the pull transport, so the push worker and the plugin
// never both take the same row.
func (h *CalibreBridgeHandler) RequirePull(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.mode(r.Context()) != calibre.ModePlugin || h.transport(r.Context()) != calibre.TransportPull {
			writeBridgeError(w, http.StatusConflict, bridgeCodeNotInPullMode,
				"Bindery is not set to let Calibre fetch books: set Calibre mode to plugin and transport to pull")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bridgeHello is GET /bridge/v1/hello.
type bridgeHello struct {
	BinderyVersion string            `json:"binderyVersion"`
	Protocol       int               `json:"protocol"`
	MaxBatch       int               `json:"maxBatch"`
	Transport      calibre.Transport `json:"transport"`
}

// Hello is GET /bridge/v1/hello. It answers in push too, so the plugin can
// tell the operator Bindery is not in pull mode rather than just failing.
func (h *CalibreBridgeHandler) Hello(w http.ResponseWriter, r *http.Request) {
	transport := h.transport(r.Context())
	if h.mode(r.Context()) != calibre.ModePlugin {
		// Pull is only in effect in plugin mode.
		transport = calibre.TransportPush
	}
	writeJSON(w, http.StatusOK, bridgeHello{
		BinderyVersion: h.version,
		Protocol:       calibre.BridgeProtocol,
		MaxBatch:       calibre.PullDefaultBatch,
		Transport:      transport,
	})
}

// List is GET /bridge/v1/deliveries?limit=&cursor=.
func (h *CalibreBridgeHandler) List(w http.ResponseWriter, r *http.Request) {
	limit := calibre.PullDefaultBatch
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeBridgeError(w, http.StatusBadRequest, bridgeCodeInvalid, "limit must be a positive integer")
			return
		}
		limit = min(n, calibre.PullMaxBatch)
	}
	caps := calibre.PullCapsFrom(calibre.ParseBridgeCapabilities(r.Header.Get("X-Bridge-Capabilities")))
	page, err := h.worker.PullList(r.Context(), caps, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		if errors.Is(err, calibre.ErrPullInvalid) {
			writeBridgeError(w, http.StatusBadRequest, bridgeCodeInvalid, err.Error())
			return
		}
		h.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// bridgeDeliveryID reads {id}, answering 400 itself when it is not one.
func bridgeDeliveryID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeBridgeError(w, http.StatusBadRequest, bridgeCodeInvalid, "invalid delivery id")
		return 0, false
	}
	return id, true
}

// pendingRow loads the pending delivery for {id}, answering itself when there
// is none.
func (h *CalibreBridgeHandler) pendingRow(w http.ResponseWriter, r *http.Request) (*models.CalibreDelivery, bool) {
	id, ok := bridgeDeliveryID(w, r)
	if !ok {
		return nil, false
	}
	row, err := h.worker.PullPending(r.Context(), id)
	if errors.Is(err, calibre.ErrPullNotFound) {
		writeBridgeError(w, http.StatusNotFound, bridgeCodeNotFound, "no pending delivery with that id")
		return nil, false
	}
	if err != nil {
		h.serverError(w, r, err)
		return nil, false
	}
	return row, true
}

// bridgeFileExt is the extension for the download's file name: the ledger's
// format, reduced to letters and digits.
func bridgeFileExt(format string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(format) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	if b.Len() == 0 || b.Len() > 10 {
		return "bin"
	}
	return b.String()
}

// File is GET /bridge/v1/deliveries/{id}/file. It serves only the path the
// delivery row records, and only when that path is inside a library root
// and is a regular file.
func (h *CalibreBridgeHandler) File(w http.ResponseWriter, r *http.Request) {
	row, ok := h.pendingRow(w, r)
	if !ok {
		return
	}
	sp, err := h.files.openServable(r.Context(), row.FilePath)
	switch {
	case errors.Is(err, errServeNotFound):
		h.worker.PullFileMissing(r.Context(), row)
		writeBridgeError(w, http.StatusNotFound, bridgeCodeNotFound, "file is gone")
		return
	case errors.Is(err, errServeOutside):
		slog.Warn("calibre bridge: refused a delivery outside the library roots", "deliveryId", row.ID, "path", row.FilePath)
		writeBridgeError(w, http.StatusForbidden, bridgeCodePathForbidden, "file is outside the library")
		return
	case errors.Is(err, errServeNotRegular):
		slog.Warn("calibre bridge: refused a delivery that is not a regular file", "deliveryId", row.ID, "path", row.FilePath)
		writeBridgeError(w, http.StatusForbidden, bridgeCodePathForbidden, "not a regular file")
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	defer sp.Close()
	// Opened through the library root, so a link swapped in after the checks
	// above still cannot reach outside it. A directory fails the regular file
	// check on the handle below.
	f, err := sp.root.Open(sp.rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			h.worker.PullFileMissing(r.Context(), row)
			writeBridgeError(w, http.StatusNotFound, bridgeCodeNotFound, "file is gone")
			return
		}
		h.serverError(w, r, err)
		return
	}
	defer f.Close()
	// Stat the handle that will be served, not the path, so what is checked
	// is what is sent.
	info, err := f.Stat()
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	if !info.Mode().IsRegular() {
		slog.Warn("calibre bridge: refused a delivery that is not a regular file", "deliveryId", row.ID, "path", row.FilePath)
		writeBridgeError(w, http.StatusForbidden, bridgeCodePathForbidden, "not a regular file")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="book.`+bridgeFileExt(row.Format)+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// Cover is GET /bridge/v1/deliveries/{id}/cover.
func (h *CalibreBridgeHandler) Cover(w http.ResponseWriter, r *http.Request) {
	row, ok := h.pendingRow(w, r)
	if !ok {
		return
	}
	path, err := h.worker.PullCoverPath(r.Context(), row)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	if path == "" {
		writeBridgeError(w, http.StatusNotFound, bridgeCodeNotFound, "no cover")
		return
	}
	// The cover comes from Bindery's own covers store, not the library, so
	// the library roots do not apply. A link there is still never followed.
	if li, err := os.Lstat(path); err != nil || !li.Mode().IsRegular() {
		writeBridgeError(w, http.StatusNotFound, bridgeCodeNotFound, "no cover")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeBridgeError(w, http.StatusNotFound, bridgeCodeNotFound, "no cover")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeBridgeError(w, http.StatusNotFound, bridgeCodeNotFound, "no cover")
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	ctype := http.DetectContentType(head[:n])
	if !strings.HasPrefix(ctype, "image/") {
		writeBridgeError(w, http.StatusNotFound, bridgeCodeNotFound, "no cover")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		h.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// decodeBridgeJSON reads a small JSON body, answering 400 itself when it is
// not one.
func decodeBridgeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(v); err != nil {
		writeBridgeError(w, http.StatusBadRequest, bridgeCodeInvalid, "invalid JSON body")
		return false
	}
	return true
}

// writePullResult maps a PullAck or PullNack error onto the response.
func (h *CalibreBridgeHandler) writePullResult(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, calibre.ErrPullInvalid):
		writeBridgeError(w, http.StatusBadRequest, bridgeCodeInvalid, err.Error())
	case errors.Is(err, calibre.ErrPullNotFound), errors.Is(err, db.ErrCalibreDeliveryNotFound):
		writeBridgeError(w, http.StatusNotFound, bridgeCodeNotFound, "no delivery with that id")
	case errors.Is(err, calibre.ErrPullNotPending):
		writeBridgeError(w, http.StatusConflict, bridgeCodeNotPending, "delivery is no longer pending")
	default:
		h.serverError(w, r, err)
	}
}

// Ack is POST /bridge/v1/deliveries/{id}/ack.
func (h *CalibreBridgeHandler) Ack(w http.ResponseWriter, r *http.Request) {
	id, ok := bridgeDeliveryID(w, r)
	if !ok {
		return
	}
	var req calibre.PullAckRequest
	if !decodeBridgeJSON(w, r, &req) {
		return
	}
	h.writePullResult(w, r, h.worker.PullAck(r.Context(), id, req))
}

// Nack is POST /bridge/v1/deliveries/{id}/nack.
func (h *CalibreBridgeHandler) Nack(w http.ResponseWriter, r *http.Request) {
	id, ok := bridgeDeliveryID(w, r)
	if !ok {
		return
	}
	var req calibre.PullNackRequest
	if !decodeBridgeJSON(w, r, &req) {
		return
	}
	h.writePullResult(w, r, h.worker.PullNack(r.Context(), id, req))
}
