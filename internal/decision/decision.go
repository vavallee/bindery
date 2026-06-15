// Package decision implements a specification-based release evaluation engine.
// Each Specification votes accept/reject on a release candidate; the
// DecisionMaker runs all specs and returns ranked survivors with rejection
// reasons attached to the rejected ones.
package decision

import (
	"time"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// Release is the decision engine's view of an indexer result.
// It intentionally mirrors newznab.SearchResult but keeps the decision
// package free of a direct dependency on the indexer sub-package.
type Release struct {
	GUID        string
	IndexerID   int64
	IndexerName string
	Title       string
	Size        int64 // bytes
	AgeMinutes  int   // minutes since pubDate
	NZBURL      string
	Protocol    string // "usenet" | "torrent"
	Language    string // ISO 639-1 or empty
	Format      string // epub, pdf, m4b, … (parsed from title)
	MediaType   string // "ebook" | "audiobook" | "" — set for dual-format book searches
	CustomScore int    // cumulative custom-format score
}

// Decision is the result of evaluating a single release.
type Decision struct {
	Release   Release
	Approved  bool
	Rejected  bool
	Rejection string // reason if Rejected
}

// Specification is a single evaluation rule.
type Specification interface {
	// IsSatisfiedBy returns (true, "") when the release passes, or
	// (false, reason) when it should be rejected.
	IsSatisfiedBy(r Release, book models.Book) (bool, string)
}

// DecisionMaker evaluates releases against a slice of Specifications.
type DecisionMaker struct {
	specs []Specification
}

// New creates a DecisionMaker with the given specifications.
func New(specs ...Specification) *DecisionMaker {
	return &DecisionMaker{specs: specs}
}

// Evaluate runs every release through all specs. Rejected releases carry the
// rejection reason from the first spec that fired. Survivors are returned in
// the original order; callers may subsequently rank them by CustomScore.
func (d *DecisionMaker) Evaluate(releases []Release, book models.Book) []Decision {
	out := make([]Decision, 0, len(releases))
	for _, r := range releases {
		dec := Decision{Release: r, Approved: true}
		for _, s := range d.specs {
			if ok, reason := s.IsSatisfiedBy(r, book); !ok {
				dec.Approved = false
				dec.Rejected = true
				dec.Rejection = reason
				break
			}
		}
		out = append(out, dec)
	}
	return out
}

// Approved returns only the accepted decisions.
func Approved(decisions []Decision) []Decision {
	out := make([]Decision, 0, len(decisions))
	for _, d := range decisions {
		if d.Approved {
			out = append(out, d)
		}
	}
	return out
}

// ReleaseFromSearchResult converts a newznab.SearchResult to a decision.Release.
// Format is parsed from the release title.
func ReleaseFromSearchResult(sr newznab.SearchResult) Release {
	return Release{
		GUID:        sr.GUID,
		IndexerID:   sr.IndexerID,
		IndexerName: sr.IndexerName,
		Title:       sr.Title,
		Size:        sr.Size,
		AgeMinutes:  PubDateToAge(sr.PubDate),
		NZBURL:      sr.NZBURL,
		Protocol:    sr.Protocol,
		Language:    sr.Language,
		Format:      indexer.ParseRelease(sr.Title).Format,
		MediaType:   sr.MediaType,
	}
}

// PubDateToAge converts a pubDate string (RFC1123Z or RFC822Z) to minutes-old.
// Returns 0 on parse error so callers can treat unknown age as fresh.
func PubDateToAge(pubDate string) int {
	for _, layout := range []string{time.RFC1123Z, time.RFC822Z, time.RFC1123, time.RFC822} {
		if t, err := time.Parse(layout, pubDate); err == nil {
			d := time.Since(t)
			if d < 0 {
				return 0
			}
			return int(d.Minutes())
		}
	}
	return 0
}
