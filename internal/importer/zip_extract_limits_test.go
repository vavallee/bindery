package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// Mutation findings for the zip guards: each test here fails when the guard it
// names is removed.

func writeZipFile(t *testing.T, folder, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(folder, name), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newJobFolder(t *testing.T) (folder string, roots []string) {
	t.Helper()
	root := t.TempDir()
	folder = filepath.Join(root, "job")
	if err := os.MkdirAll(folder, 0o750); err != nil {
		t.Fatal(err)
	}
	return folder, []string{root}
}

func readerFor(t *testing.T, b []byte) *zip.Reader {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}

// rawEntries builds an archive whose headers claim declared bytes each, with a
// few real bytes behind them.
func rawEntries(t *testing.T, n int, declared uint64) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := range n {
		w, err := zw.CreateRaw(&zip.FileHeader{Name: fmt.Sprintf("f%d.bin", i), Method: zip.Deflate, CRC32: 1,
			CompressedSize64: 10, UncompressedSize64: declared})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(make([]byte, 10))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPlanZip_DeclaredBytesShareOneBudget(t *testing.T) {
	b := rawEntries(t, 3, uint64(zipEntryRatioFloor))
	var out zipOutcome
	budget := &zipBudget{entries: 100, bytes: 2*zipEntryRatioFloor + 1}
	if _, err := planZip(readerFor(t, b), int64(len(b)), budget, &out); !errors.Is(err, errZipTooLarge) {
		t.Fatalf("three 1 MiB entries against a 2 MiB budget: err = %v, want errZipTooLarge", err)
	}
}

func TestPlanZip_TotalRatioRefusesManySmallEntries(t *testing.T) {
	// 17 entries, each within its own floor of 1 MiB, in an archive of about 1 KiB:
	// 17 MiB declared is above the 16 MiB floor of the whole archive's ratio.
	b := rawEntries(t, 17, uint64(zipEntryRatioFloor))
	var out zipOutcome
	if _, err := planZip(readerFor(t, b), int64(len(b)), &zipBudget{entries: 100, bytes: zipMaxTotalBytes}, &out); !errors.Is(err, errZipTooLarge) {
		t.Fatalf("err = %v, want errZipTooLarge", err)
	}
	b = rawEntries(t, 15, uint64(zipEntryRatioFloor))
	if _, err := planZip(readerFor(t, b), int64(len(b)), &zipBudget{entries: 100, bytes: zipMaxTotalBytes}, &out); err != nil {
		t.Fatalf("15 MiB declared is under the floor and must plan: %v", err)
	}
}

func TestZipExtraction_TooManyArchivesInOneFolder(t *testing.T) {
	folder, roots := newJobFolder(t)
	one := buildZip(t, zipEnt{name: "a.txt", body: []byte("x")})
	for i := 0; i <= zipMaxArchives; i++ {
		writeZipFile(t, folder, fmt.Sprintf("p%02d.zip", i), one)
	}
	if _, err := extractZipsInFolder(context.Background(), folder, roots); err == nil || !strings.Contains(err.Error(), "ZIP files") {
		t.Fatalf("err = %v, want the archive count refused", err)
	}
	if _, err := os.Lstat(filepath.Join(folder, "a.txt")); err == nil {
		t.Error("something was extracted from an over-count folder")
	}
}

// An entry named like the marker must not become the marker: it would let an
// archive plant content under the artifact name.
func TestZipExtraction_EntryNamedLikeTheMarkerIsNotWritten(t *testing.T) {
	folder, roots := newJobFolder(t)
	writeZipFile(t, folder, "a.zip", buildZip(t,
		zipEnt{name: zipExtractedMarker, body: []byte("forged")},
		zipEnt{name: "b.txt", body: []byte("b")}))
	out, err := extractZipsInFolder(context.Background(), folder, roots)
	if err != nil {
		t.Fatal(err)
	}
	if out.special != 1 {
		t.Errorf("special = %d, want the forged marker counted as skipped", out.special)
	}
	if b, _ := os.ReadFile(filepath.Join(folder, zipExtractedMarker)); string(b) != zipExtractedMarkerTx {
		t.Errorf("marker = %q, want Bindery's own text", b)
	}
}

func TestZipExtraction_DeviceAndPipeEntriesAreSkipped(t *testing.T) {
	folder, roots := newJobFolder(t)
	writeZipFile(t, folder, "a.zip", buildZip(t,
		zipEnt{name: "ok.txt", body: []byte("ok")},
		zipEnt{name: "dev", mode: fs.ModeDevice | fs.ModeCharDevice | 0o666},
		zipEnt{name: "blk", mode: fs.ModeDevice | 0o666},
		zipEnt{name: "fifo", mode: fs.ModeNamedPipe | 0o666},
		zipEnt{name: "sock", mode: fs.ModeSocket | 0o666}))
	out, err := extractZipsInFolder(context.Background(), folder, roots)
	if err != nil {
		t.Fatal(err)
	}
	if out.special != 4 || out.files != 1 {
		t.Errorf("special = %d files = %d, want 4 and 1", out.special, out.files)
	}
	for _, n := range []string{"dev", "blk", "fifo", "sock"} {
		if _, err := os.Lstat(filepath.Join(folder, n)); err == nil {
			t.Errorf("%s was created", n)
		}
	}
}

func TestZipExtraction_NonUTF8NamesAreWrittenAsValidUTF8(t *testing.T) {
	folder, roots := newJobFolder(t)
	writeZipFile(t, folder, "a.zip", buildZip(t, zipEnt{name: "caf\xe9 au lait.txt", body: []byte("x")}))
	if _, err := extractZipsInFolder(context.Background(), folder, roots); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(folder)
	var found bool
	for _, e := range entries {
		if !utf8.ValidString(e.Name()) {
			t.Errorf("a non UTF-8 name %q reached the folder", e.Name())
		}
		found = found || e.Name() == "caf_ au lait.txt"
	}
	if !found {
		t.Errorf("want caf_ au lait.txt, got %v", entries)
	}
}

// A file where a directory is needed is skipped, never replaced and never an
// error that fails the whole archive.
func TestZipExtraction_FileBlocksALaterDirectoryOfTheSameName(t *testing.T) {
	folder, roots := newJobFolder(t)
	writeZipFile(t, folder, "a.zip", buildZip(t,
		zipEnt{name: "f", body: []byte("file")},
		zipEnt{name: "f/x.txt", body: []byte("under it")},
		zipEnt{name: "ok.txt", body: []byte("ok")}))
	out, err := extractZipsInFolder(context.Background(), folder, roots)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(folder, "f")); string(b) != "file" {
		t.Errorf("f = %q", b)
	}
	if out.existing != 1 || out.files != 2 {
		t.Errorf("existing = %d files = %d, want 1 and 2", out.existing, out.files)
	}
}

// A folder the process cannot write to (a read only mount) fails cleanly: an
// error, the zip untouched, nothing left behind.
func TestZipExtraction_ReadOnlyFolderFailsCleanly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	folder, roots := newJobFolder(t)
	writeZipFile(t, folder, "a.zip", buildZip(t, zipEnt{name: "b.txt", body: []byte("b")}))
	if err := os.Chmod(folder, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(folder, 0o755) })
	if _, err := extractZipsInFolder(context.Background(), folder, roots); err == nil {
		t.Fatal("extraction into a read only folder reported success")
	}
	entries, _ := os.ReadDir(folder)
	if len(entries) != 1 || entries[0].Name() != "a.zip" {
		t.Errorf("folder = %v, want only a.zip", entries)
	}
}

// Once the wait runs out, a ZIP is not described as an archive Bindery never
// unpacks: it does, so the message points at why it did not.
func TestImportSourceGoneReason_ZipIsNotCalledUnsupported(t *testing.T) {
	folder, _ := newJobFolder(t)
	writeZipFile(t, folder, "a.zip", []byte("x"))
	got := importSourceGoneReason(folder)
	if !strings.Contains(got, "a.zip, ZIP") || strings.Contains(got, "does not unpack archives") {
		t.Errorf("reason = %q", got)
	}
}

// Extracting twice does not write twice: the retry after a success finds the
// marker and opens nothing.
func TestZipExtraction_RetryAfterSuccessOpensNothing(t *testing.T) {
	folder, roots := newJobFolder(t)
	writeZipFile(t, folder, "a.zip", buildZip(t, zipEnt{name: "b.txt", body: []byte("b")}))
	if _, err := extractZipsInFolder(context.Background(), folder, roots); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(folder, "b.txt")); err != nil {
		t.Fatal(err)
	}
	out, err := extractZipsInFolder(context.Background(), folder, roots)
	if err != nil || !out.done || out.files != 0 {
		t.Fatalf("second run: %+v, %v; want done and nothing written", out, err)
	}
	if _, err := os.Lstat(filepath.Join(folder, "b.txt")); err == nil {
		t.Error("the retry extracted again")
	}
}
