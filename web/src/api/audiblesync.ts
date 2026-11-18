import { request } from './core'

export interface AudibleSyncConfig {
  enabled: boolean
  baseUrl: string
  apiTokenConfigured: boolean
  // 0 means Audible Sync's own default (its oldest signed in account).
  accountId: number
}

export interface AudibleSyncAccount {
  id: number
  name: string
  countryCode: string
  enabled: boolean
  needsLogin: boolean
}

export interface AudibleSyncTestResult {
  ok: boolean
  message: string
  version?: string
  accounts?: AudibleSyncAccount[]
}

export const audibleSyncApi = {
  audibleSyncConfig: () => request<AudibleSyncConfig>('/audiblesync/config'),
  audibleSyncSetConfig: (data: {
    enabled?: boolean
    baseUrl?: string
    apiToken?: string
    accountId?: number
  }) => request<AudibleSyncConfig>('/audiblesync/config', { method: 'PUT', body: JSON.stringify(data) }),
  // Both probe the saved URL; a token passed here is tried in place of the
  // stored one, so it can be checked before it is saved.
  audibleSyncTest: (data?: { apiToken?: string }) =>
    request<AudibleSyncTestResult>('/audiblesync/test', { method: 'POST', body: JSON.stringify(data ?? {}) }),
  audibleSyncAccounts: (data?: { apiToken?: string }) =>
    request<AudibleSyncAccount[]>('/audiblesync/accounts', { method: 'POST', body: JSON.stringify(data ?? {}) }),
}
