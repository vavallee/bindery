// Non-component helpers shared between Settings tabs. Kept out of the
// component files so React Fast Refresh stays happy.

// parseCats parses a comma-separated list of Newznab category IDs.
export function parseCats(s: string): number[] {
  return s.split(',').map(t => parseInt(t.trim(), 10)).filter(n => !isNaN(n))
}

// parsePriority parses an indexer priority (any integer; higher wins ties in
// release ranking). Blank or non-numeric falls back to 0.
export function parsePriority(s: string): number {
  const n = parseInt(s.trim(), 10)
  return isNaN(n) ? 0 : n
}

// rtorrentScgiIgnoredFields lists the fields the form is showing that the SCGI
// transport cannot use, or [] when none apply.
//
// rTorrent's own SCGI listener speaks neither TLS nor any authentication — that
// is the protocol, not a gap in Bindery. But the form still shows Use SSL, a
// username and a password, and Bindery silently drops all three when the URL
// base selects SCGI. An operator who fills them in gets a client that appears
// configured and secured and is neither, with nothing said at save time.
export function rtorrentScgiIgnoredFields(
  type: string,
  urlBase: string,
  useSSL: boolean,
  username: string,
  credential: string,
): string[] {
  if (type !== 'rtorrent') return []
  if (!urlBase.trim().toLowerCase().startsWith('scgi')) return []
  const ignored: string[] = []
  if (useSSL) ignored.push('Use SSL')
  if (username.trim()) ignored.push('Username')
  if (credential) ignored.push('Password')
  return ignored
}

// downloadClientPathRemapHelp returns the help text for the path-remap field
// of a given download-client type.
export function downloadClientPathRemapHelp(type: string) {
  if (type === 'qbittorrent') {
    return "Map the path qBittorrent reports to the path Bindery can read. Example: if qBittorrent shows /downloads/books but Bindery sees that folder at /media/books, use /downloads:/media/books. Bindery also uses this in reverse when sending new torrents."
  }
  if (type === 'rtorrent') {
    return "Map the path rTorrent reports to the path Bindery can read. Seedbox installs almost always need this — e.g. rTorrent writes to /home/user/downloads while Bindery mounts that share at /media/books, so use /home/user/downloads:/media/books. Bindery also uses this in reverse when setting a new torrent's download directory, and to locate files when you remove a download with its data."
  }
  return "Optional and separate from ABS remaps. Use when this download client reports paths under a different mount than Bindery."
}
