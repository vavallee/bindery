import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SeriesPage from './SeriesPage'
import { api } from '../api/client'
import type { Series, SeriesHardcoverLink, SeriesHardcoverSearchResult, SystemStatus } from '../api/client'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      status: vi.fn(),
      listBooks: vi.fn(),
      listAuthors: vi.fn(),
      listSeries: vi.fn(),
      createSeries: vi.fn(),
      updateSeries: vi.fn(),
      deleteSeries: vi.fn(),
      deleteBook: vi.fn(),
      monitorSeries: vi.fn(),
      linkBookToSeries: vi.fn(),
      fillSeries: vi.fn(),
      autoLinkSeriesHardcover: vi.fn(),
      getSeriesHardcoverLink: vi.fn(),
      searchHardcoverSeries: vi.fn(),
      linkSeriesHardcover: vi.fn(),
      unlinkSeriesHardcover: vi.fn(),
      getSeriesHardcoverDiff: vi.fn(),
    },
  }
})

function renderSeriesPage(series: Series[], status: SystemStatus = { version: 'dev', commit: 'unknown', buildDate: '', enhancedHardcoverApi: true, hardcoverTokenConfigured: true }) {
  vi.mocked(api.listSeries).mockResolvedValue(series)
  vi.mocked(api.status).mockResolvedValue(status)
  return render(
    <MemoryRouter>
      <SeriesPage />
    </MemoryRouter>,
  )
}

describe('SeriesPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.status).mockResolvedValue({ version: 'dev', commit: 'unknown', buildDate: '', enhancedHardcoverApi: true, hardcoverTokenConfigured: true })
    vi.mocked(api.listBooks).mockResolvedValue({ items: [], total: 0, limit: 100, offset: 0 })
    vi.mocked(api.listAuthors).mockResolvedValue({ items: [], total: 0, limit: 100, offset: 0 })
    vi.mocked(api.getSeriesHardcoverLink).mockRejectedValue(new Error('not linked'))
    vi.mocked(api.searchHardcoverSeries).mockResolvedValue([])
  })

  it('hides Hardcover controls when enhanced Hardcover API is disabled', async () => {
    renderSeriesPage([
      {
        id: 11,
        foreignSeriesId: 'series-11',
        title: 'The Stormlight Archive',
        description: '',
        monitored: true,
        books: [],
        hardcoverLink: {
          id: 1,
          seriesId: 11,
          hardcoverSeriesId: 'hc-series:42',
          hardcoverProviderId: '42',
          hardcoverTitle: 'The Stormlight Archive',
          hardcoverAuthorName: 'Brandon Sanderson',
          hardcoverBookCount: 10,
          confidence: 1,
          linkedBy: 'manual',
          linkedAt: '2026-01-01T00:00:00Z',
          createdAt: '2026-01-01T00:00:00Z',
          updatedAt: '2026-01-01T00:00:00Z',
        },
      },
    ], { version: 'dev', commit: 'unknown', buildDate: '', enhancedHardcoverApi: false, hardcoverTokenConfigured: true, enhancedHardcoverDisabledReason: 'env_disabled' })

    expect(await screen.findByRole('heading', { name: 'The Stormlight Archive' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /link/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Search' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('heading', { name: 'The Stormlight Archive' }))
    expect(screen.queryByText(/Hardcover:/)).not.toBeInTheDocument()
    expect(api.getSeriesHardcoverDiff).not.toHaveBeenCalled()
  })

  it('links expanded series book rows to their book pages', async () => {
    renderSeriesPage([
      {
        id: 7,
        foreignSeriesId: 'series-7',
        title: 'Defiance of the Fall',
        description: '',
        monitored: true,
        books: [
          {
            seriesId: 7,
            bookId: 102,
            positionInSeries: '2',
            book: {
              id: 102,
              foreignBookId: 'book-102',
              authorId: 12,
              title: 'Defiance of the Fall 2',
              description: '',
              imageUrl: '',
              releaseDate: '2020-01-01',
              genres: [],
              monitored: true,
              status: 'imported',
              filePath: '',
              mediaType: 'ebook',
              ebookFilePath: '',
              audiobookFilePath: '',
              excluded: false,
            },
          },
        ],
      },
    ])

    fireEvent.click(await screen.findByRole('heading', { name: 'Defiance of the Fall' }))

    const bookLink = screen.getByRole('link', { name: /Defiance of the Fall 2/ })
    expect(bookLink).toHaveAttribute('href', '/book/102')
  })

  it('opens the Hardcover series link modal from the Search control', async () => {
    vi.mocked(api.autoLinkSeriesHardcover).mockResolvedValue({
      linked: false,
      reason: 'low confidence',
      candidates: [
        {
          foreignId: 'hc-series:42',
          providerId: '42',
          title: 'The Stormlight Archive',
          authorName: 'Brandon Sanderson',
          bookCount: 10,
          readersCount: 19323,
          books: null as unknown as string[],
          confidence: 0.7,
        },
      ],
    })

    renderSeriesPage([
      {
        id: 9,
        foreignSeriesId: 'series-9',
        title: 'Rhythm of War',
        description: '',
        monitored: true,
        books: [],
      },
    ])

    fireEvent.click(await screen.findByRole('button', { name: 'Search' }))

    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText('The Stormlight Archive')).toBeInTheDocument()
    expect(screen.getByText('70% match')).toBeInTheDocument()
  })

  it('auto-links a matching Hardcover series and loads its diff', async () => {
    const link: SeriesHardcoverLink = {
      id: 4,
      seriesId: 14,
      hardcoverSeriesId: 'hc-series:77',
      hardcoverProviderId: '77',
      hardcoverTitle: 'Mistborn',
      hardcoverAuthorName: 'Brandon Sanderson',
      hardcoverBookCount: 3,
      confidence: 0.94,
      linkedBy: 'auto',
      linkedAt: '2026-01-01T00:00:00Z',
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z',
    }
    vi.mocked(api.autoLinkSeriesHardcover).mockResolvedValue({
      linked: true,
      link,
      candidates: [],
    })
    vi.mocked(api.getSeriesHardcoverDiff).mockResolvedValue({
      seriesId: 14,
      link,
      present: [],
      missing: [
        {
          foreignBookId: 'hc:well-of-ascension',
          providerId: '78',
          title: 'The Well of Ascension',
          position: '2',
          authorName: 'Brandon Sanderson',
        },
      ],
      localOnly: [],
      uncertain: [],
      presentCount: 2,
      missingCount: 1,
    })

    renderSeriesPage([
      {
        id: 14,
        foreignSeriesId: 'series-14',
        title: 'Mistborn',
        description: '',
        monitored: true,
        books: [],
      },
    ])

    fireEvent.click(await screen.findByRole('button', { name: 'Search' }))

    await waitFor(() => expect(api.autoLinkSeriesHardcover).toHaveBeenCalledWith(14))
    expect(await screen.findByRole('button', { name: 'Auto link' })).toBeInTheDocument()
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Currently linked')).toBeInTheDocument()
    expect(within(dialog).getByText('Brandon Sanderson')).toBeInTheDocument()
    await waitFor(() => expect(api.getSeriesHardcoverDiff).toHaveBeenCalledWith(14))

    fireEvent.click(within(dialog).getByRole('button', { name: 'Close' }))
    fireEvent.click(screen.getByRole('heading', { name: 'Mistborn' }))

    expect(await screen.findByText('2 matched · 1 missing')).toBeInTheDocument()
  })

  it('opens linked Hardcover series without auto-linking again', async () => {
    renderSeriesPage([
      {
        id: 10,
        foreignSeriesId: 'series-10',
        title: 'The Stormlight Archive',
        description: '',
        monitored: true,
        books: [],
        hardcoverLink: {
          id: 1,
          seriesId: 10,
          hardcoverSeriesId: 'hc-series:42',
          hardcoverProviderId: '42',
          hardcoverTitle: 'The Stormlight Archive',
          hardcoverAuthorName: 'Brandon Sanderson',
          hardcoverBookCount: 10,
          confidence: 1,
          linkedBy: 'manual',
          linkedAt: '2026-01-01T00:00:00Z',
          createdAt: '2026-01-01T00:00:00Z',
          updatedAt: '2026-01-01T00:00:00Z',
        },
      },
    ])

    fireEvent.click(await screen.findByRole('button', { name: 'Manual link' }))

    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText('Currently linked')).toBeInTheDocument()
    expect(screen.getByText('Brandon Sanderson')).toBeInTheDocument()
    expect(api.autoLinkSeriesHardcover).not.toHaveBeenCalled()
  })

  it('confirms a manually selected Hardcover link from the modal', async () => {
    const result: SeriesHardcoverSearchResult = {
      foreignId: 'hc-series:42',
      providerId: '42',
      title: 'The Stormlight Archive',
      authorName: 'Brandon Sanderson',
      bookCount: 10,
      readersCount: 19323,
      books: ['The Way of Kings'],
      confidence: 0.68,
    }
    const link: SeriesHardcoverLink = {
      id: 2,
      seriesId: 12,
      hardcoverSeriesId: result.foreignId,
      hardcoverProviderId: result.providerId,
      hardcoverTitle: result.title,
      hardcoverAuthorName: result.authorName,
      hardcoverBookCount: result.bookCount,
      confidence: 1,
      linkedBy: 'manual',
      linkedAt: '2026-01-01T00:00:00Z',
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z',
    }
    vi.mocked(api.autoLinkSeriesHardcover).mockResolvedValue({
      linked: false,
      reason: 'low confidence',
      candidates: [result],
    })
    vi.mocked(api.linkSeriesHardcover).mockResolvedValue(link)

    renderSeriesPage([
      {
        id: 12,
        foreignSeriesId: 'series-12',
        title: 'Stormlight',
        description: '',
        monitored: true,
        books: [],
      },
    ])

    fireEvent.click(await screen.findByRole('button', { name: 'Search' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Confirm Selection' }))

    await waitFor(() => expect(api.linkSeriesHardcover).toHaveBeenCalledWith(12, result))
    expect(await screen.findByRole('button', { name: 'Manual link' })).toBeInTheDocument()
  })

  it('removes an existing Hardcover link from the modal', async () => {
    const hardcoverLink: SeriesHardcoverLink = {
      id: 3,
      seriesId: 13,
      hardcoverSeriesId: 'hc-series:42',
      hardcoverProviderId: '42',
      hardcoverTitle: 'The Stormlight Archive',
      hardcoverAuthorName: 'Brandon Sanderson',
      hardcoverBookCount: 10,
      confidence: 1,
      linkedBy: 'manual',
      linkedAt: '2026-01-01T00:00:00Z',
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z',
    }
    vi.mocked(api.getSeriesHardcoverLink).mockResolvedValue(hardcoverLink)
    vi.mocked(api.unlinkSeriesHardcover).mockResolvedValue({ success: true })

    renderSeriesPage([
      {
        id: 13,
        foreignSeriesId: 'series-13',
        title: 'The Stormlight Archive',
        description: '',
        monitored: true,
        books: [],
        hardcoverLink,
      },
    ])

    fireEvent.click(await screen.findByRole('button', { name: 'Manual link' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Remove Link' }))

    await waitFor(() => expect(api.unlinkSeriesHardcover).toHaveBeenCalledWith(13))
    await waitFor(() => expect(within(dialog).queryByText('Currently linked')).not.toBeInTheDocument())
  })

  it('creates a manual series from the page header', async () => {
    const created: Series = {
      id: 15,
      foreignSeriesId: 'manual:series:15',
      title: 'Dune Chronicles',
      description: '',
      monitored: false,
      books: [],
    }
    vi.mocked(api.listSeries).mockResolvedValueOnce([]).mockResolvedValueOnce([created])
    vi.mocked(api.createSeries).mockResolvedValue(created)

    renderSeriesPage([])

    fireEvent.click(await screen.findByRole('button', { name: 'Add Series' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add Series' })
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Dune Chronicles' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add Series' }))

    await waitFor(() => expect(api.createSeries).toHaveBeenCalledWith({ title: 'Dune Chronicles' }))
    expect(await screen.findByRole('heading', { name: 'Dune Chronicles' })).toBeInTheDocument()
  })

  it('renames and deletes a series without deleting linked books', async () => {
    const initial: Series = {
      id: 20,
      foreignSeriesId: 'manual:series:20',
      title: 'Old Series',
      description: '',
      monitored: false,
      books: [
        {
          seriesId: 20,
          bookId: 201,
          positionInSeries: '1',
          primarySeries: true,
          book: {
            id: 201,
            foreignBookId: 'book-201',
            authorId: 12,
            title: 'Existing Linked Book',
            description: '',
            imageUrl: '',
            genres: [],
            monitored: true,
            status: 'imported',
            filePath: '',
            mediaType: 'ebook',
            ebookFilePath: '',
            audiobookFilePath: '',
            excluded: false,
          },
        },
      ],
    }
    const renamed: Series = { ...initial, title: 'New Series' }
    vi.mocked(api.updateSeries).mockResolvedValue(renamed)
    vi.mocked(api.deleteSeries).mockResolvedValue(undefined)
    vi.mocked(api.deleteBook).mockResolvedValue(undefined)
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)

    try {
      renderSeriesPage([initial])

      expect(await screen.findByRole('heading', { name: 'Old Series' })).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
      const dialog = await screen.findByRole('dialog', { name: 'Rename Series' })
      fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'New Series' } })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))

      fireEvent.click(await screen.findByRole('heading', { name: 'New Series' }))
      expect(await screen.findByRole('link', { name: /Existing Linked Book/ })).toHaveAttribute('href', '/book/201')
      fireEvent.click(screen.getByRole('button', { name: 'Delete' }))

      await waitFor(() => expect(api.deleteSeries).toHaveBeenCalledWith(20))
      expect(api.deleteSeries).toHaveBeenCalledTimes(1)
      expect(api.deleteBook).not.toHaveBeenCalled()
      expect(confirmSpy).toHaveBeenCalledWith('Delete "New Series" from Series? Linked books will stay in your library.')
      await waitFor(() => expect(screen.queryByRole('heading', { name: 'New Series' })).not.toBeInTheDocument())
    } finally {
      confirmSpy.mockRestore()
    }
  })

  it('links an existing library book to an expanded series', async () => {
    const series: Series = {
      id: 30,
      foreignSeriesId: 'manual:series:30',
      title: 'Dune Chronicles',
      description: '',
      monitored: false,
      books: [
        {
          seriesId: 30,
          bookId: 101,
          positionInSeries: '1',
          primarySeries: true,
          book: {
            id: 101,
            foreignBookId: 'book-101',
            authorId: 12,
            title: 'Dune',
            description: '',
            imageUrl: '',
            genres: [],
            monitored: true,
            status: 'imported',
            filePath: '',
            mediaType: 'ebook',
            ebookFilePath: '',
            audiobookFilePath: '',
            excluded: false,
          },
        },
      ],
    }
    const candidate = {
      id: 102,
      foreignBookId: 'book-102',
      authorId: 12,
      title: 'Dune Messiah',
      description: '',
      imageUrl: '',
      genres: [],
      monitored: true,
      status: 'wanted',
      filePath: '',
      mediaType: 'ebook' as const,
      ebookFilePath: '',
      audiobookFilePath: '',
      excluded: false,
    }
    const updated: Series = {
      ...series,
      books: [...(series.books ?? []), { seriesId: 30, bookId: 102, positionInSeries: '2', primarySeries: true, book: candidate }],
    }
    vi.mocked(api.listBooks).mockResolvedValue({
      items: [series.books![0].book!, candidate],
      total: 2,
      limit: 100,
      offset: 0,
    })
    vi.mocked(api.listAuthors).mockResolvedValue({
      items: [
        {
          id: 12,
          foreignAuthorId: 'author-12',
          authorName: 'Frank Herbert',
          sortName: 'Herbert, Frank',
          description: '',
          imageUrl: '',
          disambiguation: '',
          ratingsCount: 0,
          averageRating: 0,
          monitored: true,
        },
      ],
      total: 1,
      limit: 100,
      offset: 0,
    })
    vi.mocked(api.linkBookToSeries).mockResolvedValue(updated)

    renderSeriesPage([series])

    fireEvent.click(await screen.findByRole('heading', { name: 'Dune Chronicles' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add Book' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add book to Dune Chronicles' })

    expect(within(dialog).queryByText('Dune')).not.toBeInTheDocument()
    fireEvent.click(await within(dialog).findByLabelText(/Dune Messiah/))
    fireEvent.change(within(dialog).getByLabelText('Position'), { target: { value: '2' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add' }))

    await waitFor(() => expect(api.linkBookToSeries).toHaveBeenCalledWith(30, {
      bookId: 102,
      positionInSeries: '2',
      primarySeries: true,
    }))
    expect(await screen.findByRole('link', { name: /Dune Messiah/ })).toHaveAttribute('href', '/book/102')
  })

  it('adds only the selected Hardcover missing book from its row', async () => {
    const hardcoverLink = {
      id: 1,
      seriesId: 40,
      hardcoverSeriesId: 'hc-series:42',
      hardcoverProviderId: '42',
      hardcoverTitle: 'The Stormlight Archive',
      hardcoverAuthorName: 'Brandon Sanderson',
      hardcoverBookCount: 2,
      confidence: 1,
      linkedBy: 'manual',
      linkedAt: '2026-01-01T00:00:00Z',
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z',
    }
    const series: Series = {
      id: 40,
      foreignSeriesId: 'series-40',
      title: 'The Stormlight Archive',
      description: '',
      monitored: true,
      books: [],
      hardcoverLink,
    }
    vi.mocked(api.getSeriesHardcoverDiff).mockResolvedValue({
      seriesId: 40,
      link: hardcoverLink,
      present: [],
      missing: [
        {
          foreignBookId: 'hc:words-of-radiance',
          providerId: '102',
          title: 'Words of Radiance',
          position: '2',
          authorName: 'Brandon Sanderson',
        },
      ],
      localOnly: [],
      uncertain: [],
      presentCount: 0,
      missingCount: 1,
    })
    vi.mocked(api.fillSeries).mockResolvedValue({ queued: 1 })

    renderSeriesPage([series])

    fireEvent.click(await screen.findByRole('heading', { name: 'The Stormlight Archive' }))
    expect(await screen.findByRole('button', { name: 'add all' })).toBeInTheDocument()
    const rowTitle = await screen.findByText('Words of Radiance')
    const row = rowTitle.parentElement?.parentElement
    if (!row) throw new Error('expected Hardcover missing book row')
    fireEvent.click(within(row).getByRole('button', { name: 'add' }))

    await waitFor(() => expect(api.fillSeries).toHaveBeenCalledWith(40, {
      foreignBookId: 'hc:words-of-radiance',
      providerId: '102',
      position: '2',
    }))
  })
})
