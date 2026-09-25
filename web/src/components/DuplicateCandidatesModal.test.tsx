import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, DuplicateCandidateMember, DuplicateCandidates, DuplicateRule } from '../api/client'
import DuplicateCandidatesModal from './DuplicateCandidatesModal'

vi.mock('react-i18next', () => {
  const t = (key: string, fallback?: string | Record<string, unknown>) => {
      if (typeof fallback === 'string') return fallback
      const template = String(fallback?.defaultValue ?? key)
      return Object.entries(fallback ?? {}).reduce(
        (text, [name, value]) => name === 'defaultValue' ? text : text.replaceAll(`{{${name}}}`, String(value)),
        template,
      )
  }
  return { useTranslation: () => ({ t }) }
})

vi.mock('../api/client', () => ({
  api: {
    listAuthorDuplicateCandidates: vi.fn(),
    toggleExcluded: vi.fn(),
  },
}))

function book(id: number, title: string, excluded = false, rules: DuplicateRule[] = []): DuplicateCandidateMember {
  return {
    id,
    foreignBookId: `OL${id}W`,
    authorId: 7,
    title,
    description: '',
    imageUrl: '',
    genres: [],
    monitored: true,
    status: 'wanted',
    filePath: '',
    mediaType: 'ebook',
    ebookFilePath: '',
    audiobookFilePath: '',
    excluded,
    rules,
  }
}

const result: DuplicateCandidates = {
  authorId: 7,
  count: 2,
  groups: [
    {
      key: 'dune',
      rules: ['alnum-equal'],
      books: [book(11, 'Dune'), book(12, 'Dune')],
    },
    {
      key: 'themartian',
      rules: ['article-strip', 'substring'],
      books: [
        book(13, 'The Martian', false, ['article-strip']),
        book(14, 'Martian', true, ['article-strip']),
      ],
    },
  ],
}

describe('DuplicateCandidatesModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listAuthorDuplicateCandidates).mockResolvedValue(result)
    vi.mocked(api.toggleExcluded).mockResolvedValue(book(14, 'Martian', true))
  })

  it('fetches the report and shows each group with its rules', async () => {
    render(<DuplicateCandidatesModal authorId={7} authorName="Andy Weir" onClose={() => {}} />)

    // Two books share the title "Dune".
    expect(await screen.findAllByText('Dune')).toHaveLength(2)
    expect(api.listAuthorDuplicateCandidates).toHaveBeenCalledWith(7)
    expect(screen.getByText('Identical once punctuation, case, and diacritics are ignored')).toBeInTheDocument()
    // Once in the martian group header, once per martian member.
    expect(screen.getAllByText('Same title with a leading article (The, A, An…) dropped')).toHaveLength(3)
    // The excluded member is rendered with the Excluded badge.
    expect(screen.getByText('Excluded')).toBeInTheDocument()
    // Both groups have two members, so the "2 row(s)" label appears twice.
    expect(screen.getAllByText('2 row(s)')).toHaveLength(2)
  })

  it('shows the empty state when there are no duplicate groups', async () => {
    vi.mocked(api.listAuthorDuplicateCandidates).mockResolvedValue({ authorId: 7, count: 0, groups: [] })
    render(<DuplicateCandidatesModal authorId={7} authorName="Andy Weir" onClose={() => {}} />)

    expect(await screen.findByText('No duplicate titles found.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Exclude' })).not.toBeInTheDocument()
  })

  it('excludes a row, re-fetches the report, and notifies the page', async () => {
    const onChanged = vi.fn()
    vi.mocked(api.listAuthorDuplicateCandidates)
      .mockResolvedValueOnce(result)
      .mockResolvedValueOnce({
        ...result,
        count: 1,
        groups: [{ key: 'dune', rules: ['alnum-equal'], books: [book(11, 'Dune'), book(12, 'Dune')] }],
      })
    render(<DuplicateCandidatesModal authorId={7} authorName="Andy Weir" onClose={() => {}} onChanged={onChanged} />)

    const excludeButtons = await screen.findAllByRole('button', { name: 'Exclude' })
    fireEvent.click(excludeButtons[0])

    await waitFor(() => {
      expect(api.toggleExcluded).toHaveBeenCalledWith(11)
      expect(api.listAuthorDuplicateCandidates).toHaveBeenCalledTimes(2)
      expect(onChanged).toHaveBeenCalledTimes(1)
    })
    // After the re-fetch the martian group is gone.
    expect(screen.queryByText('The Martian')).not.toBeInTheDocument()
  })

  it('offers Include for already-excluded rows', async () => {
    render(<DuplicateCandidatesModal authorId={7} authorName="Andy Weir" onClose={() => {}} />)

    const include = await screen.findByRole('button', { name: 'Include' })
    fireEvent.click(include)

    await waitFor(() => expect(api.toggleExcluded).toHaveBeenCalledWith(14))
  })

  it('keeps an in-flight row disabled while another row is toggling', async () => {
    // Two toggles in flight at once: with a single shared busy id, starting
    // the second used to re-enable the first row's button, so a second click
    // could fire an overlapping flip that cancels the first out silently.
    const pending: Array<(value: DuplicateCandidateMember) => void> = []
    vi.mocked(api.toggleExcluded).mockImplementation(
      () => new Promise<DuplicateCandidateMember>(resolve => { pending.push(resolve) }),
    )
    render(<DuplicateCandidatesModal authorId={7} authorName="Andy Weir" onClose={() => {}} />)

    const [first, second] = await screen.findAllByRole('button', { name: 'Exclude' })
    fireEvent.click(first)
    expect(first).toBeDisabled()
    fireEvent.click(second)
    expect(first).toBeDisabled()
    expect(second).toBeDisabled()

    pending.forEach(resolve => resolve(book(11, 'Dune', true)))
    await waitFor(() => {
      const buttons = screen.getAllByRole('button', { name: 'Exclude' })
      expect(buttons[0]).not.toBeDisabled()
      expect(buttons[1]).not.toBeDisabled()
    })
    expect(api.toggleExcluded).toHaveBeenCalledTimes(2)
  })

  it('shows the load error when the request fails', async () => {
    vi.mocked(api.listAuthorDuplicateCandidates).mockRejectedValue(new Error('boom'))
    render(<DuplicateCandidatesModal authorId={7} authorName="Andy Weir" onClose={() => {}} />)

    expect(await screen.findByText('boom')).toBeInTheDocument()
  })
})
