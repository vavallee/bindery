import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'

interface Props {
  // A short, context-specific line explaining why setup is needed here, e.g.
  // "configure these before adding authors" vs "...before searching".
  reasonKey: string
}

// First-run onboarding nudge shown on the Authors/Books empty states when the
// instance has no indexers AND no download clients configured. Adding an author
// or searching silently does nothing without them, so we point the user at the
// two settings tabs they need first. Modelled on DiscoverPage's empty-state
// link-to-settings pattern (card surface, emerald primary action).
export default function GettingStartedGuidance({ reasonKey }: Props) {
  const { t } = useTranslation()
  return (
    <div className="max-w-xl mx-auto mb-8 px-5 py-4 bg-slate-100 dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 rounded-lg text-left">
      <h3 className="text-sm font-semibold text-slate-900 dark:text-white mb-1">
        {t('gettingStarted.title')}
      </h3>
      <p className="text-sm text-slate-600 dark:text-zinc-400 mb-3">
        {t(reasonKey)}
      </p>
      <div className="flex flex-wrap gap-2">
        <Link
          to="/settings?tab=indexers"
          className="inline-block px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 text-white rounded-md text-sm font-medium transition-colors"
        >
          {t('gettingStarted.indexers')}
        </Link>
        <Link
          to="/settings?tab=clients"
          className="inline-block px-3 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 text-slate-900 dark:text-white rounded-md text-sm font-medium transition-colors"
        >
          {t('gettingStarted.downloadClients')}
        </Link>
      </div>
    </div>
  )
}
