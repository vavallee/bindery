import { request } from './core'
import type { BookRef, Page } from './common'

export interface HistoryEvent {
  id: number
  bookId?: number
  eventType: string
  sourceTitle: string
  data: string
  createdAt: string
  book?: BookRef
}

export interface BlocklistEntry {
  id: number
  bookId?: number
  guid: string
  title: string
  indexerId?: number
  reason: string
  createdAt: string
  // D4b audit. Surfaces who promoted this row into the blocklist. NULL
  // for system-written rows (scheduler stall-detection, readarr migration)
  // and for legacy rows that predate migration 049. Audit only; the list
  // semantics remain global. The admin "blocklisted by X" UI consuming
  // this field is a future task.
  createdByUserId?: number | null
}

export const historyApi = {
  // History
  listHistory: (params?: { bookId?: number; eventType?: string; limit?: number; offset?: number }) => {
    const q = new URLSearchParams()
    if (params?.bookId) q.set('bookId', String(params.bookId))
    if (params?.eventType) q.set('eventType', params.eventType)
    if (params?.limit !== undefined) q.set('limit', String(params.limit))
    if (params?.offset !== undefined) q.set('offset', String(params.offset))
    const qs = q.toString()
    return request<Page<HistoryEvent>>(`/history${qs ? '?' + qs : ''}`)
  },
  deleteHistory: (id: number) => request<void>(`/history/${id}`, { method: 'DELETE' }),
  blocklistFromHistory: (id: number) => request<BlocklistEntry>(`/history/${id}/blocklist`, { method: 'POST' }),

  // Blocklist
  listBlocklist: () => request<BlocklistEntry[]>('/blocklist'),
  deleteBlocklistEntry: (id: number) => request<void>(`/blocklist/${id}`, { method: 'DELETE' }),
  bulkDeleteBlocklist: (ids: number[]) => request<void>('/blocklist/bulk', { method: 'DELETE', body: JSON.stringify({ ids }) }),
}
