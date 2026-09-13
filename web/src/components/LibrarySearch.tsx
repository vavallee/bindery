import { useEffect, useId, useRef, useState } from 'react'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, LibrarySearchResponse } from '../api/client'
import AddToLibraryModal, { AddedToLibrary } from './AddToLibraryModal'

// LibrarySearch is the header typeahead over the caller's own catalogue
// (#2551). It only ever talks to /search/library; the metadata providers are
// reached through the trailing "Add … to Bindery" row, which hands the query
// to AddToLibraryModal. Indexer search stays on /search (the magnifier).
//
// The row model is one flat list so keyboard navigation does not care about
// the section a row sits in: authors, then books, then series, then the add
// row, which is always present while there is a query.

const DEBOUNCE_MS = 300

type Row =
  | { kind: 'author'; id: number; label: string; sub?: string; imageUrl?: string }
  | { kind: 'book'; id: number; label: string; sub?: string; imageUrl?: string; authorId: number }
  | { kind: 'series'; id: number; label: string }
  | { kind: 'add'; label: string }

interface Props {
  className?: string
  // Fired after a row navigates, so the mobile menu can close itself.
  onNavigate?: () => void
  autoFocus?: boolean
}

function toRows(res: LibrarySearchResponse, addLabel: string): Row[] {
  const rows: Row[] = []
  for (const a of res.authors) rows.push({ kind: 'author', id: a.id, label: a.name, imageUrl: a.imageUrl })
  for (const b of res.books) rows.push({ kind: 'book', id: b.id, label: b.title, sub: b.authorName, imageUrl: b.imageUrl, authorId: b.authorId })
  for (const s of res.series) rows.push({ kind: 'series', id: s.id, label: s.title })
  rows.push({ kind: 'add', label: addLabel })
  return rows
}

export default function LibrarySearch({ className = '', onNavigate, autoFocus }: Props) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const listId = useId()
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<LibrarySearchResponse | null>(null)
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [addQuery, setAddQuery] = useState<string | null>(null)
  const rootRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  // Monotonic request counter: a slow response for "du" must not overwrite
  // the results for "dune" that arrived before it.
  const seq = useRef(0)

  const trimmed = query.trim()

  useEffect(() => {
    if (!trimmed) {
      seq.current++
      setResults(null)
      setLoading(false)
      setError(null)
      setOpen(false)
      setActive(-1)
      return
    }
    const mine = ++seq.current
    setLoading(true)
    const timer = window.setTimeout(() => {
      api.searchLibrary(trimmed)
        .then(res => {
          if (mine !== seq.current) return
          setResults(res)
          setError(null)
          setOpen(true)
          setActive(-1)
        })
        .catch(err => {
          if (mine !== seq.current) return
          setResults(null)
          setError(err instanceof Error ? err.message : t('librarySearch.failed'))
          setOpen(true)
        })
        .finally(() => {
          if (mine === seq.current) setLoading(false)
        })
    }, DEBOUNCE_MS)
    return () => window.clearTimeout(timer)
  }, [trimmed, t])

  // Click outside closes the dropdown without clearing the query, so a user
  // who clicked away by accident can come back to the same list.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  const addLabel = t('librarySearch.addToBindery', { query: trimmed })
  const rows: Row[] = trimmed ? (results ? toRows(results, addLabel) : [{ kind: 'add', label: addLabel }]) : []
  const hasMatches = !!results && (results.authors.length + results.books.length + results.series.length > 0)

  const reset = () => {
    setQuery('')
    setOpen(false)
    setActive(-1)
  }

  const choose = (row: Row) => {
    if (row.kind === 'add') {
      setAddQuery(trimmed)
      setOpen(false)
      return
    }
    reset()
    switch (row.kind) {
      case 'author':
        navigate(`/author/${row.id}`)
        break
      case 'book':
        navigate(`/book/${row.id}`)
        break
      case 'series':
        // There is no series detail route; the Series page expands the row
        // named in location.state, as the Authors page already does.
        navigate('/series', { state: { seriesId: row.id } })
        break
    }
    onNavigate?.()
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    switch (e.key) {
      case 'ArrowDown':
        if (!rows.length) return
        e.preventDefault()
        setOpen(true)
        setActive(i => (i + 1) % rows.length)
        break
      case 'ArrowUp':
        if (!rows.length) return
        e.preventDefault()
        setOpen(true)
        setActive(i => (i <= 0 ? rows.length - 1 : i - 1))
        break
      case 'Enter': {
        if (!rows.length) return
        e.preventDefault()
        // Enter with nothing highlighted takes the escape hatch: the user
        // typed a name and asked for it, so offer to add it.
        const row = open && active >= 0 ? rows[active] : rows[rows.length - 1]
        choose(row)
        break
      }
      case 'Escape':
        if (open) {
          e.preventDefault()
          e.stopPropagation()
          setOpen(false)
          setActive(-1)
        } else if (query) {
          e.preventDefault()
          reset()
        }
        break
      case 'Home':
      case 'End':
        if (open && rows.length) {
          e.preventDefault()
          setActive(e.key === 'Home' ? 0 : rows.length - 1)
        }
        break
    }
  }

  const optionId = (i: number) => `${listId}-opt-${i}`
  const sectionLabel: Record<Exclude<Row['kind'], 'add'>, string> = {
    author: t('librarySearch.authors'),
    book: t('librarySearch.books'),
    series: t('librarySearch.series'),
  }

  const renderRows = () => {
    const out: React.ReactNode[] = []
    let lastKind: Row['kind'] | null = null
    rows.forEach((row, i) => {
      if (row.kind !== 'add' && row.kind !== lastKind) {
        out.push(
          <li key={`h-${row.kind}`} role="presentation" className="px-3 pt-2 pb-1 text-[11px] font-semibold uppercase tracking-wide text-fg-muted">
            {sectionLabel[row.kind]}
          </li>,
        )
      }
      lastKind = row.kind
      const isActive = i === active
      const base = 'flex items-center gap-3 px-3 py-2 text-sm cursor-pointer'
      const tone = isActive ? 'bg-slate-200 dark:bg-zinc-800' : 'hover:bg-slate-200/60 dark:hover:bg-zinc-800/60'
      if (row.kind === 'add') {
        out.push(
          <li
            key="add"
            id={optionId(i)}
            role="option"
            aria-selected={isActive}
            data-testid="library-search-add"
            className={`${base} ${tone} border-t border-slate-200 dark:border-zinc-800 text-emerald-700 dark:text-emerald-400 font-medium`}
            onMouseDown={e => e.preventDefault()}
            onClick={() => choose(row)}
            onMouseEnter={() => setActive(i)}
          >
            <svg className="w-4 h-4 flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24" strokeWidth={2} aria-hidden="true">
              <path strokeLinecap="round" strokeLinejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
            </svg>
            <span className="truncate">{row.label}</span>
          </li>,
        )
        return
      }
      out.push(
        <li
          key={`${row.kind}-${row.id}`}
          id={optionId(i)}
          role="option"
          aria-selected={isActive}
          className={`${base} ${tone}`}
          onMouseDown={e => e.preventDefault()}
          onClick={() => choose(row)}
          onMouseEnter={() => setActive(i)}
        >
          {row.kind !== 'series' && (
            row.imageUrl
              ? <img src={row.imageUrl} alt="" className={`flex-shrink-0 object-cover rounded ${row.kind === 'author' ? 'w-7 h-7 rounded-full' : 'w-7 h-10'}`} />
              : <span aria-hidden="true" className={`flex-shrink-0 bg-slate-300 dark:bg-zinc-700 ${row.kind === 'author' ? 'w-7 h-7 rounded-full' : 'w-7 h-10 rounded'}`} />
          )}
          <span className="min-w-0">
            <span className="block truncate">{row.label}</span>
            {row.kind === 'book' && row.sub && <span className="block truncate text-xs text-fg-muted">{row.sub}</span>}
          </span>
        </li>,
      )
    })
    return out
  }

  return (
    <div ref={rootRef} className={`relative ${className}`}>
      <label htmlFor={`${listId}-input`} className="sr-only">{t('librarySearch.label')}</label>
      <div className="relative">
        <svg className="w-4 h-4 absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-muted pointer-events-none" fill="none" stroke="currentColor" viewBox="0 0 24 24" strokeWidth={2} aria-hidden="true">
          <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-5.197-5.197m0 0A7.5 7.5 0 1 0 5.196 5.196a7.5 7.5 0 0 0 10.607 10.607Z" />
        </svg>
        <input
          ref={inputRef}
          id={`${listId}-input`}
          type="text"
          role="combobox"
          aria-expanded={open}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={open && active >= 0 ? optionId(active) : undefined}
          autoComplete="off"
          spellCheck={false}
          autoFocus={autoFocus}
          value={query}
          placeholder={t('librarySearch.placeholder')}
          onChange={e => {
            // Drop the highlight with the text it belonged to: until the new
            // response lands the rows still show the old query's results,
            // and Enter inside the debounce window must not pick one of them.
            setActive(-1)
            setQuery(e.target.value)
          }}
          onFocus={() => { if (rows.length) setOpen(true) }}
          onKeyDown={onKeyDown}
          className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md pl-8 pr-3 py-1.5 text-sm focus:outline-none focus:border-emerald-500"
        />
      </div>
      <ul
        id={listId}
        role="listbox"
        aria-label={t('librarySearch.label')}
        hidden={!open || rows.length === 0}
        className="absolute left-0 right-0 sm:right-auto sm:min-w-[20rem] mt-1 max-h-[70vh] overflow-y-auto py-1 rounded-md border border-slate-200 dark:border-zinc-700 bg-slate-50 dark:bg-zinc-900 shadow-lg z-50"
      >
        {open && loading && !results && (
          <li role="presentation" className="px-3 py-2 text-sm text-fg-muted">{t('librarySearch.searching')}</li>
        )}
        {open && error && (
          <li role="presentation" className="px-3 py-2 text-sm text-red-700 dark:text-red-300">{error}</li>
        )}
        {open && results && !hasMatches && (
          <li role="presentation" className="px-3 py-2 text-sm text-fg-muted">{t('librarySearch.noMatches')}</li>
        )}
        {open && renderRows()}
      </ul>
      {addQuery !== null && (
        <AddToLibraryModal
          initialQuery={addQuery}
          onClose={() => setAddQuery(null)}
          onAdded={(added: AddedToLibrary) => {
            setAddQuery(null)
            reset()
            navigate(added.kind === 'author' ? `/author/${added.author.id}` : `/book/${added.book.id}`)
            onNavigate?.()
          }}
        />
      )}
    </div>
  )
}
