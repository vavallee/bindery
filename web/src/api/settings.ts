import { request } from './core'

export interface HardcoverTestResult {
  ok: boolean
  tokenConfigured: boolean
  searchResults: number
  sampleSeriesId?: string
  sampleTitle?: string
  catalogOk: boolean
  catalogBookCount?: number
  message?: string
  error?: string
}

export const settingsApi = {
  // Settings
  listSettings: () => request<Array<{ key: string; value: string }>>('/setting'),
  getSetting: (key: string) => request<{ key: string; value: string }>(`/setting/${key}`),
  setSetting: (key: string, value: string) => request<void>(`/setting/${key}`, { method: 'PUT', body: JSON.stringify({ value }) }),
  testHardcover: () => request<HardcoverTestResult>('/hardcover/test', { method: 'POST' }),

  // Backup
  listBackups: () => request<Array<{ name: string; size: number; modTime: string }>>('/backup'),
  createBackup: () => request<{ name: string; size: number; modTime: string }>('/backup', { method: 'POST' }),
  deleteBackup: (filename: string) => request<void>(`/backup/${encodeURIComponent(filename)}`, { method: 'DELETE' }),
}
