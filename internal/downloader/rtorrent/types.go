package rtorrent

// Torrent is one row of a d.multicall2 poll. Field order here is the contract
// with multicallFields in client.go — changing one without the other silently
// shifts every column.
type Torrent struct {
	// Name is d.name.
	Name string
	// Hash is d.hash, normalised to lower-case hex. rTorrent itself reports
	// upper-case; Bindery stores torrent IDs lower-cased for every client, so
	// the conversion happens at the boundary.
	Hash string
	// BasePath is d.base_path — the torrent's own file or directory on the
	// rTorrent host, not the parent download directory. It is empty for a
	// magnet that has not resolved metadata yet, and for closed items after an
	// rTorrent restart.
	BasePath string
	// Directory is d.directory, the parent download directory. Used as the
	// join base for the relative paths f.multicall returns, and as a fallback
	// when BasePath is empty.
	Directory string
	// Label is d.custom1, URL-decoded. ruTorrent stores its label there
	// percent-encoded, so a label with a space arrives as "sci%20fi".
	Label string
	// SizeBytes is d.size_bytes, LeftBytes is d.left_bytes.
	SizeBytes int64
	LeftBytes int64
	// DownRate is d.down.rate in bytes/second.
	DownRate int64
	// Complete is d.complete: the torrent has all its data.
	Complete bool
	// IsActive is d.is_active: rTorrent is working the torrent (as opposed to
	// stopped or paused).
	IsActive bool
	// IsOpen is d.is_open: rTorrent holds the torrent's files open.
	IsOpen bool
	// Message is d.message — rTorrent's per-torrent error slot ("Tracker: ...",
	// hash-check failures). Empty on a healthy torrent.
	Message string
}

// Progress returns completion as a percentage in [0, 100]. A torrent whose
// size rTorrent has not resolved yet (magnet awaiting metadata) reports 0.
func (t Torrent) Progress() float64 {
	if t.SizeBytes <= 0 {
		return 0
	}
	done := t.SizeBytes - t.LeftBytes
	if done <= 0 {
		return 0
	}
	if done >= t.SizeBytes {
		return 100
	}
	return float64(done) / float64(t.SizeBytes) * 100
}

// ETA returns the estimated seconds remaining, or 0 when it cannot be
// computed. rTorrent has no ETA command — unlike Transmission and qBittorrent
// it never estimates one — so this is derived from the current rate.
func (t Torrent) ETA() int64 {
	if t.DownRate <= 0 || t.LeftBytes <= 0 {
		return 0
	}
	return t.LeftBytes / t.DownRate
}

// File is one entry of a torrent's file list, as returned by f.multicall.
// Name is relative to the torrent's Directory.
type File struct {
	Name string
	Size int64
}
