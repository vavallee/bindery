import { beforeEach, describe, expect, it } from 'vitest'
import { http, HttpResponse } from 'msw'
import { apiUrl, server } from '../test/msw'
import { booksApi } from './books'

interface Seen {
  method: string
  path: string
  query: string
  body: unknown
}

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

describe('booksApi request shapes', () => {
  beforeEach(() => {
    window.history.pushState(null, '', '/')
  })

  it('encodes metadata search and lookup terms', async () => {
    const seen = record()
    await booksApi.searchAuthors('le guin')
    await booksApi.searchBooks('a/b')
    await booksApi.lookupISBN('978-0')
    await booksApi.lookupASIN('B00 X')
    expect(seen.map(s => [s.path, Object.fromEntries(new URLSearchParams(s.query))])).toEqual([
      ['/search/author', { term: 'le guin' }],
      ['/search/book', { term: 'a/b' }],
      ['/book/lookup', { isbn: '978-0' }],
      ['/book/lookup', { asin: 'B00 X' }],
    ])
  })

  it('posts addBook and reassignFile with their JSON bodies', async () => {
    const seen = record()
    await booksApi.addBook({ foreignBookId: 'OL1W', foreignAuthorId: 'OL1A', searchOnAdd: true, mediaType: 'audiobook' })
    await booksApi.reassignFile({ path: '/dl/x.epub', targetBookId: 3, format: 'ebook' })
    expect(seen.map(s => [s.method, s.path, s.body])).toEqual([
      ['POST', '/author/book', { foreignBookId: 'OL1W', foreignAuthorId: 'OL1A', searchOnAdd: true, mediaType: 'audiobook' }],
      ['POST', '/queue/manual-import/reassign', { path: '/dl/x.epub', targetBookId: 3, format: 'ebook' }],
    ])
  })

  it('adds the format to the reassign preview only when one is given', async () => {
    const seen = record()
    await booksApi.reassignFilePreview({ path: '/dl/x.epub', targetBookId: 3 })
    await booksApi.reassignFilePreview({ path: '/dl/x.m4b', targetBookId: 4, format: 'audiobook' })
    expect(Object.fromEntries(new URLSearchParams(seen[0].query))).toEqual({ path: '/dl/x.epub', targetBookId: '3' })
    expect(Object.fromEntries(new URLSearchParams(seen[1].query))).toEqual({ path: '/dl/x.m4b', targetBookId: '4', format: 'audiobook' })
    expect(seen[0].path).toBe('/queue/manual-import/reassign/preview')
  })

  it('sends every listBooks filter and omits the query string when there are none', async () => {
    const seen = record({ 'GET /book': () => HttpResponse.json({ items: [], total: 0, limit: 0, offset: 0 }) })
    await booksApi.listBooks()
    await booksApi.listBooks({
      authorId: 2, status: 'wanted', monitored: false, includeExcluded: true, limit: 5, offset: 10,
      search: 'dune', mediaType: 'ebook', sort: 'title', releaseFrom: '2026-01-01', releaseBefore: '2026-02-01',
    })
    expect(seen[0].query).toBe('')
    expect(Object.fromEntries(new URLSearchParams(seen[1].query))).toEqual({
      authorId: '2', status: 'wanted', monitored: 'false', includeExcluded: 'true', limit: '5', offset: '10',
      search: 'dune', mediaType: 'ebook', sort: 'title', releaseFrom: '2026-01-01', releaseBefore: '2026-02-01',
    })
  })

  it('collects every page of books for listAllBooks', async () => {
    let call = 0
    const seen = record({
      'GET /book': () => {
        call++
        return call === 1
          ? HttpResponse.json({ items: [{ id: 1 }], total: 2, limit: 500, offset: 0 })
          : HttpResponse.json({ items: [{ id: 2 }], total: 2, limit: 500, offset: 1 })
      },
    })
    const books = await booksApi.listAllBooks({ authorId: 9 })
    expect(books.map(b => b.id)).toEqual([1, 2])
    expect(seen.map(s => new URLSearchParams(s.query).get('offset'))).toEqual(['0', '1'])
    expect(new URLSearchParams(seen[1].query).get('authorId')).toBe('9')
  })

  it('maps the single book actions onto the right verbs and bodies', async () => {
    const seen = record({
      'DELETE /book/4': () => new HttpResponse(null, { status: 204 }),
    })
    await booksApi.getBook(4)
    await booksApi.updateBook(4, { monitored: false })
    await booksApi.deleteBook(4)
    await booksApi.deleteBook(4, true)
    await booksApi.deleteBookFile(4, '?format=ebook')
    await booksApi.searchBook(4)
    await booksApi.getLastSearchDebug()
    await booksApi.enrichAudiobook(4)
    await booksApi.toggleExcluded(4)
    await booksApi.rebindBook(4, 'hardcover', 'hc:1')
    await booksApi.rebindBook(4, 'openlibrary', 'OL1W', true)
    expect(seen.map(s => [s.method, s.path + s.query, s.body])).toEqual([
      ['GET', '/book/4', undefined],
      ['PUT', '/book/4', { monitored: false }],
      ['DELETE', '/book/4', undefined],
      ['DELETE', '/book/4?deleteFiles=true', undefined],
      ['DELETE', '/book/4/file?format=ebook', undefined],
      ['POST', '/book/4/search', undefined],
      ['GET', '/search/last-debug', undefined],
      ['POST', '/book/4/enrich-audiobook', undefined],
      ['PUT', '/book/4/exclude', undefined],
      ['POST', '/book/4/rebind', { provider: 'hardcover', foreign_id: 'hc:1', force: false }],
      ['POST', '/book/4/rebind', { provider: 'openlibrary', foreign_id: 'OL1W', force: true }],
    ])
  })
})
