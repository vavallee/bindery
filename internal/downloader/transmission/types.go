package transmission

// Torrent represents a single torrent as returned by the Transmission RPC API.
type Torrent struct {
	ID             int64  `json:"id"`
	HashString     string `json:"hashString"`
	Name           string `json:"name"`
	TotalSize      int64  `json:"totalSize"`
	DownloadedEver int64  `json:"downloadedEver"`
	LeftUntilDone  int64  `json:"leftUntilDone"`
	// Status is the Transmission RPC status enum (stable since 2.40):
	//   0=stopped 1=queued-to-check 2=checking
	//   3=queued-to-download 4=downloading 5=queued-to-seed 6=seeding
	Status       int     `json:"status"`
	ErrorString  string  `json:"errorString"`
	DownloadRate int64   `json:"rateDownload"`
	UploadRate   int64   `json:"rateUpload"`
	ETA          int64   `json:"eta"`
	PercentDone  float64 `json:"percentDone"`
	// MetadataPercentComplete is how much of the torrent's metadata
	// Transmission holds, from 0 to 1. A magnet sits at 0 until a peer serves
	// it the torrent file; a real .torrent is 1 from the start. Reported since
	// RPC version 14 (Transmission 2.80); an older daemon simply omits it and
	// it decodes as 0.
	MetadataPercentComplete float64 `json:"metadataPercentComplete"`
	// PeersConnected is the number of peers Transmission currently has a
	// connection to. Bindery only reports it, never decides on it: a magnet
	// with peers that will not serve its metadata is just as dead as one with
	// no peers at all.
	PeersConnected int      `json:"peersConnected"`
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
