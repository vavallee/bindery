import type { TFunction } from 'i18next'
import type { LibraryRequest } from '../../api/client'

// Dispatched on window after an admin approves or declines, so the nav badge
// recounts pending requests without polling.
export const REQUESTS_CHANGED_EVENT = 'bindery:requests-changed'

// The one line status a request shows, shared by the requester's list and
// the admin queue so both say the same thing about the same row.
export function requestStatusLabel(t: TFunction, r: LibraryRequest): string {
  if (r.status === 'declined') return t('requests.status.declined')
  if (r.status === 'pending') return t('requests.status.pending')
  if (r.kind === 'author' && r.booksTotal) {
    return t('requests.status.authorProgress', { imported: r.booksImported ?? 0, total: r.booksTotal })
  }
  return r.fulfilled ? t('requests.status.available') : t('requests.status.approved')
}

export function requestStatusClass(r: LibraryRequest): string {
  if (r.status === 'declined') return 'bg-danger-subtle text-danger-strong'
  if (r.status === 'pending') return 'bg-warning-subtle text-warning-strong'
  return r.fulfilled
    ? 'bg-success-subtle text-success-strong'
    : 'bg-info-subtle text-info-strong'
}

export function mediaTypeLabel(t: TFunction, m: string): string {
  if (m === 'ebook') return t('common.ebook')
  if (m === 'audiobook') return t('common.audiobook')
  if (m === 'both') return t('common.both')
  return t('requests.anyFormat')
}
