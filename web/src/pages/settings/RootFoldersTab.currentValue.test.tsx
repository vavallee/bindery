import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import RootFoldersTab from './RootFoldersTab'
import { api } from '../../api/client'
import type { RootFolder } from '../../api/client'
import { expectFieldValuesWired } from '../../test/fieldValue'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown, opts?: Record<string, string>) => {
      if (typeof fallback !== 'string') return key
      return fallback.replace(/\{\{(\w+)\}\}/g, (_, k) => opts?.[k] ?? '')
    },
  }),
}))
vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    api: { ...actual.api, listRootFolders: vi.fn(), listSettings: vi.fn(), setSetting: vi.fn() },
  }
})

const longPath = '/mnt/storage/media/library/books/very/deeply/nested/folder/structure/ebooks'
const folders = [
  { id: 1, path: longPath, freeSpace: 0, createdAt: '' },
  { id: 2, path: '/library/audiobooks', freeSpace: 0, createdAt: '' },
] as RootFolder[]

beforeEach(() => {
  vi.mocked(api.listRootFolders).mockResolvedValue(folders)
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.setSetting).mockReset().mockResolvedValue(undefined as never)
})

describe('RootFoldersTab current value subtitles (#3989)', () => {
  it('shows the unset label, then the chosen path truncated with a title', async () => {
    render(<RootFoldersTab />)
    const select = await screen.findByRole('combobox', { name: /settings.rootfolders.defaultLabel/ })
    const sub = () => document.getElementById('default-root-folder-value')!
    expect(sub()).toHaveTextContent('Current: settings.rootfolders.defaultUnset')
    fireEvent.change(select, { target: { value: '1' } })
    await waitFor(() => expect(sub()).toHaveTextContent(`Current: ${longPath}`))
    expect(sub()).toHaveClass('truncate')
    expect(sub()).toHaveAttribute('title', `Current: ${longPath}`)
  })

  it('goes back to the saved default when the save fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.listSettings).mockResolvedValue([{ key: 'library.defaultRootFolderId', value: '2' }] as Awaited<ReturnType<typeof api.listSettings>>)
    vi.mocked(api.setSetting).mockRejectedValue(new Error('boom'))
    render(<RootFoldersTab />)
    const select = await screen.findByRole('combobox', { name: /settings.rootfolders.defaultLabel/ })
    const sub = () => document.getElementById('default-root-folder-value')!
    await waitFor(() => expect(sub()).toHaveTextContent('Current: /library/audiobooks'))
    fireEvent.change(select, { target: { value: '1' } })
    await waitFor(() => expect(api.setSetting).toHaveBeenCalled())
    await waitFor(() => expect(sub()).toHaveTextContent('Current: /library/audiobooks'))
    expect(select).toHaveValue('2')
  })

  it('wires every subtitle on the tab to a control', async () => {
    render(<RootFoldersTab />)
    await screen.findByRole('combobox', { name: /settings.rootfolders.defaultLabel/ })
    expectFieldValuesWired(document.body)
  })
})
