import { beforeEach, describe, expect, it } from 'vitest'
import { http, HttpResponse } from 'msw'
import { apiUrl, server } from '../test/msw'
import { absApi } from './abs'
import { seriesApi } from './series'
import { authApi } from './auth'

// Every endpoint helper is a thin wrapper over request(). What matters is the
// wire contract the backend routes on: method, path, query string and JSON
// body. One catch-all handler records each call so the cases below can assert
// that contract without a handler per route.

interface Recorded {
  method: string
  path: string
  body: unknown
}

let calls: Recorded[] = []

beforeEach(() => {
  calls = []
  server.use(
    http.all(apiUrl('*'), async ({ request }) => {
      const url = new URL(request.url)
      const text = request.method === 'GET' || request.method === 'HEAD' ? '' : await request.text()
      calls.push({
        method: request.method,
        path: url.pathname.replace(/^\/api\/v1/, '') + url.search,
        body: text ? JSON.parse(text) : undefined,
      })
      return HttpResponse.json({ ok: true })
    }),
  )
})

function last(): Recorded {
  expect(calls.length).toBeGreaterThan(0)
  return calls[calls.length - 1]
}

type Case = [name: string, call: () => Promise<unknown>, method: string, path: string, body?: unknown]

const absCases: Case[] = [
  ['absConfig', () => absApi.absConfig(), 'GET', '/abs/config'],
  [
    'absSetConfig',
    () => absApi.absSetConfig({ baseUrl: 'http://abs', label: 'ABS', enabled: true, libraryId: 'lib', pathRemap: '', apiKey: 'k' }),
    'PUT',
    '/abs/config',
    { baseUrl: 'http://abs', label: 'ABS', enabled: true, libraryId: 'lib', pathRemap: '', apiKey: 'k' },
  ],
  ['absTest with no args sends an empty object', () => absApi.absTest(), 'POST', '/abs/test', {}],
  ['absTest with args', () => absApi.absTest({ baseUrl: 'http://abs' }), 'POST', '/abs/test', { baseUrl: 'http://abs' }],
  ['absLibraries with no args', () => absApi.absLibraries(), 'POST', '/abs/libraries', {}],
  ['absLibraries with args', () => absApi.absLibraries({ apiKey: 'k' }), 'POST', '/abs/libraries', { apiKey: 'k' }],
  ['absImportStart with no args', () => absApi.absImportStart(), 'POST', '/abs/import', {}],
  ['absImportStart dry run', () => absApi.absImportStart({ dryRun: true }), 'POST', '/abs/import', { dryRun: true }],
  ['absImportStatus', () => absApi.absImportStatus(), 'GET', '/abs/import/status'],
  ['absImportRuns', () => absApi.absImportRuns(), 'GET', '/abs/import/runs'],
  ['absImportRollbackPreview', () => absApi.absImportRollbackPreview(7), 'POST', '/abs/import/runs/7/rollback/preview'],
  ['absImportRollback', () => absApi.absImportRollback(7), 'POST', '/abs/import/runs/7/rollback'],
  ['absReviewItems without paging', () => absApi.absReviewItems(), 'GET', '/abs/review'],
  ['absReviewItems with paging', () => absApi.absReviewItems({ limit: 20, offset: 40 }), 'GET', '/abs/review?limit=20&offset=40'],
  ['approveAbsReviewItem', () => absApi.approveAbsReviewItem(3), 'POST', '/abs/review/3/approve'],
  [
    'resolveAbsReviewAuthor',
    () => absApi.resolveAbsReviewAuthor(3, { foreignAuthorId: 'OL1A', authorName: 'Ann', applyTo: 'same_author' }),
    'POST',
    '/abs/review/3/resolve-author',
    { foreignAuthorId: 'OL1A', authorName: 'Ann', applyTo: 'same_author' },
  ],
  [
    'resolveAbsReviewBook',
    () => absApi.resolveAbsReviewBook(3, { foreignBookId: 'OL1W', title: 'T', editedTitle: 'T2' }),
    'POST',
    '/abs/review/3/resolve-book',
    { foreignBookId: 'OL1W', title: 'T', editedTitle: 'T2' },
  ],
  ['dismissAbsReviewItem', () => absApi.dismissAbsReviewItem(3), 'POST', '/abs/review/3/dismiss'],
  ['dismissAbsReviewRun', () => absApi.dismissAbsReviewRun(9), 'POST', '/abs/review/dismiss-run/9'],
  ['absConflicts without paging', () => absApi.absConflicts(), 'GET', '/abs/conflicts'],
  ['absConflicts with limit only', () => absApi.absConflicts({ limit: 5 }), 'GET', '/abs/conflicts?limit=5'],
  ['resolveAbsConflict', () => absApi.resolveAbsConflict(4, 'upstream'), 'POST', '/abs/conflicts/4/resolve', { source: 'upstream' }],
]

const hardcoverResult = {
  foreignId: 'hc:1',
  providerId: '1',
  title: 'Saga',
  authorName: 'Ann',
  bookCount: 3,
  readersCount: 10,
  books: ['One'],
}

const seriesCases: Case[] = [
  ['listSeries', () => seriesApi.listSeries(), 'GET', '/series'],
  ['createSeries', () => seriesApi.createSeries({ title: 'Saga' }), 'POST', '/series', { title: 'Saga' }],
  ['getSeries', () => seriesApi.getSeries(2), 'GET', '/series/2'],
  ['updateSeries', () => seriesApi.updateSeries(2, { title: 'New' }), 'PUT', '/series/2', { title: 'New' }],
  ['monitorSeries', () => seriesApi.monitorSeries(2, false), 'PATCH', '/series/2', { monitored: false }],
  ['deleteSeries', () => seriesApi.deleteSeries(2), 'DELETE', '/series/2'],
  ['mergeSeries', () => seriesApi.mergeSeries(2, { sourceIds: [3], dryRun: true }), 'POST', '/series/2/merge', { sourceIds: [3], dryRun: true }],
  [
    'linkBookToSeries',
    () => seriesApi.linkBookToSeries(2, { bookId: 5, positionInSeries: '1', primarySeries: true }),
    'POST',
    '/series/2/books',
    { bookId: 5, positionInSeries: '1', primarySeries: true },
  ],
  ['removeBookFromSeries', () => seriesApi.removeBookFromSeries(2, 5), 'DELETE', '/series/2/books/5'],
  ['setPrimarySeriesForBook', () => seriesApi.setPrimarySeriesForBook(2, 5), 'PUT', '/series/2/books/5/primary'],
  ['fillSeries without a book sends no body', () => seriesApi.fillSeries(2), 'POST', '/series/2/fill'],
  [
    'fillSeries with a book',
    () => seriesApi.fillSeries(2, { foreignBookId: 'hc:9', position: '2', mediaType: 'audiobook' }),
    'POST',
    '/series/2/fill',
    { foreignBookId: 'hc:9', position: '2', mediaType: 'audiobook' },
  ],
  ['fillSeriesAll without a media type sends no body', () => seriesApi.fillSeriesAll(2), 'POST', '/series/2/fill'],
  ['fillSeriesAll with a media type', () => seriesApi.fillSeriesAll(2, 'ebook'), 'POST', '/series/2/fill', { mediaType: 'ebook' }],
  ['applySeriesGenres', () => seriesApi.applySeriesGenres(2, ['Fantasy']), 'PUT', '/series/2/genres', { genres: ['Fantasy'] }],
  ['clearSeriesGenres', () => seriesApi.clearSeriesGenres(2), 'DELETE', '/series/2/genres'],
  [
    'searchHardcoverSeries encodes the term and defaults the limit',
    () => seriesApi.searchHardcoverSeries('a & b'),
    'GET',
    '/series/hardcover/search?term=a%20%26%20b&limit=10',
  ],
  ['searchHardcoverSeries with a limit', () => seriesApi.searchHardcoverSeries('x', 3), 'GET', '/series/hardcover/search?term=x&limit=3'],
  ['getSeriesHardcoverLink', () => seriesApi.getSeriesHardcoverLink(2), 'GET', '/series/2/hardcover-link'],
  ['autoLinkSeriesHardcover', () => seriesApi.autoLinkSeriesHardcover(2), 'POST', '/series/2/hardcover-link/auto'],
  ['linkSeriesHardcover', () => seriesApi.linkSeriesHardcover(2, hardcoverResult), 'PUT', '/series/2/hardcover-link', hardcoverResult],
  ['unlinkSeriesHardcover', () => seriesApi.unlinkSeriesHardcover(2), 'DELETE', '/series/2/hardcover-link'],
  ['getSeriesHardcoverDiff', () => seriesApi.getSeriesHardcoverDiff(2), 'GET', '/series/2/hardcover-diff'],
]

const authCases: Case[] = [
  ['authStatus', () => authApi.authStatus(), 'GET', '/auth/status'],
  ['oidcProviders', () => authApi.oidcProviders(), 'GET', '/auth/oidc/providers'],
  [
    'oidcSetProviders',
    () => authApi.oidcSetProviders([{ id: 'g', name: 'G', issuer: 'https://g', client_id: 'c', client_secret: 's', scopes: ['openid'] }]),
    'PUT',
    '/auth/oidc/providers',
    [{ id: 'g', name: 'G', issuer: 'https://g', client_id: 'c', client_secret: 's', scopes: ['openid'] }],
  ],
  ['oidcRedirectBase', () => authApi.oidcRedirectBase(), 'GET', '/auth/oidc/redirect-base'],
  ['oidcTestDiscovery', () => authApi.oidcTestDiscovery('https://g'), 'POST', '/auth/oidc/test-discovery', { issuer: 'https://g' }],
  ['authSetup', () => authApi.authSetup('admin', 'pw'), 'POST', '/auth/setup', { username: 'admin', password: 'pw' }],
  ['authConfig', () => authApi.authConfig(), 'GET', '/auth/config'],
  [
    'authChangePassword',
    () => authApi.authChangePassword('old', 'new'),
    'POST',
    '/auth/password',
    { currentPassword: 'old', newPassword: 'new' },
  ],
  ['authRegenerateApiKey', () => authApi.authRegenerateApiKey(), 'POST', '/auth/apikey/regenerate'],
  ['authRotateSessionSecret', () => authApi.authRotateSessionSecret(), 'POST', '/auth/session-secret/rotate'],
  ['authSetMode', () => authApi.authSetMode('local-only'), 'PUT', '/auth/mode', { mode: 'local-only' }],
  ['listUsers', () => authApi.listUsers(), 'GET', '/auth/users'],
  [
    'createUser',
    () => authApi.createUser('reader', 'pw', 'user'),
    'POST',
    '/auth/users',
    { username: 'reader', password: 'pw', role: 'user' },
  ],
  ['deleteUser without a plan', () => authApi.deleteUser(4), 'DELETE', '/auth/users/4'],
  ['deleteUser purge', () => authApi.deleteUser(4, { strategy: 'purge' }), 'DELETE', '/auth/users/4?strategy=purge'],
  [
    'deleteUser reassign to a user',
    () => authApi.deleteUser(4, { strategy: 'reassign', reassignTo: 1 }),
    'DELETE',
    '/auth/users/4?strategy=reassign&reassignTo=1',
  ],
  [
    'deleteUser reassign to global omits reassignTo',
    () => authApi.deleteUser(4, { strategy: 'reassign', reassignTo: null }),
    'DELETE',
    '/auth/users/4?strategy=reassign',
  ],
  ['setUserRole', () => authApi.setUserRole(4, 'requester'), 'PUT', '/auth/users/4/role', { role: 'requester' }],
  ['setUserAutoApprove', () => authApi.setUserAutoApprove(4, true), 'PUT', '/auth/users/4/auto-approve', { enabled: true }],
  ['resetUserPassword', () => authApi.resetUserPassword(4, 'pw2'), 'PUT', '/auth/users/4/reset-password', { password: 'pw2' }],
]

describe.each([
  ['absApi', absCases],
  ['seriesApi', seriesCases],
  ['authApi', authCases],
])('%s wire contract', (_group, cases) => {
  it.each(cases)('%s', async (_name, call, method, path, body) => {
    await call()
    const got = last()
    expect(got.method).toBe(method)
    expect(got.path).toBe(path)
    expect(got.body).toEqual(body)
  })
})
