package soulseek

import (
	"context"
	"strings"
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
	resp, err := s.client.Search(ctx, text)
	if err != nil {
		return nil, err
	}
	return Releases(s.name, resp, q.MediaType), nil
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
