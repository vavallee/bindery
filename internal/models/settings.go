package models

import (
	"path/filepath"
	"strings"
	"time"
)

// QualityRank maps file format names to a quality ordering (higher is better).
var QualityRank = map[string]int{
	"unknown": 0,
	"txt":     1,
	"rtf":     2,
	"pdf":     3,
	"mobi":    4,
	"azw":     4, // older Kindle format (KF7), mobi-equivalent; ParseRelease emits it as a distinct token from azw3
	"epub":    5,
	"azw3":    6,
	"mp3":     7, // audiobook
	"m4a":     8,
	"m4b":     9,
	"flac":    10,
}

// QualityFromFilename infers a quality label from the file extension.
func QualityFromFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	ext = strings.TrimPrefix(ext, ".")
	if _, ok := QualityRank[ext]; ok {
		return ext
	}
	return "unknown"
}

type Setting struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type RootFolder struct {
	ID        int64     `json:"id"`
	Path      string    `json:"path"`
	FreeSpace int64     `json:"freeSpace"`
	CreatedAt time.Time `json:"createdAt"`
	// OwnerUserID is the per-user ownership column added in migration 025.
	// Zero means "no recorded owner" (legacy pre-backfill rows); auth's
	// CheckOwnership treats that as visible to every authenticated caller.
	OwnerUserID int64 `json:"-"`
}

type QualityProfile struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// UpgradeAllowed and Cutoff are kept for wire and schema compatibility
	// only and are consulted by nothing. Their Settings controls were removed
	// in #2373 because no code path has ever read them: there is no upgrade
	// re-grab sweep for a cutoff to gate, so the form was promising behaviour
	// that did not exist. The quality_profiles columns and these JSON fields
	// stay so existing rows still load and a third-party client that sends
	// them still works. Wiring an upgrade path is tracked separately.
	UpgradeAllowed bool          `json:"upgradeAllowed"`
	Cutoff         string        `json:"cutoff"`
	Items          []QualityItem `json:"items"`
	CreatedAt      time.Time     `json:"createdAt"`
	// AudiobookScoring holds optional release-ranking preferences for
	// audiobooks (#2740). A nil pointer — the default for every profile that
	// predates the feature — leaves ranking exactly as it was.
	AudiobookScoring *AudiobookScoring `json:"audiobookScoring,omitempty"`
	// OwnerUserID is the per-user ownership column added in migration 025.
	// See RootFolder for legacy zero-value semantics.
	OwnerUserID int64 `json:"-"`
}

type QualityItem struct {
	Quality string `json:"quality"`
	Allowed bool   `json:"allowed"`
}

// DefaultSizePerMinuteTolerance is the band around a codec's preferred
// MiB/min that counts as a match when AudiobookScoring.ToleranceMiBPerMinute
// is left at zero. Roughly a quarter of a typical 0.8-1.2 MiB/min band.
const DefaultSizePerMinuteTolerance = 0.2

// DefaultGrabsWeight scales the log10(grabs+1) popularity term in release
// ranking. It is the historical hardcoded value, kept as the default so a
// profile that turns on audiobook scoring without touching grabsWeight does
// not silently re-weight popularity.
const DefaultGrabsWeight = 10.0

// AudiobookScoring carries a quality profile's optional size-per-minute
// ranking preferences. It is deliberately not a general scoring framework:
// the only adjustment it can make beyond the historical terms is to charge a
// release for sitting outside the density the user asked for. Ranking of
// codec quality, edition markers and identifiers is untouched.
type AudiobookScoring struct {
	// CodecTargets maps a parsed codec token ("m4b", "m4a", "mp3", "flac",
	// "ogg") to the preferred density in MiB per minute. A codec that is not
	// listed, or a release whose codec could not be parsed, gets no
	// size-per-minute adjustment at all — the term degrades to the historical
	// ranking rather than guessing.
	CodecTargets map[string]float64 `json:"codecTargets,omitempty"`
	// ToleranceMiBPerMinute is the band around the target that scores as a
	// match. Zero means DefaultSizePerMinuteTolerance.
	ToleranceMiBPerMinute float64 `json:"toleranceMiBPerMinute,omitempty"`
	// SizePerMinuteWeight is the score charged per MiB/min of deviation
	// beyond the tolerance. Zero (the default) disables the normalised term
	// entirely and keeps the flat size bonus.
	SizePerMinuteWeight float64 `json:"sizePerMinuteWeight,omitempty"`
	// GrabsWeight scales the log10(grabs+1) popularity term. Nil keeps
	// DefaultGrabsWeight; zero disables the popularity term.
	GrabsWeight *float64 `json:"grabsWeight,omitempty"`
}

type HistoryEvent struct {
	ID          int64     `json:"id"`
	BookID      *int64    `json:"bookId"`
	EventType   string    `json:"eventType"`
	SourceTitle string    `json:"sourceTitle"`
	Data        string    `json:"data"`
	CreatedAt   time.Time `json:"createdAt"`
}

const (
	HistoryEventGrabbed              = "grabbed"
	HistoryEventImportFailed         = "importFailed"
	HistoryEventBookImported         = "bookImported"
	HistoryEventDownloadFailed       = "downloadFailed"
	HistoryEventBookRenamed          = "bookRenamed"
	HistoryEventDownloadFolderImport = "downloadFolderImported"
	HistoryEventBookFileDeleted      = "bookFileDeleted"
	HistoryEventDownloadStalled      = "downloadStalled"
	HistoryEventDownloadRequeued     = "downloadRequeued"
	HistoryEventBookRebound          = "bookRebound"
	// HistoryEventBookLanguageCorrected records that an imported EPUB's
	// embedded dc:language disagreed with the language the metadata provider
	// supplied, and the file's value was adopted (#1933). Data carries "from"
	// and "to". Only emitted on a disagreement — filling a language the
	// catalogue never had is not a correction.
	HistoryEventBookLanguageCorrected = "bookLanguageCorrected"
)
