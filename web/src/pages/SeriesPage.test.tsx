import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter, useLocation } from 'react-router'
import SeriesPage from './SeriesPage'
import { api } from '../api/client'
import type { Book, Series, SeriesHardcoverLink, SeriesHardcoverSearchResult, SystemStatus } from '../api/client'
import '../i18n'
import { acceptConfirm } from '../test-utils'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      status: vi.fn(),
      listBooks: vi.fn(),
      listAuthors: vi.fn(),
      listAllBooks: vi.fn(),
      listAllAuthors: vi.fn(),
      listSeries: vi.fn(),
      createSeries: vi.fn(),
      updateSeries: vi.fn(),
      deleteSeries: vi.fn(),
      deleteBook: vi.fn(),
      monitorSeries: vi.fn(),
      linkBookToSeries: vi.fn(),
      fillSeries: vi.fn(),
      fillSeriesAll: vi.fn(),
      applySeriesGenres: vi.fn(),
      clearSeriesGenres: vi.fn(),
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
    vi.mocked(api.listAllBooks).mockResolvedValue([])
    vi.mocked(api.listAllAuthors).mockResolvedValue([])
    vi.mocked(api.getSeriesHardcoverLink).mockRejectedValue(new Error('not linked'))
    vi.mocked(api.searchHardcoverSeries).mockResolvedValue([])
  })

  it('filters series by title and restores the list when search is cleared', async () => {
    renderSeriesPage([
      { id: 1, foreignSeriesId: 'series-1', title: 'The Stormlight Archive', description: '', monitored: true, books: [] },
      { id: 2, foreignSeriesId: 'series-2', title: 'Café Chronicles', description: '', monitored: false, books: [] },
    ])

    expect(await screen.findByRole('heading', { name: 'The Stormlight Archive' })).toBeInTheDocument()
    const search = screen.getByRole('searchbox', { name: 'Search series...' })
    fireEvent.change(search, { target: { value: '  CAFE  ' } })
    expect(screen.getByRole('heading', { name: 'Café Chronicles' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'The Stormlight Archive' })).not.toBeInTheDocument()

    fireEvent.change(search, { target: { value: 'missing series' } })
    expect(screen.getByRole('status')).toHaveTextContent('No series match "missing series"')
    expect(screen.queryByRole('heading', { level: 3 })).not.toBeInTheDocument()
    expect(screen.queryByText(/Series are populated automatically/)).not.toBeInTheDocument()

    fireEvent.change(search, { target: { value: '' } })
    expect(screen.getByRole('heading', { name: 'The Stormlight Archive' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Café Chronicles' })).toBeInTheDocument()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(api.listSeries).toHaveBeenCalledTimes(1)
  })

  it('keeps the empty-library guidance when there are no series to search', async () => {
    renderSeriesPage([])

    expect(await screen.findByText('No series found')).toBeInTheDocument()
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'Stormlight' } })
    expect(screen.getByText(/Series are populated automatically/)).toBeInTheDocument()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
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

  it('does not promise automatic checks the code never runs', async () => {
    // series.monitored is written by this toggle and read back for display,
    // and nothing else consumes it: no scheduled job checks a series for new
    // books and Fill gaps ignores the flag. The label used to say "Monitor
    // series" / "Monitored", which is what led to #2523. It must not claim
    // recurring attention until something actually schedules it.
    renderSeriesPage([{
      id: 13,
      foreignSeriesId: 'series-13',
      title: 'Mistborn',
      description: '',
      monitored: true,
      books: [],
    }])

    await screen.findByRole('heading', { name: 'Mistborn' })
    expect(screen.queryByText('Monitored')).toBeNull()
    expect(screen.queryByRole('switch', { name: /monitor/i })).toBeNull()
    const toggle = screen.getByRole('switch', { name: /shortlist/i })
    // Scoped to the switch: the Shortlisted filter button (#2871) carries the
    // same word.
    expect(within(toggle).getByText('Shortlisted')).toBeInTheDocument()
    expect(toggle).toHaveAttribute('title', expect.stringContaining('does not yet check'))
  })

  it('offers a genre override before a series has books', async () => {
    vi.mocked(api.applySeriesGenres).mockResolvedValue({ updated: 0 })
    const promptSpy = vi.spyOn(window, 'prompt').mockReturnValue('Fantasy, Epic')

    try {
      renderSeriesPage([{
        id: 12,
        foreignSeriesId: 'series-12',
        title: 'Empty Series',
        description: '',
        monitored: true,
        books: [],
      }])

      fireEvent.click(await screen.findByRole('heading', { name: 'Empty Series' }))
      fireEvent.click(screen.getByRole('button', { name: 'Set genre' }))

      await waitFor(() => expect(api.applySeriesGenres).toHaveBeenCalledWith(12, ['Fantasy', 'Epic']))
      expect(await screen.findByText('Genres set on 0 book(s)')).toBeInTheDocument()
    } finally {
      promptSpy.mockRestore()
    }
  })

  it('seeds the genre prompt with the current override and clears it when emptied', async () => {
    vi.mocked(api.clearSeriesGenres).mockResolvedValue({ cleared: true })
    const promptSpy = vi.spyOn(window, 'prompt').mockReturnValue('')

    try {
      renderSeriesPage([{
        id: 14,
        foreignSeriesId: 'series-14',
        title: 'Overridden Series',
        description: '',
        monitored: true,
        genreOverride: ['Fantasy', 'Epic'],
        genreOverrideSet: true,
        books: [],
      }])

      fireEvent.click(await screen.findByRole('heading', { name: 'Overridden Series' }))
      // An active override is visible on the control itself, not hidden state.
      fireEvent.click(screen.getByRole('button', { name: 'Genre \u2713' }))

      // The prompt must arrive pre-filled, otherwise editing means retyping.
      expect(promptSpy).toHaveBeenCalledWith(expect.any(String), 'Fantasy, Epic')
      await waitFor(() => expect(api.clearSeriesGenres).toHaveBeenCalledWith(14))
      expect(api.applySeriesGenres).not.toHaveBeenCalled()
      expect(await screen.findByText('Genre override removed')).toBeInTheDocument()
    } finally {
      promptSpy.mockRestore()
    }
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

  it('does not count an excluded book as missing and marks it excluded (#2324)', async () => {
    renderSeriesPage(
      [
        {
          id: 30,
          foreignSeriesId: 'series-30',
          title: 'Foundation',
          description: '',
          monitored: true,
          books: [
            {
              seriesId: 30,
              bookId: 201,
              positionInSeries: '1',
              book: {
                id: 201,
                foreignBookId: 'book-201',
                authorId: 5,
                title: 'Foundation',
                description: '',
                imageUrl: '',
                releaseDate: '1951-01-01',
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
            {
              seriesId: 30,
              bookId: 202,
              positionInSeries: '2',
              book: {
                id: 202,
                foreignBookId: 'book-202',
                authorId: 5,
                title: 'Second Foundation',
                description: '',
                imageUrl: '',
                releaseDate: '1953-01-01',
                genres: [],
                monitored: true,
                status: 'wanted',
                filePath: '',
                mediaType: 'ebook',
                ebookFilePath: '',
                audiobookFilePath: '',
                excluded: true,
              },
            },
          ],
        },
      ],
      { version: 'dev', commit: 'unknown', buildDate: '', enhancedHardcoverApi: false, hardcoverTokenConfigured: true },
    )

    // The only outstanding book is one the user excluded, so the series is not
    // missing anything and the amber "missing" pill must not show.
    const heading = await screen.findByRole('heading', { name: 'Foundation' })
    expect(screen.queryByText('1 missing')).not.toBeInTheDocument()

    // The excluded book carries an "Excluded" marker, not shown as a plain wanted book.
    fireEvent.click(heading)
    expect(await screen.findByText('Excluded')).toBeInTheDocument()
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

    renderSeriesPage([initial])

    expect(await screen.findByRole('heading', { name: 'Old Series' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    const dialog = await screen.findByRole('dialog', { name: 'Rename Series' })
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'New Series' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))

    fireEvent.click(await screen.findByRole('heading', { name: 'New Series' }))
    expect(await screen.findByRole('link', { name: /Existing Linked Book/ })).toHaveAttribute('href', '/book/201')
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))

    // In-app modal since #2359: the warning that linked books survive is on
    // screen instead of inside a window.confirm string.
    expect(await screen.findByText('Delete "New Series" from Series? Linked books will stay in your library.')).toBeInTheDocument()
    await acceptConfirm()

    await waitFor(() => expect(api.deleteSeries).toHaveBeenCalledWith(20))
    expect(api.deleteSeries).toHaveBeenCalledTimes(1)
    expect(api.deleteBook).not.toHaveBeenCalled()
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'New Series' })).not.toBeInTheDocument())
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
    vi.mocked(api.listAllBooks).mockResolvedValue([series.books![0].book!, candidate])
    vi.mocked(api.listAllAuthors).mockResolvedValue([
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
    ])
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
      mediaType: 'ebook',
    }))
  })

  it('sends the chosen media type when adding a missing Hardcover book (#1124)', async () => {
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
    vi.mocked(api.fillSeriesAll).mockResolvedValue({ queued: 1 })

    renderSeriesPage([series])

    fireEvent.click(await screen.findByRole('heading', { name: 'The Stormlight Archive' }))

    expect(await screen.findByRole('button', { name: 'add all' })).toBeInTheDocument()

    // Pick "audiobook" in the format selector, then add the single missing book.
    fireEvent.change(await screen.findByRole('combobox', { name: 'Format to add' }), {
      target: { value: 'audiobook' },
    })
    const rowTitle = await screen.findByText('Words of Radiance')
    const row = rowTitle.parentElement?.parentElement
    if (!row) throw new Error('expected Hardcover missing book row')
    fireEvent.click(within(row).getByRole('button', { name: 'add' }))

    await waitFor(() => expect(api.fillSeries).toHaveBeenCalledWith(40, {
      foreignBookId: 'hc:words-of-radiance',
      providerId: '102',
      position: '2',
      mediaType: 'audiobook',
    }))

    // "add all" carries the same chosen format through the catalog-expansion path.
    fireEvent.click(screen.getByRole('button', { name: 'add all' }))
    await waitFor(() => expect(api.fillSeriesAll).toHaveBeenCalledWith(40, 'audiobook'))
  })

  it('links a Hardcover missing row that maps to an existing library book', async () => {
    const hardcoverLink = {
      id: 1,
      seriesId: 41,
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
      id: 41,
      foreignSeriesId: 'series-41',
      title: 'The Stormlight Archive',
      description: '',
      monitored: true,
      books: [],
      hardcoverLink,
    }
    vi.mocked(api.getSeriesHardcoverDiff).mockResolvedValue({
      seriesId: 41,
      link: hardcoverLink,
      present: [],
      missing: [
        {
          foreignBookId: 'hc:words-of-radiance',
          providerId: '102',
          title: 'Words of Radiance',
          position: '2',
          authorName: 'Brandon Sanderson',
          localBookId: 555,
        },
      ],
      localOnly: [],
      uncertain: [],
      presentCount: 0,
      missingCount: 1,
    })

    renderSeriesPage([series])

    fireEvent.click(await screen.findByRole('heading', { name: 'The Stormlight Archive' }))

    const bookLink = await screen.findByRole('link', { name: /Words of Radiance/ })
    expect(bookLink).toHaveAttribute('href', '/book/555')
    const row = within(bookLink).queryByRole('button', { name: 'add' })
    expect(row).not.toBeInTheDocument()
  })

  it('keeps a Hardcover missing row without a library match non-clickable', async () => {
    const hardcoverLink = {
      id: 1,
      seriesId: 42,
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
      id: 42,
      foreignSeriesId: 'series-42',
      title: 'The Stormlight Archive',
      description: '',
      monitored: true,
      books: [],
      hardcoverLink,
    }
    vi.mocked(api.getSeriesHardcoverDiff).mockResolvedValue({
      seriesId: 42,
      link: hardcoverLink,
      present: [],
      missing: [
        {
          foreignBookId: 'hc:rhythm-of-war',
          providerId: '103',
          title: 'Rhythm of War',
          position: '4',
          authorName: 'Brandon Sanderson',
        },
      ],
      localOnly: [],
      uncertain: [],
      presentCount: 0,
      missingCount: 1,
    })

    renderSeriesPage([series])

    fireEvent.click(await screen.findByRole('heading', { name: 'The Stormlight Archive' }))

    expect(await screen.findByText('Rhythm of War')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Rhythm of War/ })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'add' })).toBeInTheDocument()
  })
})

// #1708: near-identical light novel candidates cannot be told apart without
// opening them, so the candidate list and the linked-series display both need a
// way through to hardcover.app. The public page routes on the slug, so a row
// without one renders no link at all rather than one that 404s.
describe('SeriesPage Hardcover links out (#1708)', () => {
  const linkedSeries = (hardcoverSlug?: string): Series => ({
    id: 50,
    foreignSeriesId: 'series-50',
    title: 'The Stormlight Archive',
    description: '',
    monitored: true,
    books: [],
    hardcoverLink: {
      id: 1,
      seriesId: 50,
      hardcoverSeriesId: 'hc-series:42',
      hardcoverProviderId: '42',
      ...(hardcoverSlug ? { hardcoverSlug } : {}),
      hardcoverTitle: 'The Stormlight Archive',
      hardcoverAuthorName: 'Brandon Sanderson',
      hardcoverBookCount: 10,
      confidence: 1,
      linkedBy: 'manual',
      linkedAt: '2026-01-01T00:00:00Z',
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z',
    },
  })

  const emptyDiff = {
    seriesId: 50,
    present: [],
    missing: [],
    localOnly: [],
    uncertain: [],
    presentCount: 0,
    missingCount: 0,
  }

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.status).mockResolvedValue({ version: 'dev', commit: 'unknown', buildDate: '', enhancedHardcoverApi: true, hardcoverTokenConfigured: true })
    vi.mocked(api.listBooks).mockResolvedValue({ items: [], total: 0, limit: 100, offset: 0 })
    vi.mocked(api.listAuthors).mockResolvedValue({ items: [], total: 0, limit: 100, offset: 0 })
    vi.mocked(api.listAllBooks).mockResolvedValue([])
    vi.mocked(api.listAllAuthors).mockResolvedValue([])
    vi.mocked(api.getSeriesHardcoverLink).mockRejectedValue(new Error('not linked'))
    vi.mocked(api.searchHardcoverSeries).mockResolvedValue([])
  })

  it('links the expanded linked-series row to its Hardcover page', async () => {
    const series = linkedSeries('the-stormlight-archive')
    vi.mocked(api.getSeriesHardcoverDiff).mockResolvedValue({ ...emptyDiff, link: series.hardcoverLink! })
    renderSeriesPage([series])

    fireEvent.click(await screen.findByRole('heading', { name: 'The Stormlight Archive' }))

    const link = await screen.findByRole('link', { name: /View on Hardcover/ })
    expect(link).toHaveAttribute('href', 'https://hardcover.app/series/the-stormlight-archive')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('renders no link when the stored link has no slug', async () => {
    const series = linkedSeries()
    vi.mocked(api.getSeriesHardcoverDiff).mockResolvedValue({ ...emptyDiff, link: series.hardcoverLink! })
    renderSeriesPage([series])

    fireEvent.click(await screen.findByRole('heading', { name: 'The Stormlight Archive' }))

    expect(await screen.findByText(/Hardcover: The Stormlight Archive/)).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /View on Hardcover/ })).not.toBeInTheDocument()
  })

  it('links each Hardcover candidate in the picker, and says so when one has no page', async () => {
    const candidates: SeriesHardcoverSearchResult[] = [
      {
        foreignId: 'hc-series:42',
        providerId: '42',
        slug: 'the-stormlight-archive',
        title: 'The Stormlight Archive',
        authorName: 'Brandon Sanderson',
        bookCount: 10,
        readersCount: 19323,
        books: ['The Way of Kings'],
      },
      {
        foreignId: 'hc-series:43',
        providerId: '43',
        title: 'The Stormlight Archive (manga)',
        authorName: 'Brandon Sanderson',
        bookCount: 3,
        readersCount: 12,
        books: [],
      },
    ]
    vi.mocked(api.searchHardcoverSeries).mockResolvedValue(candidates)
    vi.mocked(api.autoLinkSeriesHardcover).mockResolvedValue({ linked: false, candidates, reason: 'ambiguous candidates' })

    renderSeriesPage([
      {
        id: 51,
        foreignSeriesId: 'series-51',
        title: 'The Stormlight Archive',
        description: '',
        monitored: true,
        books: [],
      },
    ])

    fireEvent.click(await screen.findByRole('button', { name: 'Search' }))
    const dialog = await screen.findByRole('dialog')

    const links = await within(dialog).findAllByRole('link', { name: /View on Hardcover/ })
    expect(links).toHaveLength(1)
    expect(links[0]).toHaveAttribute('href', 'https://hardcover.app/series/the-stormlight-archive')
    expect(within(dialog).getByText('No Hardcover page for this result')).toBeInTheDocument()
  })

  it('still selects a candidate after the link was moved out of the row button', async () => {
    const candidate: SeriesHardcoverSearchResult = {
      foreignId: 'hc-series:42',
      providerId: '42',
      slug: 'the-stormlight-archive',
      title: 'The Stormlight Archive',
      authorName: 'Brandon Sanderson',
      bookCount: 10,
      readersCount: 19323,
      books: [],
    }
    vi.mocked(api.searchHardcoverSeries).mockResolvedValue([candidate])
    vi.mocked(api.autoLinkSeriesHardcover).mockResolvedValue({ linked: false, candidates: [candidate], reason: 'low confidence' })
    vi.mocked(api.linkSeriesHardcover).mockResolvedValue({
      id: 9,
      seriesId: 52,
      hardcoverSeriesId: candidate.foreignId,
      hardcoverProviderId: '42',
      hardcoverSlug: 'the-stormlight-archive',
      hardcoverTitle: candidate.title,
      hardcoverAuthorName: candidate.authorName,
      hardcoverBookCount: 10,
      confidence: 1,
      linkedBy: 'manual',
      linkedAt: '2026-01-01T00:00:00Z',
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z',
    })

    renderSeriesPage([
      {
        id: 52,
        foreignSeriesId: 'series-52',
        title: 'The Stormlight Archive',
        description: '',
        monitored: true,
        books: [],
      },
    ])

    fireEvent.click(await screen.findByRole('button', { name: 'Search' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(await within(dialog).findByText('The Stormlight Archive'))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Confirm Selection' }))

    // The slug travels with the selection so the backend can store it.
    await waitFor(() => expect(api.linkSeriesHardcover).toHaveBeenCalledWith(52, candidate))
  })
})

// Series page filters (#2871). Every one of them is computed from the series
// list the page already loaded: no per series Hardcover diff, and nothing that
// marks, monitors, searches or fills.
describe('SeriesPage filters', () => {
  const enhancedOff: SystemStatus = { version: 'dev', commit: 'unknown', buildDate: '', enhancedHardcoverApi: false, hardcoverTokenConfigured: false }
  const enhancedOn: SystemStatus = { version: 'dev', commit: 'unknown', buildDate: '', enhancedHardcoverApi: true, hardcoverTokenConfigured: true }

  function book(id: number, status: string, excluded = false): Book {
    return {
      id,
      foreignBookId: `book-${id}`,
      authorId: 1,
      title: `Book ${id}`,
      description: '',
      imageUrl: '',
      genres: [],
      monitored: true,
      status,
      filePath: '',
      mediaType: 'ebook',
      ebookFilePath: '',
      audiobookFilePath: '',
      excluded,
    } as Book
  }

  function series(id: number, title: string, books: Book[], extra: Partial<Series> = {}): Series {
    return {
      id,
      foreignSeriesId: `series-${id}`,
      title,
      description: '',
      monitored: false,
      books: books.map((b, i) => ({ seriesId: id, bookId: b.id, positionInSeries: String(i + 1), book: b })),
      ...extra,
    }
  }

  function link(seriesId: number, hardcoverBookCount: number): SeriesHardcoverLink {
    return {
      id: seriesId,
      seriesId,
      hardcoverSeriesId: `hc-series:${seriesId}`,
      hardcoverProviderId: String(seriesId),
      hardcoverTitle: `HC ${seriesId}`,
      hardcoverAuthorName: 'Someone',
      hardcoverBookCount,
      confidence: 1,
      linkedBy: 'manual',
      linkedAt: '2026-01-01T00:00:00Z',
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z',
    }
  }

  let currentSearch = ''
  function LocationProbe() {
    currentSearch = useLocation().search
    return null
  }

  function renderAt(list: Series[], status: SystemStatus, entry = '/series') {
    vi.mocked(api.listSeries).mockResolvedValue(list)
    vi.mocked(api.status).mockResolvedValue(status)
    return render(
      <MemoryRouter initialEntries={[entry]}>
        <SeriesPage />
        <LocationProbe />
      </MemoryRouter>,
    )
  }

  const library = [
    series(1, 'Gappy', [book(11, 'imported'), book(12, 'wanted')], { monitored: true }),
    series(2, 'Finished', [book(21, 'imported'), book(22, 'imported')]),
    // Only outstanding book is excluded: not a gap (#2324), so complete.
    series(3, 'Excluded Tail', [book(31, 'imported'), book(32, 'wanted', true)]),
    // Nothing in it at all: neither missing nor complete.
    series(4, 'Empty Shell', []),
  ]

  const headings = () => screen.queryAllByRole('heading', { level: 3 }).map(h => h.textContent)

  beforeEach(() => {
    vi.clearAllMocks()
    currentSearch = ''
  })

  it('narrows to series with missing books and to complete series', async () => {
    renderAt(library, enhancedOff)
    expect(await screen.findByRole('heading', { name: 'Gappy' })).toBeInTheDocument()
    const group = screen.getByRole('group', { name: 'Show series' })

    fireEvent.click(within(group).getByRole('button', { name: 'Missing books' }))
    expect(headings()).toEqual(['Gappy'])
    expect(within(group).getByRole('button', { name: 'Missing books' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByText('1 of 4 series')).toBeInTheDocument()

    fireEvent.click(within(group).getByRole('button', { name: 'Complete' }))
    expect(headings()).toEqual(['Finished', 'Excluded Tail'])

    fireEvent.click(within(group).getByRole('button', { name: 'All' }))
    expect(headings()).toEqual(['Gappy', 'Finished', 'Excluded Tail', 'Empty Shell'])
    expect(screen.getByText('4 series')).toBeInTheDocument()

    // Filtering is read only.
    expect(api.fillSeries).not.toHaveBeenCalled()
    expect(api.fillSeriesAll).not.toHaveBeenCalled()
    expect(api.monitorSeries).not.toHaveBeenCalled()
    expect(api.getSeriesHardcoverDiff).not.toHaveBeenCalled()
    expect(api.listSeries).toHaveBeenCalledTimes(1)
  })

  it('counts missing exactly as the card badge does when Hardcover linking is on', async () => {
    renderAt([
      // Every local book imported, but Hardcover lists five: the badge says
      // "3 missing", so the filter must call it missing, not complete.
      series(5, 'Hardcover Gap', [book(51, 'imported'), book(52, 'imported')], { hardcoverLink: link(5, 5) }),
      series(6, 'Hardcover Done', [book(61, 'imported')], { hardcoverLink: link(6, 1) }),
    ], enhancedOn)
    expect(await screen.findByText('3 missing')).toBeInTheDocument()
    const group = screen.getByRole('group', { name: 'Show series' })

    fireEvent.click(within(group).getByRole('button', { name: 'Missing books' }))
    expect(headings()).toEqual(['Hardcover Gap'])
    fireEvent.click(within(group).getByRole('button', { name: 'Complete' }))
    expect(headings()).toEqual(['Hardcover Done'])

    // No per series diff fan out to decide either.
    expect(api.getSeriesHardcoverDiff).not.toHaveBeenCalled()
  })

  it('shows unlinked series only when Hardcover linking is on', async () => {
    renderAt([
      series(7, 'Linked', [book(71, 'imported')], { hardcoverLink: link(7, 1) }),
      series(8, 'Unlinked', [book(81, 'imported')]),
    ], enhancedOn)
    expect(await screen.findByRole('heading', { name: 'Linked' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Not linked to Hardcover' }))
    expect(headings()).toEqual(['Unlinked'])
  })

  it('does not offer the unlinked filter without Hardcover linking, even from the URL', async () => {
    renderAt(library, enhancedOff, '/series?filter=unlinked')
    expect(await screen.findByRole('heading', { name: 'Gappy' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Not linked to Hardcover' })).not.toBeInTheDocument()
    expect(headings()).toHaveLength(4)
  })

  it('narrows to shortlisted series', async () => {
    renderAt(library, enhancedOff)
    expect(await screen.findByRole('heading', { name: 'Gappy' })).toBeInTheDocument()
    fireEvent.click(within(screen.getByRole('group', { name: 'Show series' })).getByRole('button', { name: 'Shortlisted' }))
    expect(headings()).toEqual(['Gappy'])
  })

  it('reads the filter from the URL and writes it back', async () => {
    renderAt(library, enhancedOff, '/series?filter=complete')
    expect(await screen.findByRole('heading', { name: 'Finished' })).toBeInTheDocument()
    expect(headings()).toEqual(['Finished', 'Excluded Tail'])

    fireEvent.click(screen.getByRole('button', { name: 'Missing books' }))
    expect(currentSearch).toBe('?filter=missing')
    fireEvent.click(screen.getByRole('button', { name: 'All' }))
    expect(currentSearch).toBe('')
  })

  it('combines the filter with the title search', async () => {
    renderAt(library, enhancedOff, '/series?filter=complete')
    expect(await screen.findByRole('heading', { name: 'Finished' })).toBeInTheDocument()
    const search = screen.getByRole('searchbox', { name: 'Search series...' })

    fireEvent.change(search, { target: { value: 'excluded' } })
    expect(headings()).toEqual(['Excluded Tail'])

    fireEvent.change(search, { target: { value: 'gappy' } })
    expect(headings()).toEqual([])
    expect(screen.getByRole('status')).toHaveTextContent('No series match "gappy" with this filter')

    fireEvent.change(search, { target: { value: '' } })
    fireEvent.click(screen.getByRole('button', { name: 'Shortlisted' }))
    fireEvent.click(screen.getByRole('button', { name: 'Missing books' }))
    expect(headings()).toEqual(['Gappy'])
  })
})
