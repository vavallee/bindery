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
}

export interface ReorganizeResponse {
  moves: ReorganizeMove[]
  summary: ReorganizeSummary
}

export type ReorganizeScope = 'book' | 'author' | 'library'

export const reorganizeApi = {
  preview: (scope: ReorganizeScope, id?: number) => {
    const q = new URLSearchParams({ scope })
    if (id != null) q.set('id', String(id))
    return request<ReorganizeResponse>(`/reorganize/preview?${q.toString()}`)
  },
  apply: (fileIds: number[]) =>
    request<ReorganizeResponse>('/reorganize/apply', {
      method: 'POST',
      body: JSON.stringify({ fileIds }),
    }),
}
