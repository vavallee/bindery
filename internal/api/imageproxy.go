package api

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	h := &ImageProxyHandler{
		cacheDir: filepath.Join(dataDir, "image-cache"),
		client: &http.Client{
			Timeout: 15 * time.Second,
			// Re-validate redirect targets — a permissive upstream could otherwise
			// redirect from a public host into the LAN (cloud metadata, internal
			// services) and leak the body back through the cache.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if err := httpsec.ValidateOutboundURL(req.URL.String(), httpsec.PolicyStrict); err != nil {
					return fmt.Errorf("redirect blocked: %w", err)
				}
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
		validateURL: func(u string) error { return httpsec.ValidateOutboundURL(u, httpsec.PolicyStrict) },
	}
	go h.migrateFlatCache()
	return h
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
	shard := key[:2]
	imgFile := filepath.Join(h.cacheDir, shard, key)
	ctFile := imgFile + ".ct"

	// Serve from cache if fresh.
	if info, err := os.Stat(imgFile); err == nil && time.Since(info.ModTime()) < imageCacheTTL { // #nosec -- path derived from sha256(url), not user input
		ct, _ := os.ReadFile(ctFile) //nolint:gosec // #nosec -- path derived from sha256(url), not user input
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
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, raw, nil) // #nosec -- URL validated above via h.validateURL (PolicyStrict)
	if err != nil {
		http.Error(w, "upstream fetch failed", http.StatusBadGateway)
		return
	}
	resp, err := h.client.Do(upReq) // #nosec -- URL validated above via h.validateURL (PolicyStrict)
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
	// Atomic: write to .tmp, then rename so readers never see partial files.
	if mkErr := os.MkdirAll(filepath.Dir(imgFile), imageDirMode); mkErr == nil { // #nosec G301 G304 G703 -- path derived from sha256(url), not user input
		tmp := imgFile + ".tmp"
		if err := os.WriteFile(tmp, body, imageCacheMode); err == nil { // #nosec
			_ = os.Rename(tmp, imgFile) // #nosec
		}
		ctTmp := ctFile + ".tmp"
		if err := os.WriteFile(ctTmp, []byte(ct), imageCacheMode); err == nil { // #nosec
			_ = os.Rename(ctTmp, ctFile) // #nosec
		}
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=2592000")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	_, _ = w.Write(body)
}

// hexKeyRe matches a 64-character lowercase hex string (sha256 output).
var hexKeyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// migrateFlatCache moves legacy flat-layout cache files into the sharded
// directory structure (image-cache/<first2chars>/<key>). Runs once at startup.
func (h *ImageProxyHandler) migrateFlatCache() {
	entries, err := os.ReadDir(h.cacheDir) // #nosec
	if err != nil {
		return // cache dir may not exist yet
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip .ct sidecars — they'll be moved alongside their parent.
		if strings.HasSuffix(name, ".ct") {
			continue
		}
		if !hexKeyRe.MatchString(name) {
			continue
		}
		shard := name[:2]
		dst := filepath.Join(h.cacheDir, shard, name)
		if err := os.MkdirAll(filepath.Join(h.cacheDir, shard), imageDirMode); err != nil {
			slog.Warn("image cache migration: mkdir failed", "shard", shard, "error", err)
			continue
		}
		src := filepath.Join(h.cacheDir, name) // #nosec
		if err := os.Rename(src, dst); err != nil {
			slog.Warn("image cache migration: rename failed", "src", src, "error", err)
			continue
		}
		// Move the .ct sidecar if it exists.
		ctSrc := src + ".ct"
		if _, err := os.Stat(ctSrc); err == nil { // #nosec
			_ = os.Rename(ctSrc, dst+".ct") // #nosec
		}
	}
}

// StartEviction launches a background goroutine that periodically removes
// cached images older than imageCacheTTL.
func (h *ImageProxyHandler) StartEviction(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			h.evictExpired()
		}
	}()
}

func (h *ImageProxyHandler) evictExpired() {
	now := time.Now()
	_ = filepath.WalkDir(h.cacheDir, func(path string, d os.DirEntry, err error) error { // #nosec
		if err != nil || d.IsDir() {
			return nil
		}
		// Skip .ct sidecars — they're deleted alongside their parent.
		if strings.HasSuffix(d.Name(), ".ct") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if now.Sub(info.ModTime()) > imageCacheTTL {
			_ = os.Remove(path)         // #nosec
			_ = os.Remove(path + ".ct") // #nosec
		}
		return nil
	})
}

// CacheSize returns the total bytes used by the image cache directory.
func (h *ImageProxyHandler) CacheSize() (int64, error) {
	var total int64
	err := filepath.WalkDir(h.cacheDir, func(_ string, d os.DirEntry, err error) error { // #nosec
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
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
