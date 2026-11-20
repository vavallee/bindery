import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import EditAuthorModal from './EditAuthorModal'
import { api } from '../api/client'
import type { Author, MetadataProfile, QualityProfile, RootFolder } from '../api/client'
import { expectFieldValuesWired } from '../test/fieldValue'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown, opts?: Record<string, string>) => {
      if (typeof fallback !== 'string') return key
      return fallback.replace(/\{\{(\w+)\}\}/g, (_, k) => opts?.[k] ?? '')
    },
  }),
}))
vi.mock('../api/client', () => ({
  api: {
    listQualityProfiles: vi.fn(),
    listMetadataProfiles: vi.fn(),
    listRootFolders: vi.fn(),
    listAuthorSeries: vi.fn(),
    updateAuthor: vi.fn(),
  },
}))

const longPath = '/mnt/storage/media/library/books/very/deeply/nested/folder/structure/ebooks'

const author = {
  id: 42, authorName: 'Brandon Sanderson', qualityProfileId: 2, metadataProfileId: 10, rootFolderId: 100,
  monitorMode: 'future', monitorNewItems: 'none',
} as unknown as Author

const quality = [
  { id: 1, name: 'Any', items: [] }, { id: 2, name: 'EPUB Only', items: [] },
] as unknown as QualityProfile[]
const metadata = [{ id: 10, name: 'Standard' }] as unknown as MetadataProfile[]
const roots = [{ id: 100, path: longPath }, { id: 101, path: '/library/audiobooks' }] as unknown as RootFolder[]

const subOf = (el: HTMLElement) => document.getElementById(el.getAttribute('aria-describedby')!)!

beforeEach(() => {
  vi.mocked(api.listQualityProfiles).mockResolvedValue(quality)
  vi.mocked(api.listMetadataProfiles).mockResolvedValue(metadata)
  vi.mocked(api.listRootFolders).mockResolvedValue(roots)
  vi.mocked(api.listAuthorSeries).mockResolvedValue([])
})

describe('EditAuthorModal current value subtitles (#3989)', () => {
  it('shows what each field holds, with names and not ids', async () => {
    render(<EditAuthorModal author={author} onClose={vi.fn()} onSaved={vi.fn()} />)
    await screen.findByRole('option', { name: 'Any' })
    const [q, m, root, audiobookRoot, mode, newItems] = screen.getAllByRole('combobox') as HTMLSelectElement[]
    expect(subOf(q)).toHaveTextContent('Current: EPUB Only')
    expect(subOf(m)).toHaveTextContent('Current: Standard')
    expect(subOf(root)).toHaveTextContent(`Current: ${longPath}`)
    expect(subOf(audiobookRoot)).toHaveTextContent('Current: Use global audiobook folder')
    expect(subOf(mode)).toHaveTextContent('Current: Future books only (grows on refresh)')
    expect(subOf(newItems)).toHaveTextContent('Current: Don’t add them')
  })

  it('truncates a long path to one line with the whole path in the title', async () => {
    render(<EditAuthorModal author={author} onClose={vi.fn()} onSaved={vi.fn()} />)
    await screen.findByRole('option', { name: 'Any' })
    const root = screen.getAllByRole('combobox')[2] as HTMLSelectElement
    expect(subOf(root)).toHaveClass('truncate')
    expect(subOf(root)).toHaveAttribute('title', `Current: ${longPath}`)
  })

  it('follows the control as it changes', async () => {
    render(<EditAuthorModal author={author} onClose={vi.fn()} onSaved={vi.fn()} />)
    await screen.findByRole('option', { name: 'Any' })
    const [q, , , audiobookRoot, mode] = screen.getAllByRole('combobox') as HTMLSelectElement[]
    fireEvent.change(q, { target: { value: '' } })
    expect(subOf(q)).toHaveTextContent('Current: common.qualityProfileNone')
    fireEvent.change(audiobookRoot, { target: { value: '101' } })
    expect(subOf(audiobookRoot)).toHaveTextContent('Current: /library/audiobooks')
    fireEvent.change(mode, { target: { value: 'latest' } })
    expect(subOf(mode)).toHaveTextContent('Current: Latest only')
    expect(document.querySelector('[id$="-7-value"]')).toHaveTextContent('Current: 1')
  })

  it('wires every subtitle in the dialog to a control', async () => {
    render(<EditAuthorModal author={author} onClose={vi.fn()} onSaved={vi.fn()} />)
    await screen.findByRole('option', { name: 'Any' })
    expectFieldValuesWired(document.body)
  })
})
