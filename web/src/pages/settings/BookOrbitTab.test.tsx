import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'

vi.mock('../../api/client', () => ({
  api: {
    bookOrbitConfig: vi.fn(),
    bookOrbitSetConfig: vi.fn(),
    bookOrbitTest: vi.fn(),
    bookOrbitLibraries: vi.fn(),
    bookOrbitImportStart: vi.fn(),
    bookOrbitImportStatus: vi.fn(),
    bookOrbitImportRuns: vi.fn(),
  },
}))

import { api, ABSImportProgress, ABSImportRun, ABSImportStats, BookOrbitConfig } from '../../api/client'
import BookOrbitTab from './BookOrbitTab'
import '../../i18n'

const mocked = api as unknown as Record<string, ReturnType<typeof vi.fn>>

const saved: BookOrbitConfig = { baseUrl: 'http://bookorbit:3000', libraryIds: ['2'], pathRemap: '/books:/media/books', apiKeyConfigured: true }
const idle: ABSImportProgress = { running: false, processed: 0 }

function stats(overrides: Partial<ABSImportStats> = {}): ABSImportStats {
  return {
    librariesScanned: 1, pagesScanned: 1, itemsSeen: 8, itemsNormalized: 8, itemsDetailFetched: 8,
    authorsCreated: 4, authorsLinked: 4, booksCreated: 8, booksLinked: 0, booksUpdated: 0,
    seriesCreated: 3, seriesLinked: 4, editionsAdded: 9, ownedMarked: 9, pendingManual: 1,
    reviewQueued: 0, metadataMatched: 0, metadataRelinked: 0, metadataConflicts: 0, metadataAutoResolved: 0,
    skipped: 0, failed: 0,
    ...overrides,
  }
}

function run(id: number, overrides: Partial<ABSImportRun> = {}): ABSImportRun {
  return {
    id, sourceId: 'bookorbit', sourceLabel: 'BookOrbit', baseUrl: saved.baseUrl, libraryId: '2', status: 'completed', dryRun: false,
    startedAt: '2026-11-15T10:00:00Z', finishedAt: '2026-11-15T10:00:05Z',
    source: { sourceId: 'bookorbit', label: 'BookOrbit', baseUrl: saved.baseUrl, libraryId: '2', enabled: true, dryRun: false },
    summary: { dryRun: false, resumedFromCheckpoint: false, stats: stats() },
    ...overrides,
  } as ABSImportRun
}

beforeEach(() => {
  vi.resetAllMocks()
  mocked.bookOrbitConfig.mockResolvedValue(saved)
  mocked.bookOrbitImportStatus.mockResolvedValue(idle)
  mocked.bookOrbitImportRuns.mockResolvedValue([])
})

describe('BookOrbitTab', () => {
  it('never shows a saved key and sends none when the field is left blank', async () => {
    mocked.bookOrbitSetConfig.mockResolvedValue(saved)
    render(<BookOrbitTab />)
    const key = await screen.findByLabelText(/API key/)
    expect((key as HTMLInputElement).value).toBe('')
    expect((key as HTMLInputElement).type).toBe('password')

    fireEvent.change(screen.getByLabelText('BookOrbit path remap'), { target: { value: '/books:/srv/books' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(mocked.bookOrbitSetConfig).toHaveBeenCalledTimes(1))
    expect(mocked.bookOrbitSetConfig).toHaveBeenCalledWith({ baseUrl: 'http://bookorbit:3000', libraryIds: ['2'], pathRemap: '/books:/srv/books' })
  })

  it('loads libraries, shows their folders and saves the ticked ones', async () => {
    mocked.bookOrbitLibraries.mockResolvedValue([
      { id: '2', name: 'Fantasy', folders: ['/books/Fantasy'] },
      { id: '17', name: 'Romantasy', folders: ['/books/Romantasy'] },
    ])
    mocked.bookOrbitSetConfig.mockImplementation(async data => ({ ...saved, ...data, apiKeyConfigured: true }))
    render(<BookOrbitTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Load libraries' }))
    const romantasy = await screen.findByLabelText(/Romantasy/)
    expect(screen.getByText('/books/Romantasy')).toBeTruthy()
    expect((screen.getByLabelText(/Fantasy\b/) as HTMLInputElement).checked).toBe(true)
    expect((romantasy as HTMLInputElement).checked).toBe(false)

    fireEvent.click(romantasy)
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(mocked.bookOrbitSetConfig).toHaveBeenCalled())
    expect(mocked.bookOrbitSetConfig.mock.calls[0][0].libraryIds).toEqual(['2', '17'])
  })

  it('tests the connection and reports who the key is', async () => {
    mocked.bookOrbitTest.mockResolvedValue({ message: 'connected', username: 'reader', authenticationMethod: 'api_key', libraryCount: 2 })
    render(<BookOrbitTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText('Connected as reader. The key can see 2 libraries.')).toBeTruthy()
    expect(mocked.bookOrbitTest).toHaveBeenCalledWith(undefined)
  })

  it('blocks the test while the address is unsaved, since the server tests the saved one', async () => {
    render(<BookOrbitTab />)
    fireEvent.change(await screen.findByLabelText('Server URL'), { target: { value: 'http://elsewhere:3000' } })
    expect((screen.getByRole('button', { name: 'Test connection' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText(/Save the server URL before testing/)).toBeTruthy()
  })

  it('previews as a dry run and imports only on the import button', async () => {
    mocked.bookOrbitImportStart.mockResolvedValue({ running: true, processed: 0, dryRun: true, message: 'reading BookOrbit libraries' })
    render(<BookOrbitTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Preview changes' }))
    await waitFor(() => expect(mocked.bookOrbitImportStart).toHaveBeenCalledWith({ dryRun: true }))
    expect(await screen.findByText('reading BookOrbit libraries')).toBeTruthy()
    // Nothing else can be started while it runs.
    expect((screen.getByRole('button', { name: 'Preview changes' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Running…' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('sends dryRun false from the import button', async () => {
    mocked.bookOrbitImportStart.mockResolvedValue({ running: true, processed: 0, dryRun: false })
    render(<BookOrbitTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Import libraries' }))
    await waitFor(() => expect(mocked.bookOrbitImportStart).toHaveBeenCalledWith({ dryRun: false }))
  })

  it('will not start a run over unsaved changes', async () => {
    render(<BookOrbitTab />)
    fireEvent.change(await screen.findByLabelText('BookOrbit path remap'), { target: { value: '/other:/place' } })
    expect((screen.getByRole('button', { name: 'Import libraries' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Preview changes' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText(/Save your changes before starting/)).toBeTruthy()
  })

  it('will not start a run before a library is selected', async () => {
    mocked.bookOrbitConfig.mockResolvedValue({ ...saved, libraryIds: [] })
    render(<BookOrbitTab />)
    expect(((await screen.findByRole('button', { name: 'Import libraries' })) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText(/Save a server URL, an API key and at least one library first/)).toBeTruthy()
  })

  it('shows a finished preview with its counts, missing files and sample', async () => {
    mocked.bookOrbitImportStatus.mockResolvedValue({
      running: false, processed: 8, dryRun: true, stats: stats(),
      results: [{ itemId: '6', title: 'The Sign of the Four', outcome: 'created', message: 'ebook path "/books/x.epub" is not visible to Bindery; imported metadata only' }],
    })
    render(<BookOrbitTab />)
    expect(await screen.findByText('Preview complete. Nothing was changed.')).toBeTruthy()
    expect(screen.getByText('Files to register').nextElementSibling?.textContent).toBe('9')
    expect(screen.getByText('Files not found').nextElementSibling?.textContent).toBe('1')
    expect(screen.getByText('Books listed by BookOrbit').nextElementSibling?.textContent).toBe('8')
    expect(screen.getByText('The Sign of the Four')).toBeTruthy()
    expect(screen.getByText(/is not visible to Bindery/)).toBeTruthy()
  })

  it('shows why a run failed', async () => {
    mocked.bookOrbitImportStatus.mockResolvedValue({ running: false, processed: 0, dryRun: false, error: 'BookOrbit answered GET /api/v1/books/42 in a shape this importer does not understand' })
    render(<BookOrbitTab />)
    expect(await screen.findByText(/Import failed: BookOrbit answered GET \/api\/v1\/books\/42/)).toBeTruthy()
  })

  it('surfaces a refusal to start', async () => {
    mocked.bookOrbitImportStart.mockRejectedValue(new Error('another library import is running; wait for it to finish'))
    render(<BookOrbitTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Preview changes' }))
    expect(await screen.findByText(/another library import is running/)).toBeTruthy()
  })

  it('lists run history and pages through it', async () => {
    const firstPage = Array.from({ length: 11 }, (_, i) => run(100 - i, { dryRun: i === 0 }))
    mocked.bookOrbitImportRuns.mockResolvedValueOnce(firstPage).mockResolvedValueOnce([run(5, { status: 'failed', summary: { dryRun: false, resumedFromCheckpoint: false, stats: stats(), error: 'BookOrbit rejected the API key' } })])
    render(<BookOrbitTab />)
    await waitFor(() => expect(screen.getAllByText('Files registered').length).toBe(9))
    expect(mocked.bookOrbitImportRuns).toHaveBeenCalledWith({ limit: 11, offset: 0 })
    expect(screen.getAllByText(/Preview ·/).length).toBe(1)

    fireEvent.click(screen.getByRole('button', { name: 'Older' }))
    expect(await screen.findByText('BookOrbit rejected the API key')).toBeTruthy()
    expect(mocked.bookOrbitImportRuns).toHaveBeenLastCalledWith({ limit: 11, offset: 10 })
    expect((screen.getByRole('button', { name: 'Older' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Newer' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('shows library names after a reload without pressing Load libraries', async () => {
    mocked.bookOrbitConfig.mockResolvedValue({ ...saved, libraryIds: ['1'] })
    mocked.bookOrbitLibraries.mockResolvedValue([{ id: '1', name: 'Books', folders: ['/books'] }])
    mocked.bookOrbitImportRuns.mockResolvedValue([run(7, { libraryId: '1' })])
    render(<BookOrbitTab />)
    expect(await screen.findByLabelText(/Books/)).toBeTruthy()
    expect(await screen.findByText(/Import · Books/)).toBeTruthy()
    expect(screen.queryByText(/Library 1/)).toBeNull()
  })

  it('falls back to Library N when BookOrbit cannot be reached, and says nothing about it', async () => {
    mocked.bookOrbitConfig.mockResolvedValue({ ...saved, libraryIds: ['1'] })
    mocked.bookOrbitLibraries.mockRejectedValue(new Error('BookOrbit rejected the API key'))
    mocked.bookOrbitImportRuns.mockResolvedValue([run(7, { libraryId: '1' })])
    render(<BookOrbitTab />)
    expect(await screen.findByText(/Import · Library 1/)).toBeTruthy()
    expect(screen.getByLabelText(/Library 1/)).toBeTruthy()
    expect(screen.queryByText(/rejected the API key/)).toBeNull()
  })

  it('clears an earlier error when another action runs', async () => {
    mocked.bookOrbitLibraries
      .mockResolvedValueOnce([])
      .mockRejectedValueOnce(new Error('BookOrbit rejected the API key. It may be revoked.'))
    mocked.bookOrbitTest.mockResolvedValue({ message: 'connected', username: 'reader', authenticationMethod: 'api_key', libraryCount: 2 })
    render(<BookOrbitTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Load libraries' }))
    expect(await screen.findByText(/rejected the API key/)).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText('Connected as reader. The key can see 2 libraries.')).toBeTruthy()
    expect(screen.queryByText(/rejected the API key/)).toBeNull()

    mocked.bookOrbitLibraries.mockRejectedValueOnce(new Error('Host not found'))
    fireEvent.click(screen.getByRole('button', { name: 'Load libraries' }))
    expect(await screen.findByText('Host not found')).toBeTruthy()
    expect(screen.queryByText(/Connected as reader/)).toBeNull()
  })
})
