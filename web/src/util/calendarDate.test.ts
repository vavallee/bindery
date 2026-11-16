import { describe, expect, it } from 'vitest'
import { useTimeZone } from '../test/useTimeZone'
import { calendarYear, formatCalendarDate } from './calendarDate'

describe('calendarDate under a negative UTC offset', () => {
  useTimeZone('America/New_York')

  it('runs in a zone west of UTC', () => {
    expect(new Date('2014-01-01T00:00:00Z').getFullYear()).toBe(2013)
  })

  it('keeps the year at Jan 1 UTC midnight', () => {
    expect(calendarYear('2014-01-01T00:00:00Z')).toBe(2014)
  })

  it('keeps the year at Dec 31 23:59 UTC', () => {
    expect(calendarYear('2013-12-31T23:59:00Z')).toBe(2013)
  })

  it('formats the UTC day, honouring the locale', () => {
    expect(formatCalendarDate('2014-01-01T00:00:00Z', 'en-US')).toBe('Jan 1, 2014')
    expect(formatCalendarDate('2014-01-01T00:00:00Z', 'de-DE', { dateStyle: 'medium' })).toBe('01.01.2014')
    expect(formatCalendarDate('2013-12-31T23:59:00Z', 'en-US')).toBe('Dec 31, 2013')
  })

  it('returns null for empty or invalid input', () => {
    expect(calendarYear(undefined)).toBeNull()
    expect(calendarYear('nope')).toBeNull()
    expect(formatCalendarDate('')).toBeNull()
    expect(formatCalendarDate('nope')).toBeNull()
  })
})
