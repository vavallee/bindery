import { request } from './core'

export interface GrimmoryConfig {
  enabled: boolean
  baseUrl: string
  apiKeyConfigured: boolean
}

export interface GrimmoryTestResult {
  ok: boolean
  message: string
  version?: string
}

export const grimmoryApi = {
  // Grimmory
  grimmoryConfig: () => request<GrimmoryConfig>('/grimmory/config'),
  grimmorySetConfig: (data: { enabled?: boolean; baseUrl?: string; apiKey?: string }) =>
    request<GrimmoryConfig>('/grimmory/config', { method: 'PUT', body: JSON.stringify(data) }),
  grimmoryTest: (data?: { baseUrl?: string; apiKey?: string }) =>
    request<GrimmoryTestResult>('/grimmory/test', { method: 'POST', body: JSON.stringify(data ?? {}) }),
}
