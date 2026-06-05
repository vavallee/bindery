import { request, ApiError } from './core'
import type { Book } from './books'
import type { Series } from './series'
import type { Page } from './common'

export interface Author {
  id: number
  foreignAuthorId: string
  authorName: string
  sortName: string
  description: string
  imageUrl: string
  disambiguation: string
  ratingsCount: number
  averageRating: number
  monitored: boolean
  metadataProvider?: string
  monitorMode?: AuthorMonitorMode
  monitorLatestCount?: number
  qualityProfileId?: number | null
  metadataProfileId?: number | null
  rootFolderId?: number | null
  audiobookRootFolderId?: number | null
  books?: Book[]
  statistics?: { bookCount: number; availableBookCount: number; wantedBookCount: number }
  aliases?: AuthorAlias[]
  // Populated by the author Get response when monitorMode === 'series' (#810).
  // The Update endpoint accepts an updated array via UpdateAuthorRequest.
  monitoredSeriesIds?: number[]
}

export interface AuthorAlias {
  id: number
  authorId: number
  name: string
  sourceOlId?: string
  createdAt: string
}

export interface AuthorConflictBody {
  error?: string
  canonicalAuthorId?: number
  canonicalAuthor?: Author
}

export interface RelinkAuthorCandidate {
  foreignAuthorId: string
  authorName?: string
}

export interface MergeAuthorsResult {
  BooksReparented: number
  AliasesMigrated: number
  AliasesCreated: number
  TargetUpdated: boolean
}

export type MediaType = 'ebook' | 'audiobook' | 'both'
export type AuthorMonitorMode = 'all' | 'future' | 'latest' | 'none' | 'series'

export interface AddAuthorRequest {
  foreignAuthorId: string
  authorName: string
  monitored: boolean
  monitorMode?: AuthorMonitorMode
  monitorLatestCount?: number
  searchOnAdd: boolean
  metadataProfileId?: number | null
  qualityProfileId?: number | null
  rootFolderId?: number | null
  mediaType?: MediaType
}

// UpdateAuthorRequest is a partial author patch. clearAudiobookRootFolder is a
// separate flag because a null audiobookRootFolderId is ambiguous over the wire
// (omitted vs. cleared): the backend only resets the per-author audiobook root
// folder when this flag is true.
export interface UpdateAuthorRequest {
  monitored?: boolean
  monitorMode?: AuthorMonitorMode
  monitorLatestCount?: number
  qualityProfileId?: number | null
  metadataProfileId?: number | null
  rootFolderId?: number | null
  audiobookRootFolderId?: number | null
  clearAudiobookRootFolder?: boolean
  applyMonitorModeToExisting?: boolean
  // monitoredSeriesIds is the per-author series pin set (#810). Only valid
  // when monitorMode === 'series'. Backend rejects ids that don't belong to
  // this author.
  monitoredSeriesIds?: number[]
}

export const authorsApi = {
  // Authors
  listAuthors: (params?: { limit?: number; offset?: number }) => {
    const q = new URLSearchParams()
    if (params?.limit !== undefined) q.set('limit', String(params.limit))
    if (params?.offset !== undefined) q.set('offset', String(params.offset))
    const qs = q.toString()
    return request<Page<Author>>(`/author${qs ? '?' + qs : ''}`)
  },
  getAuthor: (id: number) => request<Author>(`/author/${id}`),
  addAuthor: (data: AddAuthorRequest) => request<Author>('/author', { method: 'POST', body: JSON.stringify(data) }),
  updateAuthor: (id: number, data: UpdateAuthorRequest) => request<Author>(`/author/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteAuthor: (id: number, deleteFiles = false) =>
    request<void>(`/author/${id}${deleteFiles ? '?deleteFiles=true' : ''}`, { method: 'DELETE' }),
  refreshAuthor: (id: number) => request<void>(`/author/${id}/refresh`, { method: 'POST' }),
  searchAuthorLinkCandidates: (id: number, term: string) =>
    request<Author[]>(`/author/${id}/relink-upstream/candidates?term=${encodeURIComponent(term)}`),
  relinkAuthorUpstream: (id: number, candidate?: RelinkAuthorCandidate) =>
    request<Author>(`/author/${id}/relink-upstream`, {
      method: 'POST',
      body: candidate ? JSON.stringify(candidate) : undefined,
    }),
  listAuthorAliases: (id: number) => request<AuthorAlias[]>(`/author/${id}/aliases`),
  // listAuthorSeries returns the series the author has books in. Backs the
  // per-author monitor-by-series picker in EditAuthorModal (#810).
  listAuthorSeries: (id: number) => request<Series[]>(`/author/${id}/series`),
  mergeAuthors: (targetId: number, sourceId: number, overwriteDefaults = true) =>
    request<MergeAuthorsResult>(`/author/${targetId}/merge`, {
      method: 'POST',
      body: JSON.stringify({ sourceId, overwriteDefaults }),
    }),

  // Refresh metadata for ALL authors (background job, #863). Distinct from the
  // per-selection bulkActionAuthors('refresh'): this enumerates every author and
  // refreshes sequentially with progress that survives a page reload.
  refreshAllAuthors: () => request<{ message: string }>('/authors/refresh-all', { method: 'POST' }),
  // Returns null when no refresh has ever run (the backend serves 404). A
  // stored "running" status is reconciled to "failed" server-side after a
  // restart so the UI banner never hangs.
  refreshAllAuthorsStatus: () =>
    request<AuthorRefreshStatus>('/authors/refresh-all/status').catch((err) => {
      if (err instanceof ApiError && err.status === 404) return null
      throw err
    }),
}

// Progress for the "refresh all authors" background job (#863). status is
// "running" while the job iterates authors, "completed" when done, or "failed"
// (e.g. the author list could not be loaded, or the server restarted mid-job).
export interface AuthorRefreshStatus {
  status: 'running' | 'completed' | 'failed'
  total: number
  done: number
  failed: number
  started_at: string
  completed_at?: string
  message?: string
}
