import { apiURL, request } from './core'

export interface SystemStatus {
  version: string
  commit: string
  buildDate: string
  /** Newest published release per the ping server; absent when unknown
   *  (no successful ping yet, or telemetry disabled). */
  latestVersion?: string
  imageCacheBytes?: number
  enhancedHardcoverApi: boolean
  hardcoverTokenConfigured: boolean
  hardcoverPausedUntil?: string
  /** True while Hardcover refused the saved token and requests are paused until it changes. */
  hardcoverTokenRejected?: boolean
  enhancedHardcoverDisabledReason?: 'env_disabled' | 'missing_token' | 'admin_disabled' | string
}

/** Onboarding-checklist progress; see internal/api/setup_state.go for why
 *  the indexer/client fields are current state while the rest are
 *  ever-happened. */
export interface SetupState {
  hasIndexer: boolean
  hasClient: boolean
  hasAuthor: boolean
  hasGrab: boolean
  hasImport: boolean
  complete: boolean
  /** Admin-only. The pipeline imported once but cannot import now. */
  blocked?: boolean
  /** Names the problem and the fix, never a path. English; the UI localises by blockedArea. */
  blockedReason?: string
  blockedArea?: 'storage' | 'indexer' | 'download-client'
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

export interface StorageDirStatus {
  name: string
  path: string
  exists: boolean
  writable: boolean
  reason?: string
}

export interface StorageHealth {
  downloadDir: string
  audiobookDownloadDir: string
  libraryDir: string
  audiobookDir: string
  dirs: StorageDirStatus[]
  hardlinkable: boolean
  hardlinkReason?: string
}

/** One relationship (child table to parent table) with rows whose parent no
 *  longer exists. Counts only: the endpoint never returns row ids or contents. */
export interface DBIntegrityRelationship {
  childTable: string
  parentTable: string
  /** Declared ON DELETE action: CASCADE, SET NULL, NO ACTION, ... ('' if unknown). */
  onDelete: string
  violations: number
}

/** Result of POST /system/db-integrity (#3356). */
export interface DBIntegrityReport {
  relationships: DBIntegrityRelationship[]
  total: number
  checkedAt: string
}

/** What a cleanup does with a relationship's rows: remove them, clear the
 *  reference and keep the row, or leave them alone. */
export type DBCleanupAction = 'delete' | 'clear' | 'keep' | 'skipped'

export interface DBCleanupRelationship extends DBIntegrityRelationship {
  /** 'skipped': the schema has a rule but none of these rows can be repaired
   *  automatically, so a cleanup leaves them. After a cleanup, anything left
   *  with a rule is shown as skipped. */
  action: DBCleanupAction
  /** How many of `violations` a cleanup can resolve; fewer means the rest are skipped. */
  fixable: number
}

/** Result of POST /system/db-integrity/cleanup/preview (#3357). Changes nothing.
 *  previewToken is absent when a cleanup would have nothing to do. */
export interface DBCleanupPreview {
  relationships: DBCleanupRelationship[]
  total: number
  fixable: number
  unfixable: number
  checkedAt: string
  previewToken?: string
  expiresAt?: string
}

/** Result of POST /system/db-integrity/cleanup (#3357). */
export interface DBCleanupResult {
  deleted: number
  cleared: number
  skipped: number
  skipNotes: string[]
  /** What a fresh scan still finds after the repair. Meaningless unless verified. */
  remaining: DBCleanupRelationship[]
  remainingTotal: number
  verified: boolean
  backupPath: string
  backupName: string
  durationMs: number
  completedAt: string
}

export interface LogQuery {
  level?: string
  component?: string
  from?: string
  to?: string
  q?: string
  limit?: number
  offset?: number
}

/** Serialises the log filters. Shared by the list and the export so a
 *  downloaded file reflects exactly the filters that were on screen (#1903). */
function logQueryString(params?: LogQuery): string {
  const p: Record<string, string> = {}
  if (params?.level) p.level = params.level
  if (params?.component) p.component = params.component
  if (params?.from) p.from = params.from
  if (params?.to) p.to = params.to
  if (params?.q) p.q = params.q
  if (params?.limit) p.limit = String(params.limit)
  if (params?.offset) p.offset = String(params.offset)
  const qs = new URLSearchParams(p).toString()
  return qs ? '?' + qs : ''
}

export const systemApi = {
  // System
  health: () => request<{ status: string; version: string }>('/health'),
  status: () => request<SystemStatus>('/system/status'),
  setupState: () => request<SetupState>('/system/setup-state'),
  getLogs: (params?: LogQuery) => request<LogEntry[]>(`/system/logs${logQueryString(params)}`),
  /** URL of the plain-text log export for these filters. Handed straight to a
   *  download anchor — the session cookie authenticates it and the server
   *  names the file, so there is nothing to fetch in JS. */
  logExportURL: (params?: LogQuery) => apiURL(`/system/logs/export${logQueryString(params)}`),
  getLogLevel: () => request<{ level: string }>('/system/loglevel'),
  setLogLevel: (level: string) =>
    request<{ level: string }>('/system/loglevel', { method: 'PUT', body: JSON.stringify({ level }) }),
  getStorage: () => request<StorageHealth>('/system/storage'),
  /** Read only foreign key drift scan. Admin only, a full database pass, so
   *  call it from a button and never on load. */
  checkDBIntegrity: () => request<DBIntegrityReport>('/system/db-integrity', { method: 'POST' }),
  /** What a cleanup would do, per relationship, plus the token a cleanup must
   *  present. Read only; admin only; a full database pass. */
  previewDBCleanup: () => request<DBCleanupPreview>('/system/db-integrity/cleanup/preview', { method: 'POST' }),
  /** Deletes and clears the rows the preview described, after taking a backup.
   *  The token binds the request to that preview. */
  cleanupDB: (previewToken: string) =>
    request<DBCleanupResult>('/system/db-integrity/cleanup', {
      method: 'POST',
      body: JSON.stringify({ confirm: true, previewToken }),
    }),
}
