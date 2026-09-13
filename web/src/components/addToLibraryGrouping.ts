import type { Author, Book } from '../api/client'
import { foldForSearch } from '../util/foldForSearch'

// One row of the mixed result list in AddToLibraryModal. Author rows are
// followed by the books the provider attributes to them, Readarr style, so a
// query like "le guin" reads as the author and then her works rather than as
// two unrelated lists (#1227).
export type AddResultRow =
  | { kind: 'author'; author: Author }
  | { kind: 'book'; book: Book }
  | { kind: 'divider' }

function bookBelongsTo(book: Book, author: Author): boolean {
  const bookAuthor = book.author
  if (!bookAuthor) return false
  // The foreign id is authoritative when both sides carry one. Providers do
  // not always stamp it on book results (DNB, ISBN editions), so fall back to
  // the folded name, which is the same comparison the title guard uses.
  if (bookAuthor.foreignAuthorId && author.foreignAuthorId) {
    return bookAuthor.foreignAuthorId === author.foreignAuthorId
  }
  const name = foldForSearch(bookAuthor.authorName)
  return name !== '' && name === foldForSearch(author.authorName)
}

// groupAddResults interleaves author and book results: each author row is
// followed by its books, in the order the book search returned them; a book is
// claimed by the first author it matches. Books that match no author row are
// listed after a divider, so a title search whose author did not come back
// from the author endpoint is still reachable.
export function groupAddResults(authors: Author[], books: Book[]): AddResultRow[] {
  const rows: AddResultRow[] = []
  const claimed = new Set<number>()
  for (const author of authors) {
    rows.push({ kind: 'author', author })
    books.forEach((book, i) => {
      if (claimed.has(i) || !bookBelongsTo(book, author)) return
      claimed.add(i)
      rows.push({ kind: 'book', book })
    })
  }
  const rest = books.filter((_, i) => !claimed.has(i))
  if (rest.length > 0) {
    if (rows.length > 0) rows.push({ kind: 'divider' })
    for (const book of rest) rows.push({ kind: 'book', book })
  }
  return rows
}
