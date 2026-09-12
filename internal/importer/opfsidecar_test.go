package importer

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// opfProbe is a namespace-agnostic parse-back of the fields BuildOPFXML
// writes, mirroring the style parseOPFMetadata uses to read a real EPUB's
// embedded OPF (epubmeta.go) — proving the output is not just
// string-shaped but genuinely well-formed, parseable XML.
type opfProbe struct {
	XMLName  xml.Name `xml:"package"`
	UniqueID string   `xml:"unique-identifier,attr"`
	Metadata struct {
		Title       string   `xml:"title"`
		Creator     string   `xml:"creator"`
		Contributor string   `xml:"contributor"`
		Language    string   `xml:"language"`
		Publisher   string   `xml:"publisher"`
		Date        string   `xml:"date"`
		Description string   `xml:"description"`
		Subjects    []string `xml:"subject"`
		Identifiers []struct {
			ID     string `xml:"id,attr"`
			Scheme string `xml:"scheme,attr"`
			Value  string `xml:",chardata"`
		} `xml:"identifier"`
		Meta []struct {
			Name    string `xml:"name,attr"`
			Content string `xml:"content,attr"`
		} `xml:"meta"`
	} `xml:"metadata"`
}

func parseOPFProbe(t *testing.T, xmlBytes []byte) opfProbe {
	t.Helper()
	var p opfProbe
	if err := xml.Unmarshal(xmlBytes, &p); err != nil {
		t.Fatalf("BuildOPFXML output did not parse as XML: %v\n---\n%s", err, xmlBytes)
	}
	return p
}

func metaContent(p opfProbe, name string) (string, bool) {
	for _, m := range p.Metadata.Meta {
		if m.Name == name {
			return m.Content, true
		}
	}
	return "", false
}

func fullBookFixture() (*models.Book, *models.Author, *models.Edition) {
	release := time.Date(2020, 3, 15, 0, 0, 0, 0, time.UTC)
	pub := time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC)
	isbn13 := "9780345472199"
	book := &models.Book{
		ID:               42,
		Title:            "The Way of Kings",
		SortTitle:        "Way of Kings, The",
		Description:      "A stormlight epic.",
		Genres:           []string{"Fantasy", "Epic"},
		ReleaseDate:      &release,
		Language:         "eng",
		Narrator:         "Michael Kramer",
		ASIN:             "B0031RS9YE",
		ForeignID:        "12345",
		MetadataProvider: "hardcover",
		AverageRating:    4.7,
		RatingsCount:     9001,
	}
	author := &models.Author{
		Name:     "Brandon Sanderson",
		SortName: "Sanderson, Brandon",
	}
	edition := &models.Edition{
		BookID:      42,
		ISBN13:      &isbn13,
		Publisher:   "Tor Books",
		PublishDate: &pub,
		Language:    "eng",
	}
	return book, author, edition
}

func TestBuildOPFXML_FullFields(t *testing.T) {
	book, author, edition := fullBookFixture()

	xmlBytes, err := BuildOPFXML(book, author, edition, "The Stormlight Archive", "1")
	if err != nil {
		t.Fatalf("BuildOPFXML: %v", err)
	}
	p := parseOPFProbe(t, xmlBytes)

	if p.Metadata.Title != book.Title {
		t.Errorf("title = %q, want %q", p.Metadata.Title, book.Title)
	}
	if p.Metadata.Creator != author.Name {
		t.Errorf("creator = %q, want %q", p.Metadata.Creator, author.Name)
	}
	if p.Metadata.Contributor != book.Narrator {
		t.Errorf("contributor (narrator) = %q, want %q", p.Metadata.Contributor, book.Narrator)
	}
	if p.Metadata.Language != "en" {
		t.Errorf("language = %q, want normalized %q", p.Metadata.Language, "en")
	}
	if p.Metadata.Publisher != "Tor Books" {
		t.Errorf("publisher = %q, want %q", p.Metadata.Publisher, "Tor Books")
	}
	if p.Metadata.Date != "2021-06-01" {
		t.Errorf("date = %q, want edition.PublishDate %q", p.Metadata.Date, "2021-06-01")
	}
	if p.Metadata.Description != book.Description {
		t.Errorf("description = %q, want %q", p.Metadata.Description, book.Description)
	}
	if len(p.Metadata.Subjects) != 2 || p.Metadata.Subjects[0] != "Fantasy" || p.Metadata.Subjects[1] != "Epic" {
		t.Errorf("subjects = %v, want [Fantasy Epic]", p.Metadata.Subjects)
	}
	if series, ok := metaContent(p, "calibre:series"); !ok || series != "The Stormlight Archive" {
		t.Errorf("calibre:series = %q (found=%v), want %q", series, ok, "The Stormlight Archive")
	}
	if idx, ok := metaContent(p, "calibre:series_index"); !ok || idx != "1" {
		t.Errorf("calibre:series_index = %q (found=%v), want %q", idx, ok, "1")
	}
	if sortTitle, ok := metaContent(p, "calibre:title_sort"); !ok || sortTitle != book.SortTitle {
		t.Errorf("calibre:title_sort = %q (found=%v), want %q", sortTitle, ok, book.SortTitle)
	}

	// Identifiers: bindery id is the unique-identifier target; ISBN and ASIN
	// use their special-cased Calibre scheme names.
	found := map[string]struct{ scheme, id string }{}
	for _, ident := range p.Metadata.Identifiers {
		found[ident.Value] = struct{ scheme, id string }{ident.Scheme, ident.ID}
	}
	if got := found["42"]; got.scheme != "BINDERY" || got.id != "bindery-id" {
		t.Errorf("bindery identifier = %+v, want scheme BINDERY id bindery-id", got)
	}
	if p.UniqueID != "bindery-id" {
		t.Errorf("package unique-identifier = %q, want %q", p.UniqueID, "bindery-id")
	}
	if got := found["9780345472199"]; got.scheme != "ISBN" {
		t.Errorf("isbn identifier scheme = %q, want ISBN", got.scheme)
	}
	if got := found["B0031RS9YE"]; got.scheme != "MOBI-ASIN" {
		t.Errorf("asin identifier scheme = %q, want MOBI-ASIN", got.scheme)
	}
}

// TestBuildOPFXML_ExcludesRatings is the field-scope assertion this feature
// hinges on: docs/third-party-data.md draws the exact same line for
// commercial-use compliance ("Title, author, series, edition, publisher,
// narrator, description, and cover are facts and are fine" vs.
// "average_rating and ratings_count ... [s]trip them"). The sidecar must
// never carry AverageRating/RatingsCount so a deployment that already
// excludes them from the API/UI per that doc doesn't have them leak back
// out through this file instead.
func TestBuildOPFXML_ExcludesRatings(t *testing.T) {
	book, author, edition := fullBookFixture()
	xmlBytes, err := BuildOPFXML(book, author, edition, "", "")
	if err != nil {
		t.Fatalf("BuildOPFXML: %v", err)
	}
	out := string(xmlBytes)
	for _, needle := range []string{"4.7", "9001", "rating"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(needle)) {
			t.Errorf("output must not reference AverageRating/RatingsCount, found %q in:\n%s", needle, out)
		}
	}
}

func TestBuildOPFXML_NoCoverReference(t *testing.T) {
	book, author, edition := fullBookFixture()
	book.ImageURL = "https://example.com/cover.jpg"
	xmlBytes, err := BuildOPFXML(book, author, edition, "", "")
	if err != nil {
		t.Fatalf("BuildOPFXML: %v", err)
	}
	if strings.Contains(string(xmlBytes), "cover") {
		t.Errorf("output must not reference a cover (none is ever written to the library folder), got:\n%s", xmlBytes)
	}
}

func TestBuildOPFXML_MinimalBook(t *testing.T) {
	book := &models.Book{ID: 7, Title: "Bare Book"}
	xmlBytes, err := BuildOPFXML(book, nil, nil, "", "")
	if err != nil {
		t.Fatalf("BuildOPFXML: %v", err)
	}
	p := parseOPFProbe(t, xmlBytes)
	if p.Metadata.Title != "Bare Book" {
		t.Errorf("title = %q, want %q", p.Metadata.Title, "Bare Book")
	}
	if p.Metadata.Creator != "" {
		t.Errorf("creator = %q, want empty (nil author)", p.Metadata.Creator)
	}
	if len(p.Metadata.Identifiers) != 1 || p.Metadata.Identifiers[0].Value != "7" {
		t.Errorf("identifiers = %+v, want just the bindery id", p.Metadata.Identifiers)
	}
}

func TestBuildOPFXML_NilBook(t *testing.T) {
	if _, err := BuildOPFXML(nil, nil, nil, "", ""); err == nil {
		t.Fatal("expected an error for a nil book, got nil")
	}
}

// TestBuildOPFXML_EscapesSpecialCharacters guards against a title/author
// containing XML metacharacters (an ampersand is routine — "Fantasy &
// Science Fiction") producing malformed output a reader can't parse.
func TestBuildOPFXML_EscapesSpecialCharacters(t *testing.T) {
	book := &models.Book{ID: 1, Title: `<Weird> & "Title"`}
	author := &models.Author{Name: "A & B", SortName: `B, "A"`}
	xmlBytes, err := BuildOPFXML(book, author, nil, "", "")
	if err != nil {
		t.Fatalf("BuildOPFXML: %v", err)
	}
	p := parseOPFProbe(t, xmlBytes)
	if p.Metadata.Title != book.Title {
		t.Errorf("round-tripped title = %q, want %q", p.Metadata.Title, book.Title)
	}
	if p.Metadata.Creator != author.Name {
		t.Errorf("round-tripped creator = %q, want %q", p.Metadata.Creator, author.Name)
	}
}

func TestWriteOPFSidecarFile(t *testing.T) {
	dir := t.TempDir()
	roots := []string{dir}
	bookDir := filepath.Join(dir, "Brandon Sanderson", "The Way of Kings (2020)")
	book, author, edition := fullBookFixture()

	if err := WriteOPFSidecarFile(bookDir, roots, book, author, edition, "", ""); err != nil {
		t.Fatalf("WriteOPFSidecarFile: %v", err)
	}
	dest := filepath.Join(bookDir, "metadata.opf")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", dest, err)
	}
	p := parseOPFProbe(t, data)
	if p.Metadata.Title != book.Title {
		t.Errorf("written file title = %q, want %q", p.Metadata.Title, book.Title)
	}

	// Re-writing (e.g. via Reorganize) must overwrite cleanly, not append or
	// fail because the file and directory already exist.
	book.Title = "The Way of Kings: Author's Definitive Edition"
	if err := WriteOPFSidecarFile(bookDir, roots, book, author, edition, "", ""); err != nil {
		t.Fatalf("WriteOPFSidecarFile (overwrite): %v", err)
	}
	data, err = os.ReadFile(dest)
	if err != nil {
		t.Fatalf("re-read after overwrite: %v", err)
	}
	p = parseOPFProbe(t, data)
	if p.Metadata.Title != book.Title {
		t.Errorf("after overwrite, title = %q, want %q", p.Metadata.Title, book.Title)
	}

	// The write stages through a temp file and renames; neither the first
	// write nor the overwrite may leave that temp file behind. A stray one
	// would sit in the library folder forever and, worse, keep the folder
	// non-empty so Reorganize's pruneEmptyParents could never reclaim it.
	entries, err := os.ReadDir(bookDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "metadata.opf" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("book folder holds %v, want just [metadata.opf]", names)
	}
}

// TestWriteOPFSidecarFile_RefusesOutsideRoots is the direct regression test
// for dirWithinRoots: WriteOPFSidecarFile must refuse to write when dir does
// not resolve inside any given root, rather than trusting that a caller
// already sanitized it. Every real caller derives dir from
// Renamer.DestPath/AudiobookDestDir, which already apply this exact
// containment check — this test exercises the independent guard
// WriteOPFSidecarFile itself applies regardless of what the caller did.
func TestWriteOPFSidecarFile_RefusesOutsideRoots(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // a sibling temp dir, not under root
	book, author, edition := fullBookFixture()

	err := WriteOPFSidecarFile(outside, []string{root}, book, author, edition, "", "")
	if err == nil {
		t.Fatal("want an error writing outside the given roots, got nil")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "metadata.opf")); !os.IsNotExist(statErr) {
		t.Errorf("metadata.opf should not have been written to %s", outside)
	}

	// A traversal attempt through a root-relative-looking path must not
	// escape either: filepath.Join(root, "..", "escaped") resolves outside
	// root once Cleaned, same as ensureContained already checks for
	// Renamer.DestPath.
	traversal := filepath.Join(root, "..", "escaped")
	if err := WriteOPFSidecarFile(traversal, []string{root}, book, author, edition, "", ""); err == nil {
		t.Fatal("want an error for a path that traverses outside root, got nil")
	}

	// Sanity: the same call with the correct root still succeeds.
	inside := filepath.Join(root, "Some Author", "Some Book (2020)")
	if err := WriteOPFSidecarFile(inside, []string{root}, book, author, edition, "", ""); err != nil {
		t.Fatalf("WriteOPFSidecarFile inside root: %v", err)
	}
}

// TestBuildOPFXML_EscapesEveryInterpolatedField widens the escaping guard past
// title and author. Every value below arrives from provider metadata or a user
// edit and can carry XML metacharacters, so each interpolation site is checked
// on its own rather than trusting that one escaped field means the rest are —
// a single unescaped "&" anywhere makes the whole document unparseable, taking
// the correctly-escaped fields down with it.
func TestBuildOPFXML_EscapesEveryInterpolatedField(t *testing.T) {
	isbn13 := "9780345472199"
	book := &models.Book{
		ID:          5,
		Title:       `Cats & Dogs <vol 1> "revised" 'ed'`,
		SortTitle:   `Cats & Dogs, "revised"`,
		Description: "Chapter 1 & <b>2</b>\nA \"quoted\" line — with ünïcode 😀 and an apostrophe's worth of trouble",
		Genres:      []string{`Sci-Fi & Fantasy`, `<Horror>`, `"Literary"`},
		Narrator:    `Reader & "Co"`,
		Language:    "eng",
	}
	author := &models.Author{Name: `Ann & <Bob>`, SortName: `Bob, "Ann" & Co`}
	edition := &models.Edition{Publisher: `Smith & <Sons> "Ltd"`, ISBN13: &isbn13}

	xmlBytes, err := BuildOPFXML(book, author, edition, `Saga & <One> "Two"`, `1 & 2`)
	if err != nil {
		t.Fatalf("BuildOPFXML: %v", err)
	}
	p := parseOPFProbe(t, xmlBytes)

	for _, tc := range []struct{ field, got, want string }{
		{"title", p.Metadata.Title, book.Title},
		{"creator", p.Metadata.Creator, author.Name},
		{"contributor", p.Metadata.Contributor, book.Narrator},
		{"publisher", p.Metadata.Publisher, edition.Publisher},
		{"description", p.Metadata.Description, book.Description},
	} {
		if tc.got != tc.want {
			t.Errorf("%s round-tripped as %q, want %q", tc.field, tc.got, tc.want)
		}
	}
	if len(p.Metadata.Subjects) != len(book.Genres) {
		t.Fatalf("subjects = %v, want %d entries", p.Metadata.Subjects, len(book.Genres))
	}
	for i, want := range book.Genres {
		if p.Metadata.Subjects[i] != want {
			t.Errorf("subject[%d] = %q, want %q", i, p.Metadata.Subjects[i], want)
		}
	}
	// Attribute sites (opf:file-as, calibre:*) escape separately from element
	// text, so they get their own assertions.
	for _, tc := range []struct{ name, want string }{
		{"calibre:title_sort", book.SortTitle},
		{"calibre:series", `Saga & <One> "Two"`},
		{"calibre:series_index", `1 & 2`},
	} {
		got, ok := metaContent(p, tc.name)
		if !ok || got != tc.want {
			t.Errorf("%s = %q (found=%v), want %q", tc.name, got, ok, tc.want)
		}
	}
	if len(p.Metadata.Identifiers) == 0 {
		t.Error("expected at least the bindery identifier")
	}
}

// Provider metadata occasionally carries raw control bytes or invalid UTF-8,
// which are not representable in XML at all. They must be sanitised into a
// still-parseable document rather than producing a file no reader can open.
func TestBuildOPFXML_SanitisesUnrepresentableCharacters(t *testing.T) {
	book := &models.Book{
		ID:          9,
		Title:       "Bad\x00Title\x08Here",
		Description: "invalid utf-8: \xff\xfe tail",
	}
	xmlBytes, err := BuildOPFXML(book, nil, nil, "", "")
	if err != nil {
		t.Fatalf("BuildOPFXML: %v", err)
	}
	p := parseOPFProbe(t, xmlBytes)
	if !strings.Contains(p.Metadata.Title, "Title") || !strings.Contains(p.Metadata.Title, "Here") {
		t.Errorf("title lost its printable content: %q", p.Metadata.Title)
	}
	if strings.ContainsAny(string(xmlBytes), "\x00\x08") {
		t.Error("raw control characters must not reach the file")
	}
}

// A nil author, nil edition, nil dates, an empty description and an empty
// genre must all be skipped rather than emitting an empty element a reader
// would take as "this book has no title/publisher/date" data.
func TestBuildOPFXML_SkipsEmptyFieldsEntirely(t *testing.T) {
	book := &models.Book{
		ID:          11,
		Title:       "Only A Title",
		SortTitle:   "   ",
		Description: "  \n ",
		Genres:      []string{"", "   ", "Fantasy"},
		Narrator:    " ",
		Language:    "",
	}
	author := &models.Author{Name: "   "}
	edition := &models.Edition{Publisher: " ", Language: "  "}

	xmlBytes, err := BuildOPFXML(book, author, edition, "  ", "3")
	if err != nil {
		t.Fatalf("BuildOPFXML: %v", err)
	}
	out := string(xmlBytes)
	for _, tag := range []string{"dc:creator", "dc:contributor", "dc:publisher", "dc:date", "dc:description", "dc:language", "calibre:title_sort", "calibre:series", "calibre:series_index"} {
		if strings.Contains(out, tag) {
			t.Errorf("blank field still emitted %s:\n%s", tag, out)
		}
	}
	p := parseOPFProbe(t, xmlBytes)
	if len(p.Metadata.Subjects) != 1 || p.Metadata.Subjects[0] != "Fantasy" {
		t.Errorf("subjects = %v, want only the non-blank genre", p.Metadata.Subjects)
	}
}

// The edition wins over the book for language, publisher and date — the same
// precedence calibreMetadata applies, which is the whole point of sharing the
// calibre.* helpers. A divergence here means a book's sidecar and its
// calibredb-pushed metadata disagree.
func TestBuildOPFXML_EditionOverridesBookFields(t *testing.T) {
	book, author, edition := fullBookFixture()
	book.Language = "spa"
	edition.Language = "fre"

	p := parseOPFProbe(t, mustBuildOPF(t, book, author, edition))
	if p.Metadata.Language != "fr" {
		t.Errorf("language = %q, want the edition's normalized %q", p.Metadata.Language, "fr")
	}
	if p.Metadata.Date != "2021-06-01" {
		t.Errorf("date = %q, want the edition's publish date", p.Metadata.Date)
	}

	// With no edition date, the book's release date is the fallback.
	edition.PublishDate = nil
	p = parseOPFProbe(t, mustBuildOPF(t, book, author, edition))
	if p.Metadata.Date != "2020-03-15" {
		t.Errorf("date = %q, want the book's release date fallback", p.Metadata.Date)
	}

	// With neither, no dc:date at all rather than an empty one.
	book.ReleaseDate = nil
	if out := string(mustBuildOPF(t, book, author, edition)); strings.Contains(out, "dc:date") {
		t.Errorf("no date anywhere must emit no dc:date:\n%s", out)
	}
}

func mustBuildOPF(t *testing.T, book *models.Book, author *models.Author, edition *models.Edition) []byte {
	t.Helper()
	out, err := BuildOPFXML(book, author, edition, "", "")
	if err != nil {
		t.Fatalf("BuildOPFXML: %v", err)
	}
	return out
}
