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
}
