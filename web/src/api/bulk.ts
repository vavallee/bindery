import { request } from './core'
import type { Book } from './books'
import type { MediaType } from './authors'

export type AuthorBulkAction = 'monitor' | 'unmonitor' | 'delete' | 'search' | 'refresh' | 'set_media_type'
export type BookBulkAction = 'monitor' | 'unmonitor' | 'delete' | 'search' | 'set_media_type' | 'exclude'
export type WantedBulkAction = 'search' | 'blocklist' | 'unmonitor'

export interface BulkResult {
  results: Record<string, { ok: boolean; error?: string }>
}

export const bulkApi = {
  // Wanted
  listWanted: (opts?: { includeExcluded?: boolean }) => {
    const qs = opts?.includeExcluded ? '?includeExcluded=true' : ''
    return request<Book[]>(`/wanted/missing${qs}`)
  },

  // Bulk actions
  bulkActionAuthors: (ids: number[], action: AuthorBulkAction, mediaType?: MediaType) =>
    request<BulkResult>('/author/bulk', { method: 'POST', body: JSON.stringify({ ids, action, ...(mediaType ? { mediaType } : {}) }) }),
  searchAuthorWanted: (id: number) =>
    request<BulkResult>('/author/bulk', { method: 'POST', body: JSON.stringify({ ids: [id], action: 'search' }) }),
  bulkActionBooks: (ids: number[], action: BookBulkAction, mediaType?: MediaType) =>
    request<BulkResult>('/book/bulk', { method: 'POST', body: JSON.stringify({ ids, action, ...(mediaType ? { mediaType } : {}) }) }),
  bulkActionWanted: (ids: number[], action: WantedBulkAction) =>
    request<BulkResult>('/wanted/bulk', { method: 'POST', body: JSON.stringify({ ids, action }) }),
}
