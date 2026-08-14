import { describe, expect, it } from 'vitest'
import { clientProtocol, indexerProtocol, protocolGaps } from './protocols'
import type { Indexer } from '../api/indexers'
import type { DownloadClient } from '../api/downloadclients'

const ix = (type: string, enabled = true) => ({ type, enabled }) as Indexer
const cl = (type: string, enabled = true) => ({ type, enabled }) as DownloadClient

describe('protocol mapping', () => {
  it('classifies indexers and clients', () => {
    expect(indexerProtocol('torznab')).toBe('torrent')
    expect(indexerProtocol('newznab')).toBe('usenet')
    expect(indexerProtocol('')).toBe('usenet') // backend default type
    expect(clientProtocol('qbittorrent')).toBe('torrent')
    expect(clientProtocol('transmission')).toBe('torrent')
    expect(clientProtocol('deluge')).toBe('torrent')
    expect(clientProtocol('rtorrent')).toBe('torrent')
    expect(clientProtocol('sabnzbd')).toBe('usenet')
    expect(clientProtocol('nzbget')).toBe('usenet')
  })

  // A torrent client missing from TORRENT_CLIENTS is silently classified as
  // usenet, which makes the mismatch banner claim a configured torrent client
  // doesn't exist. Mirrors downloader/adapter.go IsTorrentClient.
  it('covers a torznab indexer with an rTorrent client', () => {
    expect(protocolGaps([ix('torznab')], [cl('rtorrent')])).toEqual([])
  })
})

describe('protocolGaps', () => {
  it('flags a torznab indexer with only a usenet client', () => {
    expect(protocolGaps([ix('torznab')], [cl('sabnzbd')])).toEqual(['torrent'])
  })

  it('flags a newznab indexer with only a torrent client', () => {
    expect(protocolGaps([ix('newznab')], [cl('qbittorrent')])).toEqual(['usenet'])
  })

  it('is empty when protocols line up', () => {
    expect(protocolGaps([ix('newznab'), ix('torznab')], [cl('sabnzbd'), cl('deluge')])).toEqual([])
  })

  it('ignores disabled rows on both sides', () => {
    // Disabled torrent client doesn't cover the torznab indexer...
    expect(protocolGaps([ix('torznab')], [cl('qbittorrent', false)])).toEqual(['torrent'])
    // ...and a disabled indexer needs no coverage.
    expect(protocolGaps([ix('torznab', false)], [cl('sabnzbd')])).toEqual([])
  })

  it('is empty with no indexers (that state is the setup banner, not this warning)', () => {
    expect(protocolGaps([], [])).toEqual([])
  })
})
