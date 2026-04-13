const BASE = '/api/v1'

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || res.statusText)
  }
  return res.json()
}

export const api = {
  // System
  health: () => request<{ status: string; version: string }>('/health'),
  status: () => request<{ version: string; commit: string; buildDate: string }>('/system/status'),

  // Metadata search
  searchAuthors: (term: string) => request<Author[]>(`/search/author?term=${encodeURIComponent(term)}`),
  searchBooks: (term: string) => request<Book[]>(`/search/book?term=${encodeURIComponent(term)}`),
  lookupISBN: (isbn: string) => request<Book>(`/book/lookup?isbn=${encodeURIComponent(isbn)}`),

  // Authors
  listAuthors: () => request<Author[]>('/author'),
  getAuthor: (id: number) => request<Author>(`/author/${id}`),
  addAuthor: (data: AddAuthorRequest) => request<Author>('/author', { method: 'POST', body: JSON.stringify(data) }),
  updateAuthor: (id: number, data: Partial<Author>) => request<Author>(`/author/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteAuthor: (id: number) => request<void>(`/author/${id}`, { method: 'DELETE' }),
  refreshAuthor: (id: number) => request<void>(`/author/${id}/refresh`, { method: 'POST' }),

  // Books
  listBooks: (params?: { authorId?: number; status?: string }) => {
    const q = new URLSearchParams()
    if (params?.authorId) q.set('authorId', String(params.authorId))
    if (params?.status) q.set('status', params.status)
    const qs = q.toString()
    return request<Book[]>(`/book${qs ? '?' + qs : ''}`)
  },
  getBook: (id: number) => request<Book>(`/book/${id}`),
  updateBook: (id: number, data: Partial<Book>) => request<Book>(`/book/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteBook: (id: number, deleteFiles = false) =>
    request<void>(`/book/${id}${deleteFiles ? '?deleteFiles=true' : ''}`, { method: 'DELETE' }),
  deleteBookFile: (id: number) => request<Book>(`/book/${id}/file`, { method: 'DELETE' }),
  searchBook: (id: number) => request<SearchResult[]>(`/book/${id}/search`, { method: 'POST' }),
  enrichAudiobook: (id: number) => request<Book>(`/book/${id}/enrich-audiobook`, { method: 'POST' }),

  // Wanted
  listWanted: () => request<Book[]>('/wanted/missing'),

  // Indexers
  listIndexers: () => request<Indexer[]>('/indexer'),
  addIndexer: (data: Partial<Indexer>) => request<Indexer>('/indexer', { method: 'POST', body: JSON.stringify(data) }),
  updateIndexer: (id: number, data: Partial<Indexer>) => request<Indexer>(`/indexer/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteIndexer: (id: number) => request<void>(`/indexer/${id}`, { method: 'DELETE' }),
  testIndexer: (id: number) => request<{ message: string }>(`/indexer/${id}/test`, { method: 'POST' }),
  searchIndexers: (q: string) => request<SearchResult[]>(`/indexer/search?q=${encodeURIComponent(q)}`),

  // Download clients
  listDownloadClients: () => request<DownloadClient[]>('/downloadclient'),
  addDownloadClient: (data: Partial<DownloadClient>) => request<DownloadClient>('/downloadclient', { method: 'POST', body: JSON.stringify(data) }),
  updateDownloadClient: (id: number, data: Partial<DownloadClient>) => request<DownloadClient>(`/downloadclient/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteDownloadClient: (id: number) => request<void>(`/downloadclient/${id}`, { method: 'DELETE' }),
  testDownloadClient: (id: number) => request<{ message: string }>(`/downloadclient/${id}/test`, { method: 'POST' }),

  // Queue
  listQueue: () => request<QueueItem[]>('/queue'),
  grab: (data: GrabRequest) => request<Download>('/queue/grab', { method: 'POST', body: JSON.stringify(data) }),
  deleteFromQueue: (id: number) => request<void>(`/queue/${id}`, { method: 'DELETE' }),

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

  // Metadata Profiles
  listMetadataProfiles: () => request<MetadataProfile[]>('/metadataprofile'),
  addMetadataProfile: (data: Partial<MetadataProfile>) => request<MetadataProfile>('/metadataprofile', { method: 'POST', body: JSON.stringify(data) }),
  deleteMetadataProfile: (id: number) => request<void>(`/metadataprofile/${id}`, { method: 'DELETE' }),

  // Delay Profiles
  listDelayProfiles: () => request<DelayProfile[]>('/delayprofile'),
  addDelayProfile: (data: Partial<DelayProfile>) => request<DelayProfile>('/delayprofile', { method: 'POST', body: JSON.stringify(data) }),
  deleteDelayProfile: (id: number) => request<void>(`/delayprofile/${id}`, { method: 'DELETE' }),

  // Custom Formats
  listCustomFormats: () => request<CustomFormat[]>('/customformat'),
  addCustomFormat: (data: Partial<CustomFormat>) => request<CustomFormat>('/customformat', { method: 'POST', body: JSON.stringify(data) }),
  deleteCustomFormat: (id: number) => request<void>(`/customformat/${id}`, { method: 'DELETE' }),
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
  books?: Book[]
  statistics?: { bookCount: number; availableBookCount: number; wantedBookCount: number }
}

export type MediaType = 'ebook' | 'audiobook'

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
  narrator?: string
  durationSeconds?: number
  asin?: string
  author?: Author
}

export interface Indexer {
  id: number
  name: string
  type: string
  url: string
  apiKey: string
  categories: number[]
  enabled: boolean
}

export interface DownloadClient {
  id: number
  name: string
  type: string
  host: string
  port: number
  apiKey: string
  useSsl: boolean
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
}

export interface AddAuthorRequest {
  foreignAuthorId: string
  authorName: string
  monitored: boolean
  searchOnAdd: boolean
}

export interface GrabRequest {
  guid: string
  title: string
  nzbUrl: string
  size: number
  bookId?: number
  indexerId?: number
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
