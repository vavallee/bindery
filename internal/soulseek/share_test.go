package soulseek

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/downloader/slskd"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

type slowSearcher struct{ calls atomic.Int32 }

func (s *slowSearcher) Search(_ context.Context, _ string) ([]slskd.SearchResponse, error) {
	s.calls.Add(1)
	time.Sleep(50 * time.Millisecond)
	return []slskd.SearchResponse{{Username: "u", Files: []slskd.SearchFile{
		file(`d\Dune.epub`, 10), file(`e\Dune\01.m4b`, 10),
	}}}, nil
}

// The two legs of a dual format book search at the same moment with the same
// words. One slskd search serves both, each leg keeping its own media type.
func TestConcurrentSameTextSearchesShareOneSlskdSearch(t *testing.T) {
	f := &slowSearcher{}
	s := &Source{name: "S", client: f}
	var wg sync.WaitGroup
	got := map[string]string{}
	var mu sync.Mutex
	for _, mt := range []string{models.MediaTypeEbook, models.MediaTypeAudiobook} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Search(context.Background(), indexer.SourceQuery{Title: "Dune", Author: "Frank Herbert", MediaType: mt})
			if err != nil || len(r) != 1 {
				t.Errorf("%s: %v %v", mt, r, err)
				return
			}
			mu.Lock()
			got[mt] = r[0].Soulseek.Format
			mu.Unlock()
		}()
	}
	wg.Wait()
	if f.calls.Load() != 1 {
		t.Errorf("slskd searched %d times, want 1", f.calls.Load())
	}
	if got[models.MediaTypeEbook] != "epub" || got[models.MediaTypeAudiobook] != "m4b" {
		t.Errorf("per leg results = %v", got)
	}
}
