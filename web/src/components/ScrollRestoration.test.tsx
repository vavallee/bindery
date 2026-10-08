import { act, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes, useNavigate, type NavigateFunction } from 'react-router'
import ScrollRestoration from './ScrollRestoration'

// #3052: a book opened from far down the list opened already scrolled to the
// list's offset, and back landed at the top of the list. PUSH to a new path
// must start at the top; POP must return to where that entry was left.

let nav: NavigateFunction
function NavHandle() {
  nav = useNavigate()
  return null
}

let scrollHeight = 5000
const scrollTo = vi.fn()

function setScrollY(y: number) {
  Object.defineProperty(window, 'scrollY', { configurable: true, value: y })
  window.dispatchEvent(new Event('scroll'))
}

function renderApp(entries = ['/books']) {
  return render(
    <MemoryRouter initialEntries={entries}>
      <ScrollRestoration />
      <NavHandle />
      <Routes>
        <Route path="*" element={null} />
      </Routes>
    </MemoryRouter>,
  )
}

async function go(to: string | number) {
  await act(async () => { await (typeof to === 'number' ? nav(to) : nav(to)) })
}

beforeEach(() => {
  sessionStorage.clear()
  scrollHeight = 5000
  scrollTo.mockReset()
  vi.stubGlobal('scrollTo', scrollTo)
  Object.defineProperty(document.documentElement, 'scrollHeight', { configurable: true, get: () => scrollHeight })
  setScrollY(0)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('ScrollRestoration', () => {
  it('scrolls to the top on a push to a new page', async () => {
    renderApp()
    setScrollY(1800)
    await go('/book/7')
    expect(scrollTo).toHaveBeenLastCalledWith(0, 0)
  })

  it('restores the list offset when going back', async () => {
    renderApp()
    setScrollY(1800)
    await go('/book/7')
    setScrollY(0)
    scrollTo.mockClear()

    await go(-1)
    expect(scrollTo).toHaveBeenLastCalledWith(0, 1800)
  })

  it('waits for the list to grow tall enough before giving up on the offset', async () => {
    vi.useFakeTimers()
    renderApp()
    setScrollY(1800)
    await go('/book/7')
    setScrollY(0)
    scrollTo.mockClear()

    // The list has not loaded yet, so the page is too short to reach 1800.
    scrollHeight = 800
    await go(-1)
    expect(scrollTo).toHaveBeenCalledWith(0, 1800)
    const callsWhileShort = scrollTo.mock.calls.length

    act(() => { vi.advanceTimersByTime(200) })
    expect(scrollTo.mock.calls.length).toBeGreaterThan(callsWhileShort)

    // Rows arrive; the next attempt lands and the retries stop.
    scrollHeight = 5000
    act(() => { vi.advanceTimersByTime(60) })
    const settled = scrollTo.mock.calls.length
    act(() => { vi.advanceTimersByTime(1000) })
    expect(scrollTo.mock.calls.length).toBe(settled)
    expect(scrollTo).toHaveBeenLastCalledWith(0, 1800)
  })

  it('stops restoring once the user scrolls themselves', async () => {
    vi.useFakeTimers()
    renderApp()
    setScrollY(1800)
    await go('/book/7')
    scrollHeight = 800
    await go(-1)
    window.dispatchEvent(new Event('wheel'))
    const after = scrollTo.mock.calls.length
    act(() => { vi.advanceTimersByTime(3000) })
    expect(scrollTo.mock.calls.length).toBe(after)
  })

  it('leaves the scroll alone for a filter push or a hash link', async () => {
    renderApp()
    setScrollY(1800)
    await go('/books?status=wanted')
    await go('/settings#indexers')
    expect(scrollTo).not.toHaveBeenCalled()
  })

  it('scrolls to the top when paging pushes a new page', async () => {
    renderApp()
    setScrollY(1800)
    await go('/books?page=2')
    expect(scrollTo).toHaveBeenLastCalledWith(0, 0)
    scrollTo.mockClear()
    // Going back to page 1 restores where page 1 was left instead.
    setScrollY(1500)
    await go(-1)
    expect(scrollTo).toHaveBeenLastCalledWith(0, 1800)
  })

  it('leaves the scroll alone when a modal pushes its history entry', async () => {
    renderApp()
    setScrollY(1800)
    // useModal pushes the same pathname and search with only state changed.
    await act(async () => { await nav('/books', { state: { binderyModals: ['m1'] } }) })
    await go(-1)
    // The pop back restores the list to where it already is.
    for (const call of scrollTo.mock.calls) expect(call).toEqual([0, 1800])
  })

  it('scrolls to the top when a replace lands on a new page', async () => {
    // A navigation made from inside a modal replaces the modal's entry, so
    // the new page arrives as a REPLACE rather than a PUSH.
    renderApp()
    setScrollY(1800)
    await act(async () => { await nav('/author/3', { replace: true }) })
    expect(scrollTo).toHaveBeenLastCalledWith(0, 0)
  })

  it('keeps restoring through a replace of the same location', async () => {
    vi.useFakeTimers()
    renderApp()
    setScrollY(1800)
    await go('/book/7')
    scrollHeight = 800
    await go(-1)
    // The list page rewrites its own URL with a replace while the rows are
    // still loading; the restore must carry on.
    await act(async () => { await nav('/books', { replace: true }) })
    scrollTo.mockClear()
    scrollHeight = 5000
    act(() => { vi.advanceTimersByTime(100) })
    expect(scrollTo).toHaveBeenLastCalledWith(0, 1800)
  })
})
