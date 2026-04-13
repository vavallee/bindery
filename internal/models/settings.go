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
}

type QualityProfile struct {
	ID             int64         `json:"id"`
	Name           string        `json:"name"`
	UpgradeAllowed bool          `json:"upgradeAllowed"`
	Cutoff         string        `json:"cutoff"`
	Items          []QualityItem `json:"items"`
	CreatedAt      time.Time     `json:"createdAt"`
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
)
