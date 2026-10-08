package importer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestLibrarySnapshot_ExactTitleWinsRegardlessOfWalkOrder pins #2941 on the
// add path: FindExisting used to answer with the first file whose title
// cleared titleWordsMatch, so "Dune" took "Dune Messiah" whenever the walk
// reached it first. The book folders are named only to fix the walk order;
// both orders must land on the exact title.
func TestLibrarySnapshot_ExactTitleWinsRegardlessOfWalkOrder(t *testing.T) {
	for name, folders := range map[string][2]string{
		"exact walked first":  {"a", "b"},
		"exact walked second": {"b", "a"},
	} {
		t.Run(name, func(t *testing.T) {
			libDir := t.TempDir()
			exact := filepath.Join(libDir, "Frank Herbert", folders[0], "Dune - Frank Herbert.epub")
			other := filepath.Join(libDir, "Frank Herbert", folders[1], "Dune Messiah - Frank Herbert.epub")
			writeFile(t, exact)
			writeFile(t, other)

			snap := NewLibrarySnapshot(libDir, "")
			if got := snap.FindExisting(context.Background(), "Dune", "Frank Herbert", models.MediaTypeEbook); got != exact {
				t.Errorf("FindExisting(Dune) = %q, want the exact title %q", got, exact)
			}
			if got := snap.FindExisting(context.Background(), "Dune Messiah", "Frank Herbert", models.MediaTypeEbook); got != other {
				t.Errorf("FindExisting(Dune Messiah) = %q, want %q", got, other)
			}
		})
	}
}

// TestLibrarySnapshot_AmbiguousTitlesBindNothing: two files whose titles both
// clear the match and score within the margin of each other are not the
// wanted book's to choose between. The book stays Wanted and searchable
// instead of taking whichever file the walk yields first (#2941).
func TestLibrarySnapshot_AmbiguousTitlesBindNothing(t *testing.T) {
	libDir := t.TempDir()
	author := filepath.Join(libDir, "J. K. Rowling")
	writeFile(t, filepath.Join(author, "Harry Potter en de Steen der Wijzen (1997)", "Harry Potter en de Steen der Wijzen - J. K. Rowling.epub"))
	writeFile(t, filepath.Join(author, "Harry Potter en het vervloekte kind (2016)", "Harry Potter en het vervloekte kind - J. K. Rowling.epub"))

	snap := NewLibrarySnapshot(libDir, "")
	if got := snap.FindExisting(context.Background(), "Harry Potter", "J. K. Rowling", models.MediaTypeEbook); got != "" {
		t.Errorf("FindExisting(Harry Potter) = %q, want no match between two equally partial titles", got)
	}
}

// TestLibrarySnapshot_RivalBookKeepsItsExactFile is the Harry Potter pair
// from #2941 as the add author path sees it: one untracked file, titled
// exactly as one of the author's books, and a second book whose shorter
// title also clears the word match. The file belongs to the exact title, so
// the shorter book must not take it, and the exact book still must.
func TestLibrarySnapshot_RivalBookKeepsItsExactFile(t *testing.T) {
	libDir := t.TempDir()
	dutch := filepath.Join(libDir, "J. K. Rowling", "Harry Potter en het vervloekte kind (2016)", "Harry Potter en het vervloekte kind - J. K. Rowling.epub")
	writeFile(t, dutch)
	const short, exact = "Harry Potter", "Harry Potter en het vervloekte kind"

	snap := NewLibrarySnapshot(libDir, "")
	if got := snap.FindExistingAmong(context.Background(), short, "J. K. Rowling", models.MediaTypeBoth, []string{exact}); got != "" {
		t.Errorf("FindExistingAmong(%q) = %q, want no match: the file is %q's", short, got, exact)
	}
	if got := snap.FindExistingAmong(context.Background(), exact, "J. K. Rowling", models.MediaTypeBoth, []string{short}); got != dutch {
		t.Errorf("FindExistingAmong(%q) = %q, want %q", exact, got, dutch)
	}
	// A rival that only repeats the wanted title is the same title, not a
	// competitor: a duplicate catalogue row must not stop the bind.
	if got := snap.FindExistingAmong(context.Background(), exact, "J. K. Rowling", models.MediaTypeBoth, []string{exact}); got != dutch {
		t.Errorf("duplicate title rival: got %q, want %q", got, dutch)
	}
}

// TestLibrarySnapshot_RivalCheckLeavesNumberedFolderAlone: in a Libation
// layout every volume's file carries the unnumbered series title and only the
// folder holds the number (#2810). The unnumbered first volume is then an
// exact title match for every volume's file, so letting it compete would cost
// volume 2 its own folder. A numbered folder has already settled the volume.
func TestLibrarySnapshot_RivalCheckLeavesNumberedFolderAlone(t *testing.T) {
	abDir := t.TempDir()
	author := filepath.Join(abDir, "TheFirstDefier")
	vol1 := filepath.Join(author, "Defiance of the Fall 01", "Defiance of the Fall_B094JZMCJX_LC_128_44100_Stereo.m4b")
	vol2 := filepath.Join(author, "Defiance of the Fall 02", "Defiance of the Fall_B099SH77S4_LC_64_22050_Stereo.m4b")
	writeFile(t, vol1)
	writeFile(t, vol2)

	snap := NewLibrarySnapshot("", abDir)
	if got := snap.FindExistingAmong(context.Background(), "Defiance of the Fall 2", "TheFirstDefier", models.MediaTypeAudiobook, []string{"Defiance of the Fall"}); got != vol2 {
		t.Errorf("volume 2 = %q, want %q", got, vol2)
	}
	if got := snap.FindExistingAmong(context.Background(), "Defiance of the Fall", "TheFirstDefier", models.MediaTypeAudiobook, []string{"Defiance of the Fall 2"}); got != vol1 {
		t.Errorf("volume 1 = %q, want %q", got, vol1)
	}
}

// TestLibrarySnapshot_SameBookFilesAreNotRivals: ranking compares different
// titles. An audiobook's numbered tracks, and one title in two formats, are
// one book and must still bind rather than read as an ambiguous pair.
func TestLibrarySnapshot_SameBookFilesAreNotRivals(t *testing.T) {
	libDir := t.TempDir()
	dir := filepath.Join(libDir, "Jane Doe", "Plain Book")
	track1 := filepath.Join(dir, "Plain Book 01.mp3")
	writeFile(t, track1)
	writeFile(t, filepath.Join(dir, "Plain Book 02.mp3"))
	writeFile(t, filepath.Join(dir, "Plain Book 03.mp3"))
	epub := filepath.Join(libDir, "Jane Doe", "Other Book", "Other Book - Jane Doe.epub")
	writeFile(t, epub)
	writeFile(t, filepath.Join(libDir, "Jane Doe", "Other Book", "Other Book - Jane Doe.mobi"))

	snap := NewLibrarySnapshot(libDir, "")
	if got := snap.FindExisting(context.Background(), "Plain Book", "Jane Doe", models.MediaTypeAudiobook); got != track1 {
		t.Errorf("tracks: got %q, want the first track %q", got, track1)
	}
	if got := snap.FindExisting(context.Background(), "Other Book", "Jane Doe", models.MediaTypeEbook); got != epub {
		t.Errorf("formats: got %q, want %q", got, epub)
	}
}

// TestLibrarySnapshot_PartialRivalDoesNotWithdrawFile: a rival title that only
// shares words with the file is no evidence the file is that book's. A scored
// rival rule withdrew every one of these, so an owned book was downloaded
// again; only an exact rival title may withdraw a file (#2941 review).
func TestLibrarySnapshot_PartialRivalDoesNotWithdrawFile(t *testing.T) {
	for _, tc := range []struct {
		name, author, file, wanted string
		rivals                     []string
	}{
		{"subtitle in the file name", "Andy Weir", "Project Hail Mary A Novel - Andy Weir.epub", "Project Hail Mary",
			[]string{"Proyecto Hail Mary"}},
		{"subtitle in the wanted title", "Andy Weir", "Project Hail Mary.epub", "Project Hail Mary: A Novel",
			[]string{"Proyecto Hail Mary"}},
		{"series prefix in the file name", "Patrick Rothfuss", "The Kingkiller Chronicle 1 The Name of the Wind.epub", "The Name of the Wind",
			[]string{"The Name of the Wind: 10th Anniversary Deluxe Edition"}},
		{"series name as the file name", "Brandon Sanderson", "Mistborn.epub", "Mistborn: The Final Empire",
			[]string{"Mistborn: The Well of Ascension", "Mistborn: The Hero of Ages", "Mistborn: Secret History"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			libDir := t.TempDir()
			path := filepath.Join(libDir, tc.author, tc.file)
			writeFile(t, path)

			snap := NewLibrarySnapshot(libDir, "")
			if got := snap.FindExisting(context.Background(), tc.wanted, tc.author, models.MediaTypeEbook); got != path {
				t.Fatalf("FindExisting(%q) = %q, want %q", tc.wanted, got, path)
			}
			if got := snap.FindExistingAmong(context.Background(), tc.wanted, tc.author, models.MediaTypeEbook, tc.rivals); got != path {
				t.Errorf("FindExistingAmong(%q, %q) = %q, want %q", tc.wanted, tc.rivals, got, path)
			}
		})
	}
}

// TestLibrarySnapshot_GroupAnswersWithItsBestMember: files whose titles differ
// only in digits group as one title, but the group must answer with its best
// member. "Dune 2.epub" walks before "Dune.epub", and "Dune" must still get
// its own file.
func TestLibrarySnapshot_GroupAnswersWithItsBestMember(t *testing.T) {
	libDir := t.TempDir()
	dir := filepath.Join(libDir, "Frank Herbert")
	writeFile(t, filepath.Join(dir, "Dune 2.epub"))
	exact := filepath.Join(dir, "Dune.epub")
	writeFile(t, exact)

	snap := NewLibrarySnapshot(libDir, "")
	if got := snap.FindExisting(context.Background(), "Dune", "Frank Herbert", models.MediaTypeEbook); got != exact {
		t.Errorf("FindExisting(Dune) = %q, want %q", got, exact)
	}
}

// TestLibrarySnapshot_RivalFileStepsAsideForNextBest: a file titled exactly as
// another of the author's books is that book's, so it must leave the ranking
// before the wanted title's file is chosen, not withdraw the answer after.
// Withdrawing afterwards left "The Way of Kings" with nothing although its
// own file was on disk.
func TestLibrarySnapshot_RivalFileStepsAsideForNextBest(t *testing.T) {
	for _, tc := range []struct {
		name, author, wanted, own, rivalFile, rival string
	}{
		{"Way of Kings", "Brandon Sanderson", "The Way of Kings",
			"Stormlight Archive The Way of Kings - Brandon Sanderson.epub",
			"The Way of Kings Prime - Brandon Sanderson.epub", "The Way of Kings Prime"},
		{"Dune", "Frank Herbert", "Dune",
			"Dune_ Deluxe Edition - Frank Herbert.epub",
			"Dune Messiah - Frank Herbert.epub", "Dune Messiah"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			libDir := t.TempDir()
			own := filepath.Join(libDir, tc.author, tc.own)
			writeFile(t, own)
			writeFile(t, filepath.Join(libDir, tc.author, tc.rivalFile))

			snap := NewLibrarySnapshot(libDir, "")
			if got := snap.FindExistingAmong(context.Background(), tc.wanted, tc.author, models.MediaTypeEbook, []string{tc.rival}); got != own {
				t.Errorf("FindExistingAmong(%q, [%q]) = %q, want %q", tc.wanted, tc.rival, got, own)
			}
		})
	}
}
