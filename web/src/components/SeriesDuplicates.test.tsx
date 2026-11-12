import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import SeriesDuplicates from './SeriesDuplicates'
import { api } from '../api/client'
import type { DuplicateMergeResponse, DuplicateSeriesGroup, Series } from '../api/client'
import { acceptConfirm, cancelConfirm, confirmDialog } from '../test-utils'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) =>
      opts && typeof opts === 'object' ? `${key} ${JSON.stringify(opts)}` : key,
  }),
}))

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: { ...actual.api, listSeriesDuplicates: vi.fn(), mergeSeriesDuplicates: vi.fn(), mergeSeries: vi.fn() },
  }
})

const groups: DuplicateSeriesGroup[] = [
  {
    key: 'wayfarers', suggestedKeepId: 1, sameAuthors: true,
    series: [
      { id: 1, title: 'Wayfarers', foreignSeriesId: 'hc-series:1', bookCount: 3, authors: ['Becky Chambers'], hardcoverLinked: true, monitored: false },
      { id: 2, title: 'wayfarers', foreignSeriesId: 'abs:2', bookCount: 1, authors: ['Becky Chambers'], hardcoverLinked: false, monitored: false },
    ],
  },
  {
    key: 'legacy', suggestedKeepId: 3, sameAuthors: false,
    series: [
      { id: 3, title: 'Legacy', foreignSeriesId: 'calibre:3', bookCount: 2, authors: ['Author One'], hardcoverLinked: false, monitored: false },
      { id: 4, title: 'Legacy', foreignSeriesId: 'abs:4', bookCount: 1, authors: ['Author Two'], hardcoverLinked: false, monitored: false },
    ],
  },
  {
    key: 'murderbot', suggestedKeepId: 5, sameAuthors: true,
    series: [
      { id: 5, title: 'Murderbot', foreignSeriesId: 'calibre:5', bookCount: 2, authors: ['Martha Wells'], hardcoverLinked: false, monitored: false },
      { id: 6, title: 'Murderbot', foreignSeriesId: 'abs:6', bookCount: 0, authors: [], hardcoverLinked: false, monitored: false },
    ],
  },
]

function series(id: number, title: string): Series {
  return { id, title, foreignSeriesId: `s:${id}`, description: '', monitored: false }
}
const all = [series(1, 'Wayfarers'), series(2, 'wayfarers'), series(3, 'Legacy'), series(4, 'Legacy'), series(5, 'Murderbot'), series(6, 'Murderbot')]

function plan(targetId: number, sourceId: number, moved: number) {
  return {
    targetId, title: '', aliases: [], hardcoverLinkFrom: 0, genreOverrideFrom: 0, monitored: false,
    sources: [{
      id: sourceId, title: '', foreignSeriesId: '', kept: [], conflicts: [],
      moved: Array.from({ length: moved }, (_, i) => ({ bookId: sourceId * 10 + i, title: '', position: '', primary: false })),
    }],
  }
}

const dryRun: DuplicateMergeResponse = {
  dryRun: true, merged: 2, failed: 0,
  results: [
    { keepId: 1, sourceIds: [2], ok: true, plan: plan(1, 2, 1) },
    { keepId: 5, sourceIds: [6], ok: true, plan: plan(5, 6, 0) },
  ],
}

async function openList() {
  fireEvent.click(await screen.findByRole('button', { name: 'series.duplicates.review' }))
}

describe('SeriesDuplicates', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listSeriesDuplicates).mockResolvedValue({ groups })
  })

  it('renders nothing when there are no duplicates', async () => {
    vi.mocked(api.listSeriesDuplicates).mockResolvedValue({ groups: [] })
    const { container } = render(<SeriesDuplicates series={all} onMerged={vi.fn()} />)
    await waitFor(() => expect(api.listSeriesDuplicates).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  it('shows a banner and lists each group with counts, authors, the link and the suggested keep', async () => {
    render(<SeriesDuplicates series={all} onMerged={vi.fn()} />)
    expect(await screen.findByText('series.duplicates.banner {"count":3}')).toBeInTheDocument()
    await openList()
    const rows = screen.getAllByTestId('duplicate-group')
    expect(rows).toHaveLength(3)
    const first = within(rows[0])
    expect(first.getByText('series.merge.bookCount {"count":3}')).toBeInTheDocument()
    expect(first.getAllByText('Becky Chambers')).toHaveLength(2)
    expect(first.getByText('series.duplicates.hardcoverLinked')).toBeInTheDocument()
    expect(first.getByText('series.duplicates.suggested')).toBeInTheDocument()
    expect(first.getAllByRole('radio')[0]).toBeChecked()
    // Only the group with different authors carries the note.
    expect(within(rows[1]).getByText('series.duplicates.differentAuthors')).toBeInTheDocument()
    expect(first.queryByText('series.duplicates.differentAuthors')).toBeNull()
    expect(screen.getByRole('button', { name: 'series.duplicates.mergeAllMatching {"count":2}' })).toBeEnabled()
  })

  it('merges only the groups with matching authors, after a dry run and a confirmation', async () => {
    const onMerged = vi.fn()
    vi.mocked(api.mergeSeriesDuplicates)
      .mockResolvedValueOnce(dryRun)
      .mockResolvedValueOnce({ ...dryRun, dryRun: false })
    render(<SeriesDuplicates series={all} onMerged={onMerged} />)
    await openList()
    fireEvent.click(screen.getByRole('button', { name: 'series.duplicates.mergeAllMatching {"count":2}' }))

    await waitFor(() => expect(confirmDialog()).not.toBeNull())
    expect(api.mergeSeriesDuplicates).toHaveBeenCalledTimes(1)
    expect(api.mergeSeriesDuplicates).toHaveBeenCalledWith({
      groups: [{ keepId: 1, sourceIds: [2] }, { keepId: 5, sourceIds: [6] }],
      dryRun: true,
    })
    expect(within(confirmDialog()!).getByText(/series\.duplicates\.bulkSummary \{"count":2,"removed":2,"moved":1\}/)).toBeInTheDocument()

    await acceptConfirm()
    await waitFor(() => expect(api.mergeSeriesDuplicates).toHaveBeenCalledTimes(2))
    expect(vi.mocked(api.mergeSeriesDuplicates).mock.calls[1][0]).toEqual({
      groups: [{ keepId: 1, sourceIds: [2] }, { keepId: 5, sourceIds: [6] }],
    })
    await waitFor(() => expect(onMerged).toHaveBeenCalled())
    expect(api.listSeriesDuplicates).toHaveBeenCalledTimes(2)
    expect(await screen.findByText('series.duplicates.bulkDone {"count":2}')).toBeInTheDocument()
  })

  it('runs nothing when the confirmation is cancelled', async () => {
    vi.mocked(api.mergeSeriesDuplicates).mockResolvedValueOnce(dryRun)
    const onMerged = vi.fn()
    render(<SeriesDuplicates series={all} onMerged={onMerged} />)
    await openList()
    fireEvent.click(screen.getByRole('button', { name: 'series.duplicates.mergeAllMatching {"count":2}' }))
    await cancelConfirm()
    await waitFor(() => expect(confirmDialog()).toBeNull())
    expect(api.mergeSeriesDuplicates).toHaveBeenCalledTimes(1)
    expect(onMerged).not.toHaveBeenCalled()
  })

  it('opens the merge dialog on one group with the other series ticked, using the chosen keep', async () => {
    render(<SeriesDuplicates series={all} onMerged={vi.fn()} />)
    await openList()
    const legacy = screen.getAllByTestId('duplicate-group')[1]
    fireEvent.click(within(legacy).getAllByRole('radio')[1])
    fireEvent.click(within(legacy).getByRole('button', { name: 'series.duplicates.merge' }))
    expect(await screen.findByText('series.merge.title {"title":"Legacy"}')).toBeInTheDocument()
    const ticked = screen.getAllByRole('checkbox').filter(b => (b as HTMLInputElement).checked)
    expect(ticked).toHaveLength(1)
    vi.mocked(api.mergeSeries).mockResolvedValue(plan(4, 3, 2))
    fireEvent.click(screen.getByRole('button', { name: 'series.merge.preview' }))
    await waitFor(() => expect(api.mergeSeries).toHaveBeenCalledWith(4, { sourceIds: [3], title: undefined, dryRun: true }))
  })
})
