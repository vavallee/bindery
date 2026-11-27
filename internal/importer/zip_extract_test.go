package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"hash/crc32"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A usenet job that holds only a ZIP is unpacked in place under strict guards
// (#4131). These tests drive the importer the way the SABnzbd and NZBGet
// pollers do, and name each guard they hold.

type zipEnt struct {
	name    string
	body    []byte
	mode    fs.FileMode // 0 means a plain file
	flags   uint16
	deflate bool
}

// buildZip writes an archive with the entries as given, names unchecked.
func buildZip(t *testing.T, ents ...zipEnt) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range ents {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store, Flags: e.flags}
		if e.deflate {
			h.Method = zip.Deflate
		}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testEpubBytes is a valid EPUB for "Title T" by "Author A", padded past the
// size floor with incompressible bytes.
func testEpubBytes(t *testing.T) []byte {
	t.Helper()
	pad := make([]byte, 6<<10)
	_, _ = rand.New(rand.NewSource(7)).Read(pad)
	opf := `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Title T</dc:title><dc:creator>Author A</dc:creator><dc:language>en</dc:language></metadata></package>`
	container := `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`
	return buildZip(t,
		zipEnt{name: "META-INF/container.xml", body: []byte(container)},
		zipEnt{name: "content.opf", body: []byte(opf)},
		zipEnt{name: "pad.bin", body: pad})
}

// zipFixture is a scanner with a configured download folder, and a usenet job
// folder inside it.
type zipFixture struct {
	t        *testing.T
	s        *Scanner
	dl       *models.Download
	ctx      context.Context
	root     string // the configured download folder
	folder   string // the job folder inside it
	library  string
	settings interface {
		Set(context.Context, string, string) error
	}
}

func newZipFixture(t *testing.T) *zipFixture {
	t.Helper()
	library := t.TempDir()
	s, dl, _, _, ctx := dataLossFixture(t, library, "copy")
	root := filepath.Join(t.TempDir(), "usenet")
	folder := filepath.Join(root, "Author.Title.2021.German.Retail.EPUB.eBook-GRP")
	if err := os.MkdirAll(folder, 0o750); err != nil {
		t.Fatal(err)
	}
	s.WithDownloadDir(root)
	return &zipFixture{t: t, s: s, dl: dl, ctx: ctx, root: root, folder: folder, library: library, settings: s.settings}
}

func (f *zipFixture) put(name string, body []byte) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.folder, name), body, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// runUsenet imports the job folder as a SABnzbd download.
func (f *zipFixture) runUsenet() *models.Download { return f.run(f.folder, "sabnzbd") }

func (f *zipFixture) run(folder, clientType string) *models.Download {
	f.t.Helper()
	// The usenet roots are attached whatever the client, so the client type and
	// protocol guards are held on their own, not by a missing root.
	ctx := withZipExtractRoots(f.ctx, f.s.usenetExtractRoots(nil))
	f.s.tryImportInternal(ctx, f.dl, folder, clientType, "remote-id", "", nil, nil)
	got, err := f.s.downloads.GetByID(f.ctx, f.dl.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

func (f *zipFixture) names(dir string) []string {
	f.t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func (f *zipFixture) assertFolderUntouched(want ...string) {
	f.t.Helper()
	got := f.names(f.folder)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		f.t.Errorf("job folder = %v, want %v", got, want)
	}
}

func TestZipExtraction_BookInsideImports(t *testing.T) {
	f := newZipFixture(t)
	f.put("xxxxxxxa.zip", buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}, zipEnt{name: "file_id.diz", body: []byte("diz")}))
	f.put("xx-title.nfo", []byte("nfo"))

	got := f.runUsenet()
	if got.Status != models.StateImported {
		t.Fatalf("status %q (%s), want imported", got.Status, got.ErrorMessage)
	}
	if _, err := os.Stat(filepath.Join(f.folder, "Author - Title.epub")); err != nil {
		t.Errorf("the book was not extracted in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.folder, "xxxxxxxa.zip")); err != nil {
		t.Errorf("the zip must be kept until the normal cleanup removes the folder: %v", err)
	}
	var found string
	_ = filepath.WalkDir(f.library, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".epub") {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Error("no epub reached the library")
	}
	for _, n := range []string{".zip", ".diz", zipExtractedMarker} {
		_ = filepath.WalkDir(f.library, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, n) {
				t.Errorf("%s reached the library", p)
			}
			return nil
		})
	}
}

func TestZipExtraction_ZipSlipAndAbsoluteNamesAreRefused(t *testing.T) {
	for _, name := range []string{"../../evil.txt", "/abs-evil.txt", `..\..\evil.txt`, `C:\evil.txt`, "ok/../../evil.txt", "a/../../../evil.txt"} {
		t.Run(name, func(t *testing.T) {
			f := newZipFixture(t)
			// A clean entry first: refusing the archive must also take it back.
			f.put("a.zip", buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}, zipEnt{name: name, body: []byte("evil")}))

			got := f.runUsenet()
			if got.Status == models.StateImported {
				t.Fatal("a hostile archive was imported")
			}
			if !strings.Contains(got.ErrorMessage, "could not extract") {
				t.Errorf("message %q does not say the extraction was refused", got.ErrorMessage)
			}
			f.assertFolderUntouched("a.zip")
			if parent := f.names(f.root); len(parent) != 1 {
				t.Errorf("something was written beside the job folder: %v", parent)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(f.root), "evil.txt")); err == nil {
				t.Error("an entry escaped to the parent of the download folder")
			}
		})
	}
}

func TestZipExtraction_SymlinkEntryIsSkipped(t *testing.T) {
	f := newZipFixture(t)
	f.put("a.zip", buildZip(t,
		zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)},
		zipEnt{name: "link", body: []byte("/etc/passwd"), mode: fs.ModeSymlink | 0o777}))

	got := f.runUsenet()
	if got.Status != models.StateImported {
		t.Fatalf("status %q (%s)", got.Status, got.ErrorMessage)
	}
	if _, err := os.Lstat(filepath.Join(f.folder, "link")); err == nil {
		t.Error("a symlink entry was created")
	}
}

func TestZipExtraction_BombsAreRefused(t *testing.T) {
	t.Run("high ratio", func(t *testing.T) {
		f := newZipFixture(t)
		f.put("a.zip", buildZip(t, zipEnt{name: "zeros.bin", body: make([]byte, 64<<20), deflate: true}))
		got := f.runUsenet()
		if !strings.Contains(got.ErrorMessage, "size limits") {
			t.Errorf("message %q does not name the size limit", got.ErrorMessage)
		}
		f.assertFolderUntouched("a.zip")
	})
	t.Run("many entries", func(t *testing.T) {
		f := newZipFixture(t)
		ents := make([]zipEnt, 0, zipMaxEntries+1)
		for i := 0; i <= zipMaxEntries; i++ {
			ents = append(ents, zipEnt{name: "d/" + strings.Repeat("x", 3) + string(rune('a'+i%26)) + "-" + itoa(i) + ".txt", body: []byte("x")})
		}
		f.put("a.zip", buildZip(t, ents...))
		got := f.runUsenet()
		if !strings.Contains(got.ErrorMessage, "more than 2000 entries") {
			t.Errorf("message %q does not name the entry cap", got.ErrorMessage)
		}
		f.assertFolderUntouched("a.zip")
	})
}

// The size limit is enforced while copying, so a header that claims a small
// size cannot unpack more.
func TestCopyBounded_StopsAtTheLimitWhateverTheHeaderSays(t *testing.T) {
	var dst bytes.Buffer
	n, err := copyBounded(context.Background(), &dst, bytes.NewReader(make([]byte, 5000)), 1000)
	if err == nil || n > 1001 {
		t.Fatalf("copied %d bytes with err %v, want a stop at the limit", n, err)
	}
	dst.Reset()
	if n, err := copyBounded(context.Background(), &dst, bytes.NewReader(make([]byte, 1000)), 1000); err != nil || n != 1000 {
		t.Fatalf("exactly the limit: %d, %v", n, err)
	}
}

func TestZipEntryCap(t *testing.T) {
	if got := zipEntryCap(100 << 10); got != 100<<10*zipMaxRatio {
		t.Errorf("cap for 100 KiB compressed = %d", got)
	}
	if got := zipEntryCap(10); got != zipEntryRatioFloor {
		t.Errorf("a tiny entry gets the floor, got %d", got)
	}
	if got := zipEntryCap(^uint64(0)); got != zipMaxEntryBytes {
		t.Errorf("a huge claimed size is capped at the per entry cap, got %d", got)
	}
}

func TestZipExtraction_EncryptedEntriesAreSkippedAndSaid(t *testing.T) {
	t.Run("with a book", func(t *testing.T) {
		f := newZipFixture(t)
		f.put("a.zip", buildZip(t,
			zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)},
			zipEnt{name: "secret.epub", body: []byte("ciphertext"), flags: 0x1}))
		got := f.runUsenet()
		if got.Status != models.StateImported {
			t.Fatalf("status %q (%s)", got.Status, got.ErrorMessage)
		}
		if _, err := os.Stat(filepath.Join(f.folder, "secret.epub")); err == nil {
			t.Error("an encrypted entry was written")
		}
	})
	t.Run("only encrypted", func(t *testing.T) {
		f := newZipFixture(t)
		f.put("a.zip", buildZip(t, zipEnt{name: "secret.epub", body: []byte("ciphertext"), flags: 0x1}))
		got := f.runUsenet()
		if got.Status != models.StateImportFailed || !strings.Contains(got.ErrorMessage, "password protected") {
			t.Errorf("status %q, message %q; want importFailed naming the password protected entry", got.Status, got.ErrorMessage)
		}
		f.assertFolderUntouched(zipExtractedMarker, "a.zip")
	})
}

func TestZipExtraction_ZipHoldingOnlyARarWaitsForAnUnpacker(t *testing.T) {
	f := newZipFixture(t)
	f.put("a.zip", buildZip(t, zipEnt{name: "release.rar", body: []byte("Rar!")}, zipEnt{name: "release.nfo", body: []byte("nfo")}))
	got := f.runUsenet()
	if got.Status != models.StateImportFailed || !strings.Contains(got.ErrorMessage, "waiting for release.rar to be extracted") {
		t.Fatalf("status %q, message %q; want waiting for release.rar", got.Status, got.ErrorMessage)
	}
	if !strings.Contains(got.ErrorMessage, "does not unpack archives") || !strings.Contains(got.ErrorMessage, "unpackerr") {
		t.Errorf("message %q does not send the user to an unpacker", got.ErrorMessage)
	}
	if got.ImportRetryCount != 0 {
		t.Errorf("retries spent: %d", got.ImportRetryCount)
	}
	if importSourcePresent(f.folder, nil) {
		t.Error("the folder must keep waiting without spending retries")
	}
}

// One level only: a zip that came out of a zip is written as a file and never
// opened, on this attempt or the next.
func TestZipExtraction_NestedZipIsNotOpened(t *testing.T) {
	f := newZipFixture(t)
	inner := buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)})
	f.put("outer.zip", buildZip(t, zipEnt{name: "inner.zip", body: inner}))
	for range 2 {
		got := f.runUsenet()
		if got.Status == models.StateImported {
			t.Fatal("the zip inside the zip was extracted")
		}
		if _, err := os.Stat(filepath.Join(f.folder, "Author - Title.epub")); err == nil {
			t.Fatal("the epub of the inner zip was extracted")
		}
		if !strings.Contains(got.ErrorMessage, "waiting for") {
			t.Errorf("message %q", got.ErrorMessage)
		}
	}
	f.assertFolderUntouched(zipExtractedMarker, "inner.zip", "outer.zip")
}

func TestZipExtraction_TorrentFolderIsNeverTouched(t *testing.T) {
	f := newZipFixture(t)
	f.put("a.zip", buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}))
	// Each guard on its own, with the usenet roots attached throughout.
	// A torrent or Soulseek client with a protocol that does not say so:
	for _, clientType := range []string{"qbittorrent", "transmission", "deluge", "slskd", ""} {
		f.run(f.folder, clientType)
		f.assertFolderUntouched("a.zip")
	}
	// A torrent or Soulseek protocol on a usenet client type:
	for _, protocol := range []string{"torrent", "soulseek"} {
		f.dl.Protocol = protocol
		f.run(f.folder, "sabnzbd")
		f.assertFolderUntouched("a.zip")
	}
}

func TestZipExtraction_ManualImportIsNeverExtracted(t *testing.T) {
	f := newZipFixture(t)
	f.put("a.zip", buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}))
	ctx := withZipExtractRoots(withManualImport(f.ctx), f.s.usenetExtractRoots(nil))
	f.s.tryImportInternal(ctx, f.dl, f.folder, "sabnzbd", "x", "", nil, nil)
	f.assertFolderUntouched("a.zip")
}

func TestZipExtraction_FolderOutsideTheDownloadDirsIsNotWritten(t *testing.T) {
	t.Run("a path elsewhere", func(t *testing.T) {
		f := newZipFixture(t)
		other := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.MkdirAll(other, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other, "a.zip"), buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}), 0o644); err != nil {
			t.Fatal(err)
		}
		got := f.run(other, "sabnzbd")
		if !strings.Contains(got.ErrorMessage, "outside them") || !strings.Contains(got.ErrorMessage, "waiting for a.zip") {
			t.Errorf("message %q", got.ErrorMessage)
		}
		if names := f.names(other); len(names) != 1 {
			t.Errorf("the folder was written to: %v", names)
		}
	})
	t.Run("a symlink inside the download dir that leads outside", func(t *testing.T) {
		f := newZipFixture(t)
		other := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.MkdirAll(other, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other, "a.zip"), buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(f.root, "linked")
		if err := os.Symlink(other, link); err != nil {
			t.Skip("symlinks are not available")
		}
		f.run(link, "sabnzbd")
		if names := f.names(other); len(names) != 1 {
			t.Errorf("a client path that resolves outside the download folder was written to: %v", names)
		}
	})
	t.Run("the download dir itself", func(t *testing.T) {
		f := newZipFixture(t)
		if err := os.WriteFile(filepath.Join(f.root, "a.zip"), buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}), 0o644); err != nil {
			t.Fatal(err)
		}
		f.run(f.root, "sabnzbd")
		if names := f.names(f.root); len(names) != 2 {
			t.Errorf("the shared download folder was written to: %v", names)
		}
	})
	t.Run("a caller that attached no roots falls back to the configured folders", func(t *testing.T) {
		f := newZipFixture(t)
		f.put("a.zip", buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}))
		f.s.tryImportInternal(f.ctx, f.dl, f.folder, "sabnzbd", "x", "", nil, nil)
		if _, err := os.Stat(filepath.Join(f.folder, "Author - Title.epub")); err != nil {
			t.Errorf("a folder inside BINDERY_DOWNLOAD_DIR was not extracted for a caller without roots: %v", err)
		}
	})
	t.Run("no download folder known says so", func(t *testing.T) {
		f := newZipFixture(t)
		f.s.WithDownloadDir("")
		f.put("a.zip", buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}))
		f.s.tryImportInternal(f.ctx, f.dl, f.folder, "sabnzbd", "x", "", nil, nil)
		got, err := f.s.downloads.GetByID(f.ctx, f.dl.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.ErrorMessage, "it has none it can use") || strings.Contains(got.ErrorMessage, "outside them") {
			t.Errorf("message %q does not say the download folders are unknown", got.ErrorMessage)
		}
		f.assertFolderUntouched("a.zip")
	})
	t.Run("a client remap target counts", func(t *testing.T) {
		f := newZipFixture(t)
		mounted := t.TempDir()
		job := filepath.Join(mounted, "job")
		if err := os.MkdirAll(job, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(job, "a.zip"), buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}), 0o644); err != nil {
			t.Fatal(err)
		}
		client := &models.DownloadClient{Type: "sabnzbd", PathRemap: "/client/complete:" + mounted}
		ctx := withZipExtractRoots(f.ctx, f.s.usenetExtractRoots(client))
		f.s.tryImportInternal(ctx, f.dl, job, "sabnzbd", "x", "", nil, nil)
		if _, err := os.Stat(filepath.Join(job, "Author - Title.epub")); err != nil {
			t.Errorf("a folder inside the client's remap target was not extracted: %v", err)
		}
	})
}

func TestZipExtraction_NeverOverwritesAnExistingFile(t *testing.T) {
	f := newZipFixture(t)
	f.put("readme.nfo", []byte("mine"))
	f.put("a.zip", buildZip(t,
		zipEnt{name: "readme.nfo", body: []byte("theirs")},
		zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}))
	got := f.runUsenet()
	if got.Status != models.StateImported {
		t.Fatalf("status %q (%s)", got.Status, got.ErrorMessage)
	}
	if b, _ := os.ReadFile(filepath.Join(f.folder, "readme.nfo")); string(b) != "mine" {
		t.Errorf("readme.nfo was overwritten: %q", b)
	}
}

func TestZipExtraction_SettingOffLeavesTheZipAndSaysWaiting(t *testing.T) {
	f := newZipFixture(t)
	if err := f.settings.Set(f.ctx, "import.extract_zip", "false"); err != nil {
		t.Fatal(err)
	}
	f.put("a.zip", buildZip(t, zipEnt{name: "Author - Title.epub", body: testEpubBytes(t)}))
	got := f.runUsenet()
	if got.Status != models.StateImportFailed || !strings.Contains(got.ErrorMessage, "waiting for a.zip to be extracted") || !strings.Contains(got.ErrorMessage, "switched off") {
		t.Errorf("status %q, message %q", got.Status, got.ErrorMessage)
	}
	f.assertFolderUntouched("a.zip")
}

func TestZipExtraction_ExtractionStopsAtABookAlreadyThere(t *testing.T) {
	f := newZipFixture(t)
	f.put("Author - Title.epub", testEpubBytes(t))
	f.put("a.zip", buildZip(t, zipEnt{name: "other.txt", body: []byte("x")}))
	f.runUsenet()
	f.assertFolderUntouched("Author - Title.epub", "a.zip")
}

// A failure part way leaves nothing half written.
func TestZipExtraction_FailureLeavesNothingBehind(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("dir/first.txt")
	_, _ = w.Write([]byte("first"))
	raw := []byte("second")
	rw, err := zw.CreateRaw(&zip.FileHeader{Name: "dir/second.txt", Method: zip.Store, CRC32: crc32.ChecksumIEEE(raw) + 1,
		CompressedSize64: uint64(len(raw)), UncompressedSize64: uint64(len(raw))})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = rw.Write(raw)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "a.zip"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := extractZipsInFolder(context.Background(), folder, []string{filepath.Dir(folder)}); err == nil {
		t.Fatal("a corrupt entry did not fail the extraction")
	}
	entries, _ := os.ReadDir(folder)
	if len(entries) != 1 || entries[0].Name() != "a.zip" {
		t.Errorf("folder after a failed extraction: %v", entries)
	}
}

func TestZipExtraction_PermissionsAreFixed(t *testing.T) {
	folder := t.TempDir()
	body := buildZip(t, zipEnt{name: "sub/run.sh", body: []byte("x"), mode: 0o4777})
	if err := os.WriteFile(filepath.Join(folder, "a.zip"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := extractZipsInFolder(context.Background(), folder, []string{filepath.Dir(folder)}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(folder, "sub", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&^0o644 != 0 || fi.Mode()&(fs.ModeSetuid|fs.ModeSetgid) != 0 {
		t.Errorf("extracted file mode %v, want at most 0644 and no setuid", fi.Mode())
	}
	if di, _ := os.Stat(filepath.Join(folder, "sub")); di.Mode().Perm()&^0o755 != 0 {
		t.Errorf("extracted dir mode %v, want at most 0755", di.Mode())
	}
}

func TestCleanZipEntryName(t *testing.T) {
	for name, ok := range map[string]bool{
		"book.epub": true, "dir/book.epub": true, "dir/": true, `dir\book.epub`: true, "./book.epub": true,
		"../x": false, "a/../../x": false, `..\x`: false, "/x": false, `\x`: false, "C:/x": false, `c:\x`: false,
		"a\x00b": false, "": false,
	} {
		_, err := cleanZipEntryName(name)
		if (err == nil) != ok {
			t.Errorf("cleanZipEntryName(%q) err = %v, want ok = %v", name, err, ok)
		}
	}
}
