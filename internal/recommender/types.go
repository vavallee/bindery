// Package recommender implements Bindery's content-based book recommendation
// engine. It analyses the user's library to build a taste profile, generates
// candidates from series gaps, monitored-author works, and genre similarity,
// scores them, and persists the top results for the Discover page.
package recommender

// SeriesState tracks a user's progress through a series.
type SeriesState struct {
	SeriesID         int64
	SeriesTitle      string
	MaxPosition      float64
	MissingPositions []float64
}

// UserProfile captures the user's reading taste, built from their library.
type UserProfile struct {
	GenreWeights        map[string]float64
	MonitoredAuthors    map[int64]bool
	AuthorBookCounts    map[int64]int
	SeriesState         map[int64]SeriesState
	OwnedForeignIDs     map[string]bool
	DismissedForeignIDs map[string]bool
	ExcludedAuthors     map[string]bool
	PreferredLanguage   string
	TotalBooks          int
	LibraryMedianYear   int
}
