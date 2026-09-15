import '@testing-library/jest-dom'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { server } from './test/msw'

const originalFetch = globalThis.fetch
let mswFetch = originalFetch
const apiOrigin = 'http://localhost'

function localApiURL(pathname: string, search = '', hash = '') {
  return new URL(`${pathname}${search}${hash}`, apiOrigin)
}

function resolveRelativeApiURL(input: RequestInfo | URL): RequestInfo | URL {
  if (typeof input === 'string' && input.startsWith('/api/')) {
    return new URL(input, apiOrigin).toString()
  }
  if (input instanceof URL && input.pathname.startsWith('/api/')) {
    return localApiURL(input.pathname, input.search, input.hash)
  }
  if (typeof Request !== 'undefined' && input instanceof Request) {
    const url = new URL(input.url)
    if (url.pathname.startsWith('/api/')) {
      return new Request(localApiURL(url.pathname, url.search, url.hash), input)
    }
  }
  return input
}

beforeAll(() => {
  // jsdom lacks native dialog methods. Model the open state for component
  // tests; browser checks cover focus trapping and Escape behavior.
  Object.defineProperties(HTMLDialogElement.prototype, {
    showModal: { configurable: true, value: function (this: HTMLDialogElement) { this.setAttribute('open', '') } },
    close: { configurable: true, value: function (this: HTMLDialogElement) { this.removeAttribute('open') } },
  })
  server.listen({ onUnhandledRequest: 'error' })
  mswFetch = globalThis.fetch

  const fetchWithRelativeApiURLs: typeof fetch = (input, init) =>
    mswFetch(resolveRelativeApiURL(input), init)

  globalThis.fetch = fetchWithRelativeApiURLs
  Object.defineProperty(window, 'fetch', { value: fetchWithRelativeApiURLs, configurable: true })
})

afterEach(() => {
  server.resetHandlers()
})

afterAll(() => {
  server.close()
  globalThis.fetch = originalFetch
  Object.defineProperty(window, 'fetch', { value: originalFetch, configurable: true })
})
