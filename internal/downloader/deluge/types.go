package deluge

// TorrentStatus holds the fields returned by core.get_torrents_status.
type TorrentStatus struct {
	Name         string  `json:"name"`
	Hash         string  `json:"hash"`
	Progress     float64 `json:"progress"`              // 0–100
	State        string  `json:"state"`                 // "Downloading", "Seeding", "Paused", "Error", etc.
	ETA          int64   `json:"eta"`                   // seconds remaining; -1 or 0 if unknown
	DownloadRate int64   `json:"download_payload_rate"` // bytes/s
}
