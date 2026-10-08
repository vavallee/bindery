import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it, vi } from 'vitest'
import type { DuplicateCandidateGroup, DuplicateCandidateMember, DuplicateEvidence } from '../api/client'
import DuplicateGroupCard from './DuplicateGroupCard'

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

const noEvidence: DuplicateEvidence = { files: [], isbns: [], isbnCount: 0, asins: [], series: [] }

function member(id: number, title: string, overrides: Partial<DuplicateCandidateMember> = {}): DuplicateCandidateMember {
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
    excluded: false,
    rules: ['article-strip'],
    evidence: noEvidence,
    hasFiles: false,
    ...overrides,
  }
}

const owned = member(2, 'The Nightingale', {
  status: 'imported',
  language: 'en',
  hasFiles: true,
  evidence: {
    files: [{ kind: 'ebook', format: 'epub' }, { kind: 'audiobook', format: '' }],
    isbns: ['9780312577223'],
    isbnCount: 3,
    asins: ['B00JO8PEN2'],
    series: [{ seriesId: 4, title: 'Standalone', position: '1' }],
    year: 2015,
  },
})
const empty = member(1, 'Nightingale', {
  language: 'eng',
  evidence: { ...noEvidence, isbns: ['9780312577223'], isbnCount: 1, year: 2015 },
})

const agreeing: DuplicateCandidateGroup = {
  key: 'nightingale',
  rules: ['article-strip'],
  authorId: 7,
  authorName: 'Kristin Hannah',
  books: [empty, owned],
  signals: [{ kind: 'shared-isbn', conflict: false, bookIds: [1, 2], values: ['9780312577223'] }],
  conflict: false,
  keeperId: 2,
  suggestedExcludeIds: [1],
}

function renderCard(group: DuplicateCandidateGroup, props: Partial<Parameters<typeof DuplicateGroupCard>[0]> = {}) {
  const onToggle = vi.fn()
  const onExcludeEmpty = vi.fn()
  render(
    <MemoryRouter>
      <DuplicateGroupCard group={group} busyBooks={new Set()} onToggle={onToggle} onExcludeEmpty={onExcludeEmpty} {...props} />
    </MemoryRouter>,
  )
  return { onToggle, onExcludeEmpty }
}

describe('DuplicateGroupCard', () => {
  it('shows the evidence for each row', () => {
    renderCard(agreeing)

    const ownedEvidence = screen.getByTestId('duplicate-evidence-2')
    expect(ownedEvidence).toHaveTextContent('Ebook (epub), Audiobook')
    expect(ownedEvidence).toHaveTextContent('In Library')
    expect(ownedEvidence).toHaveTextContent('2015')
    expect(ownedEvidence).toHaveTextContent('en')
    expect(ownedEvidence).toHaveTextContent('ISBN 9780312577223 (+2)')
    expect(ownedEvidence).toHaveTextContent('ASIN B00JO8PEN2')
    expect(ownedEvidence).toHaveTextContent('Standalone #1')

    const emptyEvidence = screen.getByTestId('duplicate-evidence-1')
    expect(emptyEvidence).toHaveTextContent('No files')
    expect(emptyEvidence).toHaveTextContent('Wanted')
  })

  it('marks the row with files as the one to keep, not the empty one', () => {
    renderCard(agreeing)
    expect(within(screen.getByTestId('duplicate-row-2')).getByText('Keep: has files')).toBeInTheDocument()
    expect(within(screen.getByTestId('duplicate-row-1')).queryByText('Keep: has files')).not.toBeInTheDocument()
  })

  it('shows agreements as evidence for the same book', () => {
    renderCard(agreeing)
    const item = screen.getByText(/share ISBN 9780312577223/).closest('li')
    expect(item).toHaveAttribute('data-conflict', 'false')
    expect(item).toHaveTextContent('Likely the same book: Rows 1, 2 share ISBN 9780312577223')
  })

  it('offers excluding only the empty rows, behind one action', () => {
    const { onExcludeEmpty, onToggle } = renderCard(agreeing)
    fireEvent.click(screen.getByRole('button', { name: 'Exclude the 1 empty row(s) in this group' }))
    expect(onExcludeEmpty).toHaveBeenCalledWith(agreeing)
    expect(onToggle).not.toHaveBeenCalled()
  })

  it('never counts a row with files in the suggestion, whatever the payload says', () => {
    // A malformed payload that lists the keeper as well: the card filters it.
    renderCard({ ...agreeing, suggestedExcludeIds: [1, 2] })
    expect(screen.getByRole('button', { name: 'Exclude the 1 empty row(s) in this group' })).toBeInTheDocument()
  })

  it('shows a conflict marker and no suggestion when the rows disagree', () => {
    const conflicted: DuplicateCandidateGroup = {
      ...agreeing,
      signals: [{ kind: 'series-position-conflict', conflict: true, bookIds: [1, 2], values: ['Foundation', '1', '2'] }],
      conflict: true,
      suggestedExcludeIds: [],
    }
    renderCard(conflicted)
    const item = screen.getByText(/different positions in Foundation/).closest('li')
    expect(item).toHaveAttribute('data-conflict', 'true')
    expect(item).toHaveTextContent('May be different books: Rows 1, 2 hold different positions in Foundation: #1, #2')
    expect(screen.queryByRole('button', { name: /empty row/ })).not.toBeInTheDocument()
    expect(screen.getByText('The rows disagree, so nothing is suggested. Check the evidence before excluding a row.')).toBeInTheDocument()
    // The keeper is still marked: the files say which row to keep even when the rows disagree.
    expect(within(screen.getByTestId('duplicate-row-2')).getByText('Keep: has files')).toBeInTheDocument()
  })

  it('suggests nothing when more than one row has files', () => {
    const both: DuplicateCandidateGroup = {
      ...agreeing,
      books: [{ ...empty, hasFiles: true, evidence: { ...noEvidence, files: [{ kind: 'audiobook', format: 'm4b' }] } }, owned],
      keeperId: undefined,
      suggestedExcludeIds: [],
    }
    renderCard(both)
    expect(screen.queryByText('Keep: has files')).not.toBeInTheDocument()
    expect(screen.getAllByText('Has files')).toHaveLength(2)
    expect(screen.getByText('More than one row has files, so nothing is suggested.')).toBeInTheDocument()
  })

  it('explains a suggestion withheld for lack of evidence', () => {
    renderCard({ ...agreeing, signals: [], suggestedExcludeIds: [], suggestionWithheld: 'no-evidence' })
    expect(screen.queryByRole('button', { name: /empty row/ })).not.toBeInTheDocument()
    expect(screen.getByText(/Nothing ties every empty row to the row with files/)).toBeInTheDocument()
  })

  it('passes the whole row to the per row toggle', () => {
    const { onToggle } = renderCard(agreeing)
    const [first] = screen.getAllByRole('button', { name: 'Exclude' })
    fireEvent.click(first)
    expect(onToggle).toHaveBeenCalledWith(empty)
  })

  it('shows and links the author in the library-wide view', () => {
    renderCard(agreeing, { showAuthor: true })
    expect(screen.getByRole('link', { name: 'Kristin Hannah' })).toHaveAttribute('href', '/author/7')
    expect(screen.getByRole('link', { name: 'The Nightingale' })).toHaveAttribute('href', '/book/2')
  })

  it('renders a payload from an older server without evidence', () => {
    renderCard({ key: 'dune', rules: ['alnum-equal'], books: [member(1, 'Dune', { evidence: undefined, hasFiles: undefined }), member(2, 'Dune', { evidence: undefined })] })
    expect(screen.getAllByText('No files')).toHaveLength(2)
    expect(screen.queryByRole('button', { name: /empty row/ })).not.toBeInTheDocument()
  })
})
