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

// ScanItem is one candidate book unit found under a folder by the bulk scan.
export interface ScanItem {
  path: string
  name: string
  match: 'confident' | 'ambiguous' | 'none'
  parsedTitle: string
  parsedAuthor: string
  detectedFormat: string
  book?: Book
  candidates?: Book[]
}

export interface FolderScanResponse {
  items: ScanItem[]
  truncated: boolean
}

export interface BatchImportItem {
  path: string
  bookId: number
  format?: string
}

export interface BatchImportResult {
  path: string
  accepted: boolean
  error?: string
  downloadId?: number
}

export interface BatchImportResponse {
  results: BatchImportResult[]
  accepted: number
  failed: number
}

// QueueListResponse is the envelope returned by GET /queue. Items is the
// flat array the UI renders. Partial is true when a downloader client did not
// answer inside its per-client deadline, and staleClients names those clients
// so the page can say which one it could not reach (#2376). A client that
// failed before it could be identified appears in neither list, so partial can
// be true with staleClients empty.
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
  // The /queue endpoint returns an envelope `{items, partial, staleClients}`:
  // when a downloader client fails to answer inside the per-client deadline the
  // items array is still returned but `partial` is true. The envelope reaches
  // the caller whole (#2376). It used to be unwrapped to `items` here, which
  // meant an unreachable qBittorrent rendered as a short (often empty) queue
  // with nothing saying why, and the natural reading of that is "my downloads
  // vanished".
  listQueue: () => request<QueueListResponse>('/queue'),
  grab: (data: GrabRequest) => request<Download>('/queue/grab', { method: 'POST', body: JSON.stringify(data) }),
  retryImport: (id: number) => request<{ ok: boolean }>(`/queue/${id}/retry-import`, { method: 'POST' }),
  // retryDownload re-sends the release a failed row already holds to the
  // download client (#2295). It is NOT a fresh search: the row's own release
  // goes out again, which is what makes a bulk retry predictable. Searching for
  // a different release is the book page's Search button.
  retryDownload: (id: number) => request<Download>(`/queue/${id}/retry`, { method: 'POST' }),

  // Retry many queue rows in one request. The server picks the retry each row's
  // state has (an import stage row re-arms its import, a failed row has its
  // release re-sent) and says which one ran in `action`. This replaced a
  // Promise.all firing one POST per selected row from the browser (#2295).
  bulkRetryQueue: (ids: number[]) =>
    request<{ results: Record<string, { ok: boolean; error?: string; action?: 'import' | 'resend' }> }>('/queue/bulk-retry', {
      method: 'POST',
      body: JSON.stringify({ ids }),
    }),
  // matchDownload attaches an unmatched, import-failed download to an existing
  // book and imports the already-downloaded files against it (#1589). Returns
  // whether the files were imported directly (imported=true) or the import was
  // re-queued for the download client to place on its next poll.
  matchDownload: (downloadId: number, bookId: number) =>
    request<{ imported: boolean; retryQueued?: boolean; located?: boolean }>('/queue/manual-import/match', {
      method: 'POST',
      body: JSON.stringify({ downloadId, bookId }),
    }),
  deleteFromQueue: (id: number, deleteFiles = false) =>
    request<void>(`/queue/${id}${deleteFiles ? '?deleteFiles=true' : ''}`, { method: 'DELETE' }),

  // Remove many queue items at once. unmonitorBooks also stops monitoring each
  // linked book so the scheduler doesn't immediately re-grab it — the recovery
  // path for an accidental mass import that flooded the queue. Returns a per-id
  // result map ({ "12": { ok: true }, "13": { ok: false, error: "..." } }).
  bulkDeleteQueue: (ids: number[], opts?: { deleteFiles?: boolean; unmonitorBooks?: boolean }) =>
    request<{ results: Record<string, { ok: boolean; error?: string }> }>('/queue/bulk-delete', {
      method: 'POST',
      body: JSON.stringify({ ids, deleteFiles: opts?.deleteFiles ?? false, unmonitorBooks: opts?.unmonitorBooks ?? false }),
    }),

  // Manual import (#766)
  lookupManualImport: (path: string) =>
    request<ManualImportLookup>(`/queue/manual-import/lookup?path=${encodeURIComponent(path)}`),
  manualImport: (data: { path: string; bookId: number; format?: string }) =>
    request<Download>('/queue/manual-import', { method: 'POST', body: JSON.stringify(data) }),
  // Bulk folder import: scan a folder for book units, then import the selected ones.
  scanFolder: (path: string) =>
    request<FolderScanResponse>(`/queue/manual-import/scan?path=${encodeURIComponent(path)}`),
  batchImport: (items: BatchImportItem[]) =>
    request<BatchImportResponse>('/queue/manual-import/batch', { method: 'POST', body: JSON.stringify(items) }),

  // Pending releases
  listPending: () => request<PendingRelease[]>('/pending'),
  dismissPending: (id: number) => request<void>(`/pending/${id}`, { method: 'DELETE' }),
  grabPending: (id: number) => request<Download>(`/pending/${id}/grab`, { method: 'POST' }),
}
