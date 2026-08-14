import { request } from './core'

export const libraryApi = {
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
