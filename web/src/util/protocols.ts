import type { Indexer } from '../api/indexers'
import type { DownloadClient } from '../api/downloadclients'

// Protocol matching between indexers and download clients. A Newznab
// indexer yields NZBs that only a usenet client (SABnzbd, NZBGet) can
// take; a Torznab indexer yields torrents that only a torrent client
// (qBittorrent, Transmission, Deluge, rTorrent) can take. The rule was documented
// only in QUICKSTART.md step 4 — users discovered it when their first
// grab failed. Mirrors the backend: indexer/searcher.go protocolForType
// and downloader/adapter.go IsTorrentClient/ProtocolForClient.

export type Protocol = 'usenet' | 'torrent'

export function indexerProtocol(type: string): Protocol {
  return type === 'torznab' ? 'torrent' : 'usenet'
}

const TORRENT_CLIENTS = new Set(['transmission', 'qbittorrent', 'deluge', 'rtorrent'])

export function clientProtocol(type: string): Protocol {
  return TORRENT_CLIENTS.has(type) ? 'torrent' : 'usenet'
}

// protocolGaps returns the protocols for which enabled indexers exist but
// no enabled download client does — i.e. searches will find releases that
// nothing can download. Disabled rows don't count on either side.
export function protocolGaps(indexers: Indexer[], clients: DownloadClient[]): Protocol[] {
  const clientProtocols = new Set(
    clients.filter(c => c.enabled).map(c => clientProtocol(c.type)),
  )
  const gaps = new Set<Protocol>()
  for (const ix of indexers) {
    if (!ix.enabled) continue
    const p = indexerProtocol(ix.type)
    if (!clientProtocols.has(p)) gaps.add(p)
  }
  return [...gaps]
}
