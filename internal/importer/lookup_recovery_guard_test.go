package importer

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// A multi-disc audiobook grabbed with no book attached was bound to an
// unrelated book on retry: the recovery read a track file named
// "11. Track 11.mp3" as the title "11 Track 11", counted its "11" twice
// against a catalogue title holding "11", and took that lone, author-less
// match as confident, before the release title (which named the right book)
// was tried. Each of the three guards below stops that on its own.

// The reported shape end to end: track files that say nothing, a catalogue
// holding a book with an 11 in its title, and a release title naming the book.
func TestMatchBookForDownload_TrackNamesDoNotOutvoteTheReleaseTitle(t *testing.T) {
	s := releaseTitleFixture(t, func(ctx context.Context, books *db.BookRepo, authors *db.AuthorRepo) {
		seedBook(t, ctx, books, authors, "Ola Nordmann", "Havgapet Season 1 Episode 11")
		seedBook(t, ctx, books, authors, "Kari Nordmann", "Fjellvinden")
	})
	var files []string
	for _, disc := range []string{"Fjellvinden_Cd 01", "Fjellvinden_Cd 02"} {
		for _, track := range []string{"01. Track 1.mp3", "02. Track 2.mp3", "11. Track 11.mp3"} {
			files = append(files, "/dl/Kari Nordmann - Fjellvinden/"+disc+"/"+track)
		}
	}

	book, _ := s.matchBookForDownload(context.Background(), files, "Kari Nordmann - Fjellvinden")

	if book == nil || book.Title != "Fjellvinden" {
		t.Fatalf("matched %v, want Fjellvinden from the release title", book)
	}
}

// Guard 1: a word repeated in one title is one word of evidence.
func TestTitleMatch_RepeatedWordCountsOnce(t *testing.T) {
	for _, tc := range []struct {
		book, parsed string
		want         bool
	}{
		{"Havgapet Season 1 Episode 11", "11 Track 11", false},
		{"Fjellvinden Volume 7", "7 Spor 7", false},
		// Still matching: an exact title, and two distinct shared words.
		{"Nord Nord", "Nord Nord", true},
		{"Fjellvinden og Havgapet", "Havgapet Fjellvinden", true},
	} {
		if got := titleMatch(tc.book, tc.parsed); got != tc.want {
			t.Errorf("titleMatch(%q, %q) = %v, want %v", tc.book, tc.parsed, got, tc.want)
		}
	}
}

// Guard 2: nobody reviews this match, so a lone loose title match with no
// author is not used; the release title decides instead.
func TestMatchBookForDownload_LooseFilenameMatchNeedsCorroboration(t *testing.T) {
	s := releaseTitleFixture(t, func(ctx context.Context, books *db.BookRepo, authors *db.AuthorRepo) {
		seedBook(t, ctx, books, authors, "Ola Nordmann", "Fjellvinden og Havgapet Saga")
	})
	// Not a readable EPUB, so tier 1 has nothing; the name shares two words
	// with the catalogue title but is not it, and names no author.
	files := []string{"/dl/Havgapet Fjellvinden.epub"}

	if book, _ := s.matchBookForDownload(context.Background(), files, "Kari Nordmann - Noe annet"); book != nil {
		t.Fatalf("bound the download to %q on an uncorroborated filename match", book.Title)
	}
	// Corroborated by the author, the same loose title binds.
	files = []string{"/dl/Havgapet Fjellvinden - Ola Nordmann.epub"}
	if book, _ := s.matchBookForDownload(context.Background(), files, ""); book == nil {
		t.Fatal("a filename match the author corroborates was not used")
	}
}

// Guard 3: the tracks of a multi-file audiobook are not titles, even when one
// happens to equal a catalogue title exactly. A lone audio file still counts.
func TestMatchBookForDownload_SkipsTrackNamesButNotALoneAudioFile(t *testing.T) {
	s := releaseTitleFixture(t, func(ctx context.Context, books *db.BookRepo, authors *db.AuthorRepo) {
		seedBook(t, ctx, books, authors, "Ola Nordmann", "Intro")
		seedBook(t, ctx, books, authors, "Kari Nordmann", "Fjellvinden")
	})
	tracks := []string{"/dl/Fjellvinden/Intro.mp3", "/dl/Fjellvinden/Kapittel 1.mp3"}
	if book, _ := s.matchBookForDownload(context.Background(), tracks, "Kari Nordmann - Fjellvinden"); book == nil || book.Title != "Fjellvinden" {
		t.Fatalf("matched %v, want Fjellvinden from the release title, not a track named Intro", book)
	}
	lone := []string{"/dl/Fjellvinden - Kari Nordmann.m4b"}
	if book, _ := s.matchBookForDownload(context.Background(), lone, ""); book == nil || book.Title != "Fjellvinden" {
		t.Fatalf("matched %v, want Fjellvinden from the single audio file's name", book)
	}
}
