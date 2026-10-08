import { useCallback, useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router'

type ParamValue = string | number | null | undefined

export interface ListParamUpdateOptions {
  /** Replace the current history entry instead of pushing a new one. */
  replace?: boolean
}

/**
 * useListParams keeps a list page's state (page, search text, filters, sort)
 * in the query string, so going back from a detail page lands on the same
 * page with the same search instead of page 1 (#3052). SeriesPage and
 * ImportPage already work this way; this is the shared form of it.
 *
 * `defaults` names every key the page owns and its default value. A key whose
 * value equals its default is removed from the URL, so an untouched list stays
 * at a clean `/books`. `page` is always owned, with a default of 1.
 *
 * Updates push a history entry unless `replace` is set, so back steps through
 * pages and filter changes. Debounced search commits use replace, so typing
 * does not leave one entry per keystroke. An update that would not change the
 * URL is dropped rather than pushing a duplicate entry.
 */
export function useListParams<D extends Record<string, string>>(defaults: D) {
  const [searchParams, setSearchParams] = useSearchParams()
  const defaultsRef = useRef(defaults)

  const values = {} as { [K in keyof D]: string }
  for (const key of Object.keys(defaults) as (keyof D & string)[]) {
    values[key] = searchParams.get(key) ?? defaults[key]
  }
  const rawPage = parseInt(searchParams.get('page') ?? '', 10)
  const page = Number.isFinite(rawPage) && rawPage > 0 ? rawPage : 1

  const update = useCallback((patch: Partial<Record<keyof D | 'page', ParamValue>>, opts: ListParamUpdateOptions = {}) => {
    const next = new URLSearchParams(searchParams)
    for (const [key, raw] of Object.entries(patch) as [string, ParamValue][]) {
      const value = raw == null ? '' : String(raw)
      const fallback = key === 'page' ? '1' : (defaultsRef.current[key] ?? '')
      if (value === '' || value === fallback) next.delete(key)
      else next.set(key, value)
    }
    if (next.toString() === searchParams.toString()) return
    setSearchParams(next, { replace: opts.replace })
  }, [searchParams, setSearchParams])

  const setPage = useCallback((p: number, opts?: ListParamUpdateOptions) => {
    update({ page: p } as Partial<Record<keyof D | 'page', ParamValue>>, opts)
  }, [update])

  return { values, page, update, setPage }
}

/** Narrow a URL value to one of `allowed`, or fall back when it is unknown. */
export function oneOf<T extends string>(value: string, allowed: readonly T[], fallback: T): T {
  return (allowed as readonly string[]).includes(value) ? value as T : fallback
}

/**
 * useUrlSearchInput backs a search box whose committed value lives in the URL.
 * The box updates on every keystroke; the trimmed text is committed through
 * `commit` after `delay` ms of quiet. When the URL value changes from outside
 * (back, forward, a link), the box follows it.
 */
export function useUrlSearchInput(urlValue: string, commit: (value: string) => void, delay = 300) {
  const [text, setText] = useState(urlValue)
  const [seenUrlValue, setSeenUrlValue] = useState(urlValue)
  if (urlValue !== seenUrlValue) {
    // Adjusting state during render: the URL moved under us (back/forward).
    setSeenUrlValue(urlValue)
    if (text.trim() !== urlValue) setText(urlValue)
  }

  const commitRef = useRef(commit)
  useEffect(() => { commitRef.current = commit })

  useEffect(() => {
    const trimmed = text.trim()
    if (trimmed === urlValue) return
    const id = setTimeout(() => commitRef.current(trimmed), delay)
    return () => clearTimeout(id)
  }, [text, urlValue, delay])

  return [text, setText] as const
}
