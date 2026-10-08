import { lazy, type ComponentType, type LazyExoticComponent } from 'react'

// Route and settings tab chunks are fetched by hashed file name the first time
// they are opened. A tab left open across an upgrade still holds the old
// index, so its next navigation asks for a chunk the new build no longer
// serves, the import rejects, and the error page shows. Reloading fetches the
// new index and its chunk names.
//
// The guard is per chunk: sessionStorage records which chunks have already
// had their reload, and a chunk is only taken off that list when it loads
// itself. A single flag cleared by any successful load would loop on nested
// chunks: a settings tab whose chunk always fails would reload, the Settings
// page chunk would load and clear the flag, and the tab (still in the URL)
// would fail and reload again, forever. With the per chunk record a chunk
// that still fails after its reload goes to the error boundary.

export const CHUNK_RELOAD_KEY = 'bindery:chunk-reload'

// Messages for a failed dynamic import across browsers: Chromium, Firefox,
// Safari, and Vite's own preload helper. The MIME type variants cover a chunk
// that came back as an HTML page (a server or proxy answering a missing file
// with the app shell); WebKit on iOS reports only "'text/html' is not a valid
// JavaScript MIME type." for that, never a failed fetch.
const CHUNK_ERROR = /Failed to fetch dynamically imported module|error loading dynamically imported module|Importing a module script failed|is not a valid JavaScript MIME type|Failed to load module script|disallowed MIME type|Unable to preload CSS|ChunkLoadError|Loading (CSS )?chunk .* failed/i

export function isChunkLoadError(err: unknown): boolean {
  if (err instanceof Error) return CHUNK_ERROR.test(`${err.name}: ${err.message}`)
  return typeof err === 'string' && CHUNK_ERROR.test(err)
}

// The chunks that have had their reload, or null when storage is unavailable.
function readReloaded(): Record<string, number> | null {
  try {
    const raw = window.sessionStorage.getItem(CHUNK_RELOAD_KEY)
    if (!raw) return {}
    const parsed: unknown = JSON.parse(raw)
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, number> : {}
  } catch {
    return null
  }
}

function writeReloaded(record: Record<string, number>): boolean {
  try {
    if (Object.keys(record).length === 0) window.sessionStorage.removeItem(CHUNK_RELOAD_KEY)
    else window.sessionStorage.setItem(CHUNK_RELOAD_KEY, JSON.stringify(record))
    return true
  } catch {
    return false
  }
}

// key names the chunk in the guard; callers pass the import path. It has to be
// stable across builds: the factory's source would carry the old build's
// hashed file name before the reload and the new one after it.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function lazyWithReload<T extends ComponentType<any>>(
  factory: () => Promise<{ default: T }>,
  key: string,
): LazyExoticComponent<T> {
  return lazy(async () => {
    try {
      const mod = await factory()
      const record = readReloaded()
      if (record && key in record) {
        delete record[key]
        writeReloaded(record)
      }
      return mod
    } catch (err) {
      // Reload only when the record can be both read and written; without
      // storage there is no loop guard, so the error page is the safer end.
      const record = readReloaded()
      if (isChunkLoadError(err) && record && !(key in record) && writeReloaded({ ...record, [key]: Date.now() })) {
        window.location.reload()
        // Stay suspended until the reload replaces the page.
        return new Promise<never>(() => {})
      }
      throw err
    }
  })
}
