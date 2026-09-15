package filterengine

import (
	"fmt"

	"github.com/vavallee/bindery/internal/models"
)

// LanguageSignal ports internal/api/authors.go's metadata-profile language
// filter. Wraps models.IsLanguageAllowed verbatim rather than
// reimplementing it — that function's NormalizeLanguageCode folding and its
// len(allowed)==0 / unknown-language escape hatches are exactly what a
// parity test would catch drifting if this signal reimplemented them
// instead of calling through.
type LanguageSignal struct{ weight float64 }

// NewLanguageSignal returns the v1 veto-weight language signal.
func NewLanguageSignal() *LanguageSignal {
	return &LanguageSignal{weight: -vetoWeight}
}

func (s *LanguageSignal) ID() string      { return "language.notAllowed" }
func (s *LanguageSignal) Weight() float64 { return s.weight }

func (s *LanguageSignal) Observe(c Candidate, ctx *Context) []models.FilterObservation {
	if c.Book == nil {
		return nil
	}
	if models.IsLanguageAllowed(c.Book.Language, ctx.AllowedLanguages, ctx.UnknownLangFail) {
		return nil
	}
	return []models.FilterObservation{{
		Signal:     s.ID(),
		Weight:     s.weight,
		Confidence: 1,
		Reason:     fmt.Sprintf("language %q is not in the profile's allowed set", c.Book.Language),
	}}
}
