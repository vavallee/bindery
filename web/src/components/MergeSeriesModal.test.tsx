import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import MergeSeriesModal from './MergeSeriesModal'
import { api } from '../api/client'
import type { Series, SeriesMergePlan } from '../api/client'
import { acceptConfirm, cancelConfirm } from '../test-utils'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) =>
      opts && typeof opts === 'object' ? `${key} ${JSON.stringify(opts)}` : key,
  }),
}))

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return { ...actual, api: { ...actual.api, mergeSeries: vi.fn() } }
})

function series(id: number, title: string, books = 0): Series {
  return {
    id, title, foreignSeriesId: `s:${id}`, description: '', monitored: false,
    books: Array.from({ length: books }, (_, i) => ({ seriesId: id, bookId: id * 10 + i, positionInSeries: '' })),
  }
}

const target = series(1, 'Fjellserien', 2)
const all = [target, series(2, 'Serien om fjellet', 1), series(3, 'Fjell', 1), series(4, 'Havserien')]

const plan: SeriesMergePlan = {
  targetId: 1,
  title: 'Fjellserien',
  aliases: ['s:2'],
  sources: [{
    id: 2, title: 'Serien om fjellet', foreignSeriesId: 's:2',
    moved: [{ bookId: 21, title: 'Siste vinter', position: '3', primary: true }],
    kept: [{ bookId: 11, title: 'Fjellvinden', position: '1', primary: true }],
    conflicts: [{ bookId: 11, title: 'Fjellvinden', targetPosition: '1', sourcePosition: '2' }],
  }],
  hardcoverLinkFrom: 2,
  genreOverrideFrom: 0,
  monitored: false,
}

// A checkbox's name is the series title followed by its book count.
function check(title: string) {
  fireEvent.click(screen.getByRole('checkbox', { name: name => name.startsWith(`${title}series.merge.bookCount`) }))
}

describe('MergeSeriesModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.mergeSeries).mockResolvedValue(plan)
  })

  it('lists the other series, filters them and keeps Preview off until one is picked', () => {
    render(<MergeSeriesModal target={target} series={all} onClose={vi.fn()} onMerged={vi.fn()} />)
    expect(screen.getAllByRole('checkbox')).toHaveLength(3)
    expect(screen.getByRole('button', { name: 'series.merge.preview' })).toBeDisabled()
    fireEvent.change(screen.getByLabelText('series.merge.filterPlaceholder'), { target: { value: 'hav' } })
    expect(screen.getAllByRole('checkbox')).toHaveLength(1)
    fireEvent.change(screen.getByLabelText('series.merge.filterPlaceholder'), { target: { value: 'ingen slik serie' } })
    expect(screen.queryAllByRole('checkbox')).toHaveLength(0)
    expect(screen.getByText('series.merge.noCandidates')).toBeInTheDocument()
  })

  it('unticking the only series picked turns Preview off again', () => {
    render(<MergeSeriesModal target={target} series={all} onClose={vi.fn()} onMerged={vi.fn()} />)
    check('Serien om fjellet')
    expect(screen.getByRole('button', { name: 'series.merge.preview' })).toBeEnabled()
    check('Serien om fjellet')
    expect(screen.getByRole('button', { name: 'series.merge.preview' })).toBeDisabled()
  })

  it('previews with a dry run, then merges after confirmation', async () => {
    const onMerged = vi.fn()
    const onClose = vi.fn()
    render(<MergeSeriesModal target={target} series={all} onClose={onClose} onMerged={onMerged} />)
    check('Serien om fjellet')
    fireEvent.change(screen.getByLabelText('series.merge.renameLabel'), { target: { value: '  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'series.merge.preview' }))

    await waitFor(() => expect(api.mergeSeries).toHaveBeenCalledWith(1, { sourceIds: [2], title: undefined, dryRun: true }))
    const preview = await screen.findByTestId('merge-preview')
    expect(preview.textContent).toContain('"moved":1')
    expect(preview.textContent).toContain('series.merge.conflict')
    expect(preview.textContent).toContain('series.merge.takesHardcoverLink')

    fireEvent.click(screen.getByRole('button', { name: 'series.merge.apply' }))
    await acceptConfirm()
    await waitFor(() => expect(api.mergeSeries).toHaveBeenLastCalledWith(1, { sourceIds: [2], title: undefined, dryRun: false }))
    expect(onMerged).toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })

  it('applies nothing when the confirmation is cancelled', async () => {
    const onMerged = vi.fn()
    render(<MergeSeriesModal target={target} series={all} onClose={vi.fn()} onMerged={onMerged} />)
    check('Serien om fjellet')
    fireEvent.click(screen.getByRole('button', { name: 'series.merge.preview' }))
    await screen.findByTestId('merge-preview')
    fireEvent.click(screen.getByRole('button', { name: 'series.merge.apply' }))
    await cancelConfirm()
    expect(api.mergeSeries).toHaveBeenCalledTimes(1)
    expect(onMerged).not.toHaveBeenCalled()
  })

  it('drops the preview when the selection changes, so a stale plan is never applied', async () => {
    render(<MergeSeriesModal target={target} series={all} onClose={vi.fn()} onMerged={vi.fn()} />)
    check('Serien om fjellet')
    fireEvent.click(screen.getByRole('button', { name: 'series.merge.preview' }))
    await screen.findByTestId('merge-preview')
    check('Fjell')
    expect(screen.queryByTestId('merge-preview')).toBeNull()
    expect(screen.queryByRole('button', { name: 'series.merge.apply' })).toBeNull()
  })

  it('shows the server error', async () => {
    vi.mocked(api.mergeSeries).mockRejectedValueOnce(new Error('invalid series merge: series 9 does not exist'))
    render(<MergeSeriesModal target={target} series={all} onClose={vi.fn()} onMerged={vi.fn()} />)
    check('Serien om fjellet')
    fireEvent.click(screen.getByRole('button', { name: 'series.merge.preview' }))
    expect((await screen.findByRole('alert')).textContent).toContain('series 9 does not exist')
  })

  it('names everything the kept series takes over: a new name, genres, monitoring', async () => {
    vi.mocked(api.mergeSeries).mockResolvedValue({
      ...plan, title: 'Fjell-serien', hardcoverLinkFrom: 0, genreOverrideFrom: 99, monitored: true,
      sources: [{ ...plan.sources[0], conflicts: [] }],
    })
    // A series listed without its books counts as none.
    const bare = { ...series(5, 'Uten bøker'), books: undefined }
    render(<MergeSeriesModal target={target} series={[...all, bare]} onClose={vi.fn()} onMerged={vi.fn()} />)
    expect(screen.getByRole('checkbox', { name: name => name.startsWith('Uten bøker') }).closest('label')?.textContent).toContain('"count":0')
    check('Serien om fjellet')
    fireEvent.change(screen.getByLabelText('series.merge.renameLabel'), { target: { value: 'Fjell-serien' } })
    fireEvent.click(screen.getByRole('button', { name: 'series.merge.preview' }))

    await waitFor(() => expect(api.mergeSeries).toHaveBeenCalledWith(1, { sourceIds: [2], title: 'Fjell-serien', dryRun: true }))
    const preview = (await screen.findByTestId('merge-preview')).textContent ?? ''
    expect(preview).toContain('series.merge.renamed')
    // Series 99 is not in the list, so the note falls back to its id.
    expect(preview).toContain('series.merge.takesGenres {"title":"99"}')
    expect(preview).toContain('series.merge.becomesMonitored')
    expect(preview).not.toContain('series.merge.takesHardcoverLink')
    expect(preview).not.toContain('series.merge.conflict')
  })

  it('falls back to a generic message when the failure is not an Error', async () => {
    vi.mocked(api.mergeSeries).mockRejectedValueOnce('boom')
    render(<MergeSeriesModal target={target} series={all} onClose={vi.fn()} onMerged={vi.fn()} />)
    check('Serien om fjellet')
    fireEvent.click(screen.getByRole('button', { name: 'series.merge.preview' }))
    expect((await screen.findByRole('alert')).textContent).toBe('series.merge.failed')
  })
})
