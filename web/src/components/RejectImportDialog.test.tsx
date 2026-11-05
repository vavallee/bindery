import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RejectImportDialog from './RejectImportDialog'
import { rejectSummary } from '../util/rejectSummary'
import { api } from '../api/client'
import type { HistoryEvent, RejectImportResult } from '../api/client'
import en from '../i18n/locales/en.json'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return { ...actual, api: { ...actual.api, rejectImport: vi.fn() } }
})

// The real English strings, so the test reads what the user reads.
function lookup(key: string): string {
  const v = key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], en)
  return typeof v === 'string' ? v : key
}
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) => {
      const plural = typeof opts?.count === 'number' ? (opts.count === 1 ? '_one' : '_other') : ''
      return lookup(key + plural).replace('{{count}}', String(opts?.count))
    },
  }),
}))

const base: HistoryEvent = {
  id: 7, bookId: 3, eventType: 'bookImported', sourceTitle: 'Rel', createdAt: '2026-01-01T00:00:00Z',
  data: JSON.stringify({ guid: 'g', paths: ['/lib/a.epub'] }),
}
const result: RejectImportResult = {
  status: 'wanted', detached: ['/lib/a.epub'], deleted: ['/lib/a.epub'], keptOnDisk: [], alreadyGone: [],
  releaseKnown: true, blocklisted: true, alreadyBlocklisted: false, nothingTracked: false, supersededPaths: [], noOp: false,
}

describe('RejectImportDialog', () => {
  beforeEach(() => vi.clearAllMocks())

  it('states what will happen and rejects only after the acknowledgement', async () => {
    vi.mocked(api.rejectImport).mockResolvedValue(result)
    const onRejected = vi.fn()
    render(<RejectImportDialog event={base} onClose={() => {}} onRejected={onRejected} />)
    expect(screen.getByText(/deleted from disk\. A file anywhere else is only untracked/)).toBeTruthy()
    expect(screen.getByText(/blocklisted for this book/)).toBeTruthy()
    expect(screen.getByText('/lib/a.epub')).toBeTruthy()
    const confirm = screen.getByRole('button', { name: 'Reject import' })
    expect((confirm as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent.click(confirm)
    await waitFor(() => expect(api.rejectImport).toHaveBeenCalledWith(7))
    await waitFor(() => expect(onRejected).toHaveBeenCalled())
    expect(onRejected.mock.calls[0][0]).toContain('1 file deleted from disk.')
  })

  it('says nothing will be blocklisted when the import recorded no release', () => {
    render(<RejectImportDialog event={{ ...base, data: '{"path":"/lib/a.epub"}' }} onClose={() => {}} onRejected={() => {}} />)
    expect(screen.getByText(/did not record which release it came from/)).toBeTruthy()
  })

  it('shows the server error and stays open', async () => {
    vi.mocked(api.rejectImport).mockRejectedValue(new Error('boom'))
    const onRejected = vi.fn()
    render(<RejectImportDialog event={base} onClose={() => {}} onRejected={onRejected} />)
    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: 'Reject import' }))
    expect(await screen.findByText('boom')).toBeTruthy()
    expect(onRejected).not.toHaveBeenCalled()
  })
})

describe('rejectSummary', () => {
  const t = (k: string, o?: Record<string, unknown>) => `${k}${o?.count !== undefined ? `:${o.count}` : ''}`
  it('reports a file left on disk and a missing release as their own outcomes', () => {
    const s = rejectSummary({ ...result, deleted: [], keptOnDisk: ['/x'], releaseKnown: false, blocklisted: false }, t)
    expect(s).toContain('doneKept:1')
    expect(s).toContain('doneNoRelease')
    expect(s).not.toContain('doneDeleted')
  })
})
