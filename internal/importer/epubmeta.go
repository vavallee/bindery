package importer

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
)

// EpubMetadata is the subset of an EPUB's embedded Dublin Core metadata the
// importer uses to match a downloaded ebook to a catalogue book when the
// download has no book association (e.g. a free-text Search grab). Embedded
// metadata is far more reliable than the release filename, which routinely
// encodes author/title/series in inconsistent orders (issue #1014).
type EpubMetadata struct {
	Title  string
	Author string
	ISBN   string // normalised (digits only); ISBN-13 preferred over ISBN-10
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
	rc, err := opf.Open()
	if err != nil {
		return EpubMetadata{}, fmt.Errorf("epub: open opf: %w", err)
	}
	defer rc.Close()

	return parseOPFMetadata(rc)
}

// epubOPFPath reads META-INF/container.xml and returns the full-path of the
// first rootfile (the OPF package document).
func epubOPFPath(zr *zip.ReadCloser) (string, error) {
	f := findZipFile(zr, "META-INF/container.xml")
	if f == nil {
		return "", fmt.Errorf("epub: META-INF/container.xml missing")
	}
	rc, err := f.Open()
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
func extractISBN(raw string) (isbn13, isbn10 string) {
	// Strip everything but digits and X (ISBN-10 check digit).
	var b strings.Builder
	for _, r := range raw {
		if (r >= '0' && r <= '9') || r == 'X' || r == 'x' {
			b.WriteRune(r)
		}
	}
	digits := strings.ToUpper(b.String())
	switch {
	case len(digits) == 13 && (strings.HasPrefix(digits, "978") || strings.HasPrefix(digits, "979")):
		return digits, ""
	case len(digits) == 10:
		return "", digits
	default:
		return "", ""
	}
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
