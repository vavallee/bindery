import { request } from './core'

export interface GrimmoryConfig {
  enabled: boolean
  baseUrl: string
  apiKeyConfigured: boolean
  username: string
  passwordConfigured: boolean
}

export interface GrimmoryTestResult {
  ok: boolean
  message: string
  version?: string
}

export interface GrimmorySyncStats {
  total: number
  processed: number
  pushed: number
  alreadyPushed: number
  failed: number
}

export interface GrimmorySyncError {
  bookId: number
  title: string
  path?: string
  reason: string
}

export interface GrimmorySyncStatus {
  running: boolean
  startedAt: string
  finishedAt?: string
  message?: string
  error?: string
  stats: GrimmorySyncStats
  errors: GrimmorySyncError[]
  totalPushedFiles: number
  lastPushedAt?: string
}

export const grimmoryApi = {
  // Grimmory
  grimmoryConfig: () => request<GrimmoryConfig>('/grimmory/config'),
  grimmorySetConfig: (data: {
    enabled?: boolean
    baseUrl?: string
    apiKey?: string
    username?: string
    password?: string
  }) => request<GrimmoryConfig>('/grimmory/config', { method: 'PUT', body: JSON.stringify(data) }),
  grimmoryTest: (data?: { baseUrl?: string; apiKey?: string; username?: string; password?: string }) =>
    request<GrimmoryTestResult>('/grimmory/test', { method: 'POST', body: JSON.stringify(data ?? {}) }),
  grimmorySync: () => request<GrimmorySyncStatus>('/grimmory/sync', { method: 'POST' }),
  grimmorySyncStatus: () => request<GrimmorySyncStatus>('/grimmory/sync/status'),
}
