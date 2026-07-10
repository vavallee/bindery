import { request, uploadFile } from './core'

export interface ImportList {
  id: number
  name: string
  type: string
  url: string
  apiKey: string
  apiKeyConfigured: boolean
  // Provider-side account the list belongs to (#1489): the Hardcover username
  // reported by the token the list was loaded with. Empty for legacy rows.
  account: string
  rootFolderId?: number | null
  qualityProfileId?: number | null
  monitorNew: boolean
  autoAdd: boolean
  enabled: boolean
  // Per-list media type override: 'ebook' | 'audiobook' | 'both', or '' (unset
  // — synced books keep the source-derived media type).
  mediaType: string
  lastSyncAt?: string | null
  createdAt: string
  updatedAt: string
}

export type ImportListUpdate = Partial<ImportList> & {
  clearApiKey?: boolean
}

export interface HardcoverList {
  id: number
  name: string
  slug: string
  booksCount: number
}

// Envelope for GET /importlist/hardcover/lists (#1489): the lists plus the
// username of the account the supplied token belongs to, so the picker can
// keep two accounts' identically-slugged shelves apart.
export interface HardcoverListsResponse {
  account: string
  lists: HardcoverList[]
}

// GoodreadsRow mirrors a parsed Goodreads CSV row returned in a preview.
export interface GoodreadsRow {
  rowNumber: number
  title: string
  author: string
  additionalAuthors?: string
  isbn?: string
  isbn13?: string
  exclusiveShelf: string
  bookshelves?: string
}

export type GoodreadsOutcome = 'resolved' | 'skipped_shelf' | 'skipped_existing' | 'unresolved'

export interface GoodreadsResolvedRow {
  row: GoodreadsRow
  outcome: GoodreadsOutcome
  reason?: string
  matchedBy?: string
}

export interface GoodreadsPreview {
  token: string
  totalRows: number
  resolved: number
  skippedShelf: number
  skippedExisting: number
  unresolved: number
  shelfFilter: string[]
  rows: GoodreadsResolvedRow[]
}

export interface GoodreadsCommitResult {
  added: number
  skipped: number
  failed: number
  failures?: Record<string, string>
}

export const importListsApi = {
  // Import lists
  listImportLists: () => request<ImportList[]>('/importlist'),
  addImportList: (data: Partial<ImportList>) => request<ImportList>('/importlist', { method: 'POST', body: JSON.stringify(data) }),
  updateImportList: (id: number, data: ImportListUpdate) => request<ImportList>(`/importlist/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteImportList: (id: number) => request<void>(`/importlist/${id}`, { method: 'DELETE' }),
  syncImportList: (id: number) => request<{ status: string }>(`/importlist/${id}/sync`, { method: 'POST' }),
  hardcoverLists: (token?: string) =>
    request<HardcoverListsResponse>('/importlist/hardcover/lists', {
      headers: token ? { Authorization: `Bearer ${token}` } : undefined,
    }),
  uploadMigrate: <T>(endpoint: 'csv' | 'readarr', body: FormData) =>
    uploadFile<T>(`/migrate/${endpoint}`, body),

  // Goodreads CSV import — two steps: a dry-run preview that resolves every
  // row, then a commit of the resolved books keyed by the preview token.
  goodreadsPreview: (body: FormData) =>
    uploadFile<GoodreadsPreview>('/migrate/goodreads/preview', body),
  goodreadsCommit: (token: string) =>
    request<GoodreadsCommitResult>('/migrate/goodreads/commit', {
      method: 'POST',
      body: JSON.stringify({ token }),
    }),
}
