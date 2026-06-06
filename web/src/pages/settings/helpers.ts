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

// downloadClientPathRemapHelp returns the help text for the path-remap field
// of a given download-client type.
export function downloadClientPathRemapHelp(type: string) {
  if (type === 'qbittorrent') {
    return "Map the path qBittorrent reports to the path Bindery can read. Example: if qBittorrent shows /downloads/books but Bindery sees that folder at /media/books, use /downloads:/media/books. Bindery also uses this in reverse when sending new torrents."
  }
  return "Optional and separate from ABS remaps. Use when this download client reports paths under a different mount than Bindery."
}
