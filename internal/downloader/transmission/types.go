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

// File is a single file belonging to a torrent, as reported by torrent-get
// with fields=["files"]. Name is the path relative to the torrent's
// downloadDir; for a single-file torrent it is just the file's basename.
type File struct {
	Name string
	Size int64
}

// rpcFile mirrors the Transmission RPC shape: each entry under arguments
// .torrents[].files is {bytesCompleted, length, name}. Bindery only needs
// name + length to drive the importer; bytesCompleted is intentionally
// dropped (the per-torrent percentDone already gates whether files are
// flushed to disk).
type rpcFile struct {
	Name           string `json:"name"`
	Length         int64  `json:"length"`
	BytesCompleted int64  `json:"bytesCompleted"`
}

// torrentFilesResponse decodes the torrent-get response when only the
// files field is requested.
type torrentFilesResponse struct {
	Arguments struct {
		Torrents []struct {
			ID    int64     `json:"id"`
			Files []rpcFile `json:"files"`
		} `json:"torrents"`
	} `json:"arguments"`
	Result string `json:"result"`
}
