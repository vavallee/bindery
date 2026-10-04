package importer

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
)

// EpubMetadata is the subset of an EPUB's embedded Dublin Core metadata the
// importer uses to match a downloaded ebook to a catalogue book when the
// download has no book association (e.g. a free-text Search grab). Embedded
// metadata is far more reliable than the release filename, which routinely
// encodes author/title/series in inconsistent orders (issue #1014).
type EpubMetadata struct {
	Title    string
	Author   string
	ISBN     string // normalised (digits only); ISBN-13 preferred over ISBN-10
	Language string // ISO 639-2/B code from dc:language, normalised ("en" → "eng")
	// Languages is every dc:language the OPF declares, normalised, in document
	// order with duplicates dropped. Language is its first entry. A bilingual
	// edition declares both, and the import language check (#2998) must see
	// the second one too.
	Languages []string
}

// IsEpubFile reports whether path is an EPUB we can read embedded metadata from.
func IsEpubFile(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".epub"
}

// ReadEpubMetadata extracts dc:title, dc:creator, and an ISBN dc:identifier
// from an EPUB's OPF package document. It is best-effort: any error (not a zip,
// missing container, malformed OPF) is returned so the caller falls back to
// filename parsing rather than failing the import.
func ReadEpubMetadata(path string) (EpubMetadata, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return EpubMetadata{}, fmt.Errorf("epub: open zip: %w", err)
	}
	defer func() { _ = zr.Close() }()

	opfPath, err := epubOPFPath(zr)
	if err != nil {
		return EpubMetadata{}, err
	}

	opf := findZipFile(zr, opfPath)
	if opf == nil {
		return EpubMetadata{}, fmt.Errorf("epub: opf %q not found in archive", opfPath)
	}
	rc, err := openCappedEntry(opf)
	if err != nil {
		return EpubMetadata{}, fmt.Errorf("epub: open opf: %w", err)
	}
	defer rc.Close()

	return parseOPFMetadata(rc)
}

// maxEpubMetadataEntryBytes caps how much of a single zip entry the metadata
// reader will inflate. The XML decoder buffers a whole text token before
// returning it, so without a cap a tiny archive whose dc:title is one long run
// of a repeated byte inflates to hundreds of megabytes per import, and the
// downloaded file is untrusted.
//
// Real entries are nowhere near this. container.xml is a few hundred bytes. An
// OPF is typically under 50 KiB, and the largest ones (omnibus editions with
// thousands of manifest and spine items, or long embedded descriptions) stay
// in the hundreds of KiB. 4 MiB leaves an order of magnitude of headroom over
// those while bounding the worst case to a few times 4 MiB of allocation.
const maxEpubMetadataEntryBytes = 4 << 20

// errEpubEntryTooLarge reports a zip entry past maxEpubMetadataEntryBytes. The
// metadata read fails with it, so the import carries on without embedded
// metadata exactly as it does for any other unreadable OPF.
var errEpubEntryTooLarge = fmt.Errorf("epub: entry exceeds %d bytes", maxEpubMetadataEntryBytes)

// openCappedEntry opens f for a metadata read bounded by
// maxEpubMetadataEntryBytes. The declared size in the zip header is checked
// first as a cheap early reject, but it is attacker controlled, so the reader
// itself also refuses to return more than the cap whatever the header claims.
func openCappedEntry(f *zip.File) (io.ReadCloser, error) {
	if f.UncompressedSize64 > maxEpubMetadataEntryBytes {
		return nil, fmt.Errorf("%s: %w", f.Name, errEpubEntryTooLarge)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{newCappedReader(rc, maxEpubMetadataEntryBytes), rc}, nil
}

// cappedReader passes through at most limit bytes of r and fails with
// errEpubEntryTooLarge if r has more. Unlike io.LimitReader it does not turn
// an oversized entry into a clean, silently truncated EOF.
type cappedReader struct {
	r         io.Reader
	remaining int64
}

func newCappedReader(r io.Reader, limit int64) io.Reader {
	return &cappedReader{r: r, remaining: limit}
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.remaining < 0 {
		return 0, errEpubEntryTooLarge
	}
	// Allow one byte past the limit so a stream that ends exactly at the cap
	// is told apart from one that keeps going.
	if int64(len(p)) > c.remaining+1 {
		p = p[:c.remaining+1]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	if c.remaining < 0 {
		return n + int(c.remaining), errEpubEntryTooLarge
	}
	return n, err
}

// epubOPFPath reads META-INF/container.xml and returns the full-path of the
// first rootfile (the OPF package document).
func epubOPFPath(zr *zip.ReadCloser) (string, error) {
	f := findZipFile(zr, "META-INF/container.xml")
	if f == nil {
		return "", fmt.Errorf("epub: META-INF/container.xml missing")
	}
	rc, err := openCappedEntry(f)
	if err != nil {
		return "", fmt.Errorf("epub: open container.xml: %w", err)
	}
	defer rc.Close()

	var container struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.NewDecoder(rc).Decode(&container); err != nil {
		return "", fmt.Errorf("epub: parse container.xml: %w", err)
	}
	if len(container.Rootfiles) == 0 || strings.TrimSpace(container.Rootfiles[0].FullPath) == "" {
		return "", fmt.Errorf("epub: no rootfile in container.xml")
	}
	// Zip paths are forward-slash and not OS-dependent; clean any ./ noise.
	return strings.TrimPrefix(path.Clean(container.Rootfiles[0].FullPath), "/"), nil
}

// parseOPFMetadata walks the OPF XML namespace-agnostically and pulls the first
// dc:title, the first dc:creator (preferring one marked role="aut"), and the
// first dc:identifier that looks like an ISBN. Walking tokens rather than
// binding a fixed struct keeps it resilient to the many real-world OPF
// namespace-prefix and ordering variations.
func parseOPFMetadata(r io.Reader) (EpubMetadata, error) {
	dec := xml.NewDecoder(r)
	var (
		meta       EpubMetadata
		cur        string     // local name of the element we're inside
		curAttrs   []xml.Attr // its attributes
		creatorAut string     // a creator explicitly marked role="aut"
		isbn10     string     // fallback if no ISBN-13 found
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return EpubMetadata{}, fmt.Errorf("epub: parse opf: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			cur = strings.ToLower(t.Name.Local)
			curAttrs = t.Attr
		case xml.CharData:
			val := strings.TrimSpace(string(t))
			if val == "" {
				continue
			}
			switch cur {
			case "title":
				if meta.Title == "" {
					meta.Title = val
				}
			case "creator":
				if meta.Author == "" {
					meta.Author = val
				}
				if attrRole(curAttrs) == "aut" && creatorAut == "" {
					creatorAut = val
				}
			case "identifier":
				if i13, i10 := extractISBN(val); i13 != "" {
					meta.ISBN = i13
				} else if i10 != "" && isbn10 == "" {
					isbn10 = i10
				}
			case "language":
				// First dc:language wins. Normalise to the ISO 639-2/B code the
				// language filter and metadata profiles use so a value like "en"
				// or "en-US" matches an "eng" profile (#1160).
				if code := models.NormalizeLanguageCode(val); code != "" {
					if meta.Language == "" {
						meta.Language = code
					}
					if !slices.Contains(meta.Languages, code) {
						meta.Languages = append(meta.Languages, code)
					}
				}
			}
		case xml.EndElement:
			cur = ""
			curAttrs = nil
		}
	}
	// Prefer an explicit author (role="aut") over the first creator (which may
	// be an illustrator/editor in multi-creator books).
	if creatorAut != "" {
		meta.Author = creatorAut
	}
	if meta.ISBN == "" {
		meta.ISBN = isbn10
	}
	return meta, nil
}

// attrRole returns the opf:role attribute value (namespace-agnostic) lowercased.
func attrRole(attrs []xml.Attr) string {
	for _, a := range attrs {
		if strings.EqualFold(a.Name.Local, "role") {
			return strings.ToLower(strings.TrimSpace(a.Value))
		}
	}
	return ""
}

// extractISBN pulls a normalised ISBN out of a dc:identifier value such as
// "urn:isbn:9780345472199", "isbn:0345472195", or a bare number. Returns
// (isbn13, isbn10); at most one is non-empty.
//
// The scanning lives in isbnutil so that an ISBN reaching Bindery through an
// EPUB, through a DNB MARC record or through an Audiobookshelf item is reduced
// to the same string. This wrapper stays because the OPF walk above reads
// better naming what it is pulling out of a dc:identifier.
func extractISBN(raw string) (isbn13, isbn10 string) {
	return isbnutil.Extract(raw)
}

// findZipFile returns the zip entry whose name matches target (case-sensitive,
// forward-slash), or nil.
func findZipFile(zr *zip.ReadCloser, target string) *zip.File {
	for _, f := range zr.File {
		if f.Name == target {
			return f
		}
	}
	return nil
}
