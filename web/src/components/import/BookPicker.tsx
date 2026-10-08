import { forwardRef, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { Book } from '../../api/client'

interface Props {
  onPick: (b: Book) => void
  onCancel?: () => void
  // Prefills the search, so a person corrects a query instead of typing one.
  initialTerm?: string
  // Marks one result as the current choice (the adoption editor's radio rows).
  selectedId?: number | null
}

// BookPicker is a debounced search over the EXISTING catalogue (the same
// /book?search= endpoint FixMatchModal uses). It resolves a file to a book
// already in the library. A book that is not in the library at all is
// CatalogueAdder's job. Only the local catalogue is searched here, never a
// metadata provider.
const BookPicker = forwardRef<HTMLInputElement, Props>(function BookPicker({ onPick, onCancel, initialTerm = '', selectedId }, ref) {
  const { t } = useTranslation()
  const [term, setTerm] = useState(initialTerm)
  const [results, setResults] = useState<Book[]>([])
  const [loading, setLoading] = useState(false)

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
        if (!cancelled) setResults(items)
      } catch {
        if (!cancelled) setResults([])
      } finally {
        if (!cancelled) setLoading(false)
      }
    }, 300)
    return () => { cancelled = true; clearTimeout(handle) }
  }, [term])

  return (
    <div className="rounded border border-slate-200 dark:border-zinc-800 p-2">
      <div className="flex gap-2">
        <input
          enterKeyHint="search"
          ref={ref}
          type="text"
          value={term}
          onChange={e => setTerm(e.target.value)}
          placeholder={t('manualImport.searchPlaceholder', 'Search your library for the book…')}
          aria-label={t('manualImport.searchLabel', 'Search your library')}
          className="flex-1 px-2 py-1 rounded border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-950 text-xs"
        />
        {onCancel && (
          <button
            type="button"
            onClick={onCancel}
            className="text-xs text-slate-500 dark:text-zinc-400 hover:underline"
          >
            {t('common.cancel', 'Cancel')}
          </button>
        )}
      </div>
      <div className="mt-2 max-h-48 overflow-y-auto divide-y divide-slate-100 dark:divide-zinc-800">
        {loading && <p className="py-2 text-xs text-slate-500 dark:text-zinc-500">{t('common.loading', 'Loading…')}</p>}
        {!loading && term.trim().length >= 2 && results.length === 0 && (
          <p className="py-2 text-xs text-slate-500 dark:text-zinc-500">
            {t('manualImport.noResults', 'No matching books in your library.')}
          </p>
        )}
        {results.map(b => (
          <button
            key={b.id}
            type="button"
            onClick={() => onPick(b)}
            aria-pressed={selectedId === undefined ? undefined : selectedId === b.id}
            className={`block w-full text-left py-1.5 px-1 rounded hover:bg-slate-100 dark:hover:bg-zinc-800 ${selectedId === b.id ? 'bg-emerald-500/10' : ''}`}
          >
            <span className="text-xs font-medium text-slate-900 dark:text-white">{b.title}</span>
            {b.author?.authorName && (
              <span className="text-[11px] text-slate-500 dark:text-zinc-500"> · {b.author.authorName}</span>
            )}
          </button>
        ))}
      </div>
    </div>
  )
})

export default BookPicker
