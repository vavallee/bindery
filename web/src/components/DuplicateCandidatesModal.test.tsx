import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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
    excludeEmptyBooks: vi.fn(),
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

  describe('evidence and the empty row suggestion (#2999)', () => {
    const owned: DuplicateCandidateMember = {
      ...book(22, 'The Nightingale', false, ['article-strip']),
      status: 'imported',
      hasFiles: true,
      evidence: { files: [{ kind: 'ebook', format: 'epub' }], isbns: [], isbnCount: 0, asins: [], series: [], year: 2015 },
    }
    const emptyRow: DuplicateCandidateMember = {
      ...book(21, 'Nightingale', false, ['article-strip']),
      hasFiles: false,
      evidence: { files: [], isbns: [], isbnCount: 0, asins: [], series: [], year: 2015 },
    }
    const withKeeper: DuplicateCandidates = {
      authorId: 7,
      count: 1,
      groups: [{
        key: 'nightingale',
        rules: ['article-strip'],
        books: [emptyRow, owned],
        signals: [],
        conflict: false,
        keeperId: 22,
        suggestedExcludeIds: [21],
      }],
    }

    it('excludes the empty rows through the bulk exclude action after a confirm', async () => {
      vi.mocked(api.listAuthorDuplicateCandidates)
        .mockResolvedValueOnce(withKeeper)
        .mockResolvedValueOnce({ authorId: 7, count: 0, groups: [] })
      vi.mocked(api.excludeEmptyBooks).mockResolvedValue({ results: { '21': { ok: true } } })
      const onChanged = vi.fn()
      render(<DuplicateCandidatesModal authorId={7} authorName="Kristin Hannah" onClose={() => {}} onChanged={onChanged} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Exclude the 1 empty row(s) in this group' }))
      const dialog = await screen.findByTestId('confirm-dialog')
      expect(dialog).toHaveTextContent('Keep "The Nightingale", which has files')
      expect(dialog).toHaveTextContent('• Nightingale')
      expect(api.excludeEmptyBooks).not.toHaveBeenCalled()
      fireEvent.click(within(dialog).getByRole('button', { name: 'Exclude' }))

      await waitFor(() => {
        expect(api.excludeEmptyBooks).toHaveBeenCalledWith([21])
        expect(onChanged).toHaveBeenCalledTimes(1)
      })
      // The re-fetch dropped the group.
      expect(await screen.findByText('No duplicate titles found.')).toBeInTheDocument()
      expect(api.toggleExcluded).not.toHaveBeenCalled()
    })

    it('does nothing when the confirm is cancelled', async () => {
      vi.mocked(api.listAuthorDuplicateCandidates).mockResolvedValue(withKeeper)
      render(<DuplicateCandidatesModal authorId={7} authorName="Kristin Hannah" onClose={() => {}} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Exclude the 1 empty row(s) in this group' }))
      const dialog = await screen.findByTestId('confirm-dialog')
      fireEvent.click(within(dialog).getByRole('button', { name: 'common.cancel' }))
      await waitFor(() => expect(screen.queryByTestId('confirm-dialog')).not.toBeInTheDocument())
      expect(api.excludeEmptyBooks).not.toHaveBeenCalled()
    })

    it('asks before excluding the row that has files', async () => {
      vi.mocked(api.listAuthorDuplicateCandidates).mockResolvedValue(withKeeper)
      render(<DuplicateCandidatesModal authorId={7} authorName="Kristin Hannah" onClose={() => {}} />)

      const ownedRow = await screen.findByTestId('duplicate-row-22')
      fireEvent.click(within(ownedRow).getByRole('button', { name: 'Exclude' }))
      const dialog = await screen.findByTestId('confirm-dialog')
      expect(dialog).toHaveTextContent('Exclude a row that has files?')
      expect(api.toggleExcluded).not.toHaveBeenCalled()
      fireEvent.click(within(dialog).getByRole('button', { name: 'Exclude' }))
      await waitFor(() => expect(api.toggleExcluded).toHaveBeenCalledWith(22))
    })

    it('never sends a row with files, even when the payload suggests it', async () => {
      // A malformed or stale payload that lists the keeper among the rows to
      // exclude: the action filters it out before anything is posted.
      vi.mocked(api.listAuthorDuplicateCandidates).mockResolvedValue({
        ...withKeeper,
        groups: [{ ...withKeeper.groups[0], suggestedExcludeIds: [21, 22] }],
      })
      vi.mocked(api.excludeEmptyBooks).mockResolvedValue({ results: { '21': { ok: true } } })
      render(<DuplicateCandidatesModal authorId={7} authorName="Kristin Hannah" onClose={() => {}} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Exclude the 1 empty row(s) in this group' }))
      const dialog = await screen.findByTestId('confirm-dialog')
      expect(dialog).not.toHaveTextContent('• The Nightingale')
      fireEvent.click(within(dialog).getByRole('button', { name: 'Exclude' }))
      await waitFor(() => expect(api.excludeEmptyBooks).toHaveBeenCalledTimes(1))
      expect(api.excludeEmptyBooks).toHaveBeenCalledWith([21])
    })

    it('says which rows the server skipped because they gained files', async () => {
      vi.mocked(api.listAuthorDuplicateCandidates).mockResolvedValue(withKeeper)
      vi.mocked(api.excludeEmptyBooks).mockResolvedValue({
        results: { '21': { ok: false, error: 'book has files; not excluded', code: 'has_files' } },
      })
      render(<DuplicateCandidatesModal authorId={7} authorName="Kristin Hannah" onClose={() => {}} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Exclude the 1 empty row(s) in this group' }))
      fireEvent.click(within(await screen.findByTestId('confirm-dialog')).getByRole('button', { name: 'Exclude' }))
      expect(await screen.findByText('1 row(s) were skipped because they have files now. Review the group again.')).toBeInTheDocument()
    })
  })
})
