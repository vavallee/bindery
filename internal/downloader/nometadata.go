package downloader

import (
	"strings"

	"github.com/vavallee/bindery/internal/downloader/deluge"
	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/downloader/rtorrent"
	"github.com/vavallee/bindery/internal/downloader/transmission"
)

// StallReport is what a download client says about its own stalls, split by
// how far the signal can be trusted on its own.
//
// The split is the point of the type. The two halves are not interchangeable:
// one is a per torrent fact the client is asserting, the other is an inference
// that is only sound once enough time has passed. Returning them in one map
// invited a future caller to act on both, so they are separate fields with
// separate contracts, and the dangerous one is named after what it is.
type StallReport struct {
	// ClientReported is the download client's own signal, keyed by remote id:
	// qBittorrent's stalledDL, a Transmission torrent stopped with an
	// errorString, a Deluge torrent in the Error state, an rTorrent d.message
	// on an incomplete item. Safe to act on the moment it appears: the client
	// is asserting something went wrong with this particular torrent.
	ClientReported map[string]bool

	// NoMetadata is torrents the client accepted and is still holding but has
	// never resolved the metadata for: no file list, no total size, no
	// progress.
	//
	// THIS IS NOT SAFE TO ACT ON BY ITSELF. Two guards belong to the caller:
	//
	//   - Age. A healthy magnet looks exactly like this while it resolves, so
	//     acting on a fresh entry fails every magnet the moment it is grabbed.
	//     The caller must have waited at least the stall timeout since the
	//     download was grabbed.
	//   - Breadth. "No metadata" is a property of the network as much as of
	//     the release, so a client that has lost DHT, UDP or its port forward
	//     reports it for everything at once. See LooksLikeClientOutage.
	//
	// Scheduler.checkStalledDownloads is the only caller and applies both.
	NoMetadata map[string]bool

	// Incomplete is how many torrents the client listed that have not finished
	// downloading. It is the denominator for LooksLikeClientOutage: what
	// matters is the share of the work in flight that is stuck this way, and a
	// seeding library would otherwise dilute it to nothing.
	Incomplete int

	// UsesTorrentID matches GetLiveStatuses: true when the keys are torrent
	// hashes or client-local torrent ids rather than NZO ids.
	UsesTorrentID bool
}

// noMetadataOutageFloor and noMetadataOutageShare are the batch guard: above
// both, blame the client rather than the releases.
//
// The share is "more than half of everything the client is still working on".
// One dead magnet in a healthy queue is a bad release; most of the queue stuck
// the same way at the same time is not a run of bad luck in release selection,
// it is DHT, UDP, the VPN or the port forward. Half is deliberately not
// aggressive: a user whose grabs genuinely are mostly dead magnets still wants
// them failed, and everything below the line is still handled one at a time.
//
// The floor exists because a share means nothing at n=1 (one wanted book, one
// dead magnet, 100 percent) and it is exactly that install the feature is for.
// Three is the smallest count where "all of them, at once" is more likely to be
// one cause than three coincidences, and below the floor the damage is bounded
// anyway: a no-metadata stall fails the queue row and does not blocklist.
const (
	noMetadataOutageFloor = 3
	noMetadataOutageShare = 0.5
)

// LooksLikeClientOutage reports whether so much of this client's in-flight work
// has no metadata that the client, not the releases, is the likely cause.
//
// When it is true the caller must leave the NoMetadata entries alone. Failing
// them would take a temporary network fault and turn it into a pile of failed
// downloads, and, before this guard existed, a pile of permanent blocklist
// entries against releases that were never at fault.
func (r StallReport) LooksLikeClientOutage() bool {
	n := len(r.NoMetadata)
	if n < noMetadataOutageFloor || r.Incomplete <= 0 {
		return false
	}
	return float64(n) > float64(r.Incomplete)*noMetadataOutageShare
}

// StallKind says why one download was treated as stalled. It travels from the
// detector to the handler so the handler can pick both the wording and the
// consequences.
type StallKind uint8

const (
	// StallNone is the zero value and means "not stalled". It is never a valid
	// argument to the stall handler; Reason says so in words, because a zero
	// value that produced a plausible reason would let a future caller fail a
	// download by forgetting to set this field.
	StallNone StallKind = iota
	// StallClientReported is the download client's own per torrent signal.
	StallClientReported
	// StallNoMetadata is a torrent the client accepted and never resolved.
	StallNoMetadata
)

// String is the short machine readable label that goes in the log line.
func (k StallKind) String() string {
	switch k {
	case StallNoMetadata:
		return "no_metadata"
	case StallClientReported:
		return "client_reported"
	default:
		return "none"
	}
}

// Reason is the user facing sentence stored on the failed download, on the
// blocklist entry and in the history event.
func (k StallKind) Reason() string {
	switch k {
	case StallNoMetadata:
		return "stalled: the download client never resolved this magnet's metadata, so it has no files and no size"
	case StallClientReported:
		return "stalled: no peers / no download progress"
	default:
		return "BUG: the stall handler was called with no stall reason, please report this"
	}
}

// Blocklists reports whether a stall of this kind should blocklist the release.
//
// Only the client's own per torrent signal does. That signal is the client
// saying something is wrong with this torrent, which is a fair reason never to
// grab that release again.
//
// A no-metadata stall is not. Nothing was learned about the release: every
// magnet in the client looks the same way when DHT is blocked, a VPN drops or a
// port forward is lost, so blocklisting on it would let one network fault
// permanently ban a run of perfectly good releases, and the blocklist has no
// expiry and is clearable only by hand. Failing the download without
// blocklisting still clears the queue row and still lets a later search pick
// the release up again once the network is back.
func (k StallKind) Blocklists() bool {
	return k == StallClientReported
}

// The "accepted but never resolved" rule (#2709).
//
// A magnet that nobody is serving is accepted by every torrent client and then
// sits there for ever. The client is not in an error state and has not stopped
// trying, so none of the native stall signals fire: Transmission leaves
// errorString empty, qBittorrent parks it in metaDL rather than stalledDL,
// Deluge calls it Downloading, rTorrent keeps it as a .meta placeholder with no
// message. The reporter's torrent had been in that state for 34 days with the
// queue row still reading "downloading".
//
// The shared shape across all four clients is: the client knows nothing about
// the content. No total size, no progress, not complete. That is only ever true
// of a torrent whose metadata has not arrived, because a real torrent's size is
// known the moment its metadata is.
//
// What the shape does NOT tell you is whose fault it is, which is why
// StallReport keeps these entries apart and documents the two guards the caller
// owes them.

// transmissionHasNoMetadata reports whether a Transmission torrent is one the
// daemon has accepted but never resolved.
//
// totalSize is the load-bearing field: Transmission reports 0 until the
// metadata arrives and the real size for ever after, so it does not depend on
// which status the torrent happens to be parked in. The reporter's torrent was
// status 0 (stopped) despite never having been stopped by hand, which is why
// status is deliberately not part of the test.
//
// metadataPercentComplete is the daemon saying the same thing in its own words
// and is only a cross-check: a Transmission too old to report it (pre RPC 14,
// 2013) sends 0, which the totalSize test has already established.
func transmissionHasNoMetadata(t transmission.Torrent) bool {
	return t.TotalSize == 0 && t.PercentDone == 0 && t.MetadataPercentComplete < 1
}

// qbittorrentHasNoMetadata reports whether a qBittorrent torrent is stuck
// fetching metadata. qBittorrent names this state outright, which is why the
// size is only a guard: metaDL is the state a magnet holds from the moment it
// is added until its metadata lands, and forcedMetaDL is the same state on a
// torrent the user forced to run.
func qbittorrentHasNoMetadata(t qbittorrent.Torrent) bool {
	state := strings.ToLower(t.State)
	return (state == "metadl" || state == "forcedmetadl") && t.Size == 0 && t.Progress == 0
}

// delugeHasNoMetadata reports whether a Deluge torrent is stuck fetching
// metadata. Deluge folds libtorrent's downloading_metadata into the plain
// Downloading state, so there is no state string to key on and total_size is
// the only signal. Deluge reports the full content size there regardless of
// which files are selected, so a fully deselected torrent does not land here.
func delugeHasNoMetadata(t deluge.TorrentStatus) bool {
	return strings.EqualFold(t.State, "downloading") && t.TotalSize == 0 && t.Progress == 0
}

// rtorrentHasNoMetadata reports whether an rTorrent item is a magnet
// placeholder that never resolved. rTorrent loads a magnet as a "<hash>.meta"
// item whose d.size_bytes stays 0 until it pulls the metadata off the DHT (see
// Client.Add, which already warns about exactly this window).
func rtorrentHasNoMetadata(t rtorrent.Torrent) bool {
	return t.SizeBytes <= 0 && !t.Complete
}
