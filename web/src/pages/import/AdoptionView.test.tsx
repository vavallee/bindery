import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { MemoryRouter } from 'react-router'
import { apiUrl, server } from '../../test/msw'
import type { AdoptionItem, AdoptionListResponse } from '../../api/client'
import AdoptionView from './AdoptionView'

// AdoptionView talks to the server through the real api client, mocked at the
// network with MSW (T1), so a fetch spy sees exactly what the page requests.
// Strings render from en.json, so the assertions read like the page does.
vi.mock('react-i18next', async () => {
  const en = (await import('../../i18n/locales/en.json')).default as Record<string, unknown>
  const lookup = (key: string): unknown =>
    key.split('.').reduce<unknown>((n, p) => (n && typeof n === 'object' ? (n as Record<string, unknown>)[p] : undefined), en)
  const t = (key: string, options?: string | Record<string, unknown>) => {
    const opts = typeof options === 'object' ? options : {}
    let value = lookup(key)
    if (typeof opts.count === 'number') value = lookup(`${key}_${opts.count === 1 ? 'one' : 'other'}`) ?? value
    if (typeof value !== 'string') value = typeof options === 'string' ? options : (opts.defaultValue as string) ?? key
    return (value as string).replace(/\{\{(\w+)\}\}/g, (_, k) => String(opts[k] ?? ''))
  }
  return { useTranslation: () => ({ t, i18n: { language: 'en' } }) }
})

function item(overrides: Partial<AdoptionItem> & Pick<AdoptionItem, 'id'>): AdoptionItem {
  return {
    kind: 'file', format: 'ebook', fileCount: 1, sizeBytes: 2048, relPath: `Andy Weir/Book ${overrides.id}.epub`,
    rootPath: '/books', authorFolder: 'Andy Weir', parsedTitle: `Book ${overrides.id}`, parsedAuthor: 'Andy Weir',
    reason: 'no_title_match', candidates: [], topScore: 0, state: 'pending', bookCreated: false, authorCreated: false,
    members: [`Book ${overrides.id}.epub`], firstSeenAt: '2026-09-17T00:00:00Z',
    ...overrides,
  }
}

const martian = { id: 42, title: 'The Martian', authorId: 7, authorName: 'Andy Weir', status: 'wanted', mediaType: 'ebook', monitored: true }

function listResponse(items: AdoptionItem[], overrides: Partial<AdoptionListResponse> = {}): AdoptionListResponse {
  return {
    items,
    total: items.length,
    facets: { reasons: [{ value: 'no_title_match', count: items.length }], formats: [{ value: 'ebook', count: items.length }], folders: [] },
    summary: { pending: items.length, pendingFiles: items.length, ignored: 0, adopted: 0 },
    scan: { ran: true, ranAt: new Date(Date.now() - 2 * 3600_000).toISOString(), running: false, filesFound: 10, truncated: false, noFilesFound: false },
    ...overrides,
  }
}

const suggested = item({
  id: 1, parsedTitle: 'The Martian', relPath: 'Andy Weir/The Martian.epub',
  candidates: [{ book: martian, score: 0.97 }], topScore: 0.97,
})
const weak = item({
  id: 4, parsedTitle: 'A Martyrs Tale', relPath: 'Andy Weir/A Martyrs Tale.epub',
  candidates: [{ book: martian, score: 0.82 }], topScore: 0.82,
})
const unsuggested = item({ id: 2, parsedTitle: 'Mystery Notes', relPath: 'Andy Weir/Mystery Notes.epub' })

let fetchSpy: { mock: { calls: Parameters<typeof fetch>[] }; mockRestore: () => void }

function requestedURLs(): string[] {
  return fetchSpy.mock.calls.map(([input]: Parameters<typeof fetch>) => (typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url))
}

function serve(response: AdoptionListResponse) {
  server.use(
    http.get(apiUrl('/library/unmatched'), () => HttpResponse.json(response)),
    http.get(apiUrl('/library/unmatched/summary'), () => HttpResponse.json({ ...response.summary, scan: response.scan })),
  )
}

async function renderView() {
  render(<MemoryRouter><AdoptionView /></MemoryRouter>)
  return screen.findByRole('table', { name: /Books in your library that need a decision/ })
}

beforeEach(() => {
  localStorage.clear()
  fetchSpy = vi.spyOn(window, 'fetch')
})

afterEach(() => {
  fetchSpy.mockRestore()
})

describe('AdoptionView', () => {
  // #2944: two 1008 byte .txt files under the audiobooks root were listed as
  // ordinary ebooks with a one click Confirm into the book their folder named.
  it('labels a file too small to be a book and offers Ignore, not adoption', async () => {
    const tiny = item({
      id: 60, parsedTitle: 'Die 6. Geisel', parsedAuthor: 'James Patterson', authorFolder: 'James Patterson',
      relPath: 'James Patterson/Die 6. Geisel ()/Die 6. Geisel.txt', rootPath: '/data/media/audiobooks',
      rootFormat: 'audiobook', sizeBytes: 1008, reason: 'too_small',
    })
    serve(listResponse([tiny]))
    let ignored = false
    server.use(http.post(apiUrl('/library/unmatched/60/ignore'), () => {
      ignored = true
      return HttpResponse.json({ ...tiny, state: 'ignored' })
    }))
    const table = await renderView()

    const row = within(table).getByRole('row', { name: 'Die 6. Geisel' })
    expect(within(row).getByText('Too small to be a book')).toBeInTheDocument()
    expect(within(row).getByText('In your audiobooks folder')).toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Confirm' })).toBeNull()
    expect(within(row).queryByRole('button', { name: 'Choose book' })).toBeNull()
    fireEvent.click(within(row).getByRole('button', { name: 'Ignore' }))
    await waitFor(() => expect(ignored).toBe(true))
  })

  it('labels an ebook under the audiobooks root and makes its match a possible one', async () => {
    const misplaced = item({
      id: 61, parsedTitle: 'The Martian', relPath: 'Andy Weir/The Martian/The Martian.epub',
      rootPath: '/audiobooks', rootFormat: 'audiobook', sizeBytes: 300_000,
      candidates: [{ book: martian, score: 1 }], topScore: 1,
    })
    serve(listResponse([misplaced]))
    const table = await renderView()

    const row = within(table).getByRole('row', { name: 'The Martian' })
    expect(within(row).getByText('In your audiobooks folder')).toBeInTheDocument()
    expect(within(row).getByText('Possible match')).toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Confirm' })).toBeNull()
  })

  it('confirms a strong suggestion in one click, then undoes it', async () => {
    serve(listResponse([suggested, unsuggested]))
    let adoptBody: unknown = null
    server.use(
      http.post(apiUrl('/library/unmatched/1/adopt'), async ({ request }) => {
        adoptBody = await request.json()
        return HttpResponse.json({ ...suggested, state: 'adopted', book: martian })
      }),
      http.post(apiUrl('/library/unmatched/1/undo'), () => HttpResponse.json(suggested)),
    )
    const table = await renderView()

    const row = within(table).getByRole('row', { name: 'The Martian' })
    expect(within(row).getByText('Strong match')).toBeInTheDocument()
    fireEvent.click(within(row).getByRole('button', { name: 'Confirm' }))

    expect(await within(row).findByText(/Adopted as The Martian by Andy Weir\./)).toBeInTheDocument()
    expect(adoptBody).toEqual({ bookId: 42 })

    fireEvent.click(within(row).getByRole('button', { name: 'Undo' }))
    expect(await within(row).findByRole('button', { name: 'Confirm' })).toBeInTheDocument()
  })

  it('says quietly when Undo kept a book that is now in use', async () => {
    serve(listResponse([suggested]))
    server.use(
      http.post(apiUrl('/library/unmatched/1/adopt'), () => HttpResponse.json({ ...suggested, state: 'adopted', book: martian, bookCreated: true })),
      http.post(apiUrl('/library/unmatched/1/undo'), () =>
        HttpResponse.json({ ...suggested, message: 'The files are no longer adopted. The book it added stays in your library because it has been used since.' })),
    )
    const table = await renderView()
    const row = within(table).getByRole('row', { name: 'The Martian' })
    fireEvent.click(within(row).getByRole('button', { name: 'Confirm' }))
    fireEvent.click(await within(row).findByRole('button', { name: 'Undo' }))

    expect(await within(row).findByText('Files removed. The book stayed because it is now in use.')).toBeInTheDocument()
    expect(within(row).getByRole('button', { name: 'Confirm' })).toBeInTheDocument()
    expect(within(row).queryByRole('alert')).toBeNull()
  })

  it('says nothing extra when Undo removed the book too', async () => {
    serve(listResponse([suggested]))
    server.use(
      http.post(apiUrl('/library/unmatched/1/adopt'), () => HttpResponse.json({ ...suggested, state: 'adopted', book: martian })),
      http.post(apiUrl('/library/unmatched/1/undo'), () => HttpResponse.json(suggested)),
    )
    const table = await renderView()
    const row = within(table).getByRole('row', { name: 'The Martian' })
    fireEvent.click(within(row).getByRole('button', { name: 'Confirm' }))
    fireEvent.click(await within(row).findByRole('button', { name: 'Undo' }))
    await within(row).findByRole('button', { name: 'Confirm' })
    expect(within(row).queryByText(/The book stayed/)).toBeNull()
  })

  it('reverts an optimistic adopt and shows the error on the row', async () => {
    serve(listResponse([suggested]))
    server.use(
      http.post(apiUrl('/library/unmatched/1/adopt'), () =>
        HttpResponse.json({ error: 'Provenance.epub already belongs to a book in your library.' }, { status: 409 })),
    )
    const table = await renderView()
    const row = within(table).getByRole('row', { name: 'The Martian' })
    fireEvent.click(within(row).getByRole('button', { name: 'Confirm' }))

    expect(await within(row).findByRole('alert')).toHaveTextContent('already belongs to a book')
    expect(within(row).getByRole('button', { name: 'Confirm' })).toBeInTheDocument()
  })

  it('offers a weak suggestion as a possible match that opens the editor preselected', async () => {
    serve(listResponse([weak]))
    server.use(http.get(apiUrl('/book'), () => HttpResponse.json({ items: [], total: 0 })))
    const table = await renderView()
    const row = within(table).getByRole('row', { name: 'A Martyrs Tale' })

    expect(within(row).getByText('Possible match')).toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Confirm' })).toBeNull()
    expect(within(row).getByRole('button', { name: 'Choose book' })).toBeInTheDocument()

    fireEvent.click(within(row).getByRole('button', { name: 'The Martian' }))
    const editor = await screen.findByRole('region', { name: 'Which book is A Martyrs Tale?' })
    expect(within(editor).getByRole('radio', { name: /The Martian/ })).toBeChecked()
    expect(within(editor).getByRole('button', { name: 'Adopt as The Martian' })).toBeEnabled()
  })

  it('shows an imported suggestion as such and says the files were added alongside (#2879)', async () => {
    const owned = { ...martian, status: 'imported' }
    const copy = item({
      id: 5, parsedTitle: 'The Martian', relPath: 'Andy Weir/The Martian (2)/The Martian.epub',
      candidates: [{ book: owned, score: 1 }], topScore: 1,
    })
    serve(listResponse([copy]))
    server.use(
      http.get(apiUrl('/book'), () => HttpResponse.json({ items: [], total: 0 })),
      http.post(apiUrl('/library/unmatched/5/adopt'), () =>
        HttpResponse.json({ ...copy, state: 'adopted', book: owned, message: 'This book already had a file of this format.' })),
    )
    const table = await renderView()
    const row = within(table).getByRole('row', { name: 'The Martian' })

    expect(within(row).getByText('Possible match')).toBeInTheDocument()
    expect(within(row).getByText('Imported')).toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Confirm' })).toBeNull()

    fireEvent.click(within(row).getByRole('button', { name: 'The Martian' }))
    const editor = await screen.findByRole('region', { name: 'Which book is The Martian?' })
    expect(within(editor).getByRole('radio', { name: /The Martian/ })).toBeChecked()
    expect(within(editor).getByText('Imported')).toBeInTheDocument()
    expect(within(editor).getByRole('note')).toHaveTextContent('This book already has its files.')

    fireEvent.click(within(editor).getByRole('button', { name: 'Adopt as The Martian' }))
    expect(await within(table).findByText(/Added alongside the file it already had\./)).toBeInTheDocument()
  })

  // #2942: files naming Katy Evans in a James Patterson folder. A Patterson
  // look alike is never the preselected one click adopt, and the row says
  // which authors disagree.
  it('shows an author conflict and does not preselect the folder author\'s book (#2942)', async () => {
    const katt = { id: 51, title: 'Katt vs. Dogg', authorId: 9, authorName: 'James Patterson', status: 'wanted', mediaType: 'audiobook', monitored: true }
    const misfiled = item({
      id: 6, format: 'audiobook', fileCount: 7, parsedTitle: 'Tycoon', parsedAuthor: 'Katy Evans', authorFolder: 'James Patterson',
      relPath: 'James Patterson/$10,000,000 Marriage Proposition',
      authorConflict: { files: 'Katy Evans', folder: 'James Patterson' },
      candidates: [{ book: katt, score: 0.8, folderAuthorOnly: true }], topScore: 0.8,
    })
    serve(listResponse([misfiled]))
    server.use(http.get(apiUrl('/book'), () => HttpResponse.json({ items: [], total: 0 })))
    const table = await renderView()
    const row = within(table).getByRole('row', { name: 'Tycoon' })

    expect(within(row).getByText('Files say Katy Evans, folder says James Patterson')).toBeInTheDocument()
    expect(within(row).queryByText('Possible match')).toBeNull()
    expect(within(row).queryByRole('button', { name: 'Confirm' })).toBeNull()

    fireEvent.click(within(row).getByRole('button', { name: 'Choose book' }))
    const editor = await screen.findByRole('region', { name: 'Which book is Tycoon?' })
    expect(within(editor).getByRole('note', { name: 'Author conflict' })).toHaveTextContent('The files say Katy Evans, but the folder says James Patterson.')
    expect(within(editor).getByRole('radio', { name: /Katt vs\. Dogg/ })).not.toBeChecked()
    expect(within(editor).getByText('Folder author only')).toBeInTheDocument()
    expect(within(editor).queryByRole('button', { name: 'Adopt as Katt vs. Dogg' })).toBeNull()
    expect(within(editor).getByRole('button', { name: 'Pick a book' })).toBeDisabled()
  })

  it('keeps a conflicting row out of its folder\'s Add author group (#2942)', async () => {
    const jp = (id: number, title: string, extra: Partial<AdoptionItem> = {}) => item({
      id, parsedTitle: title, parsedAuthor: 'James Patterson', authorFolder: 'James Patterson',
      relPath: `James Patterson/${title}`, reason: 'author_not_in_library', ...extra,
    })
    serve(listResponse([
      jp(21, 'Along Came a Spider'), jp(22, 'Kiss the Girls'),
      jp(23, 'Tycoon', { parsedAuthor: 'Katy Evans', authorConflict: { files: 'Katy Evans', folder: 'James Patterson' } }),
    ]))
    const table = await renderView()

    expect(within(table).getByRole('row', { name: 'James Patterson, 2 books' })).toBeInTheDocument()
    const row = within(table).getByRole('row', { name: 'Tycoon' })
    expect(within(row).getByText('Files say Katy Evans, folder says James Patterson')).toBeInTheDocument()
    expect(within(row).getByRole('button', { name: 'Add author' })).toBeInTheDocument()
  })

  it('asks a metadata provider only when the search is submitted', async () => {
    serve(listResponse([unsuggested]))
    server.use(http.get(apiUrl('/search/book'), () => HttpResponse.json([])))
    const table = await renderView()

    const providerCalls = () => requestedURLs().filter(u => u.includes('/search/book') || u.includes('/book/lookup'))
    fireEvent.click(within(table).getByRole('button', { name: 'Choose book' }))
    await screen.findByRole('region', { name: 'Which book is Mystery Notes?' })
    fireEvent.click(screen.getByRole('button', { name: 'Not in your library? Search metadata' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Search metadata' }), { target: { value: 'mystery notes weir' } })
    expect(providerCalls()).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() => expect(providerCalls()).toHaveLength(1))
    expect(providerCalls()[0]).toContain('/search/book?term=mystery%20notes%20weir')
  })

  it('is operable from the keyboard: arrows, Enter, Esc and i', async () => {
    serve(listResponse([suggested, unsuggested]))
    server.use(http.get(apiUrl('/book'), () => HttpResponse.json({ items: [], total: 0 })))
    let ignored = false
    server.use(http.post(apiUrl('/library/unmatched/2/ignore'), () => {
      ignored = true
      return HttpResponse.json({ ...unsuggested, state: 'ignored' })
    }))
    const table = await renderView()
    const [first, second] = within(table).getAllByRole('row').slice(1)

    expect(first).toHaveAttribute('tabindex', '0')
    expect(second).toHaveAttribute('tabindex', '-1')
    first.focus()
    fireEvent.keyDown(first, { key: 'ArrowDown' })
    expect(second).toHaveFocus()
    expect(second).toHaveAttribute('tabindex', '0')

    fireEvent.keyDown(second, { key: 'Enter' })
    const editor = await screen.findByRole('region', { name: 'Which book is Mystery Notes?' })
    expect(within(editor).getByRole('textbox', { name: 'Search your library' })).toHaveFocus()

    fireEvent.keyDown(within(editor).getByRole('textbox', { name: 'Search your library' }), { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('region', { name: /Which book is/ })).toBeNull())
    expect(second).toHaveFocus()

    fireEvent.keyDown(second, { key: 'i' })
    expect(await within(second).findByText(/Ignored\. Later scans keep it out of this list\./)).toBeInTheDocument()
    expect(ignored).toBe(true)
  })

  it('shows the never scanned state with a scan action', async () => {
    serve(listResponse([], { scan: { ran: false, running: false, filesFound: 0, truncated: false, noFilesFound: false } }))
    render(<MemoryRouter><AdoptionView /></MemoryRouter>)
    expect(await screen.findByText('Your library has not been scanned yet')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Scan library' })).toBeInTheDocument()
    expect(screen.getByText('never')).toBeInTheDocument()
  })

  it('shows the all matched state after a scan with nothing left', async () => {
    serve(listResponse([]))
    render(<MemoryRouter><AdoptionView /></MemoryRouter>)
    expect(await screen.findByText('Every book in your library is matched')).toBeInTheDocument()
    expect(screen.getByText('books need a decision')).toBeInTheDocument()
  })

  it('says when the scan was truncated and when one is running', async () => {
    serve(listResponse([unsuggested], {
      scan: { ran: true, ranAt: new Date().toISOString(), running: true, filesFound: 60000, truncated: true, noFilesFound: false },
    }))
    await renderView()
    expect(screen.getByText('Scanning your library…')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Scanning…' })).toBeDisabled()
    expect(screen.getByText(/larger than one scan lists/)).toBeInTheDocument()
  })

  it('lists ignored books with Unignore and an empty ignored state', async () => {
    serve(listResponse([]))
    render(<MemoryRouter><AdoptionView /></MemoryRouter>)
    await screen.findByText('Every book in your library is matched')

    const ignoredItem = item({ id: 3, parsedTitle: 'Old Manual', state: 'ignored' })
    server.use(http.get(apiUrl('/library/unmatched'), ({ request }) =>
      HttpResponse.json(new URL(request.url).searchParams.get('state') === 'ignored' ? listResponse([ignoredItem]) : listResponse([]))))
    fireEvent.click(screen.getByRole('radio', { name: /Ignored/ }))
    const table = await screen.findByRole('table', { name: /Ignored books in your library/ })
    expect(within(table).getByRole('button', { name: 'Unignore' })).toBeInTheDocument()
  })

  it('shows books whose author is missing as one group with a single Add author', async () => {
    const becky = (id: number, title: string) => item({
      id, parsedTitle: title, parsedAuthor: 'Becky Chambers', authorFolder: 'Becky Chambers',
      relPath: `Becky Chambers/${title}.epub`, reason: 'author_not_in_library', fileCount: 7,
    })
    serve(listResponse([becky(11, 'Record of a Spaceborn Few'), unsuggested, becky(12, 'A Closed and Common Orbit')], {
      facets: { reasons: [], formats: [], folders: [{ folder: 'Becky Chambers', units: 2, files: 14, notInLibrary: 2, author: 'Becky Chambers' }] },
    }))
    const table = await renderView()

    const group = within(table).getByRole('row', { name: 'Becky Chambers, 2 books' })
    expect(within(group).getByText('2 books, 14 files')).toBeInTheDocument()
    expect(within(table).getAllByRole('button', { name: 'Add author' })).toHaveLength(1)
    expect(within(table).queryByRole('row', { name: 'Record of a Spaceborn Few' })).toBeNull()

    // The rail is navigation only: one item per folder, no actions.
    const rail = screen.getByRole('navigation', { name: 'Folders with the most books to decide' })
    expect(within(rail).queryByRole('button', { name: 'Add author' })).toBeNull()
    expect(within(rail).getByRole('button', { name: /Becky Chambers/ })).toHaveAttribute('title', 'Becky Chambers is not in your library')

    fireEvent.click(within(group).getByRole('button', { name: /14 files/ }))
    const inner = await within(table).findByRole('row', { name: 'Record of a Spaceborn Few' })
    expect(within(inner).getByRole('button', { name: 'Choose book' })).toBeInTheDocument()
    expect(within(inner).getByRole('button', { name: 'More actions for Record of a Spaceborn Few' })).toBeInTheDocument()
    expect(within(inner).queryByRole('button', { name: 'Add author' })).toBeNull()
  })
})
