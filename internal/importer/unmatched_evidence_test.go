package importer

import (
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// tracks builds n audiobook members named by name(i), with the given tags.
func tracks(n int, name func(i int) string, tagAuthor, tagTitle, tagAlbum string) []unmatchedScanFile {
	out := make([]unmatchedScanFile, n)
	for i := range out {
		out[i] = unmatchedScanFile{
			path: "/audio/Folder Author/Book/" + name(i+1), format: models.MediaTypeAudiobook,
			tags: AudioTags{Artist: tagAuthor, Title: tagTitle, Album: tagAlbum},
		}
	}
	return out
}

func TestEvidenceFor(t *testing.T) {
	evans := func(i int) string { return fmt.Sprintf("Katy Evans - Tycoon %d-7.mp3", i) }
	cases := []struct {
		name        string
		members     []unmatchedScanFile
		folder      string
		layoutTitle string
		want        unitEvidence
	}{
		{"#2942 tags and file names", tracks(7, evans, "Katy Evans", "Katy Evans - Tycoon", "Tycoon"),
			"James Patterson", "$10,000,000 Marriage Proposition", unitEvidence{"Katy Evans", "Tycoon", "James Patterson"}},
		{"#2942 file names only", tracks(7, evans, "", "", ""),
			"James Patterson", "$10,000,000 Marriage Proposition", unitEvidence{"Katy Evans", "Tycoon", "James Patterson"}},
		{"title tag carrying the author, no album", tracks(3, func(i int) string { return fmt.Sprintf("%02d.mp3", i) }, "Katy Evans", "Katy Evans - Tycoon", ""),
			"James Patterson", "Something Else", unitEvidence{"Katy Evans", "Tycoon", "James Patterson"}},
		{"tag author that is the folder's", tracks(7, evans, "James Patterson", "", "Tycoon"),
			"James Patterson", "", unitEvidence{}},
		{"comma inverted folder", tracks(3, evans, "Katy Evans", "", ""),
			"Evans, Katy", "", unitEvidence{}},
		{"contributor list naming the folder author", tracks(3, evans, "James Patterson, Peter Hermes - narrator", "", ""),
			"James Patterson", "", unitEvidence{}},
		{"no author folder", tracks(7, evans, "Katy Evans", "", "Tycoon"),
			"", "", unitEvidence{}},
		{"no evidence at all", tracks(3, func(i int) string { return fmt.Sprintf("%02d.mp3", i) }, "", "", ""),
			"James Patterson", "Katt vs. Dogg", unitEvidence{}},
		// "<Series> - <Title> NN" has the author pattern's shape. The title
		// side naming the folder's own book is what gives it away.
		{"series prefix naming the folder's book", tracks(3, func(i int) string { return fmt.Sprintf("Stormlight Archive - The Way of Kings %02d.mp3", i) }, "", "", ""),
			"Brandon Sanderson", "The Way of Kings", unitEvidence{}},
		{"book name before a chapter", tracks(3, func(i int) string { return fmt.Sprintf("Elantris - Chapter %02d.mp3", i) }, "", "", ""),
			"Brandon Sanderson", "Elantris", unitEvidence{}},
		{"book name before bare track numbers", tracks(3, func(i int) string { return fmt.Sprintf("Mistborn - %02d.mp3", i) }, "", "", ""),
			"Brandon Sanderson", "The Final Empire", unitEvidence{}},
		{"series with a stopword", tracks(3, func(i int) string { return fmt.Sprintf("Wheel of Time - Eye of the World %02d.mp3", i) }, "", "", ""),
			"Robert Jordan", "Something Else", unitEvidence{}},
		{"numbered left side", tracks(3, func(i int) string { return fmt.Sprintf("Book 1 - Tycoon %02d.mp3", i) }, "", "", ""),
			"James Patterson", "Something Else", unitEvidence{}},
		{"file names that disagree", append(tracks(2, evans, "", "", ""), tracks(1, func(int) string { return "Other Name - Tycoon 3-7.mp3" }, "", "", "")...),
			"James Patterson", "Something Else", unitEvidence{}},
		{"a single file", tracks(1, evans, "", "", ""),
			"James Patterson", "Something Else", unitEvidence{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := evidenceFor(c.members, c.folder, c.layoutTitle); got != c.want {
				t.Errorf("evidenceFor = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestFilenameAuthorTitle_TrimsTrackMarkers(t *testing.T) {
	for _, pattern := range []string{"Katy Evans - Tycoon Part %d", "Katy Evans - Tycoon - %02d", "Katy Evans - Tycoon (%d of 7)", "Katy Evans – Tycoon %d-7"} {
		members := tracks(3, func(i int) string { return fmt.Sprintf(pattern, i) + ".mp3" }, "", "", "")
		author, title := filenameAuthorTitle(members, "")
		if author != "Katy Evans" || title != "Tycoon" {
			t.Errorf("%q: got %q by %q, want Tycoon by Katy Evans", pattern, title, author)
		}
	}
}

func TestAuthorEvidenceConflict(t *testing.T) {
	cases := []struct {
		files, folder string
		want          bool
	}{
		{"Katy Evans", "James Patterson", true},
		{"Katy Evans", "", false},
		{"", "James Patterson", false},
		{"James Patterson", "James Patterson", false},
		{"Patterson, James", "James Patterson", false},
		{"Tolkien", "J.R.R. Tolkien", false},
		{"J.R.R. Tolkien", "Tolkien", false},
		{"Bill Clinton, James Patterson", "James Patterson", false},
		{"Jane Doe", "Jane Smith", true},
		{"Various Artists", "James Patterson", false},
		{"[Unknown]", "James Patterson", false},
		// Diacritics transliterated to ASCII are the same name to the
		// author matcher.
		{"Jo Nesbo", "Jo Nesbø", false},
		{"Heinrich Boell", "Heinrich Böll", false},
	}
	for _, c := range cases {
		if got := authorEvidenceConflict(c.files, c.folder); got != c.want {
			t.Errorf("authorEvidenceConflict(%q, %q) = %v, want %v", c.files, c.folder, got, c.want)
		}
	}
}

// TestIsPlaceholderAuthor: the credits rips put in an author tag that name
// nobody, in the spellings they actually arrive in. Trailing punctuation,
// "(s)", a hyphenated "Full-Cast" and a run together "HarperAudio" used to
// slip past the list and read as an author, raising a conflict with the
// folder and an Add author for "Brilliance Audio" (#2942).
func TestIsPlaceholderAuthor(t *testing.T) {
	for _, s := range []string{
		"Various Artists", "Various Artists.", "Various Authors;", "  various   authors  ",
		"[Unknown]", "Unknown Author", "Unknown Author(s)", "Unknown Authors", "Author Unknown",
		"Full Cast", "Full-Cast", "full cast.", "A Full Cast", "Anon.", "Anon", "Anonymous",
		"V.A.", "V/A", "VA", "N/A",
		"Brilliance Audio", "Recorded Books", "Audible Studios", "Tantor Audio",
		"Blackstone Audio", "Penguin Audio", "HarperAudio", "Harper Audio",
		"Macmillan Audio", "Random House Audio", "Podium Audio",
	} {
		if !isPlaceholderAuthor(s) {
			t.Errorf("isPlaceholderAuthor(%q) = false, want true", s)
		}
	}
	// Real names, including ones built from initials, stay authors.
	for _, s := range []string{
		"Katy Evans", "James Patterson", "V. E. Schwab", "N.K. Jemisin", "Anne Rice",
		"Cast", "Unknown Soldier", "Penguin", "Harper Lee",
	} {
		if isPlaceholderAuthor(s) {
			t.Errorf("isPlaceholderAuthor(%q) = true, want false", s)
		}
	}
	// End to end: a placeholder tag beside the folder author is no conflict.
	if authorEvidenceConflict("Brilliance Audio", "James Patterson") {
		t.Error(`"Brilliance Audio" raised an author conflict with the folder`)
	}
}

// withTags gives every member the same author tags.
func withTags(members []unmatchedScanFile, artist, albumArtist, composer, album string) []unmatchedScanFile {
	for i := range members {
		members[i].tags = AudioTags{Artist: artist, AlbumArtist: albumArtist, Composer: composer, Album: album}
	}
	return members
}

// TestEvidenceFor_AuthorTags: Artist, Album Artist and Composer are all
// witnesses. One agreeing with the folder settles it; when none does, Album
// Artist is the author named. Placeholders name nobody.
func TestEvidenceFor_AuthorTags(t *testing.T) {
	plain := func(i int) string { return fmt.Sprintf("%02d.mp3", i) }
	cases := []struct {
		name                             string
		artist, albumArtist, composer    string
		album, folder, layoutTitle, want string
	}{
		{"narrator in Artist, author in Album Artist", "Scott Brick", "James Patterson", "", "Kiss the Girls", "James Patterson", "Kiss the Girls", ""},
		{"narrator in Artist, author in Composer", "Scott Brick", "", "James Patterson", "Tycoon", "James Patterson", "Something Else", ""},
		{"narrator only, album naming the folder's book", "Ray Porter", "", "", "Project Hail Mary", "Andy Weir", "Project Hail Mary", ""},
		{"Album Artist preferred when none agrees", "Sebastian York", "Katy Evans", "", "Tycoon", "James Patterson", "Something Else", "Katy Evans"},
		{"placeholder Album Artist skipped", "Katy Evans", "Various Artists", "", "Tycoon", "James Patterson", "Something Else", "Katy Evans"},
		{"only placeholders", "Unknown Artist", "Various Authors", "Full Cast", "Tycoon", "James Patterson", "Something Else", ""},
		{"Audible Studios", "Audible Studios", "", "", "Tycoon", "James Patterson", "Something Else", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			members := withTags(tracks(3, plain, "", "", ""), c.artist, c.albumArtist, c.composer, c.album)
			if got := evidenceFor(members, c.folder, c.layoutTitle).author; got != c.want {
				t.Errorf("evidence author = %q, want %q", got, c.want)
			}
		})
	}
}
