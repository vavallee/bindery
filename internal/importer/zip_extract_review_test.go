package importer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Resource limits and behaviour found by the security review of #4134.

// centralRecord is one minimal central directory record naming "a".
func centralRecord() []byte {
	r := make([]byte, 46+1)
	binary.LittleEndian.PutUint32(r[0:], 0x02014b50)
	binary.LittleEndian.PutUint16(r[28:], 1) // name length
	r[46] = 'a'
	return r
}

func eocd(entries uint16, dirSize, dirOffset uint32) []byte {
	e := make([]byte, 22)
	binary.LittleEndian.PutUint32(e[0:], 0x06054b50)
	binary.LittleEndian.PutUint16(e[8:], entries)
	binary.LittleEndian.PutUint16(e[10:], entries)
	binary.LittleEndian.PutUint32(e[12:], dirSize)
	binary.LittleEndian.PutUint32(e[16:], dirOffset)
	return e
}

// A directory of 70000 records is 3.3 MB on disk and would be parsed into
// memory whole by archive/zip, whose own count check compares modulo 65536. The
// bound is on the directory's size and is checked before the reader runs.
func TestZipExtraction_OversizedCentralDirectoryIsRefusedBeforeParsing(t *testing.T) {
	folder, roots := newJobFolder(t)
	const n = 70000
	rec := centralRecord()
	body := bytes.Repeat(rec, n)
	body = append(body, eocd(uint16(n%65536), uint32(len(rec)*n), 0)...)
	writeZipFile(t, folder, "a.zip", body)
	_, err := extractZipsInFolder(context.Background(), folder, roots)
	if !errors.Is(err, errZipDirectory) {
		t.Fatalf("err = %v, want errZipDirectory", err)
	}
}

func zip64Tail(entries, dirSize uint64) []byte {
	z := make([]byte, 56)
	binary.LittleEndian.PutUint32(z[0:], 0x06064b50)
	binary.LittleEndian.PutUint64(z[32:], entries)
	binary.LittleEndian.PutUint64(z[40:], dirSize)
	loc := make([]byte, 20)
	binary.LittleEndian.PutUint32(loc[0:], 0x07064b50)
	binary.LittleEndian.PutUint64(loc[8:], 0) // the zip64 record is first in the file
	e := eocd(0xFFFF, 0xFFFFFFFF, 0xFFFFFFFF)
	return append(append(z, loc...), e...)
}

func TestCheckZipDirectory_Zip64(t *testing.T) {
	big := zip64Tail(2_000_000, 90<<20)
	if err := checkZipDirectory(bytes.NewReader(big), int64(len(big))); !errors.Is(err, errZipDirectory) {
		t.Errorf("2M entries: err = %v", err)
	}
	ok := zip64Tail(10, 1000)
	if err := checkZipDirectory(bytes.NewReader(ok), int64(len(ok))); err != nil {
		t.Errorf("10 entries: err = %v", err)
	}
	// ZIP64 markers with no locator are a lie, not a pass.
	bad := eocd(0xFFFF, 0xFFFFFFFF, 0)
	if err := checkZipDirectory(bytes.NewReader(bad), int64(len(bad))); !errors.Is(err, errZipDirectory) {
		t.Errorf("no locator: err = %v", err)
	}
}

func TestZipExtraction_NameAndPathShapeLimits(t *testing.T) {
	cases := map[string]string{
		"name too long":       strings.Repeat("n", zipMaxNameBytes+1) + ".txt",
		"path too deep":       strings.Repeat("d/", zipMaxDepth) + "f.txt",
		"many tiny segments":  strings.Repeat("d/", 30000) + "f.txt",
		"deep folder entry":   strings.Repeat("d/", zipMaxDepth+1),
		"backslash deep path": strings.Repeat(`d\`, zipMaxDepth) + "f.txt",
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			folder, roots := newJobFolder(t)
			writeZipFile(t, folder, "a.zip", buildZip(t, zipEnt{name: "ok.txt", body: []byte("ok")}, zipEnt{name: entry, body: bodyFor(entry)}))
			if _, err := extractZipsInFolder(context.Background(), folder, roots); !errors.Is(err, errZipEntryName) {
				t.Fatalf("err = %v, want the archive refused for its entry name", err)
			}
			if _, err := os.Lstat(filepath.Join(folder, "ok.txt")); err == nil {
				t.Error("the clean entry was written although the archive was refused")
			}
		})
	}
	// The limits are inclusive: 32 levels pass.
	folder, roots := newJobFolder(t)
	deepest := strings.Repeat("d/", zipMaxDepth-1) + "f.txt"
	writeZipFile(t, folder, "a.zip", buildZip(t, zipEnt{name: deepest, body: []byte("x")}))
	if _, err := extractZipsInFolder(context.Background(), folder, roots); err != nil {
		t.Fatalf("a path of exactly %d levels: %v", zipMaxDepth, err)
	}
}

func TestZipExtraction_TooManyFoldersRefusesTheArchive(t *testing.T) {
	folder, roots := newJobFolder(t)
	var ents []zipEnt
	// 800 files, each under its own three level path: 3200 folders.
	for i := range 800 {
		ents = append(ents, zipEnt{name: fmt.Sprintf("d%d/a/b/f.txt", i), body: []byte("x")})
	}
	writeZipFile(t, folder, "a.zip", buildZip(t, ents...))
	if _, err := extractZipsInFolder(context.Background(), folder, roots); !errors.Is(err, errZipShape) {
		t.Fatalf("err = %v, want errZipShape", err)
	}
	if entries, _ := os.ReadDir(folder); len(entries) != 1 {
		t.Errorf("folder = %v, want only a.zip", entries)
	}
}

// bodyFor gives a folder entry no body, as the zip writer insists.
func bodyFor(name string) []byte {
	if strings.HasSuffix(name, "/") {
		return nil
	}
	return []byte("x")
}

func TestMkdirTracked_StopsWhenTheContextIsDone(t *testing.T) {
	folder, _ := newJobFolder(t)
	root, err := os.OpenRoot(folder)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var created zipCreated
	if err := mkdirTracked(ctx, root, "a/b/c", &created); err == nil {
		t.Fatal("mkdirTracked ignored a cancelled context")
	}
	if len(created.dirs) != 0 {
		t.Errorf("created %v after cancellation", created.dirs)
	}
}

func TestCleanZipEntryName_ControlCharactersAreSanitised(t *testing.T) {
	for in, want := range map[string]string{
		"a\x1b[2Jb.txt": "a_[2Jb.txt",
		"x\ty\x7f.txt":  "x_y_.txt",
		"d\n/f.txt":     "d_/f.txt",
	} {
		got, err := cleanZipEntryName(in)
		if err != nil || got != want {
			t.Errorf("cleanZipEntryName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

// A zip next to a release notes file that is too small to be a book still
// extracts: only a real book file keeps it from running.
func TestZipExtraction_TinyNotesFileDoesNotBlockExtraction(t *testing.T) {
	f := newZipFixture(t)
	f.put("a.zip", buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}))
	f.put("readme.txt", []byte("read me"))
	got := f.runUsenet()
	if got.Status != models.StateImported {
		t.Fatalf("status %q (%s), want imported", got.Status, got.ErrorMessage)
	}
}

// A zip with only junk in it is extracted, found to hold no book, rolled back,
// and the message says what happened rather than asking for an unpacker.
func TestZipExtraction_ZipHoldingOnlyJunkIsRolledBack(t *testing.T) {
	f := newZipFixture(t)
	f.put("a.zip", buildZip(t, zipEnt{name: "setup.exe", body: []byte("MZ")}, zipEnt{name: "d/readme.nfo", body: []byte("n")}))
	got := f.runUsenet()
	if got.Status == models.StateImported {
		t.Fatal("imported a zip of junk")
	}
	if !strings.Contains(got.ErrorMessage, "found nothing in it that looks like a book") || strings.Contains(got.ErrorMessage, "unpackerr") {
		t.Errorf("message %q", got.ErrorMessage)
	}
	f.assertFolderUntouched(zipExtractedMarker, "a.zip")
	// A retry opens nothing and does not write the junk again.
	f.runUsenet()
	f.assertFolderUntouched(zipExtractedMarker, "a.zip")
}

func moveModeJob(t *testing.T, files map[string][]byte) (*Scanner, *models.Download, context.Context, string) {
	t.Helper()
	completeDir, jobDir := usenetJob(t, files)
	s, dl, _, _, ctx := dataLossFixture(t, t.TempDir(), "move")
	s.WithDownloadDir(completeDir)
	ctx = withZipExtractRoots(withClientDownloadRoots(ctx, clientRoots(completeDir)), s.usenetExtractRoots(nil))
	return s, dl, ctx, jobDir
}

// Move mode removes a fully imported job folder, the extracted zip and marker
// with it, as it did before the zip was unpacked here.
func TestZipExtraction_MoveModeSweepsTheJobFolder(t *testing.T) {
	s, dl, ctx, jobDir := moveModeJob(t, map[string][]byte{
		"a.zip": buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}, zipEnt{name: "file_id.diz", body: []byte("diz")}),
		"x.nfo": []byte("nfo"),
	})
	s.tryImportInternal(ctx, dl, jobDir, "sabnzbd", "nzo-1", "", nil, nil)
	assertImported(t, ctx, s, dl)
	assertGone(t, jobDir, "the job folder with its zip and marker")
}

// A folder with something the sweep does not know (an .exe from the zip) is
// still kept, zip or not.
func TestZipExtraction_MoveModeKeepsAFolderWithUnknownFiles(t *testing.T) {
	s, dl, ctx, jobDir := moveModeJob(t, map[string][]byte{
		"a.zip": buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}, zipEnt{name: "setup.exe", body: []byte("MZ")}),
	})
	s.tryImportInternal(ctx, dl, jobDir, "sabnzbd", "nzo-1", "", nil, nil)
	assertImported(t, ctx, s, dl)
	assertKept(t, filepath.Join(jobDir, "setup.exe"), "the unknown file")
}
