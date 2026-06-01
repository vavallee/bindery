import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AuthorMetadataLinkModal from './AuthorMetadataLinkModal'
import { api } from '../api/client'
import type { Author } from '../api/client'

const tMock = vi.hoisted(() => (key: string, options?: string | Record<string, unknown>) => {
  const strings: Record<string, string> = {
    'authorMetadataLink.title': 'Link metadata',
    'authorMetadataLink.searchPlaceholder': 'Search by author name...',
    'authorMetadataLink.search': 'Search',
    'authorMetadataLink.searching': 'Searching...',
    'authorMetadataLink.link': 'Link',
    'authorMetadataLink.linking': 'Linking...',
    'authorMetadataLink.noResults': 'No alternate metadata candidates found',
    'common.cancel': 'Cancel',
  }
  if (typeof options === 'string') return options
  if (options?.defaultValue && typeof options.defaultValue === 'string') {
    return options.defaultValue.replace('{{count}}', String(options.count ?? ''))
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

    const input = screen.getByPlaceholderText('Search by author name...')
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
})
