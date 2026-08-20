package abs

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/vavallee/bindery/internal/concurrency"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/seriesmatch"
	"github.com/vavallee/bindery/internal/textutil"
)

const (
	// hardcoverSeriesCatalogConcurrency bounds the catalog fan-out per book.
	// A search returns at most five results and a book rarely has more than
	// two usable series queries, so four in flight covers the realistic worst
	// case without turning one import into a burst against Hardcover.
	hardcoverSeriesCatalogConcurrency = 4

	// Bounded but deliberately NOT paced. RunBoundedPaced gates every launch
	// on a wall-clock interval, and the aggregator caches series catalogs
	// (internal/metadata/aggregator_series.go), so a paced fan-out would
	// pay that interval even when the call is about to be served from cache. A
	// real library reuses the same series across many books, so cache hits are
	// the common case here, and pacing would tax the whole import for a
	// burst risk that the bound above already covers.
	//
	// A cap is still meaningful: matchHardcoverSeries is called once per item,
	// serially, from Importer.importItem (importer.go:670), so this bound is
	// the ceiling on concurrent Hardcover calls for the whole import, not a
	// per-book burst on top of other work.
)

type hardcoverSeriesQuery struct {
	Title    string
	Sequence string
	FromABS  bool
}

type hardcoverSeriesCandidate struct {
	Result      metadata.SeriesSearchResult
	Catalog     *metadata.SeriesCatalog
	Book        metadata.SeriesCatalogBook
	Score       int
	MatchedBy   string
	SeriesScore int
	TitleScore  int
}

func (i *Importer) matchHardcoverSeries(ctx context.Context, cfg ImportConfig, runID int64, author *models.Author, book *models.Book, item NormalizedLibraryItem, stats *ImportStats) (metadataMergeResult, seriesUpsertResult) {
	if i.meta == nil || i.series == nil || i.books == nil || book == nil {
		return metadataMergeResult{}, seriesUpsertResult{}
	}
	queries := hardcoverSeriesQueries(item)
	if len(queries) == 0 {
		return metadataMergeResult{}, seriesUpsertResult{}
	}
	authorName := primaryAuthorName(item)
	if author != nil && strings.TrimSpace(author.Name) != "" {
		authorName = strings.TrimSpace(author.Name)
	}
	// Search first, collecting every (query, result) pair, then fetch the
	// catalogs those results point at concurrently, then score. Splitting the
	// three phases buys two things over the nested loop this replaces.
	//
	// The catalog fetches stop being serial. Each one is an outbound Hardcover
	// call, and there are up to five per query, so a cold cache used to mean
	// five round trips end to end for every book an import touches.
	//
	// Collecting unique ForeignIDs before fetching also stops the same series
	// being requested once per query that returned it, which happens routinely
	// when a book carries both an ABS series name and an alternate spelling.
	// That is tidiness rather than a saving: the aggregator already caches by
	// ForeignID, so the repeat never reached the network. It does mean the
	// fan-out width reflects real work instead of duplicates.
	type pendingCandidate struct {
		query  hardcoverSeriesQuery
		result metadata.SeriesSearchResult
	}
	var pending []pendingCandidate
	for _, query := range queries {
		results, err := i.meta.SearchSeries(ctx, query.Title, 5)
		if err != nil {
			slog.Warn("abs import: hardcover series search failed", "itemID", item.ItemID, "query", query.Title, "error", err)
			continue
		}
		for _, result := range results {
			pending = append(pending, pendingCandidate{query: query, result: result})
		}
	}

	wanted := make([]string, 0, len(pending))
	seenSeries := make(map[string]struct{}, len(pending))
	for _, p := range pending {
		if _, ok := seenSeries[p.result.ForeignID]; ok {
			continue
		}
		seenSeries[p.result.ForeignID] = struct{}{}
		wanted = append(wanted, p.result.ForeignID)
	}

	var catalogMu sync.Mutex
	catalogs := make(map[string]*metadata.SeriesCatalog, len(wanted))
	concurrency.RunBounded(ctx, wanted, hardcoverSeriesCatalogConcurrency,
		func(ctx context.Context, foreignID string) {
			catalog, err := i.meta.GetSeriesCatalog(ctx, foreignID)
			if err != nil {
				slog.Warn("abs import: hardcover series catalog failed", "itemID", item.ItemID, "series", foreignID, "error", err)
				return
			}
			if catalog == nil {
				return
			}
			catalogMu.Lock()
			catalogs[foreignID] = catalog
			catalogMu.Unlock()
		})

	// Score in the original query-then-result order so the highest-score-wins
	// tie-break below resolves exactly as it did serially. The fan-out above
	// only changes when the catalogs are fetched, never which one is picked.
	candidates := make(map[string]hardcoverSeriesCandidate)
	for _, p := range pending {
		catalog := catalogs[p.result.ForeignID]
		if catalog == nil {
			continue
		}
		candidate, ok := evaluateHardcoverSeriesCandidate(p.query, authorName, item, p.result, catalog)
		if !ok {
			continue
		}
		key := catalog.ForeignID
		if existing, exists := candidates[key]; !exists || candidate.Score > existing.Score {
			candidates[key] = candidate
		}
	}
	selected, ambiguous := selectHardcoverSeriesCandidate(candidates)
	if ambiguous {
		return metadataMergeResult{Messages: []string{"hardcover series link skipped: match was ambiguous"}}, seriesUpsertResult{}
	}
	if selected == nil {
		return metadataMergeResult{}, seriesUpsertResult{}
	}

	seriesResult, err := i.upsertHardcoverSeries(ctx, cfg, runID, book.ID, item.ItemID, selected.Catalog, selected.Book, stats)
	if err != nil {
		slog.Warn("abs import: hardcover series link failed", "itemID", item.ItemID, "series", selected.Catalog.ForeignID, "error", err)
		return metadataMergeResult{}, seriesUpsertResult{}
	}

	metaResult := metadataMergeResult{}
	if !cfg.DryRun && selected.Book.Book.ForeignID != "" {
		if mergeResult, err := i.mergeUpstreamBook(ctx, cfg, item, book, &selected.Book.Book, "hardcover_series"); err != nil {
			slog.Warn("abs import: hardcover series metadata merge failed", "itemID", item.ItemID, "book", selected.Book.Book.ForeignID, "error", err)
		} else {
			metaResult = mergeResult
		}
	}
	if !cfg.DryRun {
		extra, err := i.linkExistingHardcoverCatalogBooks(ctx, cfg, runID, author, selected.Catalog, book.ID, seriesResult.CreatedSeries)
		if err != nil {
			slog.Warn("abs import: hardcover catalog existing-book linking failed", "itemID", item.ItemID, "series", selected.Catalog.ForeignID, "error", err)
		} else if stats != nil {
			stats.SeriesLinked += extra
		}
	}
	if seriesResult.MatchedBy != "" {
		metaResult.Messages = append(metaResult.Messages, fmt.Sprintf("hardcover series linked by %s", selected.MatchedBy))
	}
	return metaResult, seriesResult
}

func hardcoverSeriesQueries(item NormalizedLibraryItem) []hardcoverSeriesQuery {
	queries := make([]hardcoverSeriesQuery, 0, len(item.Series)+1)
	seen := map[string]struct{}{}
	for _, series := range item.Series {
		title := strings.TrimSpace(series.Name)
		if title == "" {
			continue
		}
		key := normalizeTitle(title)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		queries = append(queries, hardcoverSeriesQuery{
			Title:    title,
			Sequence: strings.TrimSpace(series.Sequence),
			FromABS:  true,
		})
	}
	if len(queries) > 0 {
		return queries
	}
	if title := strings.TrimSpace(item.Title); title != "" {
		queries = append(queries, hardcoverSeriesQuery{Title: title})
	}
	return queries
}

func evaluateHardcoverSeriesCandidate(query hardcoverSeriesQuery, authorName string, item NormalizedLibraryItem, result metadata.SeriesSearchResult, catalog *metadata.SeriesCatalog) (hardcoverSeriesCandidate, bool) {
	if catalog == nil || catalog.ForeignID == "" || len(catalog.Books) == 0 {
		return hardcoverSeriesCandidate{}, false
	}
	hcAuthor := firstNonEmpty(result.AuthorName, catalog.AuthorName)
	authorScore := hardcoverAuthorScore(authorName, hcAuthor)
	if strings.TrimSpace(authorName) != "" && strings.TrimSpace(hcAuthor) != "" && authorScore == 0 {
		return hardcoverSeriesCandidate{}, false
	}
	matchedBook, titleScore, positionMatched, ok := matchHardcoverCatalogBook(item, query.Sequence, catalog.Books)
	if !ok {
		return hardcoverSeriesCandidate{}, false
	}
	seriesScore := 0
	if query.FromABS {
		seriesScore = shelfarrTitleScore(query.Title, firstNonEmpty(result.Title, catalog.Title))
		if normalizeSeriesName(query.Title) == normalizeSeriesName(firstNonEmpty(result.Title, catalog.Title)) {
			seriesScore = 100
		}
		if seriesScore < 80 {
			return hardcoverSeriesCandidate{}, false
		}
	}
	score := titleScore
	matchedBy := "hardcover_title"
	if positionMatched {
		score += 25
		matchedBy = "hardcover_position_title"
	}
	if query.FromABS {
		score += seriesScore / 2
		matchedBy = "hardcover_series_title"
		if positionMatched {
			matchedBy = "hardcover_series_position"
		}
	}
	score += authorScore
	if score < 105 {
		return hardcoverSeriesCandidate{}, false
	}
	return hardcoverSeriesCandidate{
		Result:      result,
		Catalog:     catalog,
		Book:        matchedBook,
		Score:       score,
		MatchedBy:   matchedBy,
		SeriesScore: seriesScore,
		TitleScore:  titleScore,
	}, true
}

func matchHardcoverCatalogBook(item NormalizedLibraryItem, sequence string, books []metadata.SeriesCatalogBook) (metadata.SeriesCatalogBook, int, bool, bool) {
	sequence = strings.TrimSpace(sequence)
	bestScore := 0
	var best metadata.SeriesCatalogBook
	matches := 0
	for _, book := range books {
		positionMatched := sequence != "" && sameSeriesPosition(sequence, book.Position)
		if sequence != "" && !positionMatched {
			continue
		}
		score := shelfarrTitleScore(item.Title, firstNonEmpty(book.Title, book.Book.Title))
		threshold := 88
		if positionMatched {
			threshold = 70
		}
		if score < threshold {
			continue
		}
		if score > bestScore {
			bestScore = score
			best = book
			matches = 1
			continue
		}
		if score == bestScore {
			matches++
		}
	}
	if matches == 1 {
		return best, bestScore, sequence != "" && sameSeriesPosition(sequence, best.Position), true
	}
	return metadata.SeriesCatalogBook{}, 0, false, false
}

func selectHardcoverSeriesCandidate(candidates map[string]hardcoverSeriesCandidate) (*hardcoverSeriesCandidate, bool) {
	if len(candidates) == 0 {
		return nil, false
	}
	ordered := make([]hardcoverSeriesCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, candidate)
	}
	sort.SliceStable(ordered, func(a, b int) bool {
		if ordered[a].Score == ordered[b].Score {
			return ordered[a].Catalog.ForeignID < ordered[b].Catalog.ForeignID
		}
		return ordered[a].Score > ordered[b].Score
	})
	if len(ordered) > 1 && ordered[0].Score-ordered[1].Score < 10 {
		return nil, true
	}
	return &ordered[0], false
}

func (i *Importer) upsertHardcoverSeries(ctx context.Context, cfg ImportConfig, runID, bookID int64, itemID string, catalog *metadata.SeriesCatalog, matchedBook metadata.SeriesCatalogBook, stats *ImportStats) (seriesUpsertResult, error) {
	if catalog == nil || strings.TrimSpace(catalog.Title) == "" || strings.TrimSpace(catalog.ForeignID) == "" {
		return seriesUpsertResult{}, nil
	}
	existing, err := i.series.GetByForeignID(ctx, catalog.ForeignID)
	if err != nil {
		return seriesUpsertResult{}, err
	}
	matchedBy := ""
	if existing == nil {
		match, ambiguous, err := i.findSeriesByTitle(ctx, catalog.Title)
		if err != nil {
			return seriesUpsertResult{}, err
		}
		if ambiguous {
			return seriesUpsertResult{}, fmt.Errorf("ambiguous existing series match for %q", catalog.Title)
		}
		if match != nil {
			existing = match
			matchedBy = "normalized_title"
			if shouldPromoteSeriesToHardcover(match, catalog) {
				if prior, err := i.series.GetByForeignID(ctx, catalog.ForeignID); err != nil {
					return seriesUpsertResult{}, err
				} else if prior == nil {
					if !cfg.DryRun {
						if err := i.series.UpdateForeignID(ctx, existing.ID, catalog.ForeignID); err != nil {
							return seriesUpsertResult{}, err
						}
					}
					existing.ForeignID = catalog.ForeignID
					matchedBy = "hardcover_promotion"
				}
			}
		}
	}
	created := false
	if existing == nil {
		if cfg.DryRun && stats != nil && stats.dryRunSeriesAlreadyPlanned(catalog.ForeignID, catalog.Title) {
			countKey := seriesMembershipCountKey(0, catalog.Title, catalog.ForeignID, bookID, itemID)
			membershipCreated := !stats.dryRunSeriesMembershipAlreadyPlanned(countKey)
			membershipExternalID := seriesMembershipExternalID(catalog.ForeignID, bookID, itemID)
			if membershipCreated {
				_ = i.recordRunEntity(ctx, runID, cfg, cfg.LibraryID, itemID, entityTypeSeries, membershipExternalID, 0, itemOutcomeLinked, map[string]any{
					"bookId":          bookID,
					"sequence":        strings.TrimSpace(matchedBook.Position),
					"matchedBy":       "hardcover_series",
					"hardcoverBookId": strings.TrimSpace(matchedBook.ForeignID),
				})
				stats.rememberDryRunSeriesMembership(countKey)
			}
			return seriesUpsertResult{
				IdentityExternalID:   catalog.ForeignID,
				MembershipExternalID: membershipExternalID,
				CountKey:             countKey,
				MembershipCreated:    membershipCreated,
				Linked:               true,
				MatchedBy:            "planned",
			}, nil
		}
		existing = &models.Series{
			ForeignID:   catalog.ForeignID,
			Title:       strings.TrimSpace(catalog.Title),
			Description: "",
		}
		if !cfg.DryRun {
			if err := i.series.CreateOrGet(ctx, existing); err != nil {
				return seriesUpsertResult{}, err
			}
		}
		created = true
		matchedBy = "created"
	}
	localID := existing.ID
	if cfg.DryRun && created {
		localID = 0
	}
	countKey := seriesMembershipCountKey(localID, catalog.Title, catalog.ForeignID, bookID, itemID)
	membershipExternalID := seriesMembershipExternalID(catalog.ForeignID, bookID, itemID)
	if !cfg.DryRun {
		if err := i.ensureHardcoverSeriesLink(ctx, existing.ID, catalog); err != nil {
			return seriesUpsertResult{}, err
		}
	}
	metadata := map[string]any{
		"bookId":          bookID,
		"sequence":        strings.TrimSpace(matchedBook.Position),
		"matchedBy":       "hardcover_series",
		"hardcoverBookId": strings.TrimSpace(matchedBook.ForeignID),
	}
	linkCreated := false
	if cfg.DryRun {
		if stats == nil || !stats.dryRunSeriesMembershipAlreadyPlanned(countKey) {
			linkCreated = true
			outcome := itemOutcomeLinked
			if created {
				outcome = itemOutcomeCreated
			}
			_ = i.recordRunEntity(ctx, runID, cfg, cfg.LibraryID, itemID, entityTypeSeries, membershipExternalID, localID, outcome, metadata)
			if stats != nil {
				stats.rememberDryRunSeriesMembership(countKey)
			}
		}
		if created && stats != nil {
			stats.rememberDryRunSeries(catalog.ForeignID, catalog.Title)
		}
	} else {
		var err error
		linkCreated, err = i.series.LinkBookIfMissing(ctx, existing.ID, bookID, strings.TrimSpace(matchedBook.Position), true)
		if err != nil {
			return seriesUpsertResult{}, err
		}
		if linkCreated {
			if err := i.upsertProvenance(ctx, &models.ABSProvenance{
				SourceID:    cfg.SourceID,
				LibraryID:   cfg.LibraryID,
				EntityType:  entityTypeSeries,
				ExternalID:  membershipExternalID,
				LocalID:     existing.ID,
				ItemID:      itemID,
				ImportRunID: ptrInt64(runID),
			}); err != nil {
				return seriesUpsertResult{}, err
			}
		}
	}
	outcome := itemOutcomeLinked
	if created {
		outcome = itemOutcomeCreated
	}
	if !cfg.DryRun && linkCreated {
		_ = i.recordRunEntity(ctx, runID, cfg, cfg.LibraryID, itemID, entityTypeSeries, membershipExternalID, localID, outcome, metadata)
	}
	return seriesUpsertResult{
		SeriesID:             localID,
		IdentityExternalID:   catalog.ForeignID,
		MembershipExternalID: membershipExternalID,
		CountKey:             countKey,
		CreatedSeries:        created,
		MembershipCreated:    linkCreated,
		Linked:               true,
		MatchedBy:            matchedBy,
	}, nil
}

func (i *Importer) ensureHardcoverSeriesLink(ctx context.Context, seriesID int64, catalog *metadata.SeriesCatalog) error {
	if i.series == nil || catalog == nil || seriesID == 0 {
		return nil
	}
	existing, err := i.series.GetHardcoverLink(ctx, seriesID)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	bookCount := catalog.BookCount
	if bookCount == 0 {
		bookCount = len(catalog.Books)
	}
	return i.series.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            seriesID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      strings.TrimSpace(catalog.Title),
		HardcoverAuthorName: strings.TrimSpace(catalog.AuthorName),
		HardcoverBookCount:  bookCount,
		Confidence:          0.8,
		LinkedBy:            "auto",
	})
}

func (i *Importer) linkExistingHardcoverCatalogBooks(ctx context.Context, cfg ImportConfig, runID int64, author *models.Author, catalog *metadata.SeriesCatalog, importedBookID int64, createdSeries bool) (int, error) {
	if catalog == nil || author == nil {
		return 0, nil
	}
	seriesRow, err := i.series.GetByForeignID(ctx, catalog.ForeignID)
	if err != nil {
		return 0, err
	}
	if seriesRow == nil {
		match, ambiguous, err := i.findSeriesByTitle(ctx, catalog.Title)
		if err != nil || ambiguous {
			return 0, err
		}
		seriesRow = match
	}
	if seriesRow == nil {
		return 0, nil
	}
	linked := 0
	for _, catalogBook := range catalog.Books {
		localBook, err := i.findExistingHardcoverCatalogBook(ctx, author.ID, catalogBook)
		if err != nil {
			return linked, err
		}
		if localBook == nil || localBook.ID == importedBookID {
			continue
		}
		if catalogBook.Book.Author != nil && textutil.MatchAuthorName(author.Name, catalogBook.Book.Author.Name).Kind == textutil.AuthorMatchNone {
			continue
		}
		linkCreated, err := i.series.LinkBookIfMissing(ctx, seriesRow.ID, localBook.ID, strings.TrimSpace(catalogBook.Position), false)
		if err != nil {
			return linked, err
		}
		if !linkCreated {
			continue
		}
		linkExternalID := seriesMembershipExternalID(catalog.ForeignID, localBook.ID, "")
		if err := i.upsertProvenance(ctx, &models.ABSProvenance{
			SourceID:    cfg.SourceID,
			LibraryID:   cfg.LibraryID,
			EntityType:  entityTypeSeries,
			ExternalID:  linkExternalID,
			LocalID:     seriesRow.ID,
			ItemID:      "",
			ImportRunID: ptrInt64(runID),
		}); err != nil {
			return linked, err
		}
		outcome := itemOutcomeLinked
		if createdSeries {
			outcome = itemOutcomeCreated
		}
		_ = i.recordRunEntity(ctx, runID, cfg, cfg.LibraryID, "", entityTypeSeries, linkExternalID, seriesRow.ID, outcome, map[string]any{
			"bookId":          localBook.ID,
			"sequence":        strings.TrimSpace(catalogBook.Position),
			"matchedBy":       "hardcover_catalog_existing_book",
			"hardcoverBookId": strings.TrimSpace(catalogBook.ForeignID),
		})
		linked++
	}
	return linked, nil
}

func (i *Importer) findExistingHardcoverCatalogBook(ctx context.Context, authorID int64, catalogBook metadata.SeriesCatalogBook) (*models.Book, error) {
	if strings.TrimSpace(catalogBook.ForeignID) != "" {
		existing, err := i.books.GetByForeignID(ctx, catalogBook.ForeignID)
		if err != nil || existing != nil {
			return existing, err
		}
	}
	title := firstNonEmpty(catalogBook.Title, catalogBook.Book.Title)
	match, ambiguous, err := i.findBookByNormalizedTitle(ctx, authorID, title, nil)
	if err != nil || ambiguous || match == nil {
		return nil, err
	}
	if shelfarrTitleScore(match.Title, title) < 92 {
		return nil, nil
	}
	return match, nil
}

func shouldPromoteSeriesToHardcover(existing *models.Series, catalog *metadata.SeriesCatalog) bool {
	if existing == nil || catalog == nil {
		return false
	}
	if !strings.HasPrefix(existing.ForeignID, "abs:series:") {
		return false
	}
	return normalizeSeriesName(existing.Title) == normalizeSeriesName(catalog.Title)
}

func hardcoverAuthorScore(absAuthor, hcAuthor string) int {
	absAuthor = strings.TrimSpace(absAuthor)
	hcAuthor = strings.TrimSpace(hcAuthor)
	if absAuthor == "" || hcAuthor == "" {
		return 0
	}
	match := textutil.MatchAuthorName(absAuthor, hcAuthor)
	switch match.Kind {
	case textutil.AuthorMatchExact:
		return 30
	case textutil.AuthorMatchFuzzyAuto:
		return 20
	default:
		score := shelfarrTitleScore(absAuthor, hcAuthor)
		if score >= 90 {
			return 15
		}
		return 0
	}
}

func sameSeriesPosition(a, b string) bool {
	return seriesmatch.SamePosition(a, b)
}

func normalizeSeriesName(name string) string {
	return seriesmatch.NormalizeSeriesName(name)
}

func shelfarrTitleScore(a, b string) int {
	return seriesmatch.TitleScore(a, b)
}
