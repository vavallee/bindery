import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Book } from '../api/client'

type Props = {
  sourceBookId: number
  path: string
  format: string
  onClose: () => void
  onReassigned: (targetBookId: number) => void
}

// FixMatchModal lets the user move a mis-matched file to the correct existing
// book (#1238). It searches the library and POSTs the chosen target to the
// reassign endpoint; the backend moves the file into the target's folder.
export default function FixMatchModal({ sourceBookId, path, format, onClose, onReassigned }: Props) {
  const { t } = useTranslation()
  const [term, setTerm] = useState('')
  const [results, setResults] = useState<Book[]>([])
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    const q = term.trim()
    if (q.length < 2) {
      setResults([])
      return
    }
    let cancelled = false
    setLoading(true)
    const handle = setTimeout(async () => {
      try {
        const { items } = await api.listBooks({ search: q, limit: 20 })
        if (!cancelled) setResults(items.filter(b => b.id !== sourceBookId))
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : 'search failed')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }, 300)
    return () => {
      cancelled = true
      clearTimeout(handle)
    }
  }, [term, sourceBookId])

  const reassign = async (target: Book) => {
    setSubmitting(true)
    setError('')
    try {
      await api.reassignFile({ path, targetBookId: target.id, format })
      onReassigned(target.id)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'reassign failed')
      setSubmitting(false)
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-4 pt-20"
      onClick={onClose}
      role="presentation"
    >
      <div
        className="w-full max-w-lg rounded-lg bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 shadow-xl"
        onClick={e => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
      >
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 className="text-base font-semibold">
            {t('bookDetail.fixMatch.title', 'Reassign file to another book')}
          </h3>
          <p className="mt-1 text-xs text-slate-500 dark:text-zinc-500 font-mono truncate" title={path}>
            {path}
          </p>
        </div>
        <div className="p-4">
          <input
            autoFocus
            type="text"
            value={term}
            onChange={e => setTerm(e.target.value)}
            placeholder={t('bookDetail.fixMatch.searchPlaceholder', 'Search your library for the correct book…')}
            className="w-full px-3 py-2 rounded border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-950 text-sm"
          />
          {error && <p className="mt-2 text-xs text-red-600 dark:text-red-400">{error}</p>}
          <div className="mt-3 max-h-72 overflow-y-auto divide-y divide-slate-100 dark:divide-zinc-800">
            {loading && <p className="py-3 text-xs text-slate-500 dark:text-zinc-500">{t('common.loading')}</p>}
            {!loading && term.trim().length >= 2 && results.length === 0 && (
              <p className="py-3 text-xs text-slate-500 dark:text-zinc-500">
                {t('bookDetail.fixMatch.noResults', 'No matching books in your library')}
              </p>
            )}
            {results.map(b => (
              <button
                key={b.id}
                type="button"
                disabled={submitting}
                onClick={() => reassign(b)}
                className="block w-full text-left py-2 px-1 rounded hover:bg-slate-100 dark:hover:bg-zinc-800 disabled:opacity-50"
              >
                <span className="text-sm font-medium text-slate-900 dark:text-white">{b.title}</span>
                {b.author?.authorName && (
                  <span className="text-xs text-slate-500 dark:text-zinc-500"> · {b.author.authorName}</span>
                )}
              </button>
            ))}
          </div>
        </div>
        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end">
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1.5 text-xs rounded bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700"
          >
            {t('common.cancel')}
          </button>
        </div>
      </div>
    </div>
  )
}
