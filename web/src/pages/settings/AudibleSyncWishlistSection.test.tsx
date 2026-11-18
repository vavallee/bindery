import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'

vi.mock('../../api/client', () => ({
  api: {
    listImportLists: vi.fn(),
    audibleSyncConfig: vi.fn(),
    audibleSyncAccounts: vi.fn(),
    audibleSyncListSyncStatus: vi.fn(),
    listUsers: vi.fn(),
    addImportList: vi.fn(),
    updateImportList: vi.fn(),
    deleteImportList: vi.fn(),
    syncImportList: vi.fn(),
  },
}))

import { api, ImportList } from '../../api/client'
import AudibleSyncWishlistSection from './AudibleSyncWishlistSection'
import '../../i18n'

const mocked = api as unknown as Record<keyof typeof api, ReturnType<typeof vi.fn>>

const wishlist: ImportList = {
  id: 7, name: 'Audible wishlist', type: 'audiblesync', url: '', apiKey: '', apiKeyConfigured: false,
  account: '', monitorNew: true, autoAdd: true, enabled: true, mediaType: 'audiobook',
  skipOwned: true, removeOnImport: false, ownerUserId: null, createdAt: '', updatedAt: '',
}

const idle = { running: false, startedAt: '', stats: { total: 0, processed: 0, imported: 0, skipped: 0, failed: 0, owned: 0, unresolved: 0, removed: 0 } }

beforeEach(() => {
  vi.resetAllMocks()
  mocked.listImportLists.mockResolvedValue([wishlist, { ...wishlist, id: 8, type: 'hardcover', name: 'Want to Read' }])
  mocked.audibleSyncConfig.mockResolvedValue({ enabled: true, baseUrl: 'http://audible-sync:8080', apiTokenConfigured: true, accountId: 0 })
  mocked.audibleSyncAccounts.mockResolvedValue([{ id: 1, name: 'Main', countryCode: 'uk', enabled: true, needsLogin: false }])
  mocked.audibleSyncListSyncStatus.mockResolvedValue(idle)
  mocked.listUsers.mockResolvedValue([])
})

describe('AudibleSyncWishlistSection', () => {
  it('lists only Audible Sync lists, and offers the accounts', async () => {
    render(<AudibleSyncWishlistSection />)
    expect(await screen.findByText('Audible wishlist')).toBeInTheDocument()
    expect(screen.queryByText('Want to Read')).toBeNull()
    expect(await screen.findByRole('option', { name: 'Main (UK)' })).toBeInTheDocument()
  })

  it('points to the settings and disables adding while Audible Sync is not connected', async () => {
    mocked.audibleSyncConfig.mockResolvedValue({ enabled: false, baseUrl: '', apiTokenConfigured: false, accountId: 0 })
    mocked.listImportLists.mockResolvedValue([])
    const onNavigate = vi.fn()
    render(<AudibleSyncWishlistSection onNavigate={onNavigate} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Open Audible Sync settings →' }))
    expect(onNavigate).toHaveBeenCalledWith('audiblesync')
    expect(screen.getByRole('button', { name: 'Add Audible wishlist' })).toBeDisabled()
  })

  it('saves an option and the account', async () => {
    mocked.updateImportList.mockImplementation(async (id: number, patch: Partial<ImportList>) => ({ ...wishlist, id, ...patch }))
    render(<AudibleSyncWishlistSection />)
    fireEvent.click(await screen.findByLabelText('Remove from wishlist once imported'))
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(7, { removeOnImport: true }))
    await screen.findByRole('option', { name: 'Main (UK)' })
    fireEvent.change(screen.getByLabelText('Audible account'), { target: { value: '1' } })
    await waitFor(() => expect(mocked.updateImportList).toHaveBeenCalledWith(7, { account: '1' }))
  })

  it('shows the outcome of a finished sync', async () => {
    mocked.syncImportList.mockResolvedValue({ ...idle, listId: 7, stats: { ...idle.stats, imported: 3, owned: 2, removed: 1 } })
    render(<AudibleSyncWishlistSection />)
    fireEvent.click(await screen.findByRole('button', { name: 'Sync now' }))
    expect(await screen.findByText(/3 added, 0 already tracked, 2 owned on Audible, 0 with an unknown author, 1 removed from the wishlist/)).toBeInTheDocument()
  })
})
