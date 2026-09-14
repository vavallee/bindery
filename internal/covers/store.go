// Package covers is the on-disk store for cover images Bindery owns rather
// than fetches: today the cover.jpg beside every book in an imported Calibre
// library (#2564), which the importer used to record as an absolute host path
// that nothing in the app could serve.
//
// A stored cover is referenced from books.image_url / editions.image_url as
// "bindery-cover:<sha256 of the bytes><ext>". The reference deliberately has
// a scheme rather than being a path: api.ProxyImageURL and api.OPDSImageURL
// pass anything starting with "/" through untouched, so a path would bypass
// the BINDERY_URL_BASE prefix and, for OPDS, the Basic-auth mount. With a
// scheme both rewrite it into their normal ?url= form and ImageProxyHandler
// serves the file from this store instead of fetching.
//
// Files are content addressed, so the four formats of one Calibre book (which
// all share one cover.jpg) land as a single file, and a re-import of an
// unchanged library writes nothing.
package covers

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Scheme prefixes every stored-cover reference.
const Scheme = "bindery-cover:"

const (
	// MaxBytes caps a stored cover. Same ceiling as the image proxy: a real
	// cover is a few hundred kilobytes, and a library folder with a 50 MB
	// "cover.jpg" is something the operator should hear about, not something
	// the data dir should absorb silently.
	MaxBytes = 10 * 1024 * 1024
	fileMode = 0o640
	dirMode  = 0o750
)

// keyRe is the only shape a reference's file name may take: a sha256 hex
// digest plus one of the extensions Put can produce. Anything else (a path
// separator, "..", an unexpected extension) is refused before the file system
// is consulted, so a tampered image_url row cannot name a file outside the
// store.
var keyRe = regexp.MustCompile(`^[0-9a-f]{64}\.(jpg|png|webp|gif)$`)

// ErrNotImage is returned by Put when the source is not a raster image
// Bindery serves (JPEG, PNG, WebP or GIF).
var ErrNotImage = errors.New("covers: source is not a supported image")

// ErrTooLarge is returned by Put when the source exceeds MaxBytes.
var ErrTooLarge = errors.New("covers: source exceeds size limit")

// Store keeps covers in one flat directory, <DataDir>/covers in production.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir. The directory is created on the
// first Put; a Store over a missing directory resolves nothing and stores
// nothing, which is the right behaviour for a read-only data dir.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Dir returns the directory the store writes to.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// IsRef reports whether v is a stored-cover reference (regardless of whether
// the file behind it still exists).
func IsRef(v string) bool {
	return strings.HasPrefix(strings.TrimSpace(v), Scheme)
}

// Put copies the image at src into the store and returns its reference.
// The source is sniffed, not trusted by extension: a cover.jpg holding HTML
// is refused, as is anything over MaxBytes. Putting the same bytes twice
// returns the same reference and writes nothing the second time.
func (s *Store) Put(src string) (string, error) {
	if s == nil || s.dir == "" {
		return "", errors.New("covers: store not configured")
	}
	f, err := os.Open(src) // #nosec G304 -- the importer passes the cover path it read from the Calibre library it was pointed at
	if err != nil {
		return "", err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > MaxBytes {
		return "", ErrTooLarge
	}
	ext := extFor(http.DetectContentType(body))
	if ext == "" {
		return "", ErrNotImage
	}
	sum := sha256.Sum256(body)
	name := fmt.Sprintf("%x", sum) + ext
	dst := filepath.Join(s.dir, name)
	if info, err := os.Stat(dst); err == nil && !info.IsDir() && info.Size() == int64(len(body)) {
		return Scheme + name, nil
	}
	if err := os.MkdirAll(s.dir, dirMode); err != nil {
		return "", err
	}
	// Temp file plus rename so a concurrent Put of the same bytes (two
	// editions of one book imported on different goroutines) never serves a
	// half-written file.
	tmp, err := os.CreateTemp(s.dir, ".cover-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Chmod(fileMode); err != nil {
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
	return Scheme + name, nil
}

// Resolve maps a reference to the file that backs it and its content type.
// ok is false for anything that is not a well-formed reference, for a
// reference whose file is missing, and for any name that would escape the
// store directory. Callers must not serve a path Resolve did not return.
func (s *Store) Resolve(ref string) (path, contentType string, ok bool) {
	if s == nil || s.dir == "" || !IsRef(ref) {
		return "", "", false
	}
	name := strings.TrimPrefix(strings.TrimSpace(ref), Scheme)
	if !keyRe.MatchString(name) {
		return "", "", false
	}
	path = filepath.Join(s.dir, name)
	// keyRe already forbids separators, but the containment check is what
	// the security review will look for, and it costs nothing: the served
	// file must sit directly inside the store.
	rel, err := filepath.Rel(s.dir, path)
	if err != nil || !filepath.IsLocal(rel) || rel != name {
		return "", "", false
	}
	// Lstat, not Stat: a symlink dropped into the store dir under a digest
	// shaped name must not be followed to whatever it points at.
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", false
	}
	return path, contentTypeFor(filepath.Ext(name)), true
}

func extFor(contentType string) string {
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

func contentTypeFor(ext string) string {
	switch ext {
	case ".jpg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}
