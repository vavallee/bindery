package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Third review round.

// 1. A release with more EPUBs than the cap is never judged on a prefix.
func TestEbookContentCheck_ReleaseOverTheCapIsNotJudgedOnAPrefix(t *testing.T) {
	f := newContentFixture(t, "The Racketeer", "John Grisham")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "seed.epub", "Recursion", "Blake Crouch")
	data, err := os.ReadFile(filepath.Join(f.dir, "seed.epub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.dir, "seed.epub")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if err := os.WriteFile(filepath.Join(f.dir, fmt.Sprintf("a_%02d.epub", i)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.writeEpubTitled(t, "z_racketeer.epub", "The Racketeer", "John Grisham")
	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status == models.StateImportBlocked {
		t.Fatalf("a release holding the right book was blocked: %s", dl.ErrorMessage)
	}
	if f.blocklisted(t) {
		t.Error("a release holding the right book was blocklisted")
	}
}

func TestCheckEbookContent_MoreThanTheCapFailsOpenAndSaysSo(t *testing.T) {
	f := newContentFixture(t, "The Racketeer", "John Grisham")
	f.setMode(t, ebookCheckKey, "block")
	files := make([]string, 0, 400)
	for i := 0; i < 400; i++ {
		files = append(files, fmt.Sprintf("/nowhere/book_%03d.epub", i))
	}
	res := f.scanner.checkEbookContent(context.Background(), f.book, f.author, files)
	if res.flagged || res.blocks() {
		t.Errorf("a pack of 400 EPUBs was judged: %+v", res)
	}
	if res.data["truncated"] != true {
		t.Errorf("the note does not say the check was skipped: %v", res.data)
	}
	// Exactly the cap is still judged (and flags when every one disagrees).
	f2 := newContentFixture(t, "The Racketeer", "John Grisham")
	f2.setMode(t, ebookCheckKey, "block")
	f2.writeEpubTitled(t, "seed.epub", "Recursion", "Blake Crouch")
	data, _ := os.ReadFile(filepath.Join(f2.dir, "seed.epub"))
	_ = os.Remove(filepath.Join(f2.dir, "seed.epub"))
	var paths []string
	for i := 0; i < maxContentCheckEpubReads; i++ {
		p := filepath.Join(f2.dir, fmt.Sprintf("b_%02d.epub", i))
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if res := f2.scanner.checkEbookContent(context.Background(), f2.book, f2.author, paths); !res.flagged {
		t.Errorf("exactly %d disagreeing EPUBs were not flagged: %+v", maxContentCheckEpubReads, res)
	}
}

// The boundary: exactly the cap is judged, one more is skipped and says so.
func TestCheckEbookContent_CapBoundary(t *testing.T) {
	write := func(f *contentFixture, n int) []string {
		f.writeEpubTitled(t, "seed.epub", "Recursion", "Blake Crouch")
		data, err := os.ReadFile(filepath.Join(f.dir, "seed.epub"))
		if err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(filepath.Join(f.dir, "seed.epub"))
		var paths []string
		for i := 0; i < n; i++ {
			p := filepath.Join(f.dir, fmt.Sprintf("c_%02d.epub", i))
			if err := os.WriteFile(p, data, 0o644); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, p)
		}
		return paths
	}
	f := newContentFixture(t, "The Racketeer", "John Grisham")
	f.setMode(t, ebookCheckKey, "block")
	res := f.scanner.checkEbookContent(context.Background(), f.book, f.author, write(f, maxContentCheckEpubReads))
	if !res.flagged || res.data["truncated"] != nil {
		t.Errorf("exactly %d EPUBs were not judged: %+v", maxContentCheckEpubReads, res)
	}

	f2 := newContentFixture(t, "The Racketeer", "John Grisham")
	f2.setMode(t, ebookCheckKey, "block")
	res = f2.scanner.checkEbookContent(context.Background(), f2.book, f2.author, write(f2, maxContentCheckEpubReads+1))
	if res.flagged || res.blocks() {
		t.Errorf("%d EPUBs were judged: %+v", maxContentCheckEpubReads+1, res)
	}
	if res.data["truncated"] != true || res.data["filesTotal"] != maxContentCheckEpubReads+1 {
		t.Errorf("note = %v, want truncated with filesTotal %d", res.data, maxContentCheckEpubReads+1)
	}
}

// 2. A non-Latin alias must not make an author unable to disagree.
func TestEbookAuthorMatches_NonLatinAliasDoesNotHideALatinCreator(t *testing.T) {
	names := []string{"Fyodor Dostoevsky", "Фёдор Достоевский"}
	if ebookAuthorMatches(names, "Stephen King") {
		t.Error("a Latin creator matched an author only because one alias is Cyrillic")
	}
	if !ebookAuthorMatches(names, "Fyodor Dostoevsky") {
		t.Error("the Latin name no longer matches")
	}
	if !ebookAuthorMatches(names, "Фёдор Достоевский") {
		t.Error("the Cyrillic alias no longer matches a Cyrillic creator")
	}
	if ebookAuthorMatches(names, "Лев Толстой") {
		t.Error("a different Cyrillic creator matched")
	}
	// A Cyrillic creator against an author with only Latin names cannot disagree.
	if !ebookAuthorMatches([]string{"Fyodor Dostoevsky"}, "Фёдор Достоевский") {
		t.Error("a Cyrillic creator against a Latin only author was read as a disagreement")
	}
	// A Latin creator against an author with only non-Latin names cannot disagree.
	if !ebookAuthorMatches([]string{"Фёдор Достоевский"}, "Stephen King") {
		t.Error("a Latin creator against a non-Latin only author was read as a disagreement")
	}
}

// 3. Real compilations the first strict list missed, and ordinary titles that
// must stay checked.
func TestBookIsCompilation_ReviewThreeLists(t *testing.T) {
	skipped := []string{
		"The Complete Sherlock Holmes", "The Short Stories of Ernest Hemingway", "Short Stories", "A Collection of Short Stories",
		"Collected Ghost Stories", "Collected Poems", "The Complete Chronicles of Conan", "The Complete Calvin and Hobbes",
		"Harry Potter Collection", "The Dune Saga Collection", "Treasury of Fairy Tales", "The Complete Tales of Poe",
		// Earlier requirements stay.
		"The Hunger Games Trilogy", "Dune Chronicles 1-3", "Harry Potter 1-7", "Sherlock Holmes: Four Novels",
		"The Dark Tower Series", "Foundation Saga", "The Expanse Duology", "Mistborn: The Original Trilogy",
	}
	for _, s := range skipped {
		if !bookIsCompilation(s) {
			t.Errorf("bookIsCompilation(%q) = false, want true", s)
		}
	}
	checked := []string{
		"The Best of Me", "Best of Friends", "Make the Best of It", "Complete Control", "The Complete Maus",
		"The Complete Idiot's Guide to Mythology", "A Collection of Curses", "The Memory Collection",
		"The Treasury of Deception", "Saga", "Series of Unfortunate Events",
	}
	for _, s := range checked {
		if bookIsCompilation(s) {
			t.Errorf("bookIsCompilation(%q) = true, an ordinary title would skip the check", s)
		}
	}
}

// "The Best of <Name>" is a compilation only when that name is the book's author.
func TestBookIsCompilationBy_BestOfNeedsTheAuthor(t *testing.T) {
	cases := []struct {
		title, author string
		want          bool
	}{
		{"The Best of Poe", "Edgar Allan Poe", true},
		{"Best of Hemingway", "Ernest Hemingway", true},
		{"The Best of Me", "Nicholas Sparks", false},
		{"Best of Friends", "Jane Doe", false},
		{"Make the Best of It", "Someone Else", false},
		{"The Best of Poe", "Someone Else", false},
		{"The Best of Me", "", false},
	}
	for _, c := range cases {
		if got := bookIsCompilationBy(c.title, c.author); got != c.want {
			t.Errorf("bookIsCompilationBy(%q, %q) = %v, want %v", c.title, c.author, got, c.want)
		}
	}
	// A per story folder for a skipped book imports, end to end.
	f := newAudioFixture(t, "The Best of Poe", "Edgar Allan Poe")
	f.setMode(t, audioCheckKey, "block")
	f.writeAlbum(t, "The Raven", 6)
	f.writeAlbum(t, "The Tell-Tale Heart", 5)
	f.writeAlbum(t, "The Cask of Amontillado", 4)
	f.importDownload(t)
	f.assertImportedSilently(t)
}

// 4. Part, episode, volume, book and chapter subtitles are labels, not titles.
func TestContentTitleSupports_PartSubtitlesAreNotTitles(t *testing.T) {
	cases := []struct {
		wanted, file string
		want         bool
	}{
		{"Dune: Part Two", "The Stand Part Two", false},
		{"Harry Potter and the Deathly Hallows: Part 2", "Gone With The Wind Part 2", false},
		{"The Wheel of Time: Book Three", "Another Series Book Three", false},
		{"Foo: Volume Two", "Bar Volume Two", false},
		{"Foo: Episode Four", "Bar Episode Four", false},
		{"Foo: Chapter Nine", "Bar Chapter Nine", false},
		{"Foo: Vol 3", "Bar Vol 3", false},
		// The book's own part is still itself.
		{"Dune: Part Two", "Dune: Part Two", true},
		{"Dune: Part Two", "Dune", true},
	}
	for _, c := range cases {
		if got := contentTitleSupports(c.wanted, c.file, false); got != c.want {
			t.Errorf("contentTitleSupports(%q, %q) = %v, want %v", c.wanted, c.file, got, c.want)
		}
	}
}

// 5. Placeholder lists are tightened.
func TestEbookAuthorMatches_RealNamesThatEndLikeAPublisherStayAuthors(t *testing.T) {
	names := []string{"J.K. Rowling"}
	for _, creator := range []string{"Stephen House", "Kim House", "Brian Press", "Jane Books", "Tom Media", "Ann Company", "Sam Studios"} {
		if ebookAuthorMatches(names, creator) {
			t.Errorf("creator %q was read as no creator", creator)
		}
	}
	// Publishers that are still recognised.
	for _, creator := range []string{
		"Penguin Group", "Scholastic Inc.", "Random House Publishing Group", "Hachette Book Group", "Acme Publishing",
		"Springer Verlag GmbH", "Foo Ltd", "Foo LLC", "Wiley Publishers", "Penguin Random House", "HarperCollins",
	} {
		if !ebookAuthorMatches(names, creator) {
			t.Errorf("publisher %q was read as an author", creator)
		}
	}
}

func TestEbookAuthorMatches_AtSignOnlyMakesAnEmailAddressAPlaceholder(t *testing.T) {
	if !ebookAuthorMatches([]string{"Jane Austen"}, "Jane Austen (jane@x.org)") {
		t.Error("an author with an address in brackets lost the author")
	}
	if ebookAuthorMatches([]string{"J.K. Rowling"}, "Jane Austen (jane@x.org)") {
		t.Error("an address in brackets made a different author a placeholder")
	}
	for _, creator := range []string{"scans@example.org", "<scans@example.org>", "(scans@example.org)", "[scans@example.org]"} {
		if !ebookAuthorMatches([]string{"J.K. Rowling"}, creator) {
			t.Errorf("email address %q was read as an author", creator)
		}
	}
}

func TestEvaluateAudiobookContent_DomainLikeWordsInTitlesStayTitles(t *testing.T) {
	for _, album := range []string{"Dot.com Secrets", "Ask Dr.Me", "Mr.Right and Other Stories"} {
		tags := join(tracksWithAlbum("Dune", 40), tracksWithAlbum(album, 10))
		if r := evaluateAudiobookContent("Dune", tags); !r.flagged {
			t.Errorf("album %q was read as a web address and ignored: %+v", album, r)
		}
	}
	// Real web addresses are still no title.
	for _, album := range []string{"www.release.example", "https://grp.example/x", "audiobookbay.example.com", "release-group.org", "Ripped by grp.example.net"} {
		tags := join(tracksWithAlbum("Dune", 40), tracksWithAlbum(album, 10))
		if r := evaluateAudiobookContent("Dune", tags); r.flagged {
			t.Errorf("album %q was counted as a title: %+v", album, r)
		}
	}
}

// 6. Boundaries that survived mutation.
func TestEvaluateAudiobookContent_VolumeConflictRuleBFloor(t *testing.T) {
	untagged := func(n int) []AudioTags { return make([]AudioTags, n) }
	// One conflict file of 19 never flags.
	if r := evaluateAudiobookContent("Foo 1", join(tracksWithAlbum("Foo 2", 1), untagged(18))); r.flagged {
		t.Errorf("1 conflict file of 19 flagged: %+v", r)
	}
	// Rule (b) alone (nothing supports the book, one other title): 2 of 40 is 5
	// percent and flags, 2 of 41 does not.
	if r := evaluateAudiobookContent("NYPD Red 3", join(tracksWithAlbum("NYPD Red 4", 2), untagged(38))); !r.flagged {
		t.Errorf("2 conflict files of 40 did not flag: %+v", r)
	}
	if r := evaluateAudiobookContent("NYPD Red 3", join(tracksWithAlbum("NYPD Red 4", 2), untagged(39))); r.flagged {
		t.Errorf("2 conflict files of 41 flagged: %+v", r)
	}
}

func TestEvaluateAudiobookContent_AudibleIsAPublisherButAudibleHellIsATitle(t *testing.T) {
	if r := evaluateAudiobookContent("Dune", join(tracksWithAlbum("Dune", 40), tracksWithAlbum("Audible", 10))); r.flagged {
		t.Errorf("the publisher Audible was counted as a title: %+v", r)
	}
	if r := evaluateAudiobookContent("Dune", join(tracksWithAlbum("Dune", 40), tracksWithAlbum("Audible Hell", 10))); !r.flagged {
		t.Errorf("Audible Hell was not counted as a title: %+v", r)
	}
}
