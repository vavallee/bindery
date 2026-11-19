import { request } from './core'
import type { ABSImportProgress, ABSImportRun } from './abs'

// The BookOrbit import runs through the same import engine as Audiobookshelf,
// so its progress and run records have the same shape.

export interface BookOrbitConfig {
  baseUrl: string
  // Address a browser uses to reach BookOrbit. Empty means baseUrl.
  publicUrl: string
  libraryIds: string[]
  pathRemap: string
  apiKeyConfigured: boolean
}

export interface BookOrbitLibrary {
  id: string
  name: string
  folders: string[]
}

export interface BookOrbitTestResult {
  message: string
  username: string
  authenticationMethod: string
  libraryCount: number
}

function runsQuery(params?: { limit?: number; offset?: number }): string {
  const q = new URLSearchParams()
  if (params?.limit) q.set('limit', String(params.limit))
  if (params?.offset) q.set('offset', String(params.offset))
  return q.toString() ? `?${q.toString()}` : ''
}

export const bookOrbitApi = {
  bookOrbitConfig: () => request<BookOrbitConfig>('/bookorbit/config'),
  bookOrbitSetConfig: (data: { baseUrl: string; publicUrl?: string; libraryIds: string[]; pathRemap: string; apiKey?: string }) =>
    request<BookOrbitConfig>('/bookorbit/config', { method: 'PUT', body: JSON.stringify(data) }),
  bookOrbitTest: (data?: { apiKey?: string }) =>
    request<BookOrbitTestResult>('/bookorbit/test', { method: 'POST', body: JSON.stringify(data ?? {}) }),
  bookOrbitLibraries: (data?: { apiKey?: string }) =>
    request<BookOrbitLibrary[]>('/bookorbit/libraries', { method: 'POST', body: JSON.stringify(data ?? {}) }),
  bookOrbitImportStart: (data: { dryRun: boolean; libraryIds?: string[] }) =>
    request<ABSImportProgress>('/bookorbit/import', { method: 'POST', body: JSON.stringify(data) }),
  bookOrbitImportStatus: () => request<ABSImportProgress>('/bookorbit/import/status'),
  bookOrbitImportRuns: (params?: { limit?: number; offset?: number }) =>
    request<ABSImportRun[]>(`/bookorbit/import/runs${runsQuery(params)}`),
}
