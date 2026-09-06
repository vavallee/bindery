package metadata

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/seriesmatch"
	"github.com/vavallee/bindery/internal/textutil"
)

type bookMatchCandidate struct {
	book         models.Book
	titleExact   bool
	workExact    bool
	titleScore   int
	authorKind   textutil.AuthorMatchKind
	authorScore  float64
	authorAlias  bool
	resultRank   int
	rankTie      bool
	editionExact bool
	variantKind  canonicalTitleVariantKind
	isISBN       bool
}

type canonicalTitleVariantKind int

const (
	canonicalTitleVariantSource canonicalTitleVariantKind = iota
	canonicalTitleVariantDescriptor
	canonicalTitleVariantRightSegment
	canonicalTitleVariantLeftSegment
)

type primaryBookCanonicalQuery struct {
	query                  string
	matchTitle             string
	exactTitleOnly         bool
	allowRankTieBreak      bool
	allowEditionTitleMatch bool
	hasAuthor              bool
	variantKind            canonicalTitleVariantKind
	isISBN                 bool
}

type canonicalTitleVariant struct {
	title             string
	exactTitleOnly    bool
	allowRankTieBreak bool
	kind              canonicalTitleVariantKind
}

type canonicalPrimaryBookMatch struct {
	candidate  bookMatchCandidate
	sameSource bool
}

type canonicalPrimaryBookStatus int

const (
	canonicalPrimaryBookNoMatch canonicalPrimaryBookStatus = iota
	canonicalPrimaryBookMatched
	canonicalPrimaryBookDirectISBNConfirmed
)

func (a *Aggregator) canonicalPrimaryBook(ctx context.Context, isbn string, source models.Book) (*models.Book, bool) {
	canonical, status := a.lookupCanonicalPrimaryBook(ctx, isbn, source)
	return canonical, status == canonicalPrimaryBookMatched
}

func (a *Aggregator) lookupCanonicalPrimaryBook(ctx context.Context, isbn string, source models.Book) (*models.Book, canonicalPrimaryBookStatus) {
	if a == nil || a.primary == nil || source.Title == "" {
		return nil, canonicalPrimaryBookNoMatch
	}
	sourceAuthor := bookAuthorName(source)
	if sourceAuthor == "" {
		return nil, canonicalPrimaryBookNoMatch
	}
	queries := primaryBookCanonicalQueries(isbn, source.Title, sourceAuthor, source.Language)
	if canonical, status := a.canonicalPrimaryBookFromQueries(ctx, queries, func(query primaryBookCanonicalQuery) (*canonicalPrimaryBookMatch, bool) {
		return a.canonicalPrimaryBookSearch(ctx, query, source, sourceAuthor)
	}); status == canonicalPrimaryBookMatched {
		return canonical, status
	} else if status == canonicalPrimaryBookDirectISBNConfirmed {
		if canonical, ok := a.canonicalPrimaryBookAuthorWorks(ctx, source, sourceAuthor); ok {
			return canonical, canonicalPrimaryBookMatched
		}
		return nil, status
	}
	if canonical, ok := a.canonicalPrimaryBookAuthorWorks(ctx, source, sourceAuthor); ok {
		return canonical, canonicalPrimaryBookMatched
	}
	return nil, canonicalPrimaryBookNoMatch
}

func primaryBookCanonicalQueries(isbn, title, author, language string) []primaryBookCanonicalQuery {
	queries := make([]primaryBookCanonicalQuery, 0, 4)
	allowEditionTitleMatch := true
	seen := make(map[string]struct{})
	addQuery := func(queryText, matchTitle string, exactTitleOnly, allowRankTieBreak, hasAuthor bool, kind canonicalTitleVariantKind, isISBN bool) {
		queryText = strings.TrimSpace(queryText)
		if queryText == "" {
			return
		}
		if _, ok := seen[queryText]; ok {
			return
		}
		seen[queryText] = struct{}{}
		queries = append(queries, primaryBookCanonicalQuery{
			query:                  queryText,
			matchTitle:             matchTitle,
			exactTitleOnly:         exactTitleOnly,
			allowRankTieBreak:      allowRankTieBreak,
			allowEditionTitleMatch: allowEditionTitleMatch,
			hasAuthor:              hasAuthor,
			variantKind:            kind,
			isISBN:                 isISBN,
		})
	}
	if isbn = isbnutil.Normalize(isbn); isbn != "" {
		addQuery("isbn:"+isbn, title, false, false, true, canonicalTitleVariantSource, true)
	}
	for _, variant := range canonicalTitleVariants(title, language) {
		for _, authorVariant := range canonicalAuthorQueryVariants(author) {
			addQuery(variant.title+" "+authorVariant, variant.title, variant.exactTitleOnly, variant.allowRankTieBreak, true, variant.kind, false)
			addQuery("title:"+variant.title+" author:"+authorVariant, variant.title, variant.exactTitleOnly, variant.allowRankTieBreak, true, variant.kind, false)
		}
		if canonicalAuthorNeedsTitleOnlyFallback(author) {
			addQuery(variant.title, variant.title, variant.exactTitleOnly, variant.allowRankTieBreak, false, variant.kind, false)
			addQuery("title:"+variant.title, variant.title, variant.exactTitleOnly, variant.allowRankTieBreak, false, variant.kind, false)
		}
	}
	return queries
}

func canonicalAuthorNeedsTitleOnlyFallback(author string) bool {
	for _, part := range strings.Fields(author) {
		if likelyEastAsianRomanizedNameToken(part) {
			return true
		}
	}
	for _, r := range author {
		if r > 127 {
			return true
		}
	}
	return false
}

func canonicalAuthorQueryVariants(author string) []string {
	author = strings.TrimSpace(author)
	if author == "" {
		return nil
	}
	variants := make([]string, 0, 2)
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		for _, existing := range variants {
			if strings.EqualFold(existing, candidate) {
				return
			}
		}
		variants = append(variants, candidate)
	}
	if before, after, ok := strings.Cut(author, ","); ok {
		add(strings.TrimSpace(after) + " " + strings.TrimSpace(before))
	}
	add(author)
	if concise, ok := canonicalConciseAuthorQueryVariant(author); ok {
		add(concise)
	}
	if swapped, ok := canonicalEastAsianAuthorOrderVariant(author); ok {
		add(swapped)
	}
	return variants
}

func canonicalConciseAuthorQueryVariant(author string) (string, bool) {
	if strings.Contains(author, ",") {
		return "", false
	}
	parts := strings.Fields(author)
	if len(parts) < 3 {
		return "", false
	}
	return parts[0] + " " + parts[len(parts)-1], true
}

func canonicalEastAsianAuthorOrderVariant(author string) (string, bool) {
	if strings.Contains(author, ",") {
		return "", false
	}
	parts := strings.Fields(author)
	if len(parts) != 2 {
		return "", false
	}
	if !likelyEastAsianRomanizedNameToken(parts[0]) && !likelyEastAsianRomanizedNameToken(parts[1]) {
		return "", false
	}
	return parts[1] + " " + parts[0], true
}

func likelyEastAsianRomanizedNameToken(token string) bool {
	switch strings.ToLower(strings.Trim(token, " .")) {
	case "cixin", "hua", "liu", "yu":
		return true
	default:
		return false
	}
}

func canonicalTitleVariants(title, language string) []canonicalTitleVariant {
	title = cleanCanonicalTitleVariant(title)
	if title == "" {
		return nil
	}
	isGerman := isGermanBookLanguage(language)
	sourceKey := canonicalTitleVariantKey(title)
	variants := make([]canonicalTitleVariant, 0, 8)
	seen := make(map[string]struct{})
	add := func(candidate string, requireUseful bool, kind canonicalTitleVariantKind) bool {
		candidate = cleanCanonicalTitleVariant(candidate)
		if candidate == "" {
			return false
		}
		if requireUseful && !usefulCanonicalTitleVariant(candidate) {
			return false
		}
		key := canonicalTitleVariantKey(candidate)
		if _, ok := seen[key]; ok {
			return false
		}
		seen[key] = struct{}{}
		derived := sourceKey != key
		variants = append(variants, canonicalTitleVariant{
			title:             candidate,
			exactTitleOnly:    derived,
			allowRankTieBreak: derived,
			kind:              kind,
		})
		return true
	}

	stripped := stripCanonicalTitleDescriptor(title)

	if sourceKey != "" {
		add(title, false, canonicalTitleVariantSource)
	}
	add(stripped, true, canonicalTitleVariantDescriptor)
	add(stripCanonicalTrailingParentheticalDescriptors(stripped), true, canonicalTitleVariantDescriptor)

	if isGerman {
		for _, segment := range translatedOriginalTitleSegments(title, stripped) {
			add(segment, true, canonicalTitleVariantRightSegment)
		}
	}

	for _, segment := range rightCanonicalTitleSegments(title, stripped) {
		add(stripCanonicalTitleDescriptor(segment), true, canonicalTitleVariantRightSegment)
		add(segment, true, canonicalTitleVariantRightSegment)
	}

	for _, segment := range nonLatinCanonicalTitleSegments(title) {
		add(segment, true, canonicalTitleVariantRightSegment)
	}

	for _, segment := range leftCanonicalTitleSegments(title, stripped) {
		add(stripCanonicalTitleDescriptor(segment), true, canonicalTitleVariantLeftSegment)
		add(segment, true, canonicalTitleVariantLeftSegment)
	}
	return variants
}

func canonicalTitleVariantKey(title string) string {
	title = cleanCanonicalTitleVariant(title)
	if title == "" {
		return ""
	}
	var b strings.Builder
	spacePending := false
	for _, r := range title {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			if spacePending && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(unicode.ToLower(r))
			spacePending = false
		case unicode.IsSpace(r):
			spacePending = b.Len() > 0
		default:
			spacePending = b.Len() > 0
		}
	}
	return b.String()
}

// isGermanBookLanguage reports whether a book's reported language is German,
// in any of the spellings a provider might use. It matched only "ger", "deu"
// and "de" until #2463, so a record whose language arrived as "German",
// "Deutsch" or "de-DE" missed the German-specific title handling entirely.
func isGermanBookLanguage(language string) bool {
	return models.NormalizeLanguageCode(language) == "ger"
}

func cleanCanonicalTitleVariant(title string) string {
	return norm.NFC.String(strings.Trim(strings.TrimSpace(title), `"'“”‘’.,;`))
}

func stripCanonicalTitleDescriptor(title string) string {
	title = cleanCanonicalTitleVariant(title)
	for {
		next := title
		for _, sep := range []string{":", " - ", " – ", " — "} {
			idx := strings.LastIndex(next, sep)
			if idx <= 0 {
				continue
			}
			head := cleanCanonicalTitleVariant(next[:idx])
			tail := cleanCanonicalTitleVariant(next[idx+len(sep):])
			if usefulCanonicalTitleVariant(head) && canonicalTitleDescriptor(tail) {
				next = head
				break
			}
		}
		if next == title {
			return title
		}
		title = next
	}
}

func stripCanonicalTrailingParentheticalDescriptors(title string) string {
	title = cleanCanonicalTitleVariant(title)
	for {
		if !strings.HasSuffix(title, ")") {
			return title
		}
		start := strings.LastIndex(title, "(")
		if start < 0 {
			return title
		}
		descriptor := cleanCanonicalTitleVariant(title[start+1 : len(title)-1])
		if !canonicalParentheticalProductionDescriptor(descriptor) {
			return title
		}
		title = cleanCanonicalTitleVariant(title[:start])
	}
}

func canonicalParentheticalProductionDescriptor(value string) bool {
	value = strings.ToLower(strings.Join(strings.Fields(value), " "))
	switch value {
	case "abridged", "unabridged", "dramatized adaptation":
		return true
	default:
	}
	words := strings.Fields(value)
	if len(words) != 4 || words[0] != "part" || words[2] != "of" {
		return false
	}
	return canonicalDigitsOnly(words[1]) && canonicalDigitsOnly(words[3])
}

func canonicalDigitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func translatedOriginalTitleSegments(titles ...string) []string {
	var segments []string
	for _, title := range titles {
		for _, sep := range []string{" – ", " — ", " - "} {
			parts := strings.Split(cleanCanonicalTitleVariant(title), sep)
			if len(parts) < 2 {
				continue
			}
			left := cleanCanonicalTitleVariant(parts[0])
			right := cleanCanonicalTitleVariant(parts[1])
			if usefulCanonicalTitleVariant(left) && startsWithGermanArticle(right) {
				segments = append(segments, left)
			}
		}
		if segment := leadingGermanTranslatedTitleSegment(title); segment != "" {
			segments = append(segments, segment)
		}
	}
	return segments
}

func rightCanonicalTitleSegments(titles ...string) []string {
	var segments []string
	for _, title := range titles {
		for _, sep := range []string{":", " / ", " – ", " — ", " - "} {
			parts := strings.Split(cleanCanonicalTitleVariant(title), sep)
			if len(parts) < 2 {
				continue
			}
			for _, segment := range parts[1:] {
				segment = cleanCanonicalTitleVariant(segment)
				if segment != "" {
					segments = append(segments, segment)
				}
			}
		}
	}
	return segments
}

func leftCanonicalTitleSegments(titles ...string) []string {
	var segments []string
	for _, title := range titles {
		for _, sep := range []string{":", " / ", " – ", " — ", " - "} {
			parts := strings.Split(cleanCanonicalTitleVariant(title), sep)
			if len(parts) < 2 {
				continue
			}
			segment := cleanCanonicalTitleVariant(parts[0])
			if segment != "" {
				segments = append(segments, segment)
			}
		}
	}
	return segments
}

func nonLatinCanonicalTitleSegments(title string) []string {
	var segments []string
	var b strings.Builder
	spacePending := false
	flush := func() {
		segment := cleanCanonicalTitleVariant(b.String())
		if usefulCanonicalTitleVariant(segment) {
			segments = append(segments, segment)
		}
		b.Reset()
		spacePending = false
	}
	for _, r := range cleanCanonicalTitleVariant(title) {
		switch {
		case isCanonicalNonLatinScript(r):
			if spacePending && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			spacePending = false
		case unicode.IsSpace(r):
			spacePending = b.Len() > 0
		default:
			if b.Len() > 0 {
				flush()
			}
		}
	}
	if b.Len() > 0 {
		flush()
	}
	return segments
}

func isCanonicalNonLatinScript(r rune) bool {
	return unicode.In(r,
		unicode.Arabic,
		unicode.Cyrillic,
		unicode.Han,
		unicode.Hangul,
		unicode.Hiragana,
		unicode.Katakana,
	)
}

func startsWithGermanArticle(title string) bool {
	words := strings.Fields(cleanCanonicalTitleVariant(title))
	if len(words) == 0 {
		return false
	}
	switch strings.ToLower(words[0]) {
	case "der", "die", "das":
		return true
	default:
		return false
	}
}

func leadingGermanTranslatedTitleSegment(title string) string {
	words := strings.Fields(cleanCanonicalTitleVariant(title))
	if len(words) < 4 {
		return ""
	}
	switch strings.ToLower(words[1]) {
	case "der", "die", "das":
		if canonicalTitleDescriptorSuffix(strings.Join(words[2:], " ")) {
			return words[0]
		}
	default:
	}
	return ""
}

func usefulCanonicalTitleVariant(title string) bool {
	clean := seriesmatch.CleanTitle(title)
	if clean == "" {
		return false
	}
	words := strings.Fields(clean)
	return len(words) > 0 && !canonicalTitleDescriptor(title)
}

func canonicalTitleDescriptor(title string) bool {
	clean := seriesmatch.CleanTitle(title)
	if clean == "" {
		return true
	}
	for _, word := range strings.Fields(clean) {
		if canonicalTitleDescriptorWord(word) {
			continue
		}
		return false
	}
	return true
}

func canonicalTitleDescriptorSuffix(title string) bool {
	words := strings.Fields(seriesmatch.CleanTitle(title))
	if len(words) == 0 {
		return false
	}
	return canonicalTitleDescriptorWord(words[len(words)-1])
}

func canonicalTitleDescriptorWord(word string) bool {
	switch word {
	case "roman", "science", "fiction", "sci", "fi", "translated", "translation", "new", "neu", "ubersetzt", "übersetzt":
		return true
	default:
		return false
	}
}

func (a *Aggregator) canonicalPrimaryBookSearch(ctx context.Context, query primaryBookCanonicalQuery, source models.Book, sourceAuthor string) (*canonicalPrimaryBookMatch, bool) {
	results, err := a.primary.SearchBooks(ctx, query.query)
	if err != nil {
		slog.Debug("primary canonical book search failed", "query", query.query, "title", source.Title, "author", sourceAuthor, "error", err)
		return nil, false
	}
	return a.canonicalPrimaryBookFromResults(query, source, sourceAuthor, results, false)
}

func (a *Aggregator) canonicalPrimaryBookFromQueries(ctx context.Context, queries []primaryBookCanonicalQuery, lookup func(primaryBookCanonicalQuery) (*canonicalPrimaryBookMatch, bool)) (*models.Book, canonicalPrimaryBookStatus) {
	hasSegmentVariant := canonicalQueriesHaveSegmentVariant(queries)
	var provisional *canonicalPrimaryBookMatch
	provisionalAmbiguous := false
	directISBNConfirmed := false
	for _, query := range queries {
		match, ok := lookup(query)
		if !ok {
			continue
		}
		if match.sameSource {
			if query.isISBN {
				directISBNConfirmed = true
				break
			}
			continue
		}
		if query.isISBN {
			return a.fetchCanonicalPrimaryBook(ctx, match), canonicalPrimaryBookMatched
		}
		if hasSegmentVariant || shouldDeferCanonicalPrimaryBookMatch(query, match, hasSegmentVariant) {
			provisional, provisionalAmbiguous = betterCanonicalProvisionalMatch(provisional, provisionalAmbiguous, match)
			continue
		}
		return a.fetchCanonicalPrimaryBook(ctx, match), canonicalPrimaryBookMatched
	}
	if provisional != nil && !provisionalAmbiguous {
		return a.fetchCanonicalPrimaryBook(ctx, provisional), canonicalPrimaryBookMatched
	}
	if directISBNConfirmed {
		return nil, canonicalPrimaryBookDirectISBNConfirmed
	}
	return nil, canonicalPrimaryBookNoMatch
}

func canonicalQueriesHaveSegmentVariant(queries []primaryBookCanonicalQuery) bool {
	for _, query := range queries {
		if query.variantKind == canonicalTitleVariantRightSegment || query.variantKind == canonicalTitleVariantLeftSegment {
			return true
		}
	}
	return false
}

func shouldDeferCanonicalPrimaryBookMatch(query primaryBookCanonicalQuery, match *canonicalPrimaryBookMatch, hasSegmentVariant bool) bool {
	if !hasSegmentVariant || match == nil {
		return false
	}
	if query.allowEditionTitleMatch && query.variantKind == canonicalTitleVariantDescriptor {
		return true
	}
	if query.allowEditionTitleMatch && query.variantKind == canonicalTitleVariantSource {
		return !match.candidate.workExact
	}
	if query.isISBN || query.variantKind != canonicalTitleVariantSource {
		return false
	}
	return !match.candidate.titleExact
}

func betterCanonicalProvisionalMatch(current *canonicalPrimaryBookMatch, ambiguous bool, next *canonicalPrimaryBookMatch) (*canonicalPrimaryBookMatch, bool) {
	if current == nil {
		return next, false
	}
	if next == nil {
		return current, ambiguous
	}
	switch compareBookCandidate(next.candidate, current.candidate) {
	case 1:
		return next, false
	case 0:
		if strings.TrimSpace(next.candidate.book.ForeignID) != strings.TrimSpace(current.candidate.book.ForeignID) {
			return current, true
		}
	}
	return current, ambiguous
}

func (a *Aggregator) canonicalPrimaryBookAuthorWorks(ctx context.Context, source models.Book, sourceAuthor string) (*models.Book, bool) {
	if source.Author == nil || source.Author.ForeignID == "" {
		return nil, false
	}
	authorID := strings.TrimSpace(source.Author.ForeignID)
	if source.Author.MetadataProvider != "" && normalizedProviderName(source.Author.MetadataProvider) != normalizedProviderName(providerName(a.primary)) {
		return nil, false
	}
	if source.Author.MetadataProvider == "" && providerNameForForeignID(authorID) != normalizedProviderName(providerName(a.primary)) {
		return nil, false
	}
	if _, ok := a.primary.(worksProvider); !ok {
		return nil, false
	}
	works, err := a.rawPrimaryAuthorWorks(ctx, authorID)
	if err != nil {
		slog.Debug("primary canonical author works failed", "author", authorID, "title", source.Title, "error", err)
		return nil, false
	}
	queries := make([]primaryBookCanonicalQuery, 0, len(canonicalTitleVariants(source.Title, source.Language)))
	for _, variant := range canonicalTitleVariants(source.Title, source.Language) {
		queries = append(queries, primaryBookCanonicalQuery{
			query:                  "authorworks:" + authorID,
			matchTitle:             variant.title,
			exactTitleOnly:         variant.exactTitleOnly,
			allowRankTieBreak:      variant.allowRankTieBreak,
			allowEditionTitleMatch: isGermanBookLanguage(source.Language),
			hasAuthor:              true,
			variantKind:            variant.kind,
		})
	}
	canonical, status := a.canonicalPrimaryBookFromQueries(ctx, queries, func(query primaryBookCanonicalQuery) (*canonicalPrimaryBookMatch, bool) {
		return a.canonicalPrimaryBookFromResults(query, source, sourceAuthor, works, true)
	})
	return canonical, status == canonicalPrimaryBookMatched
}

func (a *Aggregator) canonicalPrimaryBookFromResults(query primaryBookCanonicalQuery, source models.Book, sourceAuthor string, results []models.Book, assumeAuthorMatch bool) (*canonicalPrimaryBookMatch, bool) {
	matches := make([]bookMatchCandidate, 0, len(results))
	seen := make(map[string]struct{}, len(results))
	for rank, result := range results {
		if result.ForeignID == "" {
			continue
		}
		if _, ok := seen[result.ForeignID]; ok {
			continue
		}
		seen[result.ForeignID] = struct{}{}
		author := textutil.AuthorMatchResult{Kind: textutil.AuthorMatchExact, Score: 1}
		authorMatchedAlias := false
		if !assumeAuthorMatch || bookAuthorName(result) != "" {
			author, authorMatchedAlias = bookAuthorMatch(sourceAuthor, result)
			if author.Kind != textutil.AuthorMatchExact && author.Kind != textutil.AuthorMatchFuzzyAuto {
				continue
			}
		}
		candidate, ok := scoreBookCandidate(query, result, author, authorMatchedAlias, rank, query.allowEditionTitleMatch || authorMatchedAlias)
		if ok {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return nil, false
	}
	best := matches[0]
	var runnerUp bookMatchCandidate
	hasRunnerUp := false
	ambiguous := false
	for _, candidate := range matches[1:] {
		switch compareBookCandidate(candidate, best) {
		case 1:
			runnerUp, hasRunnerUp = best, true
			best = candidate
			ambiguous = false
		case 0:
			ambiguous = true
		case -1:
			if !hasRunnerUp || compareBookCandidate(candidate, runnerUp) == 1 {
				runnerUp, hasRunnerUp = candidate, true
			}
		}
	}
	if ambiguous {
		slog.Debug("primary canonical book search ambiguous", "query", query.query, "title", source.Title, "author", sourceAuthor)
		return nil, false
	}
	if hasRunnerUp && !canonicalMatchIsDecisive(best, runnerUp) {
		slog.Debug("primary canonical book match too close to call",
			"query", query.query, "title", source.Title, "author", sourceAuthor,
			"best", best.book.Title, "bestScore", best.titleScore,
			"runnerUp", runnerUp.book.Title, "runnerUpScore", runnerUp.titleScore)
		return nil, false
	}
	if strings.TrimSpace(best.book.ForeignID) == strings.TrimSpace(source.ForeignID) {
		return &canonicalPrimaryBookMatch{sameSource: true}, true
	}
	return &canonicalPrimaryBookMatch{candidate: best}, true
}

func (a *Aggregator) fetchCanonicalPrimaryBook(ctx context.Context, match *canonicalPrimaryBookMatch) *models.Book {
	full, err := a.primary.GetBook(ctx, match.candidate.book.ForeignID)
	if err != nil {
		slog.Debug("primary canonical book fetch failed", "foreignID", match.candidate.book.ForeignID, "error", err)
		return &match.candidate.book
	}
	if full == nil {
		return &match.candidate.book
	}
	return full
}

func scoreBookCandidate(query primaryBookCanonicalQuery, candidate models.Book, author textutil.AuthorMatchResult, authorAlias bool, resultRank int, allowEditionTitleMatch bool) (bookMatchCandidate, bool) {
	workExact, titleExact, titleScore, editionExact := bestBookTitleMatch(query.matchTitle, candidate, allowEditionTitleMatch)
	if query.exactTitleOnly && !query.isISBN && !titleExact {
		return bookMatchCandidate{}, false
	}
	if !query.isISBN && titleScore < 88 {
		if !dominantCatalogCandidate(candidate, author, query) {
			return bookMatchCandidate{}, false
		}
	}
	if !query.isISBN && !query.hasAuthor && candidate.EditionCount < 10 {
		return bookMatchCandidate{}, false
	}
	if !query.isISBN && !titleExact && !editionExact && weakPartialTitleMatch(query.matchTitle, candidate.Title) {
		return bookMatchCandidate{}, false
	}
	return bookMatchCandidate{
		book:         candidate,
		titleExact:   titleExact,
		workExact:    workExact,
		titleScore:   titleScore,
		authorKind:   author.Kind,
		authorScore:  author.Score,
		authorAlias:  authorAlias,
		resultRank:   resultRank,
		rankTie:      query.allowRankTieBreak,
		editionExact: editionExact,
		variantKind:  query.variantKind,
		isISBN:       query.isISBN,
	}, true
}

func dominantCatalogCandidate(candidate models.Book, author textutil.AuthorMatchResult, query primaryBookCanonicalQuery) bool {
	if query.exactTitleOnly || query.isISBN || candidate.EditionCount < 10 {
		return false
	}
	return author.Kind == textutil.AuthorMatchExact
}

func bestBookTitleMatch(matchTitle string, candidate models.Book, allowEditionTitleMatch bool) (bool, bool, int, bool) {
	workExact := workTitleExactMatch(matchTitle, candidate.Title)
	bestExact := titleExactMatch(matchTitle, candidate.Title)
	bestScore := titleMatchScore(matchTitle, candidate.Title)
	bestEditionExact := false
	if !allowEditionTitleMatch {
		return workExact, bestExact, bestScore, bestEditionExact
	}
	for _, edition := range candidate.Editions {
		if strings.TrimSpace(edition.Title) == "" {
			continue
		}
		exact := titleExactMatch(matchTitle, edition.Title)
		score := titleMatchScore(matchTitle, edition.Title)
		if exact && !bestExact {
			bestExact = true
			bestEditionExact = true
		}
		if score > bestScore {
			bestScore = score
			bestEditionExact = exact
		} else if exact && score == bestScore {
			bestEditionExact = true
		}
	}
	return workExact, bestExact, bestScore, bestEditionExact
}

func weakPartialTitleMatch(matchTitle, candidateTitle string) bool {
	matchWords := strings.Fields(seriesmatch.CleanTitle(matchTitle))
	candidateWords := strings.Fields(seriesmatch.CleanTitle(candidateTitle))
	if len(matchWords) == 0 || len(candidateWords) == 0 || strings.Join(matchWords, " ") == strings.Join(candidateWords, " ") {
		return false
	}
	if containsWordSequence(candidateWords, matchWords) {
		return len(candidateWords) > len(matchWords)
	}
	if containsWordSequence(matchWords, candidateWords) {
		return !canonicalTitleHasSegmentSeparator(matchTitle)
	}
	return false
}

func canonicalTitleHasSegmentSeparator(title string) bool {
	for _, sep := range []string{":", " / ", " – ", " — ", " - "} {
		if strings.Contains(title, sep) {
			return true
		}
	}
	return false
}

func containsWordSequence(words, sequence []string) bool {
	if len(sequence) == 0 || len(sequence) > len(words) {
		return false
	}
	for start := 0; start <= len(words)-len(sequence); start++ {
		matches := true
		for idx := range sequence {
			if words[start+idx] != sequence[idx] {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func titleExactMatch(a, b string) bool {
	left := canonicalExactTitleKey(a)
	right := canonicalExactTitleKey(b)
	return left != "" && left == right
}

func workTitleExactMatch(a, b string) bool {
	left := canonicalWorkTitleExactKey(a)
	right := canonicalWorkTitleExactKey(b)
	return left != "" && left == right
}

func canonicalWorkTitleExactKey(title string) string {
	title = strings.ReplaceAll(cleanCanonicalTitleVariant(title), "&", " and ")
	return indexer.NormalizeTitleForDedup(title)
}

func canonicalExactTitleKey(title string) string {
	title = strings.ReplaceAll(cleanCanonicalTitleVariant(title), "&", " and ")
	if clean := seriesmatch.CleanTitle(title); clean != "" {
		return clean
	}
	return indexer.NormalizeTitleForDedup(title)
}

func titleMatchScore(a, b string) int {
	if titleExactMatch(a, b) {
		return 100
	}
	return seriesmatch.TitleScore(cleanCanonicalTitleVariant(a), cleanCanonicalTitleVariant(b))
}

func bookAuthorName(book models.Book) string {
	if book.Author == nil {
		return ""
	}
	return strings.TrimSpace(book.Author.Name)
}

func bookAuthorMatch(sourceAuthor string, book models.Book) (textutil.AuthorMatchResult, bool) {
	best := textutil.MatchAuthorName(sourceAuthor, bookAuthorName(book))
	bestMatchedAlias := false
	if book.Author == nil {
		return best, bestMatchedAlias
	}
	for _, alias := range book.Author.AlternateNames {
		next := textutil.MatchAuthorName(sourceAuthor, alias)
		if compareAuthorMatch(next, best) == 1 {
			best = next
			bestMatchedAlias = true
		}
	}
	return best, bestMatchedAlias
}

func compareAuthorMatch(a, b textutil.AuthorMatchResult) int {
	if authorMatchRank(a.Kind) != authorMatchRank(b.Kind) {
		if authorMatchRank(a.Kind) > authorMatchRank(b.Kind) {
			return 1
		}
		return -1
	}
	if math.Abs(a.Score-b.Score) > 0.001 {
		if a.Score > b.Score {
			return 1
		}
		return -1
	}
	return 0
}

func compareBookCandidate(a, b bookMatchCandidate) int {
	if a.isISBN != b.isISBN {
		if a.isISBN {
			return 1
		}
		return -1
	}
	if a.titleExact != b.titleExact {
		if a.titleExact {
			return 1
		}
		return -1
	}
	if a.titleScore != b.titleScore {
		if a.titleScore > b.titleScore {
			return 1
		}
		return -1
	}
	if authorMatchRank(a.authorKind) != authorMatchRank(b.authorKind) {
		if authorMatchRank(a.authorKind) > authorMatchRank(b.authorKind) {
			return 1
		}
		return -1
	}
	if a.authorAlias != b.authorAlias {
		if !a.authorAlias {
			return 1
		}
		return -1
	}
	if math.Abs(a.authorScore-b.authorScore) > 0.001 {
		if a.authorScore > b.authorScore {
			return 1
		}
		return -1
	}
	if a.editionExact != b.editionExact {
		if a.editionExact {
			return 1
		}
		return -1
	}
	if catalogDominates(a.book.EditionCount, b.book.EditionCount) {
		return 1
	}
	if catalogDominates(b.book.EditionCount, a.book.EditionCount) {
		return -1
	}
	if a.variantKind != b.variantKind {
		if a.variantKind == canonicalTitleVariantSource && a.workExact {
			return 1
		}
		if b.variantKind == canonicalTitleVariantSource && b.workExact {
			return -1
		}
		if a.variantKind == canonicalTitleVariantRightSegment && b.variantKind == canonicalTitleVariantLeftSegment {
			return 1
		}
		if b.variantKind == canonicalTitleVariantRightSegment && a.variantKind == canonicalTitleVariantLeftSegment {
			return -1
		}
	}
	if a.workExact != b.workExact {
		if a.workExact {
			return 1
		}
		return -1
	}
	if (a.rankTie || b.rankTie) && a.resultRank != b.resultRank {
		if a.resultRank < b.resultRank {
			return 1
		}
		return -1
	}
	return 0
}

func catalogDominates(a, b int) bool {
	if a < 10 || a <= b {
		return false
	}
	if b <= 1 {
		return true
	}
	return a >= b*3
}

func authorMatchRank(kind textutil.AuthorMatchKind) int {
	switch kind {
	case textutil.AuthorMatchExact:
		return 2
	case textutil.AuthorMatchFuzzyAuto:
		return 1
	default:
		return 0
	}
}

// canonicalMatchIsDecisive reports whether the winning candidate beat the
// runner-up by enough to be trusted without a human looking.
//
// compareBookCandidate is a lexicographic ordering, so it will happily pick a
// candidate scoring 91 over one scoring 90 and report no ambiguity at all. That
// is a confident answer built on one point of fuzzy string similarity. The
// evidence for a match is not only how well the winner scored, it is also how
// much better it scored than everything else, which is the runner-up gap beets
// applies before auto-accepting a release and the "clerical review" middle
// region of Fellegi-Sunter.
//
// Three ways to be decisive, in descending order of how much they prove:
//
//   - An identity rather than a resemblance: an ISBN query, or an exact title.
//     Nothing about a runner-up can weaken those.
//   - The same work: the two candidates share a dedup key, so whichever is
//     picked the user gets the book they asked for, in a different edition.
//   - A real margin: canonicalDecisiveGap points of title score clear of the
//     runner-up.
//
// Anything else returns false and the caller declines rather than guessing.
// Declining is recoverable, since the book stays unmatched and can be matched
// by hand; picking the wrong work is not, because the wrong metadata is then
// attached to the user's book.
func canonicalMatchIsDecisive(best, runnerUp bookMatchCandidate) bool {
	if best.isISBN || best.titleExact {
		return true
	}
	if key := indexer.CanonicalDedupKey(best.book.Title); key != "" &&
		key == indexer.CanonicalDedupKey(runnerUp.book.Title) {
		return true
	}
	return best.titleScore-runnerUp.titleScore >= canonicalDecisiveGap
}

// canonicalDecisiveGap is how far ahead the winner has to be. Five points on
// the 0-100 title scale, which is wider than the noise between two spellings of
// one title and narrower than the distance to a genuinely different book: after
// #2343's weighting, "Dune" scores 100 against itself and 90 against "Dune
// Messiah", a gap of 10.
const canonicalDecisiveGap = 5
