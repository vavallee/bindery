// Package filterengine replaces Bindery's sequential boolean noise-filtering
// (each check dropping a candidate work outright, first one wins, nothing
// downstream visible) with a scored pipeline: signals emit Observations
// instead of a bare bool, observations sum to a score, and the score is
// banded into KEEP / REVIEW / EXCLUDE (#2235).
//
// The package is deliberately provider-agnostic. It does not import
// internal/metadata/openlibrary, internal/metadata/hardcover, or
// internal/api — every fact a signal needs arrives on the Candidate or the
// Context passed to it, built once per author sync by the caller. This is
// what lets a future OpenLibrary-sourced signal, a future Hardcover-sourced
// signal, and today's shipped media-type/junk/language/structural checks
// share one scoring path instead of three call sites with three different
// shapes.
//
// At v1, every shipped signal (internal/api/authors.go's media-type, junk
// title, language, part-book, missing-date, missing-ISBN, and min-pages
// checks) is a veto: it emits nothing when it does not fire, and emits a
// single Weight == -vetoWeight, Confidence == 1 observation when it does.
// Combined with migration 086's keep_threshold = exclude_threshold = 0
// default on every existing metadata profile, this reproduces the pre-#2235
// boolean chain's keep/exclude decision exactly:
//
//	no signal fires  -> score == 0            -> KEEP    (matches: kept before)
//	any signal fires -> score <= -vetoWeight   -> EXCLUDE (matches: dropped before)
//
// The only observable difference is that a record tripping more than one
// filter now carries every reason it failed, not just the first one a
// sequential chain happened to check first. TestRegistryIsVetoOnlyAtV1 pins
// this invariant: registering a graded (non-veto) or keep-direction signal
// without also revisiting the shipped thresholds would silently change
// behavior for every existing profile, so it fails the build instead.
//
// Enabling a graded signal (the natural first candidate is a cluster-level
// edition-count signal — see cluster.go's package doc for the measurement
// behind it) is a deliberate follow-up once the two-band collapse above is
// proven in, not part of this package's v1 surface. The API layer
// (internal/api/metadata_profiles.go) rejects any profile with
// exclude_threshold != keep_threshold for exactly this reason: at v1 the
// REVIEW band has no signal graded enough to populate it meaningfully, and
// no UI surface to show it on.
package filterengine

import "github.com/vavallee/bindery/internal/models"

// vetoWeight is the magnitude every v1 signal uses. Exported as a named
// constant (not inlined as -1000 at each call site) so every ported signal
// visibly shares it, and so TestRegistryIsVetoOnlyAtV1 has one number to
// check every signal against.
const vetoWeight = 1000.0

// Band is a candidate's banded scoring decision.
type Band string

const (
	// BandKeep candidates are treated exactly as an unfiltered work is today.
	BandKeep Band = "keep"
	// BandReview candidates are unreachable at v1 (see package doc): the API
	// layer refuses any profile whose thresholds could produce one.
	BandReview Band = "review"
	// BandExclude candidates are treated exactly as a filtered-out work is
	// today: not created, counted against the matching Skipped* counter.
	BandExclude Band = "exclude"
)

// BandFor bands score against a profile's thresholds. Bands are half-open by
// construction:
//
//	EXCLUDE (-inf, exclude)   REVIEW [exclude, keep)   KEEP [keep, +inf)
//
// The strict "<" on the exclude side is what makes the keep == exclude
// collapse exact: with keep == exclude, [exclude, keep) is empty, so REVIEW
// is unreachable and a score landing exactly on the shared threshold is
// deterministically KEEP — never decided by which branch happens to be
// checked first. A clean record (score == 0 under the v1 veto-only registry)
// lands here, not in the empty REVIEW band, which is why score == 0 must be
// KEEP and not EXCLUDE: exclude_threshold == 0 does not exclude a score OF
// zero, only a score strictly below it.
func BandFor(score, keepThreshold, excludeThreshold float64) Band {
	if score < excludeThreshold {
		return BandExclude
	}
	if score < keepThreshold {
		return BandReview
	}
	return BandKeep
}

// Candidate is one work being scored, plus whatever context signals need
// that isn't already carried on Context. Fields other than Book are nil
// unless the caller has already done the (potentially expensive) work of
// populating them — a signal that needs Cluster or Editions and finds them
// nil should treat that as "not applicable", never as "fails", exactly the
// way internal/api/authors.go's existing edition-gated filters already treat
// a failed edition lookup as "not enforcing for this work" rather than a
// drop (see anyEditionHasISBN's and passesMinPagesFilter's call sites).
type Candidate struct {
	Book *models.Book
	// Cluster is this candidate's grouping (see cluster.go), or nil if the
	// caller did not run clustering for this pass. No v1 signal reads it —
	// it exists so a future cluster-level signal has somewhere to look.
	Cluster *Cluster
	// Editions is populated only by callers that already need edition data
	// for another reason (MinPages, SkipMissingISBN) — see
	// internal/api/authors.go's editionsByForeignID prefetch. nil means "not
	// fetched for this candidate", not "this work has no editions".
	Editions []models.Edition
}

// Context is the profile-derived configuration and run-scoped state every
// signal in one author sync shares. Built once per sync, not per candidate.
type Context struct {
	// Prior is the score every candidate starts from before any observation
	// is summed in. Always 0 at v1 (no signal needs a nonzero baseline yet);
	// carried as a field rather than hardcoded so a future confidence-scored
	// signal set has somewhere to shift the baseline without an engine
	// signature change.
	Prior float64
	// KeepThreshold and ExcludeThreshold come directly from the profile's
	// metadata_profiles.keep_threshold / exclude_threshold columns (migration
	// 086). See BandFor.
	KeepThreshold    float64
	ExcludeThreshold float64

	// AllowedLanguages and UnknownLangFail are the profile's language filter,
	// passed straight to models.IsLanguageAllowed by the language signal
	// rather than reimplemented — see signals_language.go.
	AllowedLanguages []string
	UnknownLangFail  bool

	// SkipPartBooks, SkipMissingDate, SkipMissingISBN, MinPages mirror the
	// metadata profile fields of the same shape that
	// internal/api/authors.go's resolveSkipPartBooks / resolveSkipMissingDate
	// / resolveEditionFilters already resolve. A zero value ("not skipping",
	// "no floor") is the same no-op it is today.
	SkipPartBooks   bool
	SkipMissingDate bool
	SkipMissingISBN bool
	MinPages        int

	// NormalizedAuthor is strings.ToLower(strings.TrimSpace(author.Name)),
	// computed once per sync — the junk-title signal compares a candidate's
	// normalized title against it exactly as authors.go's inline check does
	// today.
	NormalizedAuthor string

	// MediaTypeDefault and StrictMediaType mirror authors.go's strict
	// media-type policy (#1575): when StrictMediaType is true and
	// MediaTypeDefault is a single format (not "both"), a candidate whose
	// MediaType doesn't match is excluded rather than narrowed. Narrowing a
	// "both" candidate to the default happens in the caller before scoring,
	// exactly as it does today — a signal only ever sees the final MediaType
	// a candidate would be created with.
	MediaTypeDefault string
	StrictMediaType  bool
}

// Result is one candidate's scored, banded outcome.
type Result struct {
	Score        float64
	Band         Band
	Observations []models.FilterObservation
}

// Signal is one scoring concept. Signals are pure functions of a Candidate
// and a Context: no I/O, no repository lookups of their own — anything a
// signal needs has already been fetched and placed on one of those two
// arguments by the caller. This is what keeps a signal trivially unit
// testable without a database or an HTTP fixture.
type Signal interface {
	// ID is the signal's stable, namespaced identifier, e.g.
	// "language.notAllowed". Used for duplicate-registration detection and
	// as the Signal field on every FilterObservation this signal emits.
	ID() string
	// Weight is this signal's configured magnitude for the direction it
	// fires in (negative for every v1 signal, all of which are exclude-only
	// vetoes). TestRegistryIsVetoOnlyAtV1 asserts every registered signal's
	// Weight() is exactly -vetoWeight.
	Weight() float64
	// Observe returns zero or more observations for c under ctx. Zero
	// observations means the signal did not fire — most calls, most
	// candidates. A v1 signal never returns more than one.
	Observe(c Candidate, ctx *Context) []models.FilterObservation
}

// Decide runs exactly the given signals against c under ctx and bands the
// result. Signals are not required to be the full registry: several
// authors.go call sites apply one signal at a time, interleaved with
// business logic Decide has no business seeing (existing-book resolution,
// monitoring flags, series pinning) — this lets each of those call sites
// score a single concept without constructing a batch pass around it, while
// still going through the same Contribution/Band math every other signal
// does.
func Decide(c Candidate, ctx *Context, signals ...Signal) Result {
	var obs []models.FilterObservation
	for _, s := range signals {
		obs = append(obs, s.Observe(c, ctx)...)
	}
	score := ctx.Prior
	for _, o := range obs {
		score += o.Contribution()
	}
	return Result{
		Score:        score,
		Band:         BandFor(score, ctx.KeepThreshold, ctx.ExcludeThreshold),
		Observations: obs,
	}
}
