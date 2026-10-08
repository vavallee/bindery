import { useCallback, useEffect, useMemo, useState } from 'react'

const SHARED_PAGE_SIZE_KEY = 'pageSize:shared'

function pageSizeStorage(): Storage | null {
  if (typeof window === 'undefined') return null
  try {
    return window.localStorage
  } catch {
    return null
  }
}

function readStoredPageSize(storageKey: string | undefined): number | null {
  const storage = pageSizeStorage()
  if (!storage) return null
  const keys = storageKey ? [`pageSize:${storageKey}`, SHARED_PAGE_SIZE_KEY] : [SHARED_PAGE_SIZE_KEY]
  for (const key of keys) {
    try {
      const stored = storage.getItem(key)
      if (stored) {
        const n = parseInt(stored, 10)
        if (!isNaN(n) && n > 0) return n
      }
    } catch {
      return null
    }
  }
  return null
}

function persistPageSize(storageKey: string | undefined, size: number) {
  const storage = pageSizeStorage()
  if (!storage) return
  try {
    if (storageKey) storage.setItem(`pageSize:${storageKey}`, String(size))
    storage.setItem(SHARED_PAGE_SIZE_KEY, String(size))
  } catch {
    // Storage is a user preference only; unavailable storage should not break lists.
  }
}

/**
 * useServerPagination: page/pageSize state for lists paginated on the SERVER.
 *
 * Unlike usePagination (which slices a fully-loaded client array), this tracks
 * the current page and size and builds Pagination props from a server-provided
 * `total`. The caller fetches the page whenever `page`/`pageSize` (or its own
 * filters) change — typically by listing them in a fetch effect's deps. Page
 * size is persisted under the same localStorage keys as usePagination, so the
 * user's preference carries across both kinds of list.
 *
 * Pass `options.page` and `options.setPage` to keep the page somewhere else,
 * such as the URL (useListParams), so it survives going back (#3052).
 */
export interface ControlledPage {
  /** The current page, e.g. read from the URL. */
  page: number
  /** Called for every page change; `replace` marks a correction, not a step. */
  setPage: (page: number, opts?: { replace?: boolean }) => void
}

export interface ServerPaginationOptions extends Partial<ControlledPage> {
  /**
   * False while `total` is not yet known (the first fetch is in flight). The
   * snap back to the last page waits for it, so a page taken from the URL is
   * not thrown away because the empty initial total says there is one page.
   */
  ready?: boolean
}

export function useServerPagination(total: number, defaultPageSize = 50, storageKey?: string, options: ServerPaginationOptions = {}) {
  const [ownPage, setOwnPage] = useState(1)
  const [pageSize, setPageSizeState] = useState(() => readStoredPageSize(storageKey) ?? defaultPageSize)
  const controlled = options.page !== undefined && options.setPage !== undefined
  const page = controlled ? options.page! : ownPage
  const controlledSetPage = options.setPage
  const setPage = useCallback((p: number, opts?: { replace?: boolean }) => {
    if (controlled && controlledSetPage) controlledSetPage(p, opts)
    else setOwnPage(p)
  }, [controlled, controlledSetPage])
  const ready = options.ready ?? true

  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  // If the result set shrank (e.g. a filter was applied) below the current
  // page, snap back so the user is not stranded on an empty page.
  useEffect(() => {
    if (ready && page > totalPages) setPage(totalPages, { replace: true })
  }, [ready, page, totalPages, setPage])

  const setPageSize = useCallback((size: number) => {
    setPageSizeState(size)
    setPage(1)
    persistPageSize(storageKey, size)
  }, [storageKey, setPage])

  const reset = useCallback(() => setPage(1), [setPage])
  const onPageChange = useCallback((p: number) => setPage(p), [setPage])

  return {
    page,
    pageSize,
    setPage,
    reset,
    paginationProps: {
      page: Math.min(page, totalPages),
      totalPages,
      pageSize,
      totalItems: total,
      onPageChange,
      onPageSizeChange: setPageSize,
    },
  }
}

/**
 * usePagination: client-side slicing helper.
 * Pass the full filtered list; get back the visible page + props for Pagination.
 *
 * storageKey: when provided, page size is persisted to localStorage under that
 * key so the user's preference survives navigation and page reloads. A shared
 * key is also written so that when the user first visits a new tab, they see
 * the page size they last picked elsewhere.
 *
 * controlledPage: keep the page outside the hook (e.g. in the URL).
 */
export function usePagination<T>(items: T[], defaultPageSize = 50, storageKey?: string, controlledPage?: ControlledPage) {
  const [ownPage, setOwnPage] = useState(1)
  const page = controlledPage ? controlledPage.page : ownPage
  const controlledSetPage = controlledPage?.setPage
  const setPage = useCallback((p: number) => {
    if (controlledSetPage) controlledSetPage(p)
    else setOwnPage(p)
  }, [controlledSetPage])
  const [pageSize, setPageSize] = useState(() => {
    return readStoredPageSize(storageKey) ?? defaultPageSize
  })

  const totalPages = Math.max(1, Math.ceil(items.length / pageSize))
  const safePage = Math.min(page, totalPages)
  const paged = useMemo(() => items.slice((safePage - 1) * pageSize, safePage * pageSize), [items, safePage, pageSize])

  const reset = useCallback(() => setPage(1), [setPage])

  const handlePageSizeChange = (size: number) => {
    setPageSize(size)
    setPage(1)
    persistPageSize(storageKey, size)
  }

  return {
    pageItems: paged,
    paginationProps: {
      page: safePage,
      totalPages,
      pageSize,
      totalItems: items.length,
      onPageChange: setPage,
      onPageSizeChange: handlePageSizeChange,
    },
    reset,
  }
}
