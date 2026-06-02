package deluge

// TorrentStatus holds the fields returned by core.get_torrents_status.
type TorrentStatus struct {
	Name         string  `json:"name"`
	Hash         string  `json:"hash"`
	Progress     float64 `json:"progress"`              // 0–100
	State        string  `json:"state"`                 // "Downloading", "Seeding", "Paused", "Error", etc.
	ETA          int64   `json:"eta"`                   // seconds remaining; -1 or 0 if unknown
	DownloadRate int64   `json:"download_payload_rate"` // bytes/s
	TotalSize    int64   `json:"total_size"`
	TotalDone    int64   `json:"total_done"`
}

// File is a single file belonging to a torrent, as returned by
// core.get_torrent_status with keys=["files"]. Path is the file path
// relative to the torrent's save path; for a single-file torrent it is
// just the file's basename.
type File struct {
	Name string
	Size int64
}

// rpcFile mirrors the Deluge core.get_torrent_status "files" entry shape:
// {index, offset, path, size}. Only path + size are kept.
type rpcFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// torrentFilesStatus holds the keys we request from core.get_torrent_status
// when only the file list is needed.
type torrentFilesStatus struct {
	Files []rpcFile `json:"files"`
}
