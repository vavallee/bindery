import { request } from './core'
import type { Book } from './books'
import type { BookRef } from './common'

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
  book?: BookRef
}

export interface QueueItem extends Download {
  percentage?: string
  timeLeft?: string
}

export interface ManualImportLookup {
  match: 'confident' | 'ambiguous' | 'none'
  book?: Book
  candidates?: Book[]
  detectedFormat: string
  parsedTitle: string
  parsedAuthor: string
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
  book?: BookRef
}

export const queueApi = {
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

  // Manual import (#766)
  lookupManualImport: (path: string) =>
    request<ManualImportLookup>(`/queue/manual-import/lookup?path=${encodeURIComponent(path)}`),
  manualImport: (data: { path: string; bookId: number; format?: string }) =>
    request<Download>('/queue/manual-import', { method: 'POST', body: JSON.stringify(data) }),

  // Pending releases
  listPending: () => request<PendingRelease[]>('/pending'),
  dismissPending: (id: number) => request<void>(`/pending/${id}`, { method: 'DELETE' }),
  grabPending: (id: number) => request<Download>(`/pending/${id}/grab`, { method: 'POST' }),
}
