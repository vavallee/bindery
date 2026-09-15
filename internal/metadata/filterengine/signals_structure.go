package filterengine

import "github.com/vavallee/bindery/internal/models"

// PartBookSignal ports internal/api/authors.go's SkipPartBooks metadata-
// profile filter: box-set/omnibus/carton "works" are real OpenLibrary
// records for a bundle of other books, not a book of their own. Wraps
// IsPartBookTitle (this package's relocated copy of authors.go's
// isPartBookTitle, itself a thin wrapper over
// internal/metadata.IsBundleTitle's two-tier detector) rather than
// reimplementing the pattern matching.
type PartBookSignal struct{ weight float64 }

// NewPartBookSignal returns the v1 veto-weight part-book signal.
func NewPartBookSignal() *PartBookSignal {
	return &PartBookSignal{weight: -vetoWeight}
}

func (s *PartBookSignal) ID() string      { return "structure.partBookTitle" }
func (s *PartBookSignal) Weight() float64 { return s.weight }

func (s *PartBookSignal) Observe(c Candidate, ctx *Context) []models.FilterObservation {
	if c.Book == nil || !ctx.SkipPartBooks {
		return nil
	}
	if !IsPartBookTitle(c.Book.Title) {
		return nil
	}
	return []models.FilterObservation{{
		Signal:     s.ID(),
		Weight:     s.weight,
		Confidence: 1,
		Reason:     "title looks like a box set, omnibus, or carton rather than a single book",
	}}
}
