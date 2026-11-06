package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/duplicates"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/useragent"
)

const (
	imageCacheTTL  = 30 * 24 * time.Hour
	imageMaxBytes  = 10 * 1024 * 1024 // 10 MB
	imageCacheMode = 0o640
	imageDirMode   = 0o750

	// imageCacheSizeTTL bounds how stale the memoised cache total may get when
	// nothing else refreshes it. Between refreshes the number is kept exact by
	// the incremental adjustment Serve makes after each cache write and by the
	// full recount the daily eviction sweep does, so this TTL only catches
	// changes made behind the handler's back (an operator deleting the cache
	// directory, say). It is a ceiling on how often the walk can run, not a
	// schedule: with /system/status polled from every page load (#2340) the
	// difference between a walk per request and a walk per ten minutes is the
	// whole point.
	imageCacheSizeTTL = 10 * time.Minute
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

	// local serves covers Bindery stored itself (Calibre library covers,
	// #2564). Nil until WithLocalCovers; then a bindery-cover: reference is
	// answered from disk and never reaches the fetch path.
	local *covers.Store

	// sizeMu guards the memoised cache total below. It is held across the
	// directory walk in refreshCacheSizeLocked on purpose: that serialises a
	// recount against the incremental adjustments Serve makes, so a write
	// landing mid-walk can never be counted twice or dropped, and concurrent
	// /system/status callers wait on one walk instead of starting their own.
	sizeMu sync.Mutex
	// sizeBytes is the last known total of the on-disk image cache.
	sizeBytes int64
	// sizeAt is when sizeBytes was last recomputed from a full walk. Zero
	// means never, which forces the first CacheSize call to seed it.
	sizeAt time.Time
}

// NewImageProxyHandler creates a handler that caches images under
// <dataDir>/image-cache/.
func NewImageProxyHandler(dataDir string) *ImageProxyHandler {
	h := &ImageProxyHandler{
		cacheDir: filepath.Join(dataDir, "image-cache"),
		client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: imageProxyTransport(),
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

// WithLocalCovers attaches the store that backs bindery-cover: references.
func (h *ImageProxyHandler) WithLocalCovers(store *covers.Store) *ImageProxyHandler {
	h.local = store
	return h
}

// imageProxyTransport returns the RoundTripper for the image-proxy client: a
// clone of the shared proxy transport with a per-dial PolicyStrict SSRF
// re-validation (httpsec.GuardedTransport), closing the DNS-rebind TOCTOU
// window between the up-front URL validation and the actual dial.
//
// When an outbound proxy is configured (#987) the dial to the operator-trusted
// proxy itself is exempt, so a LAN or loopback proxy keeps working; a request
// that the proxy bypass rules send direct is checked like any direct dial.
func imageProxyTransport() http.RoundTripper {
	return httpsec.GuardedTransport(httpsec.PolicyStrict)
}

// servedImageTypes is the closed set of types the proxy will ever put in a
// Content-Type header. Cover art from every metadata provider is one of
// these. SVG is deliberately absent: it is a scripting document, not a
// picture, and the proxy answers from Bindery's own origin (#2355).
var servedImageTypes = []string{"image/jpeg", "image/png", "image/webp", "image/gif", "image/avif"}

// imageResponseCSP is sent with every proxied image. A picture needs no
// resources and no scripting context, so if one is ever opened as a document
// it renders nothing and runs nothing.
const imageResponseCSP = "default-src 'none'; sandbox"

// allowedImageContentType reports whether ct is exactly one of the served
// image types. It does no normalising on purpose: the only callers check a
// value this code produced, and anything that is not already in canonical
// form (a parameter, a list, odd casing) was not written by it.
func allowedImageContentType(ct string) bool {
	for _, t := range servedImageTypes {
		if ct == t {
			return true
		}
	}
	return false
}

// sniffImageType returns the served image type the bytes actually are, or ""
// when they are not one of them. The upstream Content-Type is never consulted:
// it is text a remote host controls, whereas the bytes decide what a browser
// does with them.
func sniffImageType(body []byte) string {
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	ct := http.DetectContentType(head)
	if allowedImageContentType(ct) {
		return ct
	}
	if isAVIF(head) {
		return "image/avif"
	}
	return ""
}

// isAVIF reports whether b starts with an ISO base media "ftyp" box that
// names avif or avis as its major or a compatible brand.
// http.DetectContentType does not know the format.
func isAVIF(b []byte) bool {
	if len(b) < 16 || string(b[4:8]) != "ftyp" {
		return false
	}
	size := int(binary.BigEndian.Uint32(b[:4]))
	if size < 16 || size > len(b) {
		size = len(b)
	}
	isBrand := func(x []byte) bool { return string(x) == "avif" || string(x) == "avis" }
	if isBrand(b[8:12]) {
		return true
	}
	for i := 16; i+4 <= size; i += 4 {
		if isBrand(b[i : i+4]) {
			return true
		}
	}
	return false
}

// setImageHeaders sets the headers every proxied image carries. ct must be a
// sniffed type, never an upstream value.
func setImageHeaders(w http.ResponseWriter, ct string) {
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", imageResponseCSP)
	h.Set("Cache-Control", "public, max-age=2592000")
}

// readImageFile reads a stored image, refusing anything larger than the
// proxy would have fetched.
func readImageFile(path string) ([]byte, time.Time, error) {
	f, err := os.Open(path) // #nosec G304 -- path derived from sha256(url) or returned by covers.Store.Resolve
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, time.Time{}, fmt.Errorf("not a regular file")
	}
	body, err := io.ReadAll(io.LimitReader(f, imageMaxBytes+1))
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(body) > imageMaxBytes {
		return nil, time.Time{}, fmt.Errorf("stored image too large")
	}
	return body, info.ModTime(), nil
}

// Serve handles GET /api/v1/images?url=<encoded-external-url>.
func (h *ImageProxyHandler) Serve(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" {
		http.Error(w, "url parameter required", http.StatusBadRequest)
		return
	}

	// A cover Bindery stored itself (a Calibre library's cover.jpg, #2564)
	// is served straight from the covers store. covers.Store.Resolve is the
	// only thing that turns the reference into a path, and it refuses any
	// name that is not a digest sitting directly inside the store directory,
	// so a tampered image_url row cannot read outside it. There is no
	// fallback to the fetch path: a malformed or missing reference is a 404,
	// not a fetch of the literal string.
	if covers.IsRef(raw) {
		path, _, ok := h.local.Resolve(raw)
		if !ok {
			http.Error(w, "cover not found", http.StatusNotFound)
			return
		}
		// The store sniffed the bytes when it wrote them; judge them again
		// here so the type sent is always the one the bytes have.
		body, mod, err := readImageFile(path)
		if err != nil {
			http.Error(w, "cover not found", http.StatusNotFound)
			return
		}
		ct := sniffImageType(body)
		if ct == "" {
			http.Error(w, "stored file is not an image", http.StatusBadGateway)
			return
		}
		setImageHeaders(w, ct)
		http.ServeContent(w, r, "", mod, bytes.NewReader(body))
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

	// Serve from cache if fresh. An entry is trusted only after both halves
	// check out: the stored bytes must sniff to a served type, and a sidecar,
	// when present, must say exactly that type. Anything else (an entry
	// written by an older release, or edited on disk) is dropped and fetched
	// again below, which also covers the svg refusal of #2355.
	if info, err := os.Stat(imgFile); err == nil && time.Since(info.ModTime()) < imageCacheTTL { // #nosec -- path derived from sha256(url), not user input
		if body, mod, rerr := readImageFile(imgFile); rerr == nil {
			ct := sniffImageType(body)
			if sidecar, cerr := os.ReadFile(ctFile); cerr == nil && len(sidecar) > 0 && string(sidecar) != ct { //nolint:gosec // #nosec -- path derived from sha256(url), not user input
				ct = ""
			}
			if ct != "" {
				setImageHeaders(w, ct)
				http.ServeContent(w, r, "", mod, bytes.NewReader(body))
				return
			}
		}
		dropped := cacheEntryBytes(imgFile, ctFile)
		_ = os.Remove(imgFile) // #nosec G304 -- path derived from sha256(url), not user input
		_ = os.Remove(ctFile)  // #nosec G304 -- path derived from sha256(url), not user input
		h.addCacheBytes(-dropped)
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
	upReq.Header.Set("User-Agent", useragent.Get())
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, imageMaxBytes+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadGateway)
		return
	}
	if len(body) > imageMaxBytes {
		http.Error(w, "image exceeds 10 MB limit", http.StatusBadGateway)
		return
	}

	// The upstream Content-Type is ignored. What the bytes are decides what
	// is served and cached.
	ct := sniffImageType(body)
	if ct == "" {
		http.Error(w, "upstream response is not an image", http.StatusBadGateway)
		return
	}

	// Write to cache (best-effort — a write failure is not fatal).
	// Unique temp files prevent concurrent goroutines serving the same URL from
	// clobbering each other mid-write before the atomic rename.
	//
	// priorBytes is measured before the writes so the memoised cache total can
	// be adjusted by the real on-disk delta afterwards (#2340). Measuring both
	// sides rather than adding len(body) keeps it right when this request is
	// refreshing an expired entry instead of adding a new one, and when only
	// one of the two writes succeeded.
	priorBytes := cacheEntryBytes(imgFile, ctFile)
	if mkErr := os.MkdirAll(filepath.Dir(imgFile), imageDirMode); mkErr == nil { // #nosec G301 G304 G703 -- path derived from sha256(url), not user input
		if f, ferr := os.CreateTemp(filepath.Dir(imgFile), ".img-*"); ferr == nil { // #nosec G304
			if _, werr := f.Write(body); werr == nil {
				_ = f.Chmod(imageCacheMode)
				f.Close()
				_ = os.Rename(f.Name(), imgFile) // #nosec G304
			} else {
				f.Close()
				_ = os.Remove(f.Name())
			}
		}
		if f, ferr := os.CreateTemp(filepath.Dir(imgFile), ".ct-*"); ferr == nil { // #nosec G304
			if _, werr := f.Write([]byte(ct)); werr == nil {
				_ = f.Chmod(imageCacheMode)
				f.Close()
				_ = os.Rename(f.Name(), ctFile) // #nosec G304
			} else {
				f.Close()
				_ = os.Remove(f.Name())
			}
		}
	}
	h.addCacheBytes(cacheEntryBytes(imgFile, ctFile) - priorBytes)

	setImageHeaders(w, ct)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	if _, err := w.Write(body); err != nil {
		slog.Warn("imageproxy: write response", "error", err)
	}
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
	// Recount from scratch now that entries have been dropped. This sweep is
	// the memo's periodic source of truth (#2340); a second walk here is free
	// next to the eviction walk that just ran, and much simpler than trying to
	// subtract the removed sizes (the .ct sidecars are deleted alongside their
	// parent, so the walk's own DirEntry for them is already gone by the time
	// it gets there).
	h.sizeMu.Lock()
	defer h.sizeMu.Unlock()
	_, _ = h.refreshCacheSizeLocked()
}

// CacheSize returns the total bytes used by the image cache directory.
//
// The number is memoised (#2340). GET /system/status calls this on every
// request and the frontend calls /system/status from the root shell plus four
// pages, so the old unconditional walk cost two stats per cached cover (the
// image and its .ct sidecar) on every page load. Now the walk runs at most
// once per imageCacheSizeTTL; in between, Serve adjusts the total by the exact
// delta it writes and the daily eviction sweep recounts from scratch.
func (h *ImageProxyHandler) CacheSize() (int64, error) {
	h.sizeMu.Lock()
	defer h.sizeMu.Unlock()
	if !h.sizeAt.IsZero() && time.Since(h.sizeAt) < imageCacheSizeTTL {
		return h.sizeBytes, nil
	}
	return h.refreshCacheSizeLocked()
}

// refreshCacheSizeLocked walks the cache tree and replaces the memoised total.
// Callers must hold sizeMu.
//
// A walk error (in practice: the cache directory does not exist yet) still
// stamps sizeAt, so a fresh install does not re-walk a missing directory on
// every status request. The first successful cache write seeds the total from
// zero, which is correct because an absent directory holds nothing.
func (h *ImageProxyHandler) refreshCacheSizeLocked() (int64, error) {
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
	h.sizeBytes = total
	h.sizeAt = time.Now()
	return total, err
}

// addCacheBytes applies a signed delta to the memoised cache total. Called by
// Serve after a cache write with the exact on-disk change, so the memo stays
// accurate between full recounts. A no-op before the total has ever been
// seeded is harmless: the seeding walk replaces whatever accumulated here.
func (h *ImageProxyHandler) addCacheBytes(delta int64) {
	if delta == 0 {
		return
	}
	h.sizeMu.Lock()
	defer h.sizeMu.Unlock()
	h.sizeBytes += delta
	if h.sizeBytes < 0 {
		h.sizeBytes = 0
	}
}

// cacheEntryBytes reports the bytes a single cache entry currently occupies on
// disk (the image plus its .ct sidecar), and 0 when it is not cached. Two
// stats, taken only on the cache-miss path that is already doing a network
// fetch, which is what lets Serve compute an exact delta even when it is
// overwriting an expired entry rather than adding a new one.
func cacheEntryBytes(imgFile, ctFile string) int64 {
	var n int64
	if fi, err := os.Stat(imgFile); err == nil { // #nosec -- path derived from sha256(url), not user input
		n += fi.Size()
	}
	if fi, err := os.Stat(ctFile); err == nil { // #nosec -- path derived from sha256(url), not user input
		n += fi.Size()
	}
	return n
}

// imageProxyBase is the URL path prefix (BINDERY_URL_BASE, e.g. "/bindery")
// the app is served under. Set once at startup via SetImageProxyBase so proxied
// cover URLs resolve correctly behind a path-prefix reverse proxy. Empty for
// root-mounted deploys.
var imageProxyBase string

// SetImageProxyBase configures the path prefix prepended to proxied image URLs.
// Call once during startup, before serving requests.
func SetImageProxyBase(base string) { imageProxyBase = base }

// ProxyImageURL rewrites a raw external image URL into the local proxy path
// <base>/api/v1/images?url=<encoded>. Returns raw unchanged when it is empty or
// already a relative URL (already proxied or intentionally local).
func ProxyImageURL(raw string) string {
	if raw == "" || strings.HasPrefix(raw, "/") {
		return raw
	}
	return imageProxyBase + "/api/v1/images?url=" + url.QueryEscape(raw)
}

// OPDSImageURL is ProxyImageURL for the OPDS catalogue: same handler, same
// on-disk cache, different mount point.
//
// The feed cannot use /api/v1/images. That route sits behind auth.Middleware,
// which accepts a session cookie or the global API key but not HTTP Basic —
// and Basic is how reading apps authenticate. Pointing the feed at it would
// 401 every KOReader on a password-protected install, and the workaround
// (embedding ?apikey= in every cover link) would publish an unconditionally
// admin credential to every device that caches the feed. /opds/images is the
// same ImageProxyHandler mounted inside the OPDS group so it inherits
// OPDSAuth instead.
func OPDSImageURL(raw string) string {
	if raw == "" || strings.HasPrefix(raw, "/") {
		return raw
	}
	return imageProxyBase + "/opds/images?url=" + url.QueryEscape(raw)
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

// writeBook answers with a book the library holds, its cover as a proxy URL.
// Every handler that returns one book goes through here, the writes as well as
// the reads: the web client swaps the answer to a PUT straight into its state,
// and a raw bindery-cover: reference there turns a stored cover into "No
// cover" until the next reload (#3146).
//
// The response is a copy. A handler may still hand the same struct to a
// background search or sync, and that must keep the real reference.
func writeBook(w http.ResponseWriter, status int, b *models.Book) {
	if b == nil {
		writeJSON(w, status, b)
		return
	}
	writeJSON(w, status, proxiedBook(b))
}

// proxiedBook returns a copy of a book with its cover as a proxy URL, for a
// response body that embeds one.
func proxiedBook(b *models.Book) *models.Book {
	if b == nil {
		return nil
	}
	out := *b
	proxyBookImages(&out)
	return &out
}

// proxiedBooks is proxiedBook for a list. It keeps a nil list nil, so an
// omitempty field stays out of the response.
func proxiedBooks(books []models.Book) []models.Book {
	if books == nil {
		return nil
	}
	out := make([]models.Book, len(books))
	for i := range books {
		out[i] = books[i]
		proxyBookImages(&out[i])
	}
	return out
}

// proxiedAuthor is proxiedBook for an author and its embedded books.
func proxiedAuthor(a *models.Author) *models.Author {
	if a == nil {
		return nil
	}
	out := *a
	out.Books = append([]models.Book(nil), a.Books...)
	proxyAuthorImages(&out)
	return &out
}

// writeAuthor is writeBook for an author: the photo and any embedded books.
func writeAuthor(w http.ResponseWriter, status int, a *models.Author) {
	if a == nil {
		writeJSON(w, status, a)
		return
	}
	writeJSON(w, status, proxiedAuthor(a))
}

// writeSeries is writeBook for one series and the books it embeds.
func writeSeries(w http.ResponseWriter, status int, s *models.Series) {
	if s == nil {
		writeJSON(w, status, s)
		return
	}
	one := []models.Series{*s}
	one[0].Books = make([]models.SeriesBook, len(s.Books))
	for i, sb := range s.Books {
		if sb.Book != nil {
			b := *sb.Book
			sb.Book = &b
		}
		one[0].Books[i] = sb
	}
	proxySeriesImages(one)
	writeJSON(w, status, &one[0])
}

// proxySeriesImages rewrites ImageURL on the books each series embeds. A
// stored cover (bindery-cover:) only loads through the proxy, so a series
// returned raw showed it as a broken image (#3146).
func proxySeriesImages(series []models.Series) {
	for i := range series {
		// Book is a pointer, so the copy range hands out still reaches it.
		for _, sb := range series[i].Books {
			if sb.Book != nil {
				proxyBookImages(sb.Book)
			}
		}
	}
}

// proxyDuplicateGroups rewrites the cover of every member of a duplicate
// review group. The members embed the whole book row, so a stored cover
// (bindery-cover:) went out raw (#3146). The groups are the caller's own
// copy: the library review hands in a page copied from its cached scan.
func proxyDuplicateGroups(groups []duplicates.Group) {
	for _, g := range groups {
		members := g.Members
		for mi := range members {
			proxyBookImages(&members[mi].Book)
		}
	}
}
