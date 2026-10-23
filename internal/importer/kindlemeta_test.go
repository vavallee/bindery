package importer

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/models"
)

type exthRecord struct {
	typ uint32
	val []byte
}

// mobiFile describes a minimal Kindle file for buildMobi. Real Kindle books
// can't live in the repository, so the tests build the bytes from the layout
// documented on ReadKindleMetadata.
type mobiFile struct {
	dbName   string // Palm database name, the last-resort title
	fullName []byte // MOBI full name, raw bytes in the file's encoding
	encoding uint32 // 65001 (UTF-8) or 1252
	exth     []exthRecord
	noFlag   bool // EXTH present but the header flag not set
	// fullNameAfter puts this many zero bytes between the EXTH block and the
	// full name, to place the name past the record 0 cap.
	fullNameAfter int
	// images are image records after the text record; the MOBI header's
	// first image index points at the first of them unless noFirstImage.
	images       [][]byte
	noFirstImage bool
}

func buildMobi(m mobiFile) []byte {
	const mobiHeaderLen = 232
	var exth []byte
	if len(m.exth) > 0 {
		var recs []byte
		for _, r := range m.exth {
			recs = binary.BigEndian.AppendUint32(recs, r.typ)
			recs = binary.BigEndian.AppendUint32(recs, uint32(8+len(r.val)))
			recs = append(recs, r.val...)
		}
		exth = append([]byte("EXTH"), binary.BigEndian.AppendUint32(nil, uint32(12+len(recs)))...)
		exth = binary.BigEndian.AppendUint32(exth, uint32(len(m.exth)))
		exth = append(exth, recs...)
	}
	rec0 := make([]byte, 16+mobiHeaderLen)
	copy(rec0[16:20], "MOBI")
	binary.BigEndian.PutUint32(rec0[20:24], mobiHeaderLen)
	binary.BigEndian.PutUint32(rec0[24:28], 2)
	binary.BigEndian.PutUint32(rec0[28:32], m.encoding)
	if len(exth) > 0 && !m.noFlag {
		binary.BigEndian.PutUint32(rec0[128:132], 0x40)
	}
	firstImage := uint32(noKindleIndex)
	if len(m.images) > 0 && !m.noFirstImage {
		firstImage = 2 // after record 0 and the text record
	}
	binary.BigEndian.PutUint32(rec0[108:112], firstImage)
	rec0 = append(rec0, exth...)
	rec0 = append(rec0, make([]byte, m.fullNameAfter)...)
	if len(m.fullName) > 0 {
		binary.BigEndian.PutUint32(rec0[84:88], uint32(len(rec0)))
		binary.BigEndian.PutUint32(rec0[88:92], uint32(len(m.fullName)))
		rec0 = append(rec0, m.fullName...)
	}
	rec0 = append(rec0, 0, 0)

	records := append([][]byte{rec0, []byte("text record")}, m.images...)
	header := make([]byte, 78)
	copy(header[0:32], m.dbName)
	copy(header[60:68], "BOOKMOBI")
	binary.BigEndian.PutUint16(header[76:78], uint16(len(records)))
	// The record list, then two bytes of padding: record 0 starts at 96 for
	// the two record files the malformed file tests mutate by offset.
	at := 78 + 8*len(records) + 2
	var list []byte
	for i, rec := range records {
		list = binary.BigEndian.AppendUint32(list, uint32(at))
		list = binary.BigEndian.AppendUint32(list, uint32(i))
		at += len(rec)
	}
	out := append(header, list...)
	out = append(out, 0, 0)
	for _, rec := range records {
		out = append(out, rec...)
	}
	return out
}

func writeMobi(t *testing.T, name string, m mobiFile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, buildMobi(m), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadKindleMetadata(t *testing.T) {
	path := writeMobi(t, "fjellvinden.azw3", mobiFile{
		dbName:   "Fjellvinden_db",
		fullName: []byte("Fjellvinden (full name)"),
		encoding: 65001,
		exth: []exthRecord{
			{exthAuthor, []byte("Kari Nordmann")},
			{exthAuthor, []byte("Ola Nordmann")},
			{exthISBN, []byte("978-82-05-12345-6")},
			{exthTitle, []byte("Fjellvinden")},
			{exthLanguage, []byte("nb-NO")},
			{exthLanguage, []byte("en")},
			{exthLanguage, []byte("nb")},
			{999, []byte("an EXTH type the reader ignores")},
			{exthAuthor, nil}, // an empty value is skipped
		},
	})
	meta, err := ReadEbookMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Fjellvinden" {
		t.Errorf("title %q: EXTH 503 should win over the full name", meta.Title)
	}
	if meta.Author != "Kari Nordmann" {
		t.Errorf("author %q: the first EXTH 100 should win", meta.Author)
	}
	if meta.ISBN != "9788205123456" {
		t.Errorf("isbn %q, want the normalised ISBN-13", meta.ISBN)
	}
	if meta.Language != "nor" || !slices.Equal(meta.Languages, []string{"nor", "eng"}) {
		t.Errorf("languages %q %v, want nor first and each language once", meta.Language, meta.Languages)
	}
}

// Older files are Windows-1252; ø is 0xF8 there and must come back as UTF-8.
func TestReadKindleMetadataWindows1252(t *testing.T) {
	path := writeMobi(t, "fjellvinden.mobi", mobiFile{
		encoding: 1252,
		exth:     []exthRecord{{exthAuthor, []byte("Kari Bj\xf8rnstad")}, {exthTitle, []byte("S\xf8lvbekken")}},
	})
	meta, err := ReadKindleMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Sølvbekken" || meta.Author != "Kari Bjørnstad" {
		t.Errorf("got %q by %q", meta.Title, meta.Author)
	}
}

func TestReadKindleMetadataTitleFallbacks(t *testing.T) {
	// No EXTH at all: the MOBI full name.
	full := writeMobi(t, "a.mobi", mobiFile{dbName: "db_name", fullName: []byte("Havgapet"), encoding: 65001})
	if meta, err := ReadKindleMetadata(full); err != nil || meta.Title != "Havgapet" {
		t.Errorf("full name fallback: %q, %v", meta.Title, err)
	}
	// No EXTH and no full name: the Palm database name.
	db := writeMobi(t, "b.azw", mobiFile{dbName: "Nordlysnatt", encoding: 65001})
	if meta, err := ReadKindleMetadata(db); err != nil || meta.Title != "Nordlysnatt" {
		t.Errorf("database name fallback: %q, %v", meta.Title, err)
	}
	// An EXTH block the header doesn't flag is not read.
	unflagged := writeMobi(t, "c.mobi", mobiFile{fullName: []byte("Steinbrua"), encoding: 65001, noFlag: true,
		exth: []exthRecord{{exthTitle, []byte("Ignored")}, {exthAuthor, []byte("Ignored")}}})
	if meta, err := ReadKindleMetadata(unflagged); err != nil || meta.Title != "Steinbrua" || meta.Author != "" {
		t.Errorf("unflagged EXTH: %+v, %v", meta, err)
	}
}

func TestReadKindleMetadataISBN10Fallback(t *testing.T) {
	path := writeMobi(t, "a.mobi", mobiFile{encoding: 65001, exth: []exthRecord{{exthISBN, []byte("82-05-12345-X")}, {exthISBN, []byte("not an isbn")}}})
	meta, err := ReadKindleMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ISBN != "820512345X" {
		t.Errorf("isbn %q, want the ISBN-10 when no ISBN-13 is given", meta.ISBN)
	}
}

// A downloaded file is untrusted: anything malformed is an error or an empty
// field, never a panic or a huge allocation.
func TestReadKindleMetadataRejectsMalformedFiles(t *testing.T) {
	good := buildMobi(mobiFile{encoding: 65001, exth: []exthRecord{{exthTitle, []byte("Fjellvinden")}}})
	write := func(t *testing.T, b []byte) string {
		p := filepath.Join(t.TempDir(), "x.mobi")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mutate := func(f func(b []byte)) []byte {
		b := append([]byte(nil), good...)
		f(b)
		return b
	}
	for name, b := range map[string][]byte{
		"too short":            []byte("BOOKMOBI"),
		"not a MOBI":           mutate(func(b []byte) { copy(b[60:68], "TEXtREAd") }),
		"no records":           mutate(func(b []byte) { binary.BigEndian.PutUint16(b[76:78], 0) }),
		"record 0 past EOF":    mutate(func(b []byte) { binary.BigEndian.PutUint32(b[78:82], 1<<30) }),
		"no MOBI header":       mutate(func(b []byte) { copy(b[96+16:96+20], "XXXX") }),
		"EXTH length past end": mutate(func(b []byte) { binary.BigEndian.PutUint32(b[96+248+12+4:], 1<<20) }),
	} {
		t.Run(name, func(t *testing.T) {
			meta, err := ReadKindleMetadata(write(t, b))
			if name == "EXTH length past end" {
				if err != nil || meta.Title != "" {
					t.Errorf("a broken EXTH record should stop the walk, not fail: %+v, %v", meta, err)
				}
				return
			}
			if err == nil {
				t.Errorf("got %+v, want an error", meta)
			}
		})
	}
	if _, err := ReadKindleMetadata(filepath.Join(t.TempDir(), "missing.mobi")); err == nil {
		t.Error("a missing file gave no error")
	}
}

func TestHasEmbeddedMetadata(t *testing.T) {
	for path, want := range map[string]bool{
		"a.epub": true, "a.MOBI": true, "a.azw": true, "a.azw3": true, "a.pdf": false, "a.mp3": false, "a.kfx": false,
	} {
		if got := HasEmbeddedMetadata(path); got != want {
			t.Errorf("HasEmbeddedMetadata(%q) = %v, want %v", path, got, want)
		}
	}
}

// The bulk import and the could-not-match message read Kindle files too.
func TestKindleFilesReachBulkLookupAndUnmatchedReason(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "Kari Nordmann", "Fjellvinden")
	if err := os.MkdirAll(book, 0o755); err != nil {
		t.Fatal(err)
	}
	azw3 := filepath.Join(book, "fjellvinden.azw3")
	if err := os.WriteFile(azw3, buildMobi(mobiFile{encoding: 65001, exth: []exthRecord{
		{exthTitle, []byte("Fjellvinden")}, {exthAuthor, []byte("Kari Nordmann")}, {exthISBN, []byte("9788205123456")},
	}}), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := firstEpubIn(book); got != azw3 {
		t.Errorf("firstEpubIn(folder) = %q, want the AZW3 inside it", got)
	}
	reason := unmatchedReason([]string{azw3})
	for _, want := range []string{`embedded Kindle file: title="Fjellvinden" author="Kari Nordmann"`, "isbn=9788205123456"} {
		if !strings.Contains(reason, want) {
			t.Errorf("unmatched reason %q does not contain %q", reason, want)
		}
	}
}

// Offsets and lengths are uint32s from the file. On a 32-bit build a value of
// 0x80000000 or more converted straight to int is negative, slipped past the
// bounds checks and panicked on the slice; run with GOARCH=386 to see it.
func TestReadKindleMetadataHugeLengthsDoNotWrap(t *testing.T) {
	good := buildMobi(mobiFile{dbName: "Fjellvinden", fullName: []byte("Fjellvinden"), encoding: 65001,
		exth: []exthRecord{{exthAuthor, []byte("Kari Nordmann")}}})
	const rec0 = 96
	for name, at := range map[string]int{
		"MOBI header length": rec0 + 20,
		"full name offset":   rec0 + 84,
		"full name length":   rec0 + 88,
		"EXTH record count":  rec0 + 248 + 8,
		"EXTH record length": rec0 + 248 + 12 + 4,
	} {
		for _, v := range []uint32{0x80000000, 0xFFFFFFF8} {
			b := append([]byte(nil), good...)
			binary.BigEndian.PutUint32(b[at:], v)
			if _, err := parseMobiRecord0(b[rec0:]); err != nil {
				t.Errorf("%s = %#x: %v", name, v, err)
			}
		}
	}
}

// A record whose length is under its own 8 byte header is corrupt: the walk
// stops there rather than slicing backwards or spinning in place.
func TestReadKindleMetadataShortEXTHRecordStopsTheWalk(t *testing.T) {
	for _, length := range []uint32{0, 4, 7} {
		b := buildMobi(mobiFile{encoding: 65001, exth: []exthRecord{{exthAuthor, []byte("Kari Nordmann")}, {exthTitle, []byte("Fjellvinden")}}})
		at := 96 + 248 + 12
		binary.BigEndian.PutUint32(b[at+4:], length)
		binary.BigEndian.PutUint32(b[96+248+8:], 0xFFFFFFFF) // a huge count must not matter
		meta, err := parseMobiRecord0(b[96:])
		if err != nil || meta.Author != "" || meta.Title != "" {
			t.Errorf("length %d: %+v, %v; want the walk to stop", length, meta, err)
		}
	}
}

// Only the first MiB of record 0 is read: a full name beyond it is not.
func TestReadKindleMetadataCapsRecord0(t *testing.T) {
	path := writeMobi(t, "a.mobi", mobiFile{dbName: "Nordlysnatt", fullName: []byte("Havgapet"), encoding: 65001, fullNameAfter: maxKindleRecord0})
	meta, err := ReadKindleMetadata(path)
	if err != nil || meta.Title != "Nordlysnatt" {
		t.Errorf("title %q, %v; want the database name, the full name being past the cap", meta.Title, err)
	}
}

// A full name that runs past the record is ignored, not sliced.
func TestReadKindleMetadataFullNamePastTheRecord(t *testing.T) {
	b := buildMobi(mobiFile{dbName: "Nordlysnatt", fullName: []byte("Havgapet"), encoding: 65001})
	binary.BigEndian.PutUint32(b[96+88:], 1000)
	meta, err := parseMobiRecord0(b[96:])
	if err != nil || meta.Title != "" {
		t.Errorf("title %q, %v; want none", meta.Title, err)
	}
}

// An ISBN-13 wins over an ISBN-10 whichever comes first.
func TestReadKindleMetadataPrefersISBN13(t *testing.T) {
	path := writeMobi(t, "a.mobi", mobiFile{encoding: 65001, exth: []exthRecord{
		{exthISBN, []byte("82-05-12345-X")}, {exthISBN, []byte("978-82-05-12345-6")}, {exthISBN, []byte("978-82-05-99999-9")},
	}})
	meta, err := ReadKindleMetadata(path)
	if err != nil || meta.ISBN != "9788205123456" {
		t.Errorf("isbn %q, %v; want the first ISBN-13", meta.ISBN, err)
	}
}

// Encoding 65001 is UTF-8: ø as its two UTF-8 bytes must not come back as
// the two Windows-1252 characters those bytes are there.
func TestReadKindleMetadataUTF8(t *testing.T) {
	path := writeMobi(t, "a.mobi", mobiFile{encoding: 65001, exth: []exthRecord{{exthTitle, []byte("Sølvbekken")}, {exthAuthor, []byte("Kari Bjørnstad\xff")}}})
	meta, err := ReadKindleMetadata(path)
	if err != nil || meta.Title != "Sølvbekken" || meta.Author != "Kari Bjørnstad" {
		t.Errorf("got %q by %q, %v", meta.Title, meta.Author, err)
	}
}

func coverOffset(n uint32) exthRecord {
	return exthRecord{exthCoverOffset, binary.BigEndian.AppendUint32(nil, n)}
}

// A Kindle file's cover is the image record EXTH 201 points at, counted from
// the first image index in the MOBI header.
func TestKindleCover(t *testing.T) {
	logo, cover, mapImg := fakeImage("logo"), fakeImage("kindle-cover"), fakeImage("map")
	for name, tc := range map[string]struct {
		m    mobiFile
		want []byte
	}{
		"EXTH 201 picks the cover":    {mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(1)}, images: [][]byte{logo, cover, mapImg}}, cover},
		"offset 0 is the first image": {mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(0)}, images: [][]byte{cover}}, cover},
		"the first EXTH 201 wins":     {mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(1), coverOffset(0)}, images: [][]byte{logo, cover}}, cover},
		// No EXTH 201: the first image is often a map or a logo, so none.
		"no EXTH 201":                {mobiFile{encoding: 65001, exth: []exthRecord{{exthTitle, []byte("Fjellvinden")}}, images: [][]byte{mapImg}}, nil},
		"EXTH 201 says none":         {mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(noKindleIndex)}, images: [][]byte{cover}}, nil},
		"no first image index":       {mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(0)}, images: [][]byte{cover}, noFirstImage: true}, nil},
		"offset past the records":    {mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(5)}, images: [][]byte{cover}}, nil},
		"EXTH 201 of the wrong size": {mobiFile{encoding: 65001, exth: []exthRecord{{exthCoverOffset, []byte{0, 1}}}, images: [][]byte{logo, cover}}, nil},
		"unflagged EXTH":             {mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(0)}, images: [][]byte{cover}, noFlag: true}, nil},
		"no images at all":           {mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(0)}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			got := readFileCover(writeMobi(t, "fjellvinden.azw3", tc.m))
			if !bytes.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A downloaded file is untrusted: a cover record past the size cap, a record
// list pointing outside the file, a file that isn't a MOBI, or one reached
// through a link gives no cover, and nothing allocates what the file claims.
func TestKindleCoverRejectsHostileFiles(t *testing.T) {
	write := func(t *testing.T, b []byte) string {
		p := filepath.Join(t.TempDir(), "x.mobi")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := buildMobi(mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(0)}, images: [][]byte{fakeImage("kindle-cover")}})
	if got := kindleCover(write(t, good)); !bytes.Equal(got, fakeImage("kindle-cover")) {
		t.Fatalf("the good file: got %q", got)
	}
	// Record 2 is the cover; its list entry is at 78+8*2.
	mutate := func(f func(b []byte)) []byte {
		b := append([]byte(nil), good...)
		f(b)
		return b
	}
	for name, b := range map[string][]byte{
		"cover record past the end": mutate(func(b []byte) { binary.BigEndian.PutUint32(b[78+16:], 1<<30) }),
		"cover record at offset 0":  mutate(func(b []byte) { binary.BigEndian.PutUint32(b[78+16:], 0) }),
		"first image index huge":    mutate(func(b []byte) { binary.BigEndian.PutUint32(b[binary.BigEndian.Uint32(b[78:82])+108:], 0xFFFFFFF0) }),
		"not a MOBI":                mutate(func(b []byte) { copy(b[60:68], "TEXtREAd") }),
		"too short":                 []byte("BOOKMOBI"),
	} {
		t.Run(name, func(t *testing.T) {
			if got := kindleCover(write(t, b)); got != nil {
				t.Errorf("got %q, want no cover", got)
			}
		})
	}
	t.Run("cover record over the size cap", func(t *testing.T) {
		big := append(fakeImage("big"), make([]byte, covers.MaxBytes)...)
		p := writeMobi(t, "big.mobi", mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(0)}, images: [][]byte{big}})
		if got := kindleCover(p); got != nil {
			t.Errorf("got %d bytes, want no cover over %d", len(got), covers.MaxBytes)
		}
	})
	t.Run("a link to a Kindle file", func(t *testing.T) {
		target := write(t, good)
		link := filepath.Join(t.TempDir(), "linked.azw3")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got := readFileCover(link); got != nil {
			t.Errorf("a linked Kindle file gave a cover: %q", got)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if got := kindleCover(filepath.Join(t.TempDir(), "gone.mobi")); got != nil {
			t.Errorf("got %q", got)
		}
	})
}

// End to end through an ebook import: a book with no cover gets the AZW3's.
func TestTryImportInternal_FillsCoverFromKindleFile(t *testing.T) {
	t.Parallel()
	libraryDir, downloadPath := t.TempDir(), t.TempDir()
	img := fakeImage("kindle-import")
	body := buildMobi(mobiFile{dbName: "Fjellvinden", encoding: 65001,
		exth:   []exthRecord{{exthTitle, []byte("Fjellvinden")}, {exthAuthor, []byte("Kari Nordmann")}, coverOffset(0)},
		images: [][]byte{append(img, make([]byte, 8<<10)...)}}) // past the importer's tiny file guard
	if err := os.WriteFile(filepath.Join(downloadPath, "Fjellvinden.azw3"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "nb:kindle-cover-test", Title: "Fjellvinden", SortTitle: "fjellvinden",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook}
	s, dl, bookRepo, _, ctx := languageFixture(t, libraryDir, book)
	store := covers.NewStore(t.TempDir())
	s.WithCoverStore(store)

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", "", nil, nil)

	got, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, _, ok := store.Resolve(got.ImageURL)
	if !ok {
		t.Fatalf("image_url = %q, want a stored file cover", got.ImageURL)
	}
	if b, _ := os.ReadFile(p); !bytes.HasPrefix(b, img) {
		t.Errorf("stored cover = %q", b[:min(len(b), 40)])
	}
}

func TestMobiCoverRecord(t *testing.T) {
	rec0 := func(m mobiFile) []byte {
		b := buildMobi(m)
		start, end := binary.BigEndian.Uint32(b[78:82]), binary.BigEndian.Uint32(b[86:90])
		return b[start:end]
	}
	img := [][]byte{fakeImage("a"), fakeImage("b")}
	if idx, ok := mobiCoverRecord(rec0(mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(1)}, images: img})); !ok || idx != 3 {
		t.Errorf("got %d, %v; want record 3 (first image 2 + offset 1)", idx, ok)
	}
	// "None" is an answer of its own, not a sum to bound later.
	if _, ok := mobiCoverRecord(rec0(mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(noKindleIndex)}, images: img})); ok {
		t.Error("EXTH 201 of 0xFFFFFFFF gave a cover record")
	}
	if _, ok := mobiCoverRecord(rec0(mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(0)}, images: img, noFirstImage: true})); ok {
		t.Error("a first image index of 0xFFFFFFFF gave a cover record")
	}
	// A record 0 too short to hold the first image index is no answer, not a
	// panic.
	short := make([]byte, 100) // exactly 100: no spare capacity to slice into
	copy(short, rec0(mobiFile{encoding: 65001}))
	if _, ok := mobiCoverRecord(short); ok {
		t.Error("a short record 0 gave a cover record")
	}
}

// A cover index past the record count is refused even when the bytes after
// the record list happen to look like a valid entry.
func TestKindleCoverRespectsTheRecordCount(t *testing.T) {
	b := buildMobi(mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(0)}, images: [][]byte{fakeImage("kindle-cover")}})
	// Claim two records: the cover is record 2, whose entry is still there.
	binary.BigEndian.PutUint16(b[76:78], 2)
	p := filepath.Join(t.TempDir(), "x.mobi")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := kindleCover(p); got != nil {
		t.Errorf("got %q from a record past the count", got)
	}
}

// A file cut off inside its record list, and a record 0 too short to hold an
// EXTH block, give no cover and no metadata rather than a misread.
func TestKindleTruncatedFiles(t *testing.T) {
	good := buildMobi(mobiFile{encoding: 65001, exth: []exthRecord{coverOffset(0)}, images: [][]byte{fakeImage("kindle-cover")}})
	p := filepath.Join(t.TempDir(), "cut.azw3")
	if err := os.WriteFile(p, good[:80], 0o644); err != nil { // the header and two bytes of the list
		t.Fatal(err)
	}
	if got := kindleCover(p); got != nil {
		t.Errorf("cover from a cut file: %q", got)
	}
	if _, err := ReadKindleMetadata(p); err == nil {
		t.Error("a file cut inside its record list read without error")
	}
	called := false
	walkEXTH(make([]byte, 20), func(uint32, []byte) { called = true })
	if called {
		t.Error("walkEXTH read records from a record 0 too short for a MOBI header")
	}
}
