import { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, cleanup, render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import AuthorDetailPage from './AuthorDetailPage'
import { api, ApiError } from '../api/client'
import type { Author, Book } from '../api/client'
import '../i18n'
import { acceptConfirm, cancelConfirm, confirmDialog } from '../test-utils'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      getAuthor: vi.fn(),
      listAllBooks: vi.fn(),
      listAuthors: vi.fn(),
      refreshAuthor: vi.fn(),
      searchAuthorLinkCandidates: vi.fn(),
      relinkAuthorUpstream: vi.fn(),
      updateAuthor: vi.fn(),
      uploadAuthorPhoto: vi.fn(),
      clearAuthorPhoto: vi.fn(),
      deleteAuthor: vi.fn(),
      deleteAuthorAlias: vi.fn(),
      searchAuthorWanted: vi.fn(),
      bulkActionBooks: vi.fn(),
      listAuthorSeries: vi.fn(),
    },
  }
})

const author: Author = {
  id: 42,
  foreignAuthorId: 'OL123A',
  authorName: 'Brandon Sanderson',
  sortName: 'Sanderson, Brandon',
  description: '',
  imageUrl: '',
  disambiguation: '',
  ratingsCount: 0,
  averageRating: 0,
  monitored: true,
  metadataProvider: 'openlibrary',
}

function makeBook(overrides: Partial<Book> & Pick<Book, 'id' | 'title' | 'status'>): Book {
  const { id, title, status, ...rest } = overrides
  return {
    id,
    foreignBookId: `book-${id}`,
    authorId: 42,
    title,
    description: '',
    imageUrl: '',
    releaseDate: undefined,
    genres: [],
    monitored: true,
    status,
    filePath: '',
    mediaType: 'ebook',
    ebookFilePath: '',
    audiobookFilePath: '',
    excluded: false,
    ...rest,
  }
}

function installLocalStorageMock() {
  const values = new Map<string, string>()
  const storage = {
    get length() {
      return values.size
    },
    clear: vi.fn(() => values.clear()),
    getItem: vi.fn((key: string) => values.get(key) ?? null),
    key: vi.fn((index: number) => Array.from(values.keys())[index] ?? null),
    removeItem: vi.fn((key: string) => {
      values.delete(key)
    }),
    setItem: vi.fn((key: string, value: string) => {
      values.set(key, value)
    }),
  } as Storage
  Object.defineProperty(globalThis, 'localStorage', { value: storage, configurable: true })
  Object.defineProperty(window, 'localStorage', { value: storage, configurable: true })
}

function LocationProbe({ onLocation }: { onLocation?: (location: string) => void }) {
  const location = useLocation()
  useEffect(() => {
    onLocation?.(`${location.pathname}${location.search}${location.hash}`)
  }, [location, onLocation])
  return null
}

type NavEntry = string | { pathname: string; state?: unknown }

// Unlike LocationProbe above, this also exposes router `state` — needed to
// verify the {ids, index} payload a book link/row carries, not just where it
// points.
function StateProbe({ onState }: { onState: (state: unknown) => void }) {
  const location = useLocation()
  useEffect(() => {
    onState(location.state)
  }, [location, onState])
  return null
}

function renderAuthorDetailPage(
  books: Book[],
  view: 'grid' | 'table' = 'grid',
  authorOverride: Partial<Author> = {},
  initialPath: NavEntry | NavEntry[] = '/author/42',
  onLocation?: (location: string) => void,
) {
  localStorage.setItem('bindery.view.author-detail', view)
  vi.mocked(api.getAuthor).mockResolvedValue({ ...author, ...authorOverride })
  vi.mocked(api.listAllBooks).mockResolvedValue(books)

  // A multi-entry array simulates real browser history (e.g. arriving from
  // the Books list) so Back's navigate(-1) fallback has somewhere to land.
  const initialEntries = Array.isArray(initialPath) ? initialPath : [initialPath]

  return render(
    <MemoryRouter initialEntries={initialEntries} initialIndex={initialEntries.length - 1}>
      <LocationProbe onLocation={onLocation} />
      <Routes>
        <Route path="/author/:id" element={<AuthorDetailPage />} />
      </Routes>
    </MemoryRouter>,
  )
}

function rowForTitle(title: string): HTMLElement {
  const row = screen.getByText(title).closest('tr')
  if (!row) throw new Error(`No row found for ${title}`)
  return row
}

function mockElementOverflow(overflows: boolean): () => void {
  const scrollHeight = vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(overflows ? 120 : 60)
  const clientHeight = vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(60)
  return () => {
    scrollHeight.mockRestore()
    clientHeight.mockRestore()
  }
}

// The book filters are the shared FilterBar: Status is an inline segment, the
// other facets are radio groups behind the Filters and Sort disclosures. These
// pick a value by its data-value, opening the disclosure first when needed.
function setFilter(group: string, value: string) {
  const segment = screen.queryByRole('group', { name: group })
  if (segment) {
    fireEvent.click(segment.querySelector(`[data-value="${value}"]`)!)
    return
  }
  if (!screen.queryByRole('radiogroup', { name: group })) {
    fireEvent.click(screen.getByRole('button', { name: group === 'Sort' ? 'Sort' : /^Filters/ }))
  }
  fireEvent.click(screen.getByRole('radiogroup', { name: group }).querySelector(`[data-value="${value}"]`)!)
}

function filterValue(group: string): string | null {
  const segment = screen.queryByRole('group', { name: group })
  if (segment) return segment.querySelector('[aria-pressed="true"]')?.getAttribute('data-value') ?? null
  if (!screen.queryByRole('radiogroup', { name: group })) {
    fireEvent.click(screen.getByRole('button', { name: group === 'Sort' ? 'Sort' : /^Filters/ }))
  }
  return screen.getByRole('radiogroup', { name: group }).querySelector('[aria-checked="true"]')?.getAttribute('data-value') ?? null
}

describe('AuthorDetailPage', () => {
  it('loads the complete author catalogue via the paging fetch-all, and renders past 100 books (#1467)', async () => {
    const books = Array.from({ length: 150 }, (_, i) =>
      makeBook({ id: i + 1, title: `Book ${i + 1}`, status: 'imported' }))
    renderAuthorDetailPage(books, 'table')

    await screen.findByText('Book 1')

    // The fetch must go through listAllBooks (which pages until `total` is
    // reached) scoped to this author — a bare listBooks call silently stopped
    // at the server's default page of 100.
    expect(api.listAllBooks).toHaveBeenCalledWith({ authorId: 42, includeExcluded: false })
    expect(screen.getByText('Book 150')).toBeInTheDocument()
    // The run-on stats sentence is now a four-cell strip; the total lives in
    // the "Books" cell rather than in prose.
    expect(screen.getByTestId('author-stats')).toHaveTextContent('Books150')
  })

  // "Show excluded" folded into the status select — it is a status like any
  // other, and as a loose checkbox it read as belonging to whichever chip group
  // it happened to wrap beside.
  it('passes includeExcluded through to the fetch-all when Excluded status is picked', async () => {
    renderAuthorDetailPage([makeBook({ id: 1, title: 'Visible Book', status: 'imported' })], 'table')

    await screen.findByText('Visible Book')
    setFilter('Status', 'excluded')

    await waitFor(() =>
      expect(api.listAllBooks).toHaveBeenCalledWith({ authorId: 42, includeExcluded: true }))
  })

  beforeEach(() => {
    vi.clearAllMocks()
    installLocalStorageMock()
    vi.mocked(api.searchAuthorWanted).mockResolvedValue({
      results: { '42': { ok: true } },
    })
    vi.mocked(api.searchAuthorLinkCandidates).mockResolvedValue([])
    vi.mocked(api.relinkAuthorUpstream).mockResolvedValue(author)
    vi.mocked(api.listAuthorSeries).mockResolvedValue([])
  })

  it('renders author description markdown without citation noise', async () => {
    renderAuthorDetailPage([], 'grid', {
      description: [
        'Known for the *Mistborn* series and [*Mistborn: The Final Empire*][3].',
        '',
        'A **prolific** fantasy author.',
        '',
        '(Sources: [1][1],[2][2])',
        '',
        '[1]: https://example.com/source',
        '[3]: https://example.com/mistborn',
      ].join('\n'),
    })

    expect(await screen.findByText('Mistborn', { selector: 'em' })).toBeInTheDocument()
    expect(screen.getByText('Mistborn: The Final Empire', { selector: 'em' })).toBeInTheDocument()
    expect(screen.getByText('prolific', { selector: 'strong' })).toBeInTheDocument()
    expect(screen.queryByText(/\*Mistborn\*/)).not.toBeInTheDocument()
    expect(screen.queryByText(/\[3\]/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Sources:/)).not.toBeInTheDocument()
    expect(screen.queryByText(/https:\/\/example\.com/)).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Mistborn: The Final Empire/ })).not.toBeInTheDocument()
  })

  it('renders inline [text](url) links as safe anchors', async () => {
    renderAuthorDetailPage([], 'grid', {
      description: 'See [Wikipedia (EN)](https://en.wikipedia.org/wiki/Brandon_Sanderson) for more. Also [bad](javascript:alert(1)) here.',
    })

    const link = await screen.findByRole('link', { name: 'Wikipedia (EN)' })
    expect(link).toHaveAttribute('href', 'https://en.wikipedia.org/wiki/Brandon_Sanderson')
    expect(link).toHaveAttribute('rel', expect.stringContaining('noopener'))
    expect(link).toHaveAttribute('target', '_blank')
    // The raw markdown syntax must not leak through.
    expect(screen.queryByText(/\]\(https/)).not.toBeInTheDocument()
    // A non-http(s) URL must NOT become a link (no javascript: anchor).
    expect(screen.queryByRole('link', { name: 'bad' })).not.toBeInTheDocument()
    expect(screen.getByText('bad')).toBeInTheDocument()
  })

  it('toggles long author descriptions', async () => {
    const restoreOverflow = mockElementOverflow(true)
    try {
      renderAuthorDetailPage([], 'grid', {
        description: 'Line one.\n\nLine two.\n\nLine three.\n\nLine four.\n\nLine five.\n\nLine six.\n\nLine seven.',
      })

      const showMore = await screen.findByRole('button', { name: 'Show more' })
      expect(showMore).toHaveAttribute('aria-expanded', 'false')

      fireEvent.click(showMore)

      const showLess = screen.getByRole('button', { name: 'Show less' })
      expect(showLess).toHaveAttribute('aria-expanded', 'true')
    } finally {
      restoreOverflow()
    }
  })

  it('does not show a toggle for short author descriptions', async () => {
    const restoreOverflow = mockElementOverflow(false)
    try {
      renderAuthorDetailPage([], 'grid', {
        description: 'A short description.',
      })

      await screen.findByText('A short description.')
      expect(screen.queryByRole('button', { name: 'Show more' })).not.toBeInTheDocument()
    } finally {
      restoreOverflow()
    }
  })

  it('searches all wanted books for the current author', async () => {
    renderAuthorDetailPage([
      makeBook({ id: 10, title: 'Wanted Book', status: 'wanted' }),
      makeBook({ id: 11, title: 'Imported Book', status: 'imported' }),
    ])

    const button = await screen.findByRole('button', { name: 'Search 1 wanted' })
    expect(button).toBeEnabled()

    fireEvent.click(button)

    await waitFor(() => expect(api.searchAuthorWanted).toHaveBeenCalledWith(42))
  })

  // #2669: with automatic grabbing off the server refuses the search. Before
  // the fix it answered ok:true, so the button flashed for a moment and the
  // page said nothing at all.
  it('says no search was run when automatic grabbing is off', async () => {
    vi.mocked(api.searchAuthorWanted).mockResolvedValue({
      results: { '42': { ok: false, code: 'auto_grab_disabled', error: 'automatic grabbing is disabled' } },
    })

    renderAuthorDetailPage([makeBook({ id: 10, title: 'Wanted Book', status: 'wanted' })])

    fireEvent.click(await screen.findByRole('button', { name: 'Search 1 wanted' }))

    expect(await screen.findByText(/No search was run/i)).toBeInTheDocument()
    // The message has to name the way out, not just report a failure.
    expect(screen.getByText(/Enable automatic grabbing/i)).toBeInTheDocument()
  })

  it('disables author search when there are no monitored wanted books', async () => {
    renderAuthorDetailPage([
      makeBook({ id: 10, title: 'Unmonitored Wanted Book', status: 'wanted', monitored: false }),
      makeBook({ id: 11, title: 'Imported Book', status: 'imported' }),
    ])

    const button = await screen.findByRole('button', { name: 'Search 0 wanted' })
    expect(button).toBeDisabled()

    fireEvent.click(button)

    expect(api.searchAuthorWanted).not.toHaveBeenCalled()
  })

  // #3144: BnF's Licence Ouverte asks for the source and the date.
  it('credits the BnF with the date of the last metadata refresh for a BnF author', async () => {
    renderAuthorDetailPage([], 'grid', {
      foreignAuthorId: 'bnf:author:11885918',
      authorName: 'Lewis Carroll',
      sortName: 'Carroll, Lewis',
      metadataProvider: 'bnf',
      createdAt: '2026-10-01T08:00:00Z',
      lastMetadataRefreshAt: '2026-10-10T07:00:00Z',
    })

    const credit = await screen.findByTestId('bnf-source-credit')
    expect(credit.textContent).toContain('BnF')
    expect(credit.textContent).toContain('2026-10-10')
  })

  it('shows no BnF credit for an author from another provider', async () => {
    renderAuthorDetailPage([], 'grid', { createdAt: '2026-10-01T08:00:00Z' })
    await screen.findByRole('heading', { level: 2 })
    expect(screen.queryByTestId('bnf-source-credit')).toBeNull()
  })

  // #3287: a photo or bio an author enricher filled in carries its credit.
  it('credits an enriched photo and bio with their source, creator and licence', async () => {
    renderAuthorDetailPage([], 'grid', {
      foreignAuthorId: 'nb:author:90000001',
      authorName: 'Kari Nordmann',
      metadataProvider: 'nb',
      imageUrl: 'https://upload.example/k.jpg',
      imageSource: { provider: 'wikimedia-commons', url: 'https://commons.example/wiki/File:K.jpg', credit: 'Ola Nordmann', license: 'CC BY-SA 4.0' },
      description: 'Kari Nordmann is a writer.',
      descriptionSource: { provider: 'wikipedia', url: 'https://no.wikipedia.example/wiki/Kari_Nordmann', license: 'CC BY-SA 4.0' },
    })

    const photo = await screen.findByTestId('author-photo-credit')
    expect(photo.textContent).toBe('Photo: Ola Nordmann, CC BY-SA 4.0, via Wikimedia Commons')
    expect(photo).toHaveAttribute('href', 'https://commons.example/wiki/File:K.jpg')
    const bio = screen.getByTestId('author-bio-credit')
    expect(bio.textContent).toBe('Bio from Wikipedia, CC BY-SA 4.0')
    expect(bio).toHaveAttribute('href', 'https://no.wikipedia.example/wiki/Kari_Nordmann')
  })

  // #3287: the photo upload mirrors the book cover upload (#3017).
  it('uploads a photo picked from the author page and shows no credit for it', async () => {
    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], 'photo.png', { type: 'image/png' })
    vi.mocked(api.uploadAuthorPhoto).mockResolvedValue({ ...author, imageUrl: '/api/v1/images?url=bindery-cover%3Aabc', imageSource: { provider: 'user', url: '' } })
    renderAuthorDetailPage([], 'grid', { imageUrl: '' })
    const input = await screen.findByLabelText('Upload photo')
    expect(input.closest('label')).toHaveClass('touch-target')
    fireEvent.change(input, { target: { files: [file] } })
    await waitFor(() => expect(api.uploadAuthorPhoto).toHaveBeenCalledWith(42, file))
    expect(await screen.findByLabelText('Change photo')).toBeInTheDocument()
    expect(screen.getByRole('img', { name: author.authorName })).toHaveAttribute('src', '/api/v1/images?url=bindery-cover%3Aabc')
    // A photo the user chose is theirs: there is nobody to credit.
    expect(screen.queryByTestId('author-photo-credit')).toBeNull()
  })

  it('reports a failed photo upload', async () => {
    vi.mocked(api.uploadAuthorPhoto).mockRejectedValue(new Error('send a JPEG, PNG, WebP or GIF image'))
    renderAuthorDetailPage([], 'grid', { imageUrl: '' })
    const input = await screen.findByLabelText('Upload photo')
    fireEvent.change(input, { target: { files: [new File(['x'], 'notes.txt', { type: 'text/plain' })] } })
    expect(await screen.findByText('send a JPEG, PNG, WebP or GIF image')).toBeInTheDocument()
  })

  it('falls back to a plain message when the upload fails without an Error', async () => {
    vi.mocked(api.uploadAuthorPhoto).mockRejectedValue('network down')
    renderAuthorDetailPage([], 'grid', { imageUrl: '' })
    const input = await screen.findByLabelText('Upload photo')
    fireEvent.change(input, { target: { files: [new File(['x'], 'photo.png', { type: 'image/png' })] } })
    expect(await screen.findByText('Could not upload the photo')).toBeInTheDocument()
  })

  // Removing an uploaded photo hands the field back: the answer carries the
  // provider's photo, credited if an enricher found it.
  it('removes an uploaded photo and shows the one that replaces it', async () => {
    vi.mocked(api.clearAuthorPhoto).mockResolvedValue({ ...author, imageUrl: 'https://upload.example/k.jpg', imageSource: { provider: 'wikimedia-commons', url: 'https://commons.example/wiki/File:K.jpg', license: 'CC BY 2.5' } })
    renderAuthorDetailPage([], 'grid', { imageUrl: '/api/v1/images?url=bindery-cover%3Aabc', imageSource: { provider: 'user', url: '' } })
    fireEvent.click(await screen.findByRole('button', { name: 'Remove photo' }))
    await waitFor(() => expect(api.clearAuthorPhoto).toHaveBeenCalledWith(42))
    expect(await screen.findByTestId('author-photo-credit')).toHaveTextContent('Photo: CC BY 2.5, via Wikimedia Commons')
    expect(screen.queryByRole('button', { name: 'Remove photo' })).toBeNull()
  })

  // A bio the user wrote is theirs: nobody to credit (#3287).
  it('shows no bio credit for a bio the user wrote', async () => {
    renderAuthorDetailPage([], 'grid', { description: 'Mine.', descriptionSource: { provider: 'user', url: '' } })
    await screen.findByRole('heading', { level: 2 })
    expect(screen.queryByTestId('author-bio-credit')).toBeNull()
  })

  it('accepts exactly the image types the server stores', async () => {
    renderAuthorDetailPage([], 'grid', { imageUrl: '' })
    expect(await screen.findByLabelText('Upload photo')).toHaveAttribute('accept', 'image/jpeg,image/png,image/webp,image/gif')
  })

  // While one photo request runs, neither control can start another.
  it('disables both photo controls while uploading or removing', async () => {
    const user = { imageUrl: '/api/v1/images?url=bindery-cover%3Aabc', imageSource: { provider: 'user', url: '' } }
    vi.mocked(api.uploadAuthorPhoto).mockReturnValue(new Promise(() => {}))
    renderAuthorDetailPage([], 'grid', user)
    fireEvent.change(await screen.findByLabelText('Change photo'), { target: { files: [new File(['x'], 'p.png', { type: 'image/png' })] } })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Remove photo' })).toBeDisabled())
    expect(document.querySelector('input[type=file]')).toBeDisabled()
    cleanup()

    vi.mocked(api.clearAuthorPhoto).mockReturnValue(new Promise(() => {}))
    renderAuthorDetailPage([], 'grid', user)
    fireEvent.click(await screen.findByRole('button', { name: 'Remove photo' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Removing…' })).toBeDisabled())
    expect(document.querySelector('input[type=file]')).toBeDisabled()
  })

  it('asks to reload when the author changed while removing the photo', async () => {
    vi.mocked(api.clearAuthorPhoto).mockRejectedValue(new ApiError(409, { error: 'this author changed while you were saving; reload and try again' }, 'Conflict'))
    renderAuthorDetailPage([], 'grid', { imageUrl: '/api/v1/images?url=bindery-cover%3Aabc', imageSource: { provider: 'user', url: '' } })
    fireEvent.click(await screen.findByRole('button', { name: 'Remove photo' }))
    expect(await screen.findByText('This author changed while you were saving. Reload the page and try again.')).toBeInTheDocument()
  })

  it('says why when the photo to remove was not one the user uploaded', async () => {
    vi.mocked(api.clearAuthorPhoto).mockRejectedValue(new ApiError(409, { error: 'only a photo you uploaded can be removed', code: 'photo_not_uploaded' }, 'Conflict'))
    renderAuthorDetailPage([], 'grid', { imageUrl: '/api/v1/images?url=bindery-cover%3Aabc', imageSource: { provider: 'user', url: '' } })
    fireEvent.click(await screen.findByRole('button', { name: 'Remove photo' }))
    expect(await screen.findByText('Only a photo you uploaded can be removed. Reload the page to see the current photo.')).toBeInTheDocument()
  })

  it('offers no Remove photo for a photo the user did not upload', async () => {
    renderAuthorDetailPage([], 'grid', { imageUrl: 'https://covers.example/a.jpg' })
    await screen.findByRole('heading', { level: 2 })
    expect(screen.queryByRole('button', { name: 'Remove photo' })).toBeNull()
  })

  it('reports a failed photo removal', async () => {
    vi.mocked(api.clearAuthorPhoto).mockRejectedValue('network down')
    renderAuthorDetailPage([], 'grid', { imageUrl: '/api/v1/images?url=bindery-cover%3Aabc', imageSource: { provider: 'user', url: '' } })
    fireEvent.click(await screen.findByRole('button', { name: 'Remove photo' }))
    expect(await screen.findByText('Could not remove the photo')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Remove photo' })).toBeEnabled()
  })

  it('sends nothing when the file picker is cancelled', async () => {
    renderAuthorDetailPage([], 'grid', { imageUrl: '' })
    const input = await screen.findByLabelText('Upload photo')
    fireEvent.change(input, { target: { files: [] } })
    expect(api.uploadAuthorPhoto).not.toHaveBeenCalled()
  })

  // A Commons file that names no photographer is still credited, with its
  // licence; a source the page has no label for is shown by its own name.
  it('credits a photo with no named photographer, and an unknown source by name', async () => {
    renderAuthorDetailPage([], 'grid', {
      imageUrl: 'https://upload.example/k.jpg',
      imageSource: { provider: 'wikimedia-commons', url: 'https://commons.example/wiki/File:K.jpg', license: 'CC BY 2.5' },
      description: 'Kari Nordmann is a writer.',
      descriptionSource: { provider: 'example-encyclopedia', url: 'https://encyclopedia.example/k' },
    })
    expect((await screen.findByTestId('author-photo-credit')).textContent).toBe('Photo: CC BY 2.5, via Wikimedia Commons')
    // No licence stated: the credit still names the source, without a
    // dangling separator.
    expect(screen.getByTestId('author-bio-credit').textContent).toBe('Bio from example-encyclopedia')
  })

  // A credit URL comes from a third party: anything but http(s) keeps the
  // credit text and drops the link.
  it('does not link a credit whose URL is not http(s)', async () => {
    renderAuthorDetailPage([], 'grid', {
      imageUrl: 'https://upload.example/k.jpg',
      imageSource: { provider: 'wikimedia-commons', url: 'javascript:alert(1)', license: 'CC BY 2.5' },
      description: 'Kari Nordmann is a writer.',
      descriptionSource: { provider: 'wikipedia', url: 'data:text/html,hi', license: 'CC BY-SA 4.0' },
    })
    const photo = await screen.findByTestId('author-photo-credit')
    expect(photo.textContent).toBe('Photo: CC BY 2.5, via Wikimedia Commons')
    expect(photo).not.toHaveAttribute('href')
    expect(screen.getByTestId('author-bio-credit')).not.toHaveAttribute('href')
  })

  it('shows no enrichment credit for a photo and bio from the author\'s own provider', async () => {
    renderAuthorDetailPage([], 'grid', { imageUrl: 'https://covers.example/a.jpg', description: 'From the provider.' })
    await screen.findByRole('heading', { level: 2 })
    expect(screen.queryByTestId('author-photo-credit')).toBeNull()
    expect(screen.queryByTestId('author-bio-credit')).toBeNull()
  })

  it('shows link metadata for unlinked authors', async () => {
    renderAuthorDetailPage([], 'grid', {
      foreignAuthorId: 'abs:author:library:emilia-jae',
      authorName: 'Emilia Jae',
      sortName: 'Jae, Emilia',
      metadataProvider: 'audiobookshelf',
    })

    fireEvent.click(await screen.findByRole('button', { name: /More/ }))
    expect(await screen.findByRole('menuitem', { name: 'Link metadata' })).toBeInTheDocument()
  })

  it('shows link metadata for calibre-provider authors with legacy IDs', async () => {
    renderAuthorDetailPage([], 'grid', {
      foreignAuthorId: 'legacy-calibre-author',
      authorName: 'Calibre Author',
      sortName: 'Author, Calibre',
      metadataProvider: 'calibre',
      description: 'Imported from Calibre.',
      imageUrl: 'https://example.com/calibre.jpg',
      ratingsCount: 12,
      averageRating: 4.1,
    })

    fireEvent.click(await screen.findByRole('button', { name: /More/ }))
    expect(await screen.findByRole('menuitem', { name: 'Link metadata' })).toBeInTheDocument()
  })

  it('shows find-better metadata for linked sparse authors and relinks a selected candidate', async () => {
    const sparseAuthor = {
      foreignAuthorId: 'OL13200512A',
      authorName: 'Emilia Jae',
      sortName: 'Jae, Emilia',
      metadataProvider: 'openlibrary',
      description: '',
      imageUrl: '',
      disambiguation: '',
      ratingsCount: 0,
      averageRating: 0,
    }
    const hardcoverAuthor = {
      ...author,
      ...sparseAuthor,
      foreignAuthorId: 'hc:emilia-jae',
      metadataProvider: 'hardcover',
      description: 'Fantasy author.',
      statistics: { bookCount: 3, availableBookCount: 0, wantedBookCount: 0 },
    }
    vi.mocked(api.searchAuthorLinkCandidates).mockResolvedValue([hardcoverAuthor])
    vi.mocked(api.relinkAuthorUpstream).mockResolvedValue(hardcoverAuthor)
    vi.mocked(api.getAuthor).mockResolvedValueOnce({ ...author, ...sparseAuthor }).mockResolvedValue(hardcoverAuthor)

    renderAuthorDetailPage([], 'grid', sparseAuthor)

    fireEvent.click(await screen.findByRole('button', { name: /More/ }))
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Find better metadata' }))

    await waitFor(() => expect(api.searchAuthorLinkCandidates).toHaveBeenCalledWith(42, 'Emilia Jae'))
    expect(await screen.findByText('Hardcover')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Link' }))

    await waitFor(() => expect(api.relinkAuthorUpstream).toHaveBeenCalledWith(42, {
      foreignAuthorId: 'hc:emilia-jae',
      authorName: 'Emilia Jae',
    }))
  })

  it('shows find-better metadata for linked authors with a full record, not just sparse ones', async () => {
    renderAuthorDetailPage([], 'grid', {
      foreignAuthorId: 'OL13200512A',
      authorName: 'Emilia Jae',
      sortName: 'Jae, Emilia',
      metadataProvider: 'openlibrary',
      description: 'Fantasy author of the Shades of Magic series.',
      imageUrl: 'https://example.com/emilia.jpg',
      disambiguation: 'Fantasy author, not the botanist',
      ratingsCount: 128,
      averageRating: 4.4,
    })

    fireEvent.click(await screen.findByRole('button', { name: /More/ }))
    expect(await screen.findByRole('menuitem', { name: 'Find better metadata' })).toBeInTheDocument()
  })

  it('opens link metadata from the query string once and removes the trigger param', async () => {
    const locations: string[] = []
    renderAuthorDetailPage([], 'grid', {
      foreignAuthorId: 'abs:author:library:emilia-jae',
      authorName: 'Emilia Jae',
      sortName: 'Jae, Emilia',
      metadataProvider: 'audiobookshelf',
    }, '/author/42?linkMetadata=1&view=detail', location => locations.push(location))

    expect(await screen.findByRole('heading', { name: 'Link metadata' })).toBeInTheDocument()
    await waitFor(() => expect(api.searchAuthorLinkCandidates).toHaveBeenCalledWith(42, 'Emilia Jae'))
    await waitFor(() => expect(locations[locations.length - 1]).toBe('/author/42?view=detail'))
  })

  it('does not reopen a query-opened metadata modal after a relink reloads the author', async () => {
    const sparseAuthor = {
      foreignAuthorId: 'OL13200512A',
      authorName: 'Emilia Jae',
      sortName: 'Jae, Emilia',
      metadataProvider: 'openlibrary',
      description: '',
      imageUrl: '',
      disambiguation: '',
      ratingsCount: 0,
      averageRating: 0,
    }
    const dnbAuthor = {
      ...author,
      ...sparseAuthor,
      foreignAuthorId: 'dnb:123456789',
      metadataProvider: 'dnb',
      description: 'DNB author record.',
    }
    vi.mocked(api.searchAuthorLinkCandidates).mockResolvedValue([dnbAuthor])
    vi.mocked(api.relinkAuthorUpstream).mockResolvedValue(dnbAuthor)
    vi.mocked(api.getAuthor).mockResolvedValueOnce({ ...author, ...sparseAuthor }).mockResolvedValue(dnbAuthor)

    renderAuthorDetailPage([], 'grid', sparseAuthor, '/author/42?linkMetadata=1')

    expect(await screen.findByText('DNB')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Link' }))

    await waitFor(() => expect(api.relinkAuthorUpstream).toHaveBeenCalledWith(42, {
      foreignAuthorId: 'dnb:123456789',
      authorName: 'Emilia Jae',
    }))
    await waitFor(() => expect(api.getAuthor).toHaveBeenCalledTimes(2))
    expect(screen.queryByRole('heading', { name: 'Link metadata' })).not.toBeInTheDocument()
  })

  it('removes an author alias after confirmation', async () => {
    vi.mocked(api.deleteAuthorAlias).mockResolvedValue()
    renderAuthorDetailPage([], 'grid', {
      aliases: [{ id: 7, authorId: 42, name: 'Robert Jordan', createdAt: '2026-01-01T00:00:00Z' }],
    })

    await screen.findByText('Robert Jordan')
    fireEvent.click(screen.getByRole('button', { name: 'Remove alias Robert Jordan' }))

    // In-app modal since #2359, so the message is on screen rather than passed
    // to window.confirm.
    expect(await screen.findByText('Remove alias "Robert Jordan" from Brandon Sanderson?')).toBeInTheDocument()
    await acceptConfirm()

    await waitFor(() => {
      expect(api.deleteAuthorAlias).toHaveBeenCalledWith(42, 7)
    })
    await waitFor(() => {
      expect(screen.queryByText('Robert Jordan')).not.toBeInTheDocument()
    })
  })

  it('keeps an author alias when removal is cancelled', async () => {
    renderAuthorDetailPage([], 'grid', {
      aliases: [{ id: 8, authorId: 42, name: 'Pen Name', createdAt: '2026-01-01T00:00:00Z' }],
    })

    await screen.findByText('Pen Name')
    fireEvent.click(screen.getByRole('button', { name: 'Remove alias Pen Name' }))
    await cancelConfirm()

    await waitFor(() => expect(confirmDialog()).not.toBeInTheDocument())
    expect(api.deleteAuthorAlias).not.toHaveBeenCalled()
    expect(screen.getByText('Pen Name')).toBeInTheDocument()
  })

  it('keeps table metadata visible and repeats it in compact title rows', async () => {
    renderAuthorDetailPage(
      [
        makeBook({
          id: 101,
          title: 'Firefight',
          status: 'wanted',
          mediaType: 'ebook',
          releaseDate: '2008-01-01T00:00:00Z',
        }),
        makeBook({
          id: 102,
          title: 'Snapshot',
          status: 'skipped',
          mediaType: 'audiobook',
          releaseDate: '2023-10-10',
        }),
        makeBook({
          id: 103,
          title: 'Dual Format',
          status: 'imported',
          mediaType: 'both',
          releaseDate: '2022-05-05',
          excluded: true,
        }),
      ],
      'table',
    )

    await screen.findByText('Firefight')
    const table = screen.getByRole('table')

    expect(table).toHaveClass('table-fixed')
    expect(within(table).getByRole('columnheader', { name: 'Title' })).toHaveClass('sm:w-[46%]')
    expect(within(table).getByRole('columnheader', { name: /Published/ })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: 'Type' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: 'Status' })).toBeInTheDocument()

    // Cells: [0]=row checkbox (bulk select), [1]=title+inline metadata,
    // [2]=published year, [3]=rating, [4]=type, [5]=status. Checkbox column
    // was added for #791 bulk multi-select.
    const firefightCells = within(rowForTitle('Firefight')).getAllByRole('cell')
    expect(firefightCells).toHaveLength(6)
    expect(within(firefightCells[0]).getByRole('checkbox')).toBeInTheDocument()
    expect(firefightCells[1]).toHaveTextContent('Wanted')
    expect(firefightCells[1]).toHaveTextContent('📖 Ebook')
    expect(firefightCells[1]).toHaveTextContent('2008')
    expect(firefightCells[1]).not.toHaveTextContent('2008-01-01')
    expect(firefightCells[2]).toHaveTextContent('2008')
    expect(firefightCells[2]).not.toHaveTextContent('2008-01-01')
    expect(firefightCells[4]).toHaveTextContent('📖 Ebook')
    expect(firefightCells[5]).toHaveTextContent('Wanted')

    const snapshotCells = within(rowForTitle('Snapshot')).getAllByRole('cell')
    expect(snapshotCells[1]).toHaveTextContent('Skipped')
    expect(snapshotCells[1]).toHaveTextContent('🎧 Audiobook')
    expect(snapshotCells[1]).toHaveTextContent('2023')
    expect(snapshotCells[2]).toHaveTextContent('2023')
    expect(snapshotCells[4]).toHaveTextContent('🎧 Audiobook')
    expect(snapshotCells[5]).toHaveTextContent('Skipped')

    const dualFormatCells = within(rowForTitle('Dual Format')).getAllByRole('cell')
    expect(dualFormatCells[1]).toHaveTextContent('In library')
    expect(dualFormatCells[1]).toHaveTextContent('📖🎧 Both')
    expect(dualFormatCells[1]).toHaveTextContent('2022')
    expect(dualFormatCells[1]).toHaveTextContent('Excluded')
    expect(dualFormatCells[2]).toHaveTextContent('2022')
    expect(dualFormatCells[4]).toHaveTextContent('📖🎧 Both')
    expect(dualFormatCells[5]).toHaveTextContent('In library')
    expect(dualFormatCells[5]).toHaveTextContent('Excluded')
  })

  it('bulk-excludes selected books via /book/bulk', async () => {
    vi.mocked(api.bulkActionBooks).mockResolvedValue({
      results: { '101': { ok: true }, '102': { ok: true } },
    })
    renderAuthorDetailPage(
      [
        makeBook({ id: 101, title: 'Drop One', status: 'wanted' }),
        makeBook({ id: 102, title: 'Drop Two', status: 'wanted' }),
        makeBook({ id: 103, title: 'Keep', status: 'wanted' }),
      ],
      'table',
    )

    await screen.findByText('Drop One')
    const row1 = rowForTitle('Drop One')
    const row2 = rowForTitle('Drop Two')
    fireEvent.click(within(row1).getByRole('checkbox'))
    fireEvent.click(within(row2).getByRole('checkbox'))

    const excludeBtn = await screen.findByRole('button', { name: 'Exclude' })
    fireEvent.click(excludeBtn)
    await acceptConfirm()

    await waitFor(() => {
      expect(api.bulkActionBooks).toHaveBeenCalledWith([101, 102], 'exclude', undefined)
    })
  })

  // #3454: Exclude was offered on every selection and the Excluded view had
  // no way back. There the bar swaps it for Un-exclude, with no confirmation:
  // it undoes a hiding rather than hiding anything.
  it('offers Un-exclude instead of Exclude in the Excluded view (#3454)', async () => {
    vi.mocked(api.bulkActionBooks).mockResolvedValue({
      results: { '201': { ok: true }, '202': { ok: true } },
    })
    const kept = makeBook({ id: 200, title: 'Still Here', status: 'imported' })
    const gone1 = makeBook({ id: 201, title: 'Hidden One', status: 'wanted', excluded: true })
    const gone2 = makeBook({ id: 202, title: 'Hidden Two', status: 'wanted', excluded: true })
    renderAuthorDetailPage([kept, gone1, gone2], 'table')

    await screen.findByText('Still Here')
    fireEvent.click(within(rowForTitle('Still Here')).getByRole('checkbox'))
    expect(await screen.findByRole('button', { name: 'Exclude' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Un-exclude' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Clear' }))

    setFilter('Status', 'excluded')
    await screen.findByText('Hidden One')
    fireEvent.click(within(rowForTitle('Hidden One')).getByRole('checkbox'))
    fireEvent.click(within(rowForTitle('Hidden Two')).getByRole('checkbox'))

    expect(screen.queryByRole('button', { name: 'Exclude' })).toBeNull()
    fireEvent.click(await screen.findByRole('button', { name: 'Un-exclude' }))

    await waitFor(() => {
      expect(api.bulkActionBooks).toHaveBeenCalledWith([201, 202], 'unexclude', undefined)
    })
  })

  // #2267: searching the selected books, not every wanted book of the author.
  it('bulk-searches only the selected books', async () => {
    vi.mocked(api.bulkActionBooks).mockResolvedValue({
      results: { '101': { ok: true } },
    })
    renderAuthorDetailPage(
      [
        makeBook({ id: 101, title: 'Find Me', status: 'wanted' }),
        makeBook({ id: 102, title: 'Leave Me', status: 'wanted' }),
      ],
      'table',
    )

    await screen.findByText('Find Me')
    fireEvent.click(within(rowForTitle('Find Me')).getByRole('checkbox'))

    fireEvent.click(await screen.findByRole('button', { name: 'Search' }))

    await waitFor(() => {
      expect(api.bulkActionBooks).toHaveBeenCalledWith([101], 'search', undefined)
    })
  })

  // #2066: the author page's book list had no media-type bulk action at all,
  // so correcting several mistagged titles under one author meant editing each
  // book or leaving the page for the global Books view.
  it('bulk-sets media type from the author page book list', async () => {
    vi.mocked(api.bulkActionBooks).mockResolvedValue({
      results: { '301': { ok: true }, '302': { ok: true } },
    })
    renderAuthorDetailPage(
      [
        makeBook({ id: 301, title: 'Mistagged One', status: 'wanted' }),
        makeBook({ id: 302, title: 'Mistagged Two', status: 'wanted' }),
      ],
      'table',
    )

    await screen.findByText('Mistagged One')
    fireEvent.click(within(rowForTitle('Mistagged One')).getByRole('checkbox'))
    fireEvent.click(within(rowForTitle('Mistagged Two')).getByRole('checkbox'))

    fireEvent.click(await screen.findByRole('button', { name: '📖🎧 Set Both' }))

    await waitFor(() => {
      expect(api.bulkActionBooks).toHaveBeenCalledWith([301, 302], 'set_media_type', 'both')
    })
  })

  it('surfaces partial-failure summary when some bulk actions fail', async () => {
    vi.mocked(api.bulkActionBooks).mockResolvedValue({
      results: { '201': { ok: true }, '202': { ok: false, error: 'gone' } },
    })
    renderAuthorDetailPage(
      [
        makeBook({ id: 201, title: 'Mon One', status: 'wanted', monitored: false }),
        makeBook({ id: 202, title: 'Mon Two', status: 'wanted', monitored: false }),
      ],
      'table',
    )

    await screen.findByText('Mon One')
    fireEvent.click(within(rowForTitle('Mon One')).getByRole('checkbox'))
    fireEvent.click(within(rowForTitle('Mon Two')).getByRole('checkbox'))

    fireEvent.click(await screen.findByRole('button', { name: 'Monitor' }))

    await waitFor(() => {
      expect(screen.getByText(/Monitor: 1 of 2 succeeded\. First error: gone/)).toBeInTheDocument()
    })
  })

  it('excludes unmonitored books from the Wanted status filter (#1173)', async () => {
    renderAuthorDetailPage(
      [
        makeBook({ id: 301, title: 'Genuinely Wanted', status: 'wanted', monitored: true }),
        makeBook({ id: 302, title: 'Stale Wanted Unmonitored', status: 'wanted', monitored: false }),
        makeBook({ id: 303, title: 'Already Imported', status: 'imported', monitored: true }),
      ],
      'table',
    )

    await screen.findByText('Genuinely Wanted')

    // Apply the Wanted status filter.
    setFilter('Status', 'wanted')

    // Monitored wanted book stays; unmonitored "wanted" and imported drop out.
    expect(screen.getByText('Genuinely Wanted')).toBeInTheDocument()
    expect(screen.queryByText('Stale Wanted Unmonitored')).not.toBeInTheDocument()
    expect(screen.queryByText('Already Imported')).not.toBeInTheDocument()
  })

  it('scopes the status filter to the selected media type for dual-format books (#1406)', async () => {
    renderAuthorDetailPage(
      [
        // The #1406 repro: ebook side imported, audiobook still wanted, so the
        // aggregate status is 'wanted' even though the ebook is on disk.
        makeBook({
          id: 601, title: 'Dual Ebook Done', status: 'wanted', monitored: true,
          mediaType: 'both', ebookFilePath: '/books/dual.epub', audiobookFilePath: '',
        }),
        makeBook({ id: 602, title: 'Plain Ebook Imported', status: 'imported', mediaType: 'ebook', ebookFilePath: '/books/plain.epub' }),
        makeBook({ id: 603, title: 'Plain Ebook Wanted', status: 'wanted', mediaType: 'ebook' }),
      ],
      'table',
    )
    await screen.findByText('Dual Ebook Done')

    // Type: Ebook + Status: Imported must include the dual-format book whose
    // ebook is on disk — the aggregate 'wanted' used to hide it.
    setFilter('Type', 'ebook')
    setFilter('Status', 'imported')
    expect(screen.getByText('Dual Ebook Done')).toBeInTheDocument()
    expect(screen.getByText('Plain Ebook Imported')).toBeInTheDocument()
    expect(screen.queryByText('Plain Ebook Wanted')).not.toBeInTheDocument()

    // And its ebook is NOT wanted anymore, so the Wanted chip must drop it.
    setFilter('Status', 'wanted')
    expect(screen.queryByText('Dual Ebook Done')).not.toBeInTheDocument()
    expect(screen.getByText('Plain Ebook Wanted')).toBeInTheDocument()

    // The reverse direction: the audiobook side of the same book IS wanted.
    setFilter('Type', 'audiobook')
    expect(screen.getByText('Dual Ebook Done')).toBeInTheDocument()
    expect(screen.queryByText('Plain Ebook Wanted')).not.toBeInTheDocument()
  })

  it('lists a partially owned dual-format book under Imported and counts it in library (#3132)', async () => {
    renderAuthorDetailPage(
      [
        makeBook({
          id: 621, title: 'Partly Owned', status: 'wanted', monitored: true,
          mediaType: 'both', ebookFilePath: '/books/loop.epub', audiobookFilePath: '',
        }),
        makeBook({ id: 622, title: 'Nothing Owned', status: 'wanted', monitored: true, mediaType: 'both' }),
      ],
      'table',
    )
    await screen.findByText('Partly Owned')
    const stats = await screen.findByTestId('author-stats')
    expect(within(stats).getByText('In library').parentElement).toHaveTextContent('1')

    setFilter('Status', 'imported')
    expect(screen.getByText('Partly Owned')).toBeInTheDocument()
    expect(screen.queryByText('Nothing Owned')).not.toBeInTheDocument()

    // Still wanted too: one format is missing.
    setFilter('Status', 'wanted')
    expect(screen.getByText('Partly Owned')).toBeInTheDocument()
    expect(screen.getByText('Nothing Owned')).toBeInTheDocument()

    // With a format chip the file has to be of that format: the ebook only
    // book is not in the audiobook library, matching the Books page.
    setFilter('Status', 'imported')
    setFilter('Type', 'audiobook')
    expect(screen.queryByText('Partly Owned')).not.toBeInTheDocument()
    setFilter('Type', 'ebook')
    expect(screen.getByText('Partly Owned')).toBeInTheDocument()
  })

  it("includes 'both' books under either media-type chip (#1406)", async () => {
    renderAuthorDetailPage(
      [
        makeBook({ id: 611, title: 'Both Formats', status: 'wanted', mediaType: 'both' }),
        makeBook({ id: 612, title: 'Ebook Only', status: 'wanted', mediaType: 'ebook' }),
        makeBook({ id: 613, title: 'Audio Only', status: 'wanted', mediaType: 'audiobook' }),
      ],
      'table',
    )
    await screen.findByText('Both Formats')

    setFilter('Type', 'ebook')
    expect(screen.getByText('Both Formats')).toBeInTheDocument()
    expect(screen.getByText('Ebook Only')).toBeInTheDocument()
    expect(screen.queryByText('Audio Only')).not.toBeInTheDocument()

    setFilter('Type', 'audiobook')
    expect(screen.getByText('Both Formats')).toBeInTheDocument()
    expect(screen.queryByText('Ebook Only')).not.toBeInTheDocument()
    expect(screen.getByText('Audio Only')).toBeInTheDocument()
  })

  it('select-all toggles every displayed book and respects the active filter (#1172)', async () => {
    vi.mocked(api.bulkActionBooks).mockResolvedValue({
      results: { '401': { ok: true } },
    })
    renderAuthorDetailPage(
      [
        makeBook({ id: 401, title: 'Wanted Monitored', status: 'wanted', monitored: true }),
        makeBook({ id: 402, title: 'Imported Book', status: 'imported', monitored: true }),
      ],
      'table',
    )

    await screen.findByText('Wanted Monitored')

    // With no filter, Select all picks every book on the page.
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }))
    expect(await screen.findByText('2 selected')).toBeInTheDocument()
    // Toggle flips to Deselect all; clicking it clears the whole selection.
    fireEvent.click(screen.getByRole('button', { name: 'Deselect all' }))
    await waitFor(() => expect(screen.queryByText('2 selected')).not.toBeInTheDocument())

    // Now narrow to just the wanted book and Select all again: selection must
    // cover only the filtered (displayed) book, not the hidden imported one.
    setFilter('Status', 'wanted')
    expect(screen.queryByText('Imported Book')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Select all' }))
    expect(await screen.findByText('1 selected')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Monitor' }))
    await waitFor(() => expect(api.bulkActionBooks).toHaveBeenCalledWith([401], 'monitor', undefined))
  })

  it('groups books by series with a Standalone group when the toggle is on', async () => {
    vi.mocked(api.listAuthorSeries).mockResolvedValue([
      {
        id: 1,
        foreignSeriesId: 'OL-MB',
        title: 'Mistborn',
        description: '',
        monitored: true,
        books: [
          { seriesId: 1, bookId: 11, positionInSeries: '2' },
          { seriesId: 1, bookId: 10, positionInSeries: '1' },
        ],
      },
      {
        id: 2,
        foreignSeriesId: 'OL-SA',
        title: 'Stormlight Archive',
        description: '',
        monitored: false,
        books: [{ seriesId: 2, bookId: 12, positionInSeries: '1' }],
      },
    ])

    renderAuthorDetailPage(
      [
        makeBook({ id: 10, title: 'The Final Empire', status: 'imported' }),
        makeBook({ id: 11, title: 'The Well of Ascension', status: 'imported' }),
        makeBook({ id: 12, title: 'The Way of Kings', status: 'wanted' }),
        makeBook({ id: 13, title: 'Elantris', status: 'wanted' }),
      ],
      'table',
    )

    await screen.findByText('The Final Empire')

    // Default flat view: no series-group headings yet.
    expect(screen.queryByRole('heading', { name: /Mistborn/ })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('switch', { name: 'Group by series' }))

    // Series + Standalone group headings appear.
    expect(await screen.findByRole('heading', { name: /Mistborn/ })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: /Stormlight Archive/ })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: /Standalone/ })).toBeInTheDocument()

    await waitFor(() => expect(api.listAuthorSeries).toHaveBeenCalledWith(42))

    // Mistborn books appear in position order (#1 The Final Empire before #2).
    const tables = screen.getAllByRole('table')
    const mistbornTable = tables[0]
    const titles = within(mistbornTable)
      .getAllByRole('row')
      .map(r => r.textContent ?? '')
      .filter(txt => txt.includes('Empire') || txt.includes('Ascension'))
    expect(titles[0]).toContain('The Final Empire')
    expect(titles[1]).toContain('The Well of Ascension')

    // Elantris belongs to no series → it lands under Standalone.
    const standaloneHeading = screen.getByRole('heading', { name: /Standalone/ })
    const standaloneSection = standaloneHeading.parentElement as HTMLElement
    expect(within(standaloneSection).getByText('Elantris')).toBeInTheDocument()

    // Toggling back returns to the flat list (headings gone).
    fireEvent.click(screen.getByRole('switch', { name: 'Show flat list' }))
    await waitFor(() => expect(screen.queryByRole('heading', { name: /Mistborn/ })).not.toBeInTheDocument())
  })
})

describe('AuthorDetailPage — toolbar and stats', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    installLocalStorageMock()
    vi.mocked(api.listAuthorSeries).mockResolvedValue([])
  })

  it('offers every StatusFilter value, including the one the chips never exposed', async () => {
    renderAuthorDetailPage([makeBook({ id: 1, title: 'A', status: 'imported' })])
    const segment = await screen.findByRole('group', { name: 'Status' })
    const values = within(segment).getAllByRole('button').map(b => b.getAttribute('data-value'))
    // skipped has been in the StatusFilter type all along but the chip row only
    // ever offered wanted/imported. 'downloading' and 'downloaded' were offered
    // here too until #2374 removed them: no book could ever hold either value,
    // so both options filtered to an empty list on every library.
    expect(values).toEqual(['', 'wanted', 'imported', 'skipped', 'excluded'])
  })

  it('filters to skipped, a status the chips never offered', async () => {
    renderAuthorDetailPage([
      makeBook({ id: 1, title: 'Passed Book', status: 'skipped' }),
      makeBook({ id: 2, title: 'Shelved Book', status: 'imported' }),
    ])
    // Grid cards draw the title twice for a coverless book — once in the card
    // heading and once inside the aria-hidden CoverPlaceholder — so query the
    // heading rather than the raw text.
    await screen.findByRole('heading', { name: 'Passed Book' })
    setFilter('Status', 'skipped')
    expect(screen.getByRole('heading', { name: 'Passed Book' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Shelved Book' })).toBeNull()
  })

  it('persists the status filter across remounts', async () => {
    const { unmount } = renderAuthorDetailPage([makeBook({ id: 1, title: 'A', status: 'imported' })])
    await screen.findByRole('group', { name: 'Status' })
    setFilter('Status', 'skipped')
    unmount()

    renderAuthorDetailPage([makeBook({ id: 1, title: 'A', status: 'imported' })])
    await screen.findByRole('group', { name: 'Status' })
    expect(filterValue('Status')).toBe('skipped')
  })

  // Grid headings in document order — the order the user sees.
  const shownTitles = (titles: string[]) =>
    screen.getAllByRole('heading')
      .map(h => h.textContent ?? '')
      .filter(txt => titles.includes(txt))

  it('sorts by title, publication date and date added from the Sort select', async () => {
    renderAuthorDetailPage([
      makeBook({ id: 1, title: 'Beta', status: 'imported', releaseDate: '2010-01-01T00:00:00Z', createdAt: '2024-03-01T00:00:00Z' }),
      makeBook({ id: 2, title: 'Gamma', status: 'imported', releaseDate: '2020-01-01T00:00:00Z', createdAt: '2024-01-01T00:00:00Z' }),
      makeBook({ id: 3, title: 'Alpha', status: 'imported', releaseDate: '2015-01-01T00:00:00Z', createdAt: '2024-02-01T00:00:00Z' }),
    ])
    const all = ['Alpha', 'Beta', 'Gamma']
    await screen.findByRole('heading', { name: 'Beta' })
    expect(shownTitles(all)).toEqual(['Beta', 'Gamma', 'Alpha'])

    setFilter('Sort', 'title-az')
    expect(shownTitles(all)).toEqual(['Alpha', 'Beta', 'Gamma'])
    setFilter('Sort', 'title-za')
    expect(shownTitles(all)).toEqual(['Gamma', 'Beta', 'Alpha'])
    setFilter('Sort', 'date-new')
    expect(shownTitles(all)).toEqual(['Gamma', 'Alpha', 'Beta'])
    setFilter('Sort', 'date-old')
    expect(shownTitles(all)).toEqual(['Beta', 'Alpha', 'Gamma'])
    setFilter('Sort', 'added-new')
    expect(shownTitles(all)).toEqual(['Beta', 'Alpha', 'Gamma'])
    setFilter('Sort', 'added-old')
    expect(shownTitles(all)).toEqual(['Gamma', 'Alpha', 'Beta'])
  })

  it('sorts undated books last in both date directions', async () => {
    renderAuthorDetailPage([
      makeBook({ id: 1, title: 'Undated', status: 'imported' }),
      makeBook({ id: 2, title: 'Old', status: 'imported', releaseDate: '2001-01-01T00:00:00Z' }),
      makeBook({ id: 3, title: 'New', status: 'imported', releaseDate: '2021-01-01T00:00:00Z' }),
    ])
    const all = ['Undated', 'Old', 'New']
    await screen.findByRole('heading', { name: 'Undated' })
    setFilter('Sort', 'date-old')
    expect(shownTitles(all)).toEqual(['Old', 'New', 'Undated'])
    setFilter('Sort', 'date-new')
    expect(shownTitles(all)).toEqual(['New', 'Old', 'Undated'])
  })

  it('persists the sort and migrates the old Published-header setting', async () => {
    localStorage.setItem('bindery.sort.author-detail.date', 'desc')
    const { unmount } = renderAuthorDetailPage([makeBook({ id: 1, title: 'A', status: 'imported' })])
    await screen.findByRole('group', { name: 'Status' })
    expect(filterValue('Sort')).toBe('date-new')
    setFilter('Sort', 'title-za')
    unmount()

    renderAuthorDetailPage([makeBook({ id: 1, title: 'A', status: 'imported' })])
    await screen.findByRole('group', { name: 'Status' })
    expect(filterValue('Sort')).toBe('title-za')
  })

  it('sorts from the Title and Published column headers in table view', async () => {
    renderAuthorDetailPage([
      makeBook({ id: 1, title: 'Beta', status: 'imported', releaseDate: '2010-01-01T00:00:00Z' }),
      makeBook({ id: 2, title: 'Alpha', status: 'imported', releaseDate: '2020-01-01T00:00:00Z' }),
    ], 'table')
    await screen.findByText('Beta')
    fireEvent.click(screen.getByRole('columnheader', { name: /Title/ }))
    expect(filterValue('Sort')).toBe('title-az')
    fireEvent.click(screen.getByRole('columnheader', { name: /Title/ }))
    expect(filterValue('Sort')).toBe('title-za')
    fireEvent.click(screen.getByRole('columnheader', { name: /Published/ }))
    expect(filterValue('Sort')).toBe('date-old')
  })

  it('filters by monitored state', async () => {
    renderAuthorDetailPage([
      makeBook({ id: 1, title: 'Watched', status: 'wanted', monitored: true }),
      makeBook({ id: 2, title: 'Ignored', status: 'wanted', monitored: false }),
    ])
    await screen.findByRole('heading', { name: 'Watched' })
    setFilter('Monitored', 'unmonitored')
    expect(screen.queryByRole('heading', { name: 'Watched' })).toBeNull()
    expect(screen.getByRole('heading', { name: 'Ignored' })).toBeInTheDocument()
    setFilter('Monitored', 'monitored')
    expect(screen.getByRole('heading', { name: 'Watched' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Ignored' })).toBeNull()
  })

  it('offers a Language filter only when the books span more than one', async () => {
    const { unmount } = renderAuthorDetailPage([
      makeBook({ id: 1, title: 'English One', status: 'imported', language: 'eng' }),
      makeBook({ id: 2, title: 'English Two', status: 'imported', language: 'eng' }),
    ])
    await screen.findByRole('heading', { name: 'English One' })
    expect(screen.queryByLabelText('Language')).toBeNull()
    unmount()

    renderAuthorDetailPage([
      makeBook({ id: 1, title: 'English One', status: 'imported', language: 'eng' }),
      makeBook({ id: 2, title: 'Deutsch Eins', status: 'imported', language: 'ger' }),
    ])
    await screen.findByRole('heading', { name: 'English One' })
    setFilter('Language', 'ger')
    expect(screen.queryByRole('heading', { name: 'English One' })).toBeNull()
    expect(screen.getByRole('heading', { name: 'Deutsch Eins' })).toBeInTheDocument()
  })

  it('treats "en" and "eng" as one language in the Language filter', async () => {
    const { unmount } = renderAuthorDetailPage([
      makeBook({ id: 1, title: 'Two Letter', status: 'imported', language: 'en' }),
      makeBook({ id: 2, title: 'Three Letter', status: 'imported', language: 'eng' }),
    ])
    await screen.findByRole('heading', { name: 'Two Letter' })
    // Both spellings are English, so there is nothing to choose between.
    fireEvent.click(screen.getByRole('button', { name: /^Filters/ }))
    expect(screen.queryByRole('radiogroup', { name: 'Language' })).toBeNull()
    unmount()

    renderAuthorDetailPage([
      makeBook({ id: 1, title: 'Two Letter', status: 'imported', language: 'en' }),
      makeBook({ id: 2, title: 'Three Letter', status: 'imported', language: 'eng' }),
      makeBook({ id: 3, title: 'Deutsch', status: 'imported', language: 'de' }),
    ])
    await screen.findByRole('heading', { name: 'Two Letter' })
    fireEvent.click(screen.getByRole('button', { name: /^Filters/ }))
    expect(within(screen.getByRole('radiogroup', { name: 'Language' })).getAllByRole('radio').map(o => o.textContent?.replace('✓', ''))).toEqual(['All languages', 'English', 'German'])
    setFilter('Language', 'eng')
    expect(screen.getByRole('heading', { name: 'Two Letter' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Three Letter' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Deutsch' })).toBeNull()
  })

  it('shows a removable chip for each active filter and clears it', async () => {
    renderAuthorDetailPage([
      makeBook({ id: 1, title: 'English One', status: 'imported', language: 'eng', releaseDate: '2001-01-01T00:00:00Z' }),
      makeBook({ id: 2, title: 'Deutsch Eins', status: 'imported', language: 'ger', mediaType: 'both', releaseDate: '2002-01-01T00:00:00Z' }),
      makeBook({ id: 3, title: 'Future Book', status: 'wanted', language: 'ger', mediaType: 'both', monitored: false, releaseDate: '2999-01-01T00:00:00Z' }),
    ])
    await screen.findByRole('heading', { name: 'English One' })
    setFilter('Type', 'ebook')
    setFilter('Published', 'upcoming')
    setFilter('Monitored', 'unmonitored')
    setFilter('Language', 'ger')

    const chipLabels = () =>
      screen.getAllByText('Clear filter').map(x => x.closest('button')!.textContent!.replace('✕', '').replace('Clear filter', '').trim())
    expect(chipLabels()).toEqual(['Ebook', 'Upcoming', 'Unmonitored', 'German'])
    expect(screen.getByRole('heading', { name: 'Future Book' })).toBeInTheDocument()

    for (let left = 4; left > 0; left--) {
      fireEvent.click(screen.getAllByText('Clear filter')[0].closest('button')!)
      if (left > 1) expect(chipLabels()).toHaveLength(left - 1)
    }
    expect(screen.queryByText('Clear filter')).toBeNull()
    expect(screen.getByRole('heading', { name: 'English One' })).toBeInTheDocument()
  })

  it('selects and deselects every visible book from the table header', async () => {
    renderAuthorDetailPage(
      [
        makeBook({ id: 1, title: 'Row One', status: 'wanted' }),
        makeBook({ id: 2, title: 'Row Two', status: 'wanted' }),
      ],
      'table',
    )
    await screen.findByText('Row One')
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select all on this page' }))
    expect(screen.getByRole('checkbox', { name: 'Select Row One' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Select Row Two' })).toBeChecked()
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select all on this page' }))
    expect(screen.getByRole('checkbox', { name: 'Select Row One' })).not.toBeChecked()
  })

  it('keeps all four stat cells when a count is zero', async () => {
    // The audiobook count used to disappear at zero, changing the row's shape
    // from one author to the next.
    renderAuthorDetailPage([makeBook({ id: 1, title: 'Only Ebook', status: 'imported' })])
    const stats = await screen.findByTestId('author-stats')
    for (const label of ['Books', 'In library', 'Wanted', 'Audiobooks']) {
      expect(within(stats).getByText(label)).toBeInTheDocument()
    }
    expect(within(stats).getByText('Audiobooks').parentElement).toHaveTextContent('0')
  })

  it('puts the wanted count in the primary action label', async () => {
    renderAuthorDetailPage([
      makeBook({ id: 1, title: 'W1', status: 'wanted' }),
      makeBook({ id: 2, title: 'W2', status: 'wanted' }),
    ])
    expect(await screen.findByRole('button', { name: 'Search 2 wanted' })).toBeInTheDocument()
  })

  it('holds the long-tail actions in the More menu', async () => {
    renderAuthorDetailPage([])
    // Off the row until opened…
    expect(screen.queryByRole('button', { name: 'Rename files' })).toBeNull()
    fireEvent.click(await screen.findByRole('button', { name: /More/ }))
    for (const label of ['Rename files', 'Merge…', 'Delete']) {
      expect(screen.getByRole('menuitem', { name: label })).toBeInTheDocument()
    }
  })

  it('navigates client-side from a table row, without a full page load', async () => {
    let seen = ''
    renderAuthorDetailPage(
      [makeBook({ id: 7, title: 'Row Book', status: 'imported' })],
      'table',
      {},
      '/author/42',
      loc => { seen = loc },
    )
    await screen.findByText('Row Book')
    // The row used window.location.href while the <Link> inside it routed
    // client-side — same row, two behaviours.
    fireEvent.click(rowForTitle('Row Book'))
    await waitFor(() => expect(seen).toBe('/book/7'))
  })
})

// #1889: books dropped by the metadata-profile filters were invisible — a Debug
// log line per book, nothing on the page. An author whose catalogue was mostly
// filtered away looked exactly like an author who wrote three books.
describe('AuthorDetailPage — last sync outcome', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    installLocalStorageMock()
    vi.mocked(api.listAuthorSeries).mockResolvedValue([])
  })

  it('reports the books the last refresh skipped, and why', async () => {
    renderAuthorDetailPage([makeBook({ id: 1, title: 'Bears Discover Fire', status: 'imported' })], 'grid', {
      lastSync: {
        completedAt: '2026-08-11T12:00:00Z',
        total: 66,
        added: 1,
        matched: 0,
        skippedLanguage: 65,
        skippedJunk: 0,
        skippedMediaType: 0,
        allowedLanguages: ['eng'],
        unknownLanguageFail: true,
        skippedLanguageSample: [
          { title: 'Les Ours', language: 'fre' },
          { title: 'Untitled Work', language: '' },
        ],
      },
    })

    const notice = await screen.findByTestId('author-sync-notice')
    expect(notice).toHaveTextContent('Last refresh skipped 65 of this author’s 66 works')
    // The count leads; the reasons and the examples sit behind the info
    // alert's disclosure. Opening it here is also the check that none of it
    // was dropped when the notice stopped being an amber wall.
    fireEvent.click(within(notice).getByRole('button', { name: 'Show details' }))
    expect(notice).toHaveTextContent('65 skipped by the language filter (allowed: eng)')
    // The unknown-language half of the filter is the part that surprises
    // people, so it is named rather than folded into the count.
    expect(notice).toHaveTextContent(/reject unknown languages/)
    expect(notice).toHaveTextContent('Les Ours (fre)')
    expect(notice).toHaveTextContent('Untitled Work (no language)')
    expect(within(notice).getByRole('link', { name: 'Metadata profile settings' })).toHaveAttribute(
      'href',
      '/settings?tab=metadata',
    )
  })

  // #1815: a refresh of an author who isn't taking new books adds nothing, and
  // "nothing happened" reads as a broken refresh unless the page says why.
  it('explains the works a refresh declined to add', async () => {
    renderAuthorDetailPage([makeBook({ id: 1, title: 'The Book I Imported', status: 'imported' })], 'grid', {
      lastSync: {
        completedAt: '2026-08-11T12:00:00Z',
        total: 84,
        added: 0,
        // One work matched the imported book; the other 83 were declined.
        matched: 1,
        skippedLanguage: 0,
        skippedJunk: 0,
        skippedMediaType: 0,
        skippedNotAccepted: 83,
      },
    })

    const notice = await screen.findByTestId('author-sync-notice')
    fireEvent.click(within(notice).getByRole('button', { name: 'Show details' }))
    expect(notice).toHaveTextContent('83 not added, because this author is not taking newly discovered books')
    expect(notice).toHaveTextContent(/Books already in your library were still refreshed/)
  })

  it('stays silent when the last refresh skipped nothing', async () => {
    renderAuthorDetailPage([makeBook({ id: 1, title: 'Only Book', status: 'imported' })], 'grid', {
      lastSync: {
        completedAt: '2026-08-11T12:00:00Z',
        total: 1,
        added: 1,
        matched: 0,
        skippedLanguage: 0,
        skippedJunk: 0,
        skippedMediaType: 0,
      },
    })
    await screen.findByRole('heading', { name: 'Only Book' })
    expect(screen.queryByTestId('author-sync-notice')).toBeNull()
  })

  it('stays silent when the server reports no sync at all', async () => {
    renderAuthorDetailPage([makeBook({ id: 1, title: 'Only Book', status: 'imported' })])
    await screen.findByRole('heading', { name: 'Only Book' })
    expect(screen.queryByTestId('author-sync-notice')).toBeNull()
  })
})

describe('AuthorDetailPage — Previous/Next navigation (#2548, frontend-only)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    installLocalStorageMock()
    vi.mocked(api.listAuthorSeries).mockResolvedValue([])
  })

  it('hides the controls when there is no router state at all (opened from a Series tab, a bookmark, or after a refresh)', async () => {
    renderAuthorDetailPage([])
    await screen.findByText('Brandon Sanderson')
    expect(screen.queryByLabelText('Previous author')).toBeNull()
    expect(screen.queryByLabelText('Next author')).toBeNull()
  })

  it('renders Previous/Next from router state and follows Next to the right id, carrying the chain state', async () => {
    let lastLocation = ''
    renderAuthorDetailPage(
      [], 'grid', {},
      { pathname: '/author/42', state: { ids: [40, 42, 43, 44, 45], index: 1 } },
      loc => { lastLocation = loc },
    )
    await screen.findByText('Brandon Sanderson')

    fireEvent.click(screen.getByLabelText('Next author'))

    await waitFor(() => expect(lastLocation).toBe('/author/43'))
  })

  it('hides Previous at the first position and shows only Next', async () => {
    renderAuthorDetailPage([], 'grid', {}, { pathname: '/author/42', state: { ids: [42, 43], index: 0 } })
    await screen.findByText('Brandon Sanderson')

    expect(screen.queryByLabelText('Previous author')).toBeNull()
    expect(screen.getByLabelText('Next author')).toBeInTheDocument()
  })

  it('hides the controls for a single-author list (both ends null)', async () => {
    renderAuthorDetailPage([], 'grid', {}, { pathname: '/author/42', state: { ids: [42], index: 0 } })
    await screen.findByText('Brandon Sanderson')
    expect(screen.queryByLabelText('Previous author')).toBeNull()
    expect(screen.queryByLabelText('Next author')).toBeNull()
  })

  it('ignores state that does not match this author (stale browser back/forward state)', async () => {
    // ids[index] is 99, not 42 — a mismatch that must be treated as no
    // navigation info rather than pointing at the wrong neighbour.
    renderAuthorDetailPage([], 'grid', {}, { pathname: '/author/42', state: { ids: [98, 99, 100], index: 1 } })
    await screen.findByText('Brandon Sanderson')
    expect(screen.queryByLabelText('Previous author')).toBeNull()
    expect(screen.queryByLabelText('Next author')).toBeNull()
  })

  it('returns to the Authors list on Back when arrived via a Previous/Next chain', async () => {
    let lastLocation = ''
    renderAuthorDetailPage(
      [], 'grid', {}, { pathname: '/author/42', state: { ids: [40, 42, 43], index: 1 } },
      loc => { lastLocation = loc },
    )
    await screen.findByText('Brandon Sanderson')

    fireEvent.click(screen.getByText('← Back'))

    await waitFor(() => expect(lastLocation).toBe('/'))
  })

  it('falls back to browser history on Back when there is no nav state, so Wanted/Books/a book page are not stranded on the Authors list', async () => {
    let lastLocation = ''
    renderAuthorDetailPage([], 'grid', {}, ['/books/9', '/author/42'], loc => { lastLocation = loc })
    await screen.findByText('Brandon Sanderson')

    fireEvent.click(screen.getByText('← Back'))

    await waitFor(() => expect(lastLocation).toBe('/books/9'))
  })

  it("refetches the new author's series after Next instead of reusing the previous author's (state leak)", async () => {
    localStorage.setItem('bindery.view.author-detail', 'table')
    vi.mocked(api.getAuthor).mockImplementation((id: number) =>
      Promise.resolve({ ...author, id, authorName: id === 42 ? 'Brandon Sanderson' : 'Patrick Rothfuss' }))
    vi.mocked(api.listAllBooks).mockImplementation(({ authorId }: { authorId?: number } = {}) =>
      Promise.resolve(authorId === 42
        ? [makeBook({ id: 10, title: 'The Final Empire', status: 'imported' })]
        : [makeBook({ id: 20, title: 'The Name of the Wind', status: 'imported' })]))
    vi.mocked(api.listAuthorSeries).mockImplementation((authorId: number) =>
      Promise.resolve(authorId === 42
        ? [{
            id: 1, foreignSeriesId: 'OL-MB', title: 'Mistborn', description: '', monitored: true,
            books: [{ seriesId: 1, bookId: 10, positionInSeries: '1' }],
          }]
        : [{
            id: 2, foreignSeriesId: 'OL-KING', title: 'Kingkiller Chronicle', description: '', monitored: true,
            books: [{ seriesId: 2, bookId: 20, positionInSeries: '1' }],
          }]))

    render(
      <MemoryRouter initialEntries={[{ pathname: '/author/42', state: { ids: [42, 43], index: 0 } }]}>
        <Routes>
          <Route path="/author/:id" element={<AuthorDetailPage />} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByText('Brandon Sanderson')
    fireEvent.click(screen.getByRole('switch', { name: 'Group by series' }))
    expect(await screen.findByRole('heading', { name: /Mistborn/ })).toBeInTheDocument()

    fireEvent.click(screen.getByLabelText('Next author'))
    await screen.findByText('Patrick Rothfuss')

    // Without the fix, authorSeries.length > 0 (still 42's Mistborn) skips the
    // refetch, and book id 20 doesn't match series 1's bookId 10 — so this
    // would render Standalone only, with the Kingkiller heading missing.
    expect(await screen.findByRole('heading', { name: /Kingkiller Chronicle/ })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: /Mistborn/ })).not.toBeInTheDocument()
  })

  it('clears a stale error from the previous author after Next', async () => {
    vi.mocked(api.getAuthor).mockImplementation((id: number) =>
      Promise.resolve({ ...author, id, authorName: id === 42 ? 'Brandon Sanderson' : 'Patrick Rothfuss' }))
    vi.mocked(api.listAllBooks).mockResolvedValue([])
    vi.mocked(api.updateAuthor).mockRejectedValue(new Error('Update failed'))

    render(
      <MemoryRouter initialEntries={[{ pathname: '/author/42', state: { ids: [42, 43], index: 0 } }]}>
        <Routes>
          <Route path="/author/:id" element={<AuthorDetailPage />} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByText('Brandon Sanderson')
    fireEvent.click(screen.getByRole('switch', { name: 'Stop monitoring' }))
    expect(await screen.findByText('Update failed')).toBeInTheDocument()

    fireEvent.click(screen.getByLabelText('Next author'))
    await screen.findByText('Patrick Rothfuss')

    expect(screen.queryByText('Update failed')).not.toBeInTheDocument()
  })
})

// #2601: Refresh answers 202 and the sync runs in the background. The page used
// to re-read the author and books as soon as the 202 landed, which showed the
// state from before the click, and nothing looked again afterwards.
describe('AuthorDetailPage: manual refresh', () => {
  const oldBooks = [makeBook({ id: 1, title: 'Ancillary Justice', status: 'imported' })]
  const newBooks = [
    makeBook({ id: 1, title: 'Ancillary Justice', status: 'imported' }),
    makeBook({ id: 2, title: 'Translation State', status: 'wanted' }),
  ]

  beforeEach(() => {
    vi.clearAllMocks()
    installLocalStorageMock()
    vi.mocked(api.listAuthorSeries).mockResolvedValue([])
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  // Serves the author and books the way the server does: the pre-refresh state
  // with syncInProgress set until the sync finishes at finishAt, then the new
  // state. Call after vi.useFakeTimers so Date.now is the fake clock.
  function serveSyncFinishingAt(finishAt: number) {
    const synced = () => Date.now() >= finishAt
    vi.mocked(api.getAuthor).mockImplementation(async () =>
      synced() ? { ...author, description: 'New bio' } : { ...author, description: 'Old bio', syncInProgress: true },
    )
    vi.mocked(api.listAllBooks).mockImplementation(async () => (synced() ? newBooks : oldBooks))
  }

  async function advance(ms: number) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms)
    })
  }

  it('waits for the background sync to finish and then shows its result', async () => {
    renderAuthorDetailPage(oldBooks, 'grid', { description: 'Old bio' })
    await screen.findByRole('heading', { name: 'Ancillary Justice' })

    vi.useFakeTimers()
    serveSyncFinishingAt(Date.now() + 3000)
    vi.mocked(api.refreshAuthor).mockResolvedValue(undefined)

    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await advance(0)
    expect(api.refreshAuthor).toHaveBeenCalledWith(42)
    expect(screen.getByRole('button', { name: 'Refreshing…' })).toBeDisabled()

    // First poll: the sync is still running, so nothing changes on the page.
    await advance(2000)
    expect(screen.queryByRole('heading', { name: 'Translation State' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Refreshing…' })).toBeDisabled()

    // Second poll: the sync is done, so the page shows what it wrote.
    await advance(2000)
    expect(screen.getByRole('heading', { name: 'Translation State' })).toBeInTheDocument()
    expect(screen.getByText('New bio')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()

    // And it stops polling.
    const calls = vi.mocked(api.getAuthor).mock.calls.length
    await advance(10000)
    expect(vi.mocked(api.getAuthor).mock.calls.length).toBe(calls)
  })

  it('regroups by series after a refresh moves a book into one (#2554)', async () => {
    // Grouped by series: before the refresh the book is in none, the refresh
    // files it under one, and the page must regroup without a reload.
    localStorage.setItem('bindery.group.author-detail.series', 'true')
    const book = makeBook({ id: 7, title: 'Fjellvinden', status: 'imported' })
    renderAuthorDetailPage([book], 'table')
    await screen.findByRole('heading', { name: /Standalone/ })

    vi.useFakeTimers()
    const finishAt = Date.now() + 3000
    const synced = () => Date.now() >= finishAt
    vi.mocked(api.getAuthor).mockImplementation(async () => (synced() ? author : { ...author, syncInProgress: true }))
    vi.mocked(api.listAuthorSeries).mockImplementation(async () => (synced()
      ? [{ id: 3, foreignSeriesId: 's:3', title: 'Fjellserien', description: '', monitored: false, books: [{ seriesId: 3, bookId: 7, positionInSeries: '1' }] }]
      : []))
    vi.mocked(api.refreshAuthor).mockResolvedValue(undefined)

    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await advance(4000)
    expect(screen.getByRole('heading', { name: /Fjellserien/ })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: /Standalone/ })).toBeNull()
  })

  // Refresh frees its button after a minute, but a large catalogue outlasts
  // that (#3058): the reload still sees the sync running, so the page keeps
  // watching it and shows the result when it ends.
  it('frees the button after a minute and keeps watching a longer sync', async () => {
    renderAuthorDetailPage(oldBooks, 'grid', { description: 'Old bio' })
    await screen.findByRole('heading', { name: 'Ancillary Justice' })

    vi.useFakeTimers()
    serveSyncFinishingAt(Date.now() + 3 * 60 * 1000)
    vi.mocked(api.refreshAuthor).mockResolvedValue(undefined)

    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await advance(58000)
    expect(screen.getByRole('button', { name: 'Refreshing…' })).toBeDisabled()
    await advance(2000)
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()
    expect(screen.getByRole('status')).toHaveTextContent('Fetching this author’s catalogue')

    await advance(2 * 60 * 1000 + 10000)
    expect(screen.getByRole('heading', { name: 'Translation State' })).toBeInTheDocument()
    expect(screen.queryByRole('status')).toBeNull()

    const calls = vi.mocked(api.getAuthor).mock.calls.length
    await advance(30000)
    expect(vi.mocked(api.getAuthor).mock.calls.length).toBe(calls)
  })

  it('waits on a sync that was already running instead of reporting the 409', async () => {
    renderAuthorDetailPage(oldBooks, 'grid', { description: 'Old bio' })
    await screen.findByRole('heading', { name: 'Ancillary Justice' })

    vi.useFakeTimers()
    serveSyncFinishingAt(Date.now() + 1000)
    vi.mocked(api.refreshAuthor).mockRejectedValue(
      new ApiError(409, { error: 'a refresh for this author is already running' }, 'Conflict'),
    )

    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await advance(2000)
    expect(screen.getByRole('heading', { name: 'Translation State' })).toBeInTheDocument()
    expect(screen.queryByText(/already running/)).toBeNull()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()
  })

  // The 409 fires for any running sync, and the scheduled, bulk, Refresh all
  // and add syncs all read the metadata cache. Showing their result would
  // hand the user the cached data the click was meant to skip, so once that
  // sync ends the page asks for its own refresh and waits for that one.
  it('asks again once a background sync that answered 409 ends, and shows that refresh', async () => {
    renderAuthorDetailPage(oldBooks, 'grid', { description: 'Old bio' })
    await screen.findByRole('heading', { name: 'Ancillary Justice' })

    vi.useFakeTimers()
    const backgroundEndsAt = Date.now() + 1000
    let ownEndsAt: number | null = null
    const ownDone = () => ownEndsAt !== null && Date.now() >= ownEndsAt
    vi.mocked(api.getAuthor).mockImplementation(async () => {
      if (ownDone()) return { ...author, description: 'New bio' }
      const syncing = Date.now() < backgroundEndsAt || ownEndsAt !== null
      return { ...author, description: 'Old bio', syncInProgress: syncing }
    })
    vi.mocked(api.listAllBooks).mockImplementation(async () => (ownDone() ? newBooks : oldBooks))
    vi.mocked(api.refreshAuthor)
      .mockRejectedValueOnce(new ApiError(409, { error: 'a refresh for this author is already running' }, 'Conflict'))
      .mockImplementationOnce(async () => {
        ownEndsAt = Date.now() + 3000
      })

    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    // First poll: the background sync has ended with cached data. The page
    // starts its own refresh instead of showing that.
    await advance(2000)
    expect(api.refreshAuthor).toHaveBeenCalledTimes(2)
    expect(screen.queryByRole('heading', { name: 'Translation State' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Refreshing…' })).toBeDisabled()

    // Its own sync ends at 5 seconds; the poll at 6 seconds sees it.
    await advance(4000)
    expect(screen.getByRole('heading', { name: 'Translation State' })).toBeInTheDocument()
    expect(screen.getByText('New bio')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()
  })

  it('stops after a second 409 instead of looping', async () => {
    renderAuthorDetailPage(oldBooks, 'grid', { description: 'Old bio' })
    await screen.findByRole('heading', { name: 'Ancillary Justice' })

    vi.useFakeTimers()
    serveSyncFinishingAt(Date.now() + 1000)
    vi.mocked(api.refreshAuthor).mockRejectedValue(
      new ApiError(409, { error: 'a refresh for this author is already running' }, 'Conflict'),
    )

    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await advance(2000)
    expect(api.refreshAuthor).toHaveBeenCalledTimes(2)
    expect(screen.getByRole('heading', { name: 'Translation State' })).toBeInTheDocument()
    expect(screen.queryByText(/already running/)).toBeNull()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()

    const posts = vi.mocked(api.refreshAuthor).mock.calls.length
    const polls = vi.mocked(api.getAuthor).mock.calls.length
    await advance(20000)
    expect(vi.mocked(api.refreshAuthor).mock.calls.length).toBe(posts)
    expect(vi.mocked(api.getAuthor).mock.calls.length).toBe(polls)
  })

  // The reload after the wait used the filter captured when Refresh was
  // clicked, so switching to Excluded meanwhile had its rows overwritten by a
  // list fetched without them.
  it('reloads with the status filter picked while it waited', async () => {
    const excludedBook = makeBook({ id: 3, title: 'Provenance', status: 'wanted', excluded: true })
    renderAuthorDetailPage(oldBooks, 'table', { description: 'Old bio' })
    await screen.findByText('Ancillary Justice')

    vi.useFakeTimers()
    const finishAt = Date.now() + 3000
    const synced = () => Date.now() >= finishAt
    vi.mocked(api.getAuthor).mockImplementation(async () =>
      synced() ? { ...author, description: 'New bio' } : { ...author, description: 'Old bio', syncInProgress: true },
    )
    vi.mocked(api.listAllBooks).mockImplementation(async params => {
      const base = synced() ? newBooks : oldBooks
      return params?.includeExcluded ? [...base, excludedBook] : base
    })
    vi.mocked(api.refreshAuthor).mockResolvedValue(undefined)

    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await advance(0)
    setFilter('Status', 'excluded')
    await advance(0)

    // The sync ends at 3 seconds; the poll at 4 seconds sees it and reloads.
    await advance(4000)
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()
    expect(screen.getByText('Provenance')).toBeInTheDocument()
    expect(api.listAllBooks).toHaveBeenLastCalledWith({ authorId: 42, includeExcluded: true })
  })
})

describe('AuthorDetailPage — book link nav state (#2548, book side)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    installLocalStorageMock()
    vi.mocked(api.listAuthorSeries).mockResolvedValue([])
  })

  it('carries {ids, index} on book links and the row click, scoped to filteredBooks order', async () => {
    vi.mocked(api.getAuthor).mockResolvedValue(author)
    vi.mocked(api.listAllBooks).mockResolvedValue([
      makeBook({ id: 10, title: 'The Final Empire', status: 'imported' }),
      makeBook({ id: 11, title: 'The Well of Ascension', status: 'imported' }),
    ])
    localStorage.setItem('bindery.view.author-detail', 'table')

    let capturedState: unknown
    render(
      <MemoryRouter initialEntries={['/author/42']}>
        <StateProbe onState={s => { capturedState = s }} />
        <Routes>
          <Route path="/author/:id" element={<AuthorDetailPage />} />
        </Routes>
      </MemoryRouter>,
    )

    const row = (await screen.findByText('The Well of Ascension')).closest('tr')!
    fireEvent.click(row)

    await waitFor(() => expect(capturedState).toEqual({ ids: [10, 11], index: 1, hopDepth: 1 }))
  })

  it('carries {ids, index} on the grid card link too, not just the table row', async () => {
    vi.mocked(api.getAuthor).mockResolvedValue(author)
    vi.mocked(api.listAllBooks).mockResolvedValue([
      makeBook({ id: 10, title: 'The Final Empire', status: 'imported' }),
      makeBook({ id: 11, title: 'The Well of Ascension', status: 'imported' }),
    ])
    // No view override — grid is the page default, unlike the table-row test above.

    let capturedState: unknown
    render(
      <MemoryRouter initialEntries={['/author/42']}>
        <StateProbe onState={s => { capturedState = s }} />
        <Routes>
          <Route path="/author/:id" element={<AuthorDetailPage />} />
        </Routes>
      </MemoryRouter>,
    )

    const link = (await screen.findByRole('heading', { name: 'The Well of Ascension' })).closest('a')!
    fireEvent.click(link)

    await waitFor(() => expect(capturedState).toEqual({ ids: [10, 11], index: 1, hopDepth: 1 }))
  })

  // #3457: cleaning up a library meant leaving the author page to hunt for the
  // series by name. The heading opens it on the Series page, the same state
  // the Authors page and the header search hand over.
  it('links a series heading to the Series page with that series open (#3457)', async () => {
    vi.mocked(api.getAuthor).mockResolvedValue(author)
    vi.mocked(api.listAllBooks).mockResolvedValue([
      makeBook({ id: 10, title: 'Elantris', status: 'imported' }),
      makeBook({ id: 11, title: 'The Final Empire', status: 'imported' }),
    ])
    vi.mocked(api.listAuthorSeries).mockResolvedValue([
      { id: 7, foreignSeriesId: 'OL-MB', title: 'Mistborn', description: '', monitored: true, books: [{ seriesId: 7, bookId: 11, positionInSeries: '1' }] },
    ])
    localStorage.setItem('bindery.view.author-detail', 'table')

    let path = ''
    let capturedState: unknown
    render(
      <MemoryRouter initialEntries={['/author/42']}>
        <LocationProbe onLocation={p => { path = p }} />
        <StateProbe onState={s => { capturedState = s }} />
        <Routes>
          <Route path="/author/:id" element={<AuthorDetailPage />} />
          <Route path="/series" element={<div>series page</div>} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByText('Elantris')
    fireEvent.click(screen.getByRole('switch', { name: 'Group by series' }))
    // Standalone is not a series, so it stays plain text.
    const standalone = await screen.findByRole('heading', { name: /Standalone/ })
    expect(within(standalone).queryByRole('link')).toBeNull()

    fireEvent.click(within(screen.getByRole('heading', { name: /Mistborn/ })).getByRole('link', { name: 'Mistborn' }))

    await screen.findByText('series page')
    expect(path).toBe('/series')
    expect(capturedState).toEqual({ seriesId: 7 })
  })

  it('follows the seriesGroups order for the nav chain when Group by series is on', async () => {
    vi.mocked(api.getAuthor).mockResolvedValue(author)
    vi.mocked(api.listAllBooks).mockResolvedValue([
      makeBook({ id: 10, title: 'Elantris', status: 'imported' }),
      makeBook({ id: 11, title: 'The Final Empire', status: 'imported' }),
      makeBook({ id: 12, title: 'The Well of Ascension', status: 'imported' }),
    ])
    vi.mocked(api.listAuthorSeries).mockResolvedValue([
      {
        id: 1, foreignSeriesId: 'OL-MB', title: 'Mistborn', description: '', monitored: true,
        books: [
          { seriesId: 1, bookId: 11, positionInSeries: '1' },
          { seriesId: 1, bookId: 12, positionInSeries: '2' },
        ],
      },
    ])
    localStorage.setItem('bindery.view.author-detail', 'table')

    let capturedState: unknown
    render(
      <MemoryRouter initialEntries={['/author/42']}>
        <StateProbe onState={s => { capturedState = s }} />
        <Routes>
          <Route path="/author/:id" element={<AuthorDetailPage />} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByText('Elantris')
    fireEvent.click(screen.getByRole('switch', { name: 'Group by series' }))
    // Confirms grouping is actually active: Elantris (no series) renders
    // under Standalone, visually last, behind Mistborn's two books.
    await screen.findByRole('heading', { name: /Standalone/ })

    const row = screen.getByText('Elantris').closest('tr')!
    fireEvent.click(row)

    // Elantris is index 0 in filteredBooks (load order) but renders last
    // under Standalone. #2548 asks for the order the user sees, so with
    // grouping on the chain follows the series section (Mistborn's two
    // books, in series order) then Standalone — Elantris lands at index 2.
    await waitFor(() => expect(capturedState).toEqual({ ids: [11, 12, 10], index: 2, hopDepth: 1 }))
  })

  it('counts a book that appears in two series once, at its first-occurrence position', async () => {
    vi.mocked(api.getAuthor).mockResolvedValue(author)
    vi.mocked(api.listAllBooks).mockResolvedValue([
      makeBook({ id: 10, title: 'Shadows of Self', status: 'imported' }),
      makeBook({ id: 11, title: 'The Alloy of Law', status: 'imported' }),
    ])
    vi.mocked(api.listAuthorSeries).mockResolvedValue([
      {
        id: 1, foreignSeriesId: 'OL-MB', title: 'Mistborn', description: '', monitored: true,
        books: [
          { seriesId: 1, bookId: 11, positionInSeries: '1' },
          { seriesId: 1, bookId: 10, positionInSeries: '2' },
        ],
      },
      {
        id: 2, foreignSeriesId: 'OL-WA', title: 'Wax and Wayne', description: '', monitored: true,
        books: [
          { seriesId: 2, bookId: 11, positionInSeries: '1' },
          { seriesId: 2, bookId: 10, positionInSeries: '2' },
        ],
      },
    ])
    localStorage.setItem('bindery.view.author-detail', 'table')

    let capturedState: unknown
    render(
      <MemoryRouter initialEntries={['/author/42']}>
        <StateProbe onState={s => { capturedState = s }} />
        <Routes>
          <Route path="/author/:id" element={<AuthorDetailPage />} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByText('Shadows of Self')
    fireEvent.click(screen.getByRole('switch', { name: 'Group by series' }))
    await screen.findByRole('heading', { name: /Wax and Wayne/ })

    // Both series list the same two books; the row under Mistborn (this
    // book's first occurrence) is the one that determines its chain index.
    const row = screen.getAllByText('The Alloy of Law')[0].closest('tr')!
    fireEvent.click(row)

    await waitFor(() => expect(capturedState).toEqual({ ids: [11, 10], index: 0, hopDepth: 1 }))
  })
})

// #2457: a bulk action used to clear the selection, so running two actions on
// the same books meant picking them all again. #2456: while picking, a click
// that missed the small checkbox opened the book and threw the selection away.
describe('AuthorDetailPage — selection (#2456, #2457)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    installLocalStorageMock()
    vi.mocked(api.listAuthorSeries).mockResolvedValue([])
  })

  function checkboxFor(title: string): HTMLInputElement {
    return screen.getByRole('checkbox', { name: `Select ${title}` }) as HTMLInputElement
  }

  it('keeps the selection after a bulk action so a second action reuses it (#2457)', async () => {
    vi.mocked(api.bulkActionBooks).mockResolvedValue({
      results: { '501': { ok: true }, '502': { ok: true } },
    })
    renderAuthorDetailPage(
      [
        makeBook({ id: 501, title: 'Keep One', status: 'wanted' }),
        makeBook({ id: 502, title: 'Keep Two', status: 'wanted' }),
        makeBook({ id: 503, title: 'Not Picked', status: 'wanted' }),
      ],
      'table',
    )
    await screen.findByText('Keep One')
    fireEvent.click(checkboxFor('Keep One'))
    fireEvent.click(checkboxFor('Keep Two'))

    fireEvent.click(screen.getByRole('button', { name: 'Unmonitor' }))
    await waitFor(() => expect(api.bulkActionBooks).toHaveBeenCalledWith([501, 502], 'unmonitor', undefined))
    // Wait for the post-action reload, which is where the clear used to land.
    await waitFor(() => expect(api.listAllBooks).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Monitor' })).not.toBeNull())
    await waitFor(() => expect(screen.getByRole('button', { name: 'Monitor' })).not.toBeDisabled())

    expect(checkboxFor('Keep One')).toBeChecked()
    expect(checkboxFor('Keep Two')).toBeChecked()
    expect(checkboxFor('Not Picked')).not.toBeChecked()
    expect(screen.getByText('2 selected')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '📖🎧 Set Both' }))
    await waitFor(() => expect(api.bulkActionBooks).toHaveBeenLastCalledWith([501, 502], 'set_media_type', 'both'))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Clear' })).not.toBeDisabled())

    // Clear is still the way out.
    fireEvent.click(screen.getByRole('button', { name: 'Clear' }))
    expect(screen.queryByText(/selected$/)).toBeNull()
  })

  it('drops deleted books from the selection and never sends them again (#2457)', async () => {
    vi.mocked(api.bulkActionBooks).mockResolvedValueOnce({
      results: { '601': { ok: true }, '602': { ok: false, error: 'locked' } },
    })
    renderAuthorDetailPage(
      [
        makeBook({ id: 601, title: 'Gone Book', status: 'wanted' }),
        makeBook({ id: 602, title: 'Stuck Book', status: 'wanted' }),
        makeBook({ id: 603, title: 'Other Book', status: 'wanted' }),
      ],
      'table',
    )
    await screen.findByText('Gone Book')
    // After the delete the server no longer returns 601.
    vi.mocked(api.listAllBooks).mockResolvedValue([
      makeBook({ id: 602, title: 'Stuck Book', status: 'wanted' }),
      makeBook({ id: 603, title: 'Other Book', status: 'wanted' }),
    ])
    fireEvent.click(checkboxFor('Gone Book'))
    fireEvent.click(checkboxFor('Stuck Book'))

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(within(await screen.findByTestId('confirm-dialog')).getByText(/Delete 2 book/)).toBeInTheDocument()
    await acceptConfirm()
    await waitFor(() => expect(api.bulkActionBooks).toHaveBeenCalledWith([601, 602], 'delete', undefined))
    await waitFor(() => expect(screen.queryByText('Gone Book')).toBeNull())
    await waitFor(() => expect(screen.getByRole('button', { name: 'Delete' })).not.toBeDisabled())

    // The book that failed to delete is still picked, so retrying is one click.
    expect(checkboxFor('Stuck Book')).toBeChecked()
    expect(screen.getByText('1 selected')).toBeInTheDocument()

    vi.mocked(api.bulkActionBooks).mockResolvedValueOnce({ results: { '602': { ok: true } } })
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(within(await screen.findByTestId('confirm-dialog')).getByText(/Delete 1 book/)).toBeInTheDocument()
    await acceptConfirm()
    await waitFor(() => expect(api.bulkActionBooks).toHaveBeenLastCalledWith([602], 'delete', undefined))
  })

  it('drops deleted books from the selection even when the reload fails (#2457)', async () => {
    vi.mocked(api.bulkActionBooks).mockResolvedValue({
      results: { '701': { ok: true } },
    })
    renderAuthorDetailPage(
      [
        makeBook({ id: 701, title: 'Doomed Book', status: 'wanted' }),
        makeBook({ id: 702, title: 'Bystander', status: 'wanted' }),
      ],
      'table',
    )
    await screen.findByText('Doomed Book')
    vi.mocked(api.listAllBooks).mockRejectedValue(new Error('offline'))
    fireEvent.click(checkboxFor('Doomed Book'))

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await acceptConfirm()
    await waitFor(() => expect(api.bulkActionBooks).toHaveBeenCalledWith([701], 'delete', undefined))
    // The stale row is still on screen, but it must not stay selected: a
    // second action would send an id the server already deleted.
    await waitFor(() => expect(screen.queryByText(/selected$/)).toBeNull())
    expect(checkboxFor('Doomed Book')).not.toBeChecked()
  })

  it('toggles a grid card on click while a selection exists, and opens it otherwise (#2456)', async () => {
    let seen = ''
    renderAuthorDetailPage(
      [
        makeBook({ id: 801, title: 'First Card', status: 'wanted' }),
        makeBook({ id: 802, title: 'Second Card', status: 'wanted' }),
      ],
      'grid',
      {},
      '/author/42',
      loc => { seen = loc },
    )
    const second = await screen.findByRole('heading', { name: 'Second Card' })
    fireEvent.click(checkboxFor('First Card'))

    // Anywhere on the card (here the title, well away from the checkbox)
    // adds it to the selection instead of opening the book.
    fireEvent.click(second)
    expect(checkboxFor('Second Card')).toBeChecked()
    expect(seen).toBe('/author/42')

    // And a second click takes it back out.
    fireEvent.click(second)
    expect(checkboxFor('Second Card')).not.toBeChecked()

    // Ctrl-click is the browser's open-in-new-tab gesture; leave it alone.
    fireEvent.click(second, { ctrlKey: true })
    expect(checkboxFor('Second Card')).not.toBeChecked()

    // With nothing selected, a click opens the book exactly as before.
    fireEvent.click(checkboxFor('First Card'))
    fireEvent.click(second)
    await waitFor(() => expect(seen).toBe('/book/802'))
  })

  it('toggles a table row on click while a selection exists (#2456)', async () => {
    let seen = ''
    renderAuthorDetailPage(
      [
        makeBook({ id: 901, title: 'Row One', status: 'wanted' }),
        makeBook({ id: 902, title: 'Row Two', status: 'wanted' }),
      ],
      'table',
      {},
      '/author/42',
      loc => { seen = loc },
    )
    await screen.findByText('Row One')
    fireEvent.click(checkboxFor('Row One'))

    // The title link inside the row and the bare row both toggle.
    fireEvent.click(screen.getByText('Row Two'))
    expect(checkboxFor('Row Two')).toBeChecked()
    fireEvent.click(rowForTitle('Row Two'))
    expect(checkboxFor('Row Two')).not.toBeChecked()
    expect(seen).toBe('/author/42')

    fireEvent.click(checkboxFor('Row One'))
    fireEvent.click(rowForTitle('Row Two'))
    await waitFor(() => expect(seen).toBe('/book/902'))
  })
})

// An author whose catalogue sync is still running when the page opens, which
// is every author straight after an add, must not read as having no books
// (#3058).
describe('AuthorDetailPage: sync running on load', () => {
  const books = [makeBook({ id: 1, title: 'Fjellvinden', status: 'wanted' })]

  beforeEach(() => {
    vi.clearAllMocks()
    installLocalStorageMock()
    vi.mocked(api.listAuthorSeries).mockResolvedValue([])
    vi.useFakeTimers({ shouldAdvanceTime: true })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  // One act per second, so React renders between ticks as it does in a
  // browser (a single long act batches every update to its end).
  async function advance(ms: number) {
    for (let left = ms; left > 0; left -= 1000) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(Math.min(1000, left))
      })
    }
  }

  // Serves the author as syncing with no books until finishAt, then synced
  // with the books it found.
  function serveSyncFinishingAt(finishAt: number, found: Book[]) {
    const synced = () => Date.now() >= finishAt
    vi.mocked(api.getAuthor).mockImplementation(async () => ({ ...author, syncInProgress: !synced() }))
    vi.mocked(api.listAllBooks).mockImplementation(async () => (synced() ? found : []))
  }

  function renderPage() {
    return render(
      <MemoryRouter initialEntries={['/author/42']}>
        <Routes>
          <Route path="/author/:id" element={<AuthorDetailPage />} />
        </Routes>
      </MemoryRouter>,
    )
  }

  it('shows the sync instead of the empty state, then the books when it ends', async () => {
    serveSyncFinishingAt(Date.now() + 5 * 60 * 1000, books)
    renderPage()

    expect(await screen.findByRole('status')).toHaveTextContent('Fetching this author’s catalogue')
    expect(screen.queryByText('No books tracked for this author yet.')).toBeNull()

    // No time limit: a catalogue that takes five minutes is still picked up.
    await advance(5 * 60 * 1000 + 10000)
    expect(screen.getByRole('heading', { name: 'Fjellvinden' })).toBeInTheDocument()
    expect(screen.queryByRole('status')).toBeNull()

    const calls = vi.mocked(api.getAuthor).mock.calls.length
    await advance(30000)
    expect(vi.mocked(api.getAuthor).mock.calls.length).toBe(calls)
  })

  it('backs off to one poll every ten seconds', async () => {
    serveSyncFinishingAt(Date.now() + 60 * 60 * 1000, books)
    renderPage()
    await screen.findByRole('status')

    await advance(2 * 60 * 1000)
    const before = vi.mocked(api.getAuthor).mock.calls.length
    await advance(60 * 1000)
    expect(vi.mocked(api.getAuthor).mock.calls.length - before).toBe(6)
  })

  it('keeps polling through a failed poll', async () => {
    serveSyncFinishingAt(Date.now() + 5000, books)
    vi.mocked(api.getAuthor)
      .mockResolvedValueOnce({ ...author, syncInProgress: true })
      .mockRejectedValueOnce(new Error('network'))
    renderPage()
    await screen.findByRole('status')

    await advance(15000)
    expect(screen.getByRole('heading', { name: 'Fjellvinden' })).toBeInTheDocument()
  })

  it('shows the empty state once a sync ends having found nothing', async () => {
    serveSyncFinishingAt(Date.now() + 1000, [])
    renderPage()
    await screen.findByRole('status')

    await advance(3000)
    expect(screen.getByText('No books tracked for this author yet.')).toBeInTheDocument()
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('keeps the spinner a circle on a narrow screen (shrink-0)', async () => {
    serveSyncFinishingAt(Date.now() + 60 * 1000, books)
    renderPage()
    const status = await screen.findByRole('status')
    expect(status.querySelector('span[aria-hidden="true"]')).toHaveClass('shrink-0')
  })

  it('keeps polling when the reload after the sync ends fails, until one succeeds', async () => {
    // Poll 1 sees the sync end; the next two getAuthor calls (the reload) fail.
    let calls = 0
    vi.mocked(api.getAuthor).mockImplementation(async () => {
      calls++
      if (calls === 1) return { ...author, syncInProgress: true }
      if (calls === 2) return { ...author, syncInProgress: false }
      if (calls <= 4) throw new Error('boom')
      return { ...author, syncInProgress: false }
    })
    vi.mocked(api.listAllBooks).mockResolvedValue(books)
    renderPage()
    await screen.findByRole('status')

    await advance(60 * 1000)
    expect(screen.getByRole('heading', { name: 'Fjellvinden' })).toBeInTheDocument()
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.queryByText('boom')).toBeNull()
  })

  it('gives up after a few failed reloads: the error shows, the fetching state does not, polling stops', async () => {
    let calls = 0
    vi.mocked(api.getAuthor).mockImplementation(async () => {
      calls++
      if (calls === 1) return { ...author, syncInProgress: true }
      if (calls % 2 === 0) return { ...author, syncInProgress: false }
      throw new Error('boom')
    })
    vi.mocked(api.listAllBooks).mockResolvedValue(books)
    renderPage()
    await screen.findByRole('status')

    await advance(5 * 60 * 1000)
    expect(screen.getByText('boom')).toBeInTheDocument()
    expect(screen.queryByRole('status')).toBeNull()
    const seen = vi.mocked(api.getAuthor).mock.calls.length
    await advance(60 * 1000)
    expect(vi.mocked(api.getAuthor).mock.calls.length).toBe(seen)
  })

  it('gives up after a few failed polls in a row', async () => {
    vi.mocked(api.getAuthor)
      .mockResolvedValueOnce({ ...author, syncInProgress: true })
      .mockRejectedValue(new Error('network'))
    vi.mocked(api.listAllBooks).mockResolvedValue([])
    renderPage()
    await screen.findByRole('status')

    await advance(5 * 60 * 1000)
    expect(screen.queryByRole('status')).toBeNull()
    const seen = vi.mocked(api.getAuthor).mock.calls.length
    expect(seen).toBe(4)
    await advance(60 * 1000)
    expect(vi.mocked(api.getAuthor).mock.calls.length).toBe(seen)
  })

  it.each([404, 403])('stops at once on a %i: the author is gone or not this user\'s', async status => {
    vi.mocked(api.getAuthor)
      .mockResolvedValueOnce({ ...author, syncInProgress: true })
      .mockRejectedValue(new ApiError(status, { error: 'nope' }, 'Not Found'))
    vi.mocked(api.listAllBooks).mockResolvedValue([])
    renderPage()
    await screen.findByRole('status')

    await advance(3000)
    expect(screen.queryByRole('status')).toBeNull()
    const seen = vi.mocked(api.getAuthor).mock.calls.length
    expect(seen).toBe(2)
    await advance(60 * 1000)
    expect(vi.mocked(api.getAuthor).mock.calls.length).toBe(seen)
  })

  describe('in a hidden tab', () => {
    function setHidden(hidden: boolean) {
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
      document.dispatchEvent(new Event('visibilitychange'))
    }
    afterEach(() => { Reflect.deleteProperty(document, 'hidden') })

    it('stops polling while hidden and catches up when the tab returns (#2360)', async () => {
      serveSyncFinishingAt(Date.now() + 60 * 60 * 1000, books)
      renderPage()
      await screen.findByRole('status')

      await act(async () => { setHidden(true) })
      const hiddenAt = vi.mocked(api.getAuthor).mock.calls.length
      await advance(2 * 60 * 1000)
      expect(vi.mocked(api.getAuthor).mock.calls.length).toBe(hiddenAt)

      await act(async () => { setHidden(false) })
      expect(vi.mocked(api.getAuthor).mock.calls.length).toBe(hiddenAt + 1)
    })
  })

  it('says books appear when it finishes only while there are none yet', async () => {
    serveSyncFinishingAt(Date.now() + 60 * 1000, books)
    renderPage()
    expect(await screen.findByRole('status')).toHaveTextContent('Books appear here when it finishes')

    // A scheduled refresh over an author who already has books.
    vi.mocked(api.getAuthor).mockImplementation(async () => ({ ...author, syncInProgress: true }))
    vi.mocked(api.listAllBooks).mockImplementation(async () => books)
    cleanup()
    renderPage()
    await screen.findByRole('heading', { name: 'Fjellvinden' })
    expect(screen.getByRole('status')).not.toHaveTextContent('Books appear here when it finishes')
    expect(screen.getByRole('status')).toHaveTextContent('Fetching this author’s catalogue')
  })
})
