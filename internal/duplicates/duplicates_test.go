package duplicates

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func book(title string) models.Book {
	return models.Book{Title: title}
}

// TestAggressiveTitleKey covers the normalizer contract: everything a human
// would treat as "the same spelling" collapses, and nothing more is lost than
// punctuation, case, Unicode form, and the '&' spelling decision.
func TestAggressiveTitleKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "The Martian", "themartian"},
		{"case-insensitive", "THE MARTIAN", "themartian"},
		{"punctuation-drops", "Mistborn: The Final Empire", "mistbornthefinalempire"},
		{"punctuation-dashes", "Mistborn - The Final Empire", "mistbornthefinalempire"},
		{"ampersand-expands", "Foundation & Empire", "foundationandempire"},
		{"ampersand-vs-and", "Foundation and Empire", "foundationandempire"},
		{"apostrophe-deleted", "The Cat's Meow", "thecatsmeow"},
		{"umlaut-expanded", "Grüße aus dem Weltall", "gruesseausdemweltall"},
		{"digits-kept", "Star Wars 7", "starwars7"},
		{"trailing-punctuation", "Dune, Part Two!", "duneparttwo"},
		{"blank", "", ""},
		{"only-punctuation", "!!! ???", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AggressiveTitleKey(tc.in); got != tc.want {
				t.Errorf("AggressiveTitleKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestAggressiveTitleKeyNFC pins the #1642 failure mode at the normalizer:
// decomposed (NFD) input must fold to the same key as composed (NFC) input,
// or the same title meets itself as two strings.
func TestAggressiveTitleKeyNFC(t *testing.T) {
	// "Café" composed (é is U+00E9) vs decomposed (e + U+0301 combining acute).
	composed := "Café Society"
	decomposed := "Caf\u0065\u0301 Society"
	gotC := AggressiveTitleKey(composed)
	gotD := AggressiveTitleKey(decomposed)
	if gotC != gotD {
		t.Errorf("NFC %q -> %q but NFD %q -> %q; keys must match", composed, gotC, decomposed, gotD)
	}
	// é is a letter, so the key keeps it (same alphabet as the dedup fold);
	// the point of this test is composed == decomposed, not ASCII-isation.
	if gotC != "caf\u00e9society" {
		t.Errorf("AggressiveTitleKey(%q) = %q, want caf\u00e9society", composed, gotC)
	}
}

// TestArticleKey pins the article-strip semantics, including the whole-word
// guard that keeps "Theatre of Blood" from collapsing because "Theatre"
// merely begins with "the".
func TestArticleKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"leading-the", "The Martian", "martian"},
		{"leading-a", "A Game of Thrones", "gameofthrones"},
		{"leading-an", "An Ubik", "ubik"},
		{"no-article", "Martian", "martian"},
		{"theatre-trap", "Theatre of Blood", "theatreofblood"},
		{"there-trap", "There Will Come Soft Rains", "therewillcomesoftrains"},
		{"german-der", "Der Name des Windes", "namedeswindes"},
		{"french-le", "Le Petit Prince", "petitprince"},
		{"spanish-el", "El Nombre del Viento", "nombredelviento"},
		{"dutch-het", "Het Getuigenis", "getuigenis"},
		{"single-article-survives", "A", "a"},
		{"single-article-die", "Die", "die"},
		{"internal-untouched", "The Eye of the World", "eyeoftheworld"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ArticleKey(tc.in); got != tc.want {
				t.Errorf("ArticleKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEditionKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"unabridged-trailing", "The Hobbit (Unabridged)", "thehobbit"},
		{"two-word-marker", "Foundation Expanded Edition", "foundation"},
		{"anniversary", "The Name of the Wind Anniversary Edition", "thenameofthewind"},
		{"stacked-markers", "Dramatised Unabridged Edition", "dramatised"}, // markers stripped until the guard keeps one word
		{"leading-complete-kept", "Complete Works of Shakespeare", "completeworksofshakespeare"},
		{"trailing-complete", "Shakespeare Complete", "shakespeare"},
		{"no-marker", "The Hobbit", "thehobbit"},
		{"single-marker-survives", "Unabridged", "unabridged"},
		{"internal-kept", "The Complete Guide to Dune", "thecompleteguidetodune"},
		{"dramatised-british", "Frankenstein Dramatised", "frankenstein"},
		{"dramatized-american", "Frankenstein Dramatized", "frankenstein"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EditionKey(tc.in); got != tc.want {
				t.Errorf("EditionKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMatchRules(t *testing.T) {
	cases := []struct {
		name  string
		a, b  string
		want  []RuleID
		match bool
	}{
		{
			name:  "punctuation-variant",
			a:     "Mistborn: The Final Empire",
			b:     "Mistborn - The Final Empire",
			want:  []RuleID{RuleAlnumEqual},
			match: true,
		},
		{
			name:  "article-strip-only",
			a:     "Martian",
			b:     "The Martian",
			want:  []RuleID{RuleArticleStrip},
			match: true,
		},
		{
			name:  "star-wars-trap",
			a:     "Star Wars",
			b:     "Star Wars 2",
			match: false,
		},
		{
			name:  "leading-complete-trap",
			a:     "Complete Works of Shakespeare",
			b:     "Works of Shakespeare",
			match: false,
		},
		{
			name:  "edition-suffix-only",
			a:     "The Hobbit (Unabridged)",
			b:     "The Hobbit",
			want:  []RuleID{RuleEditionSuffix},
			match: true,
		},
		{
			name:  "article-plus-edition",
			a:     "Hobbit Unabridged",
			b:     "The Hobbit",
			want:  []RuleID{RuleArticleStrip, RuleEditionSuffix},
			match: true,
		},
		{
			name:  "subtitle-embedded",
			a:     "Mistborn",
			b:     "Mistborn: The Final Empire",
			want:  []RuleID{RuleSubstring},
			match: true,
		},
		{
			name:  "residual-guard-martian-child",
			a:     "The Martian",
			b:     "The Martian Child",
			match: false,
		},
		{
			name:  "short-key-guard",
			a:     "Go",
			b:     "Gone",
			match: false,
		},
		{
			name:  "accepted-fp-trilogy-member",
			a:     "The Lord of the Rings",
			b:     "The Lord of the Rings: The Return of the King",
			want:  []RuleID{RuleSubstring},
			match: true,
		},
		{
			name:  "blank-never-matches",
			a:     "",
			b:     "",
			match: false,
		},
		{
			name:  "blank-vs-titled",
			a:     "",
			b:     "Dune",
			match: false,
		},
		{
			name:  "unrelated",
			a:     "Dune",
			b:     "Hyperion",
			match: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchRules(tc.a, tc.b)
			if tc.match {
				if len(got) == 0 {
					t.Fatalf("MatchRules(%q, %q) = nil, want match %v", tc.a, tc.b, tc.want)
				}
				if !rulesEqual(got, tc.want) {
					t.Errorf("MatchRules(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
				}
			} else if len(got) != 0 {
				t.Errorf("MatchRules(%q, %q) = %v, want no match", tc.a, tc.b, got)
			}
		})
	}
}

func rulesEqual(a, b []RuleID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestScanGrouping pins the grouping semantics: same-key books merge, a
// single book is never a group, and output is deterministic.
func TestScanGrouping(t *testing.T) {
	books := []models.Book{
		book("Dune"),
		book("Dune"),
		book("Hyperion"),
		book("Dune, Part Two"),
		book("Dune Part Two"),
	}
	// Give stable, ordered IDs.
	for i := range books {
		books[i].ID = int64(i + 1)
	}

	groups := Scan(books)
	if len(groups) != 2 {
		t.Fatalf("Scan = %d groups, want 2 (dune pair; dune part two pair)", len(groups))
	}

	// Deterministic order: groups sorted by key.
	if groups[0].Key != "dune" || groups[1].Key != "duneparttwo" {
		t.Fatalf("group order = %q, %q; want sorted by key", groups[0].Key, groups[1].Key)
	}

	dune := groups[0]
	if len(dune.Members) != 2 {
		t.Fatalf("dune group has %d members, want 2", len(dune.Members))
	}
	if dune.Members[0].ID != 1 || dune.Members[1].ID != 2 {
		t.Errorf("members not sorted by ID: %d, %d", dune.Members[0].ID, dune.Members[1].ID)
	}
	if !rulesContain(dune.Rules, RuleAlnumEqual) {
		t.Errorf("dune group rules = %v, want alnum-equal", dune.Rules)
	}
}

// TestScanTransitiveChain pins connected-component grouping: A~B on one rule
// and B~C on another put all three in one group whose Rules is the union.
func TestScanTransitiveChain(t *testing.T) {
	books := []models.Book{
		book("Martian"),              // A
		book("The Martian"),          // B — article-strip with A
		book("The Martian: A Novel"), // C — substring with B
		book("Martian Child"),        // D — must NOT join (residual "child" < 6)
	}
	for i := range books {
		books[i].ID = int64(i + 1)
	}

	groups := Scan(books)
	if len(groups) != 1 {
		t.Fatalf("Scan = %d groups, want 1", len(groups))
	}
	g := groups[0]
	if len(g.Members) != 3 {
		t.Fatalf("group has %d members, want 3 (D excluded by residual guard)", len(g.Members))
	}
	for _, r := range []RuleID{RuleArticleStrip, RuleSubstring} {
		if !rulesContain(g.Rules, r) {
			t.Errorf("group rules = %v, want union containing %s", g.Rules, r)
		}
	}
}

// TestScanBlankTitles pins that untitled rows never group with each other.
func TestScanBlankTitles(t *testing.T) {
	books := []models.Book{book(""), book(""), book("Dune")}
	for i := range books {
		books[i].ID = int64(i + 1)
	}
	if groups := Scan(books); len(groups) != 0 {
		t.Fatalf("Scan = %d groups, want 0 (blank titles never group)", len(groups))
	}
}

// TestScanIsExclusionAgnostic pins that Scan never looks at the Excluded flag:
// exclusion policy belongs to the caller (the handler), which is the only
// layer that can act on the result.
func TestScanIsExclusionAgnostic(t *testing.T) {
	a := book("Dune")
	a.ID = 1
	b := book("Dune")
	b.ID = 2
	b.Excluded = true

	groups := Scan([]models.Book{a, b})
	if len(groups) != 1 || len(groups[0].Members) != 2 {
		t.Fatalf("excluded member must still be scanned: got %d groups", len(groups))
	}
	if !groups[0].Members[1].Excluded {
		t.Error("member Excluded flag must be preserved in the output")
	}
}

// TestScanDeterministicUnderShuffle pins that input order does not affect the
// output (groups by key, members by ID), so API responses are stable.
func TestScanDeterministicUnderShuffle(t *testing.T) {
	mk := func() []models.Book {
		titles := []string{"Dune", "Hyperion", "Dune", "Dune, Part Two", "Dune"}
		books := make([]models.Book, len(titles))
		for i, title := range titles {
			books[i] = book(title)
			books[i].ID = int64(i + 1)
		}
		return books
	}
	first := render(Scan(mk()))
	// Rebuild with a different input order, same books.
	src := mk()
	order := []int{2, 0, 3, 1, 4}
	shuffled := make([]models.Book, len(order))
	for i, s := range order {
		shuffled[i] = src[s]
	}
	if second := render(Scan(shuffled)); second != first {
		t.Errorf("scan order-dependent:\nfirst = %s\nsecond = %s", first, second)
	}
}

func render(groups []Group) string {
	var sb strings.Builder
	for _, g := range groups {
		sb.WriteString(g.Key)
		sb.WriteString(":rules=")
		sb.WriteString(strings.Join(mapRules(g.Rules), ","))
		for _, m := range g.Members {
			fmt.Fprintf(&sb, "|%d(%s)", m.ID, strings.Join(mapRules(m.Rules), ","))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func mapRules(rules []RuleID) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = string(r)
	}
	return out
}

func rulesContain(rules []RuleID, want RuleID) bool {
	for _, r := range rules {
		if r == want {
			return true
		}
	}
	return false
}

// TestScanScale pins the performance budget from the plan: 500 books with a
// few duplicate pairs must scan in well under 100ms.
func TestScanScale(t *testing.T) {
	books := make([]models.Book, 0, 500)
	for i := 0; i < 494; i++ {
		b := book(fmt.Sprintf("Distinct Title Number %03d", i))
		b.ID = int64(i + 1)
		books = append(books, b)
	}
	// Four rows share one title; two more share another. 494 + 4 + 2 = 500.
	for i, title := range []string{"The Martian", "The Martian", "Martian", "The Martian: A Novel"} {
		b := book(title)
		b.ID = int64(495 + i)
		books = append(books, b)
	}
	for i, title := range []string{"Dune", "Dune"} {
		b := book(title)
		b.ID = int64(499 + i)
		books = append(books, b)
	}

	start := time.Now()
	groups := Scan(books)
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("Scan(500 books) took %s, budget 100ms", elapsed)
	}
	if len(groups) != 2 {
		t.Fatalf("Scan = %d groups, want 2", len(groups))
	}
}
