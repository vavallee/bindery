const BASE = '/api/v1'

// Pages that render before the user is authenticated — reaching /auth/status
// will 401 in enabled mode before setup, which is expected, and we must not
// try to redirect to /login from the login/setup pages themselves.
const PUBLIC_PATHS = new Set(['/login', '/setup'])

// CSRF double-submit token, fetched once on init and refreshed after login.
let csrfToken = ''

// Read from the bindery_csrf cookie (set by GET /auth/csrf).
function readCSRFCookie(): string {
  const m = document.cookie.match(/(?:^|;\s*)bindery_csrf=([^;]+)/)
  return m ? decodeURIComponent(m[1]) : ''
}

export async function initCSRF(): Promise<void> {
  try {
    const res = await fetch(`${BASE}/auth/csrf`, {
      credentials: 'include',
      headers: { 'X-Requested-With': 'bindery-ui' },
    })
    if (res.ok) {
      const data = await res.json()
      csrfToken = data.csrfToken || readCSRFCookie()
    }
  } catch {
    // Non-fatal — mutations will fail with 403 if still missing.
  }
}

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS'])

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  // Merge caller-supplied headers on top of the defaults so we can't lose
  // the CSRF header if a caller passes their own `headers`.
  const headers = new Headers({
    'Content-Type': 'application/json',
    'X-Requested-With': 'bindery-ui',
  })
  const method = (options?.method ?? 'GET').toUpperCase()
  if (!SAFE_METHODS.has(method) && csrfToken) {
    headers.set('X-CSRF-Token', csrfToken)
  }
  if (options?.headers) {
    new Headers(options.headers).forEach((v, k) => headers.set(k, v))
  }
  const res = await fetch(`${BASE}${path}`, {
    credentials: 'include', // send + accept the session cookie
    ...options,
    headers,
  })
  if (res.status === 401 && !PUBLIC_PATHS.has(window.location.pathname)) {
    // Session expired or missing — punt to login. The router there will
    // bounce to /setup if no user exists yet.
    window.location.href = '/login'
    throw new Error('unauthorized')
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || res.statusText)
  }
  if (res.status === 204) return undefined as unknown as T
  return res.json()
}

export interface AuthStatus {
  authenticated: boolean
  setupRequired: boolean
  username?: string
  role?: string
  mode: 'enabled' | 'local-only' | 'disabled' | 'proxy'
}

export interface ManagedUser {
  id: number
  username: string
  role: string
  email?: string
  displayName?: string
  createdAt: string
}

export interface AuthConfig {
  mode: 'enabled' | 'local-only' | 'disabled'
  apiKey: string
  username: string
}

export interface OidcProvider {
  id: string
  name: string
}

export interface OidcProviderConfig {
  id: string
  name: string
  issuer: string
  client_id: string
  client_secret: string
  scopes: string[]
}

export const api = {
  // System
  health: () => request<{ status: string; version: string }>('/health'),
  status: () => request<{ version: string; commit: string; buildDate: string }>('/system/status'),
  getLogs: (params?: { level?: string; component?: string; from?: string; to?: string; q?: string; limit?: number; offset?: number }) => {
    const p: Record<string, string> = {}
    if (params?.level) p.level = params.level
    if (params?.component) p.component = params.component
    if (params?.from) p.from = params.from
    if (params?.to) p.to = params.to
    if (params?.q) p.q = params.q
    if (params?.limit) p.limit = String(params.limit)
    if (params?.offset) p.offset = String(params.offset)
    const qs = new URLSearchParams(p).toString()
    return request<LogEntry[]>(`/system/logs${qs ? '?' + qs : ''}`)
  },
  getLogLevel: () => request<{ level: string }>('/system/loglevel'),
  setLogLevel: (level: string) =>
    request<{ level: string }>('/system/loglevel', { method: 'PUT', body: JSON.stringify({ level }) }),
  getStorage: () =>
    request<{ downloadDir: string; libraryDir: string; audiobookDir: string }>('/system/storage'),

  // Auth
  authStatus: () => request<AuthStatus>('/auth/status'),
  oidcProviders: () => request<OidcProvider[]>('/auth/oidc/providers'),
  oidcSetProviders: (providers: OidcProviderConfig[]) =>
    request<void>('/auth/oidc/providers', { method: 'PUT', body: JSON.stringify(providers) }),
  authLogin: async (username: string, password: string, rememberMe: boolean) => {
    const res = await request<{ ok: boolean; username: string }>('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password, rememberMe }),
    })
    await initCSRF()
    return res
  },
  authLogout: async () => {
    const res = await request<{ ok: boolean }>('/auth/logout', { method: 'POST' })
    csrfToken = ''
    return res
  },
  authSetup: (username: string, password: string) =>
    request<{ ok: boolean }>('/auth/setup', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  authConfig: () => request<AuthConfig>('/auth/config'),
  authChangePassword: (currentPassword: string, newPassword: string) =>
    request<{ ok: boolean }>('/auth/password', {
      method: 'POST',
      body: JSON.stringify({ currentPassword, newPassword }),
    }),
  authRegenerateApiKey: () =>
    request<{ apiKey: string }>('/auth/apikey/regenerate', { method: 'POST' }),
  authSetMode: (mode: AuthStatus['mode']) =>
    request<{ mode: string }>('/auth/mode', {
      method: 'PUT',
      body: JSON.stringify({ mode }),
    }),
  listUsers: () => request<ManagedUser[]>('/auth/users'),
  createUser: (username: string, password: string, role: string) =>
    request<ManagedUser>('/auth/users', { method: 'POST', body: JSON.stringify({ username, password, role }) }),
  deleteUser: (id: number) => request<{ ok: boolean }>(`/auth/users/${id}`, { method: 'DELETE' }),
  setUserRole: (id: number, role: string) =>
    request<{ ok: boolean }>(`/auth/users/${id}/role`, { method: 'PUT', body: JSON.stringify({ role }) }),
  resetUserPassword: (id: number, password: string) =>
    request<{ ok: boolean }>(`/auth/users/${id}/reset-password`, { method: 'PUT', body: JSON.stringify({ password }) }),

  // Metadata search
  searchAuthors: (term: string) => request<Author[]>(`/search/author?term=${encodeURIComponent(term)}`),
  searchBooks: (term: string) => request<Book[]>(`/search/book?term=${encodeURIComponent(term)}`),
  lookupISBN: (isbn: string) => request<Book>(`/book/lookup?isbn=${encodeURIComponent(isbn)}`),

  // Add a single book to wanted (adds author silently if new)
  addBook: (data: { foreignBookId: string; foreignAuthorId: string; authorName?: string; searchOnAdd?: boolean }) =>
    request<Book>('/author/book', { method: 'POST', body: JSON.stringify(data) }),

  // Authors
  listAuthors: () => request<Author[]>('/author'),
  getAuthor: (id: number) => request<Author>(`/author/${id}`),
  addAuthor: (data: AddAuthorRequest) => request<Author>('/author', { method: 'POST', body: JSON.stringify(data) }),
  updateAuthor: (id: number, data: Partial<Author>) => request<Author>(`/author/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteAuthor: (id: number, deleteFiles = false) =>
    request<void>(`/author/${id}${deleteFiles ? '?deleteFiles=true' : ''}`, { method: 'DELETE' }),
  refreshAuthor: (id: number) => request<void>(`/author/${id}/refresh`, { method: 'POST' }),
  listAuthorAliases: (id: number) => request<AuthorAlias[]>(`/author/${id}/aliases`),
  mergeAuthors: (targetId: number, sourceId: number, overwriteDefaults = true) =>
    request<MergeAuthorsResult>(`/author/${targetId}/merge`, {
      method: 'POST',
      body: JSON.stringify({ sourceId, overwriteDefaults }),
    }),

  // Books
  listBooks: (params?: { authorId?: number; status?: string; includeExcluded?: boolean }) => {
    const q = new URLSearchParams()
    if (params?.authorId) q.set('authorId', String(params.authorId))
    if (params?.status) q.set('status', params.status)
    if (params?.includeExcluded) q.set('includeExcluded', 'true')
    const qs = q.toString()
    return request<Book[]>(`/book${qs ? '?' + qs : ''}`)
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

  // Wanted
  listWanted: (opts?: { includeExcluded?: boolean }) => {
    const qs = opts?.includeExcluded ? '?includeExcluded=true' : ''
    return request<Book[]>(`/wanted/missing${qs}`)
  },

  // Bulk actions
  bulkActionAuthors: (ids: number[], action: AuthorBulkAction, mediaType?: MediaType) =>
    request<BulkResult>('/author/bulk', { method: 'POST', body: JSON.stringify({ ids, action, ...(mediaType ? { mediaType } : {}) }) }),
  bulkActionBooks: (ids: number[], action: BookBulkAction, mediaType?: MediaType) =>
    request<BulkResult>('/book/bulk', { method: 'POST', body: JSON.stringify({ ids, action, ...(mediaType ? { mediaType } : {}) }) }),
  bulkActionWanted: (ids: number[], action: WantedBulkAction) =>
    request<BulkResult>('/wanted/bulk', { method: 'POST', body: JSON.stringify({ ids, action }) }),

  // Indexers
  listIndexers: () => request<Indexer[]>('/indexer'),
  addIndexer: (data: Partial<Indexer>) => request<Indexer>('/indexer', { method: 'POST', body: JSON.stringify(data) }),
  updateIndexer: (id: number, data: Partial<Indexer>) => request<Indexer>(`/indexer/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteIndexer: (id: number) => request<void>(`/indexer/${id}`, { method: 'DELETE' }),
  testIndexer: (id: number) => request<IndexerTestResult>(`/indexer/${id}/test`, { method: 'POST' }),

  // Prowlarr indexer sync
  listProwlarr: () => request<ProwlarrInstance[]>('/prowlarr'),
  addProwlarr: (data: Partial<ProwlarrInstance>) => request<ProwlarrInstance>('/prowlarr', { method: 'POST', body: JSON.stringify(data) }),
  updateProwlarr: (id: number, data: Partial<ProwlarrInstance>) => request<ProwlarrInstance>(`/prowlarr/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteProwlarr: (id: number) => request<void>(`/prowlarr/${id}`, { method: 'DELETE' }),
  testProwlarr: (id: number) => request<{ ok: string; version?: string; error?: string }>(`/prowlarr/${id}/test`, { method: 'POST' }),
  syncProwlarr: (id: number) => request<{ added: number; updated: number; removed: number }>(`/prowlarr/${id}/sync`, { method: 'POST' }),
  searchIndexers: (q: string) => request<SearchResult[]>(`/indexer/search?q=${encodeURIComponent(q)}`),

  // Download clients
  listDownloadClients: () => request<DownloadClient[]>('/downloadclient'),
  addDownloadClient: (data: Partial<DownloadClient>) => request<DownloadClient>('/downloadclient', { method: 'POST', body: JSON.stringify(data) }),
  updateDownloadClient: (id: number, data: Partial<DownloadClient>) => request<DownloadClient>(`/downloadclient/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteDownloadClient: (id: number) => request<void>(`/downloadclient/${id}`, { method: 'DELETE' }),
  testDownloadClient: (id: number) => request<{ message: string }>(`/downloadclient/${id}/test`, { method: 'POST' }),

  // Library
  triggerLibraryScan: () => request<{ message: string }>('/library/scan', { method: 'POST' }),
  libraryScanStatus: () => request<{ ran_at: string; files_found: number; reconciled: number; unmatched: number }>('/library/scan/status'),

  // Queue
  listQueue: () => request<QueueItem[]>('/queue'),
  grab: (data: GrabRequest) => request<Download>('/queue/grab', { method: 'POST', body: JSON.stringify(data) }),
  deleteFromQueue: (id: number) => request<void>(`/queue/${id}`, { method: 'DELETE' }),

  // Pending releases
  listPending: () => request<PendingRelease[]>('/pending'),
  dismissPending: (id: number) => request<void>(`/pending/${id}`, { method: 'DELETE' }),
  grabPending: (id: number) => request<Download>(`/pending/${id}/grab`, { method: 'POST' }),

  // History
  listHistory: (params?: { bookId?: number; eventType?: string }) => {
    const q = new URLSearchParams()
    if (params?.bookId) q.set('bookId', String(params.bookId))
    if (params?.eventType) q.set('eventType', params.eventType)
    const qs = q.toString()
    return request<HistoryEvent[]>(`/history${qs ? '?' + qs : ''}`)
  },
  deleteHistory: (id: number) => request<void>(`/history/${id}`, { method: 'DELETE' }),
  blocklistFromHistory: (id: number) => request<BlocklistEntry>(`/history/${id}/blocklist`, { method: 'POST' }),

  // Blocklist
  listBlocklist: () => request<BlocklistEntry[]>('/blocklist'),
  deleteBlocklistEntry: (id: number) => request<void>(`/blocklist/${id}`, { method: 'DELETE' }),
  bulkDeleteBlocklist: (ids: number[]) => request<void>('/blocklist/bulk', { method: 'DELETE', body: JSON.stringify({ ids }) }),

  // Notifications
  listNotifications: () => request<NotificationConfig[]>('/notification'),
  addNotification: (data: Partial<NotificationConfig>) => request<NotificationConfig>('/notification', { method: 'POST', body: JSON.stringify(data) }),
  updateNotification: (id: number, data: Partial<NotificationConfig>) => request<NotificationConfig>(`/notification/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteNotification: (id: number) => request<void>(`/notification/${id}`, { method: 'DELETE' }),
  testNotification: (id: number) => request<{ message: string }>(`/notification/${id}/test`, { method: 'POST' }),

  // Quality Profiles
  listQualityProfiles: () => request<QualityProfile[]>('/qualityprofile'),

  // Series
  listSeries: () => request<Series[]>('/series'),
  getSeries: (id: number) => request<Series>(`/series/${id}`),
  monitorSeries: (id: number, monitored: boolean) => request<{ monitored: boolean }>(`/series/${id}`, { method: 'PATCH', body: JSON.stringify({ monitored }) }),
  fillSeries: (id: number) => request<{ queued: number }>(`/series/${id}/fill`, { method: 'POST' }),

  // Tags
  listTags: () => request<Tag[]>('/tag'),
  addTag: (name: string) => request<Tag>('/tag', { method: 'POST', body: JSON.stringify({ name }) }),
  deleteTag: (id: number) => request<void>(`/tag/${id}`, { method: 'DELETE' }),

  // Settings
  listSettings: () => request<Array<{ key: string; value: string }>>('/setting'),
  getSetting: (key: string) => request<{ key: string; value: string }>(`/setting/${key}`),
  setSetting: (key: string, value: string) => request<void>(`/setting/${key}`, { method: 'PUT', body: JSON.stringify({ value }) }),

  // Backup
  listBackups: () => request<string[]>('/backup'),
  createBackup: () => request<{ filename: string }>('/backup', { method: 'POST' }),

  // Calibre
  testCalibre: () => request<CalibreTestResult>('/calibre/test', { method: 'POST' }),
  calibreTestPaths: () => request<{ ok: string; message: string }>('/calibre/test-paths', { method: 'POST' }),
  calibreImportStart: () => request<CalibreImportProgress>('/calibre/import', { method: 'POST' }),
  calibreImportStatus: () => request<CalibreImportProgress>('/calibre/import/status'),
  calibreSyncStart: () => request<CalibreSyncProgress>('/calibre/sync', { method: 'POST' }),
  calibreSyncStatus: () => request<CalibreSyncProgress>('/calibre/sync/status'),

  // Import lists
  listImportLists: () => request<ImportList[]>('/importlist'),
  addImportList: (data: Partial<ImportList>) => request<ImportList>('/importlist', { method: 'POST', body: JSON.stringify(data) }),
  updateImportList: (id: number, data: Partial<ImportList>) => request<ImportList>(`/importlist/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteImportList: (id: number) => request<void>(`/importlist/${id}`, { method: 'DELETE' }),
  hardcoverLists: (token: string) =>
    request<HardcoverList[]>('/importlist/hardcover/lists', {
      headers: { Authorization: `Bearer ${token}` },
    }),

  // Metadata Profiles
  listMetadataProfiles: () => request<MetadataProfile[]>('/metadataprofile'),
  addMetadataProfile: (data: Partial<MetadataProfile>) => request<MetadataProfile>('/metadataprofile', { method: 'POST', body: JSON.stringify(data) }),
  updateMetadataProfile: (id: number, data: Partial<MetadataProfile>) => request<MetadataProfile>(`/metadataprofile/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteMetadataProfile: (id: number) => request<void>(`/metadataprofile/${id}`, { method: 'DELETE' }),

  // Delay Profiles
  listDelayProfiles: () => request<DelayProfile[]>('/delayprofile'),
  addDelayProfile: (data: Partial<DelayProfile>) => request<DelayProfile>('/delayprofile', { method: 'POST', body: JSON.stringify(data) }),
  deleteDelayProfile: (id: number) => request<void>(`/delayprofile/${id}`, { method: 'DELETE' }),

  // Custom Formats
  listCustomFormats: () => request<CustomFormat[]>('/customformat'),
  addCustomFormat: (data: Partial<CustomFormat>) => request<CustomFormat>('/customformat', { method: 'POST', body: JSON.stringify(data) }),
  deleteCustomFormat: (id: number) => request<void>(`/customformat/${id}`, { method: 'DELETE' }),

  // Root Folders
  listRootFolders: () => request<RootFolder[]>('/rootfolder'),
  addRootFolder: (path: string) => request<RootFolder>('/rootfolder', { method: 'POST', body: JSON.stringify({ path }) }),
  deleteRootFolder: (id: number) => request<void>(`/rootfolder/${id}`, { method: 'DELETE' }),

  // Recommendations
  listRecommendations: (params?: { type?: string; limit?: number; offset?: number }) => {
    const q = new URLSearchParams()
    if (params?.type) q.set('type', params.type)
    if (params?.limit) q.set('limit', String(params.limit))
    if (params?.offset) q.set('offset', String(params.offset))
    const qs = q.toString()
    return request<Recommendation[]>(`/recommendations${qs ? '?' + qs : ''}`)
  },
  dismissRecommendation: (id: number) => request<void>(`/recommendations/${id}/dismiss`, { method: 'POST' }),
  addRecommendation: (id: number) => request<void>(`/recommendations/${id}/add`, { method: 'POST' }),
  refreshRecommendations: () => request<void>('/recommendations/refresh', { method: 'POST' }),
  clearRecommendationDismissals: () => request<void>('/recommendations/dismissals', { method: 'DELETE' }),
  listAuthorExclusions: () => request<string[]>('/recommendations/exclude-author'),
  addAuthorExclusion: (authorName: string) => request<void>('/recommendations/exclude-author', { method: 'POST', body: JSON.stringify({ authorName }) }),
  removeAuthorExclusion: (authorName: string) => request<void>(`/recommendations/exclude-author/${encodeURIComponent(authorName)}`, { method: 'DELETE' }),
}

// Types
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
  qualityProfileId?: number | null
  metadataProfileId?: number | null
  rootFolderId?: number | null
  books?: Book[]
  statistics?: { bookCount: number; availableBookCount: number; wantedBookCount: number }
  aliases?: AuthorAlias[]
}

export interface AuthorAlias {
  id: number
  authorId: number
  name: string
  sourceOlId?: string
  createdAt: string
}

export interface MergeAuthorsResult {
  BooksReparented: number
  AliasesMigrated: number
  AliasesCreated: number
  TargetUpdated: boolean
}

export type MediaType = 'ebook' | 'audiobook' | 'both'

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

// CalibreMode selects which integration flow runs after a successful
// Bindery import. 'off' skips Calibre entirely, 'calibredb' shells out to
// the calibredb CLI, 'plugin' posts to the Bindery Bridge Calibre plugin.
export type CalibreMode = 'off' | 'calibredb' | 'plugin'

// CalibreSettings mirrors the `calibre.*` keys stored in the settings table.
export interface CalibreSettings {
  calibre_mode: CalibreMode
  calibre_library_path: string
  calibre_binary_path: string
}

export interface CalibreTestResult {
  ok: string
  version: string
  message: string
}

// CalibreImportStats summarises one completed library import. Present
// only on the final poll (when progress.running flips false).
export interface CalibreImportStats {
  authorsAdded: number
  authorsLinked: number
  booksAdded: number
  booksUpdated: number
  editionsAdded: number
  duplicatesMerged: number
  skipped: number
}

// CalibreImportProgress is the polled shape for /calibre/import/status.
// The UI renders a progress bar from total/processed, swaps in the stats
// summary once running=false, and surfaces any error inline.
export interface CalibreImportProgress {
  running: boolean
  startedAt?: string
  finishedAt?: string
  total: number
  processed: number
  message?: string
  error?: string
  stats?: CalibreImportStats
}

// CalibreSyncError is one failed push entry returned by /calibre/sync/status.
export interface CalibreSyncError {
  bookId: number
  title: string
  path?: string
  reason: string
}

// CalibreSyncStats summarises one bulk-push run. Pushed = newly added;
// alreadyInCalibre = 409 Conflict (treated as success for idempotency);
// failed = everything else.
export interface CalibreSyncStats {
  total: number
  processed: number
  pushed: number
  alreadyInCalibre: number
  failed: number
}

// CalibreSyncProgress is the polled shape for /calibre/sync/status.
export interface CalibreSyncProgress {
  running: boolean
  startedAt?: string
  finishedAt?: string
  message?: string
  error?: string
  stats: CalibreSyncStats
  errors: CalibreSyncError[]
}

export interface Indexer {
  id: number
  name: string
  type: string
  url: string
  apiKey: string
  categories: number[]
  enabled: boolean
  prowlarrInstanceId?: number
}

export interface IndexerTestResult {
  ok: boolean
  status: number
  categories: number
  bookSearch: boolean
  latencyMs: number
  searchResults: number
  searchError?: string
  message?: string
  error?: string
}

export interface PendingRelease {
  id: number
  bookId: number
  title: string
  indexerId?: number
  guid: string
  protocol: string
  size: number
  ageMinutes: number
  quality?: string
  customScore: number
  reason: string
  firstSeen: string
  releaseJson: string
}

export interface ProwlarrInstance {
  id: number
  name: string
  url: string
  apiKey: string
  syncOnStartup: boolean
  enabled: boolean
  lastSyncAt?: string
}

export interface DownloadClient {
  id: number
  name: string
  type: string
  host: string
  port: number
  apiKey: string
  username: string
  password: string
  useSsl: boolean
  urlBase: string
  category: string
  enabled: boolean
}

export interface Download {
  id: number
  guid: string
  title: string
  status: string
  size: number
  protocol: string
  errorMessage: string
}

export interface QueueItem extends Download {
  percentage?: string
  timeLeft?: string
}

export interface SearchResult {
  guid: string
  indexerName: string
  title: string
  size: number
  nzbUrl: string
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

export interface AddAuthorRequest {
  foreignAuthorId: string
  authorName: string
  monitored: boolean
  searchOnAdd: boolean
  metadataProfileId?: number | null
  qualityProfileId?: number | null
  rootFolderId?: number | null
  mediaType?: MediaType
}

export interface GrabRequest {
  guid: string
  title: string
  nzbUrl: string
  size: number
  bookId?: number
  indexerId?: number
  protocol?: string
  mediaType?: string
}

export interface HistoryEvent {
  id: number
  bookId?: number
  eventType: string
  sourceTitle: string
  data: string
  createdAt: string
}

export interface BlocklistEntry {
  id: number
  bookId?: number
  guid: string
  title: string
  indexerId?: number
  reason: string
  createdAt: string
}

export interface NotificationConfig {
  id: number
  name: string
  type: string
  url: string
  method: string
  headers: string
  onGrab: boolean
  onImport: boolean
  onUpgrade: boolean
  onFailure: boolean
  onHealth: boolean
  enabled: boolean
}

export interface QualityProfile {
  id: number
  name: string
  upgradeAllowed: boolean
  cutoff: string
  items: Array<{ quality: string; allowed: boolean }>
}

export interface Series {
  id: number
  foreignSeriesId: string
  title: string
  description: string
  monitored: boolean
  books?: Array<{
    seriesId: number
    bookId: number
    positionInSeries: string
    book?: Book
  }>
}

export interface Tag {
  id: number
  name: string
}

export interface MetadataProfile {
  id: number
  name: string
  minPopularity: number
  minPages: number
  skipMissingDate: boolean
  skipMissingIsbn: boolean
  skipPartBooks: boolean
  allowedLanguages: string
  unknownLanguageBehavior: 'pass' | 'fail'
}

export interface DelayProfile {
  id: number
  usenetDelay: number
  torrentDelay: number
  preferredProtocol: string
  enableUsenet: boolean
  enableTorrent: boolean
  order: number
}

export interface CustomFormat {
  id: number
  name: string
  conditions: Array<{
    type: string
    pattern: string
    negate: boolean
    required: boolean
  }>
}

export interface RootFolder {
  id: number
  path: string
  freeSpace: number
  createdAt: string
}

export interface ImportList {
  id: number
  name: string
  type: string
  url: string
  apiKey: string
  rootFolderId?: number | null
  qualityProfileId?: number | null
  monitorNew: boolean
  autoAdd: boolean
  enabled: boolean
  lastSyncAt?: string | null
  createdAt: string
  updatedAt: string
}

export interface HardcoverList {
  id: number
  name: string
  slug: string
  booksCount: number
}

export interface LogEntry {
  // Ring buffer shape
  time?: string
  msg?: string
  attrs?: Record<string, string>
  // DB shape
  id?: number
  ts?: string
  level: 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'
  component?: string
  message?: string
  fields?: Record<string, string>
}

export interface Recommendation {
  id: number
  userId: number
  foreignId: string
  recType: string
  title: string
  authorName: string
  authorId?: number
  imageUrl: string
  description: string
  genres: string[]
  rating: number
  ratingsCount: number
  releaseDate?: string
  language: string
  mediaType: string
  score: number
  reason: string
  seriesId?: number
  seriesPos: string
  dismissed: boolean
  batchId: string
  createdAt: string
}

export type AuthorBulkAction = 'monitor' | 'unmonitor' | 'delete' | 'search' | 'set_media_type'
export type BookBulkAction = 'monitor' | 'unmonitor' | 'delete' | 'search' | 'set_media_type'
export type WantedBulkAction = 'search' | 'blocklist' | 'unmonitor'

export interface BulkResult {
  results: Record<string, { ok: boolean; error?: string }>
}
