import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import AddAuthorConfirm from './AddAuthorConfirm'
import { loadAuthorAddDefaults } from './authorAddDefaults'
import type { AuthorAddDefaults } from './authorAddDefaults'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      const strings: Record<string, string> = {
        'addToLibrary.backToResults': 'Back to results',
        'addToLibrary.adding': 'Adding...',
        'addToLibrary.author.confirmAdd': 'Add author',
        'addToLibrary.author.customizeMonitoring': 'Monitoring',
        'addToLibrary.author.metadataProfile': 'Metadata profile',
        'addToLibrary.author.rootFolder': 'Root folder',
        'addToLibrary.author.mediaType': 'Media type',
        'addToLibrary.author.monitorMode': 'Monitor mode',
        'addToLibrary.author.monitorModeHint': 'The whole catalogue is added either way. This only decides which of those books Bindery searches for and downloads.',
        'addToLibrary.author.monitorLatestCount': 'Latest book count',
        'addToLibrary.author.monitorNewItems': 'Monitor newly discovered books',
        'addToLibrary.author.outcome.all': 'Adds up to {{count}} books and searches for all of them.',
        'addToLibrary.author.outcome.none': 'Adds up to {{count}} books and searches for none of them. Grab any of them by hand from the Books page.',
        'addToLibrary.author.outcome.latest': 'Adds up to {{count}} books and searches for the newest {{latest}}.',
        'addToLibrary.author.outcomeNoCount.all': 'Adds the whole catalogue and searches for all of it.',
        'addToLibrary.author.outcomeNoCount.none': 'Adds the whole catalogue and searches for none of it. Grab any book by hand from the Books page.',
        'addToLibrary.author.outcomeNoCount.latest': 'Adds the whole catalogue and searches for the newest {{latest}}.',
        'addToLibrary.author.autoGrabLabel': 'Auto-grab books on add',
        'addToLibrary.author.autoGrabNoClient': 'Auto-search is on, but no download client is configured.',
        'addToLibrary.author.autoGrabNoIndexer': 'Auto-search is on, but no indexer is configured.',
        'addToLibrary.author.openExisting': 'Open existing author',
        'addToLibrary.author.findMetadata': 'Find metadata',
        'addToLibrary.author.addFail': 'Failed to add author',
        'addToLibrary.author.providerMismatchNotice': 'This record comes from {{linked}}, not from your primary metadata provider {{primary}}.',
        'common.cancel': 'Cancel',
      }
      let out = strings[key] ?? key
      for (const [k, v] of Object.entries(options ?? {})) {
        out = out.split(`{{${k}}}`).join(String(v))
      }
      return out
    },
  }),
}))

vi.mock('../api/client', () => ({
  api: {
    listMetadataProfiles: vi.fn().mockResolvedValue([]),
    listRootFolders: vi.fn().mockResolvedValue([]),
    getSetting: vi.fn().mockRejectedValue(new Error('HTTP 404')),
    listIndexers: vi.fn().mockResolvedValue([{ id: 1, enabled: true, type: 'newznab' }]),
    listDownloadClients: vi.fn().mockResolvedValue([{ id: 1, enabled: true, type: 'sabnzbd' }]),
    addAuthor: vi.fn(),
  },
}))

import { api } from '../api/client'
import type { Author } from '../api/client'

function author(overrides: Partial<Author>): Author {
  return {
    id: 0,
    foreignAuthorId: 'OL26320A',
    authorName: 'J.R.R. Tolkien',
    sortName: 'Tolkien, J.R.R.',
    description: '',
    imageUrl: '',
    disambiguation: '',
    ratingsCount: 0,
    averageRating: 0,
    monitored: true,
    ...overrides,
  }
}

const bareDefaults: AuthorAddDefaults = {
  profiles: [],
  rootFolders: [],
  rootFolderId: null,
  mediaType: 'ebook',
  monitorMode: 'all',
  monitorLatestCount: 1,
}

function renderConfirm(a: Author, props: Partial<React.ComponentProps<typeof AddAuthorConfirm>> = {}) {
  const onBack = vi.fn()
  const onClose = vi.fn()
  const onAdded = vi.fn()
  render(<AddAuthorConfirm author={a} defaults={bareDefaults} primaryProvider={null} onBack={onBack} onClose={onClose} onAdded={onAdded} {...props} />)
  return { onBack, onClose, onAdded }
}

describe('AddAuthorConfirm', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listMetadataProfiles).mockResolvedValue([])
    vi.mocked(api.listRootFolders).mockResolvedValue([])
    vi.mocked(api.getSetting).mockRejectedValue(new Error('HTTP 404'))
    vi.mocked(api.listIndexers).mockResolvedValue([{ id: 1, enabled: true, type: 'newznab' }] as never)
    vi.mocked(api.listDownloadClients).mockResolvedValue([{ id: 1, enabled: true, type: 'sabnzbd' }] as never)
    vi.mocked(api.addAuthor).mockResolvedValue(author({ id: 37 }))
  })

  it('adds with the defaults and reports the created author', async () => {
    const { onAdded, onClose } = renderConfirm(author({}))
    fireEvent.click(screen.getByRole('button', { name: 'Add author' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalledWith(expect.objectContaining({ id: 37 })))
    // Closing is the modal's job, so a caller can navigate first.
    expect(onClose).not.toHaveBeenCalled()
    const callArg = vi.mocked(api.addAuthor).mock.calls[0][0]
    expect(callArg).toEqual(expect.objectContaining({ foreignAuthorId: 'OL26320A', authorName: 'J.R.R. Tolkien', monitored: true }))
    expect('monitorMode' in callArg).toBe(false)
    expect('monitorLatestCount' in callArg).toBe(false)
    expect('monitorNewItems' in callArg).toBe(false)
  })

  it('shows the monitoring controls without a disclosure, and says the catalogue arrives whatever the mode is', () => {
    // The most common confusion in support: monitor mode reads as "which
    // books get added". It is not. Every book is catalogued either way and
    // the mode only decides what gets searched for.
    renderConfirm(author({}))
    expect(screen.getByLabelText('Monitor mode')).toBeInTheDocument()
    expect(screen.queryByText('Customize monitoring')).toBeNull()
    expect(screen.getByLabelText('Monitor newly discovered books')).toBeInTheDocument()
    expect(screen.getByText(/whole catalogue is added either way/i)).toBeInTheDocument()
  })

  it('seeds the controls from the install defaults and lets the backend apply them when unchanged', async () => {
    renderConfirm(author({}), { defaults: { ...bareDefaults, mediaType: 'audiobook', monitorMode: 'latest', monitorLatestCount: 5 } })
    expect(screen.getByLabelText('Monitor mode')).toHaveValue('latest')
    expect(screen.getByLabelText('Latest book count')).toHaveValue(5)
    expect(screen.getByLabelText('Media type')).toHaveValue('audiobook')

    fireEvent.click(screen.getByRole('button', { name: 'Add author' }))
    await waitFor(() => expect(api.addAuthor).toHaveBeenCalledTimes(1))
    const callArg = vi.mocked(api.addAuthor).mock.calls[0][0]
    expect(callArg.mediaType).toBe('audiobook')
    expect('monitorMode' in callArg).toBe(false)
    expect('monitorLatestCount' in callArg).toBe(false)
  })

  it('keeps Add disabled until the defaults arrive, then posts the seeded profile and root folder', async () => {
    // A click before the defaults land used to post a null profile and a null
    // root folder; the button now waits for them.
    const onBack = vi.fn()
    const { rerender } = render(<AddAuthorConfirm author={author({})} defaults={null} primaryProvider={null} onBack={onBack} onClose={vi.fn()} onAdded={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Add author' })).toBeDisabled()
    // Back stays available while loading.
    expect(screen.getByRole('button', { name: 'Back to results' })).toBeEnabled()

    const loaded: AuthorAddDefaults = {
      ...bareDefaults,
      profiles: [{ id: 3, name: 'Standard' }, { id: 4, name: 'German' }] as never,
      rootFolders: [{ id: 7, path: '/downloads', freeSpace: 0, createdAt: '' }, { id: 9, path: '/books', freeSpace: 0, createdAt: '' }],
      rootFolderId: 9,
    }
    rerender(<AddAuthorConfirm author={author({})} defaults={loaded} primaryProvider={null} onBack={onBack} onClose={vi.fn()} onAdded={vi.fn()} />)
    const add = screen.getByRole('button', { name: 'Add author' })
    await waitFor(() => expect(add).toBeEnabled())
    expect(screen.getByLabelText('Root folder')).toHaveValue('9')
    fireEvent.click(add)
    await waitFor(() => expect(api.addAuthor).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.addAuthor).mock.calls[0][0]).toEqual(expect.objectContaining({ metadataProfileId: 3, rootFolderId: 9 }))
  })

  it('states the predicted outcome, with the count, and follows the mode', () => {
    renderConfirm(author({ statistics: { bookCount: 87, availableBookCount: 0, wantedBookCount: 0 } }))
    const outcome = screen.getByTestId('add-author-outcome')
    expect(outcome).toHaveTextContent('Adds up to 87 books and searches for all of them.')
    fireEvent.change(screen.getByLabelText('Monitor mode'), { target: { value: 'none' } })
    expect(outcome).toHaveTextContent('Adds up to 87 books and searches for none of them.')
  })

  it('sends monitor overrides and monitorNewItems only when the controls are changed', async () => {
    renderConfirm(author({}))
    fireEvent.change(screen.getByLabelText('Monitor mode'), { target: { value: 'latest' } })
    fireEvent.change(screen.getByLabelText('Latest book count'), { target: { value: '3' } })
    fireEvent.change(screen.getByLabelText('Monitor newly discovered books'), { target: { value: 'none' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add author' }))
    await waitFor(() => expect(api.addAuthor).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.addAuthor).mock.calls[0][0]).toMatchObject({
      monitorMode: 'latest',
      monitorLatestCount: 3,
      monitorNewItems: 'none',
    })
  })

  it('shows duplicate author conflicts inline with existing-author actions', async () => {
    vi.mocked(api.addAuthor).mockRejectedValue(Object.assign(new Error('author already exists'), {
      status: 409,
      body: { error: 'author already exists', canonicalAuthorId: 60 },
    }))
    const { onAdded } = renderConfirm(author({ authorName: 'Emilia Jae' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add author' }))

    await waitFor(() => expect(screen.getByText('author already exists')).toBeInTheDocument())
    expect(screen.getByRole('link', { name: 'Open existing author' })).toHaveAttribute('href', '/author/60')
    expect(screen.getByRole('link', { name: 'Find metadata' })).toHaveAttribute('href', '/author/60?linkMetadata=1')
    expect(onAdded).not.toHaveBeenCalled()
  })

  it('shows a plain failure without the existing-author links', async () => {
    vi.mocked(api.addAuthor).mockRejectedValue(new Error('metadata provider unavailable'))
    renderConfirm(author({}))
    fireEvent.click(screen.getByRole('button', { name: 'Add author' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('metadata provider unavailable'))
    expect(screen.queryByRole('link', { name: 'Open existing author' })).not.toBeInTheDocument()
  })

  it('warns when auto-grab is on and no download client is configured', async () => {
    vi.mocked(api.listDownloadClients).mockResolvedValue([])
    renderConfirm(author({}))
    expect(await screen.findByText(/no download client is configured/)).toBeInTheDocument()
  })

  it('flags a record from another provider than the configured primary', () => {
    renderConfirm(author({ metadataProvider: 'openlibrary' }), { primaryProvider: 'hardcover' })
    const notice = screen.getByRole('alert')
    expect(notice.textContent).toContain('OpenLibrary')
    expect(notice.textContent).toContain('Hardcover')
  })

  it('wires Back and Cancel', () => {
    const { onBack, onClose } = renderConfirm(author({}))
    fireEvent.click(screen.getByRole('button', { name: 'Back to results' }))
    expect(onBack).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  // #2166: the picker used to seed from rfs[0], and that value is posted as an
  // explicit per-author rootFolderId, which the scanner resolves ahead of
  // library.defaultRootFolderId, so the install default was unreachable for
  // every author added here.
  describe('loadAuthorAddDefaults root folder seeding (#2166)', () => {
    const roots = [
      { id: 7, path: '/downloads', freeSpace: 0, createdAt: '2026-08-01T00:00:00Z' },
      { id: 9, path: '/books', freeSpace: 0, createdAt: '2026-08-01T00:00:00Z' },
    ]

    function settingsWithDefaultRoot(value: string | null) {
      return vi.fn().mockImplementation((key: string) => {
        if (key === 'library.defaultRootFolderId') {
          return value === null
            ? Promise.reject(new Error('HTTP 404'))
            : Promise.resolve({ key, value })
        }
        return Promise.reject(new Error('HTTP 404'))
      })
    }

    it('picks the configured default root folder, not the first in the list', async () => {
      vi.mocked(api.listRootFolders).mockResolvedValue(roots)
      vi.mocked(api.getSetting).mockImplementation(settingsWithDefaultRoot('9'))
      expect(await loadAuthorAddDefaults()).toEqual(expect.objectContaining({ rootFolderId: 9, rootFolders: roots }))
    })

    it('falls back to the first root folder when no default is set', async () => {
      vi.mocked(api.listRootFolders).mockResolvedValue(roots)
      vi.mocked(api.getSetting).mockImplementation(settingsWithDefaultRoot(null))
      expect(await loadAuthorAddDefaults()).toEqual(expect.objectContaining({ rootFolderId: 7 }))
    })

    it('falls back to the first root folder when the default names a deleted folder', async () => {
      vi.mocked(api.listRootFolders).mockResolvedValue(roots)
      vi.mocked(api.getSetting).mockImplementation(settingsWithDefaultRoot('404'))
      expect(await loadAuthorAddDefaults()).toEqual(expect.objectContaining({ rootFolderId: 7 }))
    })

    it('reads the install monitor and media defaults, and degrades to the built-in ones', async () => {
      vi.mocked(api.getSetting).mockImplementation(async (key: string) => {
        if (key === 'default.media_type') return { key, value: 'audiobook' }
        if (key === 'author.default_monitor_mode') return { key, value: 'latest' }
        if (key === 'author.default_monitor_latest_count') return { key, value: '5' }
        throw new Error('HTTP 404')
      })
      expect(await loadAuthorAddDefaults()).toEqual(expect.objectContaining({ mediaType: 'audiobook', monitorMode: 'latest', monitorLatestCount: 5, rootFolderId: null }))

      vi.mocked(api.getSetting).mockRejectedValue(new Error('HTTP 404'))
      vi.mocked(api.listMetadataProfiles).mockRejectedValue(new Error('HTTP 500'))
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
      expect(await loadAuthorAddDefaults()).toEqual({ profiles: [], rootFolders: [], rootFolderId: null, mediaType: 'ebook', monitorMode: 'all', monitorLatestCount: 1 })
      consoleError.mockRestore()
    })
  })
})

describe('AddAuthorConfirm while the defaults are loading', () => {
  it('disables the monitoring controls and says why, then enables them once the defaults land', () => {
    const props = { author: author({}), primaryProvider: null, onBack: vi.fn(), onClose: vi.fn(), onAdded: vi.fn() }
    const { rerender } = render(<AddAuthorConfirm {...props} defaults={null} />)
    expect(screen.getByLabelText('Media type')).toBeDisabled()
    expect(screen.getByLabelText('Monitor mode')).toBeDisabled()
    expect(screen.getByRole('status')).toHaveTextContent('addToLibrary.author.loadingDefaults')
    expect(screen.getByRole('button', { name: 'Add author' })).toBeDisabled()

    rerender(<AddAuthorConfirm {...props} defaults={{ ...bareDefaults, mediaType: 'audiobook' }} />)
    expect(screen.getByLabelText('Media type')).toBeEnabled()
    expect(screen.getByLabelText('Media type')).toHaveValue('audiobook')
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add author' })).toBeEnabled()
  })
})
