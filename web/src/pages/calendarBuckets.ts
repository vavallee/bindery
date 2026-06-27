import { Book } from '../api/client'

// bucketBooksByDay groups monitored books with a release date into the given
// month view, keyed by day-of-month.
//
// The date portion of releaseDate is parsed directly from the ISO string rather
// than via `new Date()`. releaseDate is a UTC timestamp, and reading it back
// with getFullYear/getMonth/getDate applies the browser's local-timezone
// offset, which shifts a midnight-UTC release into the previous day — and out
// of the viewed month — for users west of UTC. Mirrors the TZ-safe
// `releaseDate.slice(0, 10)` comparison used elsewhere (AuthorDetailPage).
export function bucketBooksByDay(books: Book[], viewYear: number, viewMonth: number): Record<number, Book[]> {
  const booksByDay: Record<number, Book[]> = {}
  for (const book of books) {
    if (!book.releaseDate || !book.monitored) continue
    const [y, m, dd] = book.releaseDate.slice(0, 10).split('-').map(Number)
    if (y === viewYear && m - 1 === viewMonth) {
      if (!booksByDay[dd]) booksByDay[dd] = []
      booksByDay[dd].push(book)
    }
  }
  return booksByDay
}
