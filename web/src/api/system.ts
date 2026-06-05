import { request } from './core'

export interface SystemStatus {
  version: string
  commit: string
  buildDate: string
  imageCacheBytes?: number
  enhancedHardcoverApi: boolean
  hardcoverTokenConfigured: boolean
  enhancedHardcoverDisabledReason?: 'env_disabled' | 'missing_token' | 'admin_disabled' | string
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

export const systemApi = {
  // System
  health: () => request<{ status: string; version: string }>('/health'),
  status: () => request<SystemStatus>('/system/status'),
  getLogs: (params?: { level?: string; component?: string; from?: string; to?: string; q?: string; limit?: number; offset?: number }) => {
    const p: Record<string, string> = {}
    if (params?.level) p.level = params.level
    if (params?.component) p.component = params.component
    if (params?.from) p.from = params.from
    if (params?.to) p.to = params.to
    if (params?.q) p.q = params.q
    if (params?.limit) p.limit = String(params.limit)
    if (params?.offset) p.offset = String(params.offset)
    const qs = new URLSearchParams(p).toString()
    return request<LogEntry[]>(`/system/logs${qs ? '?' + qs : ''}`)
  },
  getLogLevel: () => request<{ level: string }>('/system/loglevel'),
  setLogLevel: (level: string) =>
    request<{ level: string }>('/system/loglevel', { method: 'PUT', body: JSON.stringify({ level }) }),
  getStorage: () =>
    request<{ downloadDir: string; audiobookDownloadDir: string; libraryDir: string; audiobookDir: string }>('/system/storage'),
}
