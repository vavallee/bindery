import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AuthorMetadataLinkModal from './AuthorMetadataLinkModal'
import { api } from '../api/client'
import type { Author } from '../api/client'

const tMock = vi.hoisted(() => (key: string, options?: string | Record<string, unknown>) => {
  const strings: Record<string, string> = {
    'authorMetadataLink.title': 'Link metadata',
    'authorMetadataLink.searchPlaceholder': 'Search by author name…',
    'authorMetadataLink.search': 'Search',
    'authorMetadataLink.searching': 'Searching…',
    'authorMetadataLink.link': 'Link',
    'authorMetadataLink.linking': 'Linking…',
    'authorMetadataLink.noResults': 'No alternate metadata candidates found',
    'authorMetadataLink.previouslyLinked': 'previously linked',
    'common.cancel': 'Cancel',
  }
  if (typeof options === 'string') return options
  if (options?.defaultValue && typeof options.defaultValue === 'string') {
    return options.defaultValue
      .replace('{{count}}', String(options.count ?? ''))
      .replace('{{source}}', String(options.source ?? ''))
  }
  return strings[key] ?? key
})

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: tMock,
  }),
}))

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      searchAuthorLinkCandidates: vi.fn(),
      relinkAuthorUpstream: vi.fn(),
    },
  }
})

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function author(overrides: Partial<Author> = {}): Author {
  return {
    id: 42,
    foreignAuthorId: 'OL13200512A',
    authorName: 'Emilia Jae',
    sortName: 'Jae, Emilia',
    description: '',
    imageUrl: '',
    disambiguation: '',
    ratingsCount: 0,
    averageRating: 0,
    monitored: true,
    metadataProvider: 'openlibrary',
    ...overrides,
  }
}

describe('AuthorMetadataLinkModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.searchAuthorLinkCandidates).mockResolvedValue([])
    vi.mocked(api.relinkAuthorUpstream).mockResolvedValue(author())
  })

  it('does not let the initial search overwrite newer manual results', async () => {
    const initialSearch = deferred<Author[]>()
    const manualSearch = deferred<Author[]>()
    vi.mocked(api.searchAuthorLinkCandidates).mockImplementation(async (_id, term) => {
      if (term === 'Emilia Jae') return initialSearch.promise
      if (term === 'Manual Query') return manualSearch.promise
      return []
    })

    render(
      <AuthorMetadataLinkModal
        author={author()}
        onClose={vi.fn()}
        onLinked={vi.fn()}
      />,
    )

    await waitFor(() => expect(api.searchAuthorLinkCandidates).toHaveBeenCalledWith(42, 'Emilia Jae'))

    const input = screen.getByPlaceholderText('Search by author name…')
    fireEvent.change(input, { target: { value: 'Manual Query' } })
    const form = input.closest('form')
    if (!form) throw new Error('search form not found')
    fireEvent.submit(form)

    await waitFor(() => expect(api.searchAuthorLinkCandidates).toHaveBeenCalledWith(42, 'Manual Query'))

    await act(async () => {
      manualSearch.resolve([
        author({
          foreignAuthorId: 'hc:manual-result',
          authorName: 'Manual Result',
          metadataProvider: 'hardcover',
        }),
      ])
      await manualSearch.promise
    })
    expect(screen.getByText('Manual Result')).toBeInTheDocument()

    await act(async () => {
      initialSearch.resolve([
        author({
          foreignAuthorId: 'hc:initial-result',
          authorName: 'Initial Result',
          metadataProvider: 'hardcover',
        }),
      ])
      await initialSearch.promise
    })

    expect(screen.getByText('Manual Result')).toBeInTheDocument()
    expect(screen.queryByText('Initial Result')).not.toBeInTheDocument()
  })

  // #1754: several candidates can carry the same name, and linking is
  // disruptive enough to be worth checking first. Before this the modal
  // showed a name, a provider badge and counts, with no way to confirm which
  // record you were about to attach.
  it('links each candidate to its upstream record so it can be verified before linking', async () => {
    vi.mocked(api.searchAuthorLinkCandidates).mockResolvedValue([
      author({ id: 0, foreignAuthorId: 'OL1529996A', authorName: 'David Annandale' }),
    ])

    render(
      <AuthorMetadataLinkModal
        author={author({ foreignAuthorId: 'calibre:author:9', authorName: 'David Annandale' })}
        onClose={vi.fn()}
        onLinked={vi.fn()}
      />,
    )

    const link = await screen.findByRole('link', { name: 'View on OpenLibrary' })
    expect(link).toHaveAttribute('href', 'https://openlibrary.org/authors/OL1529996A')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  // Hardcover/DNB/Calibre/ABS foreign IDs have no stable public page, so the
  // shared helper returns null for them. Rendering a link anyway would send
  // the user to a dead URL, which is worse than showing nothing.
  it('omits the verify link for providers with no public page', async () => {
    vi.mocked(api.searchAuthorLinkCandidates).mockResolvedValue([
      author({ id: 0, foreignAuthorId: 'dnb:118540238', authorName: 'David Annandale', metadataProvider: 'dnb' }),
    ])

    render(
      <AuthorMetadataLinkModal
        author={author({ foreignAuthorId: 'calibre:author:9', authorName: 'David Annandale' })}
        onClose={vi.fn()}
        onLinked={vi.fn()}
      />,
    )

    await screen.findByRole('button', { name: 'Link' })
    expect(screen.queryByRole('link', { name: /View on/ })).toBeNull()
  })

  // #2688: a record the author was linked to before is offered again rather
  // than hidden, so relinking is not a one way door. It has to stay linkable,
  // and it has to be distinguishable from a record that was never used.
  it('marks a previously linked candidate and still offers it', async () => {
    vi.mocked(api.searchAuthorLinkCandidates).mockResolvedValue([
      { ...author({ id: 0, foreignAuthorId: 'OL220796A', authorName: 'Amy Tan' }), previouslyLinked: true },
      author({ id: 0, foreignAuthorId: 'OL9999999A', authorName: 'Amy Tan' }),
    ])

    render(
      <AuthorMetadataLinkModal
        author={author({ foreignAuthorId: 'OL15403031A', authorName: 'Amy Tan' })}
        onClose={vi.fn()}
        onLinked={vi.fn()}
      />,
    )

    await waitFor(() => expect(screen.getAllByRole('button', { name: 'Link' })).toHaveLength(2))
    expect(screen.getAllByText('previously linked')).toHaveLength(1)

    const marker = screen.getByText('previously linked')
    const row = marker.closest('div.flex.items-center.gap-3.justify-between')
    if (!row) throw new Error('candidate row not found')
    expect(row.querySelector('a[href="https://openlibrary.org/authors/OL220796A"]')).not.toBeNull()
  })
})
