package indexer

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

type stubSource struct {
	name  string
	calls atomic.Int32
	last  SourceQuery
	out   []newznab.SearchResult
	err   error
}

func (s *stubSource) Name() string { return s.name }
func (s *stubSource) Search(_ context.Context, q SourceQuery) ([]newznab.SearchResult, error) {
	s.calls.Add(1)
	s.last = q
	return s.out, s.err
}

func soulseekHit(guid, title string) newznab.SearchResult {
	bogus := int64(99) // a source must not be able to smuggle an indexer id in
	return newznab.SearchResult{
		GUID: guid, Title: title, Size: 5 << 20, Protocol: "soulseek",
		IndexerID: &bogus,
		Soulseek:  &newznab.SoulseekInfo{Username: "peer", FreeSlot: true, Format: "epub", FileCount: 1},
	}
}

func TestSearchBookAsksSourcesOnlyWhenOptedIn(t *testing.T) {
	src := &stubSource{name: "Soulseek", out: []newznab.SearchResult{soulseekHit("slskd:1", "Frank Herbert - Dune [EPUB]")}}
	s := NewSearcher()
	c := MatchCriteria{Title: "Dune", Author: "Frank Herbert", MediaType: models.MediaTypeEbook}

	// The scheduler's call has no sources in its context: nothing leaves.
	if got := s.SearchBook(context.Background(), nil, c); len(got) != 0 {
		t.Fatalf("results without opt in: %+v", got)
	}
	if src.calls.Load() != 0 {
		t.Fatal("a source was asked without WithSources")
	}

	got := s.SearchBook(WithSources(context.Background(), src), nil, c)
	if len(got) != 1 {
		t.Fatalf("got %d results, want the Soulseek one: %+v", len(got), got)
	}
	if got[0].IndexerID != nil {
		t.Errorf("IndexerID = %d, want nil", *got[0].IndexerID)
	}
	if got[0].IndexerName != "Soulseek" || got[0].Protocol != "soulseek" || got[0].Soulseek == nil {
		t.Errorf("result = %+v", got[0])
	}
	if src.last.Title != "Dune" || src.last.Author != "Frank Herbert" || src.last.MediaType != models.MediaTypeEbook {
		t.Errorf("query = %+v", src.last)
	}
}

func TestSearchBookWithDebugRecordsSources(t *testing.T) {
	ok := &stubSource{name: "Soulseek", out: []newznab.SearchResult{soulseekHit("slskd:1", "Frank Herbert - Dune [EPUB]")}}
	bad := &stubSource{name: "Second", err: errors.New("slskd is busy")}
	s := NewSearcher()
	c := MatchCriteria{Title: "Dune", Author: "Frank Herbert", MediaType: models.MediaTypeEbook}
	got, dbg := s.SearchBookWithDebug(WithSources(context.Background(), ok, bad), nil, c)
	if len(got) != 1 || got[0].IndexerID != nil {
		t.Fatalf("results = %+v", got)
	}
	if len(dbg.Sources) != 2 {
		t.Fatalf("sources = %+v", dbg.Sources)
	}
	if dbg.Sources[0].Name != "Soulseek" || dbg.Sources[0].ResultCount != 1 || dbg.Sources[0].Error != "" {
		t.Errorf("ok row = %+v", dbg.Sources[0])
	}
	if dbg.Sources[1].Error == "" || dbg.Sources[1].ResultCount != 0 {
		t.Errorf("failed row = %+v", dbg.Sources[1])
	}

	// No opt in: no rows at all, so the panel and JSON are unchanged.
	_, plain := s.SearchBookWithDebug(context.Background(), nil, c)
	if plain.Sources != nil {
		t.Errorf("sources recorded without opt in: %+v", plain.Sources)
	}
}

func TestSearchQueryAsksSources(t *testing.T) {
	src := &stubSource{name: "Soulseek", out: []newznab.SearchResult{soulseekHit("slskd:2", "Dune [EPUB]")}}
	s := NewSearcher()
	got := s.SearchQuery(WithSources(context.Background(), src), nil, "dune")
	if len(got) != 1 || got[0].IndexerID != nil {
		t.Fatalf("results = %+v", got)
	}
	if src.last.Text != "dune" || src.last.Title != "" {
		t.Errorf("query = %+v", src.last)
	}
	if n := len(s.SearchQuery(context.Background(), nil, "dune")); n != 0 {
		t.Errorf("free text search without opt in returned %d results", n)
	}
}

func TestWithSourcesNoneIsIdentity(t *testing.T) {
	ctx := context.Background()
	if WithSources(ctx) != ctx {
		t.Error("WithSources with no sources changed the context")
	}
}
