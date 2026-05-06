import '@testing-library/jest-dom'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { server } from './test/msw'

const originalFetch = globalThis.fetch
let mswFetch = originalFetch

function resolveRelativeApiURL(input: RequestInfo | URL): RequestInfo | URL {
  if (typeof input === 'string' && input.startsWith('/api/')) {
    return new URL(input, 'http://localhost').toString()
  }
  return input
}

beforeAll(() => {
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
