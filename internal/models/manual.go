package models

import "strings"

// Books and authors made by hand (#3840) carry a synthetic foreign id under
// ManualForeignIDPrefix, the same idea as a hand-made series'
// "manual:series:" id. No metadata provider owns the prefix, so the aggregator
// routes none of these ids anywhere: a refresh has nothing to fetch for them
// and leaves what the user typed alone.
const (
	ManualForeignIDPrefix       = "manual:"
	ManualBookForeignIDPrefix   = ManualForeignIDPrefix + "book:"
	ManualAuthorForeignIDPrefix = ManualForeignIDPrefix + "author:"
	// ManualMetadataProvider is the metadata_provider value such rows carry.
	ManualMetadataProvider = "manual"
)

// IsManualForeignID reports whether foreignID was made by hand rather than
// taken from a metadata provider.
func IsManualForeignID(foreignID string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(foreignID)), ManualForeignIDPrefix)
}
