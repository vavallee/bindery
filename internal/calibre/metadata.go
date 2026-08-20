package calibre

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
)

const (
	coverMaxBytes = 10 * 1024 * 1024
	coverFileMode = 0o640
	coverDirMode  = 0o750
)

var (
	identifierTypeRe    = regexp.MustCompile(`[^a-z0-9_-]+`)
	calibredbDateOnlyRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// Metadata is the Bindery-owned metadata contract passed to every Calibre
// handoff. It represents the Calibre database fields Bindery can populate
// from its own book, author, edition, and series records.
type Metadata struct {
	Title         string            `json:"title,omitempty"`
	Authors       []string          `json:"authors,omitempty"`
	AuthorSort    string            `json:"authorSort,omitempty"`
	Description   string            `json:"description,omitempty"`
	Publisher     string            `json:"publisher,omitempty"`
	PublishedDate string            `json:"publishedDate,omitempty"`
	Genres        []string          `json:"genres,omitempty"`
	Language      string            `json:"language,omitempty"`
	Series        string            `json:"series,omitempty"`
	SeriesIndex   string            `json:"seriesIndex,omitempty"`
	Rating        float64           `json:"rating,omitempty"`
	Identifiers   map[string]string `json:"identifiers,omitempty"`
	CoverPath     string            `json:"coverPath,omitempty"`
}

func (m Metadata) empty() bool {
	return strings.TrimSpace(m.Title) == "" &&
		len(m.Authors) == 0 &&
		strings.TrimSpace(m.AuthorSort) == "" &&
		strings.TrimSpace(m.Description) == "" &&
		strings.TrimSpace(m.Publisher) == "" &&
		strings.TrimSpace(m.PublishedDate) == "" &&
		len(m.Genres) == 0 &&
		strings.TrimSpace(m.Language) == "" &&
		strings.TrimSpace(m.Series) == "" &&
		strings.TrimSpace(m.SeriesIndex) == "" &&
		m.Rating <= 0 &&
		len(m.Identifiers) == 0 &&
		strings.TrimSpace(m.CoverPath) == ""
}

func (m Metadata) addArgs() []string {
	args := make([]string, 0, 18)
	if v := strings.TrimSpace(m.Title); v != "" {
		args = append(args, "--title", v)
	}
	if authors := cleanList(m.Authors); len(authors) > 0 {
		args = append(args, "--authors", strings.Join(authors, " & "))
	}
	if v := strings.TrimSpace(m.CoverPath); v != "" {
		args = append(args, "--cover", v)
	}
	for _, ident := range identifierArgs(m.Identifiers) {
		args = append(args, "--identifier", ident)
	}
	if v := strings.TrimSpace(m.Language); v != "" {
		args = append(args, "--languages", v)
	}
	if v := strings.TrimSpace(m.Series); v != "" {
		args = append(args, "--series", v)
	}
	if v := cleanCalibreSeriesIndex(m.SeriesIndex); v != "" {
		args = append(args, "--series-index", v)
	}
	if tags := cleanList(m.Genres); len(tags) > 0 {
		args = append(args, "--tags", strings.Join(tags, ","))
	}
	return args
}

func (m Metadata) setFields() []string {
	fields := make([]string, 0, 10)
	if v := strings.TrimSpace(m.Description); v != "" {
		fields = append(fields, "comments:"+v)
	}
	if v := strings.TrimSpace(m.AuthorSort); v != "" {
		fields = append(fields, "author_sort:"+v)
	}
	if v := strings.TrimSpace(m.Publisher); v != "" {
		fields = append(fields, "publisher:"+v)
	}
	if v := strings.TrimSpace(m.PublishedDate); v != "" {
		fields = append(fields, "pubdate:"+formatCalibredbPubdate(v))
	}
	if m.Rating > 0 {
		fields = append(fields, "rating:"+strconv.FormatFloat(m.Rating, 'f', -1, 64))
	}
	if v := strings.TrimSpace(m.Language); v != "" {
		fields = append(fields, "languages:"+v)
	}
	if tags := cleanList(m.Genres); len(tags) > 0 {
		fields = append(fields, "tags:"+strings.Join(tags, ","))
	}
	if v := strings.TrimSpace(m.Series); v != "" {
		fields = append(fields, "series:"+v)
	}
	if v := cleanCalibreSeriesIndex(m.SeriesIndex); v != "" {
		fields = append(fields, "series_index:"+v)
	}
	return fields
}

func cleanCalibreSeriesIndex(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return ""
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func formatCalibredbPubdate(v string) string {
	v = strings.TrimSpace(v)
	if calibredbDateOnlyRe.MatchString(v) {
		return v + "T00:00:00+00:00"
	}
	return v
}

func cleanList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	return out
}

func identifierArgs(in map[string]string) []string {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(in))
	for _, key := range keys {
		typ := cleanIdentifierType(key)
		val := cleanIdentifierValue(in[key])
		if typ == "" || val == "" {
			continue
		}
		out = append(out, typ+":"+val)
	}
	return out
}

func cleanIdentifierType(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = identifierTypeRe.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}

func cleanIdentifierValue(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", " ")
	return strings.Join(strings.Fields(s), " ")
}

// NormalizeLanguageForCalibre converts common ISO-639-2 and provider-specific
// language values into the short codes Calibre documents for CLI metadata
// updates. Unknown ISO-639-2 values are returned lowercased; Calibre accepts
// many ISO language values beyond the examples in its manual.
func NormalizeLanguageForCalibre(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return ""
	}
	switch code {
	case "eng":
		return "en"
	case "fre", "fra":
		return "fr"
	case "ger", "deu":
		return "de"
	case "spa":
		return "es"
	case "ita":
		return "it"
	case "por":
		return "pt"
	case "dut", "nld":
		return "nl"
	case "swe":
		return "sv"
	case "dan":
		return "da"
	case "nor":
		return "no"
	case "fin":
		return "fi"
	case "rus":
		return "ru"
	case "jpn":
		return "ja"
	case "chi", "zho":
		return "zh"
	case "kor":
		return "ko"
	case "pol":
		return "pl"
	case "tur":
		return "tr"
	case "ukr":
		return "uk"
	case "ara":
		return "ar"
	case "ind":
		return "id"
	case "tgl", "fil":
		return "tl"
	default:
		return code
	}
}

func FormatPublishedDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

// coverFetchClient is shared across cover downloads. It used to be built
// inline on every call, which meant a fresh Transport and therefore a fresh
// connection pool per cover: no keep-alive reuse between two covers from the
// same host, which is the normal case when importing a Calibre library. Every
// other outbound client in the codebase is built once in a constructor and
// reused; this was the one exception.
//
// The disk cache in findExistingCover suppresses repeat fetches of the same
// URL, so this only shows up on the first import of a library, which is
// exactly when there are the most covers to fetch.
var coverFetchClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if err := httpsec.ValidateOutboundURL(req.URL.String(), httpsec.PolicyStrict); err != nil {
			return fmt.Errorf("redirect blocked: %w", err)
		}
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

// MaterializeCover fetches an external cover into cacheDir and returns a local
// path Calibre can consume. It validates outbound URLs to avoid SSRF and keeps
// failures non-fatal for callers.
func MaterializeCover(ctx context.Context, cacheDir, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if cacheDir == "" || rawURL == "" {
		return "", nil
	}
	if err := httpsec.ValidateOutboundURL(rawURL, httpsec.PolicyStrict); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(rawURL))
	key := fmt.Sprintf("%x", sum)
	if existing, ok := findExistingCover(cacheDir, key); ok {
		return existing, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := coverFetchClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cover returned status %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	ext := coverExt(ct)
	if ext == "" {
		return "", fmt.Errorf("cover response is not an image")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, coverMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > coverMaxBytes {
		return "", fmt.Errorf("cover exceeds %d bytes", coverMaxBytes)
	}
	if err := os.MkdirAll(cacheDir, coverDirMode); err != nil {
		return "", err
	}
	dst := filepath.Join(cacheDir, key+ext)
	tmp, err := os.CreateTemp(cacheDir, ".cover-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Chmod(coverFileMode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return dst, nil
}

func findExistingCover(cacheDir, key string) (string, bool) {
	for _, ext := range []string{".jpg", ".png", ".webp", ".gif"} {
		path := filepath.Join(cacheDir, key+ext)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, true
		}
	}
	return "", false
}

func coverExt(contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch contentType {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ""
	}
}
