// Path prefix injected by the backend at serve time (empty for root-mounted
// deploys). Read once at module load so all API calls and redirects use a
// consistent value throughout the session.
const BINDERY_BASE: string = (window as unknown as { __BINDERY_BASE__?: string }).__BINDERY_BASE__ ?? ''
const BASE = `${BINDERY_BASE}/api/v1`

// Pages that render before the user is authenticated — reaching /auth/status
// will 401 in enabled mode before setup, which is expected, and we must not
// try to redirect to /login from the login/setup pages themselves.
const PUBLIC_PATHS = new Set([`${BINDERY_BASE}/login`, `${BINDERY_BASE}/setup`])

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

// Error thrown for any non-OK API response. Carries the HTTP status and the
// parsed JSON body so callers can branch on structured fields (e.g. a 409
// rebind conflict exposing `force_required`). Stays an `Error` subclass with a
// human-readable `.message`, so callers that only read `.message` are
// unaffected.
export class ApiError extends Error {
  status: number
  body: { error?: string; [key: string]: unknown }

  constructor(status: number, body: { error?: string; [key: string]: unknown }, fallback: string) {
    super(body.error || fallback)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }
}

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
    window.location.href = `${BINDERY_BASE}/login`
    throw new Error('unauthorized')
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new ApiError(res.status, err, res.statusText)
  }
  if (res.status === 204) return undefined as unknown as T
  return res.json()
}

// Upload a file (multipart/form-data). Omits Content-Type so the browser
// sets the boundary automatically, but still injects the CSRF token.
async function uploadFile<T>(path: string, body: FormData): Promise<T> {
  const headers = new Headers({
    'X-Requested-With': 'bindery-ui',
  })
  if (csrfToken) {
    headers.set('X-CSRF-Token', csrfToken)
  }
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST',
    credentials: 'include',
    headers,
    body,
  })
  if (res.status === 401 && !PUBLIC_PATHS.has(window.location.pathname)) {
    window.location.href = `${BINDERY_BASE}/login`
    return Promise.reject(new Error('unauthenticated'))
  }
  if (!res.ok) {
    const data = await res.json().catch(() => ({ error: res.statusText }))
    throw new ApiError(res.status, data, `HTTP ${res.status}`)
  }
  return res.json() as Promise<T>
}

export interface AuthStatus {
  authenticated: boolean
  setupRequired: boolean
  username?: string
  role?: string
  mode: 'enabled' | 'local-only' | 'disabled' | 'proxy'
  localAuthEnabled: boolean
}

export interface ManagedUser {
  id: number
  username: string
  role: string
  email?: string
  displayName?: string
  createdAt: string
}

export interface SystemStatus {
  version: string
  commit: string
  buildDate: string
  imageCacheBytes?: number
  enhancedHardcoverApi: boolean
  hardcoverTokenConfigured: boolean
  enhancedHardcoverDisabledReason?: 'env_disabled' | 'missing_token' | 'admin_disabled' | string
}

export interface AuthConfig {
  mode: 'enabled' | 'local-only' | 'disabled' | 'proxy'
  apiKey: string
  username: string
}

export interface OidcProviderStatus {
  state: 'ok' | 'failed'
  last_error?: string
  last_attempt?: string
}

export interface OidcProvider {
  id: string
  name: string
  // Optional runtime status. Present on responses from a backend that
  // tracks failed-discovery state; absent for older backends.
  status?: OidcProviderStatus
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
  status: () => request<SystemStatus>('/system/status'),
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
    request<{ downloadDir: string; audiobookDownloadDir: string; libraryDir: string; audiobookDir: string }>('/system/storage'),

  // Auth
  authStatus: () => request<AuthStatus>('/auth/status'),
  oidcProviders: () => request<OidcProvider[]>('/auth/oidc/providers'),
  oidcSetProviders: (providers: OidcProviderConfig[]) =>
    request<void>('/auth/oidc/providers', { method: 'PUT', body: JSON.stringify(providers) }),
  // Returns the public base URL Bindery will use as the prefix for OIDC
  // callback URLs (resolved from the current request) plus the path template
  // with `{id}` placeholder. The settings UI uses these to live-render the
  // redirect URI as the admin types the provider id.
  oidcRedirectBase: () => request<{ base: string; callback_path: string; configured: boolean }>('/auth/oidc/redirect-base'),
  // Probes <issuer>/.well-known/openid-configuration server-side. ok=false
  // means the IdP is unreachable / wrong / not OIDC; the error string is
  // safe to render directly. issuer_mismatch=true is the silent killer for
  // Authentik per-provider mode and Keycloak realms.
  oidcTestDiscovery: (issuer: string) =>
    request<{
      ok: boolean
      error?: string
      issuer_mismatch?: boolean
      discovered?: {
        issuer: string
        authorization_endpoint: string
        token_endpoint: string
        userinfo_endpoint?: string
        jwks_uri?: string
        scopes_supported?: string[]
      }
    }>('/auth/oidc/test-discovery', { method: 'POST', body: JSON.stringify({ issuer }) }),
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
  authRotateSessionSecret: () =>
    request<{ ok: boolean }>('/auth/session-secret/rotate', { method: 'POST' }),
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
  relinkAuthorUpstream: (id: number) => request<Author>(`/author/${id}/relink-upstream`, { method: 'POST' }),
  listAuthorAliases: (id: number) => request<AuthorAlias[]>(`/author/${id}/aliases`),
  // listAuthorSeries returns the series the author has books in. Backs the
  // per-author monitor-by-series picker in EditAuthorModal (#810).
  listAuthorSeries: (id: number) => request<Series[]>(`/author/${id}/series`),
  mergeAuthors: (targetId: number, sourceId: number, overwriteDefaults = true) =>
    request<MergeAuthorsResult>(`/author/${targetId}/merge`, {
      method: 'POST',
      body: JSON.stringify({ sourceId, overwriteDefaults }),
    }),

  // Books
  listBooks: (params?: { authorId?: number; status?: string; includeExcluded?: boolean; limit?: number; offset?: number }) => {
    const q = new URLSearchParams()
    if (params?.authorId) q.set('authorId', String(params.authorId))
    if (params?.status) q.set('status', params.status)
    if (params?.includeExcluded) q.set('includeExcluded', 'true')
    if (params?.limit !== undefined) q.set('limit', String(params.limit))
    if (params?.offset !== undefined) q.set('offset', String(params.offset))
    const qs = q.toString()
    return request<Page<Book>>(`/book${qs ? '?' + qs : ''}`)
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

  // Wanted
  listWanted: (opts?: { includeExcluded?: boolean }) => {
    const qs = opts?.includeExcluded ? '?includeExcluded=true' : ''
    return request<Book[]>(`/wanted/missing${qs}`)
  },

  // Bulk actions
  bulkActionAuthors: (ids: number[], action: AuthorBulkAction, mediaType?: MediaType) =>
    request<BulkResult>('/author/bulk', { method: 'POST', body: JSON.stringify({ ids, action, ...(mediaType ? { mediaType } : {}) }) }),
  searchAuthorWanted: (id: number) =>
    request<BulkResult>('/author/bulk', { method: 'POST', body: JSON.stringify({ ids: [id], action: 'search' }) }),
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
  testDownloadClient: (id: number) => request<{ message: string; health?: DownloadClientHealth }>(`/downloadclient/${id}/test`, { method: 'POST' }),

  // Library
  triggerLibraryScan: () => request<{ message: string }>('/library/scan', { method: 'POST' }),
  libraryScanStatus: () => request<{
    ran_at: string
    files_found: number
    reconciled: number
    unmatched: number
    tag_read_failed?: number
    unmatched_files?: Array<{ path: string; parsed_title: string; parsed_author: string }>
  }>('/library/scan/status'),

  // Queue
  //
  // The /queue endpoint returns an envelope `{items, partial, staleClients}`
  // since Wave 3 / I (Bundle I, bounded fan-out): when a downloader client
  // fails to answer inside the per-client deadline the items array is
  // still returned but `partial` is true. The current QueuePage callers
  // only consume the items array, so we unwrap here to keep the React
  // code unchanged; surfacing the partial flag is a separate FE task.
  listQueue: () => request<QueueListResponse>('/queue').then(r => r.items ?? []),
  grab: (data: GrabRequest) => request<Download>('/queue/grab', { method: 'POST', body: JSON.stringify(data) }),
  retryImport: (id: number) => request<{ ok: boolean }>(`/queue/${id}/retry-import`, { method: 'POST' }),
  deleteFromQueue: (id: number, deleteFiles = false) =>
    request<void>(`/queue/${id}${deleteFiles ? '?deleteFiles=true' : ''}`, { method: 'DELETE' }),

  // Pending releases
  listPending: () => request<PendingRelease[]>('/pending'),
  dismissPending: (id: number) => request<void>(`/pending/${id}`, { method: 'DELETE' }),
  grabPending: (id: number) => request<Download>(`/pending/${id}/grab`, { method: 'POST' }),

  // History
  listHistory: (params?: { bookId?: number; eventType?: string; limit?: number; offset?: number }) => {
    const q = new URLSearchParams()
    if (params?.bookId) q.set('bookId', String(params.bookId))
    if (params?.eventType) q.set('eventType', params.eventType)
    if (params?.limit !== undefined) q.set('limit', String(params.limit))
    if (params?.offset !== undefined) q.set('offset', String(params.offset))
    const qs = q.toString()
    return request<Page<HistoryEvent>>(`/history${qs ? '?' + qs : ''}`)
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
  getQualityProfile: (id: number) => request<QualityProfile>(`/qualityprofile/${id}`),
  addQualityProfile: (data: Partial<QualityProfile>) =>
    request<QualityProfile>('/qualityprofile', { method: 'POST', body: JSON.stringify(data) }),
  updateQualityProfile: (id: number, data: Partial<QualityProfile>) =>
    request<QualityProfile>(`/qualityprofile/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteQualityProfile: (id: number) =>
    request<void>(`/qualityprofile/${id}`, { method: 'DELETE' }),

  // Series
  listSeries: () => request<Series[]>('/series'),
  createSeries: (data: { title: string }) => request<Series>('/series', { method: 'POST', body: JSON.stringify(data) }),
  getSeries: (id: number) => request<Series>(`/series/${id}`),
  updateSeries: (id: number, data: { title: string }) => request<Series>(`/series/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  monitorSeries: (id: number, monitored: boolean) => request<{ monitored: boolean }>(`/series/${id}`, { method: 'PATCH', body: JSON.stringify({ monitored }) }),
  deleteSeries: (id: number) => request<void>(`/series/${id}`, { method: 'DELETE' }),
  linkBookToSeries: (id: number, data: { bookId: number; positionInSeries: string; primarySeries: boolean }) =>
    request<Series>(`/series/${id}/books`, { method: 'POST', body: JSON.stringify(data) }),
  fillSeries: (id: number, book?: SeriesFillBookRequest) =>
    request<{ queued: number }>(`/series/${id}/fill`, {
      method: 'POST',
      ...(book ? { body: JSON.stringify(book) } : {}),
    }),
  searchHardcoverSeries: (term: string, limit = 10) =>
    request<SeriesHardcoverSearchResult[]>(`/series/hardcover/search?term=${encodeURIComponent(term)}&limit=${limit}`),
  getSeriesHardcoverLink: (id: number) => request<SeriesHardcoverLink>(`/series/${id}/hardcover-link`),
  autoLinkSeriesHardcover: (id: number) =>
    request<SeriesHardcoverAutoResponse>(`/series/${id}/hardcover-link/auto`, { method: 'POST' }),
  linkSeriesHardcover: (id: number, result: SeriesHardcoverSearchResult) =>
    request<SeriesHardcoverLink>(`/series/${id}/hardcover-link`, {
      method: 'PUT',
      body: JSON.stringify(result),
    }),
  unlinkSeriesHardcover: (id: number) => request<{ success: boolean }>(`/series/${id}/hardcover-link`, { method: 'DELETE' }),
  getSeriesHardcoverDiff: (id: number) => request<SeriesHardcoverDiff>(`/series/${id}/hardcover-diff`),

  // Settings
  listSettings: () => request<Array<{ key: string; value: string }>>('/setting'),
  getSetting: (key: string) => request<{ key: string; value: string }>(`/setting/${key}`),
  setSetting: (key: string, value: string) => request<void>(`/setting/${key}`, { method: 'PUT', body: JSON.stringify({ value }) }),
  testHardcover: () => request<HardcoverTestResult>('/hardcover/test', { method: 'POST' }),

  // Backup
  listBackups: () => request<Array<{ name: string; size: number; modTime: string }>>('/backup'),
  createBackup: () => request<{ name: string; size: number; modTime: string }>('/backup', { method: 'POST' }),
  deleteBackup: (filename: string) => request<void>(`/backup/${encodeURIComponent(filename)}`, { method: 'DELETE' }),

  // Calibre
  testCalibre: () => request<CalibreTestResult>('/calibre/test', { method: 'POST' }),
  calibreImportStart: () => request<CalibreImportProgress>('/calibre/import', { method: 'POST' }),
  calibreImportStatus: () => request<CalibreImportProgress>('/calibre/import/status'),
  calibreSyncStart: () => request<CalibreSyncProgress>('/calibre/sync', { method: 'POST' }),
  calibreSyncStatus: () => request<CalibreSyncProgress>('/calibre/sync/status'),
  calibreRuns: (limit = 10) => request<CalibreImportRun[]>(`/calibre/runs?limit=${limit}`),
  calibreRunRollbackPreview: (runId: number) =>
    request<CalibreRollbackResult>(`/calibre/runs/${runId}/rollback/preview`),
  calibreRunRollback: (runId: number) =>
    request<CalibreRollbackResult>(`/calibre/runs/${runId}/rollback`, { method: 'POST' }),

  // Grimmory
  grimmoryConfig: () => request<GrimmoryConfig>('/grimmory/config'),
  grimmorySetConfig: (data: { enabled?: boolean; baseUrl?: string; apiKey?: string }) =>
    request<GrimmoryConfig>('/grimmory/config', { method: 'PUT', body: JSON.stringify(data) }),
  grimmoryTest: (data?: { baseUrl?: string; apiKey?: string }) =>
    request<GrimmoryTestResult>('/grimmory/test', { method: 'POST', body: JSON.stringify(data ?? {}) }),

  // Audiobookshelf
  absConfig: () => request<ABSConfig>('/abs/config'),
  absSetConfig: (data: { baseUrl: string; label: string; enabled: boolean; libraryId: string; libraryIds?: string[]; pathRemap: string; apiKey?: string }) =>
    request<ABSConfig>('/abs/config', { method: 'PUT', body: JSON.stringify(data) }),
  absTest: (data?: { baseUrl?: string; apiKey?: string }) =>
    request<ABSTestResult>('/abs/test', { method: 'POST', body: JSON.stringify(data ?? {}) }),
  absLibraries: (data?: { baseUrl?: string; apiKey?: string }) =>
    request<ABSLibrary[]>('/abs/libraries', { method: 'POST', body: JSON.stringify(data ?? {}) }),
  absImportStart: (data?: { dryRun?: boolean }) =>
    request<ABSImportProgress>('/abs/import', { method: 'POST', body: JSON.stringify(data ?? {}) }),
  absImportStatus: () => request<ABSImportProgress>('/abs/import/status'),
  absImportRuns: () => request<ABSImportRun[]>('/abs/import/runs'),
  absImportRollbackPreview: (runId: number) => request<ABSRollbackResult>(`/abs/import/runs/${runId}/rollback/preview`, { method: 'POST' }),
  absImportRollback: (runId: number) => request<ABSRollbackResult>(`/abs/import/runs/${runId}/rollback`, { method: 'POST' }),
  absReviewItems: (params?: { limit?: number; offset?: number }) => {
    const q = new URLSearchParams()
    if (params?.limit) q.set('limit', String(params.limit))
    if (params?.offset) q.set('offset', String(params.offset))
    return request<PaginatedResponse<ABSReviewItem>>(`/abs/review${q.toString() ? `?${q.toString()}` : ''}`)
  },
  approveAbsReviewItem: (id: number) => request<ABSReviewItem>(`/abs/review/${id}/approve`, { method: 'POST' }),
  resolveAbsReviewAuthor: (id: number, data: { foreignAuthorId: string; authorName: string; applyTo?: 'same_author' }) =>
    request<{ updated: number }>(`/abs/review/${id}/resolve-author`, { method: 'POST', body: JSON.stringify(data) }),
  resolveAbsReviewBook: (id: number, data: { foreignBookId: string; title: string; editedTitle?: string }) =>
    request<ABSReviewItem>(`/abs/review/${id}/resolve-book`, { method: 'POST', body: JSON.stringify(data) }),
  dismissAbsReviewItem: (id: number) => request<ABSReviewItem>(`/abs/review/${id}/dismiss`, { method: 'POST' }),
  dismissAbsReviewRun: (runId: number) =>
    request<{ dismissed: number }>(`/abs/review/dismiss-run/${runId}`, { method: 'POST' }),
  absConflicts: (params?: { limit?: number; offset?: number }) => {
    const q = new URLSearchParams()
    if (params?.limit) q.set('limit', String(params.limit))
    if (params?.offset) q.set('offset', String(params.offset))
    return request<PaginatedResponse<ABSMetadataConflict>>(`/abs/conflicts${q.toString() ? `?${q.toString()}` : ''}`)
  },
  resolveAbsConflict: (id: number, source: 'abs' | 'upstream') =>
    request<ABSMetadataConflict>(`/abs/conflicts/${id}/resolve`, { method: 'POST', body: JSON.stringify({ source }) }),

  // Import lists
  listImportLists: () => request<ImportList[]>('/importlist'),
  addImportList: (data: Partial<ImportList>) => request<ImportList>('/importlist', { method: 'POST', body: JSON.stringify(data) }),
  updateImportList: (id: number, data: ImportListUpdate) => request<ImportList>(`/importlist/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteImportList: (id: number) => request<void>(`/importlist/${id}`, { method: 'DELETE' }),
  syncImportList: (id: number) => request<{ status: string }>(`/importlist/${id}/sync`, { method: 'POST' }),
  hardcoverLists: (token?: string) =>
    request<HardcoverList[]>('/importlist/hardcover/lists', {
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

export interface MergeAuthorsResult {
  BooksReparented: number
  AliasesMigrated: number
  AliasesCreated: number
  TargetUpdated: boolean
}

export type MediaType = 'ebook' | 'audiobook' | 'both'
export type AuthorMonitorMode = 'all' | 'future' | 'latest' | 'none' | 'series'

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

// CalibreImportRun is one persisted Calibre import run (issue #643). Used
// by the "Recent imports" list in the Calibre settings tab.
export interface CalibreImportRun {
  id: number
  sourceId: string
  libraryPath: string
  status: string
  dryRun: boolean
  sourceConfigJson?: string
  summaryJson?: string
  startedAt: string
  finishedAt?: string
}

export interface CalibreRollbackStats {
  actionsPlanned: number
  entitiesDeleted: number
  provenanceUnlinked: number
  filesAffected: number
  skipped: number
  failed: number
}

export interface CalibreRollbackAction {
  entityType: string
  externalId: string
  localId: number
  displayName?: string
  outcome: string
  action: string
  reason?: string
}

export interface CalibreRollbackResult {
  runId: number
  preview: boolean
  applied: boolean
  dryRun: boolean
  status: string
  stats: CalibreRollbackStats
  actions: CalibreRollbackAction[]
  filesOnDiskWarning?: string
  finishedAt: string
}

export interface GrimmoryConfig {
  enabled: boolean
  baseUrl: string
  apiKeyConfigured: boolean
}

export interface GrimmoryTestResult {
  ok: boolean
  message: string
  version?: string
}

export interface ABSConfig {
  featureEnabled: boolean
  baseUrl: string
  label: string
  enabled: boolean
  libraryId: string
  libraryIds?: string[]
  pathRemap: string
  apiKeyConfigured: boolean
}

export interface ABSLibraryFolder {
  id: string
  fullPath: string
}

export interface ABSLibrary {
  id: string
  name: string
  mediaType: string
  icon: string
  provider: string
  folders: ABSLibraryFolder[]
}

export interface ABSTestResult {
  message: string
  username: string
  userType: string
  defaultLibraryId: string
  serverVersion: string
  source: string
}

export interface ABSImportStats {
  librariesScanned: number
  pagesScanned: number
  itemsSeen: number
  itemsNormalized: number
  itemsDetailFetched: number
  authorsCreated: number
  authorsLinked: number
  booksCreated: number
  booksLinked: number
  booksUpdated: number
  seriesCreated: number
  seriesLinked: number
  editionsAdded: number
  ownedMarked: number
  pendingManual: number
  reviewQueued: number
  metadataMatched: number
  metadataRelinked: number
  metadataConflicts: number
  metadataAutoResolved: number
  skipped: number
  failed: number
}

export interface ABSImportItemResult {
  itemId: string
  title: string
  outcome: string
  message?: string
  matchedBy?: string
  authorId?: number
  bookId?: number
  seriesCount?: number
}

export interface ABSImportProgress {
  running: boolean
  runId?: number
  dryRun?: boolean
  startedAt?: string
  finishedAt?: string
  processed: number
  message?: string
  error?: string
  resumedFromCheckpoint?: boolean
  checkpoint?: {
    libraryId: string
    page: number
    lastItemId?: string
    pageSize: number
    updatedAt: string
  }
  stats?: ABSImportStats
  results?: ABSImportItemResult[]
}

export interface ABSImportRunSummary {
  dryRun: boolean
  resumedFromCheckpoint: boolean
  checkpoint?: {
    libraryId: string
    page: number
    lastItemId?: string
    pageSize: number
    updatedAt: string
  }
  stats: ABSImportStats
  error?: string
}

export interface ABSImportRun {
  id: number
  sourceId: string
  sourceLabel: string
  baseUrl: string
  libraryId: string
  status: string
  dryRun: boolean
  startedAt: string
  finishedAt?: string
  source: {
    sourceId: string
    label: string
    baseUrl: string
    libraryId: string
    libraryIds?: string[]
    pathRemap?: string
    enabled: boolean
    dryRun: boolean
  }
  checkpoint?: {
    libraryId: string
    page: number
    lastItemId?: string
    pageSize: number
    updatedAt: string
  }
  summary: ABSImportRunSummary
}

export interface ABSRollbackAction {
  entityType: string
  externalId: string
  displayName?: string
  localId: number
  outcome: string
  action: string
  reason?: string
}

export interface ABSRollbackResult {
  runId: number
  preview: boolean
  dryRun: boolean
  status: string
  stats: {
    actionsPlanned: number
    entitiesDeleted: number
    provenanceUnlinked: number
    skipped: number
    failed: number
  }
  actions: ABSRollbackAction[]
  finishedAt: string
}

export interface ABSReviewItem {
  id: number
  sourceId: string
  libraryId: string
  itemId: string
  title: string
  primaryAuthor: string
  asin: string
  mediaType: string
  reviewReason: 'unmatched_author' | 'ambiguous_author' | 'unmatched_book' | 'ambiguous_book'
  payloadJson: string
  resolvedAuthorForeignId?: string
  resolvedAuthorName?: string
  resolvedBookForeignId?: string
  resolvedBookTitle?: string
  editedTitle?: string
  fileMappingFound: boolean
  fileMappingMessage?: string
  latestRunId?: number | null
  status: 'pending' | 'approved' | 'dismissed'
  createdAt: string
  updatedAt: string
}

export interface PaginatedResponse<T> {
  items: T[]
  total: number
  limit: number
  offset: number
}

// Page<T> is the envelope returned by the paginated List endpoints introduced
// in PR #902 (GET /book, /author, /history). Shape matches PaginatedResponse
// above and the two will likely be unified later; kept distinct for now so
// the diff stays scoped to the three new endpoints.
export interface Page<T> {
  items: T[]
  total: number
  limit: number
  offset: number
}

export interface ABSMetadataConflict {
  id: number
  sourceId: string
  libraryId: string
  itemId: string
  entityType: string
  localId: number
  entityName: string
  fieldName: string
  fieldLabel: string
  absValue: string
  upstreamValue: string
  appliedSource: 'abs' | 'upstream' | ''
  appliedValue: string
  preferredSource: 'abs' | 'upstream' | ''
  authorRelinkEligible: boolean
  resolutionStatus: 'unresolved' | 'resolved'
  updatedAt: string
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
  // categoryAudiobook overrides category for audiobook grabs only.
  // Optional; when empty (the default for pre-#700 rows) audiobook grabs
  // fall back to `category`.
  categoryAudiobook?: string
  pathRemap?: string
  enabled: boolean
  health?: DownloadClientHealth
}

export interface DownloadClientHealth {
  status: 'ok' | 'checking' | 'error'
  message: string
}

export interface Download {
  id: number
  guid: string
  title: string
  status: string
  size: number
  protocol: string
  errorMessage: string
  addedAt: string
  grabbedAt?: string
  completedAt?: string
  importedAt?: string
}

export interface QueueItem extends Download {
  percentage?: string
  timeLeft?: string
}

// QueueListResponse is the envelope returned by GET /queue. Items is the
// flat array the UI has always rendered; partial/staleClients let a
// future page iteration warn when a downloader client did not answer
// inside the per-client deadline (Wave 3 / I).
export interface QueueListResponse {
  items: QueueItem[]
  partial?: boolean
  staleClients?: Array<{ clientId: number; name?: string; message?: string }>
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
  // D4b audit. Surfaces who promoted this row into the blocklist. NULL
  // for system-written rows (scheduler stall-detection, readarr migration)
  // and for legacy rows that predate migration 049. Audit only; the list
  // semantics remain global. The admin "blocklisted by X" UI consuming
  // this field is a future task.
  createdByUserId?: number | null
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

export interface SeriesHardcoverLink {
  id: number
  seriesId: number
  hardcoverSeriesId: string
  hardcoverProviderId: string
  hardcoverTitle: string
  hardcoverAuthorName: string
  hardcoverBookCount: number
  confidence: number
  linkedBy: 'auto' | 'manual' | string
  linkedAt: string
  createdAt: string
  updatedAt: string
}

export interface HardcoverTestResult {
  ok: boolean
  tokenConfigured: boolean
  searchResults: number
  sampleSeriesId?: string
  sampleTitle?: string
  catalogOk: boolean
  catalogBookCount?: number
  message?: string
  error?: string
}

export interface SeriesHardcoverSearchResult {
  foreignId: string
  providerId: string
  title: string
  authorName: string
  bookCount: number
  readersCount: number
  books: string[]
  confidence?: number
}

export interface SeriesHardcoverAutoResponse {
  linked: boolean
  link?: SeriesHardcoverLink
  candidates: SeriesHardcoverSearchResult[]
  reason?: string
}

export interface SeriesFillBookRequest {
  foreignBookId?: string
  providerId?: string
  position?: string
}

export interface SeriesHardcoverDiffBook {
  foreignBookId: string
  providerId: string
  title: string
  subtitle?: string
  position: string
  imageUrl?: string
  authorName?: string
  releaseDate?: string
  usersCount?: number
  localBookId?: number
  localTitle?: string
  localStatus?: string
  matchConfidence?: number
}

export interface SeriesHardcoverDiff {
  seriesId: number
  link: SeriesHardcoverLink
  present: SeriesHardcoverDiffBook[]
  missing: SeriesHardcoverDiffBook[]
  localOnly: SeriesHardcoverDiffBook[]
  uncertain: SeriesHardcoverDiffBook[]
  presentCount: number
  missingCount: number
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
    primarySeries?: boolean
    book?: Book
  }>
  hardcoverLink?: SeriesHardcoverLink
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
  apiKeyConfigured: boolean
  rootFolderId?: number | null
  qualityProfileId?: number | null
  monitorNew: boolean
  autoAdd: boolean
  enabled: boolean
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
export type BookBulkAction = 'monitor' | 'unmonitor' | 'delete' | 'search' | 'set_media_type' | 'exclude'
export type WantedBulkAction = 'search' | 'blocklist' | 'unmonitor'

export interface BulkResult {
  results: Record<string, { ok: boolean; error?: string }>
}
