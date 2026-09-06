import type { Author, Book } from '../api/client'

import { foldForSearch } from '../util/foldForSearch'

export interface AuthorResultSplit {
  visible: Author[]
  hidden: Author[]
}

// Delegates to the shared fold so this guard and the search box agree about
// what "the same text" means. It used to carry its own copy, which was the only
// NFKC normalizer in the project and had no Go counterpart; the shared one adds
// accent folding and apostrophe deletion, so "José" now meets "Jose" and
// "Ender's Game" meets "Enders Game" (#1660).
function normalizeMetadataText(value: string | undefined): string {
  return foldForSearch(value)
}

function isDifferentKnownAuthor(candidate: Author, bookAuthor: Author): boolean {
  if (!candidate.foreignAuthorId || !bookAuthor.foreignAuthorId) return false
  return candidate.foreignAuthorId !== bookAuthor.foreignAuthorId
}

export function isLikelyBookTitleAuthorResult(candidate: Author, exactBookMatches: Book[], query: string): boolean {
  const normalizedCandidateName = normalizeMetadataText(candidate.authorName)
  if (normalizedCandidateName === '' || normalizedCandidateName !== normalizeMetadataText(query)) {
    return false
  }

  const normalizedTopWork = normalizeMetadataText(candidate.disambiguation)
  if (normalizedTopWork === '') return false

  return exactBookMatches.some(book => {
    if (!book.author) return false
    return (
      isDifferentKnownAuthor(candidate, book.author) &&
      normalizedTopWork === normalizeMetadataText(book.author.authorName)
    )
  })
}

export function splitAuthorSearchResults(authors: Author[], books: Book[], query: string): AuthorResultSplit {
  const normalizedQuery = normalizeMetadataText(query)
  if (normalizedQuery === '') return { visible: authors, hidden: [] }

  const exactBookMatches = books.filter(book => normalizeMetadataText(book.title) === normalizedQuery)
  if (exactBookMatches.length === 0) return { visible: authors, hidden: [] }

  const visible: Author[] = []
  const hidden: Author[] = []
  for (const author of authors) {
    if (isLikelyBookTitleAuthorResult(author, exactBookMatches, query)) {
      hidden.push(author)
    } else {
      visible.push(author)
    }
  }

  return { visible, hidden }
}
