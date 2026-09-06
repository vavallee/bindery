package abs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

type authorMatcher struct {
	authors     *db.AuthorRepo
	all         []*models.Author
	byID        map[int64]*models.Author
	aliases     []models.AuthorAlias
	aliasLoaded map[authorAliasKey]struct{}
}

type authorAliasKey struct {
	authorID int64
	name     string
}

func (i *Importer) newAuthorMatcher(ctx context.Context) (*authorMatcher, error) {
	all, err := i.authors.List(ctx)
	if err != nil {
		return nil, err
	}
	matcher := &authorMatcher{
		authors:     i.authors,
		byID:        make(map[int64]*models.Author, len(all)),
		aliasLoaded: make(map[authorAliasKey]struct{}),
	}
	for idx := range all {
		matcher.addAuthor(&all[idx])
	}
	if i.aliases != nil {
		loaded, err := i.aliases.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, alias := range loaded {
			matcher.addAlias(alias)
		}
	}
	return matcher, nil
}

func (m *authorMatcher) addAuthor(author *models.Author) {
	if m == nil || author == nil || author.ID == 0 {
		return
	}
	cp := *author
	if existing, ok := m.byID[cp.ID]; ok {
		*existing = cp
		return
	}
	m.byID[cp.ID] = &cp
	m.all = append(m.all, &cp)
}

func (m *authorMatcher) addAlias(alias models.AuthorAlias) {
	if m == nil || alias.AuthorID == 0 {
		return
	}
	alias.Name = strings.TrimSpace(alias.Name)
	if alias.Name == "" {
		return
	}
	key := authorAliasKey{authorID: alias.AuthorID, name: strings.ToLower(alias.Name)}
	if _, ok := m.aliasLoaded[key]; ok {
		return
	}
	m.aliasLoaded[key] = struct{}{}
	m.aliases = append(m.aliases, alias)
}

func (m *authorMatcher) getAuthor(ctx context.Context, id int64) (*models.Author, error) {
	if m == nil {
		return nil, nil
	}
	if a, ok := m.byID[id]; ok {
		cp := *a
		return &cp, nil
	}
	a, err := m.authors.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	m.addAuthor(a)
	if a == nil {
		return nil, nil
	}
	cp := *a
	return &cp, nil
}

// findAuthorByName looks up a local author whose name matches the supplied
// name. Matching proceeds in tiers: exact lowercase (author name, then alias),
// then exact via normalized variants (initials, suffixes, last-first swap),
// then fuzzy matching on textutil.MatchAuthorName's evidence weight. The
// returned matchedBy string distinguishes these tiers so callers can decide
// when to record a variant alias.
func (i *Importer) findAuthorByName(ctx context.Context, name string) (*models.Author, string, bool, error) {
	matcher, err := i.newAuthorMatcher(ctx)
	if err != nil {
		return nil, "", false, err
	}
	return matcher.findAuthorByName(ctx, name)
}

func (m *authorMatcher) findAuthorByName(ctx context.Context, name string) (*models.Author, string, bool, error) {
	// Tier 1: exact lowercase.
	needle := strings.ToLower(strings.TrimSpace(name))
	exact := make(map[int64]*models.Author)
	viaAlias := make(map[int64]bool)
	for _, author := range m.all {
		if strings.ToLower(strings.TrimSpace(author.Name)) == needle {
			cp := *author
			exact[cp.ID] = &cp
		}
	}
	for _, alias := range m.aliases {
		if strings.ToLower(strings.TrimSpace(alias.Name)) != needle {
			continue
		}
		author, trusted, err := m.trustedAliasAuthor(ctx, alias)
		if err != nil {
			return nil, "", false, err
		}
		if !trusted || author == nil {
			continue
		}
		if _, already := exact[author.ID]; !already {
			viaAlias[author.ID] = true
		}
		exact[author.ID] = author
	}
	if len(exact) == 1 {
		for id, author := range exact {
			matchedBy := "name"
			if viaAlias[id] {
				matchedBy = "alias"
			}
			return author, matchedBy, false, nil
		}
	}
	if len(exact) > 1 {
		return nil, "", true, nil
	}

	// Tier 2: exact via normalized variants.
	normExact := make(map[int64]*models.Author)
	normViaAlias := make(map[int64]bool)
	for _, author := range m.all {
		if textutil.MatchAuthorName(name, author.Name).Kind == textutil.AuthorMatchExact {
			cp := *author
			normExact[cp.ID] = &cp
		}
	}
	for _, alias := range m.aliases {
		if textutil.MatchAuthorName(name, alias.Name).Kind != textutil.AuthorMatchExact {
			continue
		}
		author, trusted, err := m.trustedAliasAuthor(ctx, alias)
		if err != nil {
			return nil, "", false, err
		}
		if !trusted || author == nil {
			continue
		}
		if _, already := normExact[author.ID]; !already {
			normViaAlias[author.ID] = true
		}
		normExact[author.ID] = author
	}
	if len(normExact) == 1 {
		for id, author := range normExact {
			matchedBy := "normalized_name"
			if normViaAlias[id] {
				matchedBy = "normalized_alias"
			}
			return author, matchedBy, false, nil
		}
	}
	if len(normExact) > 1 {
		return nil, "", true, nil
	}

	// Tier 3: fuzzy match. Collect the best evidence weight per author across
	// both direct name and alias comparisons.
	//
	// Ranking is on MatchAuthorName's Weight, not its Score, because the two no
	// longer agree: a candidate carried by an exact surname plus compatible
	// initials ("John Ronald Reuel Tolkien" for "J.R.R. Tolkien") auto-matches
	// at a whole-name Jaro-Winkler of 0.9040, while one carried by a shared
	// forename and a near-miss short surname ("Christopher Rose" for
	// "Christopher Ross") is refused at 0.9750. Ranking on Score would order
	// the candidates by a number that did not decide any of their bands.
	type scored struct {
		author    *models.Author
		weight    float64
		fromAlias bool
	}
	best := make(map[int64]*scored)
	consider := func(a *models.Author, weight float64, fromAlias bool) {
		if a == nil {
			return
		}
		existing, ok := best[a.ID]
		if !ok || weight > existing.weight {
			best[a.ID] = &scored{author: a, weight: weight, fromAlias: fromAlias}
			return
		}
		if weight == existing.weight && existing.fromAlias && !fromAlias {
			// Prefer a direct-name match over alias when weights tie.
			existing.fromAlias = false
		}
	}
	for _, author := range m.all {
		res := textutil.MatchAuthorName(name, author.Name)
		if res.Kind == textutil.AuthorMatchNone {
			continue
		}
		cp := *author
		consider(&cp, res.Weight, false)
	}
	for _, alias := range m.aliases {
		res := textutil.MatchAuthorName(name, alias.Name)
		if res.Kind == textutil.AuthorMatchNone {
			continue
		}
		author, trusted, err := m.trustedAliasAuthor(ctx, alias)
		if err != nil {
			return nil, "", false, err
		}
		if !trusted || author == nil {
			continue
		}
		consider(author, res.Weight, true)
	}
	if len(best) == 0 {
		return nil, "", false, nil
	}

	var top *scored
	var second float64
	for _, s := range best {
		if top == nil || s.weight > top.weight {
			if top != nil {
				second = top.weight
			}
			top = s
		} else if s.weight > second {
			second = s.weight
		}
	}
	if top.weight >= textutil.AuthorMatchAutoWeight {
		// Require a clear margin over any close runner-up before auto-matching.
		// One full point of evidence: the per-field weights move in halves, so
		// anything less than that is two candidates the scorer could not tell
		// apart, which is what the review queue is for.
		const fuzzyTieMargin = 1.0
		if len(best) > 1 && top.weight-second < fuzzyTieMargin {
			return nil, "", true, nil
		}
		matchedBy := "fuzzy_name"
		if top.fromAlias {
			matchedBy = "fuzzy_alias"
		}
		return top.author, matchedBy, false, nil
	}
	// Best candidate is in the ambiguous band (weight in [2, 5)): surface as
	// review rather than silently create or merge.
	return nil, "", true, nil
}

func (m *authorMatcher) trustedAliasAuthor(ctx context.Context, alias models.AuthorAlias) (*models.Author, bool, error) {
	author, err := m.getAuthor(ctx, alias.AuthorID)
	if err != nil || author == nil {
		return author, false, err
	}
	return author, trustedAuthorAlias(alias, author), nil
}

func trustedAuthorAlias(alias models.AuthorAlias, author *models.Author) bool {
	if author == nil {
		return false
	}
	source := strings.TrimSpace(alias.SourceOLID)
	if strings.EqualFold(source, "abs") {
		return authorNamesAutoMatch(alias.Name, author.Name)
	}
	if source != "" {
		return true
	}
	return authorNamesAutoMatch(alias.Name, author.Name)
}

func authorNamesAutoMatch(a, b string) bool {
	match := textutil.MatchAuthorName(a, b)
	return match.Kind == textutil.AuthorMatchExact ||
		match.Kind == textutil.AuthorMatchFuzzyAuto ||
		authorInitialVariantMatch(a, b)
}

func authorInitialVariantMatch(a, b string) bool {
	for _, av := range textutil.NormalizeAuthorNameWithVariants(a) {
		for _, bv := range textutil.NormalizeAuthorNameWithVariants(b) {
			if normalizedAuthorInitialVariantMatch(av, bv) {
				return true
			}
		}
	}
	return false
}

func normalizedAuthorInitialVariantMatch(a, b string) bool {
	aTokens := strings.Fields(a)
	bTokens := strings.Fields(b)
	if len(aTokens) == 0 || len(aTokens) != len(bTokens) {
		return false
	}
	sawInitial := false
	for idx := range aTokens {
		if aTokens[idx] == bTokens[idx] {
			continue
		}
		if singleRune(aTokens[idx]) && strings.HasPrefix(bTokens[idx], aTokens[idx]) {
			sawInitial = true
			continue
		}
		if singleRune(bTokens[idx]) && strings.HasPrefix(aTokens[idx], bTokens[idx]) {
			sawInitial = true
			continue
		}
		return false
	}
	return sawInitial
}

func singleRune(s string) bool {
	return len([]rune(s)) == 1
}

func (m *authorMatcher) authorMatchesABSName(ctx context.Context, author *models.Author, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if author == nil || name == "" {
		return false, nil
	}
	if authorNamesAutoMatch(name, author.Name) {
		return true, nil
	}
	if m == nil {
		return false, nil
	}
	for _, alias := range m.aliases {
		if alias.AuthorID != author.ID || !authorNamesAutoMatch(name, alias.Name) {
			continue
		}
		aliasAuthor, trusted, err := m.trustedAliasAuthor(ctx, alias)
		if err != nil {
			return false, err
		}
		if trusted && aliasAuthor != nil && aliasAuthor.ID == author.ID {
			return true, nil
		}
	}
	return false, nil
}

// shouldRecordAuthorVariantAlias returns true when the matchedBy tier is one
// that identifies the canonical author via a form different from the supplied
// ABS name, so recording the ABS form as an alias is helpful. "alias" and
// "name" are omitted because the ABS name already equals the alias/canonical
// name and re-recording would be a no-op.
func shouldRecordAuthorVariantAlias(matchedBy string) bool {
	switch matchedBy {
	case "normalized_name", "normalized_alias", "fuzzy_name", "fuzzy_alias":
		return true
	}
	return false
}

func (i *Importer) cleanupABSSourcedAliases(ctx context.Context) (int, error) {
	if i.aliases == nil || i.authors == nil {
		return 0, nil
	}
	aliases, err := i.aliases.List(ctx)
	if err != nil {
		return 0, err
	}
	authors := make(map[int64]*models.Author)
	removed := 0
	for _, alias := range aliases {
		if !strings.EqualFold(strings.TrimSpace(alias.SourceOLID), "abs") {
			continue
		}
		author, ok := authors[alias.AuthorID]
		if !ok {
			author, err = i.authors.GetByID(ctx, alias.AuthorID)
			if err != nil {
				return removed, err
			}
			authors[alias.AuthorID] = author
		}
		if author == nil {
			continue
		}
		aliasName := strings.TrimSpace(alias.Name)
		authorName := strings.TrimSpace(author.Name)
		if !authorNamesAutoMatch(aliasName, authorName) || strings.EqualFold(aliasName, authorName) {
			if err := i.aliases.Delete(ctx, alias.ID); err != nil {
				return removed, err
			}
			removed++
		}
	}
	return removed, nil
}

func (i *Importer) recordSecondaryAuthors(ctx context.Context, canonicalID int64, extras []NormalizedAuthor, matcher *authorMatcher) {
	if canonicalID == 0 || i.aliases == nil {
		return
	}
	var canonical *models.Author
	var err error
	if matcher != nil {
		canonical, err = matcher.getAuthor(ctx, canonicalID)
	} else if i.authors != nil {
		canonical, err = i.authors.GetByID(ctx, canonicalID)
	}
	if err != nil {
		slog.Debug("abs import: secondary author alias skipped", "authorID", canonicalID, "error", err)
		return
	}
	if canonical == nil {
		return
	}
	for _, author := range extras {
		name := strings.TrimSpace(author.Name)
		if name == "" || !authorNamesAutoMatch(name, canonical.Name) {
			continue
		}
		// Mark ABS-sourced secondary-author aliases with a sentinel SourceOLID so
		// trustedAuthorAlias treats them as trusted even when the alias name does
		// not fuzzy-match the canonical name (e.g. pen names like "Mark Twain" vs
		// "Samuel Clemens").
		alias := &models.AuthorAlias{AuthorID: canonicalID, Name: name, SourceOLID: "abs"}
		if err := i.aliases.Create(ctx, alias); err != nil {
			slog.Debug("abs import: alias record skipped", "name", name, "error", err)
			continue
		}
		matcher.addAlias(*alias)
	}
}

func (i *Importer) recordAuthorVariantAlias(ctx context.Context, canonicalID int64, name string, matcher *authorMatcher) {
	if canonicalID == 0 || i.aliases == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	alias := &models.AuthorAlias{AuthorID: canonicalID, Name: name}
	if err := i.aliases.Create(ctx, alias); err != nil {
		slog.Debug("abs import: author variant alias skipped", "name", name, "error", err)
		return
	}
	matcher.addAlias(*alias)
}

// resetUpstreamAuthorCache clears the per-run upstream author lookup memo. Call
// once at the start of each Run so cached results never leak between imports.
func (i *Importer) resetUpstreamAuthorCache() {
	i.upstreamAuthorMu.Lock()
	i.upstreamAuthorCache = make(map[string]upstreamAuthorLookup)
	i.upstreamAuthorMu.Unlock()
}

// lookupUpstreamAuthor resolves an ABS author name against the metadata
// providers, memoizing the result for the rest of the Run. Books by the same
// author are common, and the underlying provider search can be slow or
// unreachable (OpenLibrary author search timing out for romanized-CJK pen
// names; Hardcover 401); without the memo every book by that author re-issues
// the same search and pays the full per-request timeout again.
func (i *Importer) lookupUpstreamAuthor(ctx context.Context, name string) (*models.Author, bool, error) {
	key := upstreamAuthorCacheKey(name)
	if key == "" {
		return nil, false, nil
	}
	i.upstreamAuthorMu.Lock()
	if i.upstreamAuthorCache != nil {
		if hit, ok := i.upstreamAuthorCache[key]; ok {
			i.upstreamAuthorMu.Unlock()
			return hit.author, hit.ambiguous, hit.err
		}
	}
	i.upstreamAuthorMu.Unlock()

	author, ambiguous, err := i.lookupUpstreamAuthorUncached(ctx, name)

	// A cancelled parent context means the whole run is shutting down, not that
	// this specific name failed to resolve — don't poison the cache with it.
	// A per-request deadline (the OpenLibrary timeout in the field reports) is a
	// genuine "this name didn't resolve this run" outcome and IS cached, which
	// is the entire point of the memo.
	if errors.Is(err, context.Canceled) {
		return author, ambiguous, err
	}
	i.upstreamAuthorMu.Lock()
	if i.upstreamAuthorCache != nil {
		i.upstreamAuthorCache[key] = upstreamAuthorLookup{author: author, ambiguous: ambiguous, err: err}
	}
	i.upstreamAuthorMu.Unlock()
	return author, ambiguous, err
}

// upstreamAuthorCacheKey normalizes an author name to a stable cache key:
// trimmed, internal whitespace collapsed, lowercased.
func upstreamAuthorCacheKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

func (i *Importer) lookupUpstreamAuthorUncached(ctx context.Context, name string) (*models.Author, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false, nil
	}
	const (
		exactScore     = 1.0
		fuzzyTieMargin = 0.02
	)
	var (
		best         *models.Author
		bestScore    float64
		secondScore  float64
		matchedQuery string
		sawAmbiguous bool
		exactHits    = make(map[string]struct{})
		exactMatches = make(map[string]models.Author)
	)
	var outcome metadata.SearchOutcome
	for _, query := range authorSearchQueries(name) {
		results, queryOutcome, err := i.meta.SearchAuthorsWithOutcome(ctx, query)
		if err != nil {
			return nil, false, err
		}
		// Any query in the sequence losing the primary provider taints the
		// whole lookup: the queries are alternative spellings of one name, so
		// a record the primary would have returned for query 2 is just as
		// absent from the merged picture as one it would have returned for
		// query 1.
		if queryOutcome.PrimaryFailed {
			outcome = queryOutcome
		}
		for idx := range results {
			res := textutil.MatchAuthorName(name, results[idx].Name)
			var score float64
			switch res.Kind {
			case textutil.AuthorMatchExact:
				score = exactScore
			case textutil.AuthorMatchFuzzyAuto:
				score = res.Score
			case textutil.AuthorMatchFuzzyAmbiguous:
				sawAmbiguous = true
				continue
			default:
				continue
			}
			cp := results[idx]
			// Treat duplicates of the same upstream foreignID as the same
			// candidate rather than an ambiguity signal.
			if best != nil && best.ForeignID != "" && best.ForeignID == cp.ForeignID {
				if score > bestScore {
					bestScore = score
				}
				continue
			}
			if score >= exactScore {
				exactHits[cp.ForeignID] = struct{}{}
				if cp.ForeignID != "" {
					if existing, ok := exactMatches[cp.ForeignID]; !ok || authorSearchWorkCount(cp) > authorSearchWorkCount(existing) {
						exactMatches[cp.ForeignID] = cp
					}
				}
			}
			if best == nil || score > bestScore {
				secondScore = bestScore
				best = &cp
				bestScore = score
				matchedQuery = query
			} else if score > secondScore {
				secondScore = score
			}
		}
		if best != nil && bestScore >= exactScore {
			break
		}
	}
	if best == nil {
		if sawAmbiguous {
			slog.Info("abs import: upstream author match ambiguous band", "author", name)
			return nil, true, nil
		}
		slog.Debug("abs import: upstream author match not found", "author", name, "queries", authorSearchQueries(name))
		return nil, false, nil
	}
	if len(exactHits) > 1 {
		dominant, ok := dominantExactAuthorMatch(exactMatches)
		if !ok {
			slog.Info("abs import: upstream author match ambiguous", "author", name, "hits", len(exactHits))
			return nil, true, nil
		}
		best = &dominant
		bestScore = exactScore
	}
	if bestScore < exactScore && bestScore-secondScore < fuzzyTieMargin {
		slog.Info("abs import: upstream author match ambiguous (tie)", "author", name, "best", bestScore, "second", secondScore)
		return nil, true, nil
	}
	// The match is only allowed to become this author's permanent provider
	// link if the primary provider was actually consulted. With
	// metadata.primary_provider = hardcover and a free-tier token, a 429 used
	// to leave OpenLibrary as the only provider that answered, its record won
	// by walkover, and the author synced from OpenLibrary forever because
	// providerForForeignID reads the provider back off the id we wrote here
	// (#2271). Refusing the bind leaves the author unlinked for this run,
	// which a later import or a manual relink can still fix; writing it does
	// not.
	if !outcome.SafeToBind(best.ForeignID) {
		slog.Warn("abs import: refusing to bind author to a fallback provider",
			"author", name, "primary", outcome.Primary, "failed", outcome.FailureSummary(),
			"wouldHaveLinked", best.ForeignID)
		return nil, false, &PrimaryProviderUnavailableError{
			Primary: outcome.Primary,
			Failed:  outcome.FailureSummary(),
			Err:     outcome.FirstErr,
		}
	}
	full, err := i.meta.GetAuthor(ctx, best.ForeignID)
	if err != nil {
		return nil, false, err
	}
	slog.Info("abs import: upstream author matched", "author", name, "query", matchedQuery, "foreignId", best.ForeignID, "score", bestScore)
	return full, false, nil
}

// PrimaryProviderUnavailableError reports that a lookup was abandoned rather
// than resolved against a fallback provider, because the configured primary
// did not answer. It is not an import failure: the item still imports, the
// author simply keeps whatever identity it had instead of being bound to the
// wrong provider permanently (#2271).
type PrimaryProviderUnavailableError struct {
	Primary string
	Failed  string
	Err     error
}

func (e *PrimaryProviderUnavailableError) Error() string {
	msg := fmt.Sprintf("primary metadata provider %q did not answer", e.Primary)
	if e.Failed != "" {
		msg = fmt.Sprintf("metadata provider %s did not answer", e.Failed)
	}
	if e.Err != nil {
		return msg + ": " + e.Err.Error()
	}
	return msg
}

func (e *PrimaryProviderUnavailableError) Unwrap() error { return e.Err }

func dominantExactAuthorMatch(candidates map[string]models.Author) (models.Author, bool) {
	const minDominantGap = 10
	var best models.Author
	bestCount := -1
	secondCount := -1
	for _, candidate := range candidates {
		count := authorSearchWorkCount(candidate)
		if count > bestCount {
			secondCount = bestCount
			best = candidate
			bestCount = count
		} else if count > secondCount {
			secondCount = count
		}
	}
	if bestCount <= 0 {
		return models.Author{}, false
	}
	if secondCount < 0 {
		return best, true
	}
	if bestCount-secondCount < minDominantGap {
		return models.Author{}, false
	}
	if secondCount > 0 && bestCount < secondCount*2 {
		return models.Author{}, false
	}
	return best, true
}

func authorSearchWorkCount(author models.Author) int {
	if author.Statistics == nil {
		return 0
	}
	return author.Statistics.BookCount
}

// authorSearchQueries delegates to textutil, which owns this expansion. It and
// the three helpers it used were byte-identical to the copies in internal/api
// (#1648).
func authorSearchQueries(name string) []string {
	return textutil.AuthorSearchQueries(name)
}
