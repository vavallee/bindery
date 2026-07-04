package metadata

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestAggregator_SearchAuthors(t *testing.T) {
	want := []models.Author{{Name: "Frank Herbert", ForeignID: "OL123A"}}
	primary := &mockProvider{name: "ol", searchAuthors: want}
	agg := newTestAggregator(primary)

	got, err := agg.SearchAuthors(context.Background(), "Herbert")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Frank Herbert" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestAggregator_SearchAuthors_Error(t *testing.T) {
	primary := &mockProvider{name: "ol", searchAuthErr: errors.New("network error")}
	agg := newTestAggregator(primary)

	_, err := agg.SearchAuthors(context.Background(), "Herbert")
	if err == nil {
		t.Fatal("expected error to be propagated")
	}
}

func TestAggregator_SearchBooks(t *testing.T) {
	want := []models.Book{{Title: "Dune", ForeignID: "OL456W"}}
	primary := &mockProvider{name: "ol", searchBooks: want}
	agg := newTestAggregator(primary)

	got, err := agg.SearchBooks(context.Background(), "Dune")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Dune" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestAggregator_SearchBooks_MergesEnrichers(t *testing.T) {
	primary := &mockProvider{name: "ol", searchBooks: []models.Book{{Title: "Primary Book", ForeignID: "OL1W"}}}
	enricher := &mockProvider{name: "googlebooks", searchBooks: []models.Book{{Title: "Enricher Book", ForeignID: "gb:abc"}}}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.SearchBooks(context.Background(), "x")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 merged results, got %d: %+v", len(got), got)
	}
	// Primary results must rank first.
	if got[0].ForeignID != "OL1W" || got[1].ForeignID != "gb:abc" {
		t.Errorf("expected primary first then enricher, got %+v", got)
	}
}

func TestAggregator_SearchBooks_DedupesByISBN(t *testing.T) {
	primary := &mockProvider{name: "ol", searchBooks: []models.Book{{Title: "Dune", ForeignID: "OL1W", ISBNs: []string{"978-0-441-17271-9"}}}}
	enricher := &mockProvider{name: "googlebooks", searchBooks: []models.Book{{Title: "Dune (different edition)", ForeignID: "gb:dup", ISBNs: []string{"9780441172719"}}}}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.SearchBooks(context.Background(), "dune")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(got) != 1 || got[0].ForeignID != "OL1W" {
		t.Errorf("ISBN dup should collapse to the primary copy, got %+v", got)
	}
}

func TestAggregator_SearchBooks_DedupesByTitleAuthor(t *testing.T) {
	primary := &mockProvider{name: "ol", searchBooks: []models.Book{{Title: "The Water Knife", ForeignID: "OL1W", Author: &models.Author{Name: "Paolo Bacigalupi"}}}}
	enricher := &mockProvider{name: "googlebooks", searchBooks: []models.Book{{Title: "the water knife!", ForeignID: "gb:dup", Author: &models.Author{Name: "Paolo  Bacigalupi"}}}}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.SearchBooks(context.Background(), "water knife")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(got) != 1 || got[0].ForeignID != "OL1W" {
		t.Errorf("title+author dup should collapse to primary, got %+v", got)
	}
}

func TestAggregator_SearchBooks_KeepsDistinctFormats(t *testing.T) {
	// Same work, two formats from two providers: must NOT collapse to one — the
	// user needs to see and pick ebook vs audiobook.
	primary := &mockProvider{name: "ol", searchBooks: []models.Book{
		{Title: "The Water Knife", ForeignID: "OL1W", Author: &models.Author{Name: "Paolo Bacigalupi"}, MediaType: models.MediaTypeEbook},
	}}
	enricher := &mockProvider{name: "hardcover", searchBooks: []models.Book{
		{Title: "The Water Knife", ForeignID: "hc:wk", Author: &models.Author{Name: "Paolo Bacigalupi"}, MediaType: models.MediaTypeAudiobook},
	}}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.SearchBooks(context.Background(), "water knife")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ebook + audiobook of the same work must both survive, got %d: %+v", len(got), got)
	}
	formats := map[string]bool{}
	for _, b := range got {
		formats[b.MediaType] = true
	}
	if !formats[models.MediaTypeEbook] || !formats[models.MediaTypeAudiobook] {
		t.Errorf("expected both ebook and audiobook formats present, got %+v", formats)
	}
}

func TestAggregator_SearchBooks_StillDedupesSameFormat(t *testing.T) {
	// Same work, same format (one unspecified, treated as ebook) from two
	// providers: still collapses to the primary copy.
	primary := &mockProvider{name: "ol", searchBooks: []models.Book{
		{Title: "Dune", ForeignID: "OL1W", Author: &models.Author{Name: "Frank Herbert"}, MediaType: models.MediaTypeEbook},
	}}
	enricher := &mockProvider{name: "googlebooks", searchBooks: []models.Book{
		{Title: "Dune", ForeignID: "gb:dune", Author: &models.Author{Name: "Frank Herbert"}}, // unspecified == ebook
	}}
	agg := newTestAggregator(primary, enricher)

	got, _ := agg.SearchBooks(context.Background(), "dune")
	if len(got) != 1 || got[0].ForeignID != "OL1W" {
		t.Errorf("same-format dup should collapse to the primary copy, got %+v", got)
	}
}

func TestAggregator_SearchBooks_SkipsErroredProvider(t *testing.T) {
	primary := &mockProvider{name: "ol", searchBookErr: errors.New("openlibrary down")}
	enricher := &mockProvider{name: "googlebooks", searchBooks: []models.Book{{Title: "Found Anyway", ForeignID: "gb:x"}}}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.SearchBooks(context.Background(), "x")
	if err != nil {
		t.Fatalf("a failing provider must not fail the whole search: %v", err)
	}
	if len(got) != 1 || got[0].ForeignID != "gb:x" {
		t.Errorf("expected the enricher result, got %+v", got)
	}
}

func TestAggregator_SearchBooks_AllErrorReturnsError(t *testing.T) {
	primary := &mockProvider{name: "ol", searchBookErr: errors.New("ol down")}
	enricher := &mockProvider{name: "googlebooks", searchBookErr: errors.New("gb down")}
	agg := newTestAggregator(primary, enricher)

	if _, err := agg.SearchBooks(context.Background(), "x"); err == nil {
		t.Error("expected an error when every provider fails")
	}
}

func TestAggregator_SearchBooks_NotConfiguredEnricherIsSilentlySkipped(t *testing.T) {
	primary := &mockProvider{name: "ol", searchBooks: []models.Book{{Title: "Primary", ForeignID: "OL1W"}}}
	enricher := &mockProvider{name: "hardcover", searchBookErr: ErrProviderNotConfigured}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.SearchBooks(context.Background(), "x")
	if err != nil {
		t.Fatalf("not-configured enricher must not error the search: %v", err)
	}
	if len(got) != 1 || got[0].ForeignID != "OL1W" {
		t.Errorf("expected just the primary result, got %+v", got)
	}
}

func TestSearchRelevance_TierOrdering(t *testing.T) {
	q := "the meaning of your life"
	s := func(title string) float64 { return searchRelevance(title, q) }
	exact := s("The Meaning of Your Life")
	prefix := s("The Meaning of Your Life: Finding Purpose in an Age of Emptiness")
	substr := s("Work: The Meaning of Your Life - A Christian Perspective")
	tokens := s("Meaning of YOUR Life") // all non-stopword tokens, but not a substring (no leading "the")
	none := s("King Lear")

	if !(exact > prefix && prefix > substr && substr > tokens && tokens > none) {
		t.Errorf("tier order violated: exact=%.3f prefix=%.3f substr=%.3f tokens=%.3f none=%.3f",
			exact, prefix, substr, tokens, none)
	}
	if none != 0 {
		t.Errorf("unrelated title should score 0, got %.3f", none)
	}
}

func TestSearchRelevance_ShorterTitleWinsWithinTier(t *testing.T) {
	q := "the meaning of your life"
	short := searchRelevance("The Meaning of Your Life: A Guide", q)
	long := searchRelevance("The Meaning of Your Life: Finding Purpose in an Age of Emptiness", q)
	if !(short > long) {
		t.Errorf("tighter title should outrank looser one in the same tier: short=%.3f long=%.3f", short, long)
	}
}

// TestAggregator_SearchBooks_RanksRelevanceAcrossProviders is the headline fix:
// a strong title match from an enricher must outrank a weaker provider's block.
func TestBookSearchRelevance_AuthorAware(t *testing.T) {
	q := "the meaning of your life arthur brooks"
	real := bookSearchRelevance(models.Book{Title: "The Meaning of Your Life", Author: &models.Author{Name: "Arthur C. Brooks"}}, q)
	summary := bookSearchRelevance(models.Book{Title: "The Meaning of Your Life & Arthur Brooks' Insights", Author: &models.Author{Name: "Colston Silvester"}}, q)
	wrongAuthor := bookSearchRelevance(models.Book{Title: "The Meaning of Your Life", Author: &models.Author{Name: "Filip Swennen"}}, q)

	if !(real > summary) {
		t.Errorf("real book (author matches query) must outrank a summary that crams the author into its title: real=%.3f summary=%.3f", real, summary)
	}
	if !(real > wrongAuthor) {
		t.Errorf("right-author book must outrank same-title wrong-author: real=%.3f wrong=%.3f", real, wrongAuthor)
	}
	// A real edition by the queried author, even with a long subtitle (weaker
	// title match), must still beat a non-matching summary.
	subtitleEdition := bookSearchRelevance(models.Book{Title: "The Meaning of Your Life: Finding Purpose in an Age of Emptiness", Author: &models.Author{Name: "Arthur C. Brooks"}}, q)
	if !(subtitleEdition > summary) {
		t.Errorf("a real Brooks edition (author matches) should outrank the non-matching summary: %.3f vs %.3f", subtitleEdition, summary)
	}
}

func TestBookSearchRelevance_NoAuthorRegression(t *testing.T) {
	// A plain title query (no author tokens) must score exactly as title-only.
	q := "the meaning of your life"
	b := models.Book{Title: "The Meaning of Your Life", Author: &models.Author{Name: "Arthur C. Brooks"}}
	if got, want := bookSearchRelevance(b, q), searchRelevance(b.Title, normalizeForDedup(q)); got != want {
		t.Errorf("title-only query should equal title relevance: got %.3f want %.3f", got, want)
	}
}

func TestAggregator_SearchBooks_RanksRelevanceAcrossProviders(t *testing.T) {
	primary := &mockProvider{name: "ol", searchBooks: []models.Book{
		{Title: "Bible", ForeignID: "OLb"},
		{Title: "King Lear", ForeignID: "OLk"},
		{Title: "Discover the Meaning of Your Life", ForeignID: "OLd"},
	}}
	enricher := &mockProvider{name: "hardcover", searchBooks: []models.Book{
		{Title: "The Meaning of Your Life: Finding Purpose in an Age of Emptiness", ForeignID: "hc:brooks", RatingsCount: 500},
	}}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.SearchBooks(context.Background(), "the meaning of your life")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 results, got %d", len(got))
	}
	if got[0].ForeignID != "hc:brooks" {
		t.Errorf("prefix match from the enricher must rank first; got %q (%s)", got[0].Title, got[0].ForeignID)
	}
	last := got[len(got)-1].Title
	if last != "Bible" && last != "King Lear" {
		t.Errorf("unrelated titles should sink to the bottom; last=%q", last)
	}
}

func TestAggregator_SearchBooks_RatingsTiebreakOnlyWhenBothPresent(t *testing.T) {
	// Two equally-good substring matches: one from OL (RatingsCount 0 = unknown),
	// one from HC with ratings. The HC one must NOT auto-jump the OL one purely
	// because OL never reports ratings — equal score + one-side-unknown falls back
	// to original (provider-first) order.
	primary := &mockProvider{name: "ol", searchBooks: []models.Book{
		{Title: "The Meaning of Your Life", ForeignID: "OL1"}, // exact, ratings 0
	}}
	enricher := &mockProvider{name: "hardcover", searchBooks: []models.Book{
		{Title: "The Meaning of Your Life", ForeignID: "hc:1", RatingsCount: 9999}, // dup exact, high ratings
	}}
	agg := newTestAggregator(primary, enricher)
	got, _ := agg.SearchBooks(context.Background(), "the meaning of your life")
	// Deduped to one (same title+author); the primary copy is kept regardless of HC's rating.
	if len(got) != 1 || got[0].ForeignID != "OL1" {
		t.Errorf("expected the primary copy kept on dup, got %+v", got)
	}
}

func TestAggregator_SearchBooks_NumericQuerySkipsRerank(t *testing.T) {
	primary := &mockProvider{name: "ol", searchBooks: []models.Book{
		{Title: "Zeta", ForeignID: "OL1"},
		{Title: "Alpha", ForeignID: "OL2"},
	}}
	agg := newTestAggregator(primary)
	got, _ := agg.SearchBooks(context.Background(), "9780441172719")
	if len(got) != 2 || got[0].ForeignID != "OL1" {
		t.Errorf("numeric/ISBN query should preserve original order, got %+v", got)
	}
}

func TestCanonicalAuthorKey(t *testing.T) {
	want := "arthur c brooks"
	for _, name := range []string{"Arthur C. Brooks", "Arthur C Brooks", "Brooks, Arthur C.", "  brooks ,  arthur c "} {
		if got := canonicalAuthorKey(name); got != want {
			t.Errorf("canonicalAuthorKey(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCanonicalAuthorKey_FoldsDuplicatedProviderNoise(t *testing.T) {
	if got, want := canonicalAuthorKey("Black, Chuck, Black, Chuck"), canonicalAuthorKey("Chuck Black"); got != want {
		t.Fatalf("canonicalAuthorKey duplicated provider noise = %q, want %q", got, want)
	}
}

func TestDuplicatedAuthorName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "Black, Chuck, Black, Chuck", want: true},
		{name: "Chuck Black", want: false},
		{name: "Black, Chuck, Jr.", want: false},
		{name: "", want: false},
	}
	for _, tt := range tests {
		if got := duplicatedAuthorName(tt.name); got != tt.want {
			t.Errorf("duplicatedAuthorName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestAuthorRelevance_InversionAware(t *testing.T) {
	q := "arthur c brooks"
	if natural := authorRelevance("Arthur C. Brooks", q); natural != 1.0 {
		t.Errorf("natural exact-match want 1.0, got %.3f", natural)
	}
	if inverted := authorRelevance("Brooks, Arthur C.", q); inverted < 0.99 {
		t.Errorf("comma-inverted form should match as exact, got %.3f", inverted)
	}
}

// TestAggregator_SearchAuthors_CollapsesFragments is the headline author fix:
// OpenLibrary's fragmented same-person records (and a cross-provider dup) collapse
// to ONE record — the most-complete one (most works) — and rank by name match.
func TestAggregator_SearchAuthors_CollapsesFragments(t *testing.T) {
	stats := func(n int) *models.AuthorStats { return &models.AuthorStats{BookCount: n} }
	primary := &mockProvider{name: "ol", searchAuthors: []models.Author{
		{Name: "Arthur C. Brooks", ForeignID: "OL1A", Statistics: stats(7)},
		{Name: "Arthur C Brooks", ForeignID: "OL2A", Statistics: stats(2)},
		{Name: "Brooks, Arthur C.", ForeignID: "OL3A", Statistics: stats(14)},
		{Name: "Brooks, Arthur C.", ForeignID: "OL4A", Statistics: stats(9)},
		{Name: "Some Other Author", ForeignID: "OL9A", Statistics: stats(3)},
	}}
	enricher := &mockProvider{name: "hardcover", searchAuthors: []models.Author{
		{Name: "Arthur C. Brooks", ForeignID: "hc:arthur-c-brooks"}, // BookCount unknown (nil Statistics)
	}}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.SearchAuthors(context.Background(), "arthur c brooks")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	brooks := 0
	var kept models.Author
	for _, a := range got {
		if canonicalAuthorKey(a.Name) == "arthur c brooks" {
			brooks++
			kept = a
		}
	}
	if brooks != 1 {
		t.Fatalf("Brooks fragments + cross-provider dup should collapse to 1, got %d", brooks)
	}
	if kept.ForeignID != "OL3A" {
		t.Errorf("collapsed record should be the most-complete (OL3A, 14 works), got %s (BookCount %d)", kept.ForeignID, authorBookCount(kept))
	}
	if got[0].ForeignID != "OL3A" {
		t.Errorf("the collapsed Brooks (exact name match) should rank first, got %s", got[0].ForeignID)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 results (collapsed Brooks + Some Other Author), got %d", len(got))
	}
}

func TestAggregator_SearchAuthors_PrefersCleanNameOverDuplicatedProviderNoise(t *testing.T) {
	stats := func(n int) *models.AuthorStats { return &models.AuthorStats{BookCount: n} }
	primary := &mockProvider{name: "ol", searchAuthors: []models.Author{
		{Name: "Chuck Black", ForeignID: "OL1480449A", Statistics: stats(47), RatingsCount: 14},
		{Name: "Black, Chuck, Black, Chuck", ForeignID: "OL11441410A", Statistics: stats(1)},
	}}
	agg := newTestAggregator(primary)

	got, err := agg.SearchAuthors(context.Background(), "Chuck Black")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("duplicated provider-noise row should collapse into the clean author, got %d: %+v", len(got), got)
	}
	if got[0].ForeignID != "OL1480449A" || got[0].Name != "Chuck Black" {
		t.Fatalf("kept author = %+v, want clean Chuck Black record", got[0])
	}
}

func TestAggregator_SearchAuthors_RanksRelevanceAcrossProviders(t *testing.T) {
	primary := &mockProvider{name: "ol", searchAuthors: []models.Author{
		{Name: "Arthur Conan Doyle", ForeignID: "OLx", Statistics: &models.AuthorStats{BookCount: 50}}, // weak match, popular
	}}
	enricher := &mockProvider{name: "hardcover", searchAuthors: []models.Author{
		{Name: "Arthur C. Brooks", ForeignID: "hc:brooks"}, // exact match
	}}
	agg := newTestAggregator(primary, enricher)

	got, err := agg.SearchAuthors(context.Background(), "arthur c brooks")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(got) == 0 || got[0].ForeignID != "hc:brooks" {
		t.Errorf("exact name match from enricher must outrank a popular weak match; got %+v", got)
	}
}

func TestAggregator_GetAuthor_Success(t *testing.T) {
	author := &models.Author{Name: "Ursula K. Le Guin", ForeignID: "OL111A"}
	primary := &mockProvider{name: "ol", getAuthor: author}
	agg := newTestAggregator(primary)

	got, err := agg.GetAuthor(context.Background(), "OL111A")
	if err != nil {
		t.Fatalf("GetAuthor: %v", err)
	}
	if got.Name != "Ursula K. Le Guin" {
		t.Errorf("Name: want 'Ursula K. Le Guin', got %q", got.Name)
	}
}

func TestAggregator_GetAuthor_Cached(t *testing.T) {
	calls := 0
	primary := &mockProvider{name: "ol", getAuthor: &models.Author{Name: "Isaac Asimov"}}
	agg := newTestAggregator(primary)
	// Wrap to count calls
	origGetAuthor := primary.getAuthor

	_, _ = agg.GetAuthor(context.Background(), "OL999A")
	calls++                 // first call
	primary.getAuthor = nil // second call should use cache, not nil author
	got, err := agg.GetAuthor(context.Background(), "OL999A")
	if err != nil {
		t.Fatalf("GetAuthor (cached): %v", err)
	}
	if got.Name != origGetAuthor.Name {
		t.Errorf("expected cached author, got %+v", got)
	}
	_ = calls
}

func TestAggregator_GetAuthor_Error(t *testing.T) {
	primary := &mockProvider{name: "ol", getAuthorErr: errors.New("not found")}
	agg := newTestAggregator(primary)

	_, err := agg.GetAuthor(context.Background(), "OL999A")
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestAggregator_GetBook_LongDescription(t *testing.T) {
	// A book with a long description should NOT trigger enrichment.
	longDesc := string(make([]byte, 100)) // 100-char description
	for i := range longDesc {
		_ = i
	}
	longDesc = "This is a very long book description that exceeds the fifty character minimum and should never be enriched by secondary providers."
	book := &models.Book{Title: "Dune", Description: longDesc}

	enricherCalled := false
	enricher := &mockProvider{
		name:        "gb",
		searchBooks: []models.Book{{Description: "Should not be used"}},
	}
	// We'll detect if enricher was called by overriding its SearchBooks
	_ = enricherCalled
	primary := &mockProvider{name: "ol", getBook: book}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{enricher},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetBook(context.Background(), "OL456W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if got.Description != longDesc {
		t.Errorf("description should not be overwritten when long enough")
	}
}

func TestAggregator_GetBook_ShortDescription_Enriched(t *testing.T) {
	shortDesc := "Short."
	richerDesc := "A much richer description that is longer than the short one from the primary provider."

	primary := &mockProvider{
		name:    "ol",
		getBook: &models.Book{Title: "Foundation", Description: shortDesc},
	}
	enricher := &mockProvider{
		name:        "gb",
		searchBooks: []models.Book{{Title: "Foundation", Description: richerDesc}},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{enricher},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetBook(context.Background(), "OL789W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if got.Description != richerDesc {
		t.Errorf("expected enriched description %q, got %q", richerDesc, got.Description)
	}
}

func TestAggregator_GetBook_Enrichment_RatingFilled(t *testing.T) {
	primary := &mockProvider{
		name:    "ol",
		getBook: &models.Book{Title: "Short", Description: "x", AverageRating: 0},
	}
	enricher := &mockProvider{
		name:        "hc",
		searchBooks: []models.Book{{Title: "Short", Description: "Some desc", AverageRating: 4.5, RatingsCount: 100}},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{enricher},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetBook(context.Background(), "OL001W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if got.AverageRating != 4.5 {
		t.Errorf("rating: want 4.5, got %f", got.AverageRating)
	}
	if got.RatingsCount != 100 {
		t.Errorf("ratingsCount: want 100, got %d", got.RatingsCount)
	}
}

func TestAggregator_GetBook_Enrichment_GenresFromHardcover(t *testing.T) {
	// Hardcover's curated taxonomy replaces OpenLibrary's noisy subjects.
	primary := &mockProvider{
		name:    "ol",
		getBook: &models.Book{Title: "Mistborn", Description: "x", Genres: []string{"Fiction", "American literature", "Large type books"}},
	}
	hc := &mockProvider{
		name:        "hardcover",
		searchBooks: []models.Book{{Title: "Mistborn", Description: "Some desc", Genres: []string{"Fantasy", "Epic Fantasy"}}},
	}
	agg := &Aggregator{primary: primary, enrichers: []Provider{hc}, cache: newTTLCache(time.Minute)}

	got, err := agg.GetBook(context.Background(), "OL100W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if want := []string{"Fantasy", "Epic Fantasy"}; !slices.Equal(got.Genres, want) {
		t.Errorf("genres: want %v, got %v", want, got.Genres)
	}
}

func TestAggregator_GetBook_Enrichment_NonHardcoverGenresIgnored(t *testing.T) {
	// Google Books ships slash-delimited BISAC strings; they must not replace
	// the existing genres (gated to Hardcover provenance).
	olGenres := []string{"Fiction"}
	primary := &mockProvider{
		name:    "ol",
		getBook: &models.Book{Title: "Dune", Description: "x", Genres: olGenres},
	}
	gb := &mockProvider{
		name:        "googlebooks",
		searchBooks: []models.Book{{Title: "Dune", Description: "Some desc", Genres: []string{"Fiction / Science Fiction / General"}}},
	}
	agg := &Aggregator{primary: primary, enrichers: []Provider{gb}, cache: newTTLCache(time.Minute)}

	got, err := agg.GetBook(context.Background(), "OL200W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if !slices.Equal(got.Genres, olGenres) {
		t.Errorf("genres should be untouched by non-Hardcover enricher: want %v, got %v", olGenres, got.Genres)
	}
}

func TestAggregator_GetBook_Enrichment_EmptyHardcoverGenresDoNotBlank(t *testing.T) {
	olGenres := []string{"Fiction"}
	primary := &mockProvider{
		name:    "ol",
		getBook: &models.Book{Title: "Dune", Description: "x", Genres: olGenres},
	}
	hc := &mockProvider{
		name:        "hardcover",
		searchBooks: []models.Book{{Title: "Dune", Description: "Some desc"}}, // no genres
	}
	agg := &Aggregator{primary: primary, enrichers: []Provider{hc}, cache: newTTLCache(time.Minute)}

	got, err := agg.GetBook(context.Background(), "OL300W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if !slices.Equal(got.Genres, olGenres) {
		t.Errorf("existing genres must survive an empty Hardcover result: want %v, got %v", olGenres, got.Genres)
	}
}

func TestAggregator_GetBook_Cached(t *testing.T) {
	primary := &mockProvider{name: "ol", getBook: &models.Book{Title: "Cached Book", Description: "A sufficiently long description for caching test purposes here."}}
	agg := newTestAggregator(primary)

	first, _ := agg.GetBook(context.Background(), "OL111W")
	primary.getBook = nil // clear so second call must use cache

	second, err := agg.GetBook(context.Background(), "OL111W")
	if err != nil {
		t.Fatalf("GetBook (cache): %v", err)
	}
	if second.Title != first.Title {
		t.Errorf("cached book mismatch: got %q", second.Title)
	}
}

func TestAggregator_GetBook_Error(t *testing.T) {
	primary := &mockProvider{name: "ol", getBookErr: errors.New("lookup failed")}
	agg := newTestAggregator(primary)

	_, err := agg.GetBook(context.Background(), "OL999W")
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestAggregator_GetBook_RoutesProviderPrefixes(t *testing.T) {
	primary := &mockProvider{name: "openlibrary", getBook: &models.Book{Title: "Wrong"}}
	google := &mockProvider{name: "googlebooks", getBook: &models.Book{ForeignID: "gb:vol1", Title: "Google Book", MetadataProvider: "googlebooks"}}
	hardcover := &mockProvider{name: "hardcover", getBook: &models.Book{ForeignID: "hc:book", Title: "Hardcover Book", MetadataProvider: "hardcover"}}
	dnb := &mockProvider{name: "dnb", getBook: &models.Book{ForeignID: "dnb:123", Title: "DNB Book", MetadataProvider: "dnb"}}
	agg := newTestAggregator(primary, google, hardcover, dnb)

	tests := []struct {
		foreignID string
		wantTitle string
		provider  *mockProvider
	}{
		{foreignID: "gb:vol1", wantTitle: "Google Book", provider: google},
		{foreignID: "hc:book", wantTitle: "Hardcover Book", provider: hardcover},
		{foreignID: "dnb:123", wantTitle: "DNB Book", provider: dnb},
	}
	for _, tt := range tests {
		got, err := agg.GetBook(context.Background(), tt.foreignID)
		if err != nil {
			t.Fatalf("GetBook(%q): %v", tt.foreignID, err)
		}
		if got == nil || got.Title != tt.wantTitle {
			t.Fatalf("GetBook(%q) = %+v, want %s", tt.foreignID, got, tt.wantTitle)
		}
		if tt.provider.getBookCalls != 1 || tt.provider.gotBookIDs[0] != tt.foreignID {
			t.Fatalf("%s calls=%d ids=%v, want one %s", tt.provider.name, tt.provider.getBookCalls, tt.provider.gotBookIDs, tt.foreignID)
		}
	}
	if primary.getBookCalls != 0 {
		t.Fatalf("primary get calls = %d, want 0", primary.getBookCalls)
	}
}

func TestAggregator_GetAuthor_RoutesProviderPrefixes(t *testing.T) {
	primary := &mockProvider{name: "openlibrary", getAuthor: &models.Author{Name: "Wrong"}}
	hardcover := &mockProvider{name: "hardcover", getAuthor: &models.Author{ForeignID: "hc:author", Name: "Hardcover Author", MetadataProvider: "hardcover"}}
	agg := newTestAggregator(primary, hardcover)

	got, err := agg.GetAuthor(context.Background(), "hc:author")
	if err != nil {
		t.Fatalf("GetAuthor: %v", err)
	}
	if got == nil || got.Name != "Hardcover Author" {
		t.Fatalf("got %+v, want Hardcover Author", got)
	}
}

func TestAggregator_GetEditions_Success(t *testing.T) {
	editions := []models.Edition{{Title: "1st ed."}, {Title: "2nd ed."}}
	primary := &mockProvider{name: "ol", getEditions: editions}
	agg := newTestAggregator(primary)

	got, err := agg.GetEditions(context.Background(), "OL456W")
	if err != nil {
		t.Fatalf("GetEditions: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 editions, got %d", len(got))
	}
}

func TestAggregator_GetEditions_Cached(t *testing.T) {
	editions := []models.Edition{{Title: "Paperback"}}
	primary := &mockProvider{name: "ol", getEditions: editions}
	agg := newTestAggregator(primary)

	_, _ = agg.GetEditions(context.Background(), "OL999W")
	primary.getEditions = nil // clear; second call must use cache

	got, err := agg.GetEditions(context.Background(), "OL999W")
	if err != nil {
		t.Fatalf("GetEditions (cache): %v", err)
	}
	if len(got) != 1 || got[0].Title != "Paperback" {
		t.Errorf("cached editions mismatch: %+v", got)
	}
}

func TestAggregator_GetEditionsFromProvider_RoutesUnprefixedID(t *testing.T) {
	primary := &mockProvider{name: "ol"}
	hardcover := &mockProvider{name: "hardcover", getEditions: []models.Edition{{Title: "Audio"}}}
	agg := newTestAggregator(primary, hardcover)

	got, err := agg.GetEditionsFromProvider(context.Background(), "hardcover", "123")
	if err != nil {
		t.Fatalf("GetEditionsFromProvider: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Audio" {
		t.Fatalf("unexpected editions: %+v", got)
	}
}

// TestAggregator_ResolveBookByISBN_AcceptsDNBWithSyntheticAuthorID is the
// regression test for #608: prior to the DNB-author-foreign-id fix, the
// aggregator silently dropped DNB-only ISBN hits because Author.ForeignID
// was empty. Now that DNB populates a synthetic "dnb:gnd:" (or
// "dnb:author:") ForeignID, ResolveBookByISBN must accept it.
func TestAggregator_ResolveBookByISBN_AcceptsDNBWithSyntheticAuthorID(t *testing.T) {
	primary := &mockProvider{name: "openlibrary"} // OL doesn't have this ISBN.
	dnb := &mockProvider{name: "dnb", getByISBN: &models.Book{
		ForeignID: "dnb:bib-001",
		Title:     "Der Wüstenplanet",
		Author: &models.Author{
			ForeignID:        "dnb:gnd:118585665",
			Name:             "Frank Herbert",
			SortName:         "Herbert, Frank",
			MetadataProvider: "dnb",
		},
	}}
	agg := newTestAggregator(primary, dnb)

	got, err := agg.ResolveBookByISBN(context.Background(), "9783453198975")
	if err != nil {
		t.Fatalf("ResolveBookByISBN: %v", err)
	}
	if got == nil {
		t.Fatal("expected DNB result with synthetic author ForeignID to be accepted, got nil")
		return
	}
	if got.Author == nil || got.Author.ForeignID != "dnb:gnd:118585665" {
		t.Errorf("unexpected resolved author: %+v", got.Author)
	}
}

// TestAggregator_ResolveBookByISBN_StillSkipsResultsWithoutAuthorID guards
// the inverse: a provider that genuinely returns a book without any author
// ForeignID is still dropped, so the caller sees nil instead of a placeholder
// row it can't persist.
func TestAggregator_ResolveBookByISBN_StillSkipsResultsWithoutAuthorID(t *testing.T) {
	primary := &mockProvider{name: "openlibrary", getByISBN: &models.Book{
		Title:  "Title Only",
		Author: &models.Author{Name: "Unknown", ForeignID: ""},
	}}
	agg := newTestAggregator(primary)

	got, err := agg.ResolveBookByISBN(context.Background(), "9780000000000")
	if err != nil {
		t.Fatalf("ResolveBookByISBN: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when no provider has an author ForeignID, got %+v", got)
	}
}
