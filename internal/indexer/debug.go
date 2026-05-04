package indexer

import (
	"context"
	"log/slog"
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
}

// PipelineDebug counts how results flowed through each filter stage in the
// Searcher. Mismatches between stages reveal which filter is eating results.
type PipelineDebug struct {
	RawCount        int `json:"rawCount"`
	AfterDedupe     int `json:"afterDedupe"`
	AfterUsenetJunk int `json:"afterUsenetJunk"`
	AfterRelevance  int `json:"afterRelevance"`
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
func (s *Searcher) SearchBookWithDebug(ctx context.Context, indexers []models.Indexer, c MatchCriteria) ([]newznab.SearchResult, *SearchDebug) {
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

		wg.Add(1)
		go func(idx models.Indexer, entry IndexerDebug) {
			defer wg.Done()
			start := time.Now()

			client := newznab.New(idx.URL, idx.APIKey)
			cats := filterCategoriesForMedia(idx.Categories, c.MediaType)
			entry.Categories = cats

			hits, err := client.BookSearch(ctx, c.Title, c.Author, cats)
			entry.DurationMs = time.Since(start).Milliseconds()
			if err != nil {
				entry.Error = err.Error()
				slog.Warn("indexer search failed", "indexer", idx.Name, "error", err)
				mu.Lock()
				perIdx = append(perIdx, entry)
				mu.Unlock()
				return
			}

			protocol := protocolForType(idx.Type)
			for i := range hits {
				hits[i].IndexerID = idx.ID
				hits[i].IndexerName = idx.Name
				hits[i].Protocol = protocol
			}
			entry.ResultCount = len(hits)

			mu.Lock()
			results = append(results, hits...)
			perIdx = append(perIdx, entry)
			mu.Unlock()

			slog.Debug("indexer returned results", "indexer", idx.Name, "count", len(hits))
		}(idx, entry)
	}
	wg.Wait()

	dbg.Indexers = perIdx
	dbg.Pipeline.RawCount = len(results)

	results = dedupe(results)
	dbg.Pipeline.AfterDedupe = len(results)

	results, junkFilters := filterUsenetJunkDebug(results)
	dbg.Filters = append(dbg.Filters, junkFilters...)
	dbg.Pipeline.AfterUsenetJunk = len(results)

	results, relFilters := filterRelevantDebug(results, c.Title, c.Author, c.AuthorAliases)
	dbg.Filters = append(dbg.Filters, relFilters...)
	dbg.Pipeline.AfterRelevance = len(results)

	rankResults(results, c)

	dbg.DurationMs = time.Since(dbg.StartedAt).Milliseconds()
	return results, dbg
}

// filterUsenetJunkDebug is filterUsenetJunk instrumented to record which
// results were dropped and why.
func filterUsenetJunkDebug(results []newznab.SearchResult) ([]newznab.SearchResult, []FilterDebug) {
	out := make([]newznab.SearchResult, 0, len(results))
	var dropped []FilterDebug
	for _, r := range results {
		if usenetJunkRe.MatchString(r.Title) {
			dropped = append(dropped, FilterDebug{
				Title:       r.Title,
				IndexerName: r.IndexerName,
				Stage:       "usenet-junk",
				Reason:      "looks like a raw per-article posting (RAR/PAR2/SFV/yEnc/[N/M])",
			})
			continue
		}
		out = append(out, r)
	}
	return out, dropped
}

// filterRelevantDebug is filterRelevant instrumented to record each drop with
// the keyword set that failed to match.
func filterRelevantDebug(results []newznab.SearchResult, title, author string, aliases []string) ([]newznab.SearchResult, []FilterDebug) {
	// Strip possessive author prefix before keyword extraction (mirrors filterRelevant).
	title = stripPossessivePrefix(title, author)
	fullKws := sigWords(title)
	primaryKws := sigWords(primaryTitle(title))
	authorKws := sigWords(author)
	surname := AuthorSurname(author)

	surnames := []string{surname}
	if !isAllASCIILower(surname) {
		for _, alias := range aliases {
			if s := AuthorSurname(alias); s != "" && isAllASCIILower(s) {
				surnames = append(surnames, s)
			}
		}
	}

	tryMatch := func(n string, kws []string) bool {
		for _, sn := range surnames {
			if titleMatchesResult(n, kws, sn, true) {
				return true
			}
		}
		return false
	}

	if len(fullKws) == 0 && len(primaryKws) == 0 && len(authorKws) == 0 {
		return results, nil
	}

	normTitles := make([]string, len(results))
	for i, r := range results {
		normTitles[i] = NormalizeRelease(r.Title)
	}

	filtered := make([]newznab.SearchResult, 0, len(results))
	var dropped []FilterDebug
	for i, r := range results {
		n := normTitles[i]
		fullOK := tryMatch(n, fullKws)
		primaryOK := false
		if !fullOK && len(primaryKws) > 0 && !sameKws(primaryKws, fullKws) {
			primaryOK = tryMatch(n, primaryKws)
		}
		if fullOK || primaryOK {
			filtered = append(filtered, r)
			continue
		}
		dropped = append(dropped, FilterDebug{
			Title:       r.Title,
			IndexerName: r.IndexerName,
			Stage:       "relevance",
			Reason:      "title/author keywords did not match release name",
		})
	}
	return filtered, dropped
}
