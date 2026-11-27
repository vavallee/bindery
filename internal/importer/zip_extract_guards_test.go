package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Review follow ups for the ZIP extraction (#4131): the folder cannot be
// swapped for a link after it was checked, a root that is too broad is
// refused, a long extraction is bounded and cancellable, and the plan time
// ratio refusal holds end to end.

func TestZipExtraction_FolderSwappedForALinkAfterTheCheckWritesNothing(t *testing.T) {
	f := newZipFixture(t)
	f.put("a.zip", buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}))
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	zipBytes := buildZip(t, zipEnt{name: "evil.txt", body: []byte("evil")})
	if err := os.WriteFile(filepath.Join(outside, "a.zip"), zipBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, ok := resolveStrictlyInside(f.folder, []string{f.root})
	if !ok {
		t.Fatal("setup: the folder should resolve inside the root")
	}
	zipBeforeOpenHook = func(folder string) {
		// What an attacker with write access to the download folder does in the
		// window: move the job folder away and put a link to elsewhere in its place.
		if err := os.Rename(folder, folder+".moved"); err != nil {
			t.Error(err)
			return
		}
		if err := os.Symlink(outside, folder); err != nil {
			t.Skip("symlinks are not available")
		}
	}
	t.Cleanup(func() { zipBeforeOpenHook = nil })

	_, err := extractZipsInFolder(context.Background(), resolved, []string{f.root})
	if err == nil || !strings.Contains(err.Error(), "changed while it was being opened") {
		t.Fatalf("err = %v, want a refusal that names the swap", err)
	}
	if names := f.names(outside); len(names) != 1 {
		t.Errorf("the folder behind the swapped link was written to: %v", names)
	}
}

func TestZipExtraction_TooBroadRootsAreRefused(t *testing.T) {
	f := newZipFixture(t)
	f.put("a.zip", buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}))
	ctx := context.Background()

	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	rf := db.NewRootFolderRepo(database)
	rootFolder := filepath.Join(t.TempDir(), "calibre")
	if err := os.MkdirAll(rootFolder, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := rf.Create(ctx, rootFolder); err != nil {
		t.Fatal(err)
	}
	f.s.WithRootFolders(rf)

	if got := f.s.acceptableZipRoots(ctx, []string{f.root}); len(got) != 1 {
		t.Fatalf("a plain download folder was refused: %v", got)
	}
	for name, root := range map[string]string{
		"filesystem root":           string(filepath.Separator),
		"the user's home":           home,
		"a parent of the library":   filepath.Dir(f.library),
		"the library itself":        f.library,
		"a parent of a root folder": filepath.Dir(rootFolder),
		"a root folder itself":      rootFolder,
	} {
		if got := f.s.acceptableZipRoots(ctx, []string{root}); len(got) != 0 {
			t.Errorf("%s was accepted as a root: %v", name, got)
		}
	}

	// And through the importer: a global remap whose target is "/" must not
	// turn the guard into a no-op for a folder elsewhere.
	other := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(other, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "a.zip"), buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}), 0o644); err != nil {
		t.Fatal(err)
	}
	f.s.remapper = ParseRemap("/client:/")
	f.run(other, "sabnzbd")
	if names := f.names(other); len(names) != 1 {
		t.Errorf("a remap to / made an unrelated folder writable: %v", names)
	}
}

func TestZipExtraction_StoppedByTheDeadlineOrCancellation(t *testing.T) {
	setup := func(t *testing.T) string {
		folder := filepath.Join(t.TempDir(), "job")
		if err := os.MkdirAll(folder, 0o750); err != nil {
			t.Fatal(err)
		}
		body := buildZip(t, zipEnt{name: "a.txt", body: []byte("a")}, zipEnt{name: "d/b.txt", body: []byte("b")})
		if err := os.WriteFile(filepath.Join(folder, "a.zip"), body, 0o644); err != nil {
			t.Fatal(err)
		}
		return folder
	}
	t.Run("deadline", func(t *testing.T) {
		old := zipExtractTimeout
		zipExtractTimeout = time.Nanosecond
		t.Cleanup(func() { zipExtractTimeout = old })
		folder := setup(t)
		time.Sleep(time.Millisecond)
		_, err := extractZipsInFolder(context.Background(), folder, []string{filepath.Dir(folder)})
		if err == nil || !strings.Contains(err.Error(), "took longer") {
			t.Fatalf("err = %v, want the deadline", err)
		}
		if entries, _ := os.ReadDir(folder); len(entries) != 1 {
			t.Errorf("folder after the deadline: %v", entries)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		folder := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := extractZipsInFolder(ctx, folder, []string{filepath.Dir(folder)})
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("err = %v, want a cancellation", err)
		}
		if entries, _ := os.ReadDir(folder); len(entries) != 1 {
			t.Errorf("folder after a cancel: %v", entries)
		}
	})
	t.Run("between entries removes what was written", func(t *testing.T) {
		folder := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Cancel once the first file is on disk, as a shutdown would.
		go func() {
			for ctx.Err() == nil {
				if _, err := os.Stat(filepath.Join(folder, "a.txt")); err == nil {
					cancel()
					return
				}
			}
		}()
		// Slow the walk so the watcher can win: many tiny entries.
		ents := make([]zipEnt, 0, 1500)
		for i := range 1500 {
			ents = append(ents, zipEnt{name: "d/f" + itoa(i) + ".txt", body: []byte("x")})
		}
		ents = append([]zipEnt{{name: "a.txt", body: []byte("a")}}, ents...)
		if err := os.WriteFile(filepath.Join(folder, "a.zip"), buildZip(t, ents...), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := extractZipsInFolder(ctx, folder, []string{filepath.Dir(folder)})
		if err == nil {
			t.Skip("the extraction finished before the cancel landed")
		}
		if entries, _ := os.ReadDir(folder); len(entries) != 1 {
			t.Errorf("folder after a mid extraction cancel: %v", entries)
		}
	})
	t.Run("a copy stops between chunks", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		src := &cancelAfter{r: bytes.NewReader(make([]byte, 1<<20)), after: 64 << 10, cancel: cancel}
		var dst bytes.Buffer
		n, err := copyBounded(ctx, &dst, src, 1<<30)
		if err == nil || n >= 1<<20 {
			t.Fatalf("copied %d bytes, err %v; want a stop part way", n, err)
		}
	})
}

// cancelAfter cancels a context once it has served after bytes.
type cancelAfter struct {
	r      *bytes.Reader
	after  int
	read   int
	cancel context.CancelFunc
}

func (c *cancelAfter) Read(p []byte) (int, error) {
	if len(p) > 4096 {
		p = p[:4096]
	}
	n, err := c.r.Read(p)
	c.read += n
	if c.read >= c.after {
		c.cancel()
	}
	return n, err
}

// The plan time ratio refusal, end to end: declared sizes over the ratio refuse
// the whole archive. The entry compresses 262 to 1 by its header, above the cap
// of 100 and below any loosened one.
func TestZipExtraction_DeclaredRatioRefusesTheWholeArchive(t *testing.T) {
	f := newZipFixture(t)
	junk := make([]byte, 20<<10)
	_, _ = rand.Read(junk)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	good, _ := zw.Create("Author - Title.epub")
	_, _ = good.Write(testEpubBytes(t))
	raw, err := zw.CreateRaw(&zip.FileHeader{Name: "big.bin", Method: zip.Deflate, CRC32: 1,
		CompressedSize64: uint64(len(junk)), UncompressedSize64: 5 << 20})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = raw.Write(junk)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.put("a.zip", buf.Bytes())

	got := f.runUsenet()
	if got.Status == models.StateImported || !strings.Contains(got.ErrorMessage, "size limits") {
		t.Fatalf("status %q, message %q; want the archive refused for its ratio", got.Status, got.ErrorMessage)
	}
	f.assertFolderUntouched("a.zip")
}

// Documented behaviour: every case skips, nothing is overwritten.
func TestZipExtraction_DuplicateAndCaseNames(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "job")
	if err := os.MkdirAll(folder, 0o750); err != nil {
		t.Fatal(err)
	}
	body := buildZip(t,
		zipEnt{name: "dup.txt", body: []byte("first")},
		zipEnt{name: "dup.txt", body: []byte("second")},
		zipEnt{name: "thing/", mode: os.ModeDir | 0o755},
		zipEnt{name: "thing", body: []byte("a file named like the directory")},
		zipEnt{name: "A.txt", body: []byte("upper")},
		zipEnt{name: "a.txt", body: []byte("lower")})
	if err := os.WriteFile(filepath.Join(folder, "a.zip"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := extractZipsInFolder(context.Background(), folder, []string{filepath.Dir(folder)}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(folder, "dup.txt")); string(b) != "first" {
		t.Errorf("duplicate names: dup.txt = %q, want the first copy", b)
	}
	if fi, err := os.Stat(filepath.Join(folder, "thing")); err != nil || !fi.IsDir() {
		t.Errorf("a directory entry then a file of that name must leave the directory: %v", err)
	}
	for name, want := range map[string]string{"A.txt": "upper", "a.txt": "lower"} {
		if b, _ := os.ReadFile(filepath.Join(folder, name)); string(b) != want {
			t.Errorf("%s = %q, want %q", name, b, want)
		}
	}
}
