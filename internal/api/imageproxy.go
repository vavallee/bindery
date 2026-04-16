package api

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

const (
	imageCacheTTL  = 30 * 24 * time.Hour
	imageMaxBytes  = 10 * 1024 * 1024 // 10 MB
	imageCacheMode = 0o640
	imageDirMode   = 0o750
)

// ImageProxyHandler serves GET /api/v1/images?url=<encoded>.
// On first request it fetches the external image, validates it, writes it to a
// local disk cache under <dataDir>/image-cache/, and returns it. Subsequent
// requests within the 30-day TTL are served entirely from the cache — no
// outbound traffic, no third-party tracking.
type ImageProxyHandler struct {
	cacheDir    string
	client      *http.Client
	validateURL func(string) error // defaults to httpsec.ValidateOutboundURL; overridable in tests
}

// NewImageProxyHandler creates a handler that caches images under
// <dataDir>/image-cache/.
func NewImageProxyHandler(dataDir string) *ImageProxyHandler {
	return &ImageProxyHandler{
		cacheDir:    filepath.Join(dataDir, "image-cache"),
		client:      &http.Client{Timeout: 15 * time.Second},
		validateURL: func(u string) error { return httpsec.ValidateOutboundURL(u, httpsec.PolicyStrict) },
	}
}

// Serve handles GET /api/v1/images?url=<encoded-external-url>.
func (h *ImageProxyHandler) Serve(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" {
		http.Error(w, "url parameter required", http.StatusBadRequest)
		return
	}

	// Block SSRF — image URLs come from metadata providers we trust, but the
	// stored value could be tampered with; always re-validate before fetching.
	if err := h.validateURL(raw); err != nil {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}

	sum := sha256.Sum256([]byte(raw))
	key := fmt.Sprintf("%x", sum)
	imgFile := filepath.Join(h.cacheDir, key)
	ctFile := imgFile + ".ct"

	// Serve from cache if fresh.
	if info, err := os.Stat(imgFile); err == nil && time.Since(info.ModTime()) < imageCacheTTL {
		ct, _ := os.ReadFile(ctFile) //nolint:gosec // path is constructed from sha256, not user input
		if len(ct) == 0 {
			ct = []byte("image/jpeg")
		}
		w.Header().Set("Content-Type", string(ct))
		w.Header().Set("Cache-Control", "public, max-age=2592000")
		http.ServeFile(w, r, imgFile)
		return
	}

	// Fetch from upstream. Use NewRequestWithContext so the request respects
	// the caller's context (cancellation, deadline). The URL has already been
	// validated by h.validateURL; the nolint suppresses the gosec taint warning
	// that can't trace through the validateURL indirection.
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, raw, nil) //nolint:gosec // URL validated above
	if err != nil {
		http.Error(w, "upstream fetch failed", http.StatusBadGateway)
		return
	}
	resp, err := h.client.Do(upReq)
	if err != nil {
		http.Error(w, "upstream fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream returned non-200", http.StatusBadGateway)
		return
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		http.Error(w, "upstream response is not an image", http.StatusBadGateway)
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, imageMaxBytes+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadGateway)
		return
	}
	if len(body) > imageMaxBytes {
		http.Error(w, "image exceeds 10 MB limit", http.StatusBadGateway)
		return
	}

	// Write to cache (best-effort — a write failure is not fatal).
	if mkErr := os.MkdirAll(h.cacheDir, imageDirMode); mkErr == nil {
		_ = os.WriteFile(imgFile, body, imageCacheMode)
		_ = os.WriteFile(ctFile, []byte(ct), imageCacheMode)
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=2592000")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	_, _ = w.Write(body)
}

// ProxyImageURL rewrites a raw external image URL into the local proxy path
// /api/v1/images?url=<encoded>. Returns raw unchanged when it is empty or
// already a relative URL (already proxied or intentionally local).
func ProxyImageURL(raw string) string {
	if raw == "" || strings.HasPrefix(raw, "/") {
		return raw
	}
	return "/api/v1/images?url=" + url.QueryEscape(raw)
}

// proxyAuthorImages rewrites ImageURL on an author and all its embedded books.
// Mutates in place — callers own the struct and it is not shared with the DB layer.
func proxyAuthorImages(a *models.Author) {
	a.ImageURL = ProxyImageURL(a.ImageURL)
	for i := range a.Books {
		proxyBookImages(&a.Books[i])
	}
}

// proxyBookImages rewrites ImageURL on a book.
func proxyBookImages(b *models.Book) {
	b.ImageURL = ProxyImageURL(b.ImageURL)
}
