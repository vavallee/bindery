import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { btn, btnSize } from './buttons'

// Discoverability CTA shown on empty Queue/Wanted states (#1184). New users who
// already have files on disk often don't realise Bindery can take them in, so
// we point them straight at the Import page: its folder view for files
// elsewhere, its library view for files already in the library folder.
export default function ImportHints() {
  const { t } = useTranslation()
  return (
    <div className="mt-6 mx-auto max-w-md text-left rounded-lg border border-slate-200 dark:border-zinc-800 bg-slate-100 dark:bg-zinc-900 p-4">
      <p className="text-sm font-medium text-slate-700 dark:text-zinc-300">
        {t('importHints.heading')}
      </p>
      <p className="mt-1 text-xs text-fg-muted">
        {t('importHints.body')}
      </p>
      <div className="mt-3 flex flex-wrap gap-2">
        <Link to="/import?view=folder" className={`${btn.primary} ${btnSize.md}`}>
          {t('importHints.manualImport')}
        </Link>
        <Link to="/import" className={`${btn.secondary} ${btnSize.md}`}>
          {t('importHints.scanLibrary')}
        </Link>
      </div>
    </div>
  )
}
