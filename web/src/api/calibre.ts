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

export const calibreApi = {
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
}
