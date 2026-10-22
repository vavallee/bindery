import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { primaryFill } from './buttons'

interface Props {
  // A short, context-specific line explaining why setup is needed here, e.g.
  // "configure these before adding authors" vs "...before searching". Used
  // when BOTH pieces are missing; a half-configured state overrides it with
  // the sharper "downloads will fail" / "searches find nothing" line.
  reasonKey: string
  needsIndexer: boolean
  needsClient: boolean
}

// First-run onboarding nudge shown when part of the search→download pipeline
// is unconfigured. Adding an author or searching silently does nothing
// without an indexer AND a download client, so this points the user at
// exactly the missing settings tab(s). Modelled on DiscoverPage's
// empty-state link-to-settings pattern (card surface, emerald primary
// action).
export default function GettingStartedGuidance({ reasonKey, needsIndexer, needsClient }: Props) {
  const { t } = useTranslation()
  if (!needsIndexer && !needsClient) return null

  // Both missing → the context reason. One missing → name the consequence.
  const reason = needsIndexer && needsClient
    ? t(reasonKey)
    : needsClient
      ? t('gettingStarted.clientMissing', 'An indexer is configured but there is no download client — searches will find releases, but every grab will fail.')
      : t('gettingStarted.indexerMissing', 'A download client is configured but there is no indexer — searches have nowhere to look and will find nothing.')

  return (
    <div className="max-w-xl mx-auto mb-8 px-5 py-4 bg-slate-100 dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 rounded-lg text-left">
      <h3 className="text-sm font-semibold text-slate-900 dark:text-white mb-1">
        {t('gettingStarted.title')}
      </h3>
      <p className="text-sm text-fg-muted mb-3">
        {reason}
      </p>
      <div className="flex flex-wrap gap-2">
        {needsIndexer && (
          <Link
            to="/settings?tab=indexers"
            className={`inline-block px-3 py-1.5 ${primaryFill} rounded-md text-sm font-medium transition-colors`}
          >
            {t('gettingStarted.indexers')}
          </Link>
        )}
        {needsClient && (
          <Link
            to="/settings?tab=clients"
            className={needsIndexer
              ? 'inline-block px-3 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 text-slate-900 dark:text-white rounded-md text-sm font-medium transition-colors'
              : `inline-block px-3 py-1.5 ${primaryFill} rounded-md text-sm font-medium transition-colors`}
          >
            {t('gettingStarted.downloadClients')}
          </Link>
        )}
      </div>
    </div>
  )
}
