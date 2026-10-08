import {
  createContext,
  createElement,
  useCallback,
  useContext,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type HTMLAttributes,
  type ReactNode,
} from 'react'
import {
  UNSAFE_NavigationContext as NavigationContext,
  parsePath,
  useLocation,
  useNavigate,
  useNavigationType,
  type Location,
  type Navigator,
  type To,
} from 'react-router'

/*
 * Shared modal behaviour (#3052): dialog semantics, a focus trap, Escape,
 * focus restore, and the phone back button.
 *
 * Back: opening a modal pushes a history entry for the same URL whose state
 * names the open modals, so the Android back gesture (or button, or an iOS
 * edge swipe) pops that entry and closes the modal instead of leaving the
 * page with the edits in it. The push goes through react-router, so location
 * keys stay consistent, and ScrollRestoration ignores it because pathname and
 * search do not change. Closing the modal any other way (a button, Escape, a
 * tap on the backdrop) goes back over its entry, but only while that entry is
 * still the one on top.
 *
 * Two things make that safe:
 *
 *  - A navigation away from inside a modal (delete then go to the author,
 *    pick a search result) would otherwise leave the modal's entry under the
 *    new page, so the first back from there would land on a dead copy of the
 *    old page. ModalHistoryProvider turns any push made while a modal entry is
 *    on top into a replace of that entry.
 *  - history.go() is asynchronous in a browser. A push issued while our back
 *    is still in flight would be undone by it, so pushes wait for it to land.
 */

const STATE_KEY = 'binderyModals'

function modalIdsOf(state: unknown): string[] {
  if (state && typeof state === 'object') {
    const ids = (state as Record<string, unknown>)[STATE_KEY]
    if (Array.isArray(ids)) return ids.filter((id): id is string => typeof id === 'string')
  }
  return []
}

function isStateObject(state: unknown): state is Record<string, unknown> | null | undefined {
  return state === null || state === undefined || (typeof state === 'object' && !Array.isArray(state))
}

function stripBasename(basename: string, pathname: string): string {
  if (basename === '/' || basename === '') return pathname
  const base = basename.replace(/\/+$/, '')
  if (pathname === base) return '/'
  return pathname.startsWith(`${base}/`) ? pathname.slice(base.length) : pathname
}

function joinBasename(basename: string, pathname: string): string {
  if (basename === '/' || basename === '') return pathname
  return pathname === '/' ? basename : `${basename.replace(/\/+$/, '')}${pathname}`
}

// A safety net for a back that never reports back: go(-n) past the start of
// the session is a no-op with no popstate.
const PENDING_BACK_TIMEOUT_MS = 1000

interface ModalHistory {
  location: Location
  action: string
  /** Put an entry for this modal on top of history. */
  open: (id: string, force?: boolean) => void
  /** The modal is gone. Go back over its entry if that entry is on top. */
  close: (id: string) => void
}

const ModalHistoryContext = createContext<ModalHistory | null>(null)

/**
 * ModalHistoryProvider sits inside the router and gives modals rendered below
 * it their back button behaviour. Without it (component tests that render a
 * modal on its own) modals still trap focus and close on Escape, they just
 * add no history entry.
 */
export function ModalHistoryProvider({ children }: { children: ReactNode }) {
  const location = useLocation()
  const action = useNavigationType()
  const navigate = useNavigate()
  const parent = useContext(NavigationContext)

  // The router applies a navigation in a transition, so a render can still
  // show the previous location after a push or replace has already happened.
  // Which modal entry is on top is therefore read from the history object
  // itself where the navigator exposes it (BrowserRouter and MemoryRouter
  // both do), and otherwise from the navigations made through here.
  const seenKey = useRef<string | null>(null)
  const tracked = useRef<{ pathname: string; search: string; hash: string; state: unknown; key?: string }>(location)
  if (seenKey.current !== location.key) {
    seenKey.current = location.key
    tracked.current = location
  }
  const basenameRef = useRef(parent.basename)
  basenameRef.current = parent.basename
  const navigatorRef = useRef(parent.navigator)
  navigatorRef.current = parent.navigator
  // The location on top of history right now, basename stripped.
  const top = useCallback(() => {
    const live = (navigatorRef.current as Navigator & { location?: Location }).location
    if (!live) return tracked.current
    return { ...live, pathname: stripBasename(basenameRef.current, live.pathname) }
  }, [])
  const setTracked = (to: To, state: unknown) => {
    const path = typeof to === 'string' ? parsePath(to) : to
    tracked.current = {
      pathname: path.pathname ? stripBasename(basenameRef.current, path.pathname) : tracked.current.pathname,
      search: path.search ?? '',
      hash: path.hash ?? '',
      state,
    }
  }

  const live = useRef(new Set<string>())
  const closed = useRef(new Set<string>())
  const pendingBack = useRef(false)
  const pendingTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const queue = useRef<Array<() => void>>([])
  // The key of the entry each open modal pushed. Only that exact entry is
  // ever popped on close: if the page navigated in the meantime (a replace
  // of the modal's entry), the entry on top is a real page and stays.
  const pushedKey = useRef(new Map<string, string | undefined>())

  // Ids in the top entry's marker that belong to a modal open right now. A
  // marker can outlive its modal: a reload with a modal open, a tab the
  // browser discarded and restored, or forward onto an entry whose modal has
  // since closed. Such a marker must not change how navigation behaves.
  const ownedIds = useCallback(
    (state: unknown) => modalIdsOf(state).filter(id => live.current.has(id) && !closed.current.has(id)),
    [],
  )

  const flush = useCallback(() => {
    while (!pendingBack.current && queue.current.length > 0) {
      queue.current.shift()!()
    }
  }, [])

  const landBack = useCallback(() => {
    pendingBack.current = false
    if (pendingTimer.current !== undefined) clearTimeout(pendingTimer.current)
    pendingTimer.current = undefined
  }, [])

  // Pop the entry on top if a modal that has closed pushed it. When a back
  // lands this runs again, so nested modals closed together unwind one
  // entry at a time.
  const settle = useCallback(() => {
    if (pendingBack.current) return
    const entry = top()
    const ids = modalIdsOf(entry.state)
    const last = ids[ids.length - 1]
    if (last === undefined || !closed.current.has(last) || live.current.has(last)) return
    if (!pushedKey.current.has(last) || pushedKey.current.get(last) !== entry.key) return
    pushedKey.current.delete(last)
    pendingBack.current = true
    pendingTimer.current = setTimeout(() => {
      landBack()
      flush()
    }, PENDING_BACK_TIMEOUT_MS)
    navigate(-1)
  }, [navigate, landBack, flush, top])

  // When a back of ours lands, release whatever waited for it.
  useEffect(() => {
    if (!pendingBack.current || action !== 'POP') return
    landBack()
    settle()
    flush()
  }, [location.key, action, landBack, settle, flush])

  useEffect(() => () => {
    if (pendingTimer.current !== undefined) clearTimeout(pendingTimer.current)
  }, [])

  // On mount (a reload, or a tab restored after the browser discarded it) no
  // modal is open yet, so a marker on the top entry is stale. Drop it so the
  // entry behaves as the plain page it now is.
  useEffect(() => {
    const entry = top()
    const ids = modalIdsOf(entry.state)
    if (ids.length === 0 || ownedIds(entry.state).length > 0) return
    const state = { ...(entry.state as Record<string, unknown>) }
    delete state[STATE_KEY]
    const to = { pathname: entry.pathname, search: entry.search, hash: entry.hash }
    tracked.current = { ...to, state }
    navigate(to, { replace: true, state: Object.keys(state).length > 0 ? state : null })
    // Only on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const open = useCallback((id: string, force = false) => {
    if (live.current.has(id) && !force) return
    live.current.add(id)
    closed.current.delete(id)
    const run = () => {
      const loc = top()
      if (modalIdsOf(loc.state).includes(id)) return
      const ids = ownedIds(loc.state).filter(other => other !== id)
      const base = isStateObject(loc.state) ? loc.state ?? {} : {}
      const state = { ...base, [STATE_KEY]: [...ids, id] }
      const to = { pathname: loc.pathname, search: loc.search, hash: loc.hash }
      tracked.current = { ...to, state }
      navigate(to, { state })
      pushedKey.current.set(id, top().key)
    }
    if (pendingBack.current) queue.current.push(run)
    else run()
  }, [navigate, top, ownedIds])

  const close = useCallback((id: string) => {
    if (!live.current.delete(id)) return
    closed.current.add(id)
    settle()
  }, [settle])

  // A push made while an open modal's entry is on top replaces that entry,
  // and any navigation waits for a back of ours that is still in flight.
  const navigator = useMemo<Navigator>(() => {
    const inner = parent.navigator
    const currentPathname = () => joinBasename(parent.basename, top().pathname)
    const targetPathname = (to: To) => (typeof to === 'string' ? parsePath(to).pathname : to.pathname) ?? currentPathname()
    // Keep the open modals' marker when the page only tidies its own URL.
    // Ids of modals that have closed, or that no open modal owns, are left
    // out, so the entry is not mistaken for a modal's own later.
    const keepMarker = (to: To, state: unknown) => {
      const ids = ownedIds(top().state)
      if (ids.length === 0 || modalIdsOf(state).length > 0 || !isStateObject(state)) return state
      if (targetPathname(to) !== currentPathname()) return state
      return { ...(state ?? {}), [STATE_KEY]: ids }
    }
    const wrapped: Navigator = {
      createHref: to => inner.createHref(to),
      go: delta => inner.go(delta),
      push(to, state, opts) {
        if (pendingBack.current) {
          queue.current.push(() => wrapped.push(to, state, opts))
          return
        }
        if (ownedIds(top().state).length > 0) {
          const next = keepMarker(to, state)
          setTracked(to, next)
          inner.replace(to, next, opts)
          return
        }
        setTracked(to, state)
        inner.push(to, state, opts)
      },
      replace(to, state, opts) {
        if (pendingBack.current) {
          queue.current.push(() => wrapped.replace(to, state, opts))
          return
        }
        const next = keepMarker(to, state)
        setTracked(to, next)
        inner.replace(to, next, opts)
      },
    }
    if (inner.createURL) wrapped.createURL = to => inner.createURL!(to)
    if (inner.encodeLocation) wrapped.encodeLocation = to => inner.encodeLocation!(to)
    return wrapped
  }, [parent.navigator, parent.basename, top, ownedIds])

  const navigationValue = useMemo(() => ({ ...parent, navigator }), [parent, navigator])
  const historyValue = useMemo<ModalHistory>(
    () => ({ location, action, open, close }),
    [location, action, open, close],
  )

  return createElement(
    NavigationContext.Provider,
    { value: navigationValue },
    createElement(ModalHistoryContext.Provider, { value: historyValue }, children),
  )
}

// Focus can land on these with Tab.
const FOCUSABLE = [
  'a[href]',
  'area[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  'summary',
  'iframe',
  '[contenteditable="true"]',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

function focusableIn(panel: HTMLElement): HTMLElement[] {
  return Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE))
    .filter(el => !el.closest('[hidden], [inert]') && el.getAttribute('aria-hidden') !== 'true')
}

interface StackEntry {
  panel: () => HTMLElement | null
  escape: () => void
}

// Open modals, innermost last. Only the innermost one reacts to keys.
const stack: StackEntry[] = []

function onDocumentKeyDown(event: KeyboardEvent) {
  const top = stack[stack.length - 1]
  if (!top || event.defaultPrevented) return
  if (event.key === 'Escape') {
    event.preventDefault()
    top.escape()
    return
  }
  if (event.key !== 'Tab') return
  const panel = top.panel()
  if (!panel) return
  const focusable = focusableIn(panel)
  const active = document.activeElement as HTMLElement | null
  if (focusable.length === 0) {
    event.preventDefault()
    panel.focus({ preventScroll: true })
    return
  }
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  const inside = active ? focusable.includes(active) : false
  if (event.shiftKey && (active === first || !inside)) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && (active === last || !inside)) {
    event.preventDefault()
    first.focus()
  }
}

function pushStack(entry: StackEntry) {
  if (stack.length === 0) document.addEventListener('keydown', onDocumentKeyDown)
  stack.push(entry)
}

function removeStack(entry: StackEntry) {
  const i = stack.indexOf(entry)
  if (i >= 0) stack.splice(i, 1)
  if (stack.length === 0) document.removeEventListener('keydown', onDocumentKeyDown)
}

export interface ModalOptions {
  onClose: () => void
  /**
   * False while closing would abandon work in flight (a save, a delete).
   * Escape is then ignored, and a back press is undone so the modal stays.
   */
  canClose?: boolean
  /** Id of the element that names the dialog. Generated when omitted. */
  labelledBy?: string
  /**
   * The modal is a native <dialog> opened with showModal(), which already
   * traps focus and handles Escape. Only the back button behaviour applies.
   */
  native?: boolean
  /**
   * False for a modal that is a page of its own, where back already leaves
   * the page and that is what the user wants.
   */
  history?: boolean
}

export interface ModalPanelProps {
  ref: (el: HTMLElement | null) => void
  role: 'dialog'
  'aria-modal': 'true'
  'aria-labelledby': string
  tabIndex: -1
  'data-modal-panel': ''
}

/**
 * useModal gives a modal panel its dialog semantics and behaviour. Spread
 * panelProps on the panel element (not the backdrop) and put titleId on its
 * heading. The modal is open for as long as the component is mounted.
 */
export function useModal({ onClose, canClose = true, labelledBy, native = false, history = true }: ModalOptions): {
  titleId: string
  panelProps: ModalPanelProps
} {
  const id = useId()
  const titleId = labelledBy ?? `modal-title-${id.replace(/[^a-zA-Z0-9_-]/g, '')}`
  const ctx = useContext(ModalHistoryContext)
  const historyCtx = history ? ctx : null

  const panelRef = useRef<HTMLElement | null>(null)
  const ref = useCallback((el: HTMLElement | null) => { panelRef.current = el }, [])
  const onCloseRef = useRef(onClose)
  const canCloseRef = useRef(canClose)
  useEffect(() => {
    onCloseRef.current = onClose
    canCloseRef.current = canClose
  })

  // Whatever had focus when the modal opened, captured during the first
  // render: by the time an effect runs an autoFocus field inside the modal
  // already has it.
  const [opener] = useState<HTMLElement | null>(() =>
    typeof document !== 'undefined' && document.activeElement instanceof HTMLElement && document.activeElement !== document.body
      ? document.activeElement
      : null,
  )

  // React StrictMode unmounts and remounts every effect once in development.
  // Closing is deferred a microtask so that remount can call it off instead
  // of popping history and pushing it again.
  const pendingClose = useRef<{ cancelled: boolean } | null>(null)
  const historyRef = useRef(historyCtx)
  useEffect(() => { historyRef.current = historyCtx })

  useEffect(() => {
    const entry: StackEntry | null = native ? null : {
      panel: () => panelRef.current,
      escape: () => { if (canCloseRef.current) onCloseRef.current() },
    }
    if (entry) pushStack(entry)

    if (pendingClose.current) {
      pendingClose.current.cancelled = true
      pendingClose.current = null
    } else {
      historyRef.current?.open(id)
      const panel = panelRef.current
      if (!native && panel && !panel.contains(document.activeElement)) {
        panel.focus({ preventScroll: true })
      }
    }

    return () => {
      if (entry) removeStack(entry)
      const token = { cancelled: false }
      pendingClose.current = token
      queueMicrotask(() => {
        if (token.cancelled) return
        pendingClose.current = null
        historyRef.current?.close(id)
        if (!native && opener && opener.isConnected) {
          const active = document.activeElement
          if (!active || active === document.body) opener.focus({ preventScroll: true })
        }
      })
    }
  }, [id, native, opener])

  // Back: our entry was popped, so the modal closes, or comes straight back
  // when it cannot close right now.
  const onEntry = useRef(false)
  const location = historyCtx?.location
  const action = historyCtx?.action
  useEffect(() => {
    if (!location) return
    const ids = modalIdsOf(location.state)
    if (ids.includes(id)) {
      onEntry.current = true
      return
    }
    if (!onEntry.current) return
    onEntry.current = false
    if (action !== 'POP') return
    if (canCloseRef.current) onCloseRef.current()
    else historyRef.current?.open(id, true)
  }, [location, action, id])

  const panelProps = useMemo<ModalPanelProps>(() => ({
    ref,
    role: 'dialog',
    'aria-modal': 'true',
    'aria-labelledby': titleId,
    tabIndex: -1,
    'data-modal-panel': '',
  }), [ref, titleId])

  return { titleId, panelProps }
}

/**
 * ModalPanel is useModal as an element, for a modal written inline in a page
 * where a hook cannot sit behind the condition that opens it.
 */
export function ModalPanel({
  onClose,
  canClose,
  labelledBy,
  native: _native,
  history,
  children,
  ...rest
}: ModalOptions & { labelledBy: string; children: ReactNode } & Omit<HTMLAttributes<HTMLDivElement>, 'role'>) {
  const { panelProps } = useModal({ onClose, canClose, labelledBy, history })
  return createElement('div', { ...rest, ...panelProps }, children)
}
