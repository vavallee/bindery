import { describe, it, expect } from 'vitest'
import { bucketBooksByDay } from './calendarBuckets'
import { Book } from '../api/client'

function book(partial: Partial<Book>): Book {
  return { id: 1, title: 't', monitored: true, ...partial } as Book
}

describe('bucketBooksByDay', () => {
  it('buckets a midnight-UTC release on its calendar day regardless of local timezone', () => {
    // A book releasing 2026-06-01T00:00:00Z must land on June 1, not May 31,
    // even for a browser west of UTC where new Date(...).getDate() would be 31.
    const books = [book({ releaseDate: '2026-06-01T00:00:00Z' })]
    const byDay = bucketBooksByDay(books, 2026, 5) // month is 0-based: 5 = June
    expect(byDay[1]?.length).toBe(1)
    expect(byDay[31]).toBeUndefined()
  })

  it('keeps the first-of-month release inside the viewed month', () => {
    const books = [book({ releaseDate: '2026-06-01T00:00:00Z' })]
    // Viewing May must NOT show a June 1 release.
    expect(Object.keys(bucketBooksByDay(books, 2026, 4))).toHaveLength(0)
  })

  it('accepts a date-only string', () => {
    const byDay = bucketBooksByDay([book({ releaseDate: '2026-06-15' })], 2026, 5)
    expect(byDay[15]?.length).toBe(1)
  })

  it('skips unmonitored books and books without a release date', () => {
    const books = [
      book({ releaseDate: '2026-06-10T00:00:00Z', monitored: false }),
      book({ releaseDate: undefined }),
    ]
    expect(Object.keys(bucketBooksByDay(books, 2026, 5))).toHaveLength(0)
  })
})
