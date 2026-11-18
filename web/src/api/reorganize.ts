import { request } from './core'

export type ReorganizeStatus =
  | 'move'
  | 'noop'
  | 'collision'
  | 'missing'
  | 'error'
  | 'moved'
  | 'failed'

export interface ReorganizeMove {
  bookId: number
  fileId: number
  format: string
  bookTitle: string
  author: string
  current: string
  proposed: string
  status: ReorganizeStatus
  message?: string
  // metadata.opf action when the sidecar setting is on (#4008).
  sidecar?: 'write' | 'written' | 'unchanged' | 'foreign' | 'failed' | 'shared'
  sidecarMessage?: string
}

export interface ReorganizeSummary {
  total: number
  toMove: number
  noop: number
  collision: number
  missing: number
  errored: number
  moved: number
  failed: number
  sidecarToWrite?: number
  sidecarWritten?: number
  sidecarUnchanged?: number
  sidecarForeign?: number
  sidecarFailed?: number
  sidecarShared?: number
}

export interface ReorganizeResponse {
  moves: ReorganizeMove[]
  summary: ReorganizeSummary
  // Set on a preview when the plan holds files of a format in External import
  // mode, where another tool owns the layout (#3946).
  externalMode?: boolean
  externalFormats?: string[]
  warning?: string
}

export type ReorganizeScope = 'book' | 'author' | 'library'

export const reorganizeApi = {
  preview: (scope: ReorganizeScope, id?: number) => {
    const q = new URLSearchParams({ scope })
    if (id != null) q.set('id', String(id))
    return request<ReorganizeResponse>(`/reorganize/preview?${q.toString()}`)
  },
  apply: (fileIds: number[], confirmExternalMode = false) =>
    request<ReorganizeResponse>('/reorganize/apply', {
      method: 'POST',
      body: JSON.stringify(confirmExternalMode ? { fileIds, confirmExternalMode } : { fileIds }),
    }),
}
