import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import BlocklistTab from './BlocklistTab'
import { api } from '../../api/client'
import { acceptConfirm, cancelConfirm } from '../../test-utils'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) => {
      const count = opts && typeof opts === 'object' && 'count' in opts ? (opts as { count: number }).count : undefined
      return count === undefined ? key : `${key}:${count}`
    },
    i18n: { changeLanguage: vi.fn() },
  }),
}))

vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listBlocklist: vi.fn(),
      deleteBlocklistEntry: vi.fn(),
      bulkDeleteBlocklist: vi.fn(),
    },
  }
})

const entries = [
  { id: 1, guid: 'guid-one', title: 'First Release', reason: 'bad quality', createdAt: '2026-01-02T10:00:00Z' },
  { id: 2, guid: '', title: 'Second Release', reason: '', createdAt: '2026-01-03T10:00:00Z' },
]

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listBlocklist).mockResolvedValue(entries)
  vi.mocked(api.deleteBlocklistEntry).mockResolvedValue(undefined)
  vi.mocked(api.bulkDeleteBlocklist).mockResolvedValue(undefined)
})

describe('BlocklistTab', () => {
  it('shows the loading state, then the empty state when nothing is blocklisted', async () => {
    let resolve: (v: typeof entries) => void = () => {}
    vi.mocked(api.listBlocklist).mockReturnValue(new Promise(r => { resolve = r }))
    render(<BlocklistTab />)
    expect(screen.getByText('common.loading')).toBeInTheDocument()
    resolve([])
    expect(await screen.findByText('blocklist.empty')).toBeInTheDocument()
    expect(screen.getByText('blocklist.entries:0')).toBeInTheDocument()
  })

  it('falls back to the empty state when the list request fails', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.listBlocklist).mockRejectedValue(new Error('boom'))
    render(<BlocklistTab />)
    expect(await screen.findByText('blocklist.empty')).toBeInTheDocument()
    expect(err).toHaveBeenCalled()
    err.mockRestore()
  })

  it('renders entries with guid and an Unknown reason fallback', async () => {
    render(<BlocklistTab />)
    expect((await screen.findAllByText('First Release')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('guid-one').length).toBe(2)
    expect(screen.getAllByText('Unknown').length).toBe(2)
    expect(screen.getAllByText('bad quality').length).toBe(2)
    expect(screen.getByText('blocklist.entries:2')).toBeInTheDocument()
  })

  it('deletes a single entry and removes it from the list', async () => {
    render(<BlocklistTab />)
    await screen.findAllByText('First Release')
    fireEvent.click(screen.getAllByRole('button', { name: 'common.delete' })[0])
    await waitFor(() => expect(api.deleteBlocklistEntry).toHaveBeenCalledWith(1))
    await waitFor(() => expect(screen.queryByText('First Release')).not.toBeInTheDocument())
    expect(screen.getByText('blocklist.entries:1')).toBeInTheDocument()
  })

  it('still removes the row locally when the delete call fails', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.deleteBlocklistEntry).mockRejectedValue(new Error('nope'))
    render(<BlocklistTab />)
    await screen.findAllByText('Second Release')
    // Second row's delete button in the desktop table.
    fireEvent.click(screen.getAllByRole('button', { name: 'common.delete' })[1])
    await waitFor(() => expect(screen.queryByText('Second Release')).not.toBeInTheDocument())
    expect(err).toHaveBeenCalled()
    err.mockRestore()
  })

  it('selects all, cancels the bulk confirm, then confirms and bulk deletes', async () => {
    render(<BlocklistTab />)
    await screen.findAllByText('First Release')
    const checkboxes = screen.getAllByRole('checkbox')
    // [0] desktop select-all, [1..2] desktop rows, [3] mobile select-all, [4..5] mobile rows
    fireEvent.click(checkboxes[0])
    expect(checkboxes[0]).toBeChecked()
    expect(checkboxes[3]).toBeChecked()

    fireEvent.click(screen.getByRole('button', { name: 'blocklist.deleteSelected:2' }))
    await cancelConfirm()
    expect(api.bulkDeleteBlocklist).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'blocklist.deleteSelected:2' }))
    await acceptConfirm()
    await waitFor(() => expect(api.bulkDeleteBlocklist).toHaveBeenCalledWith([1, 2]))
    expect(await screen.findByText('blocklist.empty')).toBeInTheDocument()
  })

  it('toggles individual rows and select-all off again', async () => {
    render(<BlocklistTab />)
    await screen.findAllByText('First Release')
    const checkboxes = screen.getAllByRole('checkbox')
    fireEvent.click(checkboxes[1])
    expect(screen.getByRole('button', { name: 'blocklist.deleteSelected:1' })).toBeInTheDocument()
    fireEvent.click(checkboxes[5])
    expect(screen.getByRole('button', { name: 'blocklist.deleteSelected:2' })).toBeInTheDocument()
    expect(checkboxes[0]).toBeChecked()
    fireEvent.click(checkboxes[3])
    expect(screen.queryByRole('button', { name: /blocklist.deleteSelected/ })).not.toBeInTheDocument()
    fireEvent.click(checkboxes[4])
    fireEvent.click(checkboxes[4])
    expect(screen.queryByRole('button', { name: /blocklist.deleteSelected/ })).not.toBeInTheDocument()
  })

  it('keeps the entries when bulk delete fails', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.bulkDeleteBlocklist).mockRejectedValue(new Error('fail'))
    render(<BlocklistTab />)
    await screen.findAllByText('First Release')
    fireEvent.click(screen.getAllByRole('checkbox')[1])
    fireEvent.click(screen.getByRole('button', { name: 'blocklist.deleteSelected:1' }))
    await acceptConfirm()
    await waitFor(() => expect(api.bulkDeleteBlocklist).toHaveBeenCalledWith([1]))
    await waitFor(() => expect(err).toHaveBeenCalled())
    expect(screen.getAllByText('First Release').length).toBeGreaterThan(0)
    expect(screen.getByRole('button', { name: 'blocklist.deleteSelected:1' })).toBeEnabled()
    err.mockRestore()
  })
})
