import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { acceptConfirm } from '../../test-utils'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) =>
      opts && typeof opts === 'object' ? `${key} ${JSON.stringify(opts)}` : key,
  }),
}))

vi.mock('../../api/client', () => ({
  api: {
    absConfig: vi.fn(),
    absSetConfig: vi.fn(),
    absTest: vi.fn(),
    absLibraries: vi.fn(),
    absImportStart: vi.fn(),
    absImportStatus: vi.fn(),
    absImportRuns: vi.fn(),
    absImportRollbackPreview: vi.fn(),
    absImportRollback: vi.fn(),
    absReviewItems: vi.fn(),
    approveAbsReviewItem: vi.fn(),
    resolveAbsReviewAuthor: vi.fn(),
    resolveAbsReviewBook: vi.fn(),
    dismissAbsReviewItem: vi.fn(),
    dismissAbsReviewRun: vi.fn(),
    absConflicts: vi.fn(),
    resolveAbsConflict: vi.fn(),
    relinkAuthorUpstream: vi.fn(),
    searchAuthors: vi.fn(),
    searchBooks: vi.fn(),
  },
}))

import { api, type ABSConfig, type ABSImportRun, type ABSImportStats, type ABSMetadataConflict, type ABSReviewItem } from '../../api/client'
import ABSTab from './ABSTab'

const mocked = api as unknown as Record<string, ReturnType<typeof vi.fn>>

function config(overrides: Partial<ABSConfig> = {}): ABSConfig {
  return {
    featureEnabled: true,
    baseUrl: '',
    label: '',
    enabled: false,
    libraryId: '',
    pathRemap: '',
    apiKeyConfigured: false,
    ...overrides,
  }
}

const savedConfig = config({
  baseUrl: 'http://abs:13378',
  label: 'Home ABS',
  enabled: true,
  libraryId: 'lib-1',
  libraryIds: ['lib-1'],
  apiKeyConfigured: true,
})

function stats(overrides: Partial<ABSImportStats> = {}): ABSImportStats {
  return {
    librariesScanned: 1, pagesScanned: 1, itemsSeen: 4, itemsNormalized: 4, itemsDetailFetched: 4,
    authorsCreated: 2, authorsLinked: 1, booksCreated: 3, booksLinked: 1, booksUpdated: 0,
    seriesCreated: 1, seriesLinked: 0, editionsAdded: 3, ownedMarked: 2, pendingManual: 0,
    reviewQueued: 1, metadataMatched: 0, metadataRelinked: 0, metadataConflicts: 0,
    metadataAutoResolved: 0, skipped: 0, failed: 0,
    ...overrides,
  }
}

function absRun(id: number, overrides: Partial<ABSImportRun> = {}): ABSImportRun {
  return {
    id,
    sourceId: 'default',
    sourceLabel: 'Home ABS',
    baseUrl: 'http://abs:13378',
    libraryId: 'lib-1',
    status: 'completed',
    dryRun: false,
    startedAt: '2026-09-01T10:00:00Z',
    source: { sourceId: 'default', label: 'Home ABS', baseUrl: 'http://abs:13378', libraryId: 'lib-1', enabled: true, dryRun: false },
    summary: { dryRun: false, resumedFromCheckpoint: false, stats: stats() },
    ...overrides,
  }
}

function reviewItem(id: number, overrides: Partial<ABSReviewItem> = {}): ABSReviewItem {
  return {
    id,
    sourceId: 'default',
    libraryId: 'lib-1',
    itemId: `item-${id}`,
    title: `Title ${id}`,
    primaryAuthor: `Author ${id}`,
    asin: '',
    mediaType: 'book',
    reviewReason: 'unmatched_author',
    payloadJson: '{}',
    fileMappingFound: false,
    status: 'pending',
    createdAt: '2026-09-01T10:00:00Z',
    updatedAt: '2026-09-01T10:00:00Z',
    ...overrides,
  }
}

function conflict(id: number, overrides: Partial<ABSMetadataConflict> = {}): ABSMetadataConflict {
  return {
    id,
    sourceId: 'default',
    libraryId: 'lib-1',
    itemId: 'item-1',
    entityType: 'author',
    localId: 40 + id,
    entityName: `Entity ${id}`,
    fieldName: 'description',
    fieldLabel: 'Description',
    absValue: 'from abs',
    upstreamValue: 'from upstream',
    appliedSource: 'upstream',
    appliedValue: 'from upstream',
    preferredSource: '',
    authorRelinkEligible: true,
    resolutionStatus: 'unresolved',
    updatedAt: '2026-09-01T10:00:00Z',
    ...overrides,
  }
}

const page = <T,>(items: T[]) => ({ items, total: items.length, limit: 50, offset: 0 })

async function loaded() {
  await screen.findByText('Libraries')
}

describe('ABSTab coverage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocked.absConfig.mockResolvedValue(config())
    mocked.absLibraries.mockResolvedValue([])
    mocked.absImportStatus.mockResolvedValue({ running: false, processed: 0 })
    mocked.absImportRuns.mockResolvedValue([])
    mocked.absReviewItems.mockResolvedValue(page([]))
    mocked.absConflicts.mockResolvedValue(page([]))
  })

  it('shows a loading state, then the empty states', async () => {
    render(<ABSTab />)
    expect(screen.getByText(/Loading Audiobookshelf/)).toBeInTheDocument()
    await loaded()
    expect(screen.getByText(/No libraries selected/)).toBeInTheDocument()
    expect(screen.getByText('No ABS import runs recorded yet.')).toBeInTheDocument()
    expect(screen.getByText('No queued ABS review items.')).toBeInTheDocument()
    expect(screen.getByText('No author conflicts recorded yet.')).toBeInTheDocument()
    expect(screen.getByText('No book conflicts recorded yet.')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Paste an ABS API key')).toBeInTheDocument()
    // Nothing saved yet, so no library probe and no import.
    expect(mocked.absLibraries).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Preview changes' })).toBeDisabled()
  })

  it('shows the config load failure and the review and conflict load failures', async () => {
    mocked.absConfig.mockRejectedValue(new Error('config unavailable'))
    mocked.absReviewItems.mockRejectedValue(new Error('review unavailable'))
    mocked.absConflicts.mockRejectedValue(new Error('conflicts unavailable'))
    mocked.absImportRuns.mockRejectedValue(new Error('runs unavailable'))
    render(<ABSTab />)
    expect(await screen.findByText('config unavailable')).toBeInTheDocument()
    expect(await screen.findByText('review unavailable')).toBeInTheDocument()
    expect(await screen.findByText('conflicts unavailable')).toBeInTheDocument()
    expect(screen.getByText('No ABS import runs recorded yet.')).toBeInTheDocument()
  })

  it('loads saved libraries, edits the source, and saves the full payload', async () => {
    mocked.absConfig.mockResolvedValue(savedConfig)
    mocked.absLibraries.mockResolvedValue([
      { id: 'lib-1', name: 'Audiobooks', mediaType: 'book', icon: '', provider: 'audible', folders: [] },
      { id: 'lib-2', name: '', mediaType: 'book', icon: '', provider: '', folders: [] },
    ])
    mocked.absSetConfig.mockImplementation((data: Partial<ABSConfig>) => Promise.resolve({ ...savedConfig, ...data, apiKeyConfigured: true }))
    render(<ABSTab />)

    expect(await screen.findByText('audible · lib-1')).toBeInTheDocument()
    expect(mocked.absLibraries).toHaveBeenCalledWith()
    expect(screen.getByText('1 selected')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Saved key is hidden. Enter a new key to rotate it.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Preview changes' })).toBeEnabled()
    expect(screen.getByText(/For better initial matching/)).toBeInTheDocument()

    const checkboxes = screen.getAllByRole('checkbox').filter(c => c.closest('label')?.textContent?.includes('lib-'))
    fireEvent.click(checkboxes[1])
    expect(screen.getByText('2 selected')).toBeInTheDocument()
    // Unsaved edits block the import until saved.
    expect(screen.getByText(/so the run uses the stored source configuration/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Preview changes' })).toBeDisabled()

    fireEvent.change(screen.getByPlaceholderText('Audiobookshelf'), { target: { value: '  ' } })
    fireEvent.change(screen.getByPlaceholderText('http://audiobookshelf:13378'), { target: { value: ' http://abs2:13378 ' } })
    fireEvent.change(screen.getByPlaceholderText('Saved key is hidden. Enter a new key to rotate it.'), { target: { value: ' new-key ' } })
    fireEvent.change(screen.getByLabelText('ABS path remap'), { target: { value: ' /abs:/books ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save source' }))

    await waitFor(() => expect(mocked.absSetConfig).toHaveBeenCalledWith({
      baseUrl: 'http://abs2:13378',
      apiKey: 'new-key',
      label: 'Audiobookshelf',
      enabled: true,
      libraryId: 'lib-1',
      libraryIds: ['lib-1', 'lib-2'],
      pathRemap: '/abs:/books',
    }))
    await waitFor(() => expect(mocked.absLibraries).toHaveBeenLastCalledWith({ baseUrl: 'http://abs2:13378', apiKey: 'new-key' }))
  })

  it('shows a save failure', async () => {
    mocked.absSetConfig.mockRejectedValue(new Error('invalid base URL'))
    render(<ABSTab />)
    await loaded()
    fireEvent.click(screen.getByRole('button', { name: 'Enable Audiobookshelf source' }))
    expect(screen.getByRole('button', { name: 'Disable Audiobookshelf source' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Save source' }))
    expect(await screen.findByText('invalid base URL')).toBeInTheDocument()
    expect(mocked.absSetConfig).toHaveBeenCalledWith(expect.objectContaining({ enabled: true, libraryId: '', libraryIds: [], apiKey: undefined }))
  })

  it('tests the connection, adopts the default library, and lists libraries', async () => {
    mocked.absTest.mockResolvedValue({
      message: 'ok', username: 'listener', userType: 'user', defaultLibraryId: 'lib-9', serverVersion: '2.17.0', source: 'abs',
    })
    mocked.absLibraries.mockResolvedValue([{ id: 'lib-9', name: 'Main', mediaType: 'book', icon: '', provider: '', folders: [] }])
    render(<ABSTab />)
    await loaded()
    fireEvent.change(screen.getByPlaceholderText('http://audiobookshelf:13378'), { target: { value: 'http://abs:13378' } })
    fireEvent.change(screen.getByPlaceholderText('Paste an ABS API key'), { target: { value: 'k' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/on ABS 2\.17\.0 · default library lib-9/)).toBeInTheDocument()
    expect(screen.getByText('listener')).toBeInTheDocument()
    expect(mocked.absTest).toHaveBeenCalledWith({ baseUrl: 'http://abs:13378', apiKey: 'k' })
    await waitFor(() => expect(mocked.absLibraries).toHaveBeenCalledWith({ baseUrl: 'http://abs:13378', apiKey: 'k' }))
    expect(await screen.findByText('Main')).toBeInTheDocument()
    expect(screen.getByText('1 selected')).toBeInTheDocument()
  })

  it('shows connection test and library listing failures', async () => {
    mocked.absTest.mockRejectedValue(new Error('401 unauthorized'))
    mocked.absLibraries.mockRejectedValue(new Error('cannot list libraries'))
    render(<ABSTab />)
    await loaded()
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText('401 unauthorized')).toBeInTheDocument()
    expect(mocked.absTest).toHaveBeenCalledWith({ baseUrl: '' })

    fireEvent.click(screen.getByRole('button', { name: 'List libraries' }))
    // The library error shows both in the library box and the form.
    expect(await screen.findAllByText('cannot list libraries')).toHaveLength(2)
  })

  it('asks for a library when credentials exist but none is chosen', async () => {
    mocked.absConfig.mockResolvedValue(config({ baseUrl: 'http://abs:13378', enabled: true, apiKeyConfigured: true }))
    render(<ABSTab />)
    expect(await screen.findByText(/Choose at least one library/)).toBeInTheDocument()
  })

  it('starts a dry run, then a live import, and shows the finished results', async () => {
    mocked.absConfig.mockResolvedValue(savedConfig)
    mocked.absImportStart
      .mockResolvedValueOnce({
        running: false, dryRun: true, processed: 3, finishedAt: '2026-09-01T10:05:00Z', stats: stats(),
        results: [
          { itemId: 'i-1', title: 'Dune', outcome: 'created', message: 'new book' },
          { itemId: 'i-2', title: '', outcome: 'linked', bookId: 7 },
        ],
      })
      .mockRejectedValueOnce(new Error('import already running'))
    render(<ABSTab />)
    await loaded()

    fireEvent.click(screen.getByRole('button', { name: 'Preview changes' }))
    expect(await screen.findByText(/Dry-run complete/)).toBeInTheDocument()
    expect(mocked.absImportStart).toHaveBeenCalledWith({ dryRun: true })
    expect(screen.getByText('Dune')).toBeInTheDocument()
    expect(screen.getByText('new book')).toBeInTheDocument()
    expect(screen.getByText('i-2')).toBeInTheDocument()
    expect(screen.getByText('2 created, 1 linked.')).toBeInTheDocument()

    expect(screen.getByRole('button', { name: 'Run selected mode' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('checkbox', { name: 'Start with dry-run' }))
    fireEvent.click(screen.getByRole('button', { name: 'Import libraries' }))
    expect(await screen.findByText('import already running')).toBeInTheDocument()
    expect(mocked.absImportStart).toHaveBeenLastCalledWith({ dryRun: false })
  })

  it('shows a resumed import while it runs and a failed one', async () => {
    mocked.absConfig.mockResolvedValue(savedConfig)
    mocked.absImportStatus
      .mockResolvedValueOnce({
        running: true, processed: 12, message: 'Scanning page 3', resumedFromCheckpoint: true,
        checkpoint: { libraryId: 'lib-1', page: 3, lastItemId: 'item-77', pageSize: 50, updatedAt: '' },
      })
      .mockResolvedValue({ running: false, processed: 12, dryRun: false, error: 'ABS went away', finishedAt: '2026-09-01T10:05:00Z' })
    render(<ABSTab />)
    expect(await screen.findByText('Scanning page 3')).toBeInTheDocument()
    expect(screen.getByText('12 processed')).toBeInTheDocument()
    expect(screen.getByText('Resumed from page 3 after item-77.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Running…' })).toBeDisabled()

    // The status poll picks up the failure.
    expect(await screen.findByText('Import failed: ABS went away', {}, { timeout: 10000 })).toBeInTheDocument()
  }, 30000)

  it('lists runs and previews and applies a rollback', async () => {
    mocked.absConfig.mockResolvedValue(savedConfig)
    mocked.absLibraries.mockResolvedValue([{ id: 'lib-1', name: 'Audiobooks', mediaType: 'book', icon: '', provider: '', folders: [] }])
    mocked.absImportRuns.mockResolvedValue([
      absRun(5, { checkpoint: { libraryId: 'lib-1', page: 2, pageSize: 50, updatedAt: '' } }),
      absRun(4, { dryRun: true, status: 'needs_review' }),
      absRun(3, { status: 'rolled_back', libraryId: '', source: { ...absRun(3).source, libraryId: '', label: '' } }),
      absRun(2, { status: 'failed', libraryId: 'lib-x', summary: { dryRun: false, resumedFromCheckpoint: false, stats: stats(), error: 'ABS 500' } }),
    ])
    const actions = [
      { entityType: 'author', externalId: 'a', localId: 1, outcome: '', action: 'delete_author', displayName: 'Frank' },
      { entityType: 'book', externalId: 'b', localId: 2, outcome: '', action: 'delete_book', reason: 'created by run' },
      { entityType: 'edition', externalId: 'c', localId: 0, outcome: '', action: 'delete_edition' },
      { entityType: 'series', externalId: 'd', localId: 4, outcome: '', action: 'delete_series' },
      { entityType: 'book', externalId: 'e', localId: 5, outcome: '', action: 'restore_book' },
      { entityType: 'series', externalId: 'f', localId: 6, outcome: '', action: 'unlink_series' },
      { entityType: 'book', externalId: 'g', localId: 7, outcome: '', action: 'unlink_provenance' },
      { entityType: 'book', externalId: 'h', localId: 8, outcome: '', action: 'reset_owned_flag' },
      { entityType: 'author', externalId: 'i', localId: 9, outcome: '', action: 'skip', displayName: ' ' },
    ]
    const rollbackStats = { actionsPlanned: 9, entitiesDeleted: 4, provenanceUnlinked: 1, skipped: 1, failed: 0 }
    mocked.absImportRollbackPreview.mockResolvedValue({ runId: 5, preview: true, dryRun: false, status: 'completed', stats: rollbackStats, actions, finishedAt: '' })
    mocked.absImportRollback.mockResolvedValue({ runId: 5, preview: false, dryRun: false, status: 'rolled_back', stats: rollbackStats, actions: [], finishedAt: '' })
    render(<ABSTab />)

    expect(await screen.findByText('Run #5 · Live import · completed')).toBeInTheDocument()
    expect(screen.getByText('Run #4 · Dry-run · needs review')).toBeInTheDocument()
    expect(screen.getByText('Run #3 · Live import · rolled back')).toBeInTheDocument()
    expect(screen.getByText('Run #2 · Live import · failed')).toBeInTheDocument()
    expect(screen.getByText('ABS 500')).toBeInTheDocument()
    expect(screen.getByText('Last checkpoint: page 2.')).toBeInTheDocument()
    expect(screen.getAllByText(/Home ABS · Audiobooks \(lib-1\)/)).toHaveLength(2)
    expect(screen.getByText(/Home ABS · Unknown library/)).toBeInTheDocument()
    expect(screen.getByText(/lib-x$/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Rolled back' })).toBeDisabled()
    // Dry runs have nothing to roll back.
    expect(screen.getAllByRole('button', { name: 'Preview rollback' })).toHaveLength(3)

    fireEvent.click(screen.getAllByRole('button', { name: 'Preview rollback' })[0])
    expect(await screen.findByText('Rollback preview')).toBeInTheDocument()
    expect(mocked.absImportRollbackPreview).toHaveBeenCalledWith(5)
    for (const label of [
      'Delete author Frank', 'Delete book b', 'Delete edition c', 'Delete series d', 'Restore book metadata e',
      'Remove series link f', 'Remove ABS link g', 'reset owned flag h', 'Retain author i',
    ]) {
      expect(screen.getByText(label)).toBeInTheDocument()
    }
    expect(screen.getByText('created by run')).toBeInTheDocument()
    expect(screen.getByText('edition')).toBeInTheDocument()
    expect(screen.getByText('author #1')).toBeInTheDocument()

    fireEvent.click(screen.getAllByRole('button', { name: 'Apply rollback' })[0])
    expect(await screen.findByText('Rollback result')).toBeInTheDocument()
    expect(mocked.absImportRollback).toHaveBeenCalledWith(5)
    expect(screen.getByText('No entities would be deleted or changed.')).toBeInTheDocument()
    expect(screen.getByText('No retained entities reported.')).toBeInTheDocument()
    await waitFor(() => expect(mocked.absImportRuns.mock.calls.length).toBeGreaterThanOrEqual(3))

    fireEvent.click(screen.getByRole('button', { name: 'Refresh runs' }))
    await waitFor(() => expect(mocked.absImportRuns.mock.calls.length).toBeGreaterThanOrEqual(4))
  })

  it('shows rollback preview and apply failures', async () => {
    mocked.absImportRuns.mockResolvedValue([absRun(5)])
    mocked.absImportRollbackPreview.mockRejectedValue(new Error('preview broke'))
    mocked.absImportRollback.mockRejectedValue(new Error('apply broke'))
    render(<ABSTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Preview rollback' }))
    expect(await screen.findByText('preview broke')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Apply rollback' }))
    expect(await screen.findByText('apply broke')).toBeInTheDocument()
  })

  it('groups review items by run and approves and dismisses them', async () => {
    mocked.absReviewItems.mockResolvedValue(page([
      reviewItem(1, { latestRunId: 3, asin: 'B00ASIN', fileMappingFound: true, fileMappingMessage: 'found at /books/x', resolvedAuthorName: 'Ann Leckie', resolvedBookTitle: 'Ancillary Justice' }),
      reviewItem(2, { latestRunId: 7, reviewReason: 'ambiguous_author', primaryAuthor: '' }),
      reviewItem(3, { latestRunId: null, reviewReason: 'unmatched_book', title: '' }),
      reviewItem(4, { latestRunId: 3, reviewReason: 'ambiguous_book' }),
      reviewItem(5, { reviewReason: 'other_reason' as ABSReviewItem['reviewReason'] }),
    ]))
    mocked.approveAbsReviewItem.mockResolvedValue({})
    mocked.dismissAbsReviewItem.mockResolvedValue({})
    render(<ABSTab />)

    const headings = await screen.findAllByText(/settings\.abs\.review\.(runHeading|unknownRunHeading)/)
    // Newest run first, items with no run last.
    expect(headings.map(h => h.textContent)).toEqual([
      expect.stringContaining('"runId":7'),
      expect.stringContaining('"runId":3'),
      expect.stringContaining('unknownRunHeading'),
    ])
    expect(screen.getByText('ASIN B00ASIN')).toBeInTheDocument()
    expect(screen.getByText('File Mapping Found')).toHaveAttribute('title', 'found at /books/x')
    expect(screen.getByText('Author: Ann Leckie')).toBeInTheDocument()
    expect(screen.getByText('Book: Ancillary Justice')).toBeInTheDocument()
    expect(screen.getByText('No confident author match')).toBeInTheDocument()
    expect(screen.getByText('Multiple author matches')).toBeInTheDocument()
    expect(screen.getByText('No confident book match')).toBeInTheDocument()
    expect(screen.getByText('Multiple book matches')).toBeInTheDocument()
    expect(screen.getByText('other_reason')).toBeInTheDocument()
    expect(screen.getByText('Unknown author')).toBeInTheDocument()
    expect(screen.getByText('item-3')).toBeInTheDocument()

    const callsBefore = mocked.absReviewItems.mock.calls.length
    fireEvent.click(screen.getAllByRole('button', { name: 'Import' })[0])
    await waitFor(() => expect(mocked.approveAbsReviewItem).toHaveBeenCalledWith(2))
    await waitFor(() => expect(mocked.absReviewItems.mock.calls.length).toBe(callsBefore + 1))

    fireEvent.click(screen.getAllByRole('button', { name: 'Dismiss' })[0])
    await waitFor(() => expect(mocked.dismissAbsReviewItem).toHaveBeenCalledWith(2))

    // Collapsing the section hides the items.
    fireEvent.click(screen.getByRole('button', { name: /No-match books/ }))
    expect(screen.queryByText('ASIN B00ASIN')).not.toBeInTheDocument()
  })

  it('shows review action failures', async () => {
    mocked.absReviewItems.mockResolvedValue(page([reviewItem(1, { latestRunId: 4 })]))
    mocked.approveAbsReviewItem.mockRejectedValue(new Error('approve failed'))
    mocked.dismissAbsReviewItem.mockRejectedValue(new Error('dismiss failed'))
    mocked.dismissAbsReviewRun.mockRejectedValue(new Error('run dismiss failed'))
    mocked.searchAuthors.mockRejectedValue(new Error('author search down'))
    mocked.searchBooks.mockRejectedValue(new Error('book search down'))
    render(<ABSTab />)

    fireEvent.click(await screen.findByRole('button', { name: 'Import' }))
    expect(await screen.findByText('approve failed')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(await screen.findByText('dismiss failed')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /settings\.abs\.review\.dismissRun/ }))
    await acceptConfirm()
    expect(await screen.findByText('run dismiss failed')).toBeInTheDocument()
    expect(mocked.dismissAbsReviewRun).toHaveBeenCalledWith(4)

    fireEvent.keyDown(screen.getByDisplayValue('Author 1'), { key: 'Enter' })
    expect(await screen.findByText('author search down')).toBeInTheDocument()
    expect(mocked.searchAuthors).toHaveBeenCalledWith('Author 1')
    fireEvent.keyDown(screen.getByDisplayValue('Title 1'), { key: 'Enter' })
    expect(await screen.findByText('book search down')).toBeInTheDocument()
    expect(mocked.searchBooks).toHaveBeenCalledWith('Title 1')
  })

  it('resolves a review author from search results and reports a failed book resolve', async () => {
    mocked.absReviewItems.mockResolvedValue(page([reviewItem(1, { editedTitle: 'Edited Title' })]))
    mocked.searchAuthors.mockResolvedValue([
      { id: 1, foreignAuthorId: 'OL1A', authorName: 'Ann Leckie', disambiguation: 'b. 1966' },
    ])
    mocked.resolveAbsReviewAuthor.mockResolvedValueOnce({ updated: 1 }).mockRejectedValueOnce(new Error('author resolve failed'))
    mocked.searchBooks.mockResolvedValue([{ id: 9, foreignBookId: 'OL9W', title: 'Ancillary Justice' }])
    mocked.resolveAbsReviewBook.mockRejectedValue(new Error('book resolve failed'))
    render(<ABSTab />)

    const authorInput = await screen.findByDisplayValue('Author 1')
    fireEvent.change(authorInput, { target: { value: '  Leckie ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Author' }))
    await waitFor(() => expect(mocked.searchAuthors).toHaveBeenCalledWith('Leckie'))
    expect(screen.getByText('b. 1966')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Ann Leckie/ }))
    await waitFor(() => expect(mocked.resolveAbsReviewAuthor).toHaveBeenCalledWith(1, {
      foreignAuthorId: 'OL1A', authorName: 'Ann Leckie', applyTo: 'same_author',
    }))
    await waitFor(() => expect(screen.queryByText('b. 1966')).not.toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Author' }))
    fireEvent.click(await screen.findByRole('button', { name: /Ann Leckie/ }))
    expect(await screen.findByText('author resolve failed')).toBeInTheDocument()

    // The edited title seeds the book search; a book without an author skips the author link.
    expect(screen.getByDisplayValue('Edited Title')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Book' }))
    await waitFor(() => expect(mocked.searchBooks).toHaveBeenCalledWith('Edited Title'))
    fireEvent.click(await screen.findByRole('button', { name: /Ancillary Justice/ }))
    expect(await screen.findByText('book resolve failed')).toBeInTheDocument()
    expect(mocked.resolveAbsReviewBook).toHaveBeenCalledWith(1, { foreignBookId: 'OL9W', title: 'Ancillary Justice', editedTitle: 'Edited Title' })
    expect(mocked.resolveAbsReviewAuthor).toHaveBeenCalledTimes(2)
  })

  it('skips the search when the query is blank', async () => {
    mocked.absReviewItems.mockResolvedValue(page([reviewItem(1, { primaryAuthor: '', title: '' })]))
    render(<ABSTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Author' }))
    fireEvent.click(screen.getByRole('button', { name: 'Book' }))
    expect(mocked.searchAuthors).not.toHaveBeenCalled()
    expect(mocked.searchBooks).not.toHaveBeenCalled()
  })

  it('resolves and relinks conflicts, and shows their failures', async () => {
    mocked.absConflicts.mockResolvedValue(page([
      conflict(1),
      conflict(2, { entityType: 'book', entityName: 'Book Entity', authorRelinkEligible: false }),
    ]))
    mocked.resolveAbsConflict.mockImplementation((id: number, source: 'abs' | 'upstream') =>
      Promise.resolve(conflict(id, { resolutionStatus: 'resolved', appliedSource: source })))
    mocked.relinkAuthorUpstream.mockResolvedValueOnce({}).mockRejectedValueOnce(new Error('relink failed'))
    render(<ABSTab />)

    expect(await screen.findByText('Entity 1')).toBeInTheDocument()
    expect(screen.getByText('Book Entity')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Attempt upstream relink' }))
    await waitFor(() => expect(mocked.relinkAuthorUpstream).toHaveBeenCalledWith(41))
    await waitFor(() => expect(mocked.absConflicts.mock.calls.length).toBeGreaterThanOrEqual(3))
    fireEvent.click(screen.getByRole('button', { name: 'Attempt upstream relink' }))
    expect(await screen.findByText('relink failed')).toBeInTheDocument()

    fireEvent.click(screen.getAllByRole('button', { name: 'Use ABS' })[0])
    await waitFor(() => expect(mocked.resolveAbsConflict).toHaveBeenCalledWith(1, 'abs'))

    mocked.resolveAbsConflict.mockRejectedValueOnce(new Error('resolve failed'))
    fireEvent.click(screen.getByRole('button', { name: 'Use upstream' }))
    expect(await screen.findByText('resolve failed')).toBeInTheDocument()
    expect(mocked.resolveAbsConflict).toHaveBeenLastCalledWith(2, 'upstream')

    // Collapse both panels.
    const bookPanel = screen.getByRole('button', { name: /Book conflicts/ })
    fireEvent.click(bookPanel)
    expect(bookPanel).toHaveAttribute('aria-expanded', 'false')
    const authorPanel = screen.getByRole('button', { name: /Author conflicts/ })
    fireEvent.click(authorPanel)
    expect(authorPanel).toHaveAttribute('aria-expanded', 'false')
  })

  it('refreshes review items and conflicts on demand', async () => {
    render(<ABSTab />)
    await loaded()
    await waitFor(() => expect(mocked.absReviewItems).toHaveBeenCalled())
    const reviewBefore = mocked.absReviewItems.mock.calls.length
    const conflictsBefore = mocked.absConflicts.mock.calls.length
    const refreshButtons = screen.getAllByRole('button', { name: 'Refresh' })
    for (const button of refreshButtons) fireEvent.click(button)
    await waitFor(() => expect(mocked.absReviewItems.mock.calls.length).toBe(reviewBefore + 1))
    await waitFor(() => expect(mocked.absConflicts.mock.calls.length).toBe(conflictsBefore + 2))
    expect(within(document.body).getAllByRole('button', { name: 'Refresh' })).toHaveLength(3)
  })
})
