import { useCallback, useEffect, useReducer, useRef, useState } from 'react'
import { api } from '../../api/client'
import type { AdoptionItem, AdoptionSort, AdoptionState, AdoptTarget, AdoptionBookRef } from '../../api/client'
import { useServerPagination } from '../../components/usePagination'
import { adoptionReducer, initialAdoptionState } from './adoptionReducer'
import { notifyUnmatchedChanged } from '../../components/useUnmatchedCount'

// useAdoptionList owns the adoption view's data: filters, paging, the fetch,
// optimistic decisions (adoptionReducer) and the scan poll. Components only
// render what it returns and call its verbs.

export interface AdoptionFilters {
  state: AdoptionState
  reason: string
  authorFolder: string
  format: string
  search: string
  sort: AdoptionSort
  dir: '' | 'asc' | 'desc'
}

export const defaultAdoptionFilters: AdoptionFilters = {
  state: 'pending', reason: '', authorFolder: '', format: '', search: '', sort: 'score', dir: '',
}

// How often the page asks for the scan status while a scan runs (P5). Nothing
// polls otherwise.
export const SCAN_POLL_MS = 3000

function message(e: unknown, fallback: string): string {
  return e instanceof Error && e.message ? e.message : fallback
}

export function useAdoptionList() {
  const [state, dispatch] = useReducer(adoptionReducer, initialAdoptionState)
  const [filters, setFilters] = useState<AdoptionFilters>(defaultAdoptionFilters)
  const [debouncedSearch, setDebouncedSearch] = useState('')
  const { page, pageSize, paginationProps, reset } = useServerPagination(state.total, 50, 'adoption')
  const request = useRef(0)
  // Facets are fetched once per state and search, not on every page turn or
  // filter pick (P4); the key records what the held facets were computed for.
  const facetKey = useRef('')
  const [reloadTick, setReloadTick] = useState(0)

  useEffect(() => {
    const id = setTimeout(() => setDebouncedSearch(filters.search.trim()), 300)
    return () => clearTimeout(id)
  }, [filters.search])

  useEffect(() => { reset() }, [filters.state, filters.reason, filters.authorFolder, filters.format, debouncedSearch, filters.sort, filters.dir, reset])

  useEffect(() => {
    const n = ++request.current
    const key = `${filters.state}|${debouncedSearch}|${reloadTick}`
    const wantFacets = key !== facetKey.current
    dispatch({ type: 'loadStarted' })
    api.listUnmatched({
      state: filters.state,
      reason: filters.reason || undefined,
      authorFolder: filters.authorFolder || undefined,
      format: filters.format || undefined,
      search: debouncedSearch || undefined,
      sort: filters.sort,
      dir: filters.dir || undefined,
      limit: pageSize,
      offset: (page - 1) * pageSize,
      facets: wantFacets,
    }).then(response => {
      if (n !== request.current) return
      if (wantFacets) facetKey.current = key
      dispatch({ type: 'loaded', response })
    }).catch(e => {
      if (n === request.current) dispatch({ type: 'loadFailed', error: message(e, 'Could not load the list') })
    })
  }, [filters.state, filters.reason, filters.authorFolder, filters.format, filters.sort, filters.dir, debouncedSearch, page, pageSize, reloadTick])

  const reload = useCallback(() => setReloadTick(n => n + 1), [])

  const refreshSummary = useCallback(async () => {
    try {
      const { scan, ...summary } = await api.unmatchedSummary()
      dispatch({ type: 'scanStatus', summary, scan })
      notifyUnmatchedChanged(summary.pending)
      return scan
    } catch {
      return null
    }
  }, [])

  // Poll only while a scan runs; reload the list once it finishes.
  const running = Boolean(state.scan?.running)
  useEffect(() => {
    if (!running) return
    const id = setInterval(async () => {
      const scan = await refreshSummary()
      if (scan && !scan.running) reload()
    }, SCAN_POLL_MS)
    return () => clearInterval(id)
  }, [running, refreshSummary, reload])

  const setFilter = useCallback((patch: Partial<AdoptionFilters>) => setFilters(f => ({ ...f, ...patch })), [])

  const adopt = useCallback(async (item: AdoptionItem, target: AdoptTarget, preview: AdoptionBookRef | null) => {
    dispatch({ type: 'adoptRequested', id: item.id, preview })
    try {
      const updated = await api.adoptUnit(item.id, target)
      dispatch({ type: 'adoptSucceeded', id: item.id, item: updated })
      void refreshSummary()
    } catch (e) {
      dispatch({ type: 'requestFailed', id: item.id, error: message(e, 'Could not adopt this book'), revertTo: null })
    }
  }, [refreshSummary])

  const ignore = useCallback(async (item: AdoptionItem) => {
    dispatch({ type: 'ignoreRequested', id: item.id })
    try {
      await api.ignoreUnit(item.id)
      dispatch({ type: 'ignoreSucceeded', id: item.id })
      void refreshSummary()
    } catch (e) {
      dispatch({ type: 'requestFailed', id: item.id, error: message(e, 'Could not ignore this book'), revertTo: null })
    }
  }, [refreshSummary])

  // undo reverses whatever the row's last decision was: an adoption (undo) or
  // an ignore (unignore). On the adopted and ignored lists the row then reads
  // as restored; on the pending list it simply needs a decision again.
  const undo = useCallback(async (item: AdoptionItem) => {
    const prior = state.outcomes[item.id] ?? null
    const wasIgnored = prior?.kind === 'ignored' || (!prior && item.state === 'ignored')
    dispatch({ type: 'undoRequested', id: item.id })
    try {
      const updated = wasIgnored ? await api.unignoreUnit(item.id) : await api.undoAdoption(item.id)
      dispatch({ type: 'undoSucceeded', id: item.id, item: updated, restored: filters.state !== 'pending', keptBook: Boolean(updated.message) })
      void refreshSummary()
    } catch (e) {
      dispatch({ type: 'requestFailed', id: item.id, error: message(e, 'Could not undo'), revertTo: prior })
    }
  }, [state.outcomes, filters.state, refreshSummary])

  const startScan = useCallback(async () => {
    try {
      await api.triggerLibraryScan()
    } catch {
      // A failed trigger leaves the summary as it was. A scan already running
      // is not a failure: the request is queued behind it (#3014).
    }
    await refreshSummary()
  }, [refreshSummary])

  const ignoreFolder = useCallback(async (folder: string) => {
    await api.ignoreFolder(folder)
    await refreshSummary()
    setFilters(f => (f.authorFolder === folder ? { ...f, authorFolder: '' } : f))
    reload()
  }, [refreshSummary, reload])

  return {
    state,
    filters,
    setFilter,
    paginationProps,
    expand: (id: number) => dispatch({ type: 'expanded', id }),
    collapse: () => dispatch({ type: 'collapsed' }),
    adopt,
    ignore,
    undo,
    startScan,
    ignoreFolder,
    reload,
  }
}

export type AdoptionList = ReturnType<typeof useAdoptionList>
