package importer

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// writeTestEpub builds a minimal valid-enough EPUB (zip with container.xml +
// OPF) at a temp path and returns it.
func writeTestEpub(t *testing.T, opfPath, opfBody string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "book.epub")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	container := `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="` + opfPath + `" media-type="application/oebps-package+xml"/></rootfiles>
</container>`
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("META-INF/container.xml", container)
	add(opfPath, opfBody)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadEpubMetadata(t *testing.T) {
	// Standard dc: prefixed OPF with an ISBN identifier and role="aut" creator.
	opf := `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub-id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Pandora's Star</dc:title>
    <dc:creator opf:role="aut">Peter F. Hamilton</dc:creator>
    <dc:identifier id="pub-id">urn:isbn:9780345472199</dc:identifier>
    <dc:language>en</dc:language>
  </metadata>
</package>`
	p := writeTestEpub(t, "OEBPS/content.opf", opf)

	meta, err := ReadEpubMetadata(p)
	if err != nil {
		t.Fatalf("ReadEpubMetadata: %v", err)
	}
	if meta.Title != "Pandora's Star" {
		t.Errorf("Title = %q, want %q", meta.Title, "Pandora's Star")
	}
	if meta.Author != "Peter F. Hamilton" {
		t.Errorf("Author = %q, want %q", meta.Author, "Peter F. Hamilton")
	}
	if meta.ISBN != "9780345472199" {
		t.Errorf("ISBN = %q, want %q", meta.ISBN, "9780345472199")
	}
	// dc:language "en" is normalised to the ISO 639-2/B code the filter uses.
	if meta.Language != "eng" {
		t.Errorf("Language = %q, want %q", meta.Language, "eng")
	}
}

func TestReadEpubMetadata_LanguageRegionSubtag(t *testing.T) {
	// A region-qualified dc:language ("de-DE") normalises to the bare 639-2/B code.
	opf := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Der Schwarm</dc:title>
    <dc:creator>Frank Schätzing</dc:creator>
    <dc:language>de-DE</dc:language>
  </metadata>
</package>`
	p := writeTestEpub(t, "content.opf", opf)
	meta, err := ReadEpubMetadata(p)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Language != "ger" {
		t.Errorf("Language = %q, want %q", meta.Language, "ger")
	}
}

func TestReadEpubMetadata_PrefersAutCreator(t *testing.T) {
	// First creator is an illustrator; the role="aut" creator must win.
	opf := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>The Way of Kings</dc:title>
    <dc:creator opf:role="ill">Some Illustrator</dc:creator>
    <dc:creator opf:role="aut">Brandon Sanderson</dc:creator>
  </metadata>
</package>`
	p := writeTestEpub(t, "content.opf", opf)
	meta, err := ReadEpubMetadata(p)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Author != "Brandon Sanderson" {
		t.Errorf("Author = %q, want %q (role=aut should win)", meta.Author, "Brandon Sanderson")
	}
}

func TestReadEpubMetadata_NonEpubFails(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notazip.epub")
	if err := os.WriteFile(p, []byte("this is not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEpubMetadata(p); err == nil {
		t.Error("expected error reading a non-zip .epub, got nil")
	}
}

func TestExtractISBN(t *testing.T) {
	cases := []struct{ in, want13, want10 string }{
		{"urn:isbn:9780345472199", "9780345472199", ""},
		{"isbn:0345472195", "", "0345472195"},
		{"9780345472199", "9780345472199", ""},
		{"0-345-47219-5", "", "0345472195"},
		{"urn:uuid:1234", "", ""},
		{"123456789X", "", "123456789X"},
	}
	for _, c := range cases {
		g13, g10 := extractISBN(c.in)
		if g13 != c.want13 || g10 != c.want10 {
			t.Errorf("extractISBN(%q) = (%q,%q), want (%q,%q)", c.in, g13, g10, c.want13, c.want10)
		}
	}
}

func TestIsEpubFile(t *testing.T) {
	if !IsEpubFile("/x/Book.EPUB") {
		t.Error("IsEpubFile should be case-insensitive on extension")
	}
	if IsEpubFile("/x/Book.mobi") {
		t.Error("mobi is not an epub")
	}
}
