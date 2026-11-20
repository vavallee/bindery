package soulseek

import (
	"context"
	"errors"
	"strings"
	"sync"
	"unicode"

	"github.com/vavallee/bindery/internal/downloader/slskd"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// searcher is the slice of *slskd.Client the source uses.
type searcher interface {
	Search(ctx context.Context, text string) ([]slskd.SearchResponse, error)
}

// Source is an indexer.Source backed by one slskd client.
type Source struct {
	name   string
	client searcher

	mu       sync.Mutex
	inflight map[string]*flight
}

// flight is one Soulseek search that several callers are waiting on.
type flight struct {
	done chan struct{}
	resp []slskd.SearchResponse
	err  error
	// ownerEnded is true when the search failed because its owner's context
	// ended, so a waiter with a live context should search again.
	ownerEnded bool
}

// NewSource builds the search source for an slskd client. name is shown on the
// results (IndexerName) and in the search debug trail.
func NewSource(name string, client *slskd.Client) *Source {
	return &Source{name: name, client: client}
}

// Name implements indexer.Source.
func (s *Source) Name() string { return s.name }

// Search implements indexer.Source. Privacy: the text built here leaves
// Bindery as a search on the Soulseek network.
func (s *Source) Search(ctx context.Context, q indexer.SourceQuery) ([]newznab.SearchResult, error) {
	text := SearchText(q)
	if text == "" {
		return nil, nil
	}
	resp, err := s.search(ctx, text)
	if err != nil {
		return nil, err
	}
	return Releases(s.name, resp, q.MediaType), nil
}

// search shares one slskd search between concurrent callers with the same
// text. A book wanted as both ebook and audiobook searches once per format at
// the same moment with the same words, and slskd runs one search at a time, so
// without this the second would queue behind the first and miss the search
// budget. The media type is applied afterwards by Releases, per caller.
func (s *Source) search(ctx context.Context, text string) ([]slskd.SearchResponse, error) {
	for {
		s.mu.Lock()
		if f, ok := s.inflight[text]; ok {
			s.mu.Unlock()
			select {
			case <-f.done:
				// The owner gave up because its own context ended, which says
				// nothing about this caller's. Search again, as the new owner.
				if f.ownerEnded && ctx.Err() == nil {
					continue
				}
				return f.resp, f.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		f := &flight{done: make(chan struct{})}
		if s.inflight == nil {
			s.inflight = map[string]*flight{}
		}
		s.inflight[text] = f
		s.mu.Unlock()
		return s.own(ctx, text, f)
	}
}

// own runs the search for a flight. Whatever happens, including a panic in the
// client, the flight leaves the table and its waiters are released, so a later
// search with the same text never blocks on a flight nobody will finish.
func (s *Source) own(ctx context.Context, text string, f *flight) (resp []slskd.SearchResponse, err error) {
	defer func() {
		if r := recover(); r != nil {
			resp, err = nil, errors.New("the Soulseek search failed unexpectedly")
		}
		f.resp, f.err = resp, err
		f.ownerEnded = err != nil && ctx.Err() != nil
		s.mu.Lock()
		delete(s.inflight, text)
		s.mu.Unlock()
		close(f.done)
	}()
	return s.client.Search(ctx, text)
}

// maxTitleWords bounds the title part of a query. Soulseek matches every word,
// so a long title finds nothing.
const maxTitleWords = 6

// SearchText builds the Soulseek query: the free text as typed, or the title
// before any subtitle colon plus the author's last name. Punctuation becomes
// space, because a leading minus excludes a word on Soulseek.
func SearchText(q indexer.SourceQuery) string {
	if t := words(q.Text); len(t) > 0 {
		return strings.Join(t, " ")
	}
	title := q.Title
	if i := strings.Index(title, ":"); i > 0 {
		title = title[:i]
	}
	tw := words(title)
	if len(tw) == 0 {
		return ""
	}
	if len(tw) > maxTitleWords {
		tw = tw[:maxTitleWords]
	}
	if aw := words(q.Author); len(aw) > 0 {
		tw = append(tw, aw[len(aw)-1])
	}
	return strings.Join(tw, " ")
}

func words(s string) []string {
	s = strings.NewReplacer("'", "", "’", "").Replace(s)
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
