// The API stores release dates as UTC midnight timestamps
// (e.g. "2014-01-01T00:00:00Z"). They are calendar dates, not instants, so they
// must be read back in UTC. Reading them in the browser's zone shifts them to
// the previous day for anyone west of UTC (Dec 31 instead of Jan 1).
// Do not use these for real instants such as createdAt or last-seen times.

// calendarYear returns the UTC year of a calendar date, or null when the value
// is empty or unparseable.
export function calendarYear(value?: string | null): number | null {
  if (!value) return null
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? null : d.getUTCFullYear()
}

// formatCalendarDate renders a calendar date in the given locale
// (undefined means the browser default), using the UTC day.
export function formatCalendarDate(
  value: string | null | undefined,
  locale?: string | string[],
  options: Intl.DateTimeFormatOptions = { year: 'numeric', month: 'short', day: 'numeric' },
): string | null {
  if (!value) return null
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return null
  return d.toLocaleDateString(locale, { ...options, timeZone: 'UTC' })
}
