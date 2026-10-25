package indexer

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// SearchDebug captures the audit trail of a single search operation across all
// configured indexers. It is attached to search responses so users can see
// exactly why a search returned zero (or unexpected) results — which indexer
// was queried, what it returned, and which filter rejected each candidate.
type SearchDebug struct {
	Query      SearchQueryDebug `json:"query"`
	Indexers   []IndexerDebug   `json:"indexers"`
	Pipeline   PipelineDebug    `json:"pipeline"`
	Filters    []FilterDebug    `json:"filters"`
	StartedAt  time.Time        `json:"startedAt"`
	DurationMs int64            `json:"durationMs"`

	// The fields below say WHICH search this is, for GET /search/last-debug,
	// which more than one path now writes to (#2154). The searcher leaves them
	// empty; whoever records the trail fills them in.

	// Origin is DebugOriginInteractive for the book page's Search button, or
	// the SearchOrigin of an automatic search ("scheduled", "bulk", ...).
	Origin string `json:"origin,omitempty"`
	// BookID is the book searched for.
	BookID int64 `json:"bookId,omitempty"`
	// UserID is the signed in user who ran an interactive search. Zero for
	// an automatic search and for a caller with no user identity.
	UserID int64 `json:"userId,omitempty"`
	// Outcome is what an automatic search did with what it found, using the
	// same words as the "book search finished" log line ("grabbed",
	// "no results", "nothing approved", ...). Empty for an interactive
	// search, which only lists releases.
	Outcome string `json:"outcome,omitempty"`

	// Dropped holds the releases the relevance stage removed, whole, so the
	// interactive search can offer them for a deliberate grab (#2334). It is a
	// side field and never part of the results SearchBookWithDebug returns:
	// auto-grab reads those, and a dropped release must never reach it.
	//
	// It is not serialized (json:"-"). It carries the raw download URL and GUID,
	// credentials included, which the debug panel and GET /search/last-debug
	// must never show; the interactive handler records them in the search
	// result registry and redacts them for its own response. The handler also
	// clears the field once it has used it, so the debug log does not hold the
	// raw URLs.
	Dropped []DroppedRelease `json:"-"`
}

// Filter stage names, as FilterDebug.Stage and DroppedRelease.Stage carry them.
const (
	StageUsenetJunk = "usenet-junk"
	StageNonBook    = "non-book"
	StageRelevance  = "relevance"
	StageLanguage   = "language"
)

// DroppedRelease is a release a filter stage removed, kept whole with the stage
// and the reason. Only the relevance and language stages produce releases that
// may be offered for a grab: those are false positives that happen on real
// books. Usenet junk and non-book content are not books, so those drops are
// reported through FilterDebug alone.
type DroppedRelease struct {
	Result newznab.SearchResult
	Stage  string
	Reason string
}

// droppedFilterDebug summarises dropped releases for the Filter decisions
// panel, which carries no GUID or URL.
func droppedFilterDebug(dropped []DroppedRelease) []FilterDebug {
	if len(dropped) == 0 {
		return nil
	}
	out := make([]FilterDebug, len(dropped))
	for i, d := range dropped {
		out[i] = FilterDebug{Title: d.Result.Title, IndexerName: d.Result.IndexerName, Stage: d.Stage, Reason: d.Reason}
	}
	return out
}

// SearchQueryDebug is the effective criteria sent into the searcher.
type SearchQueryDebug struct {
	Title            string   `json:"title,omitempty"`
	Author           string   `json:"author,omitempty"`
	Year             int      `json:"year,omitempty"`
	ISBN             string   `json:"isbn,omitempty"`
	ASIN             string   `json:"asin,omitempty"`
	MediaType        string   `json:"mediaType,omitempty"`
	AllowedLanguages []string `json:"allowedLanguages,omitempty"`
	FreeText         string   `json:"freeText,omitempty"`
}

// IndexerDebug describes what happened for a single indexer: whether it was
// contacted, the categories sent, how many results came back, and any error.
type IndexerDebug struct {
	IndexerID   int64  `json:"indexerId"`
	IndexerName string `json:"indexerName"`
	Enabled     bool   `json:"enabled"`
	Skipped     bool   `json:"skipped,omitempty"`
	SkipReason  string `json:"skipReason,omitempty"`
	Categories  []int  `json:"categories,omitempty"`
	ResultCount int    `json:"resultCount"`
	DurationMs  int64  `json:"durationMs"`
	Error       string `json:"error,omitempty"`
	// NumberWordQuery is the digit form of the title ("... Book 1") when the
	// search sent the one extra number word query for this indexer (#3476),
	// and NumberWordResults how many results it returned. Both are empty on
	// the common search, so the added query cost is visible per indexer.
	NumberWordQuery   string `json:"numberWordQuery,omitempty"`
	NumberWordResults int    `json:"numberWordResults,omitempty"`
	// NumberWordError is why that extra query failed, if it did. The search
	// itself is not failed by it.
	NumberWordError string `json:"numberWordError,omitempty"`
}

// PipelineDebug counts how results flowed through each filter stage in the
// Searcher. Mismatches between stages reveal which filter is eating results.
type PipelineDebug struct {
	RawCount            int `json:"rawCount"`
	AfterDedupe         int `json:"afterDedupe"`
	AfterUsenetJunk     int `json:"afterUsenetJunk"`
	AfterNonBookContent int `json:"afterNonBookContent"`
	AfterRelevance      int `json:"afterRelevance"`
}

// FilterDebug records a per-candidate rejection emitted during the Searcher
// pipeline (Usenet junk, title/author relevance, etc.). Language filter and
// specification-based rejections happen downstream and are reflected in the
// per-result `approved`/`rejection` fields of the main response.
type FilterDebug struct {
	Title       string `json:"title"`
	IndexerName string `json:"indexerName,omitempty"`
	Stage       string `json:"stage"`
	Reason      string `json:"reason"`
}

// SearchBookWithDebug is SearchBook plus an audit trail of every decision the
// searcher made. The returned results are identical to SearchBook's output;
// callers that don't care about the debug info can keep using SearchBook.
//
// The same aggregate timeout (searchBookTimeout) as SearchBook is applied so
// the debug path cannot be used to bypass the per-search deadline.
func (s *Searcher) SearchBookWithDebug(ctx context.Context, indexers []models.Indexer, c MatchCriteria) ([]newznab.SearchResult, *SearchDebug) {
	ctx, cancel := context.WithTimeout(ctx, searchBookTimeout)
	defer cancel()

	dbg := &SearchDebug{
		StartedAt: time.Now(),
		Query: SearchQueryDebug{
			Title:            c.Title,
			Author:           c.Author,
			Year:             c.Year,
			ISBN:             c.ISBN,
			ASIN:             c.ASIN,
			MediaType:        c.MediaType,
			AllowedLanguages: c.AllowedLanguages,
		},
		Filters: []FilterDebug{},
	}

	var (
		mu      sync.Mutex
		results []newznab.SearchResult
		perIdx  = make([]IndexerDebug, 0, len(indexers))
		wg      sync.WaitGroup
	)

	for _, idx := range indexers {
		entry := IndexerDebug{
			IndexerID:   idx.ID,
			IndexerName: idx.Name,
			Enabled:     idx.Enabled,
		}
		if !idx.Enabled {
			entry.Skipped = true
			entry.SkipReason = "disabled"
			mu.Lock()
			perIdx = append(perIdx, entry)
			mu.Unlock()
			continue
		}
		// An indexer that told us to come back later is reported as skipped
		// rather than silently omitted, so the panel that showed the original
		// rate-limit error also shows why it is no longer being queried
		// (#1934).
		if reason, held := s.cooldownActive(idx); held {
			entry.Skipped = true
			entry.SkipReason = reason
			mu.Lock()
			perIdx = append(perIdx, entry)
			mu.Unlock()
			continue
		}
		// Same treatment for an indexer that has spent its daily query cap
		// (#2312): reported as skipped with the reason rather than silently
		// dropped, so the panel says why nothing came back from it.
		if reason, held := s.quotaHold(ctx, idx); held {
			entry.Skipped = true
			entry.SkipReason = reason
			mu.Lock()
			perIdx = append(perIdx, entry)
			mu.Unlock()
			continue
		}

		wg.Add(1)
		go func(idx models.Indexer, entry IndexerDebug) {
			defer wg.Done()
			defer s.flushQuota(ctx, idx, false)
			start := time.Now()

			client := s.makeClient(idx.URL, idx.APIKey)
			cats := filterCategoriesForMedia(idx.Categories, c.MediaType, idx.IncludeParentCategories)
			entry.Categories = cats

			qctx := newznab.WithQueryBudget(ctx, s.quotaBudget(idx))
			hits, trace, err := client.BookSearchTraced(qctx, c.Title, c.Author, cats)
			entry.NumberWordQuery, entry.NumberWordResults = trace.NumberWordQuery, trace.NumberWordResults
			entry.NumberWordError = trace.NumberWordError
			if trace.BudgetExhausted {
				s.flushQuota(ctx, idx, true)
			}
			entry.DurationMs = time.Since(start).Milliseconds()
			if err != nil {
				// Running out of budget part way through the tier cascade is
				// our own doing, not a refusal by the indexer, so it is
				// recorded as a skip and kept away from the health store and
				// the cooldown. HardFailures excludes skipped entries, which
				// is what stops a capped indexer being counted against the
				// grab as a failed one.
				if budgetExhausted(err) {
					s.flushQuota(ctx, idx, true)
					entry.Skipped = true
					entry.SkipReason = "daily query cap reached mid-search"
					mu.Lock()
					perIdx = append(perIdx, entry)
					mu.Unlock()
					return
				}
				entry.Error = err.Error()
				s.noteIndexerError(idx, err)
				s.health.recordFailure(ctx, idx, err)
				slog.Warn("indexer search failed", "indexer", idx.Name, "error", err)
				mu.Lock()
				perIdx = append(perIdx, entry)
				mu.Unlock()
				return
			}
			s.noteIndexerSuccess(ctx, idx)

			protocol := protocolForType(idx.Type)
			for i := range hits {
				hits[i].IndexerID = idx.ID
				hits[i].IndexerName = idx.Name
				hits[i].Protocol = protocol
				// Mirror searcher.go: scoreResult adds IndexerPriority to the
				// rank, so omitting it here made per-indexer priority a no-op
				// for manual/interactive (debug) searches.
				hits[i].IndexerPriority = idx.Priority
			}
			entry.ResultCount = len(hits)

			mu.Lock()
			results = append(results, hits...)
			perIdx = append(perIdx, entry)
			mu.Unlock()

			slog.Debug("indexer returned results", "indexer", idx.Name, "count", len(hits))
			if slog.Default().Enabled(ctx, slog.LevelDebug) {
				for _, h := range hits {
					slog.Debug("indexer result", "indexer", idx.Name, "title", h.Title, "author", h.Author)
				}
			}
		}(idx, entry)
	}
	wg.Wait()

	dbg.Indexers = perIdx
	dbg.Pipeline.RawCount = len(results)

	results = dedupe(results)
	dbg.Pipeline.AfterDedupe = len(results)

	results, junkFilters := filterUsenetJunkDebug(results, junkScope{MediaType: c.MediaType, WantedTitle: c.Title})
	dbg.Filters = append(dbg.Filters, junkFilters...)
	dbg.Pipeline.AfterUsenetJunk = len(results)

	// MUST stay in lockstep with SearchBook's pipeline (searcher.go). This stage
	// was missing here, and since SearchBookWithDebug is the only entrypoint the
	// API uses, the #1591 video guard was absent from every interactive search
	// while the scheduler had it (#1644). Ranking stays in lockstep by
	// construction: the quality profile rides on MatchCriteria, so the same
	// rankResults call below reads it (#2733).
	results, nonBook := filterNonBookContentDetailed(results)
	dbg.Filters = append(dbg.Filters, droppedFilterDebug(nonBook)...)
	dbg.Pipeline.AfterNonBookContent = len(results)

	results, relDropped := filterRelevantKeepDropped(results, c.Title, c.Author, c.AuthorAliases, c.context())
	dbg.Filters = append(dbg.Filters, droppedFilterDebug(relDropped)...)
	dbg.Pipeline.AfterRelevance = len(results)

	rankResults(results, c)
	// Most plausible first, so a cap on how many the interactive search shows
	// keeps the best ones. Side field only: results above is untouched.
	rankDropped(relDropped, c)
	dbg.Dropped = relDropped

	dbg.DurationMs = time.Since(dbg.StartedAt).Milliseconds()
	return results, dbg
}

// filterUsenetJunkDebug is filterUsenetJunk instrumented to record which
// results were dropped and why.
func filterUsenetJunkDebug(results []newznab.SearchResult, sc junkScope) ([]newznab.SearchResult, []FilterDebug) {
	out := make([]newznab.SearchResult, 0, len(results))
	var dropped []FilterDebug
	for _, r := range results {
		if reason := usenetJunkReason(r.Title, sc); reason != "" {
			dropped = append(dropped, FilterDebug{
				Title:       r.Title,
				IndexerName: r.IndexerName,
				Stage:       StageUsenetJunk,
				Reason:      reason,
			})
			continue
		}
		out = append(out, r)
	}
	return out, dropped
}

// filterRelevantDebug returns the relevance filter's kept results together with
// a record of every drop and its reason, for the interactive search panel and
// for the automatic search that projects it (SearchBookWithOutcomes). It shares
// its implementation with filterRelevant; see filterRelevantDetailed.
func filterRelevantDebug(results []newznab.SearchResult, title, author string, aliases []string) ([]newznab.SearchResult, []FilterDebug) {
	return filterRelevantDetailed(results, title, author, aliases)
}

// SearchBookWithOutcomes is SearchBook plus a per-indexer account of what
// happened, for callers that act without a human watching (#1936).
//
// SearchBook returns results and nothing else, so a failing indexer is
// indistinguishable from one that answered with nothing. Auto-grab reads that
// as "no releases exist" and picks the best of whatever the surviving indexers
// returned, with no record that part of the pool was never asked.
//
// This is a projection of SearchBookWithDebug rather than a third search
// implementation: the per-indexer records it already collects are exactly what
// is needed, they were just being kept for the interactive panel only.
func (s *Searcher) SearchBookWithOutcomes(ctx context.Context, indexers []models.Indexer, c MatchCriteria) ([]newznab.SearchResult, []IndexerDebug) {
	results, dbg := s.SearchBookWithDebug(ctx, indexers, c)
	if dbg == nil {
		return results, nil
	}
	return results, dbg.Indexers
}

// HardFailures returns the indexers that were asked and refused or failed to
// answer. Indexers skipped before being contacted (disabled, or benched by the
// #1934 cooldown) are excluded: nothing was attempted, so nothing failed.
func HardFailures(outcomes []IndexerDebug) []IndexerDebug {
	var failed []IndexerDebug
	for _, o := range outcomes {
		if !o.Skipped && o.Error != "" {
			failed = append(failed, o)
		}
	}
	return failed
}

// Attempted counts the indexers a search actually reached out to.
func Attempted(outcomes []IndexerDebug) int {
	n := 0
	for _, o := range outcomes {
		if !o.Skipped {
			n++
		}
	}
	return n
}

// rankDropped orders dropped releases by the same score the kept ones are
// ranked by, best first.
func rankDropped(dropped []DroppedRelease, c MatchCriteria) {
	if len(dropped) < 2 {
		return
	}
	ranks := newProfileRanks(c.Profile)
	scores := make(map[int]float64, len(dropped))
	idx := make([]int, len(dropped))
	for i, d := range dropped {
		idx[i] = i
		scores[i] = scoreResult(d.Result, c, ranks)
	}
	sort.SliceStable(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
	ordered := make([]DroppedRelease, len(dropped))
	for i, k := range idx {
		ordered[i] = dropped[k]
	}
	copy(dropped, ordered)
}
