package filterengine

import "github.com/vavallee/bindery/internal/models"

// ProviderNoiseSignal replays a provider-emitted
// models.SignalProviderOpenLibraryNoise claim (on Book.Observations) at this
// registry's configured weight, rather than trusting whatever weight the
// provider itself would have to guess at.
//
// This is the seam #2235 was built around: a provider client has no
// metadata-profile or threshold context (internal/metadata/openlibrary
// doesn't import internal/api or filterengine), so it cannot itself decide
// KEEP vs EXCLUDE — it can only flag "this looks like companion material"
// and hand the claim to whatever engine later has the actual configuration.
// Before #2235, OpenLibrary's shouldFilterOLNoise decided unilaterally and
// dropped the work before it ever reached AuthorHandler.fetchAuthorBooks, so
// it never entered AuthorSyncSummary.Total's accounting at all. Replaying it
// here means it now goes through the same scoring path as every other
// signal, and (new behavior, expected) it now counts.
type ProviderNoiseSignal struct{ weight float64 }

// NewProviderNoiseSignal returns the v1 veto-weight provider-noise replay
// signal.
func NewProviderNoiseSignal() *ProviderNoiseSignal {
	return &ProviderNoiseSignal{weight: -vetoWeight}
}

// ID intentionally matches the pre-#2235 counter semantics: a provider-flagged
// noise candidate is routed to the same SkippedJunk bucket a junk title
// already used, per the nearest-existing-semantic call made when #2235 was
// scoped — there was never a separate "provider noise" counter, and adding a
// tenth one for this alone isn't warranted.
func (s *ProviderNoiseSignal) ID() string      { return "junk.providerFlaggedNoise" }
func (s *ProviderNoiseSignal) Weight() float64 { return s.weight }

func (s *ProviderNoiseSignal) Observe(c Candidate, ctx *Context) []models.FilterObservation {
	if c.Book == nil {
		return nil
	}
	o, ok := models.FindObservation(c.Book.Observations, models.SignalProviderOpenLibraryNoise)
	if !ok {
		return nil
	}
	return []models.FilterObservation{{
		Signal:     s.ID(),
		Weight:     s.weight,
		Confidence: 1,
		Reason:     o.Reason,
	}}
}
