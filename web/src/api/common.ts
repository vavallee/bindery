// Shared cross-domain types: the pagination envelopes used by several List
// endpoints, and BookRef (the minimal book+author projection embedded in
// queue/pending/history/download items).

export interface PaginatedResponse<T> {
  items: T[]
  total: number
  limit: number
  offset: number
}

// Page<T> is the envelope returned by the paginated List endpoints introduced
// in PR #902 (GET /book, /author, /history). Shape matches PaginatedResponse
// above and the two will likely be unified later; kept distinct for now so
// the diff stays scoped to the three new endpoints.
export interface Page<T> {
  items: T[]
  total: number
  limit: number
  offset: number
}

// BookRef is the minimal book + author projection the backend attaches to
// queue, pending, and history items so the UI can link the book title and
// author name to /book/:id and /author/:id. Absent when the row has no
// associated book (manual downloads, orphan history events).
export interface BookRef {
  id: number
  title: string
  authorId: number
  authorName: string
}
