package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Files too small to be a book (#2944). The reporter's Import page listed two
// 1008 byte .txt files under the audiobooks root as ebooks, suggested the book
// their folder named, and adopting them made a 1 KB text file that book's
// ebook.

// reasonTooSmall is the reason code such a file is listed with, spelled out
// so the test reads the stored value rather than trusting the constant.
const reasonTooSmall = "too_small"

// tinyNotesText is a 1008 byte .txt, the size the reporter's files were.
var tinyNotesText = func() []byte {
	b := []byte(strings.Repeat("Hinweis zum Hoerbuch. ", 45))
	return append(b, bytes.Repeat([]byte("."), 1008-len(b))...)
}()

// bookSized is the shared fixture body for a file that stands in for a real
// ebook: body padded with spaces to exactly MinPlausibleEbookBytes, the
// smallest size the scan, FindExisting and adoption accept as a book
// (#2944). Fixtures used to write one to 700 bytes, which is now a notes
// file.
func bookSized(body string) []byte {
	if int64(len(body)) >= MinPlausibleEbookBytes {
		return []byte(body)
	}
	return append([]byte(body), bytes.Repeat([]byte(" "), int(MinPlausibleEbookBytes)-len(body))...)
}

func writeTinyTxt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, tinyNotesText, 0o644); err != nil {
		t.Fatal(err)
	}
	if len(tinyNotesText) != 1008 {
		t.Fatalf("setup: tiny file is %d bytes, want 1008", len(tinyNotesText))
	}
}

// writeNovellaEpub writes a real, short EPUB of about 30 KB: the container,
// an OPF and one stored chapter. It is what a short real ebook looks like, and
// it must keep being offered for adoption.
func writeNovellaEpub(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string, method uint16) {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("mimetype", "application/epub+zip", zip.Store)
	add("META-INF/container.xml", `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`, zip.Deflate)
	add("content.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"></metadata></package>`, zip.Deflate)
	add("chapter1.xhtml", "<html><body><p>"+strings.Repeat("It was a quiet morning by the river. ", 800)+"</p></body></html>", zip.Store)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() < 25<<10 || buf.Len() > 40<<10 {
		t.Fatalf("setup: novella epub is %d bytes, want about 30 KB", buf.Len())
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tinyFileFixture is a scanner over a library root and an audiobook root,
// which are the same folder when combined is true, with James Patterson and
// his "Die 6. Geisel" in the catalogue at the given status.
func tinyFileFixture(t *testing.T, combined bool, status string) (s *Scanner, books *db.BookRepo, libraryDir, audiobookDir string, book *models.Book, ctx context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx = context.Background()
	books = db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	libraryDir = t.TempDir()
	audiobookDir = libraryDir
	if !combined {
		audiobookDir = t.TempDir()
	}
	s = NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database), books, authors, db.NewHistoryRepo(database),
		libraryDir, audiobookDir, "", "", "")
	s.WithSettings(db.NewSettingsRepo(database))
	s.WithUnmatchedUnits(db.NewUnmatchedUnitRepo(database))

	author := &models.Author{ForeignID: "ol:patterson", Name: "James Patterson", SortName: "Patterson, James", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book = &models.Book{ForeignID: "ol:geisel", AuthorID: author.ID, Title: "Die 6. Geisel", Status: status,
		Monitored: status == models.BookStatusWanted, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	return s, books, libraryDir, audiobookDir, book, ctx
}

func unitAt(units []db.UnmatchedUnit, path string) *db.UnmatchedUnit {
	for i := range units {
		if units[i].UnitPath == path {
			return &units[i]
		}
	}
	return nil
}

func assertBookHasNoFiles(t *testing.T, ctx context.Context, books *db.BookRepo, bookID int64) {
	t.Helper()
	files, err := books.ListFiles(ctx, bookID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("book has files %+v, want none", files)
	}
}

// TestScanLibrary_TinyTxtUnderAudiobookRootIsNotOffered is the reporter's
// layout: two 1008 byte .txt files under the audiobooks root, in the folder
// of a book the library has. Each is listed so the user can see and ignore
// it, but labelled too small to be a book, with no suggestion to adopt it.
func TestScanLibrary_TinyTxtUnderAudiobookRootIsNotOffered(t *testing.T) {
	s, books, _, audiobookDir, book, ctx := tinyFileFixture(t, false, models.BookStatusWanted)
	folder := filepath.Join(audiobookDir, "James Patterson", "Die 6. Geisel ()")
	tiny := []string{
		filepath.Join(folder, "Die 6. Geisel.txt"),
		filepath.Join(folder, "James Patterson - Die 6. Geisel", "Die 6. Geisel.txt"),
	}
	for _, p := range tiny {
		writeTinyTxt(t, p)
	}

	s.ScanLibrary(ctx)

	units := readUnmatchedFiles(t, ctx, s)
	if len(units) != len(tiny) {
		t.Fatalf("units = %+v, want one per tiny file", units)
	}
	for _, p := range tiny {
		u := unitAt(units, p)
		if u == nil {
			t.Fatalf("%s not listed: %+v", p, units)
		}
		if u.Reason != reasonTooSmall {
			t.Errorf("%s: reason = %q, want %q", p, u.Reason, reasonTooSmall)
		}
		if len(u.Candidates) != 0 {
			t.Errorf("%s: suggested %+v for adoption, want no suggestion for a 1008 byte file", p, u.Candidates)
		}
	}
	assertBookHasNoFiles(t, ctx, books, book.ID)
}

// TestScanLibrary_ShortRealEpubIsStillOffered guards the threshold from the
// other side: a 30 KB novella is a real book and keeps its suggestion.
func TestScanLibrary_ShortRealEpubIsStillOffered(t *testing.T) {
	s, _, libraryDir, _, book, ctx := tinyFileFixture(t, false, models.BookStatusSkipped)
	p := filepath.Join(libraryDir, "James Patterson", "Die 6. Geisel", "Die 6. Geisel.epub")
	writeNovellaEpub(t, p)

	s.ScanLibrary(ctx)

	u := unitAt(readUnmatchedFiles(t, ctx, s), p)
	if u == nil {
		t.Fatalf("%s not listed", p)
	}
	if u.Reason == reasonTooSmall {
		t.Errorf("a 30 KB epub was labelled too small to be a book")
	}
	if len(u.Candidates) == 0 || u.Candidates[0].BookID != book.ID {
		t.Errorf("candidates = %+v, want %q suggested", u.Candidates, book.Title)
	}
}

// TestScanLibrary_TinyFileIsNotGroupedWithItsBook: ebooks group by folder and
// file stem, so a 1 KB "Title.txt" beside a real "Title.epub" used to ride
// along in the epub's row, and adopting the epub registered the .txt too. The
// tiny file is its own row now.
func TestScanLibrary_TinyFileIsNotGroupedWithItsBook(t *testing.T) {
	s, _, libraryDir, _, book, ctx := tinyFileFixture(t, false, models.BookStatusSkipped)
	folder := filepath.Join(libraryDir, "James Patterson", "Die 6. Geisel")
	epub := filepath.Join(folder, "Die 6. Geisel.epub")
	txt := filepath.Join(folder, "Die 6. Geisel.txt")
	writeNovellaEpub(t, epub)
	writeTinyTxt(t, txt)

	s.ScanLibrary(ctx)

	units := readUnmatchedFiles(t, ctx, s)
	real := unitAt(units, epub)
	if real == nil {
		t.Fatalf("units = %+v, want the epub listed", units)
	}
	if len(real.MemberPaths) != 1 || real.MemberPaths[0] != epub {
		t.Errorf("epub row members = %v, want only the epub", real.MemberPaths)
	}
	if len(real.Candidates) == 0 || real.Candidates[0].BookID != book.ID {
		t.Errorf("epub candidates = %+v, want %q", real.Candidates, book.Title)
	}
	tiny := unitAt(units, txt)
	if tiny == nil || tiny.Reason != reasonTooSmall || len(tiny.Candidates) != 0 {
		t.Errorf("txt row = %+v, want its own row, too small, with no suggestion", tiny)
	}
}

// TestScanLibrary_TinyFileCombinedRoot: with BINDERY_AUDIOBOOK_DIR the same
// folder as the library, the same rules hold and audiobooks are unaffected:
// audio is never judged by size, a track can be small.
func TestScanLibrary_TinyFileCombinedRoot(t *testing.T) {
	s, _, libraryDir, _, book, ctx := tinyFileFixture(t, true, models.BookStatusSkipped)
	folder := filepath.Join(libraryDir, "James Patterson", "Die 6. Geisel")
	txt := filepath.Join(folder, "Liesmich.txt")
	epub := filepath.Join(folder, "Die 6. Geisel.epub")
	writeTinyTxt(t, txt)
	writeNovellaEpub(t, epub)
	audioFolder := filepath.Join(libraryDir, "James Patterson", "Die 6. Geisel (Hoerbuch)")
	if err := os.MkdirAll(audioFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"01.mp3", "02.mp3"} {
		if err := os.WriteFile(filepath.Join(audioFolder, name), []byte("ID3 tiny track"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s.ScanLibrary(ctx)

	units := readUnmatchedFiles(t, ctx, s)
	if u := unitAt(units, txt); u == nil || u.Reason != reasonTooSmall || len(u.Candidates) != 0 {
		t.Errorf("txt row = %+v, want too small with no suggestion", u)
	}
	if u := unitAt(units, epub); u == nil || u.Reason == reasonTooSmall || len(u.Candidates) == 0 || u.Candidates[0].BookID != book.ID {
		t.Errorf("epub row = %+v, want %q suggested", u, book.Title)
	}
	audio := unitAt(units, audioFolder)
	if audio == nil || audio.Format != models.MediaTypeAudiobook || audio.Reason == reasonTooSmall {
		t.Errorf("audio row = %+v, want the audiobook folder listed as an audiobook, not judged by size", audio)
	}
}

func TestTooSmallToBeABook(t *testing.T) {
	for _, tc := range []struct {
		path string
		size int64
		want bool
	}{
		{"Die 6. Geisel.txt", 1008, true},
		{"Notes.pdf", MinPlausibleEbookBytes - 1, true},
		{"Novella.epub", MinPlausibleEbookBytes, false},
		{"Novella.epub", 30 << 10, false},
		// Audio is never judged by size: a track can be small.
		{"01 Intro.mp3", 900, false},
		{"Book.m4b", 10, false},
		// Not a book extension at all.
		{"cover.jpg", 10, false},
	} {
		if got := TooSmallToBeABook(tc.path, tc.size); got != tc.want {
			t.Errorf("TooSmallToBeABook(%q, %d) = %v, want %v", tc.path, tc.size, got, tc.want)
		}
	}
	if reasonTooSmall != unmatchedReasonTooSmall {
		t.Errorf("test reason %q drifted from %q", reasonTooSmall, unmatchedReasonTooSmall)
	}
}

// TestScanLibrary_TinyFileIsNeverReconciled is the worse form of #2944: the
// scan's own reconcile, with nobody confirming anything, made a 1008 byte
// "Die 6. Geisel.txt" under the ebook root the Wanted book's file, the book
// went to Imported, and the real ebook was never searched for.
func TestScanLibrary_TinyFileIsNeverReconciled(t *testing.T) {
	for _, combined := range []bool{false, true} {
		name := "separate roots"
		if combined {
			name = "combined root"
		}
		t.Run(name, func(t *testing.T) {
			s, books, libraryDir, _, book, ctx := tinyFileFixture(t, combined, models.BookStatusWanted)
			tiny := filepath.Join(libraryDir, "James Patterson", "Die 6. Geisel", "Die 6. Geisel.txt")
			writeTinyTxt(t, tiny)

			s.ScanLibrary(ctx)

			assertBookHasNoFiles(t, ctx, books, book.ID)
			after, err := books.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != models.BookStatusWanted {
				t.Errorf("book status = %s, want wanted so the real ebook is still searched for", after.Status)
			}
			u := unitAt(readUnmatchedFiles(t, ctx, s), tiny)
			if u == nil || u.Reason != reasonTooSmall || len(u.Candidates) != 0 {
				t.Errorf("tiny file row = %+v, want listed as too small with no suggestion", u)
			}
		})
	}
}

// TestScanLibrary_TinyNotesBesideItsEpubStaysASidecar: the reconcile gate
// sits after the claim check, so a notes .txt beside the .epub that claimed
// the book is still that epub's sidecar, counted and not listed (#2188),
// rather than a too small row for the user to deal with.
func TestScanLibrary_TinyNotesBesideItsEpubStaysASidecar(t *testing.T) {
	s, books, libraryDir, _, book, ctx := tinyFileFixture(t, false, models.BookStatusWanted)
	folder := filepath.Join(libraryDir, "James Patterson", "Die 6. Geisel")
	epub := filepath.Join(folder, "Die 6. Geisel.epub")
	writeNovellaEpub(t, epub)
	writeTinyTxt(t, filepath.Join(folder, "Die 6. Geisel.txt"))

	s.ScanLibrary(ctx)

	files, err := books.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != epub {
		t.Errorf("book files = %+v, want only the epub", files)
	}
	if units := readUnmatchedFiles(t, ctx, s); len(units) != 0 {
		t.Errorf("units = %+v, want none: the notes file is the epub's sidecar", units)
	}
}

// TestFindExisting_SkipsTinyFile: the add author path binds the first file
// FindExisting returns with SetFilePath and skips the automatic search, so a
// 1 KB notes file must never be the answer.
func TestFindExisting_SkipsTinyFile(t *testing.T) {
	dir := t.TempDir()
	writeTinyTxt(t, filepath.Join(dir, "James Patterson", "Die 6. Geisel", "Die 6. Geisel.txt"))
	if got := NewLibrarySnapshot(dir, dir).FindExisting(context.Background(), "Die 6. Geisel", "James Patterson", models.MediaTypeEbook); got != "" {
		t.Errorf("FindExisting = %q, want no match for a 1008 byte file", got)
	}
	real := filepath.Join(dir, "James Patterson", "Die 6. Geisel", "Die 6. Geisel.epub")
	writeNovellaEpub(t, real)
	if got := NewLibrarySnapshot(dir, dir).FindExisting(context.Background(), "Die 6. Geisel", "James Patterson", models.MediaTypeEbook); got != real {
		t.Errorf("FindExisting = %q, want the real epub %q", got, real)
	}
}

// TestScanLibrary_TinyFileTierGates covers the other two reconcile tiers:
// a file under the floor is refused by the ASIN tier and the series position
// tier exactly as by the title tier, and a book sized file with the same name
// is still claimed by each, so the gate and not the fixture is what refuses.
func TestScanLibrary_TinyFileTierGates(t *testing.T) {
	for _, tc := range []struct {
		name string
		// file is the name under the library root; seed links the book so
		// only the tier under test can match it.
		file string
		seed func(t *testing.T, ctx context.Context, s *Scanner, database *sql.DB, book *models.Book)
	}{
		{
			name: "asin",
			file: "Unrelated Name [B0TINYASIN].txt",
			seed: func(t *testing.T, ctx context.Context, _ *Scanner, database *sql.DB, book *models.Book) {
				if _, err := database.ExecContext(ctx, `UPDATE books SET asin = ? WHERE id = ?`, "B0TINYASIN", book.ID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "series position",
			file: "[Stormlight Archive, Book 1] The Way of Kings - Brandon Sanderson.txt",
			seed: func(t *testing.T, ctx context.Context, s *Scanner, database *sql.DB, book *models.Book) {
				series := db.NewSeriesRepo(database)
				s.WithSeriesRepo(series)
				ser := &models.Series{ForeignID: "manual:stormlight", Title: "Stormlight Archive"}
				if err := series.Create(ctx, ser); err != nil {
					t.Fatal(err)
				}
				if err := series.LinkBook(ctx, ser.ID, book.ID, "1", true); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		for _, tiny := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s tiny=%v", tc.name, tiny), func(t *testing.T) {
				database, err := db.OpenMemory()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { database.Close() })
				ctx := context.Background()
				books := db.NewBookRepo(database)
				authors := db.NewAuthorRepo(database)
				libDir := t.TempDir()
				s := NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database), books, authors,
					db.NewHistoryRepo(database), libDir, "", "", "", "")
				author := &models.Author{ForeignID: "ol:north", Name: "Ursula North", SortName: "North, Ursula"}
				if err := authors.Create(ctx, author); err != nil {
					t.Fatal(err)
				}
				book := &models.Book{ForeignID: "ol:qg", AuthorID: author.ID, Title: "Quantum Gardens",
					Status: models.BookStatusWanted, Monitored: true, Genres: []string{}}
				if err := books.Create(ctx, book); err != nil {
					t.Fatal(err)
				}
				tc.seed(t, ctx, s, database, book)
				p := filepath.Join(libDir, tc.file)
				if tiny {
					writeTinyTxt(t, p)
				} else if err := os.WriteFile(p, bookSized("x"), 0o644); err != nil {
					t.Fatal(err)
				}

				s.ScanLibrary(ctx)

				files, err := books.ListFiles(ctx, book.ID)
				if err != nil {
					t.Fatal(err)
				}
				if tiny && len(files) != 0 {
					t.Errorf("tier attached a 1008 byte file: %+v", files)
				}
				if !tiny && (len(files) != 1 || files[0].Path != p) {
					t.Errorf("tier did not attach the book sized file: %+v", files)
				}
			})
		}
	}
}

// TestRootFormat names each scanned root's format only when the roots are
// separate folders, and nothing for a combined or unknown root.
func TestRootFormat(t *testing.T) {
	separate := NewScanner(nil, nil, nil, nil, nil, "/data/books", "/data/audiobooks", "", "", "")
	combined := NewScanner(nil, nil, nil, nil, nil, "/data/media", "", "", "", "")
	for _, tc := range []struct {
		s    *Scanner
		root string
		want string
	}{
		{separate, "/data/books", models.MediaTypeEbook},
		{separate, "/data/audiobooks/", models.MediaTypeAudiobook},
		{separate, "/data/elsewhere", ""},
		{separate, "", ""},
		{combined, "/data/media", ""},
	} {
		if got := tc.s.RootFormat(tc.root); got != tc.want {
			t.Errorf("RootFormat(%q) = %q, want %q", tc.root, got, tc.want)
		}
	}
}
