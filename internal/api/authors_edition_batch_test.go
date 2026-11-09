package api

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// batchingHardcover is a Hardcover provider that counts how it is asked for
// editions: one call per work, or one per batch.
type batchingHardcover struct {
	mu       sync.Mutex
	perBook  int
	batches  [][]string
	unanswer map[string]bool // ids a batch leaves out
}

func (p *batchingHardcover) Name() string { return "hardcover" }
func (p *batchingHardcover) SearchAuthors(context.Context, string) ([]models.Author, error) {
	return nil, nil
}
func (p *batchingHardcover) SearchBooks(context.Context, string) ([]models.Book, error) {
	return nil, nil
}
func (p *batchingHardcover) GetAuthor(context.Context, string) (*models.Author, error) {
	return nil, nil
}
func (p *batchingHardcover) GetBook(context.Context, string) (*models.Book, error) { return nil, nil }
func (p *batchingHardcover) GetBookByISBN(context.Context, string) (*models.Book, error) {
	return nil, nil
}
func (p *batchingHardcover) GetEditions(_ context.Context, id string) ([]models.Edition, error) {
	p.mu.Lock()
	p.perBook++
	p.mu.Unlock()
	return []models.Edition{{ForeignID: "hc:per-" + id}}, nil
}
func (p *batchingHardcover) GetEditionsBatch(_ context.Context, ids []string) (map[string][]models.Edition, error) {
	p.mu.Lock()
	p.batches = append(p.batches, ids)
	p.mu.Unlock()
	out := map[string][]models.Edition{}
	for _, id := range ids {
		if !p.unanswer[id] {
			out[id] = []models.Edition{{ForeignID: "hc:batch-" + id}}
		}
	}
	return out, nil
}

func TestPrefetchHardcoverEditionsBatchesPerChunk(t *testing.T) {
	provider := &batchingHardcover{unanswer: map[string]bool{"hc:book-07": true}}
	h := &AuthorHandler{meta: metadata.NewAggregator(provider)}

	var targets []editionTarget
	for i := 1; i <= 25; i++ {
		// A mix of the two fetchers: both resolve to Hardcover.
		targets = append(targets, editionTarget{ForeignID: fmt.Sprintf("hc:book-%02d", i), ViaProvider: i%2 == 0})
	}
	cache := h.prefetchHardcoverEditions(context.Background(), targets, nil)

	// 25 works are 3 batched calls; the one work the batch left out is the only
	// per work call. Before #2795 this was 25 per work calls.
	if len(provider.batches) != 3 {
		t.Fatalf("batch calls = %d, want 3", len(provider.batches))
	}
	if provider.perBook != 1 {
		t.Fatalf("per work calls = %d, want 1 (the unanswered work)", provider.perBook)
	}
	t.Logf("25 works: %d batch calls + %d per work call (was 25 per work calls)", len(provider.batches), provider.perBook)
	for _, target := range targets {
		got, ok := cache.lookup(target.cacheKey())
		if !ok || len(got) != 1 {
			t.Fatalf("%s: not cached", target.cacheKey())
		}
		want := "hc:batch-" + target.ForeignID
		if target.ForeignID == "hc:book-07" {
			want = "hc:per-" + target.ForeignID
		}
		if got[0].ForeignID != want {
			t.Errorf("%s: edition %q, want %q", target.cacheKey(), got[0].ForeignID, want)
		}
	}
}

func TestPrefetchHardcoverEditionsKeepsOtherProvidersPerBook(t *testing.T) {
	provider := &batchingHardcover{}
	h := &AuthorHandler{meta: metadata.NewAggregator(provider)}
	targets := []editionTarget{{ForeignID: "OL1W"}, {ForeignID: "OL2W"}}
	h.prefetchHardcoverEditions(context.Background(), targets, nil)
	if len(provider.batches) != 0 {
		t.Fatalf("non Hardcover ids were batched: %v", provider.batches)
	}
}
