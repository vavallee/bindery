package metadata

import (
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

func (a *Aggregator) providerForForeignID(foreignID string) Provider {
	if a == nil {
		return nil
	}
	want := providerNameForForeignID(foreignID)
	if want == "" {
		return a.primary
	}
	for _, provider := range a.providers() {
		if provider == nil {
			continue
		}
		if normalizedProviderName(provider.Name()) == want {
			return provider
		}
	}
	if want == "openlibrary" || want == normalizedProviderName(providerName(a.primary)) {
		return a.primary
	}
	return nil
}

func providerName(provider Provider) string {
	if provider == nil {
		return ""
	}
	return provider.Name()
}

func sameProvider(a, b Provider) bool {
	return normalizedProviderName(providerName(a)) == normalizedProviderName(providerName(b))
}

// providerNameForForeignID classifies a foreign ID by its prefix. It delegates
// to models rather than repeating the switch: this copy had drifted to four
// branches and was missing "calibre:" and "abs:", so an importer's synthetic ID
// fell through to the openlibrary default and providerForForeignID handed it to
// the OpenLibrary client, which 404s on it (#2352).
//
// It routes author and book IDs both, which is fine: AuthorProviderFromForeignID
// and BookProviderFromForeignID agree branch for branch, and the prefixes are a
// single shared namespace precisely so one classifier can answer for either.
func providerNameForForeignID(foreignID string) string {
	return models.AuthorProviderFromForeignID(foreignID)
}

func normalizedProviderName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ol", "openlibrary", "open_library":
		return "openlibrary"
	case "gb", "googlebooks", "google_books":
		return "googlebooks"
	case "hc", "hardcover":
		return "hardcover"
	case "dnb":
		return "dnb"
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

func (a *Aggregator) providers() []Provider {
	if a == nil {
		return nil
	}
	providers := make([]Provider, 0, len(a.enrichers)+1)
	if a.primary != nil {
		providers = append(providers, a.primary)
	}
	providers = append(providers, a.enrichers...)
	return providers
}

// PrimaryProviderName returns the normalized name of the provider wired as
// primary ("openlibrary", "hardcover", ...), or "" when there is none. The
// primary is fixed at boot (cmd/bindery), so this names the provider that
// serves catalogue syncs for records carrying no other provider's prefix.
// Exported so the API layer can tell when a record being linked will sync
// from somewhere other than the configured primary (#2237).
func (a *Aggregator) PrimaryProviderName() string {
	if a == nil {
		return ""
	}
	return normalizedProviderName(providerName(a.primary))
}

// SearchOutcome reports which providers took part in a fan-out search and
// which dropped out. It exists because the two ways a search can come back
// without the primary provider's records look identical to a caller and are
// not the same thing at all (#2271):
//
//   - the primary answered and has no such record: a fact, and acting on the
//     fallback's answer is correct;
//   - the primary was rate limited, timed out or errored: a fact about the
//     last few seconds only, and acting on the fallback's answer writes a
//     permanent provider link on the strength of a transient failure.
//
// A Hardcover free-tier token makes the second case routine rather than rare:
// one 72-item Audiobookshelf import produced 86 rate-limit rejections, and 38
// of 42 authors were left linked to OpenLibrary with the primary set to
// Hardcover and nothing reported.
type SearchOutcome struct {
	// Primary is the normalized name of the configured primary provider, or
	// "" when there is none.
	Primary string
	// FailedProviders names the providers that errored or timed out.
	// A provider skipped for missing credentials is NOT a failure and does
	// not appear here.
	FailedProviders []string
	// Answered names the providers that returned without error, match or
	// not, in provider order. A provider skipped for missing credentials
	// neither failed nor answered. An importer's "no match" message names
	// these rather than assuming which providers were asked (#2332).
	Answered []string
	// PrimaryFailed is true when Primary is one of FailedProviders.
	PrimaryFailed bool
	// FirstErr is the first provider failure, kept for logging. Results may
	// still be usable: a fan-out error is only returned to the caller when
	// every provider failed.
	FirstErr error
}

// newSearchOutcome builds the outcome of a lookup that lost the providers
// named in failed and heard from those in answered, flagging PrimaryFailed
// when the primary is among the failures.
func newSearchOutcome(primary string, failed, answered []string, firstErr error) SearchOutcome {
	outcome := SearchOutcome{Primary: primary, FailedProviders: failed, Answered: answered, FirstErr: firstErr}
	for _, name := range failed {
		if name == primary {
			outcome.PrimaryFailed = true
			break
		}
	}
	return outcome
}

// SafeToBind reports whether a match may be written as an author's permanent
// provider link. It refuses exactly one case: the primary provider failed and
// the match did not come from it, so a fallback won by default rather than on
// the merits.
//
// An empty foreign ID counts as not the primary's. It names no provider at
// all, but the classifier files it under openlibrary, so on an OpenLibrary
// primary it used to pass. Google Books author results carry no ID and win
// same name ties against the enrichers registered after it, which made that
// a live bypass (#2332).
//
// It deliberately does NOT refuse when the primary simply returned nothing.
// That is #2237's case, where the record really is absent from the primary's
// search and the operator is better served by the author existing, with the
// mismatch surfaced, than by the import stalling.
func (o SearchOutcome) SafeToBind(foreignID string) bool {
	if !o.PrimaryFailed || o.Primary == "" {
		return true
	}
	if strings.TrimSpace(foreignID) == "" {
		return false
	}
	return providerNameForForeignID(foreignID) == o.Primary
}

// FailureSummary renders the failed providers for a log line or a user-facing
// import message. Empty when nothing failed.
func (o SearchOutcome) FailureSummary() string {
	return strings.Join(o.FailedProviders, ", ")
}

// AnsweredSummary renders the providers that answered, for a user facing
// "no match" message. Empty when none did.
func (o SearchOutcome) AnsweredSummary() string {
	return strings.Join(o.Answered, ", ")
}
