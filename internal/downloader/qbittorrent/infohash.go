package qbittorrent

import "github.com/vavallee/bindery/internal/downloader/infohash"

// infoHashFromTorrentFile computes a torrent's v1 infohash — the SHA-1 of the
// bencoded "info" dictionary — from raw .torrent file bytes. It returns "" when
// data is not a bencoded dictionary containing an "info" key.
//
// qBittorrent's 409 "already present" response to POST /torrents/add carries no
// hash, so when a torrent is submitted as a file upload this lets AddTorrent
// recover the hash of the torrent qBittorrent already holds.
//
// The bencode walk lives in internal/downloader/infohash because rTorrent needs
// the same derivation (its load.* commands return 0, never a hash).
func infoHashFromTorrentFile(data []byte) string {
	return infohash.FromTorrentFile(data)
}

// infoHashFromMagnet extracts the lower-cased btih topic of a magnet URI, or ""
// when raw is not a magnet link carrying one.
func infoHashFromMagnet(raw string) string {
	return infohash.FromMagnet(raw)
}
