package metadata

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

type worksProvider interface {
	GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error)
}

// workLanguageFiller is the optional capability a provider implements when it
// can derive a work-level language for books that arrived without one (e.g.
// OpenLibrary, whose works carry no language and must be edition-sampled; #891).
type workLanguageFiller interface {
	FillMissingWorkLanguages(ctx context.Context, books []models.Book) int
}

// FillMissingAuthorWorkLanguages asks the primary provider to derive a language
// for any book in books that has Language=="" by edition-sampling, mutating the
// slice in place. It is a no-op when the primary provider lacks the capability.
//
// Callers should gate this on the active metadata profile actually restricting
// language: the edition sampling is bounded but still costs upstream
// round-trips, which are wasted when allowed_languages is "any" and every book
// passes the filter regardless of its language (#891).
func (a *Aggregator) FillMissingAuthorWorkLanguages(ctx context.Context, books []models.Book) int {
	if filler, ok := a.primary.(workLanguageFiller); ok {
		return filler.FillMissingWorkLanguages(ctx, books)
	}
	return 0
}

// workCoverFiller is the optional capability a provider implements when it can
// backfill a missing cover for a work by sampling its editions (OpenLibrary,
// whose work records often lack a cover their editions carry; #1748).
type workCoverFiller interface {
	FillMissingWorkCovers(ctx context.Context, books []models.Book) int
}

type authorWorksByNameProvider interface {
	Name() string
	GetAuthorWorksByName(ctx context.Context, authorName string) ([]models.Book, error)
}

// authorWorksByIdentityProvider is the optional capability of selecting an
// author's works by that provider's own author id, rather than by name (#1734).
//
// Name matching cannot separate two real people who publish under the same
// name, so it merges their catalogues: one author picks up the other's books
// and every metadata refresh re-applies the mistake. Once the row is linked to
// a specific upstream author there is a better identity available, and this is
// how a provider offers to use it.
type authorWorksByIdentityProvider interface {
	Name() string
	GetAuthorWorksByIdentity(ctx context.Context, providerAuthorID string) ([]models.Book, error)
}

// authorWorksSupplement fetches one provider's supplemental catalogue for an
// author, preferring identity over name whenever both are available.
//
// Falling back to the name query when an identity lookup returns nothing would
// defeat the point: the name query is what merges same-named authors, so
// widening to it at the moment the caller has a precise identity would
// reintroduce exactly the bug this avoids. An identity that finds nothing means
// the author genuinely has no works there.
func (a *Aggregator) authorWorksSupplement(ctx context.Context, provider authorWorksByNameProvider, author models.Author, authorName string) ([]models.Book, error) {
	if ip, ok := provider.(authorWorksByIdentityProvider); ok {
		if id, known := author.ProviderIdentity(provider.Name()); known {
			slog.Debug("author works supplement by identity",
				"provider", provider.Name(), "author", authorName, "identity", id)
			return ip.GetAuthorWorksByIdentity(ctx, id)
		}
	}
	return provider.GetAuthorWorksByName(ctx, authorName)
}

// GetAuthorWorks fetches all works by an author using the dedicated primary
// provider endpoint. It retains the legacy foreign-ID-only behavior for tests
// and existing callers; author ingestion should use GetAuthorWorksForAuthor so
// enrichers can run author-scoped supplemental queries.
func (a *Aggregator) GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	key := "authorworks:" + authorForeignID
	if cached, ok := a.cache.get(key); ok {
		return cloneBooks(cached.([]models.Book)), nil
	}

	books, err := a.rawPrimaryAuthorWorks(ctx, authorForeignID)
	if err != nil {
		return nil, err
	}
	a.enrichMissingAuthorWorkCovers(ctx, books)
	a.cache.set(key, cloneBooks(books))
	return books, nil
}

// GetAuthorWorksForAuthor fetches the primary provider's author works and
// merges any author-scoped supplemental catalogs from enrichers before falling
// back to per-title cover enrichment for remaining gaps.
func (a *Aggregator) GetAuthorWorksForAuthor(ctx context.Context, author models.Author) ([]models.Book, error) {
	// The identity is part of the key: a relink changes which upstream author
	// the supplements query without changing ForeignID or Name in every case,
	// and serving the pre-relink answer would hide the fix (#1734).
	key := "authorworks-author:" + author.ForeignID + ":" +
		strings.ToLower(strings.TrimSpace(author.Name)) + ":" + authorIdentityCacheKey(author)
	if cached, ok := a.cache.get(key); ok {
		return cloneBooks(cached.([]models.Book)), nil
	}

	books, err := a.rawPrimaryAuthorWorks(ctx, author.ForeignID)
	if err != nil {
		return nil, err
	}

	authorName := strings.TrimSpace(author.Name)
	supplementsComplete := true
	compilationTitles := map[string]struct{}{}
	if authorName != "" {
		for _, provider := range a.authorWorksByNameProviders() {
			supplemental, err := a.authorWorksSupplement(ctx, provider, author, authorName)
			if err != nil {
				supplementsComplete = false
				if errors.Is(err, ErrProviderNotConfigured) {
					continue
				}
				slog.Warn("author works supplement failed", "provider", provider.Name(), "author", authorName, "error", err)
				continue
			}
			if len(supplemental) == 0 {
				continue
			}
			// Enrichers like Hardcover classify which of an author's works are
			// compilations / omnibuses / box sets. Record those titles so the
			// matching primary-provider (e.g. OpenLibrary) works can be pruned
			// below, and drop the compilation entries themselves instead of
			// merging them — otherwise the same content surfaces as several
			// "bundle" rows, the clutter users see on author refresh.
			kept := supplemental[:0]
			for _, b := range supplemental {
				if b.IsCompilation {
					if k := authorWorkMergeKey(b.Title); k != "" {
						compilationTitles[k] = struct{}{}
					}
					continue
				}
				kept = append(kept, b)
			}
			books = mergeAuthorWorks(books, kept)
		}
	}
	books = pruneAuthorWorkCompilations(books, compilationTitles)
	books = pruneAuthorWorkRedundantTitles(books)
	books = pruneAuthorWorkSubjectOutliers(books)
	books = pruneAuthorWorkSelfReference(books, author)

	a.enrichMissingAuthorWorkCovers(ctx, books)
	if supplementsComplete {
		a.cache.set(key, cloneBooks(books))
	}
	return books, nil
}

func (a *Aggregator) rawPrimaryAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	key := "authorworks-raw:" + authorForeignID
	if cached, ok := a.cache.get(key); ok {
		return cloneBooks(cached.([]models.Book)), nil
	}

	books, err := a.primaryAuthorWorks(ctx, authorForeignID)
	if err != nil {
		return nil, err
	}
	a.cache.set(key, cloneBooks(books))
	return cloneBooks(books), nil
}

func (a *Aggregator) primaryAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	provider := a.providerForForeignID(authorForeignID)
	if provider == nil {
		return nil, nil
	}
	if wp, ok := provider.(worksProvider); ok {
		return wp.GetAuthorWorks(ctx, authorForeignID)
	}
	if !sameProvider(provider, a.primary) {
		return nil, nil
	}
	return a.primary.SearchBooks(ctx, authorForeignID)
}

func (a *Aggregator) authorWorksByNameProviders() []authorWorksByNameProvider {
	if a == nil {
		return nil
	}
	providers := make([]authorWorksByNameProvider, 0, len(a.enrichers))
	for _, enricher := range a.enrichers {
		if provider, ok := enricher.(authorWorksByNameProvider); ok {
			providers = append(providers, provider)
		}
	}
	return providers
}

// pruneAuthorWorkCompilations drops works whose normalized title matches one an
// enricher flagged as a compilation/omnibus, plus any book still carrying the
// compilation flag directly. Primary-provider works (OpenLibrary) carry no
// compilation signal of their own, so the enricher's classification is the only
// way to know an inflated "bundle" came through the primary list. The filter is
// in place and order-preserving.
func pruneAuthorWorkCompilations(books []models.Book, compilationTitles map[string]struct{}) []models.Book {
	filtered := books[:0]
	for _, b := range books {
		if b.IsCompilation {
			continue
		}
		if _, ok := compilationTitles[authorWorkMergeKey(b.Title)]; ok {
			continue
		}
		filtered = append(filtered, b)
	}
	return filtered
}

// fragmentSuffixRe matches a trailing "part of a whole" marker OpenLibrary
// sometimes appends to a work's title when it lists one printed volume of a
// larger work as its own record: "The Silmarillion. 1/?", "... 2/3",
// "The Way of Kings, Part One" / "... Part Two", "..., Book 1 of 3".
var fragmentSuffixRe = regexp.MustCompile(`(?i)` +
	`(?:[,.]\s*|\s+)\(?(?:part|pt\.?|book)\s+(?:one|two|three|four|five|six|seven|eight|nine|ten|i{1,3}|iv|v|vi{0,3}|\d+)(?:\s+of\s+\d+)?\)?\s*$` +
	`|(?:[,.]\s*|\s+)\d+\s*/\s*[\d?]+\s*$`)

// slashJoinedGroupRe matches a trailing parenthetical listing two or more
// "/"-separated titles, e.g. "Novels (Fellowship of the Ring / Hobbit)" or
// "Forever (Heart's Victory / Rules of the Game)" — an OpenLibrary omnibus
// record naming its own contents rather than a title in its own right.
var slashJoinedGroupRe = regexp.MustCompile(`\(([^()]+/[^()]+)\)\s*$`)

// leadingArticleRe strips a leading "The"/"A"/"An" for the sole purpose of
// matching a fragment or omnibus-segment title against a standalone title
// elsewhere in the same author's list ("Hobbit" segment vs. "The Hobbit"
// core entry). Not used for the general merge key: two distinct books can
// differ by exactly a leading article ("A Study in Scarlet"), so folding
// articles together globally would risk a false merge.
var leadingArticleRe = regexp.MustCompile(`(?i)^(?:the|an?)\s+`)

func articleInsensitiveTitleKey(title string) string {
	key := authorWorkMergeKey(title)
	return leadingArticleRe.ReplaceAllString(key, "")
}

// pruneAuthorWorkRedundantTitles drops two shapes of duplicate noise that
// survive authorWorkMergeKey because the duplicate never shares an exact
// normalized title with the work it duplicates:
//
//   - Fragment splits: a whole work is also present in the list under its
//     own title, and this record is a "part N of M" split of that same
//     work (see fragmentSuffixRe).
//   - Slash-joined omnibus records: every "/"-segment inside a trailing
//     parenthetical already matches a standalone title elsewhere in the
//     list (see slashJoinedGroupRe), so the joined record adds nothing.
//
// Both checks require the whole/segment titles to genuinely be present
// elsewhere in the same list, so a trilogy with no standalone omnibus or
// whole-work edition keeps its per-volume entries untouched.
func pruneAuthorWorkRedundantTitles(books []models.Book) []models.Book {
	if len(books) < 2 {
		return books
	}

	known := make(map[string]struct{}, len(books))
	for _, b := range books {
		if k := articleInsensitiveTitleKey(b.Title); k != "" {
			known[k] = struct{}{}
		}
	}

	filtered := books[:0]
	for _, b := range books {
		if loc := fragmentSuffixRe.FindStringIndex(b.Title); loc != nil {
			base := articleInsensitiveTitleKey(b.Title[:loc[0]])
			if _, ok := known[base]; ok && base != "" {
				continue
			}
		}
		if segments, ok := slashJoinedSegments(b.Title); ok {
			if allSegmentsKnown(segments, known) {
				continue
			}
		}
		filtered = append(filtered, b)
	}
	return filtered
}

func slashJoinedSegments(title string) ([]string, bool) {
	m := slashJoinedGroupRe.FindStringSubmatch(title)
	if m == nil {
		return nil, false
	}
	parts := strings.Split(m[1], "/")
	if len(parts) < 2 {
		return nil, false
	}
	segments := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, false
		}
		segments = append(segments, p)
	}
	return segments, true
}

func allSegmentsKnown(segments []string, known map[string]struct{}) bool {
	for _, seg := range segments {
		key := articleInsensitiveTitleKey(seg)
		if key == "" {
			return false
		}
		if _, ok := known[key]; !ok {
			return false
		}
	}
	return true
}

// pruneAuthorWorkSubjectOutliers drops obvious same-name-author collisions from
// primary-provider author work lists. OpenLibrary occasionally groups unrelated
// people who share a name; when a catalog is overwhelmingly fiction/juvenile
// fiction, a computer-networking textbook is almost certainly from another
// author and should not be auto-wanted under the fiction author.
func pruneAuthorWorkSubjectOutliers(books []models.Book) []models.Book {
	if len(books) < 4 || fictionCatalogSignalCount(books) < 3 {
		return books
	}
	filtered := books[:0]
	for _, b := range books {
		if isTechnicalSubjectOutlier(b) {
			continue
		}
		filtered = append(filtered, b)
	}
	return filtered
}

// pruneAuthorWorkSelfReference drops works whose subjects list credits them
// as being *about* the author rather than *by* them — a companion guide, art
// book, or biography that OpenLibrary nonetheless links to the author's
// works list. OpenLibrary carries no role field distinguishing "author" from
// "subject" on a work record (verified empirically: a companion guide's
// authors array is structurally identical to a real novel's), so the only
// provider-native signal available is that these secondary works are
// commonly subject-tagged with the author's own Library-of-Congress name
// authority string, e.g. "Tolkien, j, r. r. (john ronald ruel), 1892-1973".
//
// Deliberately conservative to avoid false-positiving on a legitimately
// self-titled work (e.g. a memoir): requires both the "Surname, " prefix
// match against the author's own catalogued surname AND a four-digit year
// elsewhere in the same subject string, since LoC personal-name authority
// subjects for a deceased or dated author almost always carry birth/death
// years in that shape, while an unrelated subject starting with the same
// surname token essentially never does.
func pruneAuthorWorkSelfReference(books []models.Book, author models.Author) []models.Book {
	surname := authorSurnameForSelfReferenceCheck(author.SortName)
	if surname == "" {
		return books
	}
	filtered := books[:0]
	for _, b := range books {
		if isSelfReferentialSubjectWork(b, surname) {
			slog.Debug("pruning work whose subjects describe the author rather than crediting their own writing",
				"title", b.Title, "foreignId", b.ForeignID, "author", author.Name)
			continue
		}
		filtered = append(filtered, b)
	}
	return filtered
}

// authorSurnameForSelfReferenceCheck extracts the surname from an
// OpenLibrary-style "Surname, Given Names" sort name. Returns "" (disabling
// the check) for anything shorter than 3 characters or with no comma, since
// a short or malformed surname token risks matching unrelated subjects.
func authorSurnameForSelfReferenceCheck(sortName string) string {
	idx := strings.Index(sortName, ",")
	if idx < 0 {
		return ""
	}
	surname := strings.ToLower(strings.TrimSpace(sortName[:idx]))
	if len(surname) < 3 {
		return ""
	}
	return surname
}

// yearInSubjectRe matches a bare four-digit year, the shape LoC personal-name
// authority subjects use for birth/death dates (e.g. "1892-1973", "1892-").
var yearInSubjectRe = regexp.MustCompile(`\b\d{4}\b`)

func isSelfReferentialSubjectWork(book models.Book, surname string) bool {
	for _, genre := range book.Genres {
		g := strings.ToLower(strings.TrimSpace(genre))
		if strings.HasPrefix(g, surname+",") && yearInSubjectRe.MatchString(g) {
			return true
		}
	}
	return false
}

func fictionCatalogSignalCount(books []models.Book) int {
	count := 0
	for _, b := range books {
		if hasFictionSubjectSignal(b) {
			count++
		}
	}
	return count
}

func isTechnicalSubjectOutlier(book models.Book) bool {
	if hasFictionSubjectSignal(book) {
		return false
	}
	signals := 0
	for _, genre := range book.Genres {
		g := strings.ToLower(strings.TrimSpace(genre))
		switch {
		case strings.Contains(g, "computer network"),
			strings.Contains(g, "software-defined network"),
			strings.Contains(g, "networking"),
			strings.Contains(g, "telecommunication"),
			strings.Contains(g, "programming language"),
			strings.Contains(g, "software engineering"):
			signals++
		}
	}
	return signals >= 2
}

func hasFictionSubjectSignal(book models.Book) bool {
	for _, genre := range book.Genres {
		g := strings.ToLower(strings.TrimSpace(genre))
		switch {
		case strings.Contains(g, "fiction"),
			strings.Contains(g, "fantasy"),
			strings.Contains(g, "science fiction"),
			strings.Contains(g, "juvenile"),
			strings.Contains(g, "christian life"),
			strings.Contains(g, "religious"):
			return true
		}
	}
	return false
}

func cloneBooks(books []models.Book) []models.Book {
	if books == nil {
		return nil
	}
	cloned := make([]models.Book, len(books))
	copy(cloned, books)
	return cloned
}

// authorWorkCoverEnrichConcurrency bounds the per-work enricher fan-out below.
// This loop used to be strictly serial, so an author whose works mostly lack a
// work-level cover — the normal case for OpenLibrary — spent one enricher
// round trip per work, in sequence, before the sync loop had even started. On a
// 65-work author that is 65 serial calls, and it is a large share of why an
// author refresh could take an hour (#1888). Matches the pace used by the other
// provider fan-outs (authorAutoSearchConcurrency, authorWorkSampleConcurrency).
const authorWorkCoverEnrichConcurrency = 4

func (a *Aggregator) enrichMissingAuthorWorkCovers(ctx context.Context, books []models.Book) {
	// enrichBook writes only into the book it is handed and the enricher
	// clients plus a.cache are goroutine-safe, so bounding the fan-out is a
	// pure latency win: each index is touched by exactly one goroutine.
	targets := make([]int, 0, len(books))
	for i := range books {
		if books[i].ImageURL == "" {
			targets = append(targets, i)
		}
	}
	concurrency.RunBounded(ctx, targets, authorWorkCoverEnrichConcurrency, func(ctx context.Context, i int) {
		a.enrichBook(ctx, &books[i])
	})
	// Edition-cover fallback for the still-empty ones. enrichBook only consults
	// work-level covers (enricher title search, cover-by-ISBN providers); a work
	// whose cover lives only on an edition stays blank after it. When the primary
	// provider can sample editions, give those books one more chance from the
	// edition list (#1748). Bounded and memoized per work inside the provider.
	// Runs on both add and refresh — both reach here through GetAuthorWorks /
	// GetAuthorWorksForAuthor.
	if filler, ok := a.primary.(workCoverFiller); ok {
		filler.FillMissingWorkCovers(ctx, books)
	}
}

func mergeAuthorWorks(primary, supplemental []models.Book) []models.Book {
	books := make([]models.Book, 0, len(primary)+len(supplemental))
	index := make(map[string]int, len(primary)+len(supplemental))
	for _, book := range primary {
		key := authorWorkMergeKey(book.Title)
		if key != "" {
			if _, exists := index[key]; !exists {
				index[key] = len(books)
			}
		}
		books = append(books, book)
	}
	for _, book := range supplemental {
		key := authorWorkMergeKey(book.Title)
		if key == "" {
			continue
		}
		if pos, ok := index[key]; ok {
			mergeAuthorWorkMetadata(&books[pos], book)
			continue
		}
		index[key] = len(books)
		books = append(books, book)
	}
	return books
}

func authorWorkMergeKey(title string) string {
	// CanonicalDedupKey (vs. plain NormalizeTitleForDedup) also strips
	// bracket suffixes, so a primary-provider title like "The Book of Lost
	// Tales [1/2]" now matches a bracket-free supplemental/compilation
	// title for the same work instead of missing the merge over the
	// suffix alone.
	key := indexer.CanonicalDedupKey(title)
	if key != "" {
		return key
	}
	return strings.ToLower(strings.TrimSpace(title))
}

func mergeAuthorWorkMetadata(dst *models.Book, src models.Book) {
	if dst.HardcoverForeignID == "" {
		dst.HardcoverForeignID = hardcoverForeignIDForAuthorWork(src)
	}
	if dst.ImageURL == "" {
		dst.ImageURL = src.ImageURL
	}
	if dst.Description == "" {
		dst.Description = src.Description
	}
	// #807: keep the (average, count) pair together and prefer the source with
	// the materially stronger ratings_count rather than filling each field
	// independently (which could pair an average from one source with a count
	// from another).
	if preferStrongerRating(dst.AverageRating, dst.RatingsCount, src.AverageRating, src.RatingsCount) {
		dst.AverageRating = src.AverageRating
		dst.RatingsCount = src.RatingsCount
	}
	if dst.ReleaseDate == nil {
		dst.ReleaseDate = src.ReleaseDate
	}
	// Genres: a Hardcover supplement replaces OpenLibrary's noisy "subjects"
	// bag with its curated taxonomy. Gated to Hardcover provenance so other
	// enrichers (e.g. Google Books BISAC categories) don't overwrite with
	// slash-delimited strings. Non-Hardcover sources keep the fill-empty rule.
	if src.MetadataProvider == "hardcover" && len(src.Genres) > 0 {
		dst.Genres = src.Genres
	} else if len(dst.Genres) == 0 {
		dst.Genres = src.Genres
	}
	if dst.DurationSeconds == 0 {
		dst.DurationSeconds = src.DurationSeconds
	}
	if dst.ASIN == "" {
		dst.ASIN = src.ASIN
	}
	if dst.MediaType == "" {
		dst.MediaType = src.MediaType
	}
}

func hardcoverForeignIDForAuthorWork(book models.Book) string {
	if id := strings.TrimSpace(book.HardcoverForeignID); strings.HasPrefix(id, "hc:") {
		return id
	}
	if id := strings.TrimSpace(book.ForeignID); strings.HasPrefix(id, "hc:") && normalizedProviderName(book.MetadataProvider) == "hardcover" {
		return id
	}
	return ""
}

// authorIdentityCacheKey renders an author's known provider identities in a
// stable order so they can take part in a cache key.
func authorIdentityCacheKey(author models.Author) string {
	if len(author.ProviderIdentifiers) == 0 {
		return ""
	}
	providers := make([]string, 0, len(author.ProviderIdentifiers))
	for p := range author.ProviderIdentifiers {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	parts := make([]string, 0, len(providers))
	for _, p := range providers {
		parts = append(parts, p+"="+author.ProviderIdentifiers[p])
	}
	return strings.Join(parts, ",")
}
