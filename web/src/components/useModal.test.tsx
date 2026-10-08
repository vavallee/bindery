import { StrictMode, useState } from 'react'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes, useLocation, useNavigate, type Location, type NavigateFunction } from 'react-router'
import { ModalHistoryProvider, useModal } from './useModal'
import EditBookModal from './EditBookModal'
import type { Book } from '../api/client'
import '../i18n'

// #3052: Android back with a modal open left the page and threw the edits
// away, Escape and Tab did nothing in most modals, and focus was lost on close.

let nav: NavigateFunction
let current: Location
const seen: Location[] = []
function Probe() {
  nav = useNavigate()
  current = useLocation()
  seen.push(current)
  return null
}

function TestModal({ name, onClose, canClose, children }: { name: string; onClose: () => void; canClose?: boolean; children?: React.ReactNode }) {
  const { titleId, panelProps } = useModal({ onClose, canClose })
  return (
    <div className="modal-overlay fixed inset-0" onClick={onClose}>
      <div {...panelProps} onClick={e => e.stopPropagation()}>
        <h3 id={titleId}>{name}</h3>
        <input aria-label={`${name} field`} />
        <button type="button" onClick={onClose}>{`close ${name}`}</button>
        {children}
      </div>
    </div>
  )
}

function Page({ busy = false, onNavigateAway }: { busy?: boolean; onNavigateAway?: (navigate: NavigateFunction) => void }) {
  const [outer, setOuter] = useState(false)
  const [inner, setInner] = useState(false)
  const navigate = useNavigate()
  return (
    <div>
      <p>the page</p>
      <button type="button" onClick={() => setOuter(true)}>open</button>
      {outer && (
        <TestModal name="outer" onClose={() => setOuter(false)} canClose={!busy}>
          <button type="button" onClick={() => setInner(true)}>open inner</button>
          <button type="button" onClick={() => { setOuter(false); onNavigateAway?.(navigate) }}>go away</button>
          {inner && <TestModal name="inner" onClose={() => setInner(false)} />}
        </TestModal>
      )}
    </div>
  )
}

function renderPage(props: Parameters<typeof Page>[0] = {}, strict = false) {
  seen.length = 0
  const tree = (
    <MemoryRouter initialEntries={['/prev', '/books?page=2']} initialIndex={1}>
      <ModalHistoryProvider>
        <Probe />
        <Routes>
          <Route path="/books" element={<Page {...props} />} />
          <Route path="*" element={<p>elsewhere</p>} />
        </Routes>
      </ModalHistoryProvider>
    </MemoryRouter>
  )
  return render(strict ? <StrictMode>{tree}</StrictMode> : tree)
}

async function flush() {
  await act(async () => { await Promise.resolve() })
}

async function back() {
  await act(async () => { await nav(-1) })
}

async function open(name = 'open') {
  const opener = screen.getByRole('button', { name })
  opener.focus()
  await act(async () => { fireEvent.click(opener) })
  await flush()
  return opener
}

afterEach(() => {
  vi.useRealTimers()
})

describe('useModal history', () => {
  it('pushes an entry for the same URL when the modal opens', async () => {
    renderPage()
    const before = current
    await open()
    expect(current.key).not.toBe(before.key)
    expect(current.pathname).toBe('/books')
    expect(current.search).toBe('?page=2')
  })

  it('back closes the modal and the page stays', async () => {
    renderPage()
    const pageKey = current.key
    await open()
    expect(screen.getByRole('dialog', { name: 'outer' })).toBeInTheDocument()
    await back()
    await flush()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByText('the page')).toBeInTheDocument()
    expect(current.key).toBe(pageKey)
    // Still on the page: one more back leaves it.
    await back()
    expect(current.pathname).toBe('/prev')
  })

  it('closing with a button removes the entry it pushed', async () => {
    renderPage()
    const pageKey = current.key
    await open()
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'close outer' })) })
    await flush()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(current.key).toBe(pageKey)
    // No dead entry left behind: the next back leaves the page.
    await back()
    expect(current.pathname).toBe('/prev')
  })

  it('Escape closes the modal and removes its entry', async () => {
    renderPage()
    const pageKey = current.key
    await open()
    await act(async () => { fireEvent.keyDown(screen.getByRole('textbox', { name: 'outer field' }), { key: 'Escape' }) })
    await flush()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(current.key).toBe(pageKey)
  })

  it('back from a nested modal closes only the top one', async () => {
    renderPage()
    await open()
    const outerKey = current.key
    await open('open inner')
    expect(screen.getAllByRole('dialog')).toHaveLength(2)
    await back()
    await flush()
    expect(screen.queryByRole('dialog', { name: 'inner' })).toBeNull()
    expect(screen.getByRole('dialog', { name: 'outer' })).toBeInTheDocument()
    expect(current.key).toBe(outerKey)
    await back()
    await flush()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(current.pathname).toBe('/books')
  })

  it('Escape in a nested modal closes only the top one', async () => {
    renderPage()
    await open()
    const outerKey = current.key
    await open('open inner')
    await act(async () => { fireEvent.keyDown(document.activeElement!, { key: 'Escape' }) })
    await flush()
    expect(screen.queryByRole('dialog', { name: 'inner' })).toBeNull()
    expect(screen.getByRole('dialog', { name: 'outer' })).toBeInTheDocument()
    expect(current.key).toBe(outerKey)
  })

  it('a navigation away from inside the modal replaces its entry', async () => {
    renderPage({ onNavigateAway: navigate => navigate('/book/9') })
    await open()
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'go away' })) })
    await flush()
    expect(current.pathname).toBe('/book/9')
    // Back goes to the page once, not to a dead copy of it first.
    await back()
    expect(current.pathname).toBe('/books')
    expect(current.state).toBeNull()
    await back()
    expect(current.pathname).toBe('/prev')
  })

  it('back while the modal is busy keeps it open', async () => {
    renderPage({ busy: true })
    await open()
    await back()
    await flush()
    expect(screen.getByRole('dialog', { name: 'outer' })).toBeInTheDocument()
    // Its entry is back on top, so the next back is still caught.
    expect(current.state).toMatchObject({ binderyModals: expect.any(Array) })
  })

  it('Escape is ignored while the modal is busy', async () => {
    renderPage({ busy: true })
    await open()
    await act(async () => { fireEvent.keyDown(document.activeElement!, { key: 'Escape' }) })
    expect(screen.getByRole('dialog', { name: 'outer' })).toBeInTheDocument()
  })

  it('pushes one entry under StrictMode', async () => {
    renderPage({}, true)
    const pageKey = current.key
    await open()
    await back()
    await flush()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(current.key).toBe(pageKey)
  })

  it('keeps the page state under the modal entry', async () => {
    seen.length = 0
    render(
      <MemoryRouter initialEntries={[{ pathname: '/books', state: { seriesId: 4 } }]}>
        <ModalHistoryProvider>
          <Probe />
          <Routes><Route path="/books" element={<Page />} /></Routes>
        </ModalHistoryProvider>
      </MemoryRouter>,
    )
    await open()
    expect(current.state).toMatchObject({ seriesId: 4 })
    await back()
    await flush()
    expect(current.state).toEqual({ seriesId: 4 })
  })
})

describe('useModal stale markers', () => {
  // A marker can outlive its modal: a reload with a modal open, a tab the
  // browser discarded, or forward onto an entry whose modal has closed.
  function renderAt(entries: Parameters<typeof MemoryRouter>[0]['initialEntries'], index: number, props: Parameters<typeof Page>[0] = {}) {
    seen.length = 0
    return render(
      <MemoryRouter initialEntries={entries} initialIndex={index}>
        <ModalHistoryProvider>
          <Probe />
          <Routes>
            <Route path="/books" element={<Page {...props} />} />
            <Route path="*" element={<p>elsewhere</p>} />
          </Routes>
        </ModalHistoryProvider>
      </MemoryRouter>,
    )
  }

  it('drops the marker on mount and a push after a reload stays a push', async () => {
    renderAt(['/prev', '/books?page=2', { pathname: '/books', search: '?page=2', state: { binderyModals: ['gone'], keep: 1 } }], 2)
    await flush()
    expect(current.state).toEqual({ keep: 1 })
    const reloadedKey = current.key
    await act(async () => { await nav('/books?page=3') })
    expect(current.search).toBe('?page=3')
    // One back returns to the reloaded entry, not past it.
    await back()
    expect(current.key).toBe(reloadedKey)
    expect(current.search).toBe('?page=2')
  })

  it('a push from an entry whose modal has closed stays a push', async () => {
    renderAt(['/prev', '/books?page=2'], 1)
    await open()
    const modalKey = current.key
    await back()
    await flush()
    expect(screen.queryByRole('dialog')).toBeNull()
    // Forward onto the dead entry, then navigate.
    await act(async () => { await nav(1) })
    expect(current.key).toBe(modalKey)
    await act(async () => { await nav('/book/1') })
    expect(current.pathname).toBe('/book/1')
    await back()
    expect(current.key).toBe(modalKey)
  })

  it('a same page navigation from a handler that closes the modal is kept', async () => {
    renderAt(['/prev', '/books?page=2'], 1, { onNavigateAway: navigate => navigate('/books?page=3') })
    const pageKey = current.key
    await open()
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'go away' })) })
    await flush()
    await flush()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(current.search).toBe('?page=3')
    await back()
    expect(current.key).toBe(pageKey)
  })
})

describe('useModal focus', () => {
  it('moves focus into the dialog and restores it to the opener on close', async () => {
    renderPage()
    const opener = await open()
    const dialog = screen.getByRole('dialog', { name: 'outer' })
    expect(dialog.contains(document.activeElement)).toBe(true)
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'close outer' })) })
    await flush()
    expect(document.activeElement).toBe(opener)
  })

  it('restores focus after back too', async () => {
    renderPage()
    const opener = await open()
    await back()
    await flush()
    expect(document.activeElement).toBe(opener)
  })

  it('traps Tab and Shift+Tab inside the dialog', async () => {
    renderPage()
    await open()
    const dialog = screen.getByRole('dialog', { name: 'outer' })
    const field = screen.getByRole('textbox', { name: 'outer field' })
    const last = screen.getByRole('button', { name: 'go away' })
    last.focus()
    fireEvent.keyDown(last, { key: 'Tab' })
    expect(document.activeElement).toBe(field)
    fireEvent.keyDown(field, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(last)
    // Focus that escaped the dialog is pulled back in.
    document.body.focus()
    fireEvent.keyDown(document.body, { key: 'Tab' })
    expect(dialog.contains(document.activeElement)).toBe(true)
  })
})

describe('useModal without a router', () => {
  it('still traps focus and closes on Escape', async () => {
    const onClose = vi.fn()
    render(<TestModal name="bare" onClose={onClose} />)
    const dialog = screen.getByRole('dialog', { name: 'bare' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    fireEvent.keyDown(document.body, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

describe('a real modal on a page', () => {
  function BookPage() {
    const [editing, setEditing] = useState(false)
    const book = { id: 7, title: 'Original', genres: [], lockedFields: [] } as unknown as Book
    return (
      <div>
        <p>book page</p>
        <button type="button" onClick={() => setEditing(true)}>Edit</button>
        {editing && <EditBookModal book={book} onClose={() => setEditing(false)} onSaved={() => {}} />}
      </div>
    )
  }

  it('Android back closes Edit metadata and keeps the book page', async () => {
    seen.length = 0
    render(
      <MemoryRouter initialEntries={['/books', '/book/7']} initialIndex={1}>
        <ModalHistoryProvider>
          <Probe />
          <Routes>
            <Route path="/book/7" element={<BookPage />} />
            <Route path="*" element={<p>elsewhere</p>} />
          </Routes>
        </ModalHistoryProvider>
      </MemoryRouter>,
    )
    await open('Edit')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    await back()
    await flush()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByText('book page')).toBeInTheDocument()
    expect(current.pathname).toBe('/book/7')
  })
})
