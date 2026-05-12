package abs

import (
	"context"
	"log/slog"
	"strings"
	"unicode"

	"github.com/vavallee/bindery/internal/db"
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
// then Jaro-Winkler fuzzy matching. The returned matchedBy string distinguishes
// these tiers so callers can decide when to record a variant alias.
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

	// Tier 3: Jaro-Winkler fuzzy match. Collect the best score per author
	// across both direct name and alias comparisons.
	type scored struct {
		author    *models.Author
		score     float64
		fromAlias bool
	}
	best := make(map[int64]*scored)
	consider := func(a *models.Author, score float64, fromAlias bool) {
		if a == nil {
			return
		}
		existing, ok := best[a.ID]
		if !ok || score > existing.score {
			best[a.ID] = &scored{author: a, score: score, fromAlias: fromAlias}
			return
		}
		if score == existing.score && existing.fromAlias && !fromAlias {
			// Prefer a direct-name match over alias when scores tie.
			existing.fromAlias = false
		}
	}
	for _, author := range m.all {
		res := textutil.MatchAuthorName(name, author.Name)
		if res.Score < textutil.AuthorMatchAmbiguousMinimum {
			continue
		}
		cp := *author
		consider(&cp, res.Score, false)
	}
	for _, alias := range m.aliases {
		res := textutil.MatchAuthorName(name, alias.Name)
		if res.Score < textutil.AuthorMatchAmbiguousMinimum {
			continue
		}
		author, trusted, err := m.trustedAliasAuthor(ctx, alias)
		if err != nil {
			return nil, "", false, err
		}
		if !trusted || author == nil {
			continue
		}
		consider(author, res.Score, true)
	}
	if len(best) == 0 {
		return nil, "", false, nil
	}

	var top *scored
	var second float64
	for _, s := range best {
		if top == nil || s.score > top.score {
			if top != nil {
				second = top.score
			}
			top = s
		} else if s.score > second {
			second = s.score
		}
	}
	if top.score >= textutil.AuthorMatchAutoThreshold {
		// Require a clear margin over any close runner-up before auto-matching.
		const fuzzyTieMargin = 0.02
		if len(best) > 1 && top.score-second < fuzzyTieMargin {
			return nil, "", true, nil
		}
		matchedBy := "fuzzy_name"
		if top.fromAlias {
			matchedBy = "fuzzy_alias"
		}
		return top.author, matchedBy, false, nil
	}
	// Best score is in the ambiguous band (0.88 <= score < 0.94): surface as
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

func (i *Importer) lookupUpstreamAuthor(ctx context.Context, name string) (*models.Author, bool, error) {
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
	for _, query := range authorSearchQueries(name) {
		results, err := i.meta.SearchAuthors(ctx, query)
		if err != nil {
			return nil, false, err
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
	full, err := i.meta.GetAuthor(ctx, best.ForeignID)
	if err != nil {
		return nil, false, err
	}
	slog.Info("abs import: upstream author matched", "author", name, "query", matchedQuery, "foreignId", best.ForeignID, "score", bestScore)
	return full, false, nil
}

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

func authorSearchQueries(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	queries := []string{name}
	if compact := compactInitialsAuthorQuery(name); compact != "" {
		queries = append(queries, compact)
	}
	if norm := normalizeAuthorName(name); norm != "" {
		queries = append(queries, norm)
		if surname := initialedSurnameFallback(norm); surname != "" {
			queries = append(queries, surname)
		}
	}
	return dedupeStrings(queries)
}

func compactInitialsAuthorQuery(name string) string {
	fields := strings.Fields(name)
	if len(fields) < 3 {
		return ""
	}
	initials := make([]string, 0, len(fields)-1)
	idx := 0
	for idx < len(fields)-1 {
		initial, ok := authorInitial(fields[idx])
		if !ok {
			break
		}
		initials = append(initials, strings.ToUpper(initial)+".")
		idx++
	}
	if len(initials) < 2 || idx >= len(fields) {
		return ""
	}
	return strings.Join(initials, "") + " " + strings.Join(fields[idx:], " ")
}

func authorInitial(token string) (string, bool) {
	var letters []rune
	for _, r := range token {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters = append(letters, unicode.ToLower(r))
		}
	}
	if len(letters) != 1 {
		return "", false
	}
	return string(letters[0]), true
}

func initialedSurnameFallback(normalized string) string {
	fields := strings.Fields(normalized)
	if len(fields) < 2 {
		return ""
	}
	for _, field := range fields[:len(fields)-1] {
		if len([]rune(field)) != 1 {
			return ""
		}
	}
	return fields[len(fields)-1]
}
