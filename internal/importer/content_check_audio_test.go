package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

const audioCheckKey = "import.audiobook.content_check"

// buildID3v23Album is buildID3v23 for the two tags the audiobook check reads: a
// TIT2 track title and a TALB album.
func buildID3v23Album(title, album string) []byte {
	textFrame := func(id, text string) []byte {
		body := append([]byte{0x03}, []byte(text)...) // $03 = UTF-8
		return append(frameHeaderV23(id, len(body)), body...)
	}
	var frames []byte
	if title != "" {
		frames = append(frames, textFrame("TIT2", title)...)
	}
	if album != "" {
		frames = append(frames, textFrame("TALB", album)...)
	}
	header := []byte("ID3")
	header = append(header, 0x03, 0x00, 0x00)
	header = append(header, synchsafe(uint32(len(frames)))...)
	return append(header, frames...)
}

func newAudioFixture(t *testing.T, bookTitle, authorName string) *contentFixture {
	t.Helper()
	f := newContentFixtureFor(t, bookTitle, authorName, models.MediaTypeAudiobook)
	f.scanner.audiobookDir = t.TempDir()
	f.dl.Title = "Release.Audiobook.M4B"
	return f
}

// writeAlbum drops n flat tracks tagged with the given album into the download
// folder, each with a numbered chapter title.
func (f *contentFixture) writeAlbum(t *testing.T, album string, n int) {
	t.Helper()
	slug := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, album)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("%s_%03d.mp3", slug, i)
		data := buildID3v23Album(fmt.Sprintf("Chapter %d", i), album)
		if err := os.WriteFile(filepath.Join(f.dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writeTrackTitled drops one flat track with the given track title and no album.
func (f *contentFixture) writeTrackTitled(t *testing.T, name, title string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), buildID3v23Album(title, ""), 0o644); err != nil {
		t.Fatal(err)
	}
}

// countReads makes the scanner count and record every tag read the content
// check makes, still reading the real file.
func (f *contentFixture) countReads() *tagReadLog {
	log := &tagReadLog{}
	f.scanner.readTags = func(path string) (AudioTags, error) {
		log.add(path)
		return ReadAudioTags(path)
	}
	return log
}

type tagReadLog struct {
	mu    sync.Mutex
	paths []string
}

func (l *tagReadLog) add(p string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = append(l.paths, p)
}

func (l *tagReadLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.paths)
}

func (f *contentFixture) placedAudiobookFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(f.scanner.audiobookDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

type auditCase struct {
	name   string
	wanted string
	albums []struct {
		album string
		n     int
	}
	others map[string]int // expected "found" titles and counts
}

func albums(pairs ...any) []struct {
	album string
	n     int
} {
	var out []struct {
		album string
		n     int
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, struct {
			album string
			n     int
		}{pairs[i].(string), pairs[i+1].(int)})
	}
	return out
}

// The four #3246 acceptance shapes that must be flagged, at their real sizes.
var flaggedAudioCases = []auditCase{
	{"1 Chase", "Chase", albums("Chase", 2, "Corralled", 9, "Coming Home to Myself", 8), map[string]int{"Corralled": 9, "Coming Home to Myself": 8}},
	{"2 Firm", "The Firm", albums("The Firm", 60, "The Runaway Jury", 228), map[string]int{"The Runaway Jury": 228}},
	{"3 The End", "The End", albums("The End", 14, "The Winter Sea", 2), map[string]int{"The Winter Sea": 2}},
	{"4a NYPD Red 3", "NYPD Red 3", albums("NYPD Red 4", 83, "Coffin Road", 33, "Cold Frame", 26, "Artful", 19),
		map[string]int{"NYPD Red 4": 83, "Coffin Road": 33, "Cold Frame": 26, "Artful": 19}},
	{"4b all NYPD Red 4", "NYPD Red 3", albums("NYPD Red 4", 161), map[string]int{"NYPD Red 4": 161}},
}

// Acceptance cases 1 to 4 and 11 in block mode: the download is blocked naming
// what was found, nothing is attached, the book stays Wanted.
func TestAudiobookContentCheck_BlockRefusesTheMixedDownloads(t *testing.T) {
	for _, c := range flaggedAudioCases {
		t.Run(c.name, func(t *testing.T) {
			f := newAudioFixture(t, c.wanted, "Author Name")
			f.setMode(t, audioCheckKey, "block")
			total := 0
			for _, a := range c.albums {
				f.writeAlbum(t, a.album, a.n)
				total += a.n
			}
			f.importDownload(t)

			dl, book := f.reload(t)
			if dl.Status != models.StateImportBlocked {
				t.Fatalf("status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImportBlocked)
			}
			for title, n := range c.others {
				want := fmt.Sprintf("%s (%d)", title, n)
				if !strings.Contains(dl.ErrorMessage, want) {
					t.Errorf("message does not name %q: %s", want, dl.ErrorMessage)
				}
			}
			for _, want := range []string{c.wanted, "Match to book"} {
				if !strings.Contains(dl.ErrorMessage, want) {
					t.Errorf("message does not mention %q: %s", want, dl.ErrorMessage)
				}
			}
			if book.Status != models.BookStatusWanted {
				t.Errorf("book status = %q, want wanted", book.Status)
			}
			if n := f.bookFileCount(t); n != 0 {
				t.Errorf("%d book file(s) attached to a refused download", n)
			}
			if placed := f.placedAudiobookFiles(t); len(placed) != 0 {
				t.Errorf("%d file(s) placed in the audiobook library", len(placed))
			}
			if !f.blocklisted(t) {
				t.Error("the release was not blocklisted")
			}
			if dl.ImportPath == "" {
				t.Error("the download path was not recorded for Match to book")
			}
			cc, ok := f.eventData(t, models.HistoryEventImportFailed)["contentCheck"].(map[string]any)
			if !ok {
				t.Fatal("importFailed event has no contentCheck")
			}
			if cc["kind"] != "audiobook" || int(cc["tracksRead"].(float64)) != total {
				t.Errorf("contentCheck = %v, want audiobook with %d tracks read", cc, total)
			}
			call := f.notif.lookup("downloadFailed")
			if call == nil {
				t.Fatal("no downloadFailed webhook")
			}
			if _, has := call.payload["contentCheck"]; !has {
				t.Error("the failure webhook did not carry contentCheck")
			}
		})
	}
}

// Case 10: warn mode, which is the default. The same four downloads import, and
// the History event and the webhook show the mismatches.
func TestAudiobookContentCheck_WarnImportsAndAttachesTheFinding(t *testing.T) {
	for _, mode := range []string{"", "warn"} {
		for _, c := range flaggedAudioCases {
			t.Run(c.name+"/"+mode, func(t *testing.T) {
				f := newAudioFixture(t, c.wanted, "Author Name")
				if mode != "" {
					f.setMode(t, audioCheckKey, mode)
				}
				total := 0
				for _, a := range c.albums {
					f.writeAlbum(t, a.album, a.n)
					total += a.n
				}
				f.importDownload(t)

				dl, _ := f.reload(t)
				if dl.Status != models.StateImported {
					t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
				}
				if f.blocklisted(t) {
					t.Error("warn mode blocklisted the release")
				}
				cc, ok := f.eventData(t, models.HistoryEventBookImported)["contentCheck"].(map[string]any)
				if !ok {
					t.Fatalf("bookImported event has no contentCheck: %v", f.eventData(t, models.HistoryEventBookImported))
				}
				if int(cc["tracksRead"].(float64)) != total {
					t.Errorf("tracksRead = %v, want %d", cc["tracksRead"], total)
				}
				found := map[string]int{}
				for _, m := range cc["mismatches"].([]any) {
					m := m.(map[string]any)
					found[m["title"].(string)] = int(m["count"].(float64))
				}
				for title, n := range c.others {
					if found[title] != n {
						t.Errorf("mismatch %q = %d, want %d (found %v)", title, found[title], n, found)
					}
				}
				call := f.notif.lookup("bookImported")
				if call == nil {
					t.Fatal("no bookImported webhook")
				}
				if _, has := call.payload["contentCheck"]; !has {
					t.Error("the import webhook did not carry contentCheck")
				}
			})
		}
	}
}

// Cases 5 to 9, in block mode: these import and say nothing.
func TestAudiobookContentCheck_BlockImportsTheCleanShapes(t *testing.T) {
	t.Run("5 seventy six chapter files, album equals the wanted title", func(t *testing.T) {
		f := newAudioFixture(t, "The Hobbit", "J. R. R. Tolkien")
		f.setMode(t, audioCheckKey, "block")
		f.writeAlbum(t, "The Hobbit", 76)
		f.importDownload(t)
		f.assertImportedSilently(t)
	})
	t.Run("6 a folder with no tags", func(t *testing.T) {
		f := newAudioFixture(t, "The Hobbit", "J. R. R. Tolkien")
		f.setMode(t, audioCheckKey, "block")
		for i := 1; i <= 8; i++ {
			if err := os.WriteFile(filepath.Join(f.dir, fmt.Sprintf("track%02d.mp3", i)), []byte("not an audio file at all"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		f.importDownload(t)
		f.assertImportedSilently(t)
	})
	t.Run("7 album is the main title of a wanted title with a subtitle and volume", func(t *testing.T) {
		f := newAudioFixture(t, "Dominion of Blades: A LitRPG Adventure (Volume 1)", "Author Name")
		f.setMode(t, audioCheckKey, "block")
		f.writeAlbum(t, "Dominion of Blades", 30)
		f.importDownload(t)
		f.assertImportedSilently(t)
	})
	t.Run("8 unique chapter names with an album equal to the wanted title", func(t *testing.T) {
		f := newAudioFixture(t, "The Hobbit", "J. R. R. Tolkien")
		f.setMode(t, audioCheckKey, "block")
		for i := 0; i < 20; i++ {
			data := buildID3v23Album(fmt.Sprintf("Unique Chapter Name %d", i), "The Hobbit")
			if err := os.WriteFile(filepath.Join(f.dir, fmt.Sprintf("t%02d.mp3", i)), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		f.importDownload(t)
		f.assertImportedSilently(t)
	})
	t.Run("9 two excerpt bonus tracks in an otherwise matching folder", func(t *testing.T) {
		f := newAudioFixture(t, "The Hobbit", "J. R. R. Tolkien")
		f.setMode(t, audioCheckKey, "block")
		f.writeAlbum(t, "The Hobbit", 30)
		f.writeTrackTitled(t, "bonus1.mp3", "Excerpt from The Silmarillion")
		f.writeTrackTitled(t, "bonus2.mp3", "Excerpt from The Silmarillion")
		f.importDownload(t)
		f.assertImportedSilently(t)
	})
	t.Run("a translated edition whose every file carries one other title", func(t *testing.T) {
		f := newAudioFixture(t, "The Lord of the Rings", "J. R. R. Tolkien")
		f.setMode(t, audioCheckKey, "block")
		f.writeAlbum(t, "Der Herr der Ringe", 40)
		f.importDownload(t)
		dl, _ := f.reload(t)
		if dl.Status != models.StateImported || f.blocklisted(t) {
			t.Fatalf("status = %q (%s), blocklisted = %v, want a clean import", dl.Status, dl.ErrorMessage, f.blocklisted(t))
		}
		// The pass is recorded as a quiet note and nothing else.
		cc, _ := f.eventData(t, models.HistoryEventBookImported)["contentCheck"].(map[string]any)
		if rt, _ := cc["retitled"].(map[string]any); rt["title"] != "Der Herr der Ringe" {
			t.Errorf("no retitled note: %v", cc)
		}
	})
}

// Off does nothing and reads no tag.
func TestAudiobookContentCheck_OffReadsNothing(t *testing.T) {
	f := newAudioFixture(t, "Chase", "Author Name")
	f.setMode(t, audioCheckKey, "off")
	reads := f.countReads()
	f.writeAlbum(t, "Chase", 2)
	f.writeAlbum(t, "Corralled", 9)
	f.importDownload(t)
	f.assertImportedSilently(t)
	if n := reads.count(); n != 0 {
		t.Errorf("%d tag read(s) with the check off", n)
	}
	if len(f.checks) != 0 {
		t.Errorf("the check ran %v with the setting off", f.checks)
	}
}

// Case 11: Match to book on a blocked item attaches the folder and does not run
// the check again. Retry import does run it again.
func TestAudiobookContentCheck_MatchToBookSkipsAndRetryReRuns(t *testing.T) {
	f := newAudioFixture(t, "Chase", "Author Name")
	f.setMode(t, audioCheckKey, "block")
	reads := f.countReads()
	f.writeAlbum(t, "Chase", 2)
	f.writeAlbum(t, "Corralled", 9)
	f.writeAlbum(t, "Coming Home to Myself", 8)
	f.importDownload(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImportBlocked {
		t.Fatalf("setup: status = %q, want blocked", dl.Status)
	}
	if n := reads.count(); n != 19 {
		t.Errorf("first attempt read %d tags, want one per file (19)", n)
	}

	// Retry import goes back through the poll path and checks again.
	f.importDownload(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImportBlocked {
		t.Fatalf("retry: status = %q, want blocked again", dl.Status)
	}
	if n := reads.count(); n != 38 {
		t.Errorf("after a retry %d tags were read, want 38 (19 per attempt)", n)
	}

	// Match to book is a manual import: it attaches the folder, reads no tag.
	f.checks = nil
	f.scanner.ImportFromPath(context.Background(), f.dl, f.dir, "")
	dl, book := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("Match to book: status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
	if book.Status != models.BookStatusImported || f.bookFileCount(t) == 0 {
		t.Error("Match to book did not attach the folder")
	}
	if n := reads.count(); n != 38 {
		t.Errorf("Match to book read tags: %d reads, want still 38", n)
	}
	if len(f.checks) != 0 {
		t.Errorf("the check ran %v on a manual import", f.checks)
	}
}

// Reading is once per import and bounded: a folder of 1200 files reads at most
// maxContentCheckTagReads tags, spread over the whole folder, never a file that
// is not audio.
func TestAudiobookContentCheck_ReadingIsBounded(t *testing.T) {
	f := newAudioFixture(t, "Chase", "Author Name")
	f.setMode(t, audioCheckKey, "block")
	var mu sync.Mutex
	var paths []string
	f.scanner.readTags = func(path string) (AudioTags, error) {
		mu.Lock()
		paths = append(paths, path)
		mu.Unlock()
		return AudioTags{Album: "Chase", Title: "x"}, nil
	}
	const total = 1200
	for i := 0; i < total; i++ {
		if err := os.WriteFile(filepath.Join(f.dir, fmt.Sprintf("track_%04d.mp3", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.dir, "cover.jpg"), []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.importDownload(t)

	if len(paths) != maxContentCheckTagReads {
		t.Errorf("read %d tags of %d files, want exactly the cap %d", len(paths), total, maxContentCheckTagReads)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Errorf("%s was read twice", p)
		}
		seen[p] = true
		if !strings.HasSuffix(p, ".mp3") {
			t.Errorf("read tags of %s, which is not an audio file", p)
		}
	}
	if len(paths) == 0 {
		t.Fatal("no tag was read at all")
	}
	sort.Strings(paths)
	if !strings.HasSuffix(paths[0], "track_0000.mp3") || !strings.HasSuffix(paths[len(paths)-1], "track_1199.mp3") {
		t.Errorf("the sample does not span the folder: first %s, last %s", paths[0], paths[len(paths)-1])
	}
	if dl, _ := f.reload(t); dl.Status != models.StateImported {
		t.Errorf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
}

// A wanted box set or omnibus is not compared, and no tag is read.
func TestAudiobookContentCheck_BoxSetBookIsNotChecked(t *testing.T) {
	f := newAudioFixture(t, "The Hobbit Omnibus", "J. R. R. Tolkien")
	f.setMode(t, audioCheckKey, "block")
	reads := f.countReads()
	f.writeAlbum(t, "The Hobbit", 10)
	f.writeAlbum(t, "The Fellowship of the Ring", 10)
	f.importDownload(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
	if reads.count() != 0 || len(f.checks) != 0 {
		t.Errorf("the check ran for a box set: %d reads, %v", reads.count(), f.checks)
	}
}

// The check is for downloads. A library scan and a manual import never run it.
func TestAudiobookContentCheck_NotRunByLibraryScanOrManualImport(t *testing.T) {
	f := newAudioFixture(t, "Chase", "Author Name")
	f.setMode(t, audioCheckKey, "block")
	reads := f.countReads()

	// A library scan over a folder of mixed tracks.
	lib := filepath.Join(f.library, "Author Name", "Mixed")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		album := []string{"Chase", "Corralled"}[i%2]
		if err := os.WriteFile(filepath.Join(lib, fmt.Sprintf("t%d.mp3", i)), buildID3v23Album("Chapter", album), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.scanner.ScanLibrary(context.Background())

	// A manual import of the same mix.
	f.writeAlbum(t, "Chase", 3)
	f.writeAlbum(t, "Corralled", 3)
	f.scanner.ImportFromPath(context.Background(), f.dl, f.dir, "")

	if reads.count() != 0 || len(f.checks) != 0 {
		t.Errorf("the content check ran outside the download path: %d reads, %v", reads.count(), f.checks)
	}

	// The same files arriving as a polled download are checked, once.
	f2 := newAudioFixture(t, "Chase", "Author Name")
	f2.setMode(t, audioCheckKey, "block")
	f2.writeAlbum(t, "Chase", 3)
	f2.writeAlbum(t, "Corralled", 3)
	f2.importDownload(t)
	if len(f2.checks) != 1 || f2.checks[0] != "audiobook" {
		t.Errorf("a polled download ran checks %v, want exactly one audiobook check", f2.checks)
	}
}

// File text is untrusted and the failure message goes to chat webhooks.
func TestAudiobookContentCheck_FileTextIsNeutralisedBeforeItIsSent(t *testing.T) {
	f := newAudioFixture(t, "Chase", "Author Name")
	f.setMode(t, audioCheckKey, "block")
	f.writeAlbum(t, "Chase", 2)
	f.writeAlbum(t, "@everyone <!channel> [click](evil)", 5)
	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q, want blocked", dl.Status)
	}
	call := f.notif.lookup("downloadFailed")
	if call == nil {
		t.Fatal("no downloadFailed webhook")
	}
	blob, _ := json.Marshal(call.payload)
	events, err := f.history.ListByType(context.Background(), models.HistoryEventImportFailed)
	if err != nil || len(events) == 0 {
		t.Fatalf("no importFailed event: %v", err)
	}
	for name, text := range map[string]string{"message": dl.ErrorMessage, "webhook": string(blob), "history": events[0].Data} {
		for _, bad := range []string{"@everyone", "<!channel>", "](evil)"} {
			if strings.Contains(text, bad) {
				t.Errorf("%s still contains %q: %s", name, bad, text)
			}
		}
	}
}

func TestAudiobookContentCheck_UnknownValueFallsBackToWarn(t *testing.T) {
	f := newAudioFixture(t, "Chase", "Author Name")
	f.setMode(t, audioCheckKey, "reject")
	f.writeAlbum(t, "Chase", 2)
	f.writeAlbum(t, "Corralled", 9)
	f.importDownload(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImported {
		t.Fatalf("a typo in the setting blocked an import: status %q", dl.Status)
	}
}
