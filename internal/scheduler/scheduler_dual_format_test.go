package scheduler

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// stubSearcher records how many times SearchBook is called and with which
// media types, without touching any network.
type stubSearcher struct {
	calls      atomic.Int32
	mediaTypes []string
}

func (s *stubSearcher) SearchBook(_ context.Context, _ []models.Indexer, c indexer.MatchCriteria) []newznab.SearchResult {
	s.calls.Add(1)
	s.mediaTypes = append(s.mediaTypes, c.MediaType)
	return nil // no results → grab nothing, but the search was counted
}

func (s *stubSearcher) SearchAndGrabBook(_ context.Context, _ models.Book) {}

// TestSearchAndGrabBook_EbookOnly verifies that a single-format ebook book
// fires exactly one search with mediaType='ebook'.
func TestSearchAndGrabBook_EbookOnly(t *testing.T) {
	ss := &stubSearcher{}
	sched := &Scheduler{
		searcher:  ss,
		indexers:  nil, // stubSearcher ignores this
		downloads: nil,
		clients:   nil,
		settings:  nil,
		blocklist: nil,
		authors:   nil,
	}

	book := models.Book{
		Title:     "Ender's Game",
		MediaType: models.MediaTypeEbook,
		// EbookFilePath is empty → NeedsEbook() = true
	}

	sched.SearchAndGrabBook(context.Background(), book)

	if n := int(ss.calls.Load()); n != 1 {
		t.Errorf("expected 1 search call for ebook-only book, got %d", n)
	}
	if len(ss.mediaTypes) > 0 && ss.mediaTypes[0] != models.MediaTypeEbook {
		t.Errorf("expected mediaType=%q, got %q", models.MediaTypeEbook, ss.mediaTypes[0])
	}
}

// TestSearchAndGrabBook_AudiobookOnly verifies a single-format audiobook.
func TestSearchAndGrabBook_AudiobookOnly(t *testing.T) {
	ss := &stubSearcher{}
	sched := &Scheduler{searcher: ss}

	book := models.Book{
		Title:     "Project Hail Mary",
		MediaType: models.MediaTypeAudiobook,
		// AudiobookFilePath empty → NeedsAudiobook() = true
	}

	sched.SearchAndGrabBook(context.Background(), book)

	if n := int(ss.calls.Load()); n != 1 {
		t.Errorf("expected 1 search call for audiobook-only book, got %d", n)
	}
	if len(ss.mediaTypes) > 0 && ss.mediaTypes[0] != models.MediaTypeAudiobook {
		t.Errorf("expected mediaType=%q, got %q", models.MediaTypeAudiobook, ss.mediaTypes[0])
	}
}

// TestSearchAndGrabBook_Both_BothMissing verifies that a 'both' book with
// neither format on disk fires two independent searches.
func TestSearchAndGrabBook_Both_BothMissing(t *testing.T) {
	ss := &stubSearcher{}
	sched := &Scheduler{searcher: ss}

	book := models.Book{
		Title:     "The Martian",
		MediaType: models.MediaTypeBoth,
		// Both EbookFilePath and AudiobookFilePath are empty.
	}

	sched.SearchAndGrabBook(context.Background(), book)

	if n := int(ss.calls.Load()); n != 2 {
		t.Errorf("expected 2 search calls for 'both' book, got %d", n)
	}
	sawEbook, sawAudiobook := false, false
	for _, mt := range ss.mediaTypes {
		switch mt {
		case models.MediaTypeEbook:
			sawEbook = true
		case models.MediaTypeAudiobook:
			sawAudiobook = true
		}
	}
	if !sawEbook {
		t.Error("expected a search with mediaType='ebook'")
	}
	if !sawAudiobook {
		t.Error("expected a search with mediaType='audiobook'")
	}
}

// TestSearchAndGrabBook_Both_EbookAlreadyImported verifies that when one
// format is already on disk, only the missing format is searched.
func TestSearchAndGrabBook_Both_EbookAlreadyImported(t *testing.T) {
	ss := &stubSearcher{}
	sched := &Scheduler{searcher: ss}

	book := models.Book{
		Title:             "Dune",
		MediaType:         models.MediaTypeBoth,
		EbookFilePath:     "/lib/dune.epub", // already on disk
		AudiobookFilePath: "",               // still needed
	}

	sched.SearchAndGrabBook(context.Background(), book)

	if n := int(ss.calls.Load()); n != 1 {
		t.Errorf("expected 1 search call (audiobook only), got %d", n)
	}
	if len(ss.mediaTypes) > 0 && ss.mediaTypes[0] != models.MediaTypeAudiobook {
		t.Errorf("expected mediaType=%q for remaining search, got %q",
			models.MediaTypeAudiobook, ss.mediaTypes[0])
	}
}

// TestSearchAndGrabBook_Both_FullySatisfied verifies that no searches are
// fired when both formats are already on disk.
func TestSearchAndGrabBook_Both_FullySatisfied(t *testing.T) {
	ss := &stubSearcher{}
	sched := &Scheduler{searcher: ss}

	book := models.Book{
		Title:             "Foundation",
		MediaType:         models.MediaTypeBoth,
		EbookFilePath:     "/lib/foundation.epub",
		AudiobookFilePath: "/ab/foundation",
	}

	sched.SearchAndGrabBook(context.Background(), book)

	if n := int(ss.calls.Load()); n != 0 {
		t.Errorf("expected 0 search calls when fully satisfied, got %d", n)
	}
}
