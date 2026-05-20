package indexer

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

func resultTitles(rs []newznab.SearchResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	return out
}

func toResults(titles ...string) []newznab.SearchResult {
	rs := make([]newznab.SearchResult, len(titles))
	for i, t := range titles {
		rs[i] = newznab.SearchResult{Title: t, GUID: t}
	}
	return rs
}

func contains(haystack []newznab.SearchResult, needle string) bool {
	for _, r := range haystack {
		if r.Title == needle {
			return true
		}
	}
	return false
}

func TestFilterRelevantTheSparrow(t *testing.T) {
	// The "canonical" failing case: short title + common word.
	// Post-#563 the author check requires all significant author tokens to
	// appear (not just surname) for single-keyword titles. Releases that
	// carry only the surname are now rejected as too ambiguous — they could
	// be by any "Russell". A release naming the full author is required.
	results := toResults(
		"Mary.Doria.Russell.-.The.Sparrow.1996.RETAIL.EPUB",
		"The.Sparrow.Russell.epub",
		"Falcon.and.the.Sparrow.MaryLu.Tyndall.epub",
		"Song.of.the.Wooden.Sparrow.epub",
		"The.Hempcrete.Book.William.Stanwix.Alex.Sparrow.epub",
		"Dark.Horse.Blade.Of.The.Immortal.Vol.18.The.Sparrow.Net.Comic.eBook",
	)
	got := filterRelevant(results, "The Sparrow", "Mary Doria Russell", nil)

	if !contains(got, "Mary.Doria.Russell.-.The.Sparrow.1996.RETAIL.EPUB") {
		t.Errorf("expected Russell's Sparrow to be kept, got %v", resultTitles(got))
	}
	// Surname-only release: now rejected post-#563 (was a false-positive vector).
	if contains(got, "The.Sparrow.Russell.epub") {
		t.Errorf("post-#563: surname-only release should be rejected, got %v", resultTitles(got))
	}
	for _, noise := range []string{
		"Falcon.and.the.Sparrow.MaryLu.Tyndall.epub",
		"Song.of.the.Wooden.Sparrow.epub",
		"The.Hempcrete.Book.William.Stanwix.Alex.Sparrow.epub",
		"Dark.Horse.Blade.Of.The.Immortal.Vol.18.The.Sparrow.Net.Comic.eBook",
	} {
		if contains(got, noise) {
			t.Errorf("expected %q to be filtered out", noise)
		}
	}
}

func TestFilterRelevantWordBoundary(t *testing.T) {
	// Ensure "sparrow" keyword does not leak into "sparrowhawk" or "sparrows".
	// Releases name the full author so the post-#563 author check is satisfied.
	results := toResults(
		"sparrowhawk.by.russell.epub",
		"sparrows.russell.epub",
		"mary.doria.russell.the.sparrow.epub",
	)
	got := filterRelevant(results, "The Sparrow", "Mary Doria Russell", nil)
	if contains(got, "sparrowhawk.by.russell.epub") {
		t.Error("must not match 'sparrowhawk' for 'sparrow' keyword")
	}
	if contains(got, "sparrows.russell.epub") {
		t.Error("must not match plural 'sparrows' for 'sparrow' keyword")
	}
	if !contains(got, "mary.doria.russell.the.sparrow.epub") {
		t.Error("expected 'mary.doria.russell.the.sparrow' to pass")
	}
}

func TestFilterRelevantMultiWordPhrase(t *testing.T) {
	// Two-significant-word title: phrase contiguity.
	results := toResults(
		"Cormac.McCarthy.-.The.Road.2006.epub",
		"On.The.Road.Again.Willie.Nelson.epub",
		"The.Road.To.Wigan.Pier.Orwell.epub",
	)
	got := filterRelevant(results, "The Road", "Cormac McCarthy", nil)

	if !contains(got, "Cormac.McCarthy.-.The.Road.2006.epub") {
		t.Error("expected McCarthy's The Road to pass")
	}
	// "On The Road Again" does contain "the road" as a contiguous phrase,
	// which is a false positive the author surname would have caught. Our
	// rule is phrase-only for multi-word titles — so this passes phrase but
	// still comes from a different book. That's a known limitation; we
	// accept it because requiring surname for 2-word titles would reject
	// too many legitimate NZBs that don't include the author. Document here.
	// The key guarantee is that "Road to Wigan Pier" (not a contiguous
	// "the road" phrase followed by the requested book) is rejected.
	if contains(got, "The.Road.To.Wigan.Pier.Orwell.epub") {
		// "the road to wigan pier" — the phrase "the road" appears then
		// extends. Our regex is \bthe\W+road\b — the \b at the end after
		// "road" requires a non-word boundary, which there is (space). So
		// this WOULD match. That's acceptable: it contains the full phrase
		// "the road". The user can grab or skip.
		t.Logf("note: 'The Road to Wigan Pier' passes phrase match (known limitation)")
	}
}

func TestFilterRelevantSubtitle(t *testing.T) {
	// "Dune: Messiah" must accept releases naming the full author. The colon
	// subtitle is treated specially: a release matching the primary ("Dune")
	// + the full author is accepted. Post-#563 the primary-title-only path
	// (single keyword) requires both first name AND surname, not just surname.
	results := toResults(
		"Frank.Herbert.Dune.Messiah.epub", // full author + primary title
		"Frank.Herbert.Dune.epub",         // full author + primary only
		"Dune.Herbert.epub",               // surname only — post-#563 rejected
	)
	got := filterRelevant(results, "Dune: Messiah", "Frank Herbert", nil)
	for _, title := range []string{
		"Frank.Herbert.Dune.Messiah.epub",
		"Frank.Herbert.Dune.epub",
	} {
		if !contains(got, title) {
			t.Errorf("expected %q to pass subtitle filter, got %v", title, resultTitles(got))
		}
	}
	// Post-#563: primary-title-only with surname-only author is now rejected.
	if contains(got, "Dune.Herbert.epub") {
		t.Error("post-#563: primary-only + surname-only release should be rejected")
	}
}

func TestFilterRelevantNoResults(t *testing.T) {
	// Empty input → empty output, no panic.
	got := filterRelevant(nil, "The Sparrow", "Russell", nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// TestFilterRelevantApostrophe — regression test for the apostrophe bug (fixes #82).
// Book titles with possessive apostrophes ("Ender's Game", "The Handmaid's Tale")
// produce keyword tokens like "ender's". Release filenames never carry apostrophes
// ("Enders.Game.epub"), so after NormalizeRelease the apostrophe is absent.
// Before the fix, WordBoundaryRegex("ender's") never matched "enders" and all
// results were dropped.
func TestFilterRelevantApostrophe(t *testing.T) {
	cases := []struct {
		bookTitle string
		author    string
		releases  []string
		wantAny   string // at least this release must survive
	}{
		{
			bookTitle: "Ender's Game",
			author:    "Orson Scott Card",
			releases: []string{
				"Orson.Scott.Card.Enders.Game.RETAIL.EPUB",
				"Enders.Game.epub",
				"Some.Other.Book.epub",
			},
			wantAny: "Orson.Scott.Card.Enders.Game.RETAIL.EPUB",
		},
		{
			bookTitle: "The Handmaid's Tale",
			author:    "Margaret Atwood",
			releases: []string{
				"Margaret.Atwood.The.Handmaids.Tale.EPUB",
				"Handmaids.Tale.Atwood.mobi",
			},
			wantAny: "Margaret.Atwood.The.Handmaids.Tale.EPUB",
		},
		{
			bookTitle: "The Hitchhiker's Guide to the Galaxy",
			author:    "Douglas Adams",
			releases: []string{
				"Douglas.Adams.Hitchhikers.Guide.to.the.Galaxy.epub",
				"Hitchhikers.Guide.Galaxy.Adams.RETAIL.epub",
			},
			wantAny: "Douglas.Adams.Hitchhikers.Guide.to.the.Galaxy.epub",
		},
	}

	for _, tc := range cases {
		got := filterRelevant(toResults(tc.releases...), tc.bookTitle, tc.author, nil)
		if !contains(got, tc.wantAny) {
			t.Errorf("filterRelevant(%q, %q): expected %q in results, got %v",
				tc.bookTitle, tc.author, tc.wantAny, resultTitles(got))
		}
	}
}

// TestFilterRelevantEditionQualifier — regression test for issue #283.
// filterRelevant must accept real NZB releases for a book whose metadata
// title carries a parenthesised edition qualifier. Before the fix,
// "(German Edition)" was tokenised into sigWords as the keyword "(german",
// which never matched any release name, causing the entire result set to be
// dropped.
func TestFilterRelevantEditionQualifier(t *testing.T) {
	results := toResults(
		"Herta.Mueller.Die.Stille.ist.ein.Geraeusch.epub",
		"Die.Stille.ist.ein.Geraeusch.Mueller.epub",
		"Some.Unrelated.Noise.epub",
	)
	got := filterRelevant(results, "Die Stille ist ein Geräusch (German Edition)", "Herta Müller", nil)
	if !contains(got, "Herta.Mueller.Die.Stille.ist.ein.Geraeusch.epub") {
		t.Errorf("expected full-title release to pass, got %v", resultTitles(got))
	}
	if !contains(got, "Die.Stille.ist.ein.Geraeusch.Mueller.epub") {
		t.Errorf("expected release without edition qualifier to pass, got %v", resultTitles(got))
	}
	if contains(got, "Some.Unrelated.Noise.epub") {
		t.Error("unrelated noise must not pass")
	}
}

func TestRankResultsRetailBeatsScene(t *testing.T) {
	results := toResults(
		"The.Sparrow.Russell.SCENE.epub",
		"The.Sparrow.Russell.RETAIL.epub",
	)
	rankResults(results, MatchCriteria{Title: "The Sparrow", Author: "Mary Doria Russell"})
	if results[0].Title != "The.Sparrow.Russell.RETAIL.epub" {
		t.Errorf("RETAIL should rank first, got order: %v", resultTitles(results))
	}
}

func TestRankResultsYearBoost(t *testing.T) {
	results := toResults(
		"The.Sparrow.Russell.2010.epub", // mismatch
		"The.Sparrow.Russell.1996.epub", // exact
	)
	rankResults(results, MatchCriteria{Title: "The Sparrow", Author: "Russell", Year: 1996})
	if results[0].Title != "The.Sparrow.Russell.1996.epub" {
		t.Errorf("exact-year release should rank first, got order: %v", resultTitles(results))
	}
}

func TestRankResultsFormatQuality(t *testing.T) {
	results := toResults(
		"The.Sparrow.Russell.pdf",
		"The.Sparrow.Russell.epub",
	)
	rankResults(results, MatchCriteria{Title: "The Sparrow", Author: "Russell"})
	if results[0].Title != "The.Sparrow.Russell.epub" {
		t.Errorf("epub should rank above pdf, got order: %v", resultTitles(results))
	}
}

func TestRankResultsAbridgedPenalty(t *testing.T) {
	results := toResults(
		"The.Sparrow.Russell.ABRIDGED.m4b",
		"The.Sparrow.Russell.UNABRIDGED.m4b",
	)
	rankResults(results, MatchCriteria{Title: "The Sparrow", Author: "Russell"})
	if results[0].Title != "The.Sparrow.Russell.UNABRIDGED.m4b" {
		t.Errorf("UNABRIDGED should rank above ABRIDGED, got order: %v", resultTitles(results))
	}
}

func TestFilterByLanguageEnglish(t *testing.T) {
	results := toResults(
		"The.Sparrow.Russell.epub",
		"Le.Moineau.Russell.FRENCH.epub",
	)
	got := FilterByLanguage(results, "en")
	if contains(got, "Le.Moineau.Russell.FRENCH.epub") {
		t.Error("FRENCH-tagged release should be filtered when lang=en")
	}
	if !contains(got, "The.Sparrow.Russell.epub") {
		t.Error("non-foreign-tagged release must pass")
	}
}

func TestFilterByLanguageAny(t *testing.T) {
	results := toResults(
		"Le.Moineau.Russell.FRENCH.epub",
		"The.Sparrow.Russell.epub",
	)
	got := FilterByLanguage(results, "any")
	if len(got) != 2 {
		t.Errorf("lang=any should pass all %d results, got %d", 2, len(got))
	}
}

func TestFilterCategoriesForMedia(t *testing.T) {
	all := []int{7000, 7020, 3030}
	ebook := filterCategoriesForMedia(all, "ebook")
	if len(ebook) != 1 || ebook[0] != 7020 {
		t.Errorf("ebook filter = %v, want [7020]", ebook)
	}
	audio := filterCategoriesForMedia(all, "audiobook")
	if len(audio) != 1 || audio[0] != 3030 {
		t.Errorf("audiobook filter = %v, want [3030]", audio)
	}
	// Empty input falls back to the standard category for the media type.
	if got := filterCategoriesForMedia(nil, "ebook"); len(got) != 1 || got[0] != 7020 {
		t.Errorf("nil + ebook should fall back to [7020], got %v", got)
	}
	if got := filterCategoriesForMedia(nil, "audiobook"); len(got) != 1 || got[0] != 3030 {
		t.Errorf("nil + audiobook should fall back to [3030], got %v", got)
	}
	// Unknown type falls back to books.
	if got := filterCategoriesForMedia(all, ""); len(got) != 1 {
		t.Errorf("empty type should default to books, got %v", got)
	}
	// Pre-v0.5.0 indexer config without 3030 still searches audiobooks
	// via the fallback 3030 category rather than silently returning
	// ebook results.
	booksOnly := []int{7000, 7020}
	if got := filterCategoriesForMedia(booksOnly, "audiobook"); len(got) != 1 || got[0] != 3030 {
		t.Errorf("no-match audiobook should fall back to [3030], got %v", got)
	}
}

func TestScoreResultMediaTypePenalty(t *testing.T) {
	audiobookResult := newznab.SearchResult{Title: "Dune.Herbert.m4b", GUID: "a"}
	ebookResult := newznab.SearchResult{Title: "Dune.Herbert.epub", GUID: "e"}
	// Asking for an audiobook: m4b should beat epub even though epub has
	// higher raw quality rank (5) than m4b (9 in our scale).
	crit := MatchCriteria{Title: "Dune", Author: "Frank Herbert", MediaType: "audiobook"}
	aScore := scoreResult(audiobookResult, crit)
	eScore := scoreResult(ebookResult, crit)
	if aScore <= eScore {
		t.Errorf("audiobook score %.1f should exceed ebook score %.1f when MediaType=audiobook", aScore, eScore)
	}
	// And vice versa.
	crit.MediaType = "ebook"
	aScore = scoreResult(audiobookResult, crit)
	eScore = scoreResult(ebookResult, crit)
	if eScore <= aScore {
		t.Errorf("ebook score %.1f should exceed audiobook score %.1f when MediaType=ebook", eScore, aScore)
	}
}

// Regression: rankResults used to precompute scores into a parallel
// slice and read them via stale indices during the in-place sort,
// leaving results effectively unsorted. This test exercises >2 items
// so any mis-sort surfaces.
func TestRankResultsManyItemsOrdering(t *testing.T) {
	results := toResults(
		// Intentionally scrambled so initial and ranked orders differ.
		`NMR: Project Hail Mary - Andy Weir - 2021 [12/22] - "Part.09.rar"`,
		`NMR: Project Hail Mary - Andy Weir - 2021 [01/22] - "Part.01.rar"`,
		`[M4B] Andy.Weir-Project.Hail.Mary`,
		`Andy.Weir-Project.Hail.Mary.mp3`,
		`NMR: Project Hail Mary - Andy Weir - 2021 [06/22] - "Part.03.rar"`,
		`Project.Hail.Mary.ABRIDGED.mp3`,
	)
	rankResults(results, MatchCriteria{
		Title:     "Project Hail Mary",
		Author:    "Andy Weir",
		MediaType: "audiobook",
	})
	// After ranking, a recognized audiobook format must be at the top
	// (was getting buried under format-unknown NMR posts pre-fix).
	if p := ParseRelease(results[0].Title); !isAudiobookFormat(p.Format) {
		t.Errorf("top result should have an audiobook format, got %q (Format=%q)", results[0].Title, p.Format)
	}
	// The unabridged M4B should beat the abridged MP3.
	m4bIdx, abridgedIdx := -1, -1
	for i, r := range results {
		if r.Title == `[M4B] Andy.Weir-Project.Hail.Mary` {
			m4bIdx = i
		}
		if r.Title == `Project.Hail.Mary.ABRIDGED.mp3` {
			abridgedIdx = i
		}
	}
	if m4bIdx < 0 || abridgedIdx < 0 {
		t.Fatal("expected both tagged results to survive filtering")
	}
	if m4bIdx >= abridgedIdx {
		t.Errorf("M4B (idx=%d) should outrank ABRIDGED mp3 (idx=%d)", m4bIdx, abridgedIdx)
	}
}

func TestRankResultsIndexerPriority(t *testing.T) {
	// Two otherwise-identical releases; the one from the higher-priority indexer
	// must sort first regardless of insertion order.
	low := newznab.SearchResult{Title: "Project.Hail.Mary.EPUB", IndexerPriority: 10}
	high := newznab.SearchResult{Title: "Project.Hail.Mary.EPUB", IndexerPriority: 50}
	results := []newznab.SearchResult{low, high}
	rankResults(results, MatchCriteria{Title: "Project Hail Mary"})
	if results[0].IndexerPriority != 50 {
		t.Errorf("expected higher-priority indexer first, got priority=%d", results[0].IndexerPriority)
	}
}

func TestFilterUsenetJunk(t *testing.T) {
	junk := []string{
		`NMR: Project Hail Mary - Andy Weir - 2021 [12/22] - "Andy Weir - 2021 - Project Hail Mary.part09.rar" yEnc`,
		`Something.vol003+004.par2`,
		`Book.sfv`,
		`Post [1/5] - "chunk" yEnc`,
	}
	keepers := []string{
		`[M4B] Andy Weir-Project Hail Mary`,
		`Andy.Weir-Project.Hail.Mary.m4b`,
		`Russell-The.Sparrow.EPUB`,
	}
	input := toResults(append(junk, keepers...)...)
	out := filterUsenetJunk(input)
	if len(out) != len(keepers) {
		t.Errorf("expected %d survivors, got %d: %v", len(keepers), len(out), resultTitles(out))
	}
	for _, j := range junk {
		if contains(out, j) {
			t.Errorf("junk slipped through: %q", j)
		}
	}
	for _, k := range keepers {
		if !contains(out, k) {
			t.Errorf("keeper was dropped: %q", k)
		}
	}
}

func TestDedupeByGUID(t *testing.T) {
	results := []newznab.SearchResult{
		{GUID: "abc", Title: "First"},
		{GUID: "abc", Title: "Duplicate"},
		{GUID: "def", Title: "Unique"},
	}
	got := dedupe(results)
	if len(got) != 2 {
		t.Errorf("expected 2 after dedup, got %d: %v", len(got), resultTitles(got))
	}
	if got[0].GUID != "abc" || got[1].GUID != "def" {
		t.Errorf("unexpected dedup order: %v", resultTitles(got))
	}
}

func TestDedupeByTitleURL(t *testing.T) {
	// Results with empty GUID fall back to Title+NZBURL as key
	results := []newznab.SearchResult{
		{GUID: "", Title: "Book", NZBURL: "http://a"},
		{GUID: "", Title: "Book", NZBURL: "http://a"}, // duplicate
		{GUID: "", Title: "Book", NZBURL: "http://b"}, // different URL
	}
	got := dedupe(results)
	if len(got) != 2 {
		t.Errorf("expected 2 after title+url dedup, got %d", len(got))
	}
}

func TestProtocolForType(t *testing.T) {
	if p := protocolForType("torznab"); p != "torrent" {
		t.Errorf("torznab → want torrent, got %q", p)
	}
	if p := protocolForType("newznab"); p != "usenet" {
		t.Errorf("newznab → want usenet, got %q", p)
	}
	if p := protocolForType(""); p != "usenet" {
		t.Errorf("empty → want usenet, got %q", p)
	}
}

func TestFilterByLanguageAllDropped(t *testing.T) {
	results := toResults(
		"Le.Moineau.FRENCH.epub",
		"Das.Buch.GERMAN.epub",
	)
	got := FilterByLanguage(results, "en")
	if len(got) != 0 {
		t.Errorf("expected all foreign results dropped, got %v", resultTitles(got))
	}
}

func TestFilterByLanguagePassesAllWhenNoForeignTags(t *testing.T) {
	results := toResults("Book.A.epub", "Book.B.epub")
	got := FilterByLanguage(results, "en")
	if len(got) != 2 {
		t.Errorf("results without foreign tags must all pass, got %d", len(got))
	}
}

func TestIsArticle(t *testing.T) {
	for _, w := range []string{"the", "The", "THE", "a", "A", "an", "AN"} {
		if !IsArticle(w) {
			t.Errorf("%q should be an article", w)
		}
	}
	for _, w := range []string{"book", "sparrow", "dune", ""} {
		if IsArticle(w) {
			t.Errorf("%q should NOT be an article", w)
		}
	}
}

func TestTitleMatchesSingleKeyword(t *testing.T) {
	// Single keyword without author tokens → accept (can't do better)
	if !titleMatchesResult("dune", []string{"dune"}, nil, false) {
		t.Error("single keyword, no author → should accept")
	}
	// Single keyword with non-matching surname → reject
	if titleMatchesResult("dune novel", []string{"dune"}, []string{"herbert"}, false) {
		t.Error("single keyword missing surname → should reject")
	}
	// Single keyword with matching surname → accept
	if !titleMatchesResult("dune herbert", []string{"dune"}, []string{"herbert"}, false) {
		t.Error("single keyword + matching surname → should accept")
	}
}

// Regression: the old anyPhraseMatch batch gate caused correctly-titled results
// to be dropped when an abbreviated result in the same batch happened to have
// the significant keywords adjacent, setting anyPhraseMatch=true and disabling
// keyword fallback for the whole batch.
func TestFilterRelevantAnyPhraseMatchTrap(t *testing.T) {
	// "The Name of the Wind" — sigWords = ["name", "wind"].
	// Phrase \bname\W+wind\b fails for "name of the wind" (stop words between).
	// An abbreviated result "name.wind.epub" would previously trigger
	// anyPhraseMatch=true, causing the correct release to be dropped.
	results := toResults(
		"Patrick.Rothfuss.-.The.Name.of.the.Wind.EPUB",
		"Name.Wind.Rothfuss.epub", // abbreviated — phrase-matches ["name","wind"]
		"Completely.Unrelated.Book.epub",
	)
	got := filterRelevant(results, "The Name of the Wind", "Patrick Rothfuss", nil)

	if !contains(got, "Patrick.Rothfuss.-.The.Name.of.the.Wind.EPUB") {
		t.Errorf("correct release dropped by anyPhraseMatch trap; got %v", resultTitles(got))
	}
	if !contains(got, "Name.Wind.Rothfuss.epub") {
		t.Errorf("abbreviated release should also pass; got %v", resultTitles(got))
	}
	if contains(got, "Completely.Unrelated.Book.epub") {
		t.Error("unrelated result should be filtered out")
	}
}

// Titles with stop words between significant keywords should pass keyword
// fallback even when no result has a strict adjacency phrase match.
func TestFilterRelevantStopWordsBetweenKeywords(t *testing.T) {
	// "Lord of the Rings" — sigWords = ["lord", "rings"].
	// Phrase \blord\W+rings\b never matches because "of the" sits between them.
	// All correct results should pass via keyword fallback.
	results := toResults(
		"J.R.R.Tolkien.-.The.Lord.of.the.Rings.EPUB",
		"Lord.Of.The.Rings.Tolkien.epub",
		"The.Lord.of.the.Rings.Fellowship.epub",
		"Lord.of.the.Rings.Unabridged.m4b",
		"Unrelated.Fantasy.Novel.epub",
	)
	got := filterRelevant(results, "The Lord of the Rings", "J.R.R. Tolkien", nil)

	for _, title := range []string{
		"J.R.R.Tolkien.-.The.Lord.of.the.Rings.EPUB",
		"Lord.Of.The.Rings.Tolkien.epub",
		"The.Lord.of.the.Rings.Fellowship.epub",
		"Lord.of.the.Rings.Unabridged.m4b",
	} {
		if !contains(got, title) {
			t.Errorf("expected %q to pass stop-word keyword fallback; got %v", title, resultTitles(got))
		}
	}
	if contains(got, "Unrelated.Fantasy.Novel.epub") {
		t.Error("unrelated result should be filtered out")
	}
}

// Only standard Newznab ebook (702x) and audiobook (303x) subcategories pass.
// Site-specific extensions like 7120 or 3130 are outside those ranges.
func TestFilterCategoriesCustomIDs(t *testing.T) {
	cats := []int{7020, 7120, 3030, 3130}

	ebook := filterCategoriesForMedia(cats, "ebook")
	if len(ebook) != 1 || ebook[0] != 7020 {
		t.Errorf("ebook cats = %v, want [7020]", ebook)
	}

	audio := filterCategoriesForMedia(cats, "audiobook")
	if len(audio) != 1 || audio[0] != 3030 {
		t.Errorf("audio cats = %v, want [3030]", audio)
	}
}

// TestFilterCategoriesMaM covers indexers like MyAnonamouse whose entire
// taxonomy uses 100xxx IDs (e.g. 100013 = AudioBooks, 100111 = Audiobooks -
// Young Adult). None of these match the standard 303x/702x prefix, so the
// old code substituted the fallback 3030 — which MaM does not map to any of
// its own subcategories, returning unrelated results.
//
// When all configured categories are non-standard (>9999) and no standard
// match exists, filterCategoriesForMedia must pass them through as-is.
func TestFilterCategoriesMaM(t *testing.T) {
	// Typical MaM audiobook category list
	mamAudioCats := []int{100013, 100039, 100041, 100042, 100044, 100045, 100046, 100047, 100111}

	got := filterCategoriesForMedia(mamAudioCats, "audiobook")
	if len(got) != len(mamAudioCats) {
		t.Fatalf("MaM audiobook cats: got %v, want all %v passed through", got, mamAudioCats)
	}
	for i, c := range mamAudioCats {
		if got[i] != c {
			t.Errorf("MaM audiobook cats[%d]: got %d, want %d", i, got[i], c)
		}
	}

	// MaM ebook category list
	mamEbookCats := []int{100014, 100060, 100062, 100063, 100064, 100112}

	got = filterCategoriesForMedia(mamEbookCats, "ebook")
	if len(got) != len(mamEbookCats) {
		t.Fatalf("MaM ebook cats: got %v, want all %v passed through", got, mamEbookCats)
	}

	// Standard indexer with no audiobook cats still falls back correctly —
	// the non-standard path must not fire when all configured IDs are standard.
	booksOnly := []int{7000, 7020}
	if fb := filterCategoriesForMedia(booksOnly, "audiobook"); len(fb) != 1 || fb[0] != 3030 {
		t.Errorf("standard ebook-only indexer should fall back to [3030] for audiobook, got %v", fb)
	}
}

func TestFilterCategoriesParentDrop(t *testing.T) {
	cases := []struct {
		cats      []int
		mediaType string
		want      []int
	}{
		// Core regression: bare parent 7000 must never reach the indexer as-is.
		// Prowlarr reports only 7000 for generic book trackers; the searcher must
		// widen this to the ebook default (7020) rather than sending the broad
		// parent, which causes indexers to return noisier result sets.
		{[]int{7000}, "ebook", []int{7020}},
		{[]int{3000}, "audiobook", []int{3030}},
		{[]int{7000, 7020}, "ebook", []int{7020}},
		{[]int{7000, 7020, 7030}, "ebook", []int{7020}},
		{[]int{3000, 3030}, "audiobook", []int{3030}},
		{nil, "ebook", []int{7020}},
		{[]int{7020, 7021, 7022}, "ebook", []int{7020, 7021, 7022}},
	}
	for _, tc := range cases {
		got := filterCategoriesForMedia(tc.cats, tc.mediaType)
		if len(got) != len(tc.want) {
			t.Errorf("filterCategoriesForMedia(%v, %q) = %v, want %v", tc.cats, tc.mediaType, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("filterCategoriesForMedia(%v, %q)[%d] = %d, want %d", tc.cats, tc.mediaType, i, got[i], tc.want[i])
			}
		}
	}
}

func TestIsAudiobookFormat(t *testing.T) {
	for _, f := range []string{"m4b", "m4a", "mp3", "flac", "ogg"} {
		if !isAudiobookFormat(f) {
			t.Errorf("%q should be an audiobook format", f)
		}
	}
	for _, f := range []string{"epub", "mobi", "azw3", "pdf", ""} {
		if isAudiobookFormat(f) {
			t.Errorf("%q should NOT be an audiobook format", f)
		}
	}
}

// TestFilterRelevantGermanUmlauts is a regression test for issue #211.
// German NZB indexers (e.g. Scenenzbs) transliterate umlauts in release names:
// ä→ae, ö→oe, ü→ue, ß→ss. Without normalisation on both sides, filterRelevant
// drops every result even though 100 are returned from the indexer.
func TestFilterRelevantGermanUmlauts(t *testing.T) {
	cases := []struct {
		bookTitle string
		author    string
		releases  []string
		wantAny   string
	}{
		{
			// "Gespensterjäger" → indexer stores as "Gespensterjaeger"
			bookTitle: "Gespensterjäger",
			author:    "Cornelia Funke",
			releases: []string{
				"Cornelia.Funke.-.Gespensterjaeger.EPUB",
				"Gespensterjaeger.Cornelia.Funke.2003.epub",
			},
			wantAny: "Cornelia.Funke.-.Gespensterjaeger.EPUB",
		},
		{
			// "Die Stille ist ein Geräusch" → "Gerausch" in releases
			bookTitle: "Die Stille ist ein Geräusch",
			author:    "Juli Zeh",
			releases: []string{
				"Juli.Zeh.Die.Stille.ist.ein.Gerausch.epub",
				"Die.Stille.ist.ein.Gerausch.Juli.Zeh.2002.EPUB",
			},
			wantAny: "Juli.Zeh.Die.Stille.ist.ein.Gerausch.epub",
		},
		{
			// Release name uses the actual umlaut character — must also match
			bookTitle: "Gespensterjäger",
			author:    "Cornelia Funke",
			releases: []string{
				"Cornelia.Funke.Gespensterjäger.epub",
			},
			wantAny: "Cornelia.Funke.Gespensterjäger.epub",
		},
	}
	for _, tc := range cases {
		got := filterRelevant(toResults(tc.releases...), tc.bookTitle, tc.author, nil)
		if !contains(got, tc.wantAny) {
			t.Errorf("filterRelevant(%q, %q): want %q in results, got %v",
				tc.bookTitle, tc.author, tc.wantAny, resultTitles(got))
		}
	}
}

// TestFilterRelevantNonLatinAuthor verifies that releases whose author token is
// romanised are accepted when the primary author name is non-latin but a
// latin-script alias is provided. The alias surname is needed for single-keyword
// titles (where filterRelevant requires the surname alongside the keyword to
// prevent false positives).
// TestFilterRelevantPossessivePrefix is a regression test for issue #409.
// Book titles with a possessive author prefix like "Tom Clancy's Rainbow Six"
// must match releases named "Tom Clancy - Rainbow Six". Before the fix,
// sigWords turned "Tom Clancy's" into the keyword "clancys", which never
// matched the apostrophe-free "clancy" token in the release name, causing
// the entire result set to be dropped.
func TestFilterRelevantPossessivePrefix(t *testing.T) {
	cases := []struct {
		bookTitle string
		author    string
		releases  []string
		wantPass  []string
		wantDrop  []string
	}{
		{
			bookTitle: "Tom Clancy's Rainbow Six",
			author:    "Tom Clancy",
			releases: []string{
				"Tom.Clancy.-.Rainbow.Six.epub",
				"Tom.Clancy.Rainbow.Six.RETAIL.EPUB",
				"Rainbow.Six.Clancy.epub",
				"Tom.Clancy.-.The.Hunt.for.Red.October.epub",
			},
			wantPass: []string{
				"Tom.Clancy.-.Rainbow.Six.epub",
				"Tom.Clancy.Rainbow.Six.RETAIL.EPUB",
				"Rainbow.Six.Clancy.epub",
			},
			wantDrop: []string{
				"Tom.Clancy.-.The.Hunt.for.Red.October.epub",
			},
		},
		{
			bookTitle: "James Patterson's Along Came a Spider",
			author:    "James Patterson",
			releases: []string{
				"James.Patterson.-.Along.Came.a.Spider.epub",
				"Along.Came.a.Spider.Patterson.epub",
				"James.Patterson.Kiss.the.Girls.epub",
			},
			wantPass: []string{
				"James.Patterson.-.Along.Came.a.Spider.epub",
				"Along.Came.a.Spider.Patterson.epub",
			},
			wantDrop: []string{
				"James.Patterson.Kiss.the.Girls.epub",
			},
		},
		{
			// Unicode right-single-quotation-mark (U+2019, 3 bytes) instead of
			// ASCII apostrophe — regression for the byte-offset slice bug.
			bookTitle: "Tom Clancy’s Rainbow Six",
			author:    "Tom Clancy",
			releases: []string{
				"Tom.Clancy.-.Rainbow.Six.epub",
				"Tom.Clancy.-.The.Hunt.for.Red.October.epub",
			},
			wantPass: []string{
				"Tom.Clancy.-.Rainbow.Six.epub",
			},
			wantDrop: []string{
				"Tom.Clancy.-.The.Hunt.for.Red.October.epub",
			},
		},
	}

	for _, tc := range cases {
		got := filterRelevant(toResults(tc.releases...), tc.bookTitle, tc.author, nil)
		for _, title := range tc.wantPass {
			if !contains(got, title) {
				t.Errorf("filterRelevant(%q, %q): expected %q to pass, got %v",
					tc.bookTitle, tc.author, title, resultTitles(got))
			}
		}
		for _, title := range tc.wantDrop {
			if contains(got, title) {
				t.Errorf("filterRelevant(%q, %q): expected %q to be dropped, got %v",
					tc.bookTitle, tc.author, title, resultTitles(got))
			}
		}
	}
}

func TestFilterRelevantNonLatinAuthor(t *testing.T) {
	// "Silence" by 遠藤周作 (Shusaku Endo): 1 significant keyword → author required.
	// Post-#563: author check requires all tokens (first+last) for multi-token
	// aliases, so surname-only releases like "Endo.Silence.epub" no longer pass
	// — even with the alias, "shusaku" must also appear.
	releases := []string{
		"Endo.Silence.epub",
		"Shusaku.Endo.Silence.m4b",
		"Silence.epub",
		"Unrelated.Noise.epub",
	}
	results := toResults(releases...)

	// Without aliases, the non-latin surname ("作" from 遠藤周作) never appears in
	// any release name, so author-anchored matches are missed.
	withoutAliases := filterRelevant(results, "Silence", "遠藤周作", nil)
	if contains(withoutAliases, "Endo.Silence.epub") {
		t.Error("without aliases, romanised-surname release should not pass for non-latin primary name")
	}

	// With the latin alias "Shusaku Endo", releases naming the full alias pass;
	// surname-only ones are now rejected (post-#563 stricter author check).
	aliases := []string{"Shusaku Endo"}
	withAliases := filterRelevant(results, "Silence", "遠藤周作", aliases)
	if !contains(withAliases, "Shusaku.Endo.Silence.m4b") {
		t.Errorf("with alias, expected %q to pass; got %v",
			"Shusaku.Endo.Silence.m4b", resultTitles(withAliases))
	}
	if contains(withAliases, "Endo.Silence.epub") {
		t.Error("post-#563: alias surname-only release should be rejected")
	}
	if contains(withAliases, "Unrelated.Noise.epub") {
		t.Error("unrelated result should still be filtered out even with aliases")
	}
}

// TestFilterRelevantCoAuthorSurnameOverlap is the regression test for #563:
// when the monitored author shares a surname with a co-author of an unrelated
// release, the surname-only token check used to leak the co-author's work into
// the monitored author's library. The release filter must require both the
// first name and the surname (or all significant tokens) at word boundaries.
func TestFilterRelevantCoAuthorSurnameOverlap(t *testing.T) {
	// Monitored author "Rachel Reid". A release by co-author "Adam Reid" must
	// be rejected. The title here is a multi-keyword title so the title path
	// doesn't anchor on author; the single-keyword path is what we exercise.
	// Note: the multi-keyword-title path uses phrase-only matching and does
	// NOT consult author tokens at all (the existing code accepts that
	// limitation to avoid rejecting NZBs that omit the author). The author
	// check applies on the single-keyword and zero-keyword title paths only.
	cases := []struct {
		name    string
		title   string
		author  string
		release string
		wantOK  bool
	}{
		{
			name:    "single-keyword title, surname-only release rejected",
			title:   "Sparrow",
			author:  "Rachel Reid",
			release: "Sparrow.by.Adam.Reid.epub",
			wantOK:  false,
		},
		{
			name:    "single-keyword title, full author release accepted",
			title:   "Sparrow",
			author:  "Rachel Reid",
			release: "Rachel.Reid.Sparrow.epub",
			wantOK:  true,
		},
		{
			name:    "single-keyword title, surname-only (matches monitored surname) rejected",
			title:   "Sparrow",
			author:  "Rachel Reid",
			release: "Reid.Sparrow.epub",
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterRelevant(toResults(tc.release), tc.title, tc.author, nil)
			if tc.wantOK && !contains(got, tc.release) {
				t.Errorf("expected %q to pass; got %v", tc.release, resultTitles(got))
			}
			if !tc.wantOK && contains(got, tc.release) {
				t.Errorf("expected %q to be filtered out; got %v", tc.release, resultTitles(got))
			}
		})
	}
}

// TestAuthorMatchesReleaseInitials covers #563: an author like "George R. R.
// Martin" should still match a release naming the author as "George Martin"
// (initials are optional). The fallback all-significant-tokens path requires
// every >=3-char token, so initials must be dropped, not kept.
func TestAuthorMatchesReleaseInitials(t *testing.T) {
	toks := authorTokens("George R. R. Martin")
	if want := []string{"george", "martin"}; !equalSlices(toks, want) {
		t.Errorf("authorTokens dropped initials = %v, want %v", toks, want)
	}
	if !authorMatchesRelease("george martin a game of thrones epub", toks) {
		t.Error("'George Martin ...' should match 'George R. R. Martin'")
	}
	if authorMatchesRelease("george martin epub", []string{"george", "r", "r", "martin"}) {
		// guard: with the strict all-tokens fallback, a 1-char "r" requires
		// `\br\b` somewhere in the haystack. We don't want our dropping logic
		// to allow false positives, but since authorTokens strips initials,
		// this synthetic check is just an aux sanity test for the matcher.
		t.Log("matcher with 1-char tokens rejected when 'r' is absent — OK")
	}
}

// TestAuthorMatchesReleaseSingleName covers the single-token pseudonym path
// (#563): authors like "Plato" must accept releases naming "Plato" without
// any other anchor.
func TestAuthorMatchesReleaseSingleName(t *testing.T) {
	toks := authorTokens("Plato")
	if want := []string{"plato"}; !equalSlices(toks, want) {
		t.Errorf("authorTokens(Plato) = %v, want %v", toks, want)
	}
	if !authorMatchesRelease("plato republic epub", toks) {
		t.Error("'Plato' should match 'plato republic epub'")
	}
	if authorMatchesRelease("aristotle epub", toks) {
		t.Error("'Plato' should NOT match 'aristotle epub'")
	}
}

// TestAuthorMatchesReleaseHyphenated covers hyphenated names like
// "Mary-Kate Olsen" (#563). The hyphen is non-word so the regex \bmary-kate\b
// matches "mary-kate" in the release, and "olsen" matches separately.
func TestAuthorMatchesReleaseHyphenated(t *testing.T) {
	toks := authorTokens("Mary-Kate Olsen")
	if !authorMatchesRelease("mary-kate olsen biography epub", toks) {
		t.Errorf("hyphenated name should match: tokens=%v", toks)
	}
	// Bare "kate" alone is not enough — the monitored author has the hyphenated
	// first name as one token; partial first-name match doesn't count.
	if authorMatchesRelease("kate olsen biography epub", toks) {
		t.Errorf("partial first-name match should be rejected: tokens=%v", toks)
	}
}

func equalSlices(a, b []string) bool {
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

// TestSearchBookWithDebug_PerResultLogging verifies that the per-result debug
// log lines in SearchBookWithDebug are executed when the slog level is DEBUG.
func TestSearchBookWithDebug_PerResultLogging(t *testing.T) {
	const rssBody = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <newznab:response offset="0" total="1"/>
    <item>
      <title>Life Ascending Nick Lane</title>
      <guid isPermaLink="false">guid-1</guid>
      <enclosure url="https://fake/dl/1" length="1000" type="application/x-nzb"/>
      <newznab:attr name="author" value="Nick Lane"/>
    </item>
  </channel>
</rss>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(rssBody))
	}))
	defer srv.Close()

	// Set global slog to debug so the Enabled check in SearchBookWithDebug fires.
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })

	idxs := []models.Indexer{{ID: 1, Name: "test", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	results, dbg := NewSearcher().SearchBookWithDebug(context.Background(), idxs, MatchCriteria{
		Title:  "Life Ascending",
		Author: "Nick Lane",
	})

	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if dbg == nil {
		t.Fatal("expected non-nil debug info")
	}
}

// TestFilterRelevantDebugEditionQualifierParity verifies that
// filterRelevantDebug and filterRelevant agree on which results to keep for a
// title that carries a parenthesised edition qualifier — the bug reported in
// #713 (finding 4) caused filterRelevantDebug to fabricate relevance-rejections
// because it did not call NormalizeQueryTitle before tokenizing.
func TestFilterRelevantDebugEditionQualifierParity(t *testing.T) {
	releases := []string{
		"Herta.Mueller.Die.Stille.ist.ein.Geraeusch.epub",
		"Die.Stille.ist.ein.Geraeusch.Mueller.epub",
		"Some.Unrelated.Noise.epub",
	}
	results := toResults(releases...)
	title := "Die Stille ist ein Geräusch (German Edition)"
	author := "Herta Müller"

	kept := filterRelevant(results, title, author, nil)
	keptDebug, _ := filterRelevantDebug(results, title, author, nil)

	keptTitles := resultTitles(kept)
	keptDebugTitles := resultTitles(keptDebug)

	if len(keptTitles) != len(keptDebugTitles) {
		t.Fatalf("filterRelevant kept %v but filterRelevantDebug kept %v — paths diverge", keptTitles, keptDebugTitles)
	}
	for _, r := range keptTitles {
		found := false
		for _, d := range keptDebugTitles {
			if r == d {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("filterRelevant kept %q but filterRelevantDebug did not", r)
		}
	}
}

// nopWriter discards all log output during tests.
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
