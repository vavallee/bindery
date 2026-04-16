package transmission

// Torrent represents a single torrent as returned by the Transmission RPC API.
type Torrent struct {
	ID             int64    `json:"id"`
	HashString     string   `json:"hashString"`
	Name           string   `json:"name"`
	TotalSize      int64    `json:"totalSize"`
	DownloadedEver int64    `json:"downloadedEver"`
	LeftUntilDone  int64    `json:"leftUntilDone"`
	Status         int      `json:"status"` // 0=stopped, 1=checking, 2=downloading, 3=seeding, 4=allocating, 5=checking, 6=stopped
	ErrorString    string   `json:"errorString"`
	DownloadRate   int64    `json:"rateDownload"`
	UploadRate     int64    `json:"rateUpload"`
	ETA            int64    `json:"eta"`
	PercentDone    float64  `json:"percentDone"`
	DownloadDir    string   `json:"downloadDir"`
	Labels         []string `json:"labels"`
}

// TorrentAddResponse is returned when adding a torrent.
type TorrentAddResponse struct {
	Arguments struct {
		TorrentAdded     Torrent `json:"torrent-added"`
		TorrentDuplicate Torrent `json:"torrent-duplicate"`
	} `json:"arguments"`
	Result string `json:"result"`
}

// TorrentGetResponse is returned when getting torrent data.
type TorrentGetResponse struct {
	Arguments struct {
		Torrents []Torrent `json:"torrents"`
	} `json:"arguments"`
	Result string `json:"result"`
}

// SimpleResponse is a generic response from Transmission RPC.
type SimpleResponse struct {
	Result string `json:"result"`
}
