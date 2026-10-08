import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { usePagination, useServerPagination } from './usePagination'

const items = Array.from({ length: 23 }, (_, i) => i + 1)

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('usePagination', () => {
  it('slices the first page with the default size', () => {
    const { result } = renderHook(() => usePagination(items, 10))
    expect(result.current.pageItems).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10])
    expect(result.current.paginationProps).toMatchObject({ page: 1, totalPages: 3, pageSize: 10, totalItems: 23 })
  })

  it('moves between pages and resets to the first', () => {
    const { result } = renderHook(() => usePagination(items, 10))
    act(() => result.current.paginationProps.onPageChange(3))
    expect(result.current.pageItems).toEqual([21, 22, 23])
    act(() => result.current.reset())
    expect(result.current.paginationProps.page).toBe(1)
  })

  it('clamps the page when the list shrinks under it', () => {
    const { result, rerender } = renderHook(({ list }) => usePagination(list, 10), { initialProps: { list: items } })
    act(() => result.current.paginationProps.onPageChange(3))
    rerender({ list: items.slice(0, 5) })
    expect(result.current.paginationProps.page).toBe(1)
    expect(result.current.pageItems).toEqual([1, 2, 3, 4, 5])
  })

  it('persists a changed page size under its own key and the shared key', () => {
    const { result } = renderHook(() => usePagination(items, 10, 'books'))
    act(() => result.current.paginationProps.onPageChange(2))
    act(() => result.current.paginationProps.onPageSizeChange(5))
    expect(result.current.paginationProps).toMatchObject({ page: 1, pageSize: 5, totalPages: 5 })
    expect(localStorage.getItem('pageSize:books')).toBe('5')
    expect(localStorage.getItem('pageSize:shared')).toBe('5')
  })

  it('persists only the shared key when there is no storage key', () => {
    const { result } = renderHook(() => usePagination(items, 10))
    act(() => result.current.paginationProps.onPageSizeChange(20))
    expect(localStorage.getItem('pageSize:shared')).toBe('20')
    expect(localStorage.length).toBe(1)
  })

  it('prefers the per-list stored size, then the shared one', () => {
    localStorage.setItem('pageSize:shared', '25')
    expect(renderHook(() => usePagination(items, 10, 'books')).result.current.paginationProps.pageSize).toBe(25)
    localStorage.setItem('pageSize:books', '5')
    expect(renderHook(() => usePagination(items, 10, 'books')).result.current.paginationProps.pageSize).toBe(5)
  })

  it('ignores stored sizes that are not positive numbers', () => {
    localStorage.setItem('pageSize:books', 'abc')
    localStorage.setItem('pageSize:shared', '0')
    expect(renderHook(() => usePagination(items, 10, 'books')).result.current.paginationProps.pageSize).toBe(10)
  })

  it('falls back to the default when storage throws on read, and does not break on write', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota') })
    const { result } = renderHook(() => usePagination(items, 10, 'books'))
    expect(result.current.paginationProps.pageSize).toBe(10)
    act(() => result.current.paginationProps.onPageSizeChange(5))
    expect(result.current.paginationProps.pageSize).toBe(5)
  })

  it('treats an inaccessible localStorage as no storage at all', () => {
    const descriptor = Object.getOwnPropertyDescriptor(window, 'localStorage')!
    Object.defineProperty(window, 'localStorage', { configurable: true, get: () => { throw new Error('blocked') } })
    try {
      const { result } = renderHook(() => usePagination(items, 10, 'books'))
      expect(result.current.paginationProps.pageSize).toBe(10)
      act(() => result.current.paginationProps.onPageSizeChange(5))
      expect(result.current.paginationProps.pageSize).toBe(5)
    } finally {
      Object.defineProperty(window, 'localStorage', descriptor)
    }
  })
})

describe('useServerPagination', () => {
  it('derives page count from the server total', () => {
    const { result } = renderHook(() => useServerPagination(120, 50))
    expect(result.current.page).toBe(1)
    expect(result.current.pageSize).toBe(50)
    expect(result.current.paginationProps).toMatchObject({ page: 1, totalPages: 3, pageSize: 50, totalItems: 120 })
  })

  it('reports at least one page for an empty result', () => {
    const { result } = renderHook(() => useServerPagination(0))
    expect(result.current.paginationProps.totalPages).toBe(1)
  })

  it('snaps back when the total shrinks below the current page', () => {
    const { result, rerender } = renderHook(({ total }) => useServerPagination(total, 10), { initialProps: { total: 100 } })
    act(() => result.current.setPage(8))
    expect(result.current.page).toBe(8)
    rerender({ total: 25 })
    expect(result.current.page).toBe(3)
    act(() => result.current.reset())
    expect(result.current.page).toBe(1)
  })

  it('resets to page 1 and persists when the page size changes', () => {
    const { result } = renderHook(() => useServerPagination(100, 10, 'authors'))
    act(() => result.current.paginationProps.onPageChange(4))
    act(() => result.current.paginationProps.onPageSizeChange(25))
    expect(result.current.page).toBe(1)
    expect(result.current.pageSize).toBe(25)
    expect(localStorage.getItem('pageSize:authors')).toBe('25')
    expect(localStorage.getItem('pageSize:shared')).toBe('25')
  })

  it('starts from a stored page size', () => {
    localStorage.setItem('pageSize:authors', '100')
    const { result } = renderHook(() => useServerPagination(500, 50, 'authors'))
    expect(result.current.pageSize).toBe(100)
  })
})
