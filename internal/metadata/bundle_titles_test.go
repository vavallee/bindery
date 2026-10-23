package metadata

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestIsUnambiguousBundleTitle pins the tier boundary: only the keywords that
// name a physical multi-volume product may match, because this tier runs on
// every catalogue ingestion with no setting to turn it off. Anything that a
// real single book could plausibly say about itself belongs in the ambiguous
// tier and must return false here.
func TestIsUnambiguousBundleTitle(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		// Unambiguous tier: pruned for everyone.
		{"Expanse Hardcover Boxed Set : Leviathan Wakes, Caliban's War, Abaddon's Gate", true},
		{"Expanse Series, Collection Set of 3 Books. Leviathan Wakes, Caliban's War, Abaddon's Gate", true},
		{"Leviathan Falls - Carton of 10 Signed Copies", true},
		{"J.R.R. Tolkien-4 Vol. (Boxed)", true},
		{"Skyward Series 3 Books Set", true},
		{"The Lord of the Rings Boxset", true},
		{"Discworld 5 Volumes Set", true},
		{"Just Beyond (Set)", true},  // #3013, R. L. Stine, OL28394382W
		{"Goosebumps ( SET )", true}, // padding inside the parentheses
		{"Goosebumps (set)", true},   // case insensitive, like (Boxed)

		// Ambiguous tier: still gated behind SkipPartBooks, so false here.
		{"The Silmarillion Omnibus", false},
		{"Books 1-3", false},
		{"The Martian / Artemis / Project Hail Mary", false},
		{"Novels (Fellowship of the Ring / Hobbit)", false},
		{"Forever (Heart's Victory / Rules of the Game)", false},
		{"The Hobbit & The Lord of the Rings [collection/set]", false},
		{"A Collection of 12 Ghost Stories", false},

		// Real single books that must survive an unconditional filter.
		// "Trilogy" is deliberately in neither tier: far too many real books
		// are subtitled "Book One of the X Trilogy".
		{"Lord of the Rings Trilogy", false},
		{"The Broken Earth Trilogy", false},
		{"The Fellowship of the Ring", false},
		{"A Collection of Essays", false}, // Orwell, a single real volume.
		{"The Collected Stories of Eudora Welty", false},
		{"The Complete History of Middle-Earth", false},
		{"Boxed In", false},
		{"Set This House on Fire", false},

		{"Collected Short Stories [51 stories]", false},
		{"A Novel [Set in Wartime London]", false},
		{"Leviathan Wakes", false},
		{"Catch 22", false},
		{"Fahrenheit 451", false},
		{"", false},

		// Near misses for the "(Set)" branch (#3013). Only the whole
		// parenthesised word at the END of the title counts: a bare "set" is
		// an everyday word, a longer parenthetical that starts with it
		// describes a setting, and a "(Set)" with text after it is part of a
		// title rather than a product label. A trailing "!" or "." is not
		// accepted: no real bundle listing was observed with one, and each
		// extra allowance widens an unconditional filter.
		{"The Set", false},
		{"Set in Stone", false},
		{"Upset", false},
		{"Sunset Boulevard", false},
		{"Offset", false},
		{"Reset (Set in Paris)", false},
		{"Hotel (Settings)", false},
		{"Gold (Settled)", false},
		{"A Novel (Setup)", false},
		{"Foo (Set): A Novel", false},
		{"Foo (SET) 2", false},
		{"Ready (Set) Go", false},
		{"Ready (Set) Go (Set)", true}, // only the trailing one is the label
	}
	for _, c := range cases {
		if got := IsUnambiguousBundleTitle(c.title); got != c.want {
			t.Errorf("IsUnambiguousBundleTitle(%q) = %v, want %v", c.title, got, c.want)
		}
	}
}

// TestIsBundleTitle checks the full two-tier detector still answers exactly as
// the pre-split detector did, so moving the keyword lists into one place did
// not quietly change what SkipPartBooks screens out. The corpus mirrors
// TestIsPartBookTitle in internal/api, including its documented residual
// false positives.
func TestIsBundleTitle(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"Expanse Hardcover Boxed Set : Leviathan Wakes, Caliban's War, Abaddon's Gate", true},
		{"Expanse Series, Collection Set of 3 Books. Leviathan Wakes, Caliban's War, Abaddon's Gate", true},
		{"Leviathan Falls - Carton of 10 Signed Copies", true},
		{"The Silmarillion Omnibus", true},
		{"Books 1-3", true},
		{"The Martian / Artemis / Project Hail Mary", true},
		{"J.R.R. Tolkien-4 Vol. (Boxed)", true},
		{"Skyward Series 3 Books Set", true},
		{"The Hobbit & The Lord of the Rings [collection/set]", true},
		{"Novels (Fellowship of the Ring / Hobbit)", true},
		{"Forever (Heart's Victory / Rules of the Game)", true},
		// Documented residuals of the ambiguous tier, unchanged by the split.
		{"The Anti-Christ / Ecce Homo / Twilight of the Idols", true},
		{"The New Turing Omnibus", true},
		{"Thrown under the omnibus", true},
		// Real single books the ambiguous tier was narrowed to spare.
		{"The Omnibus of Crime", false},
		{"Omnibus Press Presents...", false},
		{"He/She/It", false},
		{"Rock/Paper/Scissors", false},
		{"Boxed In", false},
		{"Collected Short Stories [51 stories]", false},
		{"A Novel [Set in Wartime London]", false},
		{"Lord of the Rings Trilogy", false},
		{"Leviathan Wakes", false},
		{"Catch 22", false},
		{"Fahrenheit 451", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsBundleTitle(c.title); got != c.want {
			t.Errorf("IsBundleTitle(%q) = %v, want %v", c.title, got, c.want)
		}
	}
}

// TestBundleTitleMarkers3013 pins the ambiguous-tier markers added for #3013
// ("(series) N-N", a trailing "N Books", "N Name Books!") and, more
// importantly, the real titles they must not touch: this filter drops
// catalogue works, so a false positive silently hides a real book.
//
// Every row states both tiers. The ambiguous markers must be false in
// IsUnambiguousBundleTitle (they only run for profiles that enabled
// SkipPartBooks) and true in IsBundleTitle.
func TestBundleTitleMarkers3013(t *testing.T) {
	cases := []struct {
		title     string
		unambig   bool
		ambiguous bool // IsBundleTitle
	}{
		// Positives from the issue.
		{"Just Beyond (Set)", true, true},
		{"How To Train Your Dragon - 10 Books", false, true},
		{"Harry Potter (series) 1-3", false, true},
		{"Harry Potter (series) 1-4", false, true},
		{"2 Daniel Silva Books! 1) The Order of Death", false, true},

		// Shape variants of the same markers.
		{"Harry Potter (Series) 1 - 7", false, true},
		{"Harry Potter (SERIES) 1-3", false, true},
		{"Dragon Riders (3 Books)", false, true},
		{"Dragon Riders: 12 Books", false, true},
		{"5 Stephen King Books!", false, true},
		{"How To Train Your Dragon - 10 Books!", false, true},
		{"How To Train Your Dragon - 10 books.", false, true},
		{"Dragon Riders (3 Books)!", false, true},

		// Near misses that must survive both tiers.
		{"The Set", false, false},
		{"Set in Stone", false, false},
		{"Upset", false, false},
		{"Sunset Boulevard", false, false},
		{"Offset", false, false},
		// A parenthesised "(Series)" with no range is a label, not a count.
		{"Redwall (Series)", false, false},
		{"Harry Potter (series)", false, false},
		{"Mastering Python (Second Edition)", false, false},
		// Spelled numbers are never counted.
		{"The Seven Books of Magic", false, false},
		{"Ten Books of Wisdom", false, false},
		{"Book 3: The Third", false, false},
		// The trailing-count tolerance for "!" and "." must not start
		// matching mid-title.
		{"Oh No! 2 Books Are Missing", false, false},
		{"Wow. 3 Books. A Story", false, false},
		// Numbers as part of a real title.
		{"1984", false, false},
		{"11/22/63", false, false},
		{"Catch-22", false, false},
		{"Fahrenheit 451", false, false},
		{"The 5 Love Languages", false, false},
		{"2 Fast 2 Furious", false, false},
		{"The 7 Habits of Highly Effective People", false, false},
		// "N Books" mid-title is a nonfiction title about books, not a
		// bundle: neither marker anchors on the number unless "Books" ends
		// the title or carries the "!" of a retailer bundle listing.
		{"10 Books That Changed America", false, false},
		{"100 Books You Must Read Before You Die", false, false},
		{"The 100 Best Books Ever", false, false},
		// A count of one is a single book.
		{"Life in Letters - 1 Book", false, false},
	}
	for _, c := range cases {
		if got := IsUnambiguousBundleTitle(c.title); got != c.unambig {
			t.Errorf("IsUnambiguousBundleTitle(%q) = %v, want %v", c.title, got, c.unambig)
		}
		if got := IsBundleTitle(c.title); got != c.ambiguous {
			t.Errorf("IsBundleTitle(%q) = %v, want %v", c.title, got, c.ambiguous)
		}
	}
}

func booksWithTitles(titles ...string) []models.Book {
	books := make([]models.Book, len(titles))
	for i, title := range titles {
		books[i] = models.Book{ForeignID: "OL" + string(rune('A'+i)) + "W", Title: title}
	}
	return books
}

func titlesOf(books []models.Book) []string {
	out := make([]string, len(books))
	for i, b := range books {
		out[i] = b.Title
	}
	return out
}

// TestPruneAuthorWorkBundleTitles checks the prune drops box sets and keeps
// everything else, with no metadata profile involved at all. The survivors are
// the point of the test: a trilogy, ordinary novels, and titles that use
// "collection", "collected", "boxed" and "set" in a non-bundle sense.
func TestPruneAuthorWorkBundleTitles(t *testing.T) {
	books := booksWithTitles(
		"The Fellowship of the Ring",
		"Lord of the Rings Deluxe Illustrated Box Set",
		"Lord of the Rings Trilogy",
		"Expanse Series, Collection Set of 3 Books",
		"A Collection of Essays",
		"The Collected Stories of Eudora Welty",
		"Boxed In",
		"Set This House on Fire",
		"Leviathan Falls - Carton of 10 Signed Copies",
		"The Silmarillion Omnibus",
		"The Martian / Artemis / Project Hail Mary",
		"Skyward Series 3 Books Set",
	)
	want := []string{
		"The Fellowship of the Ring",
		"Lord of the Rings Trilogy",
		"A Collection of Essays",
		"The Collected Stories of Eudora Welty",
		"Boxed In",
		"Set This House on Fire",
		// Ambiguous tier: SkipPartBooks still owns these, so they survive the
		// unconditional prune and reach the author sync loop as before.
		"The Silmarillion Omnibus",
		"The Martian / Artemis / Project Hail Mary",
	}

	got := titlesOf(pruneAuthorWorkBundleTitles(books))
	if len(got) != len(want) {
		t.Fatalf("survivors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("survivor %d = %q, want %q (order must be preserved)", i, got[i], want[i])
		}
	}
}

// TestPruneAuthorWorkBundleTitles_TolkienCatalogue runs the exact titles #1780
// pulled from OpenLibrary's first 100 works for J. R. R. Tolkien
// (/authors/OL26320A/works.json), the catalogue an OpenLibrary-only install
// with no Hardcover token actually receives.
//
// Six of the ten go, which is the whole box-set group. The four that stay are
// there on purpose:
//
//   - the two "Complete History of Middle-earth" records, because "complete"
//     is not a bundle keyword in either tier ("The Complete Guide to ...",
//     "The Complete Robot") and nothing else in the title marks them;
//   - "Lord of the Rings Trilogy", because "trilogy" is deliberately untouched;
//   - "Lord of the Rings Movie Trilogy Colouring Book", which is not a bundle
//     at all but tie-in merchandise, and belongs to olNoiseTitleFragments if
//     it is worth filtering.
//
// The issue thread estimated eight of ten. That count holds only if the two
// "Complete History" rows are treated as caught, and no keyword in either tier
// matches them, so the real figure for this change is six.
func TestPruneAuthorWorkBundleTitles_TolkienCatalogue(t *testing.T) {
	books := booksWithTitles(
		"The Complete History of Middle-Earth",
		"The Complete History of Middle-earth, Vol. 2 (The History of Middle-earth)",
		"The Lord of the Rings 3 Books Box Set By J. R. R. Tolkien",
		"Tolkien Myths and Legends Box Set",
		"The Lord of the Rings Trilogy, 3 Volumes boxed Set",
		"Hobbit and the Lord of the Rings Illustrated by Alan Lee Box Set",
		"Lord of the Rings Deluxe Illustrated Box Set",
		"Lord of the Rings Trilogy",
		"Lord of the Rings Movie Trilogy Colouring Book",
		"Lord of the Rings Collector's Edition Box Set",
	)
	want := []string{
		"The Complete History of Middle-Earth",
		"The Complete History of Middle-earth, Vol. 2 (The History of Middle-earth)",
		"Lord of the Rings Trilogy",
		"Lord of the Rings Movie Trilogy Colouring Book",
	}

	got := titlesOf(pruneAuthorWorkBundleTitles(books))
	if len(got) != len(want) {
		t.Fatalf("survivors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("survivor %d = %q, want %q", i, got[i], want[i])
		}
	}

	// The four survivors are not saved by the profile setting either: the
	// full detector, which is what SkipPartBooks applies, leaves them too.
	for _, title := range want {
		if IsBundleTitle(title) {
			t.Errorf("IsBundleTitle(%q) = true, want false", title)
		}
	}
}
