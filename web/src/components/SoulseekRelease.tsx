import { useTranslation } from 'react-i18next'
import type { SearchResult } from '../api/books'

// A Soulseek result comes from a peer through slskd, not from an indexer
// (#1717). It is shown beside the indexer results but cannot be grabbed yet:
// sending a download to slskd arrives in a later release.
export function isSoulseek(r: Pick<SearchResult, 'protocol'>): boolean {
  return r.protocol === 'soulseek'
}

// SoulseekBadge is the source badge on a Soulseek result.
export function SoulseekBadge() {
  const { t } = useTranslation()
  return (
    <span
      data-testid="soulseek-badge"
      className="px-1.5 py-0 rounded bg-sky-100 dark:bg-sky-950/50 text-sky-800 dark:text-sky-300 text-[10px] font-medium uppercase tracking-wide flex-shrink-0"
    >
      {t('search.soulseek.badge')}
    </span>
  )
}

// SoulseekDetail appends the peer's name, folder size in files, and whether
// the transfer would start now or wait in the peer's queue. Peer supplied text
// renders as plain text.
export function SoulseekDetail({ r }: { r: SearchResult }) {
  const { t } = useTranslation()
  const s = r.soulseek
  if (!s) return null
  return (
    <>
      {' · '}
      <span data-testid="soulseek-user">{s.username}</span>
      {' · '}
      {t('search.soulseek.files', { count: s.fileCount })}
      {' · '}
      <span data-testid="soulseek-slot">
        {s.freeSlot ? t('search.soulseek.freeSlot') : t('search.soulseek.queued', { count: s.queueLength })}
      </span>
    </>
  )
}

// useSoulseekGrabTitle is the tooltip of the disabled Grab button.
export function useSoulseekGrabTitle(): string {
  const { t } = useTranslation()
  return t('search.soulseek.grabUnavailable')
}
