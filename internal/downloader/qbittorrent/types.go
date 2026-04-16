package qbittorrent

// Torrent represents a single torrent as returned by the qBittorrent WebUI API.
type Torrent struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
	State    string  `json:"state"`
	Category string  `json:"category"`
	SavePath string  `json:"save_path"`
	ETA      int     `json:"eta"`
	AddedOn  int64   `json:"added_on"`
}
