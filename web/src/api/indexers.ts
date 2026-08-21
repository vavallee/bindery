import { request } from './core'
import type { SearchResult } from './books'

export interface Indexer {
  id: number
  name: string
  type: string
  url: string
  apiKey: string
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
  apiKey: string
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
