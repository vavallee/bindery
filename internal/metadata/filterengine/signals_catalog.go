package filterengine

import "github.com/vavallee/bindery/internal/models"

// MissingDateSignal ports internal/api/authors.go's SkipMissingDate
// metadata-profile filter: ReleaseDate is already merged in from the
// provider's work data by the time a candidate reaches this signal, so this
// is a straight presence check, not a fetch.
type MissingDateSignal struct{ weight float64 }

// NewMissingDateSignal returns the v1 veto-weight missing-release-date
// signal.
func NewMissingDateSignal() *MissingDateSignal {
	return &MissingDateSignal{weight: -vetoWeight}
}

func (s *MissingDateSignal) ID() string      { return "catalog.missingReleaseDate" }
func (s *MissingDateSignal) Weight() float64 { return s.weight }

func (s *MissingDateSignal) Observe(c Candidate, ctx *Context) []models.FilterObservation {
	if c.Book == nil || !ctx.SkipMissingDate || c.Book.ReleaseDate != nil {
		return nil
	}
	return []models.FilterObservation{{
		Signal:     s.ID(),
		Weight:     s.weight,
		Confidence: 1,
		Reason:     "work has no release date",
	}}
}

// MissingISBNSignal ports internal/api/authors.go's SkipMissingISBN
// metadata-profile filter. Wraps AnyEditionHasISBN verbatim. Callers must
// only invoke this signal when an edition lookup for the candidate actually
// succeeded (c.Editions set, possibly to an empty-but-non-nil slice) —
// exactly the internal/api/authors.go call-site gating that already exists
// today (editionsByForeignID[b.ForeignID], comma-ok). A lookup that failed
// (network error, provider outage) must never reach this signal at all: the
// pre-#2235 behavior for a failed lookup is "not enforcing for this work",
// not "treat as zero editions and exclude it".
type MissingISBNSignal struct{ weight float64 }

// NewMissingISBNSignal returns the v1 veto-weight missing-ISBN signal.
func NewMissingISBNSignal() *MissingISBNSignal {
	return &MissingISBNSignal{weight: -vetoWeight}
}

func (s *MissingISBNSignal) ID() string      { return "catalog.missingISBN" }
func (s *MissingISBNSignal) Weight() float64 { return s.weight }

func (s *MissingISBNSignal) Observe(c Candidate, ctx *Context) []models.FilterObservation {
	if c.Book == nil || !ctx.SkipMissingISBN {
		return nil
	}
	if AnyEditionHasISBN(c.Editions) {
		return nil
	}
	return []models.FilterObservation{{
		Signal:     s.ID(),
		Weight:     s.weight,
		Confidence: 1,
		Reason:     "no edition carries an ISBN-13 or ISBN-10",
	}}
}

// MinPagesSignal ports internal/api/authors.go's MinPages metadata-profile
// floor. Wraps PassesMinPagesFilter verbatim, including its "unknown page
// count passes through unfiltered" semantics — see that function's doc for
// why that asymmetry with MissingISBNSignal is deliberate, not an oversight.
// Same call-site gating requirement as MissingISBNSignal: only invoke this
// signal when the candidate's edition lookup actually succeeded.
type MinPagesSignal struct{ weight float64 }

// NewMinPagesSignal returns the v1 veto-weight minimum-page-count signal.
func NewMinPagesSignal() *MinPagesSignal {
	return &MinPagesSignal{weight: -vetoWeight}
}

func (s *MinPagesSignal) ID() string      { return "catalog.belowMinPages" }
func (s *MinPagesSignal) Weight() float64 { return s.weight }

func (s *MinPagesSignal) Observe(c Candidate, ctx *Context) []models.FilterObservation {
	if c.Book == nil || ctx.MinPages <= 0 {
		return nil
	}
	if PassesMinPagesFilter(c.Editions, ctx.MinPages) {
		return nil
	}
	return []models.FilterObservation{{
		Signal:     s.ID(),
		Weight:     s.weight,
		Confidence: 1,
		Reason:     "no edition meets the minimum page count",
	}}
}
