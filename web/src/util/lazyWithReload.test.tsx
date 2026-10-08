import { Suspense, type ComponentType } from 'react'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ErrorBoundary from '../components/ErrorBoundary'
import { CHUNK_RELOAD_KEY, isChunkLoadError, lazyWithReload } from './lazyWithReload'

// The error a browser raises when a lazy route's chunk 404s after an upgrade.
const chunkError = () => new TypeError('Failed to fetch dynamically imported module: http://localhost/assets/BooksPage-abc123.js')

function Page() {
  return <p>page loaded</p>
}

function mount(factory: () => Promise<{ default: ComponentType }>, key = './pages/BooksPage') {
  const Lazy = lazyWithReload(factory, key)
  return render(
    <ErrorBoundary>
      <Suspense fallback={<p>loading</p>}>
        <Lazy />
      </Suspense>
    </ErrorBoundary>,
  )
}

describe('lazyWithReload', () => {
  const original = window.location
  let reload: ReturnType<typeof vi.fn>

  beforeEach(() => {
    sessionStorage.clear()
    reload = vi.fn()
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...original, reload },
    })
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  afterEach(() => {
    Object.defineProperty(window, 'location', { configurable: true, value: original })
    sessionStorage.clear()
    vi.restoreAllMocks()
  })

  it('reloads the page once when a chunk fails to load, instead of showing the error page', async () => {
    mount(() => Promise.reject(chunkError()))
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
    expect(sessionStorage.getItem(CHUNK_RELOAD_KEY)).not.toBeNull()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByText('loading')).toBeInTheDocument()
  })

  it('shows the error page instead of reloading again when the chunk still fails after the reload', async () => {
    sessionStorage.setItem(CHUNK_RELOAD_KEY, JSON.stringify({ './pages/BooksPage': 1 }))
    mount(() => Promise.reject(chunkError()))
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(reload).not.toHaveBeenCalled()
  })

  it('does not reload for an error that is not a failed chunk load', async () => {
    mount(() => Promise.reject(new Error('kaboom')))
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(reload).not.toHaveBeenCalled()
  })

  it('clears the guard after a chunk loads, so a later upgrade can reload again', async () => {
    sessionStorage.setItem(CHUNK_RELOAD_KEY, JSON.stringify({ './pages/BooksPage': 1 }))
    mount(() => Promise.resolve({ default: Page }))
    expect(await screen.findByText('page loaded')).toBeInTheDocument()
    expect(sessionStorage.getItem(CHUNK_RELOAD_KEY)).toBeNull()
    expect(reload).not.toHaveBeenCalled()
  })

  it('keeps the guard of a chunk when a different chunk loads', async () => {
    sessionStorage.setItem(CHUNK_RELOAD_KEY, JSON.stringify({ './settings/GeneralTab': 1 }))
    mount(() => Promise.resolve({ default: Page }), './pages/SettingsPage')
    expect(await screen.findByText('page loaded')).toBeInTheDocument()
    expect(JSON.parse(sessionStorage.getItem(CHUNK_RELOAD_KEY) ?? '{}')).toHaveProperty(['./settings/GeneralTab'])
  })

  // A settings tab is a lazy chunk inside the lazy Settings page chunk, and the
  // tab is in the URL, so after the reload the page loads the same tab again.
  // A guard cleared by any chunk loading let the page chunk clear it before the
  // tab failed again, and the tab reloaded forever.
  it('stops after one reload when a nested chunk keeps failing under a parent that loads', async () => {
    const renderPage = () => {
      const Tab = lazyWithReload<ComponentType>(() => Promise.reject(chunkError()), './settings/GeneralTab')
      const Settings = lazyWithReload<ComponentType>(
        () => Promise.resolve({ default: () => <Suspense fallback={<p>tab loading</p>}><Tab /></Suspense> }),
        './pages/SettingsPage',
      )
      return render(
        <ErrorBoundary>
          <Suspense fallback={<p>loading</p>}>
            <Settings />
          </Suspense>
        </ErrorBoundary>,
      )
    }

    // First visit: the tab chunk is gone, so the page reloads once.
    const first = renderPage()
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
    first.unmount()

    // After the reload (fresh lazy components, same session): the Settings
    // chunk loads, the tab still fails, and the error shows instead of a
    // second reload.
    renderPage()
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('recognises the chunk error messages of each browser', () => {
    expect(isChunkLoadError(chunkError())).toBe(true)
    expect(isChunkLoadError(new TypeError('error loading dynamically imported module: x.js'))).toBe(true)
    expect(isChunkLoadError(new TypeError('Importing a module script failed.'))).toBe(true)
    expect(isChunkLoadError(new Error('Unable to preload CSS for /assets/x.css'))).toBe(true)
    expect(isChunkLoadError(new Error('kaboom'))).toBe(false)
    expect(isChunkLoadError(undefined)).toBe(false)
  })

  // A server that answered a missing chunk with the HTML app shell made iOS
  // WebKit fail the import on the MIME type rather than the fetch, and the
  // stale tab went to the error page instead of reloading.
  it('recognises the MIME type rejections of WebKit, Chromium and Firefox', () => {
    expect(isChunkLoadError(new TypeError("'text/html' is not a valid JavaScript MIME type."))).toBe(true)
    expect(isChunkLoadError(new TypeError('Failed to load module script: Expected a JavaScript module script but the server responded with a MIME type of "text/html".'))).toBe(true)
    expect(isChunkLoadError(new TypeError('Loading module from "http://localhost/assets/x.js" was blocked because of a disallowed MIME type ("text/html").'))).toBe(true)
    expect(isChunkLoadError(new TypeError('Importing a module script failed.'))).toBe(true)
    expect(isChunkLoadError(new TypeError('error loading dynamically imported module: http://localhost/assets/x.js'))).toBe(true)
    expect(isChunkLoadError("'text/html' is not a valid JavaScript MIME type.")).toBe(true)
    expect(isChunkLoadError(new TypeError("undefined is not an object (evaluating 'x.length')"))).toBe(false)
  })

  it('reloads once for a WebKit MIME type rejection and then shows the error page', async () => {
    const mimeError = () => new TypeError("'text/html' is not a valid JavaScript MIME type.")
    mount(() => Promise.reject(mimeError()))
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
    cleanup()
    mount(() => Promise.reject(mimeError()))
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(reload).toHaveBeenCalledTimes(1)
  })
})

// Every code split route and settings tab must go through the wrapper: a bare
// React.lazy brings the error page back for that one route.
const sources = import.meta.glob(['../App.tsx', '../pages/SettingsPage.tsx'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

describe('lazy routes', () => {
  it.each(Object.keys(sources))('%s loads every chunk through lazyWithReload', file => {
    const src = sources[file]
    expect(src).toMatch(/lazyWithReload\(\(\) => import\(/)
    expect(src).not.toMatch(/\blazy\(\(\) => import\(/)
    // Each chunk is keyed by its own import path, so no two share a guard.
    const calls = src.match(/lazyWithReload\(\(\) => import\(/g) ?? []
    const keyed = src.match(/lazyWithReload\(\(\) => import\('([^']+)'\), '\1'\)/g) ?? []
    expect(keyed.length).toBe(calls.length)
  })
})
