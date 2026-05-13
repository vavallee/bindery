package qbittorrent

import "encoding/json"

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

// Category represents a qBittorrent category. Different qBittorrent versions
// have used different JSON keys for the category save path, so UnmarshalJSON
// accepts all observed variants and normalizes them to SavePath.
type Category struct {
	Name     string `json:"name"`
	SavePath string `json:"savePath"`
}

func (c *Category) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name          string `json:"name"`
		SavePath      string `json:"savePath"`
		SavePathSnake string `json:"save_path"`
		DownloadPath  string `json:"download_path"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Name = raw.Name
	c.SavePath = raw.SavePath
	if c.SavePath == "" {
		c.SavePath = raw.SavePathSnake
	}
	if c.SavePath == "" {
		c.SavePath = raw.DownloadPath
	}
	return nil
}
