package qbittorrent

import (
	"encoding/json"
	"strings"
)

// Torrent represents a single torrent as returned by the qBittorrent WebUI API.
type Torrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	Size        int64   `json:"size"`
	AmountLeft  int64   `json:"amount_left"`
	Progress    float64 `json:"progress"`
	State       string  `json:"state"`
	Category    string  `json:"category"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
	ETA         int     `json:"eta"`
	AddedOn     int64   `json:"added_on"`
	DLSpeed     int64   `json:"dlspeed"`
}

// normalizePath rewrites Windows-style backslashes to forward slashes so
// downstream Linux path code (filepath.Walk, pathmap.Apply, pathIsAtOrUnder)
// can process paths reported by a qBittorrent instance running on Windows.
// A path with no backslashes is returned unchanged. The only false positive
// would be a literal "\" in a Linux save path or torrent name — qBittorrent
// running on Linux does not produce such paths (it uses forward slashes), so
// the conversion is unambiguous in practice.
func normalizePath(p string) string {
	if !strings.Contains(p, `\`) {
		return p
	}
	return strings.ReplaceAll(p, `\`, "/")
}

// Category represents a qBittorrent category. Different qBittorrent versions
// have used different JSON keys for the category save path, so UnmarshalJSON
// accepts all observed variants and normalizes them to SavePath.
type Category struct {
	Name     string `json:"name"`
	SavePath string `json:"savePath"`
}

func (c *Category) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name          string          `json:"name"`
		SavePath      json.RawMessage `json:"savePath"`
		SavePathSnake json.RawMessage `json:"save_path"`
		DownloadPath  json.RawMessage `json:"download_path"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Name = raw.Name
	c.SavePath = categoryPathString(raw.SavePath)
	if c.SavePath == "" {
		c.SavePath = categoryPathString(raw.SavePathSnake)
	}
	if c.SavePath == "" {
		c.SavePath = categoryPathString(raw.DownloadPath)
	}
	// Windows-qBit reports save paths with backslashes; downstream Linux path
	// code can't process those. Convert at the boundary so consumers see only
	// forward-slash paths (PixieApples #800 follow-up: import-side failures
	// after the health check passed).
	c.SavePath = normalizePath(c.SavePath)
	return nil
}

// File is a single file belonging to a torrent, as returned by
// /api/v2/torrents/files?hash=<hash>. Name is the path relative to the
// torrent's save path (forward-slash normalised); for a single-file
// torrent it is just the file's basename.
type File struct {
	Name string
	Size int64
}

// rpcFile mirrors the qBittorrent v2 API shape for a torrent file entry.
// Only name + size are needed by the importer; progress, priority, and the
// rest are intentionally discarded.
type rpcFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func categoryPathString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}
