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

// The provider prefix of a foreign id ("dnb:123" gives "dnb"); ids without
// one are OpenLibrary's bare "OL…A" form and get the empty prefix.
function providerPrefix(id: string): string {
  const i = id.indexOf(':')
  return i === -1 ? '' : id.slice(0, i).toLowerCase()
}

// bookBelongsTo decides whether book groups under author. nameCounts holds,
// for each folded author name, how many distinct authors in this result set
// carry it (the same record returned twice counts once): the name fallback is
// only trusted when exactly one does, because two different ids sharing a
// name are two people the providers could not tell apart, and guessing the
// first one hands a stranger's books to the wrong author.
function bookBelongsTo(book: Book, author: Author, nameCounts: Map<string, number>): boolean {
  const bookAuthor = book.author
  if (!bookAuthor) return false
  const bookId = bookAuthor.foreignAuthorId
  const rowId = author.foreignAuthorId
  // Equal ids are a positive match. A mismatch is decisive only when both ids
  // come from the same provider: with the default setup every non primary
  // provider is an enricher, so a DNB book carries a dnb: author id while the
  // author row has been collapsed to the primary provider's record, and the
  // two ids can never agree even though they name the same person. Anything
  // else falls back to the folded name, the same comparison the title guard
  // uses, so those books still group under the row.
  if (bookId && rowId) {
    if (bookId === rowId) return true
    if (providerPrefix(bookId) === providerPrefix(rowId)) return false
  }
  const name = foldForSearch(bookAuthor.authorName)
  if (name === '' || name !== foldForSearch(author.authorName)) return false
  return (nameCounts.get(name) ?? 0) === 1
}

// groupAddResults interleaves author and book results: each author row is
// followed by its books, in the order the book search returned them. An exact
// author id match claims a book first, so a book is never lost to a same name
// row that happens to come earlier; the name fallback then claims the rest.
// Books that match no author row, or whose name matches several, are listed
// after a divider, so a title search whose author did not come back from the
// author endpoint is still reachable.
export function groupAddResults(authors: Author[], books: Book[]): AddResultRow[] {
  const idsByName = new Map<string, Set<string>>()
  authors.forEach((author, a) => {
    const n = foldForSearch(author.authorName)
    if (n === '') return
    const ids = idsByName.get(n) ?? new Set<string>()
    ids.add(author.foreignAuthorId || `row:${a}`)
    idsByName.set(n, ids)
  })
  const nameCounts = new Map<string, number>()
  idsByName.forEach((ids, n) => nameCounts.set(n, ids.size))
  const owner = new Map<number, number>()
  books.forEach((book, i) => {
    const id = book.author?.foreignAuthorId
    if (!id) return
    const a = authors.findIndex(x => x.foreignAuthorId === id)
    if (a !== -1) owner.set(i, a)
  })
  books.forEach((book, i) => {
    if (owner.has(i)) return
    const a = authors.findIndex(x => bookBelongsTo(book, x, nameCounts))
    if (a !== -1) owner.set(i, a)
  })
  const rows: AddResultRow[] = []
  const claimed = new Set<number>()
  authors.forEach((author, a) => {
    rows.push({ kind: 'author', author })
    books.forEach((book, i) => {
      if (owner.get(i) !== a) return
      claimed.add(i)
      rows.push({ kind: 'book', book })
    })
  })
  const rest = books.filter((_, i) => !claimed.has(i))
  if (rest.length > 0) {
    if (rows.length > 0) rows.push({ kind: 'divider' })
    for (const book of rest) rows.push({ kind: 'book', book })
  }
  return rows
}
