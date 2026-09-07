// Shared cross-domain types: the pagination envelope used by every List
// endpoint, and BookRef (the minimal book+author projection embedded in
// queue/pending/history/download items).

// Page<T> is the envelope returned by every paginated List endpoint
// (GET /book, /author, /history, /abs/review, /abs/conflicts). It was briefly
// duplicated as PaginatedResponse<T> when PR #902 added the first three; the
// two declarations were identical and are now one (#1000).
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
