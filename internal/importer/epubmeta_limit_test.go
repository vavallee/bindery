package importer

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// oversizedEntryBytes is how much a hostile entry in these tests inflates to:
// four times the cap. A run of one repeated byte deflates roughly 1000:1, so
// the archive on disk stays tiny while the entry is far past any real OPF or
// container.xml.
const oversizedEntryBytes = 4 * maxEpubMetadataEntryBytes

// writeEpubEntries writes a zip with the given name to body pairs, streaming
// each body from a reader so the test never holds the inflated bytes itself.
func writeEpubEntries(t *testing.T, entries []struct {
	name string
	body io.Reader
}) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "book.epub")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(w, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

// repeatReader yields n copies of b.
func repeatReader(b byte, n int64) io.Reader {
	return io.LimitReader(byteRepeater(b), n)
}

type byteRepeater byte

func (r byteRepeater) Read(p []byte) (int, error) {
	if len(p) > 0 {
		p[0] = byte(r)
		for filled := 1; filled < len(p); filled *= 2 {
			copy(p[filled:], p[:filled])
		}
	}
	return len(p), nil
}

const testContainerXML = `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`

// readEpubMeasured runs ReadEpubMetadata and reports how many bytes the heap
// allocated while it ran.
func readEpubMeasured(t *testing.T, p string) (EpubMetadata, uint64, error) {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	meta, err := ReadEpubMetadata(p)
	runtime.ReadMemStats(&after)
	return meta, after.TotalAlloc - before.TotalAlloc, err
}

// shortErr keeps a failure message readable when the error embeds a value
// read from the hostile entry.
func shortErr(err error) string {
	if err == nil {
		return "<nil>"
	}
	if msg := err.Error(); len(msg) > 120 {
		return fmt.Sprintf("%s... (%d bytes)", msg[:120], len(msg))
	}
	return err.Error()
}

// allocBudget is far above what a bounded read of one capped entry costs and
// half of what parsing the oversized entry costs at the very least (the
// returned title alone is oversizedEntryBytes, and the decoder buffers and
// copies the token before that).
const allocBudget = oversizedEntryBytes / 2

// TestReadEpubMetadata_OversizedTitleIsBounded is the regression for an EPUB
// whose OPF holds a dc:title that inflates to tens of megabytes. Before the
// cap the decoder buffered the whole title, so a small archive could make
// every import allocate gigabytes. The read must now fail cleanly, without
// returning the giant title, and without allocating anything close to the
// inflated size.
func TestReadEpubMetadata_OversizedTitleIsBounded(t *testing.T) {
	head := `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>`
	tail := `</dc:title></metadata></package>`
	opf := io.MultiReader(strings.NewReader(head), repeatReader('a', oversizedEntryBytes), strings.NewReader(tail))
	p := writeEpubEntries(t, []struct {
		name string
		body io.Reader
	}{
		{"META-INF/container.xml", strings.NewReader(testContainerXML)},
		{"OEBPS/content.opf", opf},
	})

	meta, alloc, err := readEpubMeasured(t, p)
	if err == nil {
		t.Errorf("expected an error for an oversized OPF, got title of %d bytes", len(meta.Title))
	} else if !errors.Is(err, errEpubEntryTooLarge) {
		t.Errorf("expected errEpubEntryTooLarge, got %s", shortErr(err))
	}
	if len(meta.Title) > maxEpubMetadataEntryBytes {
		t.Errorf("returned title is %d bytes, above the %d byte cap", len(meta.Title), maxEpubMetadataEntryBytes)
	}
	if alloc > allocBudget {
		t.Errorf("allocated %d MiB reading an oversized OPF, budget %d MiB", alloc>>20, allocBudget>>20)
	}
}

// TestReadEpubMetadata_OversizedContainerIsBounded covers the same attack on
// META-INF/container.xml, which is parsed before the OPF is even located.
func TestReadEpubMetadata_OversizedContainerIsBounded(t *testing.T) {
	head := `<?xml version="1.0"?><container><rootfiles><rootfile full-path="`
	tail := `"/></rootfiles></container>`
	container := io.MultiReader(strings.NewReader(head), repeatReader('a', oversizedEntryBytes), strings.NewReader(tail))
	p := writeEpubEntries(t, []struct {
		name string
		body io.Reader
	}{
		{"META-INF/container.xml", container},
	})

	_, alloc, err := readEpubMeasured(t, p)
	if !errors.Is(err, errEpubEntryTooLarge) {
		t.Errorf("expected errEpubEntryTooLarge, got %s", shortErr(err))
	}
	if alloc > allocBudget {
		t.Errorf("allocated %d MiB reading an oversized container.xml, budget %d MiB", alloc>>20, allocBudget>>20)
	}
}

// TestReadEpubMetadata_LargeButLegitimateOPF pins that the cap is generous:
// an OPF padded well past any real one (a 1 MiB description) still yields its
// metadata.
func TestReadEpubMetadata_LargeButLegitimateOPF(t *testing.T) {
	opf := `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:description>` + strings.Repeat("lorem ipsum ", (1<<20)/12) + `</dc:description>
    <dc:title>Pandora's Star</dc:title>
    <dc:creator>Peter F. Hamilton</dc:creator>
  </metadata>
</package>`
	p := writeTestEpub(t, "OEBPS/content.opf", opf)
	meta, err := ReadEpubMetadata(p)
	if err != nil {
		t.Fatalf("ReadEpubMetadata: %v", err)
	}
	if meta.Title != "Pandora's Star" || meta.Author != "Peter F. Hamilton" {
		t.Errorf("got title %q author %q", meta.Title, meta.Author)
	}
}

// TestCappedReader checks the enforcement that does not depend on the zip
// header: a stream exactly at the cap reads through, one byte more fails.
func TestCappedReader(t *testing.T) {
	const capBytes = 1024
	got, err := io.ReadAll(newCappedReader(bytes.NewReader(make([]byte, capBytes)), capBytes))
	if err != nil || len(got) != capBytes {
		t.Fatalf("at the cap: got %d bytes, err %v", len(got), err)
	}
	_, err = io.ReadAll(newCappedReader(bytes.NewReader(make([]byte, capBytes+1)), capBytes))
	if !errors.Is(err, errEpubEntryTooLarge) {
		t.Fatalf("one byte over the cap: expected errEpubEntryTooLarge, got %v", err)
	}
	// A stream that never ends must still stop at the cap.
	n, err := io.Copy(io.Discard, newCappedReader(byteRepeater('x'), capBytes))
	if !errors.Is(err, errEpubEntryTooLarge) || n > capBytes {
		t.Fatalf("endless stream: copied %d bytes, err %v", n, err)
	}
}
