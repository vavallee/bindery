import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import QueuePage from './QueuePage'
import { api } from '../api/client'
import type { Download, PendingRelease, QueueItem } from '../api/client'

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listQueue: vi.fn(),
      listPending: vi.fn(),
      deleteFromQueue: vi.fn(),
      retryImport: vi.fn(),
      dismissPending: vi.fn(),
      grabPending: vi.fn(),
    },
  }
})

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      if (key === 'queue.remaining') return `${String(options?.time)} remaining`
      const labels: Record<string, string> = {
        'common.loading': 'Loading...',
        'queue.title': 'Queue',
        'queue.empty': 'Queue is empty',
        'queue.remove': 'Remove',
        'queue.retryImport': 'Retry import',
        'queue.retryingImport': 'Retrying…',
        'queue.retryImportHint': 'After fixing the path remap or moving the completed files in the download client, retry import to reuse the existing download.',
        'queue.retryImportError': `Retry failed: ${String(options?.error)}`,
      }
      return labels[key] ?? key
    },
  }),
}))

function makeQueueItem(overrides: Partial<QueueItem> = {}): QueueItem {
  return {
    id: 1,
    guid: 'queue-guid',
    title: 'Queued Release',
    status: 'downloading',
    size: 1048576,
    protocol: 'usenet',
    errorMessage: '',
    addedAt: '2026-05-01T12:00:00Z',
    ...overrides,
  }
}

function makePendingRelease(overrides: Partial<PendingRelease> = {}): PendingRelease {
  return {
    id: 10,
    bookId: 42,
    title: 'Pending Release',
    guid: 'pending-guid',
    protocol: 'torrent',
    size: 1572864,
    ageMinutes: 30,
    quality: 'EPUB',
    customScore: 100,
    reason: 'Waiting for better quality',
    firstSeen: '2026-05-01T12:00:00Z',
    releaseJson: '{}',
    ...overrides,
  }
}

function makeDownload(overrides: Partial<Download> = {}): Download {
  return {
    id: 99,
    guid: 'download-guid',
    title: 'Grabbed Pending Release',
    status: 'queued',
    size: 1572864,
    protocol: 'torrent',
    errorMessage: '',
    addedAt: '2026-05-01T12:00:00Z',
    ...overrides,
  }
}

function deferred<T>() {
  let resolve: (value: T) => void = () => {}
  let reject: (reason?: unknown) => void = () => {}
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function renderQueuePage() {
  return render(
    <MemoryRouter>
      <QueuePage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  document.title = 'Bindery'
  vi.mocked(api.listQueue).mockResolvedValue([])
  vi.mocked(api.listPending).mockResolvedValue([])
  vi.mocked(api.deleteFromQueue).mockResolvedValue(undefined)
  vi.mocked(api.retryImport).mockResolvedValue({ ok: true })
  vi.mocked(api.dismissPending).mockResolvedValue(undefined)
  vi.mocked(api.grabPending).mockResolvedValue(makeDownload())
})

afterEach(() => {
  vi.useRealTimers()
})

describe('QueuePage', () => {
  it('renders loading and then the empty queue state', async () => {
    const queueLoad = deferred<QueueItem[]>()
    const pendingLoad = deferred<PendingRelease[]>()
    vi.mocked(api.listQueue).mockReturnValue(queueLoad.promise)
    vi.mocked(api.listPending).mockReturnValue(pendingLoad.promise)

    renderQueuePage()

    expect(screen.getByText('Loading...')).toBeInTheDocument()

    await act(async () => {
      queueLoad.resolve([])
      pendingLoad.resolve([])
      await Promise.resolve()
    })

    expect(await screen.findByText('Queue is empty')).toBeInTheDocument()
  })

  it('renders queue statuses, progress, fallback errors, and error prefixes', async () => {
    vi.mocked(api.listQueue).mockResolvedValue([
      makeQueueItem({
        id: 1,
        title: 'Dune EPUB',
        status: 'downloading',
        size: 2147483648,
        percentage: '45',
        timeLeft: '12 minutes',
        protocol: 'usenet',
      }),
      makeQueueItem({
        id: 2,
        title: 'Blocked Import',
        status: 'importBlocked',
        size: 512000,
        protocol: 'torrent',
      }),
      makeQueueItem({
        id: 3,
        title: 'Failed Import',
        status: 'importFailed',
        size: 1048576,
        errorMessage: 'Missing target folder',
      }),
      makeQueueItem({
        id: 4,
        title: 'Failed Download',
        status: 'failed',
        size: 1048576,
        errorMessage: 'Client rejected download',
      }),
    ])

    const { container } = renderQueuePage()

    expect(await screen.findByText('Dune EPUB')).toBeInTheDocument()
    const duneCard = screen.getByText('Dune EPUB').closest('div')!.parentElement!
    expect(within(duneCard).getByText('Downloading')).toBeInTheDocument()
    expect(within(duneCard).getByText('2.0 GB')).toBeInTheDocument()
    expect(within(duneCard).getByText('45%')).toBeInTheDocument()
    expect(within(duneCard).getByText('12 minutes remaining')).toBeInTheDocument()
    expect(within(duneCard).getByText('usenet')).toBeInTheDocument()
    expect(screen.getByText('Import Blocked')).toBeInTheDocument()
    expect(screen.getByText(/Import blocked — manual intervention required/)).toBeInTheDocument()
    expect(screen.getByText('Import Failed')).toBeInTheDocument()
    expect(screen.getByText('Import failed:')).toBeInTheDocument()
    expect(screen.getByText('Missing target folder')).toBeInTheDocument()
    expect(screen.getByText(/After fixing the path remap/)).toBeInTheDocument()
    expect(screen.getByText('Failed')).toBeInTheDocument()
    expect(screen.getByText('Error:')).toBeInTheDocument()
    expect(screen.getByText('Client rejected download')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Retry import' })).toHaveLength(1)
    expect(container.querySelector('[style="width: 45%;"]')).toBeInTheDocument()
  })

  it('retries an import-failed queue item and reloads the queue', async () => {
    const retry = deferred<{ ok: boolean }>()
    vi.mocked(api.listQueue)
      .mockResolvedValueOnce([makeQueueItem({
        id: 3,
        title: 'Retry Me',
        status: 'importFailed',
        errorMessage: 'Missing target folder',
      })])
      .mockResolvedValueOnce([])
    vi.mocked(api.retryImport).mockReturnValue(retry.promise)

    renderQueuePage()

    expect(await screen.findByText('Retry Me')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Retry import' }))
    expect(api.retryImport).toHaveBeenCalledWith(3)
    expect(screen.getByRole('button', { name: 'Retrying…' })).toBeDisabled()

    await act(async () => {
      retry.resolve({ ok: true })
      await Promise.resolve()
    })

    await waitFor(() => expect(api.listQueue).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('Queue is empty')).toBeInTheDocument()
  })

  it('shows an inline error when retrying an import fails', async () => {
    vi.mocked(api.listQueue).mockResolvedValue([makeQueueItem({
      id: 4,
      title: 'Retry Fails',
      status: 'importFailed',
      errorMessage: 'Missing target folder',
    })])
    vi.mocked(api.retryImport).mockRejectedValue(new Error('download is not in importFailed state'))

    renderQueuePage()

    expect(await screen.findByText('Retry Fails')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Retry import' }))

    expect(await screen.findByText('Retry failed: download is not in importFailed state')).toBeInTheDocument()
  })

  it('renders pending releases and supports grabbing a pending release', async () => {
    let resolveGrab: (download: Download) => void = () => {}
    vi.mocked(api.listPending)
      .mockResolvedValueOnce([makePendingRelease()])
      .mockResolvedValueOnce([])
    vi.mocked(api.grabPending).mockImplementation(() => new Promise(resolve => { resolveGrab = resolve }))

    renderQueuePage()

    expect(await screen.findByText('Pending Releases (1)')).toBeInTheDocument()
    expect(screen.getByText('Pending Release')).toBeInTheDocument()
    expect(screen.getByText('Waiting for better quality')).toBeInTheDocument()
    expect(screen.getByText('1.5 MB')).toBeInTheDocument()
    expect(screen.getByText('EPUB')).toBeInTheDocument()
    expect(screen.getByText('torrent')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Grab Now' }))
    expect(screen.getByRole('button', { name: 'Grabbing…' })).toBeDisabled()
    expect(api.grabPending).toHaveBeenCalledWith(10)

    await act(async () => {
      resolveGrab(makeDownload())
      await Promise.resolve()
    })

    await waitFor(() => expect(api.listPending).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('Queue is empty')).toBeInTheDocument()
  })

  it('dismisses a pending release and reloads pending releases', async () => {
    vi.mocked(api.listPending)
      .mockResolvedValueOnce([makePendingRelease({ id: 11, title: 'Dismiss Me' })])
      .mockResolvedValueOnce([])

    renderQueuePage()

    const pendingTitle = await screen.findByText('Dismiss Me')
    const pendingCard = pendingTitle.closest('div')!.parentElement!
    fireEvent.click(within(pendingCard).getByRole('button', { name: 'Dismiss' }))

    await waitFor(() => expect(api.dismissPending).toHaveBeenCalledWith(11))
    await waitFor(() => expect(api.listPending).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('Queue is empty')).toBeInTheDocument()
  })

  it('deletes a queue item via the confirmation modal and reloads the queue', async () => {
    vi.mocked(api.listQueue)
      .mockResolvedValueOnce([makeQueueItem({ id: 7, title: 'Remove Me' })])
      .mockResolvedValueOnce([])

    renderQueuePage()

    const item = await screen.findByText('Remove Me')
    const card = item.closest('div')!.parentElement!
    fireEvent.click(within(card).getByRole('button', { name: 'Remove' }))

    // The card's Remove button opens a confirmation modal; the actual delete
    // only fires on the modal's confirm button. Its label is the untranslated
    // i18n key here because the test's t() mock only maps a curated subset.
    fireEvent.click(await screen.findByRole('button', { name: 'queue.removeConfirm' }))

    await waitFor(() => expect(api.deleteFromQueue).toHaveBeenCalledWith(7, false))
    await waitFor(() => expect(api.listQueue).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('Queue is empty')).toBeInTheDocument()
  })

  it('passes deleteFiles=true when the modal "delete files" checkbox is ticked', async () => {
    vi.mocked(api.listQueue)
      .mockResolvedValueOnce([makeQueueItem({ id: 7, title: 'Remove Me' })])
      .mockResolvedValueOnce([])

    renderQueuePage()

    const item = await screen.findByText('Remove Me')
    const card = item.closest('div')!.parentElement!
    fireEvent.click(within(card).getByRole('button', { name: 'Remove' }))

    fireEvent.click(await screen.findByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: 'queue.removeConfirm' }))

    await waitFor(() => expect(api.deleteFromQueue).toHaveBeenCalledWith(7, true))
  })

  it('polls the queue every five seconds and clears the interval on unmount', async () => {
    vi.useFakeTimers()

    const clearIntervalSpy = vi.spyOn(globalThis, 'clearInterval')
    vi.mocked(api.listQueue).mockResolvedValue([])
    vi.mocked(api.listPending).mockResolvedValue([])

    const { unmount } = renderQueuePage()

    expect(api.listQueue).toHaveBeenCalledTimes(1)
    expect(api.listPending).toHaveBeenCalledTimes(1)

    await act(async () => {
      vi.advanceTimersByTime(5000)
      await Promise.resolve()
    })

    expect(api.listQueue).toHaveBeenCalledTimes(2)
    expect(api.listPending).toHaveBeenCalledTimes(2)

    unmount()

    expect(clearIntervalSpy).toHaveBeenCalled()

    await act(async () => {
      vi.advanceTimersByTime(5000)
      await Promise.resolve()
    })

    expect(api.listQueue).toHaveBeenCalledTimes(2)
    expect(api.listPending).toHaveBeenCalledTimes(2)
    clearIntervalSpy.mockRestore()
  })
})
