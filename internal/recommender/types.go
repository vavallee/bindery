// Package recommender implements Bindery's content-based book recommendation
// engine. It analyses the user's library to build a taste profile, generates
// candidates from series gaps, monitored-author works, and genre similarity,
// scores them, and persists the top results for the Discover page.
package recommender

import "github.com/vavallee/bindery/internal/models"

// SeriesState tracks a user's progress through a series.
type SeriesState struct {
	SeriesID         int64
	SeriesTitle      string
	MaxPosition      float64
	MissingPositions []float64
}

// UserProfile captures the user's reading taste, built from their library.
type UserProfile struct {
	GenreWeights     map[string]float64
	MonitoredAuthors map[int64]bool
	AuthorBookCounts map[int64]int
	SeriesState      map[int64]SeriesState
	OwnedForeignIDs  map[string]bool
	// OwnedWorkKeys keys ownership by work identity — indexer.CanonicalDedupKey
	// of the title crossed with a normalized author-name variant (see
	// ownedWorkKeys). ForeignIDs are provider-specific (hc:… vs OL… vs
	// audible:…), so OwnedForeignIDs alone never matches the same work owned
	// via a different metadata provider (#1726).
	OwnedWorkKeys map[string]bool
	// AuthorNames maps local author ID to display name so candidate
	// generators can pass an author name to IsOwned for books that only carry
	// an AuthorID. Populated by BuildProfile from the authors it already loads.
	AuthorNames         map[int64]string
	DismissedForeignIDs map[string]bool
	ExcludedAuthors     map[string]bool
	PreferredLanguage   string
	TotalBooks          int
	LibraryMedianYear   int
	// Scope limits the profile and every candidate to one user's library:
	// books and authors that user owns, plus unowned (shared) ones. 0 means
	// the whole instance, which is what an admin, or anyone with tenancy off,
	// gets. See Engine.libraryScope.
	Scope int64
}

// visible reports whether a book may feed this profile or be recommended
// from it.
func (p *UserProfile) visible(b *models.Book) bool {
	return b != nil && (p.Scope == 0 || b.OwnerUserID == 0 || b.OwnerUserID == p.Scope)
}
