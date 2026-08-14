import { describe, it, expect } from 'vitest'
import { rtorrentScgiIgnoredFields, downloadClientPathRemapHelp } from './helpers'

// rTorrent's SCGI listener speaks neither TLS nor any authentication — that is
// the protocol, not a gap in Bindery. The form still shows Use SSL, Username
// and Password though, and the backend drops all three when the URL base
// selects SCGI. Without this the operator saves a client that looks configured
// and secured and is neither, and nothing says so until they read QUICKSTART.
describe('rtorrentScgiIgnoredFields', () => {
  it('names each field SCGI cannot carry', () => {
    expect(rtorrentScgiIgnoredFields('rtorrent', 'scgi://', true, 'admin', 'hunter2'))
      .toEqual(['Use SSL', 'Username', 'Password'])
    expect(rtorrentScgiIgnoredFields('rtorrent', 'scgi://127.0.0.1:5000', false, '', 'hunter2'))
      .toEqual(['Password'])
    expect(rtorrentScgiIgnoredFields('rtorrent', 'SCGI:///var/run/rtorrent/rpc.sock', true, '', ''))
      .toEqual(['Use SSL'])
    // Whitespace-only username is not a configured username.
    expect(rtorrentScgiIgnoredFields('rtorrent', '  scgi://  ', false, '   ', '')).toEqual([])
  })

  it('stays silent when nothing is being dropped', () => {
    // No SCGI: the HTTP transport carries all three.
    expect(rtorrentScgiIgnoredFields('rtorrent', '/RPC2', true, 'admin', 'hunter2')).toEqual([])
    expect(rtorrentScgiIgnoredFields('rtorrent', '', true, 'admin', 'hunter2')).toEqual([])
    // A path that merely starts with the letters is still an HTTP path.
    expect(rtorrentScgiIgnoredFields('rtorrent', '/scgi', true, 'admin', 'hunter2')).toEqual([])
    // Another client type never reaches this transport at all.
    expect(rtorrentScgiIgnoredFields('qbittorrent', 'scgi://', true, 'admin', 'hunter2')).toEqual([])
    // SCGI with nothing filled in has nothing to warn about.
    expect(rtorrentScgiIgnoredFields('rtorrent', 'scgi://', false, '', '')).toEqual([])
  })
})

describe('downloadClientPathRemapHelp', () => {
  it('describes the reverse and delete uses for rTorrent', () => {
    const help = downloadClientPathRemapHelp('rtorrent')
    expect(help).toContain('rTorrent')
    expect(help).toContain('remove a download with its data')
  })
})
