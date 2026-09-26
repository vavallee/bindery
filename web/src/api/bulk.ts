import { request } from './core'
import type { Book } from './books'
import type { AuthorMonitorMode, MediaType, MonitorNewItems } from './authors'

export type AuthorBulkAction = 'monitor' | 'unmonitor' | 'delete' | 'search' | 'refresh' | 'set_media_type'
export type AuthorBulkMonitorMode = Exclude<AuthorMonitorMode, 'series'>
export type BookBulkAction = 'monitor' | 'unmonitor' | 'delete' | 'search' | 'set_media_type' | 'exclude'
export type WantedBulkAction = 'search' | 'blocklist' | 'unmonitor'

export interface BulkResult {
  // `code` is a stable machine readable reason for a failed entry, present
  // only where the client should react to the specific cause. Today the one
  // value is 'auto_grab_disabled' (#2669); see util/autoGrabRefusal.
  results: Record<string, { ok: boolean; error?: string; code?: string }>
}

export interface BulkSetAuthorMonitorModeOptions {
  monitorLatestCount?: number
  applyMonitorModeToExisting?: boolean
  // Omit to leave each author's existing value alone. Monitor mode and
  // monitor-new-items are independent settings on the server (#2065).
  monitorNewItems?: MonitorNewItems
}

export const bulkApi = {
  // Wanted
  listWanted: (opts?: { includeExcluded?: boolean }) => {
    const qs = opts?.includeExcluded ? '?includeExcluded=true' : ''
    return request<Book[]>(`/wanted/missing${qs}`)
  },

  // Bulk actions
  // `applyMonitorModeToExisting` applies only to 'monitor' and 'unmonitor',
  // where it rewrites each author's existing books to match the author's new
  // monitoring (#2742). Omitted means false, so the action stays a pure author
  // level write, matching the unticked-by-default box on the single author
  // path.
  bulkActionAuthors: (ids: number[], action: AuthorBulkAction, mediaType?: MediaType, applyMonitorModeToExisting?: boolean) =>
    request<BulkResult>('/author/bulk', {
      method: 'POST',
      body: JSON.stringify({
        ids,
        action,
        ...(mediaType ? { mediaType } : {}),
        ...(applyMonitorModeToExisting ? { applyMonitorModeToExisting: true } : {}),
      }),
    }),
  bulkSetAuthorMonitorMode: (ids: number[], monitorMode: AuthorBulkMonitorMode, opts: BulkSetAuthorMonitorModeOptions = {}) =>
    request<BulkResult>('/author/bulk', {
      method: 'POST',
      body: JSON.stringify({
        ids,
        action: 'set_monitor_mode',
        monitorMode,
        ...(opts.monitorLatestCount !== undefined ? { monitorLatestCount: opts.monitorLatestCount } : {}),
        ...(opts.applyMonitorModeToExisting !== undefined ? { applyMonitorModeToExisting: opts.applyMonitorModeToExisting } : {}),
        ...(opts.monitorNewItems !== undefined ? { monitorNewItems: opts.monitorNewItems } : {}),
      }),
    }),
  searchAuthorWanted: (id: number) =>
    request<BulkResult>('/author/bulk', { method: 'POST', body: JSON.stringify({ ids: [id], action: 'search' }) }),
  // #2668: the automatic search for one book. There is deliberately no
  // /book/{id}/autosearch route behind this. POST /book/{id}/search is the
  // interactive path (it returns releases and grabs nothing), and the only
  // server entry point into scheduler.SearchAndGrabBook from the API is the
  // bulk handler, which already checks ownership per id and already refuses
  // with code 'auto_grab_disabled' when the global switch is off. A dedicated
  // alias would have to repeat both guards, and a guard that exists twice is a
  // guard that can drift, so this posts the same body the author page's
  // searchAuthorWanted above does, with one id.
  searchBookAutomatic: (id: number) =>
    request<BulkResult>('/book/bulk', { method: 'POST', body: JSON.stringify({ ids: [id], action: 'search' }) }),
  bulkActionBooks: (ids: number[], action: BookBulkAction, mediaType?: MediaType) =>
    request<BulkResult>('/book/bulk', { method: 'POST', body: JSON.stringify({ ids, action, ...(mediaType ? { mediaType } : {}) }) }),
  bulkActionWanted: (ids: number[], action: WantedBulkAction) =>
    request<BulkResult>('/wanted/bulk', { method: 'POST', body: JSON.stringify({ ids, action }) }),
}
