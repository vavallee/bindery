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
	// UpgradeAllowed turns on the ebook upgrade sweep for books this profile
	// governs (#2420): an imported ebook whose best file ranks below Cutoff is
	// searched again, and a release at or above the cutoff that beats the
	// owned file is grabbed. It is off by default. Migration 122 cleared the
	// column on every existing row, because the column defaulted to on and
	// nothing read it until now, so no install changes behaviour on upgrade.
	// Audiobooks are never upgraded.
	UpgradeAllowed bool `json:"upgradeAllowed"`
	// Cutoff is the ebook format an upgrade aims for. A book that already
	// holds a file at or above it is left alone. It must be a ticked ebook
	// format in Items for an upgrade to run (indexer.EbookUpgradeEligible).
	Cutoff string `json:"cutoff"`
	// RemoveReplaced makes a verified upgrade delete the file it replaced.
	// False, the default, keeps the old file next to the new one. The flag is
	// "remove" rather than "keep" on purpose: a client that does not send the
	// field decodes it as false, and false must be the safe answer (#1357).
	RemoveReplaced bool `json:"removeReplaced"`
	// Items is the profile's format list in preference order, best first
	// within each media type. The media type of an entry is derived from its
	// token (indexer.MediaTypeForFormat), so the one slice holds an ebook list
	// and an audiobook list and only the relative order within each matters;
	// the editor writes ebook entries then audiobook entries, and any
	// interleaving is accepted. A format absent from a list the profile has
	// entries for is not allowed; an empty list means no opinion on that media
	// type. The wire shape is unchanged from before the order was read:
	// [{quality, allowed}], nothing else.
	Items []QualityItem `json:"items"`
	// PreferredNarrators is the audiobook narrator preference, best first, and
	// EditionPreference one of the EditionPref constants. Both empty (the
	// default) leave ranking exactly as it was (#1358).
	PreferredNarrators []string  `json:"preferredNarrators"`
	EditionPreference  string    `json:"editionPreference"`
	CreatedAt          time.Time `json:"createdAt"`
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
	// OwnerUserID is the user the event belongs to (migration 117), 0 for
	// none. HistoryRepo.Create stamps the book's owner when the book has one
	// and falls back to this value, so callers set it to the download's owner
	// or the request identity. It outlives the book, which is what keeps a
	// deleted book's history with its owner.
	OwnerUserID int64 `json:"-"`
}

const (
	HistoryEventGrabbed              = "grabbed"
	HistoryEventImportFailed         = "importFailed"
	HistoryEventBookImported         = "bookImported"
	HistoryEventDownloadFailed       = "downloadFailed"
	HistoryEventBookRenamed          = "bookRenamed"
	HistoryEventDownloadFolderImport = "downloadFolderImported"
	HistoryEventBookFileDeleted      = "bookFileDeleted"
	// HistoryEventImportRejected records a Reject import action (#3250): the
	// imported file was detached, its source release blocklisted and the book
	// returned to Wanted.
	HistoryEventImportRejected   = "importRejected"
	HistoryEventDownloadStalled  = "downloadStalled"
	HistoryEventDownloadRequeued = "downloadRequeued"
	HistoryEventBookRebound      = "bookRebound"
	// HistoryEventBookFileMoved records that Fix match moved a tracked file from
	// one book to another (#2937). BookID is the book that now holds it; Data
	// carries "path", "sourcePath", "fromBookId", "fromTitle" and "message".
	HistoryEventBookFileMoved = "bookFileMoved"
	// HistoryEventBookLanguageCorrected records that an imported EPUB's
	// embedded dc:language disagreed with the language the metadata provider
	// supplied, and the file's value was adopted (#1933). Data carries "from"
	// and "to". Only emitted on a disagreement — filling a language the
	// catalogue never had is not a correction.
	HistoryEventBookLanguageCorrected = "bookLanguageCorrected"
)
