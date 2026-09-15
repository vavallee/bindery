package filterengine

import "github.com/vavallee/bindery/internal/models"

// JunkTitleSignal ports internal/api/authors.go's junk-title filter: an
// OpenLibrary "work" whose title is empty or is just the author's name is a
// recurring OL data-quality problem (the Work record was never titled and
// falls back to the author's name), and letting it through pollutes the
// Wanted page and produces nonsense destination folders. Wraps IsJunkTitle
// (predicates.go) rather than inlining the check itself.
type JunkTitleSignal struct{ weight float64 }

// NewJunkTitleSignal returns the v1 veto-weight junk-title signal.
func NewJunkTitleSignal() *JunkTitleSignal {
	return &JunkTitleSignal{weight: -vetoWeight}
}

func (s *JunkTitleSignal) ID() string      { return "junk.titleEmptyOrAuthorName" }
func (s *JunkTitleSignal) Weight() float64 { return s.weight }

func (s *JunkTitleSignal) Observe(c Candidate, ctx *Context) []models.FilterObservation {
	if c.Book == nil || !IsJunkTitle(c.Book.Title, ctx.NormalizedAuthor) {
		return nil
	}
	return []models.FilterObservation{{
		Signal:     s.ID(),
		Weight:     s.weight,
		Confidence: 1,
		Reason:     "title is empty or matches the author's own name",
	}}
}
