// Sort helpers for DataTable pages. List pages carry one combined sort value
// ("title-az", "date-new") because that is what the server whitelists; the
// table shows it as a column plus a direction.

/**
 * Splits a list page's combined sort value ("title-az", "date-new") into the
 * column key and direction DataTable shows, given each column's ascending and
 * descending values. Unknown values report no active column.
 */
export function splitSort(
  value: string,
  columns: Record<string, { asc: string; desc: string }>,
): { key: string; direction: 'asc' | 'desc' } {
  for (const [key, { asc, desc }] of Object.entries(columns)) {
    if (value === asc) return { key, direction: 'asc' }
    if (value === desc) return { key, direction: 'desc' }
  }
  return { key: '', direction: 'asc' }
}

/**
 * The next sort value when a column header is clicked: the active column
 * flips, another column starts ascending. The two values are the whitelisted
 * keys the server accepts for that column.
 */
export function nextSort(
  current: string,
  key: string,
  columns: Record<string, { asc: string; desc: string }>,
): string {
  const col = columns[key]
  if (!col) return current
  return current === col.asc ? col.desc : col.asc
}
