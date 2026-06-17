import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { btn, btnSize } from './buttons'

// Discoverability CTA shown on empty Queue/Wanted states (#1184). New users who
// already have files on disk often don't realise Manual Import / Scan Library
// exist, so we point them straight at the existing flows rather than building a
// new import UI. Both targets are Settings sub-tabs (deep-linked via ?tab=…):
// Manual Import lives under Import / Migrate, Scan Library under General.
export default function ImportHints() {
  const { t } = useTranslation()
  return (
    <div className="mt-6 mx-auto max-w-md text-left rounded-lg border border-slate-200 dark:border-zinc-800 bg-slate-100 dark:bg-zinc-900 p-4">
      <p className="text-sm font-medium text-slate-700 dark:text-zinc-300">
        {t('importHints.heading')}
      </p>
      <p className="mt-1 text-xs text-slate-600 dark:text-zinc-500">
        {t('importHints.body')}
      </p>
      <div className="mt-3 flex flex-wrap gap-2">
        <Link to="/settings?tab=import" className={`${btn.primary} ${btnSize.md}`}>
          {t('importHints.manualImport')}
        </Link>
        <Link to="/settings?tab=general" className={`${btn.secondary} ${btnSize.md}`}>
          {t('importHints.scanLibrary')}
        </Link>
      </div>
    </div>
  )
}
