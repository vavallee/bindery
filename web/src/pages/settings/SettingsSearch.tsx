import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { searchSettings, SearchResult, SettingsTabId } from './settingsSearchIndex'

// A search box at the top of Settings that jumps to a tab or section heading
// (#3059). It is a combobox over a listbox (ARIA 1.2): focus stays in the input,
// the highlighted option is named by aria-activedescendant, and a polite live
// region announces how many results there are.

export interface SettingsSearchPick {
  tab: SettingsTabId
  /** Heading text to scroll to once the tab has rendered. */
  heading?: string
}

interface Props {
  tabs: { id: SettingsTabId; label: string }[]
  isAdmin: boolean
  /** Registered setting keys (admins only), so a key quoted in docs finds Advanced. */
  settingKeys?: string[]
  onPick: (pick: SettingsSearchPick) => void
  /** Called when the box is first focused, so the parent can load keys lazily. */
  onActivate?: () => void
}

export default function SettingsSearch({ tabs, isAdmin, settingKeys, onPick, onActivate }: Props) {
  const { t } = useTranslation()
  const uid = useId()
  const inputId = `${uid}-input`
  const listId = `${uid}-list`
  const inputRef = useRef<HTMLInputElement>(null)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)

  const [focused, setFocused] = useState(false)

  const { results, total } = useMemo(
    () => searchSettings({ query, tabs, isAdmin, settingKeys, t: (key, fallback) => t(key, fallback ?? key) }),
    [query, tabs, isAdmin, settingKeys, t],
  )

  // "/" focuses the box, unless the person is already typing somewhere.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey || e.repeat || e.isComposing) return
      const el = e.target as HTMLElement | null
      if (el && el.closest) {
        if (/^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) return
        // jsdom has no isContentEditable, so read the attribute too.
        if (el.isContentEditable || el.closest('[contenteditable=""],[contenteditable="true"]')) return
        // A modal owns the keyboard while it is open.
        if (el.closest('[role=dialog],[aria-modal=true]')) return
      }
      e.preventDefault()
      inputRef.current?.focus()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [])

  const pick = (r: SearchResult) => {
    setQuery('')
    setActive(0)
    onPick({ tab: r.tab, heading: r.kind === 'heading' ? r.heading : undefined })
  }

  const open = query.trim() !== ''
  const showList = open && focused && results.length > 0
  const current = Math.min(active, Math.max(results.length - 1, 0))
  const optionId = (i: number) => `${listId}-${i}`

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Escape') {
      if (query !== '') {
        e.preventDefault()
        setQuery('')
        setActive(0)
      }
    } else if (e.key === 'ArrowDown' && results.length > 0) {
      e.preventDefault()
      setActive((current + 1) % results.length)
    } else if (e.key === 'ArrowUp' && results.length > 0) {
      e.preventDefault()
      setActive((current - 1 + results.length) % results.length)
    } else if (e.key === 'Enter' && results.length > 0) {
      e.preventDefault()
      pick(results[current])
    }
  }

  return (
    <div className="relative mb-4 w-full max-w-md">
      <label htmlFor={inputId} className="block text-sm font-medium text-slate-700 dark:text-zinc-300 mb-1">
        {t('settings.search.label')}
      </label>
      <input
        ref={inputRef}
        id={inputId}
        type="search"
        role="combobox"
        aria-expanded={showList}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={showList ? optionId(current) : undefined}
        autoComplete="off"
        value={query}
        placeholder={t('settings.search.placeholder')}
        onChange={e => { setQuery(e.target.value); setActive(0) }}
        onKeyDown={onKeyDown}
        onFocus={() => { setFocused(true); onActivate?.() }}
        onBlur={() => setFocused(false)}
        className="w-full bg-white dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm text-slate-900 dark:text-zinc-100 focus:outline-hidden -outline-offset-2"
      />
      {/* A mouse down anywhere in the list (rows, scrollbar, padding, the
          "more" note) must not take focus off the input, or the blur would
          close the list before the click lands. */}
      <div
        hidden={!showList}
        onMouseDown={e => e.preventDefault()}
        className="absolute z-20 left-0 right-0 mt-1 rounded border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 shadow-lg"
      >
        <ul
          id={listId}
          role="listbox"
          tabIndex={-1}
          aria-label={t('settings.search.results')}
          className="max-h-72 overflow-auto"
        >
          {results.map((r, i) => (
            <li
              key={r.id}
              id={optionId(i)}
              role="option"
              aria-selected={i === current}
                onClick={() => pick(r)}
              className={`cursor-pointer min-h-11 flex flex-wrap items-center gap-x-2 px-3 py-2 text-sm border-l-4 forced-colors:outline-2 -outline-offset-2 ${
                i === current
                  ? 'border-emerald-600 dark:border-emerald-500 bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white forced-colors:outline'
                  : 'border-transparent text-slate-700 dark:text-zinc-300 hover:bg-slate-100 dark:hover:bg-zinc-800/50'
              }`}
            >
              <span className="font-medium break-words">
                {r.kind === 'tab' ? r.tabLabel : r.kind === 'heading' ? r.heading : r.settingKey}
              </span>
              {r.kind !== 'tab' && (
                <span className="text-xs text-fg-muted">{r.tabLabel}</span>
              )}
            </li>
          ))}
        </ul>
        {total > results.length && (
          <p className="px-3 py-2 text-xs text-fg-muted border-t border-slate-200 dark:border-zinc-800">
            {t('settings.search.more', { shown: results.length, total })}
          </p>
        )}
      </div>
      <p role="status" aria-live="polite" className="sr-only">
        {open ? (results.length === 0 ? t('settings.search.none') : (total > results.length ? t('settings.search.moreAnnounce', { shown: results.length, total }) : t('settings.search.count', { count: results.length }))) : ''}
      </p>
      {open && results.length === 0 && (
        <p className="mt-1 text-xs text-fg-muted" aria-hidden="true">{t('settings.search.none')}</p>
      )}
    </div>
  )
}
