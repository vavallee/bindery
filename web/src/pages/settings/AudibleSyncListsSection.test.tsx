import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'

vi.mock('../../api/client', () => ({
  api: {
    listImportLists: vi.fn(),
    audibleSyncConfig: vi.fn(),
    audibleSyncLists: vi.fn(),
    audibleSyncListSyncStatus: vi.fn(),
    listUsers: vi.fn(),
    addImportList: vi.fn(),
    updateImportList: vi.fn(),
    deleteImportList: vi.fn(),
    syncImportList: vi.fn(),
  },
}))

import { api, AudibleSyncList, ImportList } from '../../api/client'
import AudibleSyncListsSection from './AudibleSyncListsSection'
import '../../i18n'

const mocked = api as unknown as Record<keyof typeof api, ReturnType<typeof vi.fn>>

const offered: AudibleSyncList[] = [
  { accountId: 1, accountName: 'Main', countryCode: 'uk', needsLogin: false, source: 'wishlist', booksCount: 12 },
  { accountId: 1, accountName: 'Main', countryCode: 'uk', needsLogin: false, source: 'unavailable', booksCount: 3 },
  { accountId: 2, accountName: 'Spare', countryCode: 'us', needsLogin: true, source: 'wishlist', booksCount: -1 },
]

const savedWishlist: ImportList = {
  id: 7, name: 'Main: wishlist', type: 'audiblesync', url: 'wishlist', apiKey: '', apiKeyConfigured: false,
  account: '1', monitorNew: true, autoAdd: true, enabled: true, mediaType: 'audiobook',
  skipOwned: true, removeOnImport: false, ownerUserId: null, createdAt: '', updatedAt: '',
}

const idle = { running: false, startedAt: '', stats: { total: 0, processed: 0, imported: 0, skipped: 0, failed: 0, owned: 0, unresolved: 0, removed: 0, released: 0 } }

beforeEach(() => {
  vi.resetAllMocks()
  mocked.listImportLists.mockResolvedValue([savedWishlist, { ...savedWishlist, id: 8, type: 'hardcover', name: 'Want to Read', url: 'want-to-read', account: 'reader' }])
  mocked.audibleSyncConfig.mockResolvedValue({ enabled: true, baseUrl: 'http://audible-sync:8080', apiTokenConfigured: true })
  mocked.audibleSyncLists.mockResolvedValue(offered)
  mocked.audibleSyncListSyncStatus.mockResolvedValue(idle)
  mocked.listUsers.mockResolvedValue([])
})

describe('AudibleSyncListsSection', () => {
  it('offers every list of every account, ticking the saved one', async () => {
    render(<AudibleSyncListsSection />)
    const wish = await screen.findByLabelText('Watch Main: wishlist')
    expect(wish).toBeChecked()
    expect(screen.getByLabelText('Watch Main: unavailable books')).not.toBeChecked()
    expect(screen.getByLabelText('Watch Spare: wishlist')).not.toBeChecked()
    expect(screen.getByText('Needs signing in again in Audible Sync')).toBeInTheDocument()
    expect(screen.queryByText('Want to Read')).toBeNull()
  })

  it('saves a list the first time it is ticked, for its account and source', async () => {
    mocked.addImportList.mockImplementation(async (data: Partial<ImportList>) => ({ ...savedWishlist, ...data, id: 9 }))
    render(<AudibleSyncListsSection />)
    fireEvent.click(await screen.findByLabelText('Watch Main: unavailable books'))
    await waitFor(() => expect(mocked.addImportList).toHaveBeenCalled())
    expect(mocked.addImportList.mock.calls[0][0]).toMatchObject({
      name: 'Main: unavailable books', type: 'audiblesync', url: 'unavailable', account: '1', skipOwned: false, removeOnImport: false,
    })
    expect(await screen.findByLabelText('Watch Main: unavailable books')).toBeChecked()
  })

  it('pauses a saved list instead of saving it again', async () => {
    mocked.updateImportList.mockImplementation(async (id: number, patch: Partial<ImportList>) => ({ ...savedWishlist, id, ...patch }))
    render(<AudibleSyncListsSection />)
    fireEvent.click(await screen.findByLabelText('Watch Main: wishlist'))
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(7, { enabled: false }))
    expect(mocked.addImportList).not.toHaveBeenCalled()
  })

  it('shows the wishlist options only on a wishlist', async () => {
    mocked.listImportLists.mockResolvedValue([
      savedWishlist,
      { ...savedWishlist, id: 9, name: 'Main: unavailable books', url: 'unavailable', skipOwned: false },
    ])
    render(<AudibleSyncListsSection />)
    await screen.findByLabelText('Watch Main: unavailable books')
    expect(screen.getAllByText('Skip owned books')).toHaveLength(1)
    expect(screen.getAllByText('Remove from wishlist once imported')).toHaveLength(1)
    expect(screen.getAllByText('Download books from this list')).toHaveLength(2)
  })

  it('points to the settings while Audible Sync is not connected', async () => {
    mocked.audibleSyncConfig.mockResolvedValue({ enabled: false, baseUrl: '', apiTokenConfigured: false })
    mocked.listImportLists.mockResolvedValue([])
    const onNavigate = vi.fn()
    render(<AudibleSyncListsSection onNavigate={onNavigate} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Open Audible Sync settings →' }))
    expect(onNavigate).toHaveBeenCalledWith('audiblesync')
    expect(mocked.audibleSyncLists).not.toHaveBeenCalled()
  })

  it('shows the outcome of a finished sync', async () => {
    mocked.syncImportList.mockResolvedValue({ ...idle, listId: 7, stats: { ...idle.stats, imported: 3, owned: 2, released: 1 } })
    render(<AudibleSyncListsSection />)
    fireEvent.click(await screen.findByRole('button', { name: 'Sync now' }))
    expect(await screen.findByText(/3 added, 0 already tracked, 2 owned on Audible, .* 1 downloadable again/)).toBeInTheDocument()
  })

  it('offers the owner only when Bindery has more than one user', async () => {
    mocked.listUsers.mockResolvedValue([{ id: 1, username: 'admin' }])
    const { unmount } = render(<AudibleSyncListsSection />)
    await screen.findByLabelText('Watch Main: wishlist')
    await waitFor(() => expect(mocked.listUsers).toHaveBeenCalled())
    expect(screen.queryByLabelText('Owner')).toBeNull()
    unmount()

    mocked.listUsers.mockResolvedValue([{ id: 1, username: 'admin' }, { id: 2, username: 'reader' }])
    render(<AudibleSyncListsSection />)
    expect(await screen.findByLabelText('Owner')).toBeInTheDocument()
  })

  it('keeps the owner of a list that has one, even with one user', async () => {
    mocked.listUsers.mockResolvedValue([{ id: 1, username: 'admin' }])
    mocked.listImportLists.mockResolvedValue([{ ...savedWishlist, ownerUserId: 1 }])
    render(<AudibleSyncListsSection />)
    expect(await screen.findByLabelText('Owner')).toHaveValue('1')
  })
})
