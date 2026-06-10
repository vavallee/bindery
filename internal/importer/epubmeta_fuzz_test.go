package importer

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// maxEpubFuzzInput caps fuzz inputs at 1 MiB. ReadEpubMetadata opens a zip and
// parses two XML members; the cap keeps each iteration cheap and stops the
// mutator from wandering into multi-megabyte archives that only test the
// allocator.
const maxEpubFuzzInput = 1 << 20

// fuzzEpubZip builds an in-memory zip from name→body pairs (ordered slice so
// seeds are deterministic).
func fuzzEpubZip(f *testing.F, entries [][2]string) []byte {
	f.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			f.Fatal(err)
		}
		if _, err := w.Write([]byte(e[1])); err != nil {
			f.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		f.Fatal(err)
	}
	return buf.Bytes()
}

// FuzzReadEpubMetadata exercises the EPUB embedded-metadata reader (the #1014
// import-matching path) with arbitrary bytes written to a temp .epub. The
// reader runs on downloaded, untrusted archives, so it must never panic and
// must stay fast on any input: a hostile zip/OPF must come back as an error
// (and the caller falls back to filename parsing), never a crash or a hang.
func FuzzReadEpubMetadata(f *testing.F) {
	container := `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`
	opf := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
  <metadata>
    <dc:title>Project Hail Mary</dc:title>
    <dc:creator opf:role="aut">Andy Weir</dc:creator>
    <dc:identifier>urn:isbn:9780593135204</dc:identifier>
  </metadata>
</package>`

	seeds := [][]byte{
		nil,
		[]byte("not a zip at all"),
		[]byte("PK\x03\x04"), // zip magic, truncated
		// Fully valid minimal EPUB.
		fuzzEpubZip(f, [][2]string{
			{"mimetype", "application/epub+zip"},
			{"META-INF/container.xml", container},
			{"OEBPS/content.opf", opf},
		}),
		// Valid zip, no container.xml.
		fuzzEpubZip(f, [][2]string{{"mimetype", "application/epub+zip"}}),
		// container.xml points at a missing OPF.
		fuzzEpubZip(f, [][2]string{{"META-INF/container.xml", container}}),
		// Malformed container XML.
		fuzzEpubZip(f, [][2]string{{"META-INF/container.xml", "<container><rootfiles>"}}),
		// Rootfile with an absolute / dot-laced path that needs cleaning.
		fuzzEpubZip(f, [][2]string{
			{"META-INF/container.xml", `<container><rootfiles><rootfile full-path="/./OEBPS/../OEBPS/content.opf"/></rootfiles></container>`},
			{"OEBPS/content.opf", opf},
		}),
		// Malformed OPF XML mid-element.
		fuzzEpubZip(f, [][2]string{
			{"META-INF/container.xml", container},
			{"OEBPS/content.opf", "<package><metadata><dc:title>unterminated"},
		}),
		// OPF with an ISBN-10 identifier and no role on the creator.
		fuzzEpubZip(f, [][2]string{
			{"META-INF/container.xml", container},
			{"OEBPS/content.opf", `<package><metadata><dc:creator>Someone</dc:creator><dc:identifier>isbn:034547219X</dc:identifier></metadata></package>`},
		}),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	// One temp file per worker process, overwritten each iteration —
	// t.TempDir() inside the fuzz function costs a mkdir+rm per exec and
	// throttles throughput to a crawl.
	p := filepath.Join(f.TempDir(), "in.epub")

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxEpubFuzzInput {
			t.Skip("input over size cap")
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}

		start := time.Now()
		meta, err := ReadEpubMetadata(p) // must not panic on any input
		// Generous wall-clock bound: a ≤1 MiB archive that takes this long
		// (e.g. a decompression bomb in container.xml/OPF) is a real DoS
		// finding on the import path, not fuzzer noise.
		if elapsed := time.Since(start); elapsed > 20*time.Second {
			t.Fatalf("ReadEpubMetadata took %v on a %d-byte input", elapsed, len(data))
		}
		if err != nil {
			// Error path is the fallback-to-filename-parsing contract: it must
			// hand back a zero value, not partially-populated metadata.
			if meta != (EpubMetadata{}) {
				t.Fatalf("non-zero metadata %+v alongside error %v", meta, err)
			}
			return
		}
		// ISBN, when present, is normalised: ISBN-13 (digits, 978/979) or
		// ISBN-10 (digits with optional X check digit).
		switch len(meta.ISBN) {
		case 0:
		case 13:
			if strings.Trim(meta.ISBN, "0123456789") != "" ||
				(!strings.HasPrefix(meta.ISBN, "978") && !strings.HasPrefix(meta.ISBN, "979")) {
				t.Fatalf("malformed ISBN-13 %q", meta.ISBN)
			}
		case 10:
			if strings.Trim(meta.ISBN, "0123456789X") != "" {
				t.Fatalf("malformed ISBN-10 %q", meta.ISBN)
			}
		default:
			t.Fatalf("ISBN with impossible length: %q", meta.ISBN)
		}
	})
}
