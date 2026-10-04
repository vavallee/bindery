import { request } from './core'

// Library adoption (#2547): the books a library scan could not match, grouped
// one row per book, and the decisions an admin makes about them. Every route
// is admin only. No request carries a path; rows are addressed by id.

export type AdoptionState = 'pending' | 'adopted' | 'ignored'

export type AdoptionReason =
  | 'author_not_in_library'
  | 'no_candidate_books'
  | 'no_title_match'
  | 'no_title_parsed'
  // An ebook format file under 4 KiB: a notes or readme file, not a book. It
  // has no suggestions and the server refuses to adopt it (#2944).
  | 'too_small'

export interface AdoptionBookRef {
  id: number
  title: string
  authorId: number
  authorName: string
  imageUrl?: string
  status: string
  mediaType: string
  monitored: boolean
}

export interface AdoptionCandidate {
  book: AdoptionBookRef
  score: number
  // Set on a row with an author conflict for a book that is not by the author
  // the files name: a look alike from the folder's author (#2942).
  folderAuthorOnly?: boolean
}

// The authors that disagree when a row's files (tags or names) name someone
// other than the author folder they sit in (#2942).
export interface AdoptionAuthorConflict {
  files: string
  folder: string
}

export interface AdoptionItem {
  id: number
  kind: 'file' | 'folder'
  format: 'ebook' | 'audiobook'
  fileCount: number
  sizeBytes: number
  relPath: string
  rootPath: string
  authorFolder: string
  parsedTitle: string
  parsedAuthor: string
  reason: AdoptionReason | ''
  authorConflict?: AdoptionAuthorConflict
  // The format the row's library root holds, set only when the ebook and
  // audiobook roots are separate folders. A row whose format differs sits in
  // the other format's folder (#2944).
  rootFormat?: 'ebook' | 'audiobook'
  candidates: AdoptionCandidate[]
  topScore: number
  state: AdoptionState | 'adopting' | 'undoing'
  book?: AdoptionBookRef
  bookCreated: boolean
  authorCreated: boolean
  members: string[]
  firstSeenAt: string
  resolvedAt?: string
  // Set when an outcome needs explaining, such as Undo keeping a book that
  // has been used since the adoption added it.
  message?: string
}

export interface AdoptionFacetCount {
  value: string
  count: number
}

export interface AdoptionFolderFacet {
  folder: string
  units: number
  files: number
  notInLibrary: number
  author: string
}

export interface AdoptionFacets {
  reasons: AdoptionFacetCount[]
  formats: AdoptionFacetCount[]
  folders: AdoptionFolderFacet[]
}

export interface AdoptionSummary {
  pending: number
  pendingFiles: number
  ignored: number
  adopted: number
}

export interface AdoptionScanStatus {
  ran: boolean
  ranAt?: string
  running: boolean
  filesFound: number
  truncated: boolean
  error?: string
  noFilesFound: boolean
}

export interface AdoptionListResponse {
  items: AdoptionItem[]
  total: number
  facets?: AdoptionFacets
  summary: AdoptionSummary
  scan: AdoptionScanStatus
}

export type AdoptionSort = 'score' | 'title' | 'folder' | 'files' | 'size' | 'seen'

export interface AdoptionListParams {
  state?: AdoptionState
  reason?: string
  authorFolder?: string
  format?: string
  search?: string
  sort?: AdoptionSort
  dir?: 'asc' | 'desc'
  limit?: number
  offset?: number
  facets?: boolean
}

// What a row is adopted as: a book already in the library, or a metadata
// result the server adds (unmonitored, in the adopted format) first.
export type AdoptTarget =
  | { bookId: number; format?: string }
  | { foreignBookId: string; foreignAuthorId: string; authorName: string; format?: string }

export const adoptionApi = {
  listUnmatched: (p: AdoptionListParams = {}) => {
    const q = new URLSearchParams()
    for (const [k, v] of Object.entries(p)) {
      if (v === undefined || v === '' || v === false) continue
      q.set(k, v === true ? '1' : String(v))
    }
    const qs = q.toString()
    return request<AdoptionListResponse>(`/library/unmatched${qs ? `?${qs}` : ''}`)
  },
  unmatchedSummary: () =>
    request<AdoptionSummary & { scan: AdoptionScanStatus }>('/library/unmatched/summary'),
  adoptUnit: (id: number, target: AdoptTarget) =>
    request<AdoptionItem>(`/library/unmatched/${id}/adopt`, { method: 'POST', body: JSON.stringify(target) }),
  undoAdoption: (id: number) =>
    request<AdoptionItem>(`/library/unmatched/${id}/undo`, { method: 'POST' }),
  ignoreUnit: (id: number) =>
    request<AdoptionItem>(`/library/unmatched/${id}/ignore`, { method: 'POST' }),
  unignoreUnit: (id: number) =>
    request<AdoptionItem>(`/library/unmatched/${id}/unignore`, { method: 'POST' }),
  ignoreFolder: (authorFolder: string) =>
    request<{ ignored: number }>('/library/unmatched/ignore', { method: 'POST', body: JSON.stringify({ authorFolder }) }),
}
