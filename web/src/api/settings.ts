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

// SettingType mirrors internal/api/settings_registry.go. It is the shape of a
// stored value, and it is what tells a generic settings screen which control to
// render.
export type SettingType = 'string' | 'bool' | 'int' | 'duration' | 'enum'

// SettingState mirrors internal/api/settings_registry.go. "active" is the only
// state a client may offer as an editable control: "internal" is bookkeeping
// Bindery writes for itself and "inert" is a key nothing reads any more.
export type SettingState = 'active' | 'internal' | 'inert'

// SettingDescriptor is one entry of GET /api/v1/settings/descriptors: what a
// key holds, what it defaults to, what it accepts, whether a change needs a
// restart, and how it is gated. The Advanced settings tab renders entirely from
// these, so a new key needs no control, no label and no locale entry.
export interface SettingDescriptor {
  key: string
  type: SettingType
  default: string
  values?: string[]
  min?: string
  max?: string
  description: string
  restartRequired: boolean
  state: SettingState
  secret: boolean
  adminOnly: boolean
  writable: boolean
}

export const settingsApi = {
  // Settings
  listSettings: () => request<Array<{ key: string; value: string }>>('/setting'),
  getSetting: (key: string) => request<{ key: string; value: string }>(`/setting/${key}`),
  setSetting: (key: string, value: string) => request<void>(`/setting/${key}`, { method: 'PUT', body: JSON.stringify({ value }) }),
  // Deleting a row restores the key's default, which is why the Advanced tab
  // calls this "Reset" rather than "Delete". Admin only server side, and
  // refused for secrets.
  deleteSetting: (key: string) => request<void>(`/setting/${encodeURIComponent(key)}`, { method: 'DELETE' }),
  // The descriptor registry. Admin only, and carries no stored values.
  listSettingDescriptors: () => request<SettingDescriptor[]>('/settings/descriptors'),
  testHardcover: () => request<HardcoverTestResult>('/hardcover/test', { method: 'POST' }),

  // Backup
  listBackups: () => request<Array<{ name: string; size: number; modTime: string }>>('/backup'),
  createBackup: (label?: string) =>
    request<{ name: string; size: number; modTime: string }>('/backup', {
      method: 'POST',
      body: label ? JSON.stringify({ label }) : undefined,
    }),
  deleteBackup: (filename: string) => request<void>(`/backup/${encodeURIComponent(filename)}`, { method: 'DELETE' }),
}
