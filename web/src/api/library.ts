import { request } from './core'
import type { DuplicateCandidateGroup } from './authors'

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

// Library-wide duplicate review (#2999): every author's duplicate groups,
// paginated, with the same shape the per-author window returns.
export interface LibraryDuplicateCandidates {
  groups: DuplicateCandidateGroup[]
  total: number
  count: number
  limit: number
  offset: number
}

export const libraryApi = {
  listLibraryDuplicateCandidates: (limit: number, offset: number) =>
    request<LibraryDuplicateCandidates>(`/library/duplicate-candidates?limit=${limit}&offset=${offset}`),
  // Library search (local catalogue only; the metadata search is searchBooks / searchAuthors)
  searchLibrary: (q: string, limit?: number) => {
    const params = new URLSearchParams({ q })
    if (limit) params.set('limit', String(limit))
    return request<LibrarySearchResponse>(`/search/library?${params.toString()}`)
  },
  // Library
  // queued is true when a scan was already running: the request is not
  // dropped, one more scan runs as soon as that one finishes (#3014).
  // scanId is the scan_id the requested scan's result will carry in
  // libraryScanStatus, so a caller can recognise it without comparing clocks.
  triggerLibraryScan: () => request<{ message: string; queued?: boolean; scanId?: string }>('/library/scan', { method: 'POST' }),
  libraryScanStatus: () => request<{
    ran_at: string
    // Which scan produced this result, and whether a scan is walking or
    // queued right now (#3014).
    scan_id?: string
    running?: boolean
    queued?: boolean
    files_found: number
    reconciled: number
    unmatched: number
    tag_read_failed?: number
    // Books the scan could not match (library adoption, /library/unmatched)
    // and how many of them are ignored. unmatched_files is always empty now
    // and kept only so a cached older bundle still parses.
    unmatched_units?: number
    ignored_units?: number
    units_truncated?: boolean
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
