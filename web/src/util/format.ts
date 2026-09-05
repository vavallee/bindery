// Shared display formatters. Byte sizes had seven near-copies across the pages
// with four different behaviours (#2350): the same 800 MB release read
// "800 MB" on Search and "800.0 MB" on Queue, Queue and Wanted rendered "0 KB"
// for a zero size where the others rendered nothing, and RootFoldersTab had no
// unit clamp so anything a petabyte or larger came out as "1.1 undefined".

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'] as const

/**
 * formatBytes renders a byte count as a human-readable size: whole numbers for
 * B and KB, one decimal from MB up. The unit index is clamped to the table, so
 * a petabyte-scale number stays "1.0 PB" instead of running off the end.
 *
 * A zero, negative or non-finite input returns '' rather than "0 B". Most
 * callers are release sizes, where an unknown size should render as nothing (or
 * as the caller's own em-dash placeholder), not as a confident "0 B". Use
 * formatBytesZero for the cases that genuinely mean zero, like free disk space.
 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return ''
  const i = Math.min(UNITS.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)))
  const value = bytes / Math.pow(1024, i)
  return `${value.toFixed(i <= 1 ? 0 : 1)} ${UNITS[i]}`
}

/**
 * formatBytesZero is formatBytes for the callers where zero is a real value
 * worth showing: a root folder with no free space, a backup of nothing.
 */
export function formatBytesZero(bytes: number): string {
  return formatBytes(bytes) || '0 B'
}
