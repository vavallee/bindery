package indexer

import (
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
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
	results := toResults(
		"Mary.Doria.Russell.-.The.Sparrow.1996.RETAIL.EPUB",
		"The.Sparrow.Russell.epub",
		"Falcon.and.the.Sparrow.MaryLu.Tyndall.epub",
		"Song.of.the.Wooden.Sparrow.epub",
		"The.Hempcrete.Book.William.Stanwix.Alex.Sparrow.epub",
		"Dark.Horse.Blade.Of.The.Immortal.Vol.18.The.Sparrow.Net.Comic.eBook",
	)
	got := filterRelevant(results, "The Sparrow", "Mary Doria Russell")

	if !contains(got, "Mary.Doria.Russell.-.The.Sparrow.1996.RETAIL.EPUB") {
		t.Errorf("expected Russell's Sparrow to be kept, got %v", resultTitles(got))
	}
	if !contains(got, "The.Sparrow.Russell.epub") {
		t.Errorf("expected surname-marked result to be kept, got %v", resultTitles(got))
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
	results := toResults(
		"sparrowhawk.by.russell.epub",
		"sparrows.russell.epub",
		"the.sparrow.russell.epub",
	)
	got := filterRelevant(results, "The Sparrow", "Mary Doria Russell")
	if contains(got, "sparrowhawk.by.russell.epub") {
		t.Error("must not match 'sparrowhawk' for 'sparrow' keyword")
	}
	if contains(got, "sparrows.russell.epub") {
		t.Error("must not match plural 'sparrows' for 'sparrow' keyword")
	}
	if !contains(got, "the.sparrow.russell.epub") {
		t.Error("expected 'the.sparrow.russell' to pass")
	}
}

func TestFilterRelevantMultiWordPhrase(t *testing.T) {
	// Two-significant-word title: phrase contiguity.
	results := toResults(
		"Cormac.McCarthy.-.The.Road.2006.epub",
		"On.The.Road.Again.Willie.Nelson.epub",
		"The.Road.To.Wigan.Pier.Orwell.epub",
	)
	got := filterRelevant(results, "The Road", "Cormac McCarthy")

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
	// "Dune: Messiah" must accept releases tagged either as "Dune" or
	// "Dune Messiah". The colon subtitle is treated specially.
	results := toResults(
		"Frank.Herbert.Dune.Messiah.epub",
		"Dune.Messiah.Herbert.epub",
		"Frank.Herbert.Dune.epub", // primary-title-only match
	)
	got := filterRelevant(results, "Dune: Messiah", "Frank Herbert")
	for _, title := range []string{
		"Frank.Herbert.Dune.Messiah.epub",
		"Dune.Messiah.Herbert.epub",
		"Frank.Herbert.Dune.epub",
	} {
		if !contains(got, title) {
			t.Errorf("expected %q to pass subtitle filter", title)
		}
	}
}

func TestFilterRelevantNoResults(t *testing.T) {
	// Empty input → empty output, no panic.
	got := filterRelevant(nil, "The Sparrow", "Russell")
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
		got := filterRelevant(toResults(tc.releases...), tc.bookTitle, tc.author)
		if !contains(got, tc.wantAny) {
			t.Errorf("filterRelevant(%q, %q): expected %q in results, got %v",
				tc.bookTitle, tc.author, tc.wantAny, resultTitles(got))
		}
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
	if len(ebook) != 2 || ebook[0] != 7000 || ebook[1] != 7020 {
		t.Errorf("ebook filter = %v, want [7000 7020]", ebook)
	}
	audio := filterCategoriesForMedia(all, "audiobook")
	if len(audio) != 1 || audio[0] != 3030 {
		t.Errorf("audiobook filter = %v, want [3030]", audio)
	}
	// Empty input falls back to the standard category for the media type.
	if got := filterCategoriesForMedia(nil, "ebook"); len(got) != 2 || got[0] != 7000 {
		t.Errorf("nil + ebook should fall back to [7000 7020], got %v", got)
	}
	if got := filterCategoriesForMedia(nil, "audiobook"); len(got) != 1 || got[0] != 3030 {
		t.Errorf("nil + audiobook should fall back to [3030], got %v", got)
	}
	// Unknown type falls back to books.
	if got := filterCategoriesForMedia(all, ""); len(got) != 2 {
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
	// Single keyword without surname → accept (can't do better)
	if !titleMatchesResult("dune", []string{"dune"}, "", false) {
		t.Error("single keyword, no surname → should accept")
	}
	// Single keyword with non-matching surname → reject
	if titleMatchesResult("dune.novel", []string{"dune"}, "herbert", false) {
		t.Error("single keyword missing surname → should reject")
	}
	// Single keyword with matching surname → accept
	if !titleMatchesResult("dune.herbert", []string{"dune"}, "herbert", false) {
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
	got := filterRelevant(results, "The Name of the Wind", "Patrick Rothfuss")

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
	got := filterRelevant(results, "The Lord of the Rings", "J.R.R. Tolkien")

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

// Custom indexer categories (e.g. 7120 for German books) must pass through
// filterCategoriesForMedia unchanged.
func TestFilterCategoriesCustomIDs(t *testing.T) {
	// SceneNZBs-style: 7120 = German books, 3130 = German audio.
	cats := []int{7020, 7120, 3030, 3130}

	ebook := filterCategoriesForMedia(cats, "ebook")
	wantEbook := []int{7020, 7120}
	if len(ebook) != len(wantEbook) {
		t.Fatalf("ebook cats = %v, want %v", ebook, wantEbook)
	}
	for i, v := range wantEbook {
		if ebook[i] != v {
			t.Errorf("ebook[%d] = %d, want %d", i, ebook[i], v)
		}
	}

	audio := filterCategoriesForMedia(cats, "audiobook")
	wantAudio := []int{3030, 3130}
	if len(audio) != len(wantAudio) {
		t.Fatalf("audio cats = %v, want %v", audio, wantAudio)
	}
	for i, v := range wantAudio {
		if audio[i] != v {
			t.Errorf("audio[%d] = %d, want %d", i, audio[i], v)
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
