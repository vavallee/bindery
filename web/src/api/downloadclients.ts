import { request } from './core'

export interface DownloadClient {
  id: number
  name: string
  type: string
  host: string
  port: number
  apiKey: string
  username: string
  password: string
  useSsl: boolean
  urlBase: string
  category: string
  // categoryAudiobook overrides category for audiobook grabs only.
  // Optional; when empty (the default for pre-#700 rows) audiobook grabs
  // fall back to `category`.
  categoryAudiobook?: string
  pathRemap?: string
  enabled: boolean
  health?: DownloadClientHealth
}

export interface DownloadClientHealth {
  status: 'ok' | 'checking' | 'error'
  message: string
}

export const downloadClientsApi = {
  // Download clients
  listDownloadClients: () => request<DownloadClient[]>('/downloadclient'),
  addDownloadClient: (data: Partial<DownloadClient>) => request<DownloadClient>('/downloadclient', { method: 'POST', body: JSON.stringify(data) }),
  updateDownloadClient: (id: number, data: Partial<DownloadClient>) => request<DownloadClient>(`/downloadclient/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteDownloadClient: (id: number) => request<void>(`/downloadclient/${id}`, { method: 'DELETE' }),
  testDownloadClient: (id: number) => request<{ message: string; health?: DownloadClientHealth }>(`/downloadclient/${id}/test`, { method: 'POST' }),
  // Test an unsaved download-client config (Add/Edit form Test button). Does
  // not persist; mirrors testDownloadClient's response (minus async health).
  testDownloadClientConfig: (data: Partial<DownloadClient>) =>
    request<{ message: string }>('/downloadclient/test', { method: 'POST', body: JSON.stringify(data) }),
}
