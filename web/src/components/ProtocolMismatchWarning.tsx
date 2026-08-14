import { useTranslation } from 'react-i18next'
import type { Indexer } from '../api/indexers'
import type { DownloadClient } from '../api/downloadclients'
import { protocolGaps } from '../util/protocols'

// Amber warning shown on the Indexers/Clients settings tabs when an enabled
// indexer's protocol has no enabled download client to hand grabs to:
// searches will list releases, every grab fails. The pairing rule (Newznab →
// SABnzbd/NZBGet, Torznab → torrent client) previously lived only in
// QUICKSTART.md, so users met it as an unexplained grab failure.
//
// Pure over the lists SettingsPage already holds, so it re-evaluates on
// every save without extra fetches. Navigation goes through the same
// onNavigate callback the tabs use (not a router Link) — SettingsPage is
// deliberately renderable without a Router context.
export default function ProtocolMismatchWarning({
  indexers,
  clients,
  onNavigate,
}: {
  indexers: Indexer[]
  clients: DownloadClient[]
  onNavigate: (tab: string) => void
}) {
  const { t } = useTranslation()
  const gaps = protocolGaps(indexers, clients)
  if (gaps.length === 0) return null

  return (
    <div className="mb-4 px-4 py-3 rounded-lg border border-amber-300 dark:border-amber-700/60 bg-amber-50 dark:bg-amber-950/40 text-sm text-amber-900 dark:text-amber-200">
      {gaps.map(p => (
        <p key={p} className="[&:not(:first-child)]:mt-1">
          {p === 'torrent'
            ? t('protocolMismatch.torrent', 'A Torznab indexer is enabled but no torrent download client (qBittorrent, Transmission, Deluge, rTorrent) is — its releases can be found but never downloaded.')
            : t('protocolMismatch.usenet', 'A Newznab indexer is enabled but no usenet download client (SABnzbd, NZBGet) is — its releases can be found but never downloaded.')}
        </p>
      ))}
      <button
        onClick={() => onNavigate('clients')}
        className="inline-block mt-2 font-medium underline hover:no-underline"
      >
        {t('gettingStarted.downloadClients', 'Set up Download Clients')}
      </button>
    </div>
  )
}
