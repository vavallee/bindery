import { setupServer } from 'msw/node'

const API_BASE = 'http://localhost/api/v1'

export const server = setupServer()

export function apiUrl(path: string): string {
  return `${API_BASE}${path.startsWith('/') ? path : `/${path}`}`
}
