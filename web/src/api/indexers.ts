import { request } from './core'
import type { SearchResult } from './books'

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
