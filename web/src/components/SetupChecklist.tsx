import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, type SetupState } from '../api/client'

const STORAGE_KEY = 'bindery.setupChecklistDismissed'

interface Step {
  key: keyof Omit<SetupState, 'complete'>
  labelKey: string
  fallback: string
  to?: string
  actionKey?: string
  actionFallback?: string
}

// Ordered as the pipeline runs. Account creation is deliberately absent:
// the user cannot be looking at this without having done it, so listing it
// would spend the first line of the checklist on a step that is always
// already ticked.
const STEPS: Step[] = [
  {
    key: 'hasIndexer',
    labelKey: 'setupChecklist.indexer',
    fallback: 'Add an indexer',
    to: '/settings?tab=indexers',
    actionKey: 'gettingStarted.indexers',
    actionFallback: 'Set up Indexers',
  },
  {
    key: 'hasClient',
    labelKey: 'setupChecklist.client',
    fallback: 'Add a download client',
    to: '/settings?tab=clients',
    actionKey: 'gettingStarted.downloadClients',
    actionFallback: 'Set up Download Clients',
  },
  { key: 'hasAuthor', labelKey: 'setupChecklist.author', fallback: 'Add an author' },
  { key: 'hasGrab', labelKey: 'setupChecklist.grab', fallback: 'Grab a book' },
  { key: 'hasImport', labelKey: 'setupChecklist.import', fallback: 'First book imported' },
]

// First-run progress checklist, shown on the Authors page until the
// pipeline has produced an import. This is the "your setup works"
// confirmation the app never had: previously the only signal that setup
// succeeded was a download appearing hours later, so a user who had
// mis-wired something had no way to tell the difference between "working,
// still searching" and "silently broken".
//
// Auto-hides for good once complete; also dismissible early for users who
// deliberately run a partial setup (e.g. catalogue-only, no downloads).
export default function SetupChecklist() {
  const { t } = useTranslation()
  const [state, setState] = useState<SetupState | null>(null)
  const [dismissed, setDismissed] = useState(() => {
    try {
      return localStorage.getItem(STORAGE_KEY) === '1'
    } catch {
      return false
    }
  })

  useEffect(() => {
    let cancelled = false
    api.setupState()
      .then(s => { if (!cancelled) setState(s) })
      .catch(() => { /* leave hidden — never block the page on this */ })
    return () => { cancelled = true }
  }, [])

  if (!state || state.complete || dismissed) return null

  const done = STEPS.filter(s => state[s.key]).length
  const next = STEPS.find(s => !state[s.key])

  // Stop nagging once all but one step is done. The server's `complete` is the
  // real definition of "set up" and already hides this; it counts indexer,
  // client, author and import, deliberately not the first grab. This second
  // threshold covers the remaining near-done shapes, where a checklist is more
  // clutter than help and the last step tends to happen on its own.
  if (done >= STEPS.length - 1) return null

  const dismiss = () => {
    try {
      localStorage.setItem(STORAGE_KEY, '1')
    } catch {
      // localStorage unavailable — dismiss for this render only.
    }
    setDismissed(true)
  }

  return (
    <div className="mb-6 flex items-center gap-3 px-3 py-2 rounded-lg bg-slate-100 dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 text-sm">
      <span className="font-medium text-slate-900 dark:text-white whitespace-nowrap">
        {t('setupChecklist.title', 'Setup progress')}
      </span>

      {/* Segmented progress, one segment per step. Carries the count for
          assistive tech so the bar is not the only way to read it. */}
      <span
        className="hidden sm:flex items-center gap-1"
        role="img"
        aria-label={t('setupChecklist.progress', '{{done}} of {{total}} steps done', { done, total: STEPS.length })}
      >
        {STEPS.map(step => (
          <span
            key={step.key}
            className={`h-1.5 w-6 rounded-full ${state[step.key] ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
          />
        ))}
      </span>
      <span className="text-fg-muted whitespace-nowrap sm:hidden">{done}/{STEPS.length}</span>

      {next && (
        <span className="min-w-0 flex items-center gap-2 truncate">
          <span className="text-fg-muted whitespace-nowrap">{t('setupChecklist.next', 'Next:')}</span>
          <span className="text-slate-800 dark:text-zinc-200 truncate">{t(next.labelKey, next.fallback)}</span>
          {next.to && (
            <Link to={next.to} className="text-emerald-700 dark:text-emerald-400 hover:underline whitespace-nowrap">
              {t(next.actionKey!, next.actionFallback!)}
            </Link>
          )}
        </span>
      )}

      <button
        onClick={dismiss}
        className="ml-auto text-xs text-fg-muted hover:text-slate-900 dark:hover:text-white transition-colors whitespace-nowrap"
      >
        {t('common.dismiss', 'Dismiss')}
      </button>
    </div>
  )
}
