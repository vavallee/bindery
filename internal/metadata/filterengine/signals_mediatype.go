package filterengine

import (
	"fmt"

	"github.com/vavallee/bindery/internal/models"
)

// MediaTypeSignal ports internal/api/authors.go's strict media-type policy
// (#1575): when the profile's default media type is a single format
// (ebook or audiobook, not "both") and strict mode is on, a candidate whose
// final MediaType isn't that format is excluded — it has nothing the user
// asked for and would otherwise be created as an un-grabbable row.
//
// Narrowing a "both" candidate DOWN to the default happens in the caller
// before scoring, exactly as it does in the pre-#2235 loop: this signal only
// ever sees the MediaType a candidate would actually be created with, so it
// only fires for a work that is ONLY the other format.
type MediaTypeSignal struct{ weight float64 }

// NewMediaTypeSignal returns the v1 veto-weight media-type signal.
func NewMediaTypeSignal() *MediaTypeSignal {
	return &MediaTypeSignal{weight: -vetoWeight}
}

func (s *MediaTypeSignal) ID() string      { return "mediatype.strictMismatch" }
func (s *MediaTypeSignal) Weight() float64 { return s.weight }

func (s *MediaTypeSignal) Observe(c Candidate, ctx *Context) []models.FilterObservation {
	if c.Book == nil || !ctx.StrictMediaType {
		return nil
	}
	switch ctx.MediaTypeDefault {
	case models.MediaTypeEbook, models.MediaTypeAudiobook:
	default:
		// A "both" default disables the clamp entirely — the user wants
		// everything.
		return nil
	}
	if c.Book.MediaType == ctx.MediaTypeDefault {
		return nil
	}
	return []models.FilterObservation{{
		Signal:     s.ID(),
		Weight:     s.weight,
		Confidence: 1,
		Reason:     fmt.Sprintf("media type %q doesn't match the strict default %q", c.Book.MediaType, ctx.MediaTypeDefault),
	}}
}
