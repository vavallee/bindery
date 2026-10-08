import { request } from './core'

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
  // sample is the path of the real book the plugin was asked to open,
  // after the push path remap (#2831). Absent when no book was probed.
  sample?: string
  // warning names an outdated Bindery Bridge. Also present on a failed
  // test's error body.
  warning?: string
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
  // skipped counts books the run never attempted. Deliberately not part of
  // total, which is the denominator of the progress bar.
  skipped: number
}

// CalibreSyncSkip is one book the bulk push did not attempt, and why.
// Before these existed a skipped book showed up nowhere at all, so an empty
// report could mean either "already in Calibre" or "every book was dropped by
// a filter you cannot see" (discussion #1592).
export interface CalibreSyncSkip {
  bookId: number
  title: string
  reason: string
}

// CalibreSyncProgress is the polled shape for /calibre/sync/status.
export interface CalibreSyncProgress {
  // running stays true while any book the run queued is still waiting for
  // Calibre, which lasts as long as Calibre is closed.
  running: boolean
  // queueing is Push all itself walking the library. Only this part blocks a
  // second Push all.
  queueing?: boolean
  startedAt?: string
  finishedAt?: string
  message?: string
  error?: string
  stats: CalibreSyncStats
  errors: CalibreSyncError[]
  // skips samples the skipped books, capped the same way errors is.
  // stats.skipped always holds the full count.
  skips: CalibreSyncSkip[]
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
  // null from servers before the empty slice fix, for a run with nothing to undo.
  actions: CalibreRollbackAction[] | null
  filesOnDiskWarning?: string
  finishedAt: string
}

// CalibreDeliveryState is where one ebook file stands in the Calibre delivery
// queue (#2832).
export type CalibreDeliveryState = 'pending' | 'delivered' | 'failed' | 'skipped'

// CalibreDeliveryHealth is what the delivery worker last learned about
// Calibre. It only refreshes while something is waiting.
export interface CalibreDeliveryHealth {
  lastPassAt?: string
  checkedAt?: string
  reachable?: boolean
  lastError?: string
}

// CalibreDeliverySummary is GET /calibre/deliveries/summary. Admin only.
export interface CalibreDeliverySummary {
  pending: number
  delivered: number
  failed: number
  skipped: number
  lastDeliveredAt?: string
  mode: CalibreMode
  target: CalibreDeliveryHealth
  // transport and pull are #2833. Older servers send neither.
  transport?: CalibreTransport
  pull?: CalibrePullContact
}

// CalibreTransport is which side connects in plugin mode (#2833).
export type CalibreTransport = 'push' | 'pull'

// CalibrePullContact is what Bindery last heard from a pulling plugin. It is
// kept in memory, so it is empty after a restart until the plugin checks in.
export interface CalibrePullContact {
  lastSeen?: string
  pluginVersion?: string
  capabilities?: string[]
  remoteAddr?: string
  library?: string
}

// CalibreDelivery is one queue row with the book's title and author.
export interface CalibreDelivery {
  id: number
  bookId: number
  bookFileId: number
  filePath: string
  format: string
  state: CalibreDeliveryState
  outcome: string
  attempts: number
  lastError: string
  lastErrorCode: string
  calibreId?: number
  updatedAt: string
  deliveredAt?: string
  bookTitle: string
  authorName: string
}

export interface CalibreDeliveryList {
  items: CalibreDelivery[]
  total: number
}

// BookCalibreState is GET /book/{id}/calibre. Everyone who can see the book
// gets state; the other fields are only sent to admins. 'off' means the
// integration is off and 'none' that the book was never queued.
export interface BookCalibreState {
  state: CalibreDeliveryState | 'off' | 'none'
  outcome?: string
  lastError?: string
  lastErrorCode?: string
  attempts?: number
  calibreId?: number
  deliveredAt?: string
}

export const calibreApi = {
  // Calibre
  testCalibre: () => request<CalibreTestResult>('/calibre/test', { method: 'POST' }),
  calibreImportStart: () => request<CalibreImportProgress>('/calibre/import', { method: 'POST' }),
  calibreImportStatus: () => request<CalibreImportProgress>('/calibre/import/status'),
  calibreSyncStart: () => request<CalibreSyncProgress>('/calibre/sync', { method: 'POST' }),
  calibreSyncStatus: () => request<CalibreSyncProgress>('/calibre/sync/status'),
  calibreDeliverySummary: () => request<CalibreDeliverySummary>('/calibre/deliveries/summary'),
  calibreDeliveries: (state: CalibreDeliveryState | '' = '', limit = 50, offset = 0) =>
    request<CalibreDeliveryList>(
      `/calibre/deliveries?state=${encodeURIComponent(state)}&limit=${limit}&offset=${offset}`,
    ),
  calibreDeliveryRetry: (state: 'failed' | 'skipped' | '' = 'failed') =>
    request<{ requeued: number }>('/calibre/deliveries/retry', {
      method: 'POST',
      body: JSON.stringify({ state }),
    }),
  calibreDeliveryClearPending: () =>
    request<{ cleared: number }>('/calibre/deliveries?state=pending', { method: 'DELETE' }),
  calibreDeliveryReset: () =>
    request<{ removed: number }>('/calibre/deliveries/reset', {
      method: 'POST',
      body: JSON.stringify({ confirm: true }),
    }),
  bookCalibreState: (bookId: number) => request<BookCalibreState>(`/book/${bookId}/calibre`),
  calibreRuns: (limit = 10) => request<CalibreImportRun[]>(`/calibre/runs?limit=${limit}`),
  calibreRunRollbackPreview: (runId: number) =>
    request<CalibreRollbackResult>(`/calibre/runs/${runId}/rollback/preview`),
  calibreRunRollback: (runId: number) =>
    request<CalibreRollbackResult>(`/calibre/runs/${runId}/rollback`, { method: 'POST' }),
}
