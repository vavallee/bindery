package filterengine

import (
	"strings"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// IsPartBookTitle reports whether title looks like a box set, omnibus, or
// carton rather than a single book. This IS internal/api/authors.go's
// isPartBookTitle — that function now delegates here rather than the other
// way around, so there is exactly one implementation for both
// PartBookSignal below and authors.go's/catalogue_reconciliation.go's
// pre-existing call sites to share, per #2235's "wrap, don't reimplement"
// rule applied to itself: relocating the canonical body is still one
// implementation, duplicating it would not be.
func IsPartBookTitle(title string) bool {
	return metadata.IsBundleTitle(title)
}

// IsJunkTitle reports whether title is empty or is just the author's own
// name — a recurring OpenLibrary data-quality problem where a Work record
// was never titled and falls back to the author's name. normalizedAuthor
// must already be lowercased/trimmed by the caller (matching Context's own
// NormalizedAuthor convention); title is normalized here the same way
// before comparing.
//
// This is the one shared implementation for JunkTitleSignal.Observe below
// and internal/api's isAuthorWorkMonitorCandidate and
// reconciliationRejectReason, which independently inlined the identical
// check before this consolidation (#2235 PR review) — per this PR's own
// "wrap, don't reimplement" rule applied to itself, the same discipline
// IsPartBookTitle/AnyEditionHasISBN/PassesMinPagesFilter above already
// follow.
func IsJunkTitle(title, normalizedAuthor string) bool {
	normalizedTitle := strings.ToLower(strings.TrimSpace(title))
	return normalizedTitle == "" || normalizedTitle == normalizedAuthor
}

// AnyEditionHasISBN reports whether any edition carries an ISBN-13 or
// ISBN-10. Returns false for a nil or empty slice — a work with no editions
// to check has no ISBN to confirm. Relocated from
// internal/api/authors.go's identically-named, identically-bodied function,
// which now delegates here (see IsPartBookTitle's doc for why).
func AnyEditionHasISBN(editions []models.Edition) bool {
	for _, e := range editions {
		if e.ISBN13 != nil && strings.TrimSpace(*e.ISBN13) != "" {
			return true
		}
		if e.ISBN10 != nil && strings.TrimSpace(*e.ISBN10) != "" {
			return true
		}
	}
	return false
}

// PassesMinPagesFilter reports whether a work satisfies a MinPages floor.
// Mirrors Chaptarr's semantics: an edition meeting the floor passes the
// work, but if no edition reports a page count at all, that's treated as
// unknown rather than zero and passes through unfiltered — a work with no
// page data is not the same as a work with no pages. Relocated from
// internal/api/authors.go's identically-named, identically-bodied function
// (see IsPartBookTitle's doc for why).
func PassesMinPagesFilter(editions []models.Edition, minPages int) bool {
	anyReported := false
	for _, e := range editions {
		if e.NumPages == nil || *e.NumPages <= 0 {
			continue
		}
		anyReported = true
		if *e.NumPages >= minPages {
			return true
		}
	}
	return !anyReported
}
