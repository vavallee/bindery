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
  // Owner of books/authors synced from this list under multi-user tenancy.
  // null = global (visible to all users); a user id scopes synced content to
  // that Bindery user. Only meaningful for provider-sync lists (e.g. hardcover).
  ownerUserId?: number | null
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

// ImportListSyncStats counts one list pass: books seen, created, already
// tracked, and errored.
export interface ImportListSyncStats {
  total: number
  processed: number
  imported: number
  skipped: number
  failed: number
}

// ImportListSyncProgress is the polled shape of the Hardcover list sync (#1854).
// "Sync now" answers 202 with this snapshot and the sync then runs in the
// background, so the UI polls /importlist/sync/status until running is false.
// The scheduler's own runs report here too (trigger: 'scheduled').
export interface ImportListSyncProgress {
  running: boolean
  listId?: number
  listName?: string
  trigger?: 'manual' | 'scheduled'
  startedAt: string
  finishedAt?: string
  message?: string
  error?: string
  stats: ImportListSyncStats
}

// MigrateResult is one section of an import summary (authors, indexers, ...).
export interface MigrateResult {
  requested?: number
  added?: number
  skipped?: number
  errors?: number
  addedNames?: string[]
  failures?: Record<string, string>
}

export interface ReadarrResult {
  authors?: MigrateResult
  indexers?: MigrateResult
  downloadClients?: MigrateResult
  blocklist?: MigrateResult
}

// ReadarrImportProgress is the polled shape of GET /migrate/readarr/status.
// The upload answers 202 with this snapshot. `running` covers only the database
// import; the catalogue backfill that follows (one author every 3 seconds) is
// reported by the catalogue* fields and keeps going after `running` is false.
// The catalogue fields are optional because an older server omits them, and
// they are in memory only, so a restart zeroes them.
export interface ReadarrImportProgress {
  running: boolean
  startedAt?: string
  finishedAt?: string
  message?: string
  error?: string
  result?: ReadarrResult
  catalogueRunning?: boolean
  catalogueTotal?: number
  catalogueDone?: number
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
  // 202 Accepted — the sync runs in the background; poll importListSyncStatus.
  syncImportList: (id: number) => request<ImportListSyncProgress>(`/importlist/${id}/sync`, { method: 'POST' }),
  importListSyncStatus: () => request<ImportListSyncProgress>('/importlist/sync/status'),
  hardcoverLists: (token?: string) =>
    request<HardcoverListsResponse>('/importlist/hardcover/lists', {
      headers: token ? { Authorization: `Bearer ${token}` } : undefined,
    }),
  uploadMigrate: <T>(endpoint: 'csv' | 'readarr', body: FormData) =>
    uploadFile<T>(`/migrate/${endpoint}`, body),
  readarrImportStatus: () => request<ReadarrImportProgress>('/migrate/readarr/status'),

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
