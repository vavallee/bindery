package importer

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/models"
)

// fakeImage is enough of a PNG for content sniffing; tag must differ between
// fixtures so a test can tell which source a cover came from.
func fakeImage(tag string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte("fixture:"+tag)...)
}

// writeCoverEpub writes an EPUB whose package document declares img as its
// cover, the EPUB 3 way (manifest properties) or the EPUB 2 way (meta name).
func writeCoverEpub(t *testing.T, dst string, epub3 bool, img []byte) {
	t.Helper()
	manifest := `<item id="cover-img" href="images/cover%20art.png" media-type="image/png"/>`
	meta := `<meta name="cover" content="cover-img"/>`
	if epub3 {
		manifest = `<item id="ci" href="images/cover%20art.png" media-type="image/png" properties="cover-image"/>`
		meta = ""
	}
	opf := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Fixture</dc:title>` + meta + `</metadata>
  <manifest>` + manifest + `<item id="t" href="text.xhtml" media-type="application/xhtml+xml"/></manifest>
</package>`
	files := map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`),
		"OEBPS/content.opf":      []byte(opf),
	}
	if img != nil {
		files["OEBPS/images/cover art.png"] = img
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeTaggedMP3 writes an MP3 that is just an ID3v2.3 tag carrying img as
// its front-cover APIC frame, which is all the tag reader looks at.
func writeTaggedMP3(t *testing.T, dst string, img []byte) {
	t.Helper()
	var frame bytes.Buffer
	frame.WriteByte(0) // ISO-8859-1
	frame.WriteString("image/png\x00")
	frame.WriteByte(3) // front cover
	frame.WriteByte(0) // empty description
	frame.Write(img)

	var tag bytes.Buffer
	tag.WriteString("APIC")
	_ = binary.Write(&tag, binary.BigEndian, uint32(frame.Len()))
	tag.Write([]byte{0, 0})
	tag.Write(frame.Bytes())

	size := tag.Len()
	header := []byte{'I', 'D', '3', 3, 0, 0,
		byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
	if err := os.WriteFile(dst, append(header, tag.Bytes()...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadFileCover(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, body []byte) string {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	epub3 := filepath.Join(dir, "three.epub")
	writeCoverEpub(t, epub3, true, fakeImage("epub3"))
	epub2 := filepath.Join(dir, "two.epub")
	writeCoverEpub(t, epub2, false, fakeImage("epub2"))
	noCover := filepath.Join(dir, "none.epub")
	writeCoverEpub(t, noCover, false, nil)
	mp3 := filepath.Join(dir, "track.mp3")
	writeTaggedMP3(t, mp3, fakeImage("mp3"))

	// A multi-file audiobook folder: the folder image beats the tracks' art.
	withImage := filepath.Join(dir, "with-image")
	write("with-image/cover.jpg", fakeImage("folder"))
	writeTaggedMP3(t, filepath.Join(withImage, "01.mp3"), fakeImage("track"))
	// A folder with only tagged tracks uses the first track's art.
	tracksOnly := filepath.Join(dir, "tracks-only")
	write("tracks-only/notes.txt", []byte("x"))
	writeTaggedMP3(t, filepath.Join(tracksOnly, "02.mp3"), fakeImage("second"))
	writeTaggedMP3(t, filepath.Join(tracksOnly, "01.mp3"), fakeImage("first"))
	// A single file whose folder holds a cover.jpg: the image may belong to
	// another book in the same folder, so only the file's own art counts.
	write("shared/cover.jpg", fakeImage("someone-else"))
	untagged := write("shared/book.epub", []byte("not a zip"))

	for name, tc := range map[string]struct {
		path string
		want string
	}{
		"epub 3 cover-image":        {epub3, "epub3"},
		"epub 2 meta cover":         {epub2, "epub2"},
		"epub without cover":        {noCover, ""},
		"mp3 APIC":                  {mp3, "mp3"},
		"folder image wins":         {withImage, "folder"},
		"first track's art":         {tracksOnly, "first"},
		"no folder image for files": {untagged, ""},
		"missing path":              {filepath.Join(dir, "gone.m4b"), ""},
	} {
		got := readFileCover(tc.path)
		switch {
		case tc.want == "" && got != nil:
			t.Errorf("%s: got %q, want no cover", name, got)
		case tc.want != "" && !bytes.Equal(got, fakeImage(tc.want)):
			t.Errorf("%s: got %q, want the %q image", name, got, tc.want)
		}
	}
}

// A cover read from the library is shown to everyone who can see the book,
// so a link is never followed to one (as #2961 does for serving files): not a
// folder's cover.jpg, not a track, not the book file itself.
func TestReadFileCover_DoesNotFollowLinks(t *testing.T) {
	dir := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "private.jpg")
	if err := os.WriteFile(elsewhere, fakeImage("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	tagged := filepath.Join(t.TempDir(), "elsewhere.mp3")
	writeTaggedMP3(t, tagged, fakeImage("elsewhere-track"))
	epub := filepath.Join(t.TempDir(), "elsewhere.epub")
	writeCoverEpub(t, epub, true, fakeImage("elsewhere-epub"))

	folder := filepath.Join(dir, "book")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	link := func(target, name string) string {
		p := filepath.Join(folder, name)
		if err := os.Symlink(target, p); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		return p
	}
	link(elsewhere, "cover.jpg")
	link(tagged, "01.mp3")
	if got := readFileCover(folder); got != nil {
		t.Errorf("folder of links: got %q, want no cover", got)
	}
	if got := readFileCover(link(epub, "book.epub")); got != nil {
		t.Errorf("linked epub: got %q, want no cover", got)
	}
}

// End to end through an ebook import: a book with no cover gets the EPUB's.
func TestTryImportInternal_FillsCoverFromEpub(t *testing.T) {
	t.Parallel()
	libraryDir, downloadPath := t.TempDir(), t.TempDir()
	writeCoverEpub(t, filepath.Join(downloadPath, "Recursion.epub"), true, fakeImage("import"))

	book := &models.Book{ForeignID: "nb:cover-test", Title: "Recursion", SortTitle: "recursion",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook}
	s, dl, bookRepo, _, ctx := languageFixture(t, libraryDir, book)
	store := covers.NewStore(t.TempDir())
	s.WithCoverStore(store)

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", "", nil, nil)

	got, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !covers.IsRef(got.ImageURL) {
		t.Fatalf("image_url = %q, want a stored file cover", got.ImageURL)
	}
	p, _, ok := store.Resolve(got.ImageURL)
	if !ok {
		t.Fatalf("stored cover %q does not resolve", got.ImageURL)
	}
	if body, _ := os.ReadFile(p); !bytes.Equal(body, fakeImage("import")) {
		t.Errorf("stored cover = %q", body)
	}
}

// A provider cover is never replaced by the file's.
func TestTryImportInternal_KeepsProviderCover(t *testing.T) {
	t.Parallel()
	libraryDir, downloadPath := t.TempDir(), t.TempDir()
	writeCoverEpub(t, filepath.Join(downloadPath, "Recursion.epub"), true, fakeImage("import"))

	const provider = "https://covers.example.invalid/b/id/1-L.jpg"
	book := &models.Book{ForeignID: "OL1W", Title: "Recursion", SortTitle: "recursion",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, ImageURL: provider}
	s, dl, bookRepo, _, ctx := languageFixture(t, libraryDir, book)
	s.WithCoverStore(covers.NewStore(t.TempDir()))

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", "", nil, nil)

	got, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ImageURL != provider {
		t.Errorf("image_url = %q, want the provider cover kept", got.ImageURL)
	}
}

// Books imported before covers were read from files get one on the next
// library scan; a book without files is left alone.
func TestBackfillFileCovers(t *testing.T) {
	t.Parallel()
	libraryDir := t.TempDir()
	withFile := &models.Book{ForeignID: "nb:backfill", Title: "Recursion", SortTitle: "recursion",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeAudiobook}
	s, _, bookRepo, _, ctx := languageFixture(t, libraryDir, withFile)
	s.WithCoverStore(covers.NewStore(t.TempDir()))

	track := filepath.Join(libraryDir, "Recursion.mp3")
	writeTaggedMP3(t, track, fakeImage("backfill"))
	if err := bookRepo.AddBookFile(ctx, withFile.ID, models.MediaTypeAudiobook, track); err != nil {
		t.Fatal(err)
	}
	noFile := &models.Book{ForeignID: "nb:nofile", Title: "Other", SortTitle: "other",
		AuthorID: withFile.AuthorID, Status: models.BookStatusWanted}
	if err := bookRepo.Create(ctx, noFile); err != nil {
		t.Fatal(err)
	}

	s.backfillFileCovers(ctx)

	if got, _ := bookRepo.GetByID(ctx, withFile.ID); !strings.HasPrefix(got.ImageURL, covers.Scheme) {
		t.Errorf("book with a file: image_url = %q, want a stored cover", got.ImageURL)
	}
	if got, _ := bookRepo.GetByID(ctx, noFile.ID); got.ImageURL != "" {
		t.Errorf("book without a file: image_url = %q, want none", got.ImageURL)
	}
}

// A file found to have no art is not read again by the next backfill while
// it is unchanged, and is read again once it changes.
func TestBackfillFileCovers_SkipsUnchangedFilesWithoutArt(t *testing.T) {
	t.Parallel()
	libraryDir := t.TempDir()
	book := &models.Book{ForeignID: "nb:noart", Title: "Fjellvinden", SortTitle: "fjellvinden",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeAudiobook}
	s, _, bookRepo, _, ctx := languageFixture(t, libraryDir, book)
	s.WithCoverStore(covers.NewStore(t.TempDir()))
	track := filepath.Join(libraryDir, "Fjellvinden.mp3")
	writeTaggedMP3(t, track, nil)
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, track); err != nil {
		t.Fatal(err)
	}
	reads := 0
	s.readCover = func(p string) []byte { reads++; return readFileCover(p) }

	s.backfillFileCovers(ctx)
	s.backfillFileCovers(ctx)
	if reads != 1 {
		t.Fatalf("reads = %d after two scans of an unchanged file, want 1", reads)
	}
	// The file gains art: its stamp changes, so it is read again.
	writeTaggedMP3(t, track, fakeImage("later"))
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(track, later, later); err != nil {
		t.Fatal(err)
	}
	s.backfillFileCovers(ctx)
	if reads != 2 {
		t.Errorf("reads = %d after the file changed, want 2", reads)
	}
	if got, _ := bookRepo.GetByID(ctx, book.ID); !strings.HasPrefix(got.ImageURL, covers.Scheme) {
		t.Errorf("image_url = %q, want the cover the file gained", got.ImageURL)
	}
}

// A provider cover that lands after the book was read is not overwritten by
// the file's art.
func TestFillCoverFromFile_KeepsACoverThatArrivedMeanwhile(t *testing.T) {
	t.Parallel()
	libraryDir := t.TempDir()
	book := &models.Book{ForeignID: "nb:race", Title: "Fjellvinden", SortTitle: "fjellvinden",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeAudiobook}
	s, _, bookRepo, _, ctx := languageFixture(t, libraryDir, book)
	s.WithCoverStore(covers.NewStore(t.TempDir()))
	track := filepath.Join(libraryDir, "Fjellvinden.mp3")
	writeTaggedMP3(t, track, fakeImage("file"))

	stale := *book // read before the provider cover arrived: no cover
	if err := bookRepo.SetImageURL(ctx, book.ID, "https://covers.example/fjellvinden.jpg"); err != nil {
		t.Fatal(err)
	}
	s.fillCoverFromFile(ctx, &stale, track)
	if got, _ := bookRepo.GetByID(ctx, book.ID); got.ImageURL != "https://covers.example/fjellvinden.jpg" {
		t.Errorf("image_url = %q, want the provider cover kept", got.ImageURL)
	}
	if stale.ImageURL != "" {
		t.Errorf("in-memory book took the file cover %q although it was not written", stale.ImageURL)
	}
}

// Without a store configured the feature is off and nothing is written.
func TestFillCoverFromFile_DisabledWithoutStore(t *testing.T) {
	t.Parallel()
	libraryDir := t.TempDir()
	book := &models.Book{ForeignID: "nb:off", Title: "Recursion", SortTitle: "recursion",
		Status: models.BookStatusWanted}
	s, _, bookRepo, _, ctx := languageFixture(t, libraryDir, book)
	track := filepath.Join(libraryDir, "Recursion.mp3")
	writeTaggedMP3(t, track, fakeImage("off"))

	s.fillCoverFromFile(ctx, book, track)

	if got, _ := bookRepo.GetByID(ctx, book.ID); got.ImageURL != "" {
		t.Errorf("image_url = %q, want none without a cover store", got.ImageURL)
	}
}

// Cover art is read through the same guards as the importer's own tag and
// EPUB reads (#2957): a file that claims a huge picture, or an EPUB whose
// package document is oversized, yields no cover rather than a large
// allocation. An honest picture is still read.
func TestReadFileCover_HostileClaimsAreRejected(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, body []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	art := fakeImage("honest")
	honest := write("honest.flac", flacFile(flacBlock{6, flacPicture(uint32(len(art)), art)}))
	if got := readFileCover(honest); !bytes.Equal(got, art) {
		t.Errorf("honest FLAC picture: got %q", got)
	}
	hostile := write("hostile.flac", flacFile(flacBlock{6, flacPicture(hostilePictureClaim, []byte("tiny"))}))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := readFileCover(hostile)
	runtime.ReadMemStats(&after)
	if got != nil {
		t.Errorf("hostile FLAC picture claim: got %d bytes, want none", len(got))
	}
	// The danger is the allocation, not the result: the library allocates a
	// picture's claimed size before it finds the data missing.
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > audioAllocBudget {
		t.Errorf("hostile FLAC picture claim: allocated %d MiB, budget %d MiB", alloc>>20, audioAllocBudget>>20)
	}

	big := filepath.Join(dir, "big.epub")
	writeCoverEpub(t, big, true, fakeImage("big"))
	// Rewrite the package document past the metadata entry cap.
	zr, err := zip.OpenReader(big)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rc)
		rc.Close()
		if strings.HasSuffix(f.Name, ".opf") {
			body = append([]byte("<!--"+strings.Repeat(" ", maxEpubMetadataEntryBytes)+"-->"), body...)
		}
		w, _ := zw.Create(f.Name)
		_, _ = w.Write(body)
	}
	zr.Close()
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	write("big.epub", buf.Bytes())
	if got := readFileCover(big); got != nil {
		t.Errorf("oversized package document: got a cover, want none")
	}
}
