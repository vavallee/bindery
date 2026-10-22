import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { resolveBookQuery } from '../../api/booklookup'
import type { Book } from '../../api/client'
import { btn, btnSize } from '../buttons'

interface Props {
  // Prefilled from what the scan read out of the file.
  initialQuery: string
  // Adds the chosen result somehow (add then import, or adopt). A thrown
  // error is shown inline.
  onChoose: (b: Book) => Promise<void>
  // Sentence under the search box saying what choosing a result does.
  hint?: string
  actionLabel?: string
  busyLabel?: string
}

// CatalogueAdder resolves the case BookPicker cannot: the file is for a book
// that is not in the library at all, so there is no row to bind it to (#1719).
// It searches provider metadata (the same ISBN / ASIN / free-text dispatch the
// Add Book modal uses) and hands the chosen result to onChoose.
//
// It starts collapsed, and a provider is asked only when the person submits
// the search. Opening it, typing and rendering never spend provider quota.
export default function CatalogueAdder({ initialQuery, onChoose, hint, actionLabel, busyLabel }: Props) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [term, setTerm] = useState(initialQuery)
  const [results, setResults] = useState<Book[]>([])
  const [searching, setSearching] = useState(false)
  const [searched, setSearched] = useState(false)
  const [adding, setAdding] = useState('')
  const [error, setError] = useState('')

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-expanded={false}
        className="text-xs text-fg-muted hover:underline"
      >
        {t('manualImport.metadataOpen', 'Not in your library? Search metadata')}
      </button>
    )
  }

  const search = async () => {
    const q = term.trim()
    if (!q) return
    setSearching(true)
    setError('')
    try {
      setResults(await resolveBookQuery(q))
      setSearched(true)
    } catch (e) {
      setResults([])
      setError(e instanceof Error ? e.message : 'Metadata search failed')
    } finally {
      setSearching(false)
    }
  }

  const choose = async (b: Book) => {
    if (!b.foreignBookId || !b.author?.authorName) return
    setAdding(b.foreignBookId)
    setError('')
    try {
      await onChoose(b)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not add the book')
    } finally {
      setAdding('')
    }
  }

  return (
    <div className="rounded border border-slate-200 dark:border-zinc-800 p-2">
      <form
        className="flex gap-2"
        onSubmit={e => { e.preventDefault(); void search() }}
      >
        <input
          type="text"
          value={term}
          onChange={e => setTerm(e.target.value)}
          placeholder={t('manualImport.metadataPlaceholder', 'Title, author, ISBN, or ASIN')}
          aria-label={t('manualImport.metadataLabel', 'Search metadata')}
          className="flex-1 px-2 py-1 rounded border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-950 text-xs"
        />
        <button
          type="submit"
          disabled={searching || !term.trim()}
          className={`${btn.secondary} ${btnSize.sm}`}
        >
          {searching ? t('manualImport.metadataSearching', 'Searching…') : t('manualImport.metadataSearch', 'Search')}
        </button>
        <button
          type="button"
          onClick={() => setOpen(false)}
          className="text-xs text-fg-muted hover:underline"
        >
          {t('common.cancel', 'Cancel')}
        </button>
      </form>
      <p className="mt-1 text-[11px] text-fg-muted">
        {hint ?? t('manualImport.metadataHint', 'Adds the book to your library and links this file to it. No indexer search is started.')}
      </p>
      {error && <p role="alert" className="mt-1 text-xs text-red-600 dark:text-red-400">{error}</p>}
      <div className="mt-2 max-h-48 overflow-y-auto divide-y divide-slate-100 dark:divide-zinc-800">
        {!searching && searched && results.length === 0 && !error && (
          <p className="py-2 text-xs text-fg-muted">
            {t('manualImport.metadataNoResults', 'No metadata results for that search.')}
          </p>
        )}
        {results.map(b => {
          // An author NAME is enough; the backend resolves a missing author id.
          // Without one there is nothing to create the book under.
          const canAdd = Boolean(b.author?.authorName)
          return (
            <div key={b.foreignBookId || b.title} className="flex items-center gap-2 py-1.5">
              <div className="min-w-0 flex-1">
                <span className="text-xs font-medium text-slate-900 dark:text-white">{b.title}</span>
                {b.author?.authorName && (
                  <span className="text-[11px] text-fg-muted"> · {b.author.authorName}</span>
                )}
              </div>
              <button
                type="button"
                onClick={() => choose(b)}
                disabled={!canAdd || adding !== ''}
                title={canAdd ? undefined : t('manualImport.metadataAuthorMissing', 'Author name missing on this result')}
                className={`${btn.secondary} ${btnSize.sm} flex-shrink-0`}
              >
                {adding === b.foreignBookId
                  ? (busyLabel ?? t('manualImport.metadataAdding', 'Adding…'))
                  : (actionLabel ?? t('manualImport.metadataAdd', 'Add and use'))}
              </button>
            </div>
          )
        })}
      </div>
    </div>
  )
}
