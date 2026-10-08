import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router'
import WantedPage from './WantedPage'
import { api } from '../api/client'
import type { Book } from '../api/client'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return { ...actual, api: { ...actual.api, listWanted: vi.fn() } }
})

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) =>
      key === 'wanted.changeFormat' ? `Change format for ${String(options?.title)}` : key,
  }),
}))

const book = {
  id: 1,
  foreignBookId: 'b1',
  authorId: 2,
  title: 'A Book With A Long Enough Title',
  description: '',
  imageUrl: '',
  genres: [],
  monitored: true,
  status: 'wanted',
  filePath: '',
  mediaType: 'ebook',
  ebookFilePath: '',
  audiobookFilePath: '',
  excluded: false,
} as Book

// At 375px the five column row left the title column about 13px wide: the
// fixed format and action columns took the rest. Below sm the row keeps
// checkbox, cover and title on the first line and puts format and actions on
// a second line under the title.
describe('WantedPage on a phone', () => {
  beforeEach(() => {
    vi.mocked(api.listWanted).mockResolvedValue([book])
  })

  it('uses a three column row below sm and the five column row from sm', async () => {
    render(<MemoryRouter><WantedPage /></MemoryRouter>)
    const select = await screen.findByRole('combobox', { name: `Change format for ${book.title}` })
    const row = select.closest('div.grid') as HTMLElement
    expect(row.className).toContain('grid-cols-[1.5rem_2rem_minmax(0,1fr)]')
    expect(row.className).toContain('sm:grid-cols-[1.5rem_2rem_1fr_6rem_8.5rem]')
  })

  it('puts format and actions on their own line under the title below sm', async () => {
    render(<MemoryRouter><WantedPage /></MemoryRouter>)
    const select = await screen.findByRole('combobox', { name: `Change format for ${book.title}` })
    const search = screen.getByRole('button', { name: 'common.search' })
    // One wrapper holds both, spans the title and cover columns on a phone,
    // and dissolves into the grid (display: contents) from sm.
    const line = select.closest('[data-testid="wanted-row-controls"]') as HTMLElement
    expect(line).not.toBeNull()
    expect(line).toContainElement(search)
    expect(line.className).toContain('col-span-2')
    expect(line.className).toContain('sm:contents')
    // The list clips to its rounded border, so on the narrowest phones the
    // line wraps rather than pushing the buttons past the clip.
    expect(line.className).toContain('flex-wrap')
  })

  it('hides the format and actions column headings below sm', async () => {
    render(<MemoryRouter><WantedPage /></MemoryRouter>)
    await screen.findByRole('combobox', { name: `Change format for ${book.title}` })
    expect(screen.getByText('wanted.colFormat').className).toContain('hidden sm:block')
    expect(screen.getByText('wanted.colActions').className).toContain('hidden sm:block')
  })
})
