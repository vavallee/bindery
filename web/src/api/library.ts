import { request } from './core'

// Header library search (#2551): the caller's own catalogue, grouped, a few
// rows per group. Rows are deliberately thin; open the entity for the rest.
export interface LibrarySearchAuthor {
  id: number
  name: string
  imageUrl?: string
}

export interface LibrarySearchBook {
  id: number
  title: string
  authorId: number
  authorName?: string
  imageUrl?: string
}

export interface LibrarySearchSeries {
  id: number
  title: string
}

export interface LibrarySearchResponse {
  authors: LibrarySearchAuthor[]
  books: LibrarySearchBook[]
  series: LibrarySearchSeries[]
}

export const libraryApi = {
  // Library search (local catalogue only; the metadata search is searchBooks / searchAuthors)
  searchLibrary: (q: string, limit?: number) => {
    const params = new URLSearchParams({ q })
    if (limit) params.set('limit', String(limit))
    return request<LibrarySearchResponse>(`/search/library?${params.toString()}`)
  },
  // Library
  triggerLibraryScan: () => request<{ message: string }>('/library/scan', { method: 'POST' }),
  libraryScanStatus: () => request<{
    ran_at: string
    files_found: number
    reconciled: number
    unmatched: number
    tag_read_failed?: number
    // reason is the scanner's per-file diagnostic (#1958): 'author_not_in_library',
    // 'no_candidate_books', 'no_title_match' or 'no_title_parsed'. Optional —
    // older cached scan results were written before it existed.
    unmatched_files?: Array<{ path: string; parsed_title: string; parsed_author: string; reason?: string }>
    // Additive (feat/library-scan-visibility): the resolved roots the scan
    // walked and an explicit zero-files signal. Optional so older cached scan
    // results (persisted before these fields existed) still parse.
    library_dir?: string
    audiobook_dir?: string
    scanned_paths?: string[]
    no_files_found?: boolean
    // #965: non-empty when the scan could not complete (library dir unset, or
    // the book listing failed). Optional so older cached results still parse.
    scan_error?: string
  }>('/library/scan/status'),
}
