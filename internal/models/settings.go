package models

import (
	"path/filepath"
	"strings"
	"time"
)

// QualityRank maps file format names to a quality ordering (higher is better).
// It is the fallback ranking: an author whose quality profile lists formats of
// the media type being searched is ranked by that profile's order instead
// (internal/indexer/quality_order.go, #2733). Free text search and authors
// without a profile rank by this map alone.
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
	UpgradeAllowed bool   `json:"upgradeAllowed"`
	Cutoff         string `json:"cutoff"`
	// Items is the profile's format list in preference order, best first
	// within each media type. The media type of an entry is derived from its
	// token (indexer.MediaTypeForFormat), so the one slice holds an ebook list
	// and an audiobook list and only the relative order within each matters;
	// the editor writes ebook entries then audiobook entries, and any
	// interleaving is accepted. A format absent from a list the profile has
	// entries for is not allowed; an empty list means no opinion on that media
	// type. The wire shape is unchanged from before the order was read:
	// [{quality, allowed}], nothing else.
	Items     []QualityItem `json:"items"`
	CreatedAt time.Time     `json:"createdAt"`
	// OwnerUserID is the per-user ownership column added in migration 025.
	// See RootFolder for legacy zero-value semantics.
	OwnerUserID int64 `json:"-"`
}

type QualityItem struct {
	Quality string `json:"quality"`
	Allowed bool   `json:"allowed"`
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
