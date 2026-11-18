import { request } from './core'

export interface AudibleSyncConfig {
  enabled: boolean
  baseUrl: string
  apiTokenConfigured: boolean
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

// One list an Audible Sync account offers, for the Import tab's picker: its
// wishlist or its unavailable books (titles Audible no longer lets it
// download, typically withdrawn from Plus). booksCount is -1 when unknown.
export interface AudibleSyncList {
  accountId: number
  accountName: string
  countryCode: string
  needsLogin: boolean
  source: 'wishlist' | 'unavailable'
  booksCount: number
}

export const audibleSyncApi = {
  audibleSyncConfig: () => request<AudibleSyncConfig>('/audiblesync/config'),
  audibleSyncSetConfig: (data: {
    enabled?: boolean
    baseUrl?: string
    apiToken?: string
  }) => request<AudibleSyncConfig>('/audiblesync/config', { method: 'PUT', body: JSON.stringify(data) }),
  // Both probe the saved URL; a token passed here is tried in place of the
  // stored one, so it can be checked before it is saved.
  audibleSyncTest: (data?: { apiToken?: string }) =>
    request<AudibleSyncTestResult>('/audiblesync/test', { method: 'POST', body: JSON.stringify(data ?? {}) }),
  audibleSyncLists: () => request<AudibleSyncList[]>('/importlist/audiblesync/lists'),
  audibleSyncAccounts: (data?: { apiToken?: string }) =>
    request<AudibleSyncAccount[]>('/audiblesync/accounts', { method: 'POST', body: JSON.stringify(data ?? {}) }),
}
