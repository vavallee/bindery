import { request } from './core'
import type { Author, MediaType } from './authors'
import type { Page } from './common'

export interface BookFile {
  id: number
  bookId: number
  format: 'ebook' | 'audiobook'
  path: string
  sizeBytes: number
  createdAt: string
}

export interface Book {
  id: number
  foreignBookId: string
  authorId: number
  title: string
  description: string
  imageUrl: string
  releaseDate?: string
  genres: string[]
  monitored: boolean
  status: string
  filePath: string
  mediaType: MediaType
  // Per-format file paths for dual-format books (mediaType='both').
  ebookFilePath: string
  audiobookFilePath: string
  // All on-disk files tracked in book_files (populated on single-book GET).
  bookFiles?: BookFile[]
  excluded: boolean
  narrator?: string
  durationSeconds?: number
  asin?: string
  language?: string
  calibre_id?: number
  author?: Author
}

export interface SearchResult {
  guid: string
  indexerName: string
  title: string
  size: number
  nzbUrl: string
  infoUrl?: string // human-readable indexer detail/release page (open in new tab); absent when the indexer provides none
  grabs: number
  pubDate: string
  protocol: string   // "usenet" or "torrent"
  language?: string  // ISO 639-1 from newznab:attr language (when present)
  mediaType?: string // "ebook" or "audiobook"; set for dual-format book searches
  approved?: boolean
  rejection?: string
}

export interface SearchQueryDebug {
  title?: string
  author?: string
  year?: number
  isbn?: string
  asin?: string
  mediaType?: string
  allowedLanguages?: string[]
  freeText?: string
}

export interface IndexerDebug {
  indexerId: number
  indexerName: string
  enabled: boolean
  skipped?: boolean
  skipReason?: string
  categories?: number[]
  resultCount: number
  durationMs: number
  error?: string
}

export interface PipelineDebug {
  rawCount: number
  afterDedupe: number
  afterUsenetJunk: number
  afterRelevance: number
}

export interface FilterDebug {
  title: string
  indexerName?: string
  stage: string
  reason: string
}

export interface SearchDebug {
  query: SearchQueryDebug
  indexers: IndexerDebug[]
  pipeline: PipelineDebug
  filters: FilterDebug[]
  startedAt: string
  durationMs: number
}

export interface SearchBookResponse {
  results: SearchResult[]
  debug: SearchDebug | null
}

export const booksApi = {
  // Metadata search
  searchAuthors: (term: string) => request<Author[]>(`/search/author?term=${encodeURIComponent(term)}`),
  searchBooks: (term: string) => request<Book[]>(`/search/book?term=${encodeURIComponent(term)}`),
  lookupISBN: (isbn: string) => request<Book>(`/book/lookup?isbn=${encodeURIComponent(isbn)}`),
  lookupASIN: (asin: string) => request<Book>(`/book/lookup?asin=${encodeURIComponent(asin)}`),

  // Add a single book to wanted (adds author silently if new). mediaType
  // optionally forces ebook/audiobook/both; empty keeps the provider value /
  // default.media_type setting (#1397).
  addBook: (data: { foreignBookId: string; foreignAuthorId: string; authorName?: string; searchOnAdd?: boolean; mediaType?: string }) =>
    request<Book>('/author/book', { method: 'POST', body: JSON.stringify(data) }),

  // Fix Match (#1238): move a mis-matched file to a different book. The file is
  // detached from its current book and re-imported into the target's folder.
  reassignFile: (data: { path: string; targetBookId: number; format?: string }) =>
    request<{ id: number }>('/queue/manual-import/reassign', { method: 'POST', body: JSON.stringify(data) }),

  // Books
  listBooks: (params?: { authorId?: number; status?: string; includeExcluded?: boolean; limit?: number; offset?: number; search?: string; mediaType?: string; sort?: string; releaseFrom?: string; releaseBefore?: string }) => {
    const q = new URLSearchParams()
    if (params?.authorId) q.set('authorId', String(params.authorId))
    if (params?.status) q.set('status', params.status)
    if (params?.includeExcluded) q.set('includeExcluded', 'true')
    if (params?.limit !== undefined) q.set('limit', String(params.limit))
    if (params?.offset !== undefined) q.set('offset', String(params.offset))
    if (params?.search) q.set('search', params.search)
    if (params?.mediaType) q.set('mediaType', params.mediaType)
    if (params?.sort) q.set('sort', params.sort)
    if (params?.releaseFrom) q.set('releaseFrom', params.releaseFrom)
    if (params?.releaseBefore) q.set('releaseBefore', params.releaseBefore)
    const qs = q.toString()
    return request<Page<Book>>(`/book${qs ? '?' + qs : ''}`)
  },
  // listAllBooks pages through the server until the full set is collected. For
  // callers that need every book (calendar month, author detail, series book
  // picker) rather than one display page. Heavy on very large libraries by
  // design — prefer paginated listBooks for browse views; use this only when
  // the query itself is bounded (one author, one month, ...).
  listAllBooks: async (params?: { authorId?: number; search?: string; status?: string; mediaType?: string; sort?: string; includeExcluded?: boolean; releaseFrom?: string; releaseBefore?: string }): Promise<Book[]> => {
    const pageSize = 500
    let offset = 0
    const all: Book[] = []
    for (;;) {
      const { items, total } = await booksApi.listBooks({ ...params, limit: pageSize, offset })
      all.push(...items)
      offset += items.length
      if (items.length === 0 || all.length >= total) break
    }
    return all
  },
  getBook: (id: number) => request<Book>(`/book/${id}`),
  updateBook: (id: number, data: Partial<Book>) => request<Book>(`/book/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteBook: (id: number, deleteFiles = false) =>
    request<void>(`/book/${id}${deleteFiles ? '?deleteFiles=true' : ''}`, { method: 'DELETE' }),
  deleteBookFile: (id: number, queryParams = '') => request<Book>(`/book/${id}/file${queryParams}`, { method: 'DELETE' }),
  searchBook: (id: number) => request<SearchBookResponse>(`/book/${id}/search`, { method: 'POST' }),
  getLastSearchDebug: () => request<SearchDebug>(`/search/last-debug`),
  enrichAudiobook: (id: number) => request<Book>(`/book/${id}/enrich-audiobook`, { method: 'POST' }),
  toggleExcluded: (id: number) => request<Book>(`/book/${id}/exclude`, { method: 'PUT' }),
  rebindBook: (id: number, provider: 'openlibrary' | 'hardcover', foreignId: string, force = false) =>
    request<Book>(`/book/${id}/rebind`, { method: 'POST', body: JSON.stringify({ provider, foreign_id: foreignId, force }) }),
}
