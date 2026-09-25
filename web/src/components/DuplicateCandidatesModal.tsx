import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, DuplicateCandidates, DuplicateRule } from '../api/client'
import { btn, btnSize } from './buttons'

interface Props {
  authorId: number
  authorName: string
  onClose: () => void
  // Called after a successful exclusion toggle so the page can refresh its
  // book lists (the modal re-fetches its own groups on its own).
  onChanged?: () => void
}

const ruleDefaults: Record<DuplicateRule, string> = {
  'alnum-equal': 'Identical once punctuation, case, and diacritics are ignored',
  'article-strip': 'Same title with a leading article (The, A, An…) dropped',
  'edition-suffix': 'Same title with an edition marker (Unabridged, Audiobook…) dropped',
  'substring': 'One title is contained in the other',
}

export default function DuplicateCandidatesModal({ authorId, authorName, onClose, onChanged }: Props) {
  const { t } = useTranslation()
  const [result, setResult] = useState<DuplicateCandidates | null>(null)
  const [loading, setLoading] = useState(true)
  const [busyBooks, setBusyBooks] = useState<Set<number>>(() => new Set())
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(() => {
    setLoading(true)
    setError(null)
    api.listAuthorDuplicateCandidates(authorId)
      .then(setResult)
      .catch(err => {
        setError(err instanceof Error ? err.message : t('duplicateCandidates.loadFailed', 'Loading duplicates failed'))
      })
      .finally(() => { setLoading(false) })
  }, [authorId, t])

  useEffect(() => { load() }, [load])

  const toggleExclusion = async (bookId: number) => {
    setBusyBooks(prev => new Set(prev).add(bookId))
    setError(null)
    try {
      await api.toggleExcluded(bookId)
      load()
      onChanged?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('duplicateCandidates.toggleFailed', 'Changing the exclusion failed'))
    } finally {
      setBusyBooks(prev => {
        const next = new Set(prev)
        next.delete(bookId)
        return next
      })
    }
  }

  const renderRules = (rules: DuplicateRule[]) => (
    <span className="flex flex-wrap gap-1">
      {rules.map(rule => (
        <span
          key={rule}
          className="rounded bg-slate-200 px-1.5 py-0.5 text-[11px] text-slate-600 dark:bg-zinc-800 dark:text-zinc-400"
        >
          {t(`duplicateCandidates.rules.${rule}`, ruleDefaults[rule])}
        </span>
      ))}
    </span>
  )

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div
        className="bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-700 rounded-lg shadow-xl p-6 w-full max-w-2xl mx-4 max-h-[85vh] flex flex-col"
        onClick={event => event.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="duplicate-candidates-title"
      >
        <h2 id="duplicate-candidates-title" className="text-base font-semibold text-slate-900 dark:text-white">
          {t('duplicateCandidates.title', 'Review duplicate titles')}
        </h2>
        <p className="mt-1 text-xs text-slate-500 dark:text-zinc-400">
          {t('duplicateCandidates.description', {
            author: authorName,
            defaultValue: 'Titles for {{author}} that look like the same book. Nothing changes until you exclude a row.',
          })}
        </p>

        {loading ? (
          <p className="mt-4 text-sm text-slate-500 dark:text-zinc-400">
            {t('duplicateCandidates.loading', 'Scanning the catalogue…')}
          </p>
        ) : error && !result ? (
          <p className="mt-4 text-sm text-red-600 dark:text-red-400">{error}</p>
        ) : result ? (
          result.count === 0 ? (
            <p className="mt-4 text-sm text-slate-600 dark:text-zinc-400">
              {t('duplicateCandidates.empty', 'No duplicate titles found.')}
            </p>
          ) : (
            <div className="mt-4 min-h-0 flex-1 overflow-y-auto rounded border border-slate-200 dark:border-zinc-800">
              {result.groups.map(group => (
                <section key={group.key} className="border-b border-slate-200 px-3 py-3 last:border-b-0 dark:border-zinc-800">
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-xs font-semibold text-slate-800 dark:text-zinc-200">
                      {t('duplicateCandidates.group', { count: group.books.length, defaultValue: '{{count}} row(s)' })}
                    </span>
                    {renderRules(group.rules)}
                  </div>
                  <ul className="mt-2 divide-y divide-slate-100 dark:divide-zinc-800">
                    {group.books.map(book => (
                      <li key={book.id} className="flex items-start justify-between gap-3 py-2 text-xs">
                        <span className="min-w-0">
                          <span
                            className={`block truncate font-medium ${book.excluded
                              ? 'text-slate-400 line-through dark:text-zinc-500'
                              : 'text-slate-800 dark:text-zinc-200'}`}
                          >
                            {book.title}
                          </span>
                          <span className="mt-0.5 flex flex-wrap items-center gap-1">
                            {book.excluded && (
                              <span className="rounded bg-amber-100 px-1.5 py-0.5 text-[11px] text-amber-700 dark:bg-amber-900/40 dark:text-amber-300">
                                {t('duplicateCandidates.excluded', 'Excluded')}
                              </span>
                            )}
                            {renderRules(book.rules)}
                          </span>
                        </span>
                        <button
                          type="button"
                          onClick={() => toggleExclusion(book.id)}
                          disabled={busyBooks.has(book.id)}
                          className={`${btn.secondary} ${btnSize.sm} shrink-0`}
                          aria-label={book.excluded
                            ? t('duplicateCandidates.include', 'Include')
                            : t('duplicateCandidates.exclude', 'Exclude')}
                        >
                          {book.excluded
                            ? t('duplicateCandidates.include', 'Include')
                            : t('duplicateCandidates.exclude', 'Exclude')}
                        </button>
                      </li>
                    ))}
                  </ul>
                </section>
              ))}
            </div>
          )
        ) : null}

        {error && result && <p className="mt-3 text-sm text-red-600 dark:text-red-400">{error}</p>}

        <div className="mt-4 flex justify-end gap-2 border-t border-slate-200 pt-4 dark:border-zinc-800">
          <button type="button" onClick={load} disabled={loading} className={`${btn.secondary} ${btnSize.sm}`}>
            {t('duplicateCandidates.refresh', 'Rescan')}
          </button>
          <button type="button" onClick={onClose} className={`${btn.secondary} ${btnSize.sm}`}>
            {t('common.close', 'Close')}
          </button>
        </div>
      </div>
    </div>
  )
}
