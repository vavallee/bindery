package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// SourceQuery is what a Source is asked. Book searches carry Title, Author and
// MediaType; the free text search carries Text only.
type SourceQuery struct {
	Title     string
	Author    string
	MediaType string
	Text      string
}

// Source contributes release objects to a search without being an indexer: no
// row in the indexers table, no Newznab client, no indexer id. The first one is
// Soulseek through slskd (#1717). Results it returns have a nil IndexerID, and
// everything downstream of a search must tolerate that.
//
// A Source owns its own limits and timeouts and must return promptly when ctx
// is done. It is asked only by searches that opt in with WithSources, which is
// how interactive search shows Soulseek results while the scheduler's automatic
// searches, which cannot grab from Soulseek yet, never touch the network.
type Source interface {
	// Name labels the source in the search debug trail and as the result's
	// IndexerName.
	Name() string
	Search(ctx context.Context, q SourceQuery) ([]newznab.SearchResult, error)
}

// SourceDebug is one source's row in the search debug trail.
type SourceDebug struct {
	Name        string `json:"name"`
	ResultCount int    `json:"resultCount"`
	DurationMs  int64  `json:"durationMs"`
	Error       string `json:"error,omitempty"`
}

// sourceBudget is the most a search waits for any one source. A slow or hung
// source (slskd waiting on peers, or queued behind another search) must not
// hold the indexer results back, so past this the search goes on without it and
// the debug trail says it timed out. A var so tests can shorten it.
var sourceBudget = 20 * time.Second

type sourcesKey struct{}

// WithSources returns a context that makes the searches run under it also ask
// srcs. With none, it returns ctx unchanged.
func WithSources(ctx context.Context, srcs ...Source) context.Context {
	if len(srcs) == 0 {
		return ctx
	}
	return context.WithValue(ctx, sourcesKey{}, srcs)
}

func sourcesFrom(ctx context.Context) []Source {
	srcs, _ := ctx.Value(sourcesKey{}).([]Source)
	return srcs
}

// askSource runs one source under sourceBudget. The source is given a context
// that ends at the budget and is expected to honour it; a source that does not
// is abandoned shortly after, so one that ignores its context cannot hold the
// search either.
func askSource(ctx context.Context, src Source, q SourceQuery) ([]newznab.SearchResult, error) {
	sctx, cancel := context.WithTimeout(ctx, sourceBudget)
	defer cancel()
	type answer struct {
		hits []newznab.SearchResult
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		hits, err := src.Search(sctx, q)
		done <- answer{hits, err}
	}()
	grace := time.NewTimer(sourceBudget + 2*time.Second)
	defer grace.Stop()
	select {
	case a := <-done:
		if a.err != nil && sctx.Err() != nil && ctx.Err() == nil {
			return nil, fmt.Errorf("timed out after %s", sourceBudget)
		}
		return a.hits, a.err
	case <-grace.C:
		return nil, fmt.Errorf("timed out after %s", sourceBudget)
	}
}

// startSources asks every source in ctx concurrently and returns a function
// that waits for them and returns their results and debug rows. With no
// sources it starts nothing and the wait returns immediately.
func startSources(ctx context.Context, q SourceQuery) func() ([]newznab.SearchResult, []SourceDebug) {
	srcs := sourcesFrom(ctx)
	if len(srcs) == 0 {
		return func() ([]newznab.SearchResult, []SourceDebug) { return nil, nil }
	}
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results []newznab.SearchResult
		rows    = make([]SourceDebug, len(srcs))
	)
	for i, src := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			hits, err := askSource(ctx, src, q)
			row := SourceDebug{Name: src.Name(), DurationMs: time.Since(start).Milliseconds()}
			if err != nil {
				row.Error = err.Error()
				slog.Warn("search source failed", "source", src.Name(), "error", err)
			} else {
				for j := range hits {
					// A source never has an indexer id, whatever it set.
					hits[j].IndexerID = nil
					if hits[j].IndexerName == "" {
						hits[j].IndexerName = src.Name()
					}
				}
				row.ResultCount = len(hits)
			}
			mu.Lock()
			if err == nil {
				results = append(results, hits...)
			}
			rows[i] = row
			mu.Unlock()
		}()
	}
	return func() ([]newznab.SearchResult, []SourceDebug) {
		wg.Wait()
		return results, rows
	}
}
