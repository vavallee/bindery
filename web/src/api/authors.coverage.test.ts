import { beforeEach, describe, expect, it } from 'vitest'
import { http, HttpResponse } from 'msw'
import { apiUrl, server } from '../test/msw'
import { authorsApi } from './authors'

interface Seen {
  method: string
  path: string
  query: string
  body: unknown
}

// Records every request the module makes and answers from a per-path table, so
// each test can assert the exact method, path, query string and JSON body.
function record(responses: Record<string, () => Response> = {}): Seen[] {
  const seen: Seen[] = []
  server.use(
    http.all(apiUrl('*'), async ({ request }) => {
      const url = new URL(request.url)
      const path = url.pathname.replace('/api/v1', '')
      const text = request.method === 'GET' ? '' : await request.text()
      seen.push({ method: request.method, path, query: url.search, body: text ? JSON.parse(text) : undefined })
      const make = responses[`${request.method} ${path}`]
      return make ? make() : HttpResponse.json({})
    }),
  )
  return seen
}

describe('authorsApi request shapes', () => {
  beforeEach(() => {
    window.history.pushState(null, '', '/')
  })

  it('omits the query string when listing with no params', async () => {
    const seen = record({ 'GET /author': () => HttpResponse.json({ items: [], total: 0, limit: 0, offset: 0 }) })
    await authorsApi.listAuthors()
    expect(seen[0]).toMatchObject({ method: 'GET', path: '/author', query: '' })
  })

  it('sends every list filter it is given, including monitored=false', async () => {
    const seen = record({ 'GET /author': () => HttpResponse.json({ items: [], total: 0, limit: 0, offset: 0 }) })
    await authorsApi.listAuthors({ limit: 10, offset: 20, search: 'weir', sort: 'za', monitored: false })
    const q = new URLSearchParams(seen[0].query)
    expect(Object.fromEntries(q)).toEqual({ limit: '10', offset: '20', search: 'weir', sort: 'za', monitored: 'false' })
  })

  it('pages through every author until the total is reached', async () => {
    let call = 0
    const seen = record({
      'GET /author': () => {
        call++
        if (call === 1) return HttpResponse.json({ items: [{ id: 1 }, { id: 2 }], total: 3, limit: 500, offset: 0 })
        return HttpResponse.json({ items: [{ id: 3 }], total: 3, limit: 500, offset: 2 })
      },
    })
    const all = await authorsApi.listAllAuthors({ search: 'x' })
    expect(all.map(a => a.id)).toEqual([1, 2, 3])
    expect(seen.map(s => new URLSearchParams(s.query).get('offset'))).toEqual(['0', '2'])
    expect(new URLSearchParams(seen[0].query).get('limit')).toBe('500')
    expect(new URLSearchParams(seen[0].query).get('search')).toBe('x')
  })

  it('stops paging when the server returns an empty page short of the total', async () => {
    const seen = record({ 'GET /author': () => HttpResponse.json({ items: [], total: 99, limit: 500, offset: 0 }) })
    expect(await authorsApi.listAllAuthors()).toEqual([])
    expect(seen).toHaveLength(1)
  })

  it('maps single author CRUD calls onto the right verbs and bodies', async () => {
    const seen = record()
    await authorsApi.getAuthor(5)
    await authorsApi.addAuthor({ foreignAuthorId: 'OL1A', authorName: 'A', monitored: true, searchOnAdd: false })
    await authorsApi.updateAuthor(5, { monitored: false, clearAudiobookRootFolder: true })
    await authorsApi.applyAuthorGenres(5, ['fantasy'])
    expect(seen.map(s => [s.method, s.path, s.body])).toEqual([
      ['GET', '/author/5', undefined],
      ['POST', '/author', { foreignAuthorId: 'OL1A', authorName: 'A', monitored: true, searchOnAdd: false }],
      ['PUT', '/author/5', { monitored: false, clearAudiobookRootFolder: true }],
      ['PUT', '/author/5/genres', { genres: ['fantasy'] }],
    ])
  })

  it('only asks to delete files when told to', async () => {
    const seen = record({
      'DELETE /author/5': () => new HttpResponse(null, { status: 204 }),
    })
    await authorsApi.deleteAuthor(5)
    await authorsApi.deleteAuthor(5, true)
    expect(seen.map(s => [s.method, s.path, s.query])).toEqual([
      ['DELETE', '/author/5', ''],
      ['DELETE', '/author/5', '?deleteFiles=true'],
    ])
  })

  it('reaches refresh, reconciliation, duplicates, aliases, series and merge endpoints', async () => {
    const seen = record()
    await authorsApi.refreshAuthor(5)
    await authorsApi.previewAuthorCatalogueReconciliation(5)
    await authorsApi.applyAuthorCatalogueReconciliation(5, [1, 2])
    await authorsApi.listAuthorDuplicateCandidates(5)
    await authorsApi.listAuthorAliases(5)
    await authorsApi.deleteAuthorAlias(5, 9)
    await authorsApi.listAuthorSeries(5)
    await authorsApi.mergeAuthors(5, 6)
    await authorsApi.mergeAuthors(5, 7, false)
    await authorsApi.refreshAllAuthors()
    expect(seen.map(s => [s.method, s.path, s.body])).toEqual([
      ['POST', '/author/5/refresh', undefined],
      ['GET', '/author/5/catalogue-reconciliation', undefined],
      ['POST', '/author/5/catalogue-reconciliation', { bookIds: [1, 2] }],
      ['GET', '/author/5/duplicate-candidates', undefined],
      ['GET', '/author/5/aliases', undefined],
      ['DELETE', '/author/5/aliases/9', undefined],
      ['GET', '/author/5/series', undefined],
      ['POST', '/author/5/merge', { sourceId: 6, overwriteDefaults: true }],
      ['POST', '/author/5/merge', { sourceId: 7, overwriteDefaults: false }],
      ['POST', '/authors/refresh-all', undefined],
    ])
  })

  it('encodes the relink search term and sends a candidate only when given one', async () => {
    const seen = record()
    await authorsApi.searchAuthorLinkCandidates(5, 'a & b')
    await authorsApi.relinkAuthorUpstream(5)
    await authorsApi.relinkAuthorUpstream(5, { foreignAuthorId: 'OL2A', authorName: 'B' })
    expect(seen[0]).toMatchObject({ method: 'GET', path: '/author/5/relink-upstream/candidates', query: '?term=a%20%26%20b' })
    expect(new URLSearchParams(seen[0].query).get('term')).toBe('a & b')
    expect(seen[1]).toMatchObject({ method: 'POST', path: '/author/5/relink-upstream', body: undefined })
    expect(seen[2]).toMatchObject({ method: 'POST', body: { foreignAuthorId: 'OL2A', authorName: 'B' } })
  })

  it('treats a 404 from an older server as no refresh-all status', async () => {
    record({ 'GET /authors/refresh-all/status': () => HttpResponse.json({ error: 'not found' }, { status: 404 }) })
    await expect(authorsApi.refreshAllAuthorsStatus()).resolves.toBeNull()
  })

  it('returns the refresh-all status and rethrows anything but a 404', async () => {
    record({ 'GET /authors/refresh-all/status': () => HttpResponse.json({ status: 'running', total: 4, done: 1, failed: 0, started_at: '' }) })
    await expect(authorsApi.refreshAllAuthorsStatus()).resolves.toMatchObject({ status: 'running', done: 1 })

    record({ 'GET /authors/refresh-all/status': () => HttpResponse.json({ error: 'boom' }, { status: 500 }) })
    await expect(authorsApi.refreshAllAuthorsStatus()).rejects.toThrow('boom')
  })
})
