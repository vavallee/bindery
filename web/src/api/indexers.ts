import { request } from './core'
import type { SearchResult } from './books'

export interface Indexer {
  id: number
  name: string
  type: string
  url: string
  // Write-only: the API never returns the stored key, so this is '' on every
  // response. Send it only when setting or rotating a key; a blank value on
  // update means "keep the stored one". Use clearApiKey to actually remove it.
  apiKey: string
  // Response-only: whether a key is stored server-side (#2212).
  apiKeyConfigured?: boolean
  categories: number[]
  includeParentCategories?: boolean
  priority: number
  enabled: boolean
  prowlarrInstanceId?: number
  // Per-indexer seed-ratio override (#883). Omitted/null = no override (the
  // download client keeps its global rule); -1 = unlimited (seed forever).
  seedRatio?: number | null
  // Provenance of seedRatio (#1065): 'prowlarr' = auto-populated from Prowlarr's
  // seedCriteria.seedRatio (a later Prowlarr change may refresh it); 'user' = the
  // user set/cleared it (Prowlarr won't touch it); omitted/'' = unset.
  seedRatioSource?: string
  // Only auto-grab freeleech releases from this indexer. Non-freeleech
  // releases are held in the Pending queue for manual approval instead of
  // being grabbed automatically. Interactive search is unaffected.
  freeleechOnly?: boolean
  // Cap on how many requests Bindery will send this indexer in a rolling 24
  // hours (#2312). Omitted/null, and 0, all mean no cap. The unit is outbound
  // requests rather than books: one book on one indexer costs between one and
  // eight, depending on how far the query cascade falls through.
  dailyQueryLimit?: number | null
  // How much of dailyQueryLimit is spent, written by the backend rather than
  // the user, and absent on an indexer with no cap. It lags the live tally by
  // up to one flush interval, so it is a display figure and not the number the
  // cap is enforced against.
  dailyQueriesUsed?: number
  // Search health, written by the backend rather than the user (#1935). All
  // four are absent on an indexer that has never been searched. lastError is
  // cleared on the next successful search, so a present value means the last
  // thing this indexer said was a refusal. lastErrorCode is the Newznab code
  // when the indexer itself rejected us: 1xx (100 bad credentials, 101 account
  // suspended, 102 VPN forbidden) needs a human, 5xx is a rate limit that
  // clears on its own, and absent means a transport failure.
  lastError?: string | null
  lastErrorCode?: number | null
  lastFailureAt?: string | null
  lastSuccessAt?: string | null
}

// indexerNeedsAttention reports whether an indexer's last search failed with
// something only a human can fix. Mirrors models.Indexer.NeedsAttention.
export function indexerNeedsAttention(idx: Indexer): boolean {
  if (!idx.lastError || idx.lastErrorCode == null) return false
  return idx.lastErrorCode >= 100 && idx.lastErrorCode <= 199
}

// indexerQueryCap returns the configured cap and how much of it is spent, or
// null when this indexer has no cap. Mirrors the backend's rule that a null or
// non-positive limit means unlimited, so the row renders nothing rather than
// "0 of 0".
export function indexerQueryCap(idx: Indexer): { used: number; limit: number; reached: boolean } | null {
  if (idx.dailyQueryLimit == null || idx.dailyQueryLimit <= 0) return null
  const used = idx.dailyQueriesUsed ?? 0
  return { used, limit: idx.dailyQueryLimit, reached: used >= idx.dailyQueryLimit }
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

export interface ProwlarrInstance {
  id: number
  name: string
  url: string
  // Write-only, same contract as Indexer.apiKey (#2212).
  apiKey: string
  // Response-only: whether a key is stored server-side.
  apiKeyConfigured?: boolean
  syncOnStartup: boolean
  enabled: boolean
  lastSyncAt?: string
}

export const indexersApi = {
  // Indexers
  listIndexers: () => request<Indexer[]>('/indexer'),
  addIndexer: (data: Partial<Indexer>) => request<Indexer>('/indexer', { method: 'POST', body: JSON.stringify(data) }),
  updateIndexer: (id: number, data: Partial<Indexer>) => request<Indexer>(`/indexer/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteIndexer: (id: number) => request<void>(`/indexer/${id}`, { method: 'DELETE' }),
  testIndexer: (id: number) => request<IndexerTestResult>(`/indexer/${id}/test`, { method: 'POST' }),
  // Test an unsaved indexer config (Add/Edit form Test button). Same response
  // shape as testIndexer so the UI reuses one rendering path.
  testIndexerConfig: (data: Partial<Indexer>) =>
    request<IndexerTestResult>('/indexer/test', { method: 'POST', body: JSON.stringify(data) }),

  // Prowlarr indexer sync
  listProwlarr: () => request<ProwlarrInstance[]>('/prowlarr'),
  addProwlarr: (data: Partial<ProwlarrInstance>) => request<ProwlarrInstance>('/prowlarr', { method: 'POST', body: JSON.stringify(data) }),
  updateProwlarr: (id: number, data: Partial<ProwlarrInstance>) => request<ProwlarrInstance>(`/prowlarr/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteProwlarr: (id: number) => request<void>(`/prowlarr/${id}`, { method: 'DELETE' }),
  testProwlarr: (id: number) => request<{ ok: string; version?: string; error?: string }>(`/prowlarr/${id}/test`, { method: 'POST' }),
  syncProwlarr: (id: number) => request<{ added: number; updated: number; removed: number }>(`/prowlarr/${id}/sync`, { method: 'POST' }),
  searchIndexers: (q: string) => request<SearchResult[]>(`/indexer/search?q=${encodeURIComponent(q)}`),
}
