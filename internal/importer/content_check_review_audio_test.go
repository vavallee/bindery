package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// Review round, audiobook side: the compilation skip, placeholder and secondary
// albums, the fraction guards, subtitle only albums, the wall clock budget and
// the pass note.

// A. Ordinary titles that use the words the skip used to match must be checked;
// real compilations must be skipped.
func TestBookIsCompilation_OrdinaryTitlesAreNotSkipped(t *testing.T) {
	for _, s := range []string{
		"The Best of Me", "Best of Friends", "Make the Best of It", "Complete Control", "The Complete Maus",
		"The Complete Idiot's Guide to Mythology", "A Collection of Curses", "The Memory Collection",
		"The Treasury of Deception", "Fahrenheit 451", "Catch 22", "The Hunger Games", "Dune", "Foundation",
		"Mistborn: The Final Empire", "Saga", "Series of Unfortunate Events",
	} {
		if bookIsCompilation(s) {
			t.Errorf("bookIsCompilation(%q) = true: an ordinary title would silently skip the check", s)
		}
	}
}

func TestBookIsCompilation_RealCompilationsAreSkipped(t *testing.T) {
	for _, s := range []string{
		"The Hunger Games Trilogy", "Mistborn: The Original Trilogy", "The Dark Tower Series", "The Expanse Duology",
		"Foundation Saga", "Sherlock Holmes: Four Novels", "Dune Chronicles 1-3", "Harry Potter 1-7",
		"Alex Cross Box Set", "The Hobbit Omnibus", "Dangerous Women: An Anthology", "Collected Works of Poe",
		"The Complete Works of Shakespeare", "The Complete Novels of Jane Austen", "Complete Short Stories of Hemingway",
		"Books 1-3", "The Complete Discworld Series", "3 Novels in 1", "The Quartet", "Boxed Set",
	} {
		if !bookIsCompilation(s) {
			t.Errorf("bookIsCompilation(%q) = false: a compilation would be compared file by file", s)
		}
	}
}

// B. Placeholder and secondary albums on two or more files must not flag a good
// folder.
func TestEvaluateAudiobookContent_PlaceholderAndSecondaryAlbumsAreNoTitle(t *testing.T) {
	withArtist := func(album, artist string, n int) []AudioTags {
		out := tracksWithAlbum(album, n)
		for i := range out {
			out[i].Artist = artist
		}
		return out
	}
	withComposer := func(album, composer string, n int) []AudioTags {
		out := tracksWithAlbum(album, n)
		for i := range out {
			out[i].Composer = composer
		}
		return out
	}
	cases := []struct {
		name string
		tags []AudioTags
	}{
		{"narrator as album, matching artist tag", join(tracksWithAlbum("Dune", 60), withArtist("Scott Brick", "Scott Brick", 2))},
		{"narrator as album, artist is the narrator in another case", join(tracksWithAlbum("Dune", 60), withArtist("scott brick", "Scott Brick", 3))},
		{"composer tag as album", join(tracksWithAlbum("Dune", 60), withComposer("Frank Herbert", "Frank Herbert", 3))},
		{"publisher as album", join(tracksWithAlbum("Dune", 60), tracksWithAlbum("Audible Studios", 2))},
		{"publisher list: Audible", join(tracksWithAlbum("Dune", 60), tracksWithAlbum("Audible", 3))},
		{"publisher list: Blackstone Audio", join(tracksWithAlbum("Dune", 60), tracksWithAlbum("Blackstone Audio", 3))},
		{"publisher list: Audiobook", join(tracksWithAlbum("Dune", 60), tracksWithAlbum("Audiobook", 3))},
		{"unknown album placeholder", join(tracksWithAlbum("Dune", 40), tracksWithAlbum("Unknown Album", 10))},
		{"unknown placeholder", join(tracksWithAlbum("Dune", 40), tracksWithAlbum("Unknown", 10))},
		{"release group url as album", join(tracksWithAlbum("Dune", 10), tracksWithAlbum("www.release-group.example", 40))},
		{"release group url with scheme", join(tracksWithAlbum("Dune", 10), tracksWithAlbum("Ripped by https://grp.example/x", 40))},
		{"release group domain as album", join(tracksWithAlbum("Dune", 10), tracksWithAlbum("audiobookbay.example.com", 40))},
		{"two intro files with the series name as album", join(tracksWithAlbum("Dune", 60), tracksWithAlbum("Dune Chronicles Sampler Series", 2))},
		{"no album, two files sharing a track title", join(tracksWithAlbum("Dune", 60), tracksWithTitle("Some Other Title", 2))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if r := evaluateAudiobookContent("Dune", c.tags); r.flagged {
				t.Errorf("flagged a good folder: %+v", r)
			}
		})
	}
}

// The fraction guard on rule (a): fewer than 5 percent of the tracks read do not
// flag, and the acceptance cases still do.
func TestEvaluateAudiobookContent_FractionGuard(t *testing.T) {
	// 2 of 41 is 4.9 percent: not flagged.
	if r := evaluateAudiobookContent("Chase", join(tracksWithAlbum("Chase", 39), tracksWithAlbum("Corralled", 2))); r.flagged {
		t.Errorf("2 of 41 files flagged the folder: %+v", r)
	}
	// 2 of 40 is exactly 5 percent: flagged.
	if r := evaluateAudiobookContent("Chase", join(tracksWithAlbum("Chase", 38), tracksWithAlbum("Corralled", 2))); !r.flagged {
		t.Errorf("2 of 40 files did not flag the folder: %+v", r)
	}
	// Acceptance case 3: 2 of 16 is 12.5 percent and must still flag.
	if r := evaluateAudiobookContent("The End", join(tracksWithAlbum("The End", 14), tracksWithAlbum("The Winter Sea", 2))); !r.flagged {
		t.Errorf("acceptance case 3 no longer flags: %+v", r)
	}
	// Two intro files named for the series, in front of the book: 2 of 62.
	if r := evaluateAudiobookContent("The Eye of the World", join(tracksWithAlbum("The Eye of the World", 60), tracksWithAlbum("The Wheel of Time", 2))); r.flagged {
		t.Errorf("two series intro files flagged the book: %+v", r)
	}
}

// C. Subtitle only albums.
func TestContentTitleSupports_SubtitleAsAnAcceptedForm(t *testing.T) {
	cases := []struct {
		wanted, file string
		reverse      bool
		want         bool
	}{
		{"Mistborn: The Final Empire", "The Final Empire", false, true},
		{"Mistborn: The Final Empire", "Mistborn: The Final Empire", false, true},
		{"Mistborn: The Final Empire", "Mistborn", false, true},
		{"The Lord of the Rings: The Fellowship of the Ring", "The Fellowship of the Ring", false, true},
		{"The Lord of the Rings: The Fellowship of the Ring", "The Fellowship of the Ring, Book One", false, true},
		{"Star Wars: A New Hope", "A New Hope", false, true},
		// A generic subtitle is not a title.
		{"Dominion of Blades: A LitRPG Adventure", "Other Series: A LitRPG Adventure", false, false},
		{"Gone Girl: A Novel", "The Novel", false, false},
		// A one word subtitle is not enough to stand for the book.
		{"Dune: Messiah", "Messiah", false, false},
		// Containment decisions are unchanged.
		{"Dune Messiah", "Dune", false, false},
		{"Dune", "Dune Messiah", false, true},
	}
	for _, c := range cases {
		if got := contentTitleSupports(c.wanted, c.file, c.reverse); got != c.want {
			t.Errorf("contentTitleSupports(%q, %q, %v) = %v, want %v", c.wanted, c.file, c.reverse, got, c.want)
		}
	}
}

func TestEvaluateAudiobookContent_SubtitleOnlyAlbumsAreTheBook(t *testing.T) {
	r := evaluateAudiobookContent("Mistborn: The Final Empire", join(
		tracksWithAlbum("Mistborn: The Final Empire", 30), tracksWithAlbum("The Final Empire", 30)))
	if r.flagged {
		t.Errorf("Mistborn split flagged: %+v", r)
	}
	r = evaluateAudiobookContent("The Lord of the Rings: The Fellowship of the Ring", join(
		tracksWithAlbum("The Fellowship of the Ring", 30), tracksWithAlbum("The Fellowship of the Ring, Book One", 30)))
	if r.flagged {
		t.Errorf("Fellowship split flagged: %+v", r)
	}
	// A different series with the same generic subtitle is still another work.
	other := join(tracksWithAlbum("Dominion of Blades: A LitRPG Adventure", 30), tracksWithAlbum("Other Series: A LitRPG Adventure", 10))
	if r := evaluateAudiobookContent("Dominion of Blades: A LitRPG Adventure", other); !r.flagged {
		t.Errorf("another series with the same subtitle was not flagged: %+v", r)
	}
}

// D. Rule (b) needs at least 2 files and 5 percent of the tracks.
func TestEvaluateAudiobookContent_VolumeConflictFloor(t *testing.T) {
	if r := evaluateAudiobookContent("Foo 1", join(tracksWithAlbum("Foo 1", 40), tracksWithAlbum("Foo 2", 2))); r.flagged {
		t.Errorf("2 bonus files of Foo 2 flagged 40 files of Foo 1: %+v", r)
	}
	if r := evaluateAudiobookContent("Foo 1", join(tracksWithAlbum("Foo 1", 40), tracksWithAlbum("Foo 1.5", 2))); r.flagged {
		t.Errorf("2 bonus files of Foo 1.5 flagged 40 files of Foo 1: %+v", r)
	}
	// 2 of 40 is 5 percent.
	if r := evaluateAudiobookContent("Foo 1", join(tracksWithAlbum("Foo 1", 38), tracksWithAlbum("Foo 2", 2))); !r.flagged {
		t.Errorf("2 of 40 files of another volume did not flag: %+v", r)
	}
	// Acceptance case 4b is unchanged.
	if r := evaluateAudiobookContent("NYPD Red 3", tracksWithAlbum("NYPD Red 4", 161)); !r.flagged {
		t.Errorf("acceptance case 4b no longer flags: %+v", r)
	}
}

// F. The retitled pass leaves a trace.
func TestEvaluateAudiobookContent_RetitledPassIsRecorded(t *testing.T) {
	r := evaluateAudiobookContent("The Firm", tracksWithAlbum("The Runaway Jury", 100))
	if r.flagged {
		t.Fatalf("flagged: %+v", r)
	}
	if r.retitled == nil || r.retitled.title != "The Runaway Jury" || r.retitled.count != 100 {
		t.Errorf("retitled = %+v, want The Runaway Jury x 100", r.retitled)
	}
	r = evaluateAudiobookContent("Chase", tracksWithAlbum("Corralled", 40))
	if r.retitled == nil || r.retitled.count != 40 {
		t.Errorf("Chase with 40 Corralled: retitled = %+v", r.retitled)
	}
	// Not recorded when some file supports the book, or for a clean folder.
	if r := evaluateAudiobookContent("Chase", join(tracksWithAlbum("Chase", 30), tracksWithAlbum("Corralled", 1))); r.retitled != nil {
		t.Errorf("retitled recorded beside matching files: %+v", r.retitled)
	}
	if r := evaluateAudiobookContent("Chase", tracksWithAlbum("Chase", 30)); r.retitled != nil {
		t.Errorf("retitled recorded for a clean folder: %+v", r.retitled)
	}
	// A flagged folder is not a retitled pass.
	if r := evaluateAudiobookContent("NYPD Red 3", tracksWithAlbum("NYPD Red 4", 161)); r.retitled != nil {
		t.Errorf("retitled recorded for a flagged folder: %+v", r.retitled)
	}
}

func TestAudiobookContentCheck_RetitledPassCarriesANoteInEveryMode(t *testing.T) {
	for _, mode := range []string{"warn", "block"} {
		t.Run(mode, func(t *testing.T) {
			f := newAudioFixture(t, "Chase", "Author Name")
			f.setMode(t, audioCheckKey, mode)
			f.writeAlbum(t, "Corralled", 12)
			f.importDownload(t)
			dl, _ := f.reload(t)
			if dl.Status != models.StateImported {
				t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
			}
			if f.blocklisted(t) {
				t.Error("a retitled pass was blocklisted")
			}
			cc, ok := f.eventData(t, models.HistoryEventBookImported)["contentCheck"].(map[string]any)
			if !ok {
				t.Fatalf("no contentCheck note on the import: %v", f.eventData(t, models.HistoryEventBookImported))
			}
			rt, _ := cc["retitled"].(map[string]any)
			if rt["title"] != "Corralled" || rt["files"] != float64(12) {
				t.Errorf("retitled = %v", cc["retitled"])
			}
			if mm, _ := cc["mismatches"].([]any); len(mm) != 0 {
				t.Errorf("a retitled pass listed mismatches: %v", mm)
			}
		})
	}
}

// E. The wall clock budget fails open and says so.
func TestAudiobookContentCheck_BudgetFailsOpenAndSaysSo(t *testing.T) {
	old := contentCheckBudget
	contentCheckBudget = 40 * time.Millisecond
	defer func() { contentCheckBudget = old }()

	f := newAudioFixture(t, "Chase", "Author Name")
	f.setMode(t, audioCheckKey, "block")
	var reads atomic.Int32
	f.scanner.readTags = func(path string) (AudioTags, error) {
		reads.Add(1)
		time.Sleep(15 * time.Millisecond)
		if strings.Contains(path, "other") {
			return AudioTags{Album: "Corralled"}, nil
		}
		return AudioTags{Album: "Chase"}, nil
	}
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("a_%03d.mp3", i)
		if i%2 == 0 {
			name = fmt.Sprintf("other_%03d.mp3", i)
		}
		if err := os.WriteFile(filepath.Join(f.dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("a cut short check blocked the import: status %q (%s)", dl.Status, dl.ErrorMessage)
	}
	if n := reads.Load(); n >= 60 {
		t.Errorf("read %d of 60 files, the budget did not stop the loop", n)
	}
	cc, ok := f.eventData(t, models.HistoryEventBookImported)["contentCheck"].(map[string]any)
	if !ok || cc["truncated"] != true {
		t.Errorf("contentCheck = %v, want truncated: true", cc)
	}
}

// A context that ended during the reads cannot flag, and so cannot run the block
// path's writes with a dead context.
func TestCheckAudiobookContent_CancelledContextNeverFlags(t *testing.T) {
	f := newAudioFixture(t, "Chase", "Author Name")
	f.setMode(t, audioCheckKey, "block")
	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int32
	f.scanner.readTags = func(string) (AudioTags, error) {
		if n.Add(1) == 30 {
			cancel()
		}
		if n.Load()%2 == 0 {
			return AudioTags{Album: "Corralled"}, nil
		}
		return AudioTags{Album: "Chase"}, nil
	}
	files := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		files = append(files, fmt.Sprintf("/x/%03d.mp3", i))
	}
	res := f.scanner.checkAudiobookContent(ctx, f.book, f.author, files)
	if res.flagged || res.blocks() {
		t.Errorf("a cancelled check flagged: %+v", res)
	}
}
