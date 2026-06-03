import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import SettingsPage from './SettingsPage'
import { api, type ABSImportRun, type DownloadClient, type HardcoverList, type ImportList, type Indexer, type OidcProvider, type ProwlarrInstance, type RootFolder, type SystemStatus } from '../api/client'

const mockAuthContext = vi.hoisted(() => ({
  status: {
    authenticated: true,
    setupRequired: false,
    username: 'admin',
    role: 'admin',
    mode: 'enabled' as const,
  },
  loading: false,
  isAdmin: true,
  refresh: vi.fn(),
  logout: vi.fn(),
}))

vi.mock('../components/ThemeToggle', () => ({ default: () => <button type="button">Theme</button> }))
vi.mock('../components/LanguageSwitcher', () => ({ default: () => <select aria-label="Language" /> }))
vi.mock('../auth/AuthContext', () => ({
  useAuth: () => mockAuthContext,
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => typeof fallback === 'string' ? fallback : key,
    i18n: { changeLanguage: vi.fn() },
  }),
}))
vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listIndexers: vi.fn(),
      addIndexer: vi.fn(),
      updateIndexer: vi.fn(),
      deleteIndexer: vi.fn(),
      testIndexer: vi.fn(),
      listDownloadClients: vi.fn(),
      addDownloadClient: vi.fn(),
      updateDownloadClient: vi.fn(),
      deleteDownloadClient: vi.fn(),
      testDownloadClient: vi.fn(),
      listProwlarr: vi.fn(),
      addProwlarr: vi.fn(),
      syncProwlarr: vi.fn(),
      testProwlarr: vi.fn(),
      deleteProwlarr: vi.fn(),
      absConfig: vi.fn(),
      absSetConfig: vi.fn(),
      absLibraries: vi.fn(),
      absImportStart: vi.fn(),
      absImportStatus: vi.fn(),
      absImportRuns: vi.fn(),
      absReviewItems: vi.fn(),
      absConflicts: vi.fn(),
      listSettings: vi.fn(),
      listBackups: vi.fn(),
      libraryScanStatus: vi.fn(),
      getStorage: vi.fn(),
      listRootFolders: vi.fn(),
      status: vi.fn(),
      setSetting: vi.fn(),
      testHardcover: vi.fn(),
      listImportLists: vi.fn(),
      addImportList: vi.fn(),
      updateImportList: vi.fn(),
      deleteImportList: vi.fn(),
      syncImportList: vi.fn(),
      hardcoverLists: vi.fn(),
      triggerLibraryScan: vi.fn(),
      createBackup: vi.fn(),
      deleteBackup: vi.fn(),
      addRootFolder: vi.fn(),
      authConfig: vi.fn(),
      authSetMode: vi.fn(),
      authRegenerateApiKey: vi.fn(),
      authChangePassword: vi.fn(),
      oidcProviders: vi.fn(),
      oidcSetProviders: vi.fn(),
      oidcRedirectBase: vi.fn(),
      oidcTestDiscovery: vi.fn(),
    },
  }
})

const defaultStatus: SystemStatus = {
  version: 'dev',
  commit: 'unknown',
  buildDate: '',
  enhancedHardcoverApi: false,
  hardcoverTokenConfigured: false,
  enhancedHardcoverDisabledReason: 'env_disabled',
}

function makeIndexer(overrides: Partial<Indexer> = {}): Indexer {
  return {
    id: 1,
    name: 'NZBGeek',
    type: 'newznab',
    url: 'https://nzbgeek.example.com',
    apiKey: 'indexer-key',
    categories: [7020],
    enabled: true,
    ...overrides,
  }
}

function makeProwlarr(overrides: Partial<ProwlarrInstance> = {}): ProwlarrInstance {
  return {
    id: 10,
    name: 'Prowlarr',
    url: 'http://prowlarr:9696',
    apiKey: 'prowlarr-key',
    syncOnStartup: true,
    enabled: true,
    ...overrides,
  }
}

function makeClient(overrides: Partial<DownloadClient> = {}): DownloadClient {
  return {
    id: 20,
    name: 'SABnzbd',
    type: 'sabnzbd',
    host: 'sabnzbd',
    port: 8080,
    apiKey: 'sab-key',
    username: '',
    password: '',
    useSsl: false,
    urlBase: '',
    category: 'books',
    pathRemap: '',
    enabled: true,
    ...overrides,
  }
}

function makeRootFolder(overrides: Partial<RootFolder> = {}): RootFolder {
  return {
    id: 1,
    path: '/books',
    freeSpace: 1024,
    createdAt: '2026-05-06T12:00:00Z',
    ...overrides,
  }
}

function makeABSImportRun(id: number, libraryId: string): ABSImportRun {
  return {
    id,
    sourceId: 'abs',
    sourceLabel: 'Shelf',
    baseUrl: 'https://abs.example.com',
    libraryId,
    status: 'completed',
    dryRun: false,
    startedAt: '2026-05-20T14:00:00Z',
    source: {
      sourceId: 'abs',
      label: 'Shelf',
      baseUrl: 'https://abs.example.com',
      libraryId,
      libraryIds: ['lib-books', 'lib-audio'],
      pathRemap: '',
      enabled: true,
      dryRun: false,
    },
    summary: {
      dryRun: false,
      resumedFromCheckpoint: false,
      stats: {
        librariesScanned: 1,
        pagesScanned: 1,
        itemsSeen: 0,
        itemsNormalized: 0,
        itemsDetailFetched: 0,
        authorsCreated: 0,
        authorsLinked: 0,
        booksCreated: 0,
        booksLinked: 0,
        booksUpdated: 0,
        seriesCreated: 0,
        seriesLinked: 0,
        editionsAdded: 0,
        ownedMarked: 0,
        pendingManual: 0,
        reviewQueued: 0,
        metadataMatched: 0,
        metadataRelinked: 0,
        metadataConflicts: 0,
        metadataAutoResolved: 0,
        skipped: 0,
        failed: 0,
      },
    },
  }
}

function makeImportList(overrides: Partial<ImportList> = {}): ImportList {
  return {
    id: 1,
    name: 'Want to Read',
    type: 'hardcover',
    url: 'want-to-read',
    apiKey: '',
    apiKeyConfigured: false,
    rootFolderId: null,
    qualityProfileId: null,
    monitorNew: true,
    autoAdd: true,
    enabled: false,
    lastSyncAt: null,
    createdAt: '2026-05-06T12:00:00Z',
    updatedAt: '2026-05-06T12:00:00Z',
    ...overrides,
  }
}

function makeHardcoverList(overrides: Partial<HardcoverList> = {}): HardcoverList {
  return {
    id: -1,
    name: 'Want to Read',
    slug: 'want-to-read',
    booksCount: 12,
    ...overrides,
  }
}

function seedSettingsMocks(options: {
  indexers?: Indexer[]
  clients?: DownloadClient[]
  prowlarr?: ProwlarrInstance[]
  status?: SystemStatus
  settings?: Array<{ key: string; value: string }>
  rootFolders?: RootFolder[]
  oidcProviders?: OidcProvider[]
  importLists?: ImportList[]
  hardcoverLists?: HardcoverList[]
} = {}) {
  vi.mocked(api.listIndexers).mockResolvedValue(options.indexers ?? [])
  vi.mocked(api.addIndexer).mockImplementation(async data => makeIndexer({ id: 100, ...data }))
  vi.mocked(api.updateIndexer).mockImplementation(async (id, data) => makeIndexer({ ...data, id }))
  vi.mocked(api.deleteIndexer).mockResolvedValue(undefined)
  vi.mocked(api.testIndexer).mockResolvedValue({ ok: true, status: 200, categories: 12, bookSearch: true, latencyMs: 34, searchResults: 2 })

  vi.mocked(api.listDownloadClients).mockResolvedValue(options.clients ?? [])
  vi.mocked(api.addDownloadClient).mockImplementation(async data => makeClient({ id: 200, ...data }))
  vi.mocked(api.updateDownloadClient).mockImplementation(async (id, data) => makeClient({ ...data, id }))
  vi.mocked(api.deleteDownloadClient).mockResolvedValue(undefined)
  vi.mocked(api.testDownloadClient).mockResolvedValue({ message: 'ok' })

  vi.mocked(api.listProwlarr).mockResolvedValue(options.prowlarr ?? [])
  vi.mocked(api.addProwlarr).mockImplementation(async data => makeProwlarr({ id: 300, ...data }))
  vi.mocked(api.syncProwlarr).mockResolvedValue({ added: 0, updated: 0, removed: 0 })
  vi.mocked(api.testProwlarr).mockResolvedValue({ ok: 'true', version: '1.0.0' })
  vi.mocked(api.deleteProwlarr).mockResolvedValue(undefined)

    vi.mocked(api.absConfig).mockResolvedValue({ featureEnabled: false, baseUrl: '', label: '', enabled: false, libraryId: '', libraryIds: [], pathRemap: '', apiKeyConfigured: false })
    vi.mocked(api.absSetConfig).mockResolvedValue({ featureEnabled: false, baseUrl: '', label: '', enabled: false, libraryId: '', libraryIds: [], pathRemap: '', apiKeyConfigured: false })
    vi.mocked(api.absLibraries).mockResolvedValue([])
    vi.mocked(api.absImportStart).mockResolvedValue({ running: true, dryRun: true, processed: 0 })
    vi.mocked(api.absImportStatus).mockResolvedValue({ running: false, processed: 0 })
    vi.mocked(api.absImportRuns).mockResolvedValue([])
    vi.mocked(api.absReviewItems).mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
    vi.mocked(api.absConflicts).mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
    vi.mocked(api.listSettings).mockResolvedValue(options.settings ?? [{ key: 'hardcover.enhanced_series_enabled', value: 'false' }])
    vi.mocked(api.listBackups).mockResolvedValue([])
    vi.mocked(api.libraryScanStatus).mockRejectedValue(new Error('no scan'))
    vi.mocked(api.getStorage).mockResolvedValue({ downloadDir: '/downloads', audiobookDownloadDir: '', libraryDir: '/books', audiobookDir: '' })
    vi.mocked(api.listRootFolders).mockResolvedValue(options.rootFolders ?? [])
    vi.mocked(api.status).mockResolvedValue(options.status ?? defaultStatus)
    vi.mocked(api.setSetting).mockResolvedValue(undefined)
    vi.mocked(api.listImportLists).mockResolvedValue(options.importLists ?? [])
    vi.mocked(api.addImportList).mockImplementation(async data => makeImportList({ id: 900, ...data }))
    vi.mocked(api.updateImportList).mockImplementation(async (id, data) => makeImportList({ id, ...data }))
    vi.mocked(api.deleteImportList).mockResolvedValue(undefined)
    vi.mocked(api.syncImportList).mockResolvedValue({ status: 'ok' })
    vi.mocked(api.hardcoverLists).mockResolvedValue(options.hardcoverLists ?? [])
    vi.mocked(api.triggerLibraryScan).mockResolvedValue({ message: 'started' })
    vi.mocked(api.createBackup).mockResolvedValue({ name: 'bindery-backup.zip', size: 0, modTime: '' })
    vi.mocked(api.deleteBackup).mockResolvedValue(undefined)
    vi.mocked(api.addRootFolder).mockImplementation(async path => makeRootFolder({ id: 99, path }))
    vi.mocked(api.testHardcover).mockResolvedValue({
      ok: true,
      tokenConfigured: true,
      searchResults: 2,
      sampleSeriesId: 'hc-series:1150',
      sampleTitle: 'Dune',
      catalogOk: true,
      catalogBookCount: 8,
      message: 'Found 2 series; catalog "Dune" has 8 books',
    })
    vi.mocked(api.authConfig).mockResolvedValue({ mode: 'enabled', apiKey: 'key', username: 'admin' })
    vi.mocked(api.authSetMode).mockResolvedValue({ mode: 'enabled' })
    vi.mocked(api.authRegenerateApiKey).mockResolvedValue({ apiKey: 'rotated-key' })
    vi.mocked(api.authChangePassword).mockResolvedValue({ ok: true })
    vi.mocked(api.oidcProviders).mockResolvedValue(options.oidcProviders ?? [])
    vi.mocked(api.oidcSetProviders).mockResolvedValue(undefined)
    vi.mocked(api.oidcRedirectBase).mockResolvedValue({ base: 'http://localhost', callback_path: '/api/v1/auth/oidc/{id}/callback', configured: true })
    vi.mocked(api.oidcTestDiscovery).mockResolvedValue({
      ok: true,
      discovered: {
        issuer: 'https://accounts.example.com',
        authorization_endpoint: 'https://accounts.example.com/auth',
        token_endpoint: 'https://accounts.example.com/token',
        scopes_supported: ['openid', 'email', 'profile'],
      },
    })
}

function renderSettings(options?: Parameters<typeof seedSettingsMocks>[0]) {
  if (options) seedSettingsMocks(options)
  return render(<SettingsPage />)
}

async function openIndexersTab() {
  fireEvent.click(await screen.findByRole('button', { name: 'settings.tabs.indexers' }))
}

async function openClientsTab() {
  fireEvent.click(await screen.findByRole('button', { name: 'settings.tabs.clients' }))
}

async function openImportTab() {
  fireEvent.click(await screen.findByRole('button', { name: 'settings.tabs.import' }))
}

function sectionForHeading(name: string) {
  const heading = screen.getByRole('heading', { name })
  const section = heading.closest('section')
  if (!section) throw new Error(`No section found for heading: ${name}`)
  return within(section as HTMLElement)
}

describe('SettingsPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockAuthContext.status = {
      authenticated: true,
      setupRequired: false,
      username: 'admin',
      role: 'admin',
      mode: 'enabled',
    }
    mockAuthContext.loading = false
    mockAuthContext.isAdmin = true
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true,
    })
    Object.defineProperty(window, 'isSecureContext', {
      value: true,
      configurable: true,
    })
    Object.defineProperty(document, 'execCommand', {
      value: vi.fn().mockReturnValue(false),
      configurable: true,
    })
    seedSettingsMocks()
  })

  it('adds a write-only Hardcover token field with API link', async () => {
    renderSettings()

    expect(await screen.findByText('Hardcover API Token')).toBeInTheDocument()
    const link = screen.getByRole('link', { name: 'Create or copy a Hardcover API token' })
    expect(link).toHaveAttribute('href', 'https://hardcover.app/account/api')

    fireEvent.change(screen.getByPlaceholderText('Paste a Hardcover API token'), { target: { value: 'hc-secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save Hardcover API token' }))

    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('hardcover.api_token', 'hc-secret')
    })
  })

  it('persists the enhanced Hardcover admin toggle separately from effective status', async () => {
    renderSettings()

    await screen.findByRole('heading', { name: 'settings.general.apiKeys' })
    const apiKeys = sectionForHeading('settings.general.apiKeys')
    fireEvent.click(apiKeys.getByTitle('common.enable'))

    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('hardcover.enhanced_series_enabled', 'true')
    })
  })

  it('tests the configured Hardcover token without exposing it', async () => {
    vi.mocked(api.status).mockResolvedValue({
      version: 'dev',
      commit: 'unknown',
      buildDate: '',
      enhancedHardcoverApi: true,
      hardcoverTokenConfigured: true,
    })

    renderSettings()

    fireEvent.click(await screen.findByRole('button', { name: 'Test Hardcover API' }))

    await waitFor(() => {
      expect(api.testHardcover).toHaveBeenCalled()
    })
    expect(await screen.findByText('Found 2 series; catalog "Dune" has 8 books')).toBeInTheDocument()
    expect(screen.queryByText('hc-secret')).not.toBeInTheDocument()
  })

  it('adds global-token Hardcover lists disabled by default from the import picker', async () => {
    renderSettings({
      status: {
        version: 'dev',
        commit: 'unknown',
        buildDate: '',
        enhancedHardcoverApi: false,
        hardcoverTokenConfigured: true,
      },
      hardcoverLists: [makeHardcoverList({ name: 'Sci-Fi Backlog', slug: 'sci-fi-backlog', booksCount: 7 })],
    })

    await openImportTab()
    expect(await screen.findByText('Sci-Fi Backlog')).toBeInTheDocument()
    await waitFor(() => {
      expect(api.hardcoverLists).toHaveBeenCalledWith(undefined)
    })

    fireEvent.click(screen.getByRole('checkbox', { name: 'settings.import.hardcoverImportList' }))

    await waitFor(() => {
      expect(api.addImportList).toHaveBeenCalledWith(expect.objectContaining({
        name: 'Sci-Fi Backlog',
        type: 'hardcover',
        url: 'sci-fi-backlog',
        apiKey: '',
        enabled: false,
      }))
    })
  })

  it('keeps per-list Hardcover override tokens write-only in the import picker', async () => {
    renderSettings({
      importLists: [makeImportList({ id: 44, name: 'Want to Read', url: 'want-to-read', apiKeyConfigured: true, enabled: true })],
      hardcoverLists: [makeHardcoverList()],
    })

    await openImportTab()
    expect(await screen.findByText('Override token')).toBeInTheDocument()
    expect(screen.queryByText('stored-hc-secret')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Token override' }))
    expect(screen.getByPlaceholderText('Override token is hidden. Enter a new token to replace it.')).toHaveValue('')

    fireEvent.click(screen.getByRole('button', { name: 'Clear override' }))
    await waitFor(() => {
      expect(api.updateImportList).toHaveBeenCalledWith(44, { clearApiKey: true })
    })
  })

  it('keeps duplicate saved Hardcover lists with the same slug visible and actionable', async () => {
    renderSettings({
      importLists: [
        makeImportList({ id: 44, name: 'Global Want to Read', url: 'want-to-read', apiKeyConfigured: false, enabled: false }),
        makeImportList({ id: 45, name: 'Override Want to Read', url: 'want-to-read', apiKeyConfigured: true, enabled: true }),
      ],
      hardcoverLists: [makeHardcoverList({ name: 'Remote Want to Read', slug: 'want-to-read', booksCount: 12 })],
    })

    await openImportTab()
    const globalName = await screen.findByText('Global Want to Read')
    expect(screen.getByText('Override Want to Read')).toBeInTheDocument()
    expect(screen.getAllByText('want-to-read')).toHaveLength(2)
    expect(screen.getByText('Global token')).toBeInTheDocument()
    expect(screen.getByText('Override token')).toBeInTheDocument()

    const globalRow = globalName.closest('label')
    expect(globalRow).not.toBeNull()
    fireEvent.click(within(globalRow as HTMLElement).getByRole('checkbox'))

    await waitFor(() => {
      expect(api.updateImportList).toHaveBeenCalledWith(44, { enabled: true })
    })
  })

  it('can load Hardcover list picker results from a per-list override token', async () => {
    renderSettings({
      hardcoverLists: [makeHardcoverList({ id: 51, name: 'Other Account', slug: 'other-account', booksCount: 3 })],
    })

    await openImportTab()
    expect(await screen.findByText('Other Account')).toBeInTheDocument()
    fireEvent.change(screen.getByPlaceholderText('settings.import.hardcoverTokenPlaceholder'), { target: { value: 'override-token' } })
    fireEvent.click(screen.getByRole('button', { name: 'Load token lists' }))

    await waitFor(() => {
      expect(api.hardcoverLists).toHaveBeenCalledWith('override-token')
    })
  })

  it('persists import mode, naming templates, and preferred language', async () => {
    renderSettings({
      settings: [
        { key: 'import.mode', value: 'move' },
        { key: 'naming.bookTemplate', value: '{OldBook}' },
        { key: 'naming_template_audiobook', value: '{OldAudio}' },
        { key: 'search.preferredLanguage', value: 'en' },
        { key: 'hardcover.enhanced_series_enabled', value: 'false' },
      ],
    })

    expect(await screen.findByText('Import Mode')).toBeInTheDocument()
    const fileNaming = sectionForHeading('settings.general.fileNaming')

    fireEvent.click(fileNaming.getByRole('button', { name: 'Copy' }))
    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith('import.mode', 'copy'))

    fireEvent.change(fileNaming.getByPlaceholderText('{Author}/{Title} ({Year})/{Title} - {Author}.{ext}'), {
      target: { value: '{Author}/{Title}/{Title}.{ext}' },
    })
    fireEvent.click(fileNaming.getAllByRole('button', { name: 'common.save' })[0])
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('naming.bookTemplate', '{Author}/{Title}/{Title}.{ext}')
    })

    fireEvent.change(fileNaming.getByPlaceholderText('{Author}/{Title} ({Year})'), {
      target: { value: '{Author}/{Title}' },
    })
    fireEvent.click(fileNaming.getAllByRole('button', { name: 'common.save' })[1])
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('naming_template_audiobook', '{Author}/{Title}')
    })

    const downloads = sectionForHeading('settings.general.downloads')
    fireEvent.change(downloads.getByRole('combobox'), { target: { value: 'any' } })
    fireEvent.click(downloads.getByRole('button', { name: 'common.save' }))
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('search.preferredLanguage', 'any')
    })
  })

  it('refreshes library scan status', async () => {
    vi.mocked(api.libraryScanStatus)
      .mockResolvedValueOnce({ ran_at: new Date(Date.now() - 10_000).toISOString(), files_found: 2, reconciled: 1, unmatched: 1 })
      .mockResolvedValueOnce({ ran_at: new Date(Date.now() + 10_000).toISOString(), files_found: 9, reconciled: 5, unmatched: 4 })

    renderSettings()

    expect(await screen.findByText('settings.general.lastScan')).toBeInTheDocument()
    expect(screen.getByText('2')).toBeInTheDocument()

    const library = sectionForHeading('settings.general.library')
    fireEvent.click(library.getByRole('button', { name: 'settings.general.scanLibraryButton' }))

    await waitFor(() => expect(api.triggerLibraryScan).toHaveBeenCalled())
    await waitFor(() => expect(api.libraryScanStatus).toHaveBeenCalledTimes(2), { timeout: 2500 })
    expect(await screen.findByText('9', {}, { timeout: 2500 })).toBeInTheDocument()
    expect(screen.getByText('5')).toBeInTheDocument()
    expect(screen.getByText('4')).toBeInTheDocument()
  })

  it('surfaces scanned paths and warns when no files were found', async () => {
    vi.mocked(api.libraryScanStatus).mockResolvedValue({
      ran_at: new Date().toISOString(),
      files_found: 0,
      reconciled: 0,
      unmatched: 0,
      library_dir: '/books',
      audiobook_dir: '',
      scanned_paths: ['/books'],
      no_files_found: true,
    })

    renderSettings()

    expect(await screen.findByText('settings.general.lastScan')).toBeInTheDocument()
    // The resolved path label and the path itself must both render.
    expect(screen.getByText('settings.general.scannedPaths')).toBeInTheDocument()
    expect(screen.getByText('/books')).toBeInTheDocument()
    // Zero-files warning is shown, naming the path via the t() key.
    expect(screen.getByText('settings.general.scanNoFilesWarning')).toBeInTheDocument()
  })

  it('hints to populate catalogue when files found but none matched', async () => {
    vi.mocked(api.libraryScanStatus).mockResolvedValue({
      ran_at: new Date().toISOString(),
      files_found: 12,
      reconciled: 0,
      unmatched: 12,
      library_dir: '/books',
      scanned_paths: ['/books'],
      no_files_found: false,
    })

    renderSettings()

    expect(await screen.findByText('settings.general.lastScan')).toBeInTheDocument()
    expect(screen.getByText('settings.general.scanAllUnmatchedHint')).toBeInTheDocument()
    // The no-files warning must NOT appear when files were found.
    expect(screen.queryByText('settings.general.scanNoFilesWarning')).not.toBeInTheDocument()
  })

  it('persists default root folder and media type choices', async () => {
    const existing = makeRootFolder({ id: 7, path: '/mnt/books' })
    const added = makeRootFolder({ id: 8, path: '/mnt/audiobooks' })

    renderSettings({
      rootFolders: [existing],
      settings: [
        { key: 'library.defaultRootFolderId', value: '' },
        { key: 'default.media_type', value: 'ebook' },
        { key: 'hardcover.enhanced_series_enabled', value: 'false' },
      ],
    })
    vi.mocked(api.addRootFolder).mockResolvedValue(added)

    await screen.findByRole('heading', { name: 'Default library location' })
    const defaultLocation = sectionForHeading('Default library location')

    fireEvent.change(defaultLocation.getByRole('combobox'), { target: { value: '7' } })
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('library.defaultRootFolderId', '7')
    })

    fireEvent.click(defaultLocation.getByRole('button', { name: '+ Add root folder' }))
    fireEvent.change(defaultLocation.getByPlaceholderText('/mnt/books'), { target: { value: ' /mnt/audiobooks ' } })
    fireEvent.click(defaultLocation.getByRole('button', { name: 'Add' }))

    await waitFor(() => expect(api.addRootFolder).toHaveBeenCalledWith('/mnt/audiobooks'))
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('library.defaultRootFolderId', '8')
    })
    expect(defaultLocation.getByRole('combobox')).toHaveValue('8')

    const authorDefaults = sectionForHeading('Author defaults')
    const authorDefaultSelects = authorDefaults.getAllByRole('combobox')
    fireEvent.change(authorDefaultSelects[0], { target: { value: 'audiobook' } })
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('default.media_type', 'audiobook')
    })
    fireEvent.change(authorDefaultSelects[1], { target: { value: 'latest' } })
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('author.default_monitor_mode', 'latest')
    })
    fireEvent.change(authorDefaults.getByRole('spinbutton'), { target: { value: '3' } })
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('author.default_monitor_latest_count', '3')
    })
  })

  it('persists auto-grab and recommendations toggles', async () => {
    renderSettings({
      settings: [
        { key: 'autoGrab.enabled', value: 'true' },
        { key: 'recommendations.enabled', value: 'false' },
        { key: 'hardcover.enhanced_series_enabled', value: 'false' },
      ],
    })

    await screen.findByRole('heading', { name: 'settings.general.autoGrab' })

    fireEvent.click(sectionForHeading('settings.general.autoGrab').getByTitle('common.disable'))
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('autoGrab.enabled', 'false')
    })

    fireEvent.click(sectionForHeading('settings.general.recommendations').getByTitle('common.enable'))
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('recommendations.enabled', 'true')
    })
  })

  it('persists authentication mode changes and refreshes auth status', async () => {
    vi.mocked(api.authConfig)
      .mockResolvedValueOnce({ mode: 'enabled', apiKey: 'api-secret', username: 'admin' })
      .mockResolvedValue({ mode: 'local-only', apiKey: 'api-secret', username: 'admin' })
    vi.mocked(api.authSetMode).mockResolvedValue({ mode: 'local-only' })

    renderSettings()

    await screen.findByRole('heading', { name: 'Security' })
    fireEvent.change(sectionForHeading('Security').getByRole('combobox'), { target: { value: 'local-only' } })

    await waitFor(() => expect(api.authSetMode).toHaveBeenCalledWith('local-only'))
    expect(mockAuthContext.refresh).toHaveBeenCalled()
    await waitFor(() => expect(api.authConfig).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(sectionForHeading('Security').getByRole('combobox')).toHaveValue('local-only'))
  })

  it('shows, copies, and regenerates the security API key', async () => {
    vi.mocked(api.authConfig).mockResolvedValue({ mode: 'enabled', apiKey: 'api-secret', username: 'admin' })
    vi.mocked(api.authRegenerateApiKey).mockResolvedValue({ apiKey: 'rotated-secret' })
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)

    try {
      renderSettings()

      await screen.findByRole('heading', { name: 'Security' })
      const security = sectionForHeading('Security')
      expect(security.queryByText('api-secret')).not.toBeInTheDocument()

      fireEvent.click(security.getByRole('button', { name: 'Show' }))
      expect(security.getByText('api-secret')).toBeInTheDocument()
      fireEvent.click(security.getByRole('button', { name: 'Hide' }))
      expect(security.queryByText('api-secret')).not.toBeInTheDocument()

      fireEvent.click(security.getByRole('button', { name: 'Copy' }))
      await waitFor(() => {
        expect(navigator.clipboard.writeText).toHaveBeenCalledWith('api-secret')
      })
      expect(await security.findByRole('button', { name: 'Copied' })).toBeInTheDocument()

      fireEvent.click(security.getByRole('button', { name: 'Regenerate' }))
      await waitFor(() => expect(api.authRegenerateApiKey).toHaveBeenCalled())
      expect(confirmSpy).toHaveBeenCalledWith('Regenerate the API key? Existing integrations using the old key will stop working.')
      expect(await security.findByText('rotated-secret')).toBeInTheDocument()
    } finally {
      confirmSpy.mockRestore()
    }
  })

  it('shows manual copy fallback when security API key clipboard access is blocked', async () => {
    vi.mocked(api.authConfig).mockResolvedValue({ mode: 'enabled', apiKey: 'api-secret', username: 'admin' })
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
      configurable: true,
    })

    renderSettings()

    await screen.findByRole('heading', { name: 'Security' })
    const security = sectionForHeading('Security')
    expect(security.queryByDisplayValue('api-secret')).not.toBeInTheDocument()

    fireEvent.click(security.getByRole('button', { name: 'Copy' }))
    await waitFor(() => {
      expect(navigator.clipboard.writeText).toHaveBeenCalledWith('api-secret')
    })

    expect(await security.findByRole('status')).toHaveTextContent('Clipboard access is blocked')
    expect(security.getByLabelText('Text to copy')).toHaveValue('api-secret')
  })

  it('validates and submits password changes', async () => {
    renderSettings()

    await screen.findByRole('heading', { name: 'Security' })
    const security = sectionForHeading('Security')
    const current = security.getByPlaceholderText('Current password')
    const next = security.getByPlaceholderText('New password')
    const confirm = security.getByPlaceholderText('Confirm new password')
    const submit = security.getByRole('button', { name: 'Change password' })

    fireEvent.change(current, { target: { value: 'old-password' } })
    fireEvent.change(next, { target: { value: 'long-enough' } })
    fireEvent.change(confirm, { target: { value: 'different' } })
    fireEvent.click(submit)

    expect(await security.findByText('New passwords do not match')).toBeInTheDocument()
    expect(api.authChangePassword).not.toHaveBeenCalled()

    fireEvent.change(next, { target: { value: 'short' } })
    fireEvent.change(confirm, { target: { value: 'short' } })
    fireEvent.click(submit)

    expect(await security.findByText('Password must be at least 8 characters')).toBeInTheDocument()
    expect(api.authChangePassword).not.toHaveBeenCalled()

    fireEvent.change(next, { target: { value: 'long-enough' } })
    fireEvent.change(confirm, { target: { value: 'long-enough' } })
    fireEvent.click(submit)

    await waitFor(() => {
      expect(api.authChangePassword).toHaveBeenCalledWith('old-password', 'long-enough')
    })
    expect(await security.findByText('Password updated')).toBeInTheDocument()
    expect(current).toHaveValue('')
    expect(next).toHaveValue('')
    expect(confirm).toHaveValue('')
  })

  it('renders OIDC empty state and adds a provider with callback preview', async () => {
    vi.mocked(api.oidcRedirectBase).mockResolvedValue({
      base: 'https://bindery.example.com',
      callback_path: '/api/v1/auth/oidc/{id}/callback',
      configured: true,
    })

    renderSettings()

    expect(await screen.findByText('settings.oidc.empty')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'settings.oidc.addButton' }))

    const form = screen.getByText('settings.oidc.addHeading').closest('form')
    if (!form) throw new Error('OIDC add form not found')
    const oidcForm = within(form)
    const inputs = Array.from(form.querySelectorAll('input')) as HTMLInputElement[]

    fireEvent.change(oidcForm.getByPlaceholderText('google'), { target: { value: ' okta ' } })
    fireEvent.change(oidcForm.getByPlaceholderText('Google'), { target: { value: ' Okta ' } })
    expect(await screen.findByText('https://bindery.example.com/api/v1/auth/oidc/okta/callback')).toBeInTheDocument()
    fireEvent.change(oidcForm.getByPlaceholderText('https://accounts.google.com'), {
      target: { value: ' https://issuer.example.com ' },
    })
    fireEvent.change(inputs[3], { target: { value: ' client-id ' } })
    fireEvent.change(inputs[4], { target: { value: ' client-secret ' } })
    fireEvent.change(inputs[5], { target: { value: 'openid email profile groups' } })

    const add = oidcForm.getByRole('button', { name: 'settings.oidc.addSave' })
    expect(add).toBeEnabled()
    fireEvent.click(add)

    await waitFor(() => {
      expect(api.oidcSetProviders).toHaveBeenCalledWith([
        {
          id: 'okta',
          name: 'Okta',
          issuer: 'https://issuer.example.com',
          client_id: 'client-id',
          client_secret: 'client-secret',
          scopes: ['openid', 'email', 'profile', 'groups'],
        },
      ])
    })
  })

  it('persists external API key controls without exposing stored Hardcover secrets', async () => {
    renderSettings({
      status: {
        version: 'dev',
        commit: 'unknown',
        buildDate: '',
        enhancedHardcoverApi: false,
        hardcoverTokenConfigured: true,
        enhancedHardcoverDisabledReason: 'admin_disabled',
      },
      settings: [
        { key: 'googlebooks.apiKey', value: '' },
        { key: 'hardcover.enhanced_series_enabled', value: 'false' },
      ],
    })

    await screen.findByRole('heading', { name: 'settings.general.apiKeys' })
    const apiKeys = sectionForHeading('settings.general.apiKeys')

    fireEvent.change(apiKeys.getByPlaceholderText('AIza...'), { target: { value: 'AIza-test-key' } })
    fireEvent.click(apiKeys.getByRole('button', { name: 'common.save' }))
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('googlebooks.apiKey', 'AIza-test-key')
    })

    expect(apiKeys.getByText('Token configured')).toBeInTheDocument()
    expect(apiKeys.getByPlaceholderText('Saved token is hidden. Enter a new token to rotate it.')).toHaveValue('')
    expect(apiKeys.queryByText('stored-hc-secret')).not.toBeInTheDocument()
    expect(apiKeys.getByRole('button', { name: 'Save Hardcover API token' })).toBeDisabled()

    fireEvent.click(apiKeys.getByRole('button', { name: 'Clear Hardcover API token' }))
    await waitFor(() => {
      expect(api.setSetting).toHaveBeenCalledWith('hardcover.api_token', '')
    })
  })

  it('requires saved Audiobookshelf settings before starting an import', async () => {
    vi.mocked(api.absConfig).mockResolvedValue({
      featureEnabled: true,
      baseUrl: 'https://abs.example.com',
      label: 'Shelf',
      enabled: true,
      libraryId: 'lib-books',
      libraryIds: ['lib-books'],
      pathRemap: '/abs:/books',
      apiKeyConfigured: true,
    })
    vi.mocked(api.absSetConfig).mockImplementation(async data => ({
      featureEnabled: true,
      baseUrl: data.baseUrl,
      label: data.label,
      enabled: data.enabled,
      libraryId: data.libraryId,
      libraryIds: data.libraryIds ?? [data.libraryId],
      pathRemap: data.pathRemap,
      apiKeyConfigured: true,
    }))

    renderSettings()

    fireEvent.click(await screen.findByRole('button', { name: 'settings.tabs.abs' }))
    const preview = await screen.findByRole('button', { name: 'Preview changes' })
    expect(preview).toBeEnabled()

    fireEvent.change(screen.getByPlaceholderText('/audiobookshelf:/books/audiobookshelf,/abs:/books'), { target: { value: '/draft:/books' } })
    expect(preview).toBeDisabled()
    expect(screen.getByText('Save Audiobookshelf settings before starting an import so the run uses the stored source configuration.')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Save source' }))
    await waitFor(() => {
      expect(api.absSetConfig).toHaveBeenCalledWith({
        baseUrl: 'https://abs.example.com',
        apiKey: undefined,
        label: 'Shelf',
        enabled: true,
        libraryId: 'lib-books',
        libraryIds: ['lib-books'],
        pathRemap: '/draft:/books',
      })
    })
    await waitFor(() => expect(preview).toBeEnabled())

    fireEvent.click(preview)
    await waitFor(() => {
      expect(api.absImportStart).toHaveBeenCalledWith({ dryRun: true })
    })
  })

  it('uses a multi-library selector instead of the old Audiobookshelf library dropdown', async () => {
    vi.mocked(api.absConfig).mockResolvedValue({
      featureEnabled: true,
      baseUrl: 'https://abs.example.com',
      label: 'Shelf',
      enabled: true,
      libraryId: 'lib-books',
      libraryIds: ['lib-books', 'lib-audio'],
      pathRemap: '/abs:/books',
      apiKeyConfigured: true,
    })
    vi.mocked(api.absLibraries).mockResolvedValue([
      { id: 'lib-books', name: 'Books', mediaType: 'book', icon: '', provider: 'local', folders: [] },
      { id: 'lib-audio', name: 'Audiobooks', mediaType: 'book', icon: '', provider: 'audible', folders: [] },
    ])

    renderSettings()

    fireEvent.click(await screen.findByRole('button', { name: 'settings.tabs.abs' }))
    expect(await screen.findByRole('heading', { name: 'Libraries' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Library')).not.toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: /Books/ })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: /Audiobooks/ })).toBeChecked()
  })

  it('labels recent Audiobookshelf import runs by library', async () => {
    vi.mocked(api.absConfig).mockResolvedValue({
      featureEnabled: true,
      baseUrl: 'https://abs.example.com',
      label: 'Shelf',
      enabled: true,
      libraryId: 'lib-books',
      libraryIds: ['lib-books', 'lib-audio'],
      pathRemap: '/abs:/books',
      apiKeyConfigured: true,
    })
    vi.mocked(api.absLibraries).mockResolvedValue([
      { id: 'lib-books', name: 'Books', mediaType: 'book', icon: '', provider: 'local', folders: [] },
      { id: 'lib-audio', name: 'Audiobooks', mediaType: 'book', icon: '', provider: 'audible', folders: [] },
    ])
    vi.mocked(api.absImportRuns).mockResolvedValue([
      makeABSImportRun(42, 'lib-books'),
      makeABSImportRun(43, 'lib-audio'),
    ])

    renderSettings()

    fireEvent.click(await screen.findByRole('button', { name: 'settings.tabs.abs' }))

    expect(await screen.findByText(/Shelf · Books \(lib-books\)/)).toBeInTheDocument()
    expect(screen.getByText(/Shelf · Audiobooks \(lib-audio\)/)).toBeInTheDocument()
  })

  it('saves all selected Audiobookshelf libraries', async () => {
    vi.mocked(api.absConfig).mockResolvedValue({
      featureEnabled: true,
      baseUrl: 'https://abs.example.com',
      label: 'Shelf',
      enabled: true,
      libraryId: 'lib-books',
      libraryIds: ['lib-books'],
      pathRemap: '/abs:/books',
      apiKeyConfigured: true,
    })
    vi.mocked(api.absLibraries).mockResolvedValue([
      { id: 'lib-books', name: 'Books', mediaType: 'book', icon: '', provider: 'local', folders: [] },
      { id: 'lib-audio', name: 'Audiobooks', mediaType: 'book', icon: '', provider: 'audible', folders: [] },
    ])
    vi.mocked(api.absSetConfig).mockImplementation(async data => ({
      featureEnabled: true,
      baseUrl: data.baseUrl,
      label: data.label,
      enabled: data.enabled,
      libraryId: data.libraryId,
      libraryIds: data.libraryIds ?? [],
      pathRemap: data.pathRemap,
      apiKeyConfigured: true,
    }))

    renderSettings()

    fireEvent.click(await screen.findByRole('button', { name: 'settings.tabs.abs' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /Audiobooks/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Save source' }))

    await waitFor(() => {
      expect(api.absSetConfig).toHaveBeenCalledWith({
        baseUrl: 'https://abs.example.com',
        apiKey: undefined,
        label: 'Shelf',
        enabled: true,
        libraryId: 'lib-books',
        libraryIds: ['lib-books', 'lib-audio'],
        pathRemap: '/abs:/books',
      })
    })
  })

  it('keeps ABS import disabled until selected libraries are saved', async () => {
    vi.mocked(api.absConfig).mockResolvedValue({
      featureEnabled: true,
      baseUrl: 'https://abs.example.com',
      label: 'Shelf',
      enabled: true,
      libraryId: '',
      libraryIds: [],
      pathRemap: '/abs:/books',
      apiKeyConfigured: true,
    })
    vi.mocked(api.absLibraries).mockResolvedValue([
      { id: 'lib-books', name: 'Books', mediaType: 'book', icon: '', provider: 'local', folders: [] },
    ])
    vi.mocked(api.absSetConfig).mockImplementation(async data => ({
      featureEnabled: true,
      baseUrl: data.baseUrl,
      label: data.label,
      enabled: data.enabled,
      libraryId: data.libraryId,
      libraryIds: data.libraryIds ?? [],
      pathRemap: data.pathRemap,
      apiKeyConfigured: true,
    }))

    renderSettings()

    fireEvent.click(await screen.findByRole('button', { name: 'settings.tabs.abs' }))
    const preview = await screen.findByRole('button', { name: 'Preview changes' })
    expect(preview).toBeDisabled()
    fireEvent.click(await screen.findByRole('checkbox', { name: /Books/ }))
    expect(preview).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Save source' }))
    await waitFor(() => expect(preview).toBeEnabled())
  })

  it('adds an indexer with parsed categories', async () => {
    renderSettings()
    await openIndexersTab()

    fireEvent.click(screen.getByRole('button', { name: 'settings.indexers.addButton' }))
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.namePlaceholderExample'), { target: { value: 'SceneNZBs' } })
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'torznab' } })
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.urlPlaceholderExample'), { target: { value: 'http://prowlarr:9696/1/api' } })
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.apiKey'), { target: { value: 'scene-key' } })
    fireEvent.change(screen.getByDisplayValue('7020'), { target: { value: '7020, 7120, bad, 3030' } })
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))

    await waitFor(() => {
      expect(api.addIndexer).toHaveBeenCalledWith({
        name: 'SceneNZBs',
        url: 'http://prowlarr:9696/1/api',
        apiKey: 'scene-key',
        type: 'torznab',
        categories: [7020, 7120, 3030],
        enabled: true,
      })
    })
    expect(await screen.findByText('SceneNZBs')).toBeInTheDocument()
  })

  it('edits an indexer while preserving existing fields', async () => {
    const indexer = makeIndexer({ id: 7, name: 'Old Indexer', categories: [7020, 3030], enabled: true })

    renderSettings({ indexers: [indexer] })
    await openIndexersTab()

    fireEvent.click(screen.getByRole('button', { name: 'common.edit' }))
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.namePlaceholder'), { target: { value: 'DrunkenSlug' } })
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'torznab' } })
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.urlPlaceholder'), { target: { value: 'https://slug.example.com/api' } })
    fireEvent.change(screen.getByPlaceholderText('settings.indexers.form.apiKey'), { target: { value: 'slug-key' } })
    fireEvent.change(screen.getByDisplayValue('7020, 3030'), { target: { value: '7020, bad, 3030' } })
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))

    await waitFor(() => {
      expect(api.updateIndexer).toHaveBeenCalledWith(7, {
        ...indexer,
        name: 'DrunkenSlug',
        type: 'torznab',
        url: 'https://slug.example.com/api',
        apiKey: 'slug-key',
        categories: [7020, 3030],
      })
    })
    expect(await screen.findByText('DrunkenSlug')).toBeInTheDocument()
  })

  it('toggles and deletes an indexer', async () => {
    const indexer = makeIndexer({ id: 8, name: 'Toggle Indexer', enabled: true })
    vi.mocked(api.updateIndexer).mockResolvedValue({ ...indexer, enabled: false })

    renderSettings({ indexers: [indexer] })
    await openIndexersTab()

    fireEvent.click(screen.getByTitle('common.disable'))
    await waitFor(() => {
      expect(api.updateIndexer).toHaveBeenCalledWith(8, { ...indexer, enabled: false })
    })
    expect(await screen.findByTitle('common.enable')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'common.delete' }))
    fireEvent.click(screen.getByRole('button', { name: 'common.yes' }))
    await waitFor(() => expect(api.deleteIndexer).toHaveBeenCalledWith(8))
    await waitFor(() => expect(screen.queryByText('Toggle Indexer')).not.toBeInTheDocument())
  })

  it('renders indexer test success, warning, and failure states', async () => {
    const indexer = makeIndexer({ id: 9, name: 'Probe Indexer' })

    renderSettings({ indexers: [indexer] })
    await openIndexersTab()

    vi.mocked(api.testIndexer).mockResolvedValueOnce({ ok: true, status: 200, categories: 12, bookSearch: true, latencyMs: 20, searchResults: 3 })
    fireEvent.click(screen.getByRole('button', { name: 'common.test' }))
    await waitFor(() => expect(api.testIndexer).toHaveBeenCalledWith(9))
    expect(await screen.findByText('settings.indexers.testOk')).toBeInTheDocument()

    vi.mocked(api.testIndexer).mockResolvedValueOnce({ ok: true, status: 200, categories: 12, bookSearch: true, latencyMs: 20, searchResults: 0, searchError: 'no book results' })
    fireEvent.click(screen.getByRole('button', { name: 'common.test' }))
    expect(await screen.findByText(/settings\.indexers\.testWarn/)).toBeInTheDocument()
    expect(screen.getByText(/no book results/)).toBeInTheDocument()

    vi.mocked(api.testIndexer).mockRejectedValueOnce(new Error('bad key'))
    fireEvent.click(screen.getByRole('button', { name: 'common.test' }))
    expect(await screen.findByText('settings.indexers.testFail')).toBeInTheDocument()
  })

  it('adds Prowlarr and immediately syncs refreshed indexers', async () => {
    const added = makeProwlarr({ id: 31, name: 'Main Prowlarr', url: 'http://prowlarr:9696' })
    vi.mocked(api.addProwlarr).mockResolvedValue(added)
    vi.mocked(api.syncProwlarr).mockResolvedValue({ added: 2, updated: 1, removed: 0 })
    vi.mocked(api.listProwlarr).mockResolvedValueOnce([]).mockResolvedValueOnce([{ ...added, lastSyncAt: '2026-05-06T12:00:00Z' }])

    renderSettings()
    await openIndexersTab()

    fireEvent.click(screen.getByRole('button', { name: 'settings.prowlarr.addButton' }))
    fireEvent.change(screen.getByPlaceholderText('settings.prowlarr.namePlaceholder'), { target: { value: 'Main Prowlarr' } })
    fireEvent.change(screen.getByPlaceholderText('settings.prowlarr.urlPlaceholder'), { target: { value: 'http://prowlarr:9696' } })
    fireEvent.change(screen.getByPlaceholderText('settings.prowlarr.apiKeyPlaceholder'), { target: { value: 'prowlarr-secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'settings.prowlarr.saveAndSync' }))

    await waitFor(() => {
      expect(api.addProwlarr).toHaveBeenCalledWith({
        name: 'Main Prowlarr',
        url: 'http://prowlarr:9696',
        apiKey: 'prowlarr-secret',
        syncOnStartup: true,
        enabled: true,
      })
    })
    await waitFor(() => expect(api.syncProwlarr).toHaveBeenCalledWith(31))
    await waitFor(() => expect(api.listProwlarr).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(api.listIndexers).toHaveBeenCalledTimes(2))
  })

  it('keeps a newly added Prowlarr instance when immediate sync fails', async () => {
    const added = makeProwlarr({ id: 32, name: 'Fallback Prowlarr' })
    vi.mocked(api.addProwlarr).mockResolvedValue(added)
    vi.mocked(api.syncProwlarr).mockRejectedValue(new Error('sync failed'))

    renderSettings()
    await openIndexersTab()

    fireEvent.click(screen.getByRole('button', { name: 'settings.prowlarr.addButton' }))
    fireEvent.change(screen.getByPlaceholderText('settings.prowlarr.namePlaceholder'), { target: { value: 'Fallback Prowlarr' } })
    fireEvent.change(screen.getByPlaceholderText('settings.prowlarr.urlPlaceholder'), { target: { value: 'http://prowlarr:9696' } })
    fireEvent.change(screen.getByPlaceholderText('settings.prowlarr.apiKeyPlaceholder'), { target: { value: 'prowlarr-secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'settings.prowlarr.saveAndSync' }))

    await waitFor(() => expect(api.syncProwlarr).toHaveBeenCalledWith(32))
    expect(await screen.findByText('Fallback Prowlarr')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'settings.prowlarr.saveAndSync' })).not.toBeInTheDocument()
  })

  it('tests, syncs, and deletes an existing Prowlarr instance', async () => {
    const prowlarr = makeProwlarr({ id: 33, name: 'Library Prowlarr', lastSyncAt: '2026-05-06T12:00:00Z' })
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)

    try {
      renderSettings({ prowlarr: [prowlarr], indexers: [makeIndexer({ id: 11, prowlarrInstanceId: 33 })] })
      vi.mocked(api.syncProwlarr).mockResolvedValue({ added: 1, updated: 2, removed: 3 })
      vi.mocked(api.listProwlarr).mockResolvedValue([{ ...prowlarr, lastSyncAt: '2026-05-06T13:00:00Z' }])
      await openIndexersTab()

      fireEvent.click(screen.getByRole('button', { name: 'settings.prowlarr.test' }))
      await waitFor(() => expect(api.testProwlarr).toHaveBeenCalledWith(33))
      expect(await screen.findByText('settings.prowlarr.connectedVersion')).toBeInTheDocument()

      fireEvent.click(screen.getByRole('button', { name: 'settings.prowlarr.syncNow' }))
      await waitFor(() => expect(api.syncProwlarr).toHaveBeenCalledWith(33))
      expect(await screen.findByText('settings.prowlarr.synced')).toBeInTheDocument()
      await waitFor(() => expect(api.listIndexers).toHaveBeenCalledTimes(2))
      await waitFor(() => expect(api.listProwlarr).toHaveBeenCalledTimes(2))

      fireEvent.click(screen.getByRole('button', { name: 'settings.prowlarr.delete' }))
      await waitFor(() => expect(api.deleteProwlarr).toHaveBeenCalledWith(33))
      expect(confirmSpy).toHaveBeenCalledWith('settings.prowlarr.confirmDelete')
      await waitFor(() => expect(screen.queryByText('Library Prowlarr')).not.toBeInTheDocument())
    } finally {
      confirmSpy.mockRestore()
    }
  })

  it('adds a SABnzbd download client with API key, SSL, URL Base, and category mapping', async () => {
    renderSettings()
    await openClientsTab()

    fireEvent.click(screen.getByRole('button', { name: 'settings.clients.addButton' }))
    fireEvent.change(screen.getByPlaceholderText('Name'), { target: { value: 'SAB Books' } })
    fireEvent.change(screen.getByPlaceholderText('Host'), { target: { value: 'sabnzbd' } })
    fireEvent.change(screen.getByPlaceholderText('Port'), { target: { value: '8085' } })
    fireEvent.click(screen.getByRole('checkbox', { name: 'Use SSL' }))
    fireEvent.change(screen.getByPlaceholderText('/sabnzbd'), { target: { value: ' /sab ' } })
    fireEvent.change(screen.getByPlaceholderText('API Key'), { target: { value: 'sab-secret' } })
    fireEvent.change(screen.getByDisplayValue('books'), { target: { value: 'ebooks' } })
    fireEvent.change(screen.getByLabelText('Download client path remap'), { target: { value: ' /media:/books ' } })
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))

    await waitFor(() => {
      expect(api.addDownloadClient).toHaveBeenCalledWith({
        name: 'SAB Books',
        host: 'sabnzbd',
        port: 8085,
        apiKey: 'sab-secret',
        username: '',
        password: '',
        category: 'ebooks',
        categoryAudiobook: '',
        pathRemap: '/media:/books',
        type: 'sabnzbd',
        enabled: true,
        useSsl: true,
        urlBase: '/sab',
      })
    })
    expect(await screen.findByText('SAB Books')).toBeInTheDocument()
  })

  it('updates add-client defaults and clears stale credentials when switching types', async () => {
    renderSettings()
    await openClientsTab()

    fireEvent.click(screen.getByRole('button', { name: 'settings.clients.addButton' }))
    fireEvent.change(screen.getByPlaceholderText('API Key'), { target: { value: 'stale-key' } })

    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'nzbget' } })
    expect(screen.getByPlaceholderText('Name')).toHaveValue('NZBGet')
    expect(screen.getByPlaceholderText('Port')).toHaveValue('6789')
    expect(screen.getByPlaceholderText('Username')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Password')).toHaveValue('')

    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'qbittorrent' } })
    expect(screen.getByPlaceholderText('Name')).toHaveValue('qBittorrent')
    expect(screen.getByPlaceholderText('Port')).toHaveValue('8080')

    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'transmission' } })
    expect(screen.getByPlaceholderText('Name')).toHaveValue('Transmission')
    expect(screen.getByPlaceholderText('Port')).toHaveValue('9091')
    expect(screen.getByText('Download Directory')).toBeInTheDocument()

    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'deluge' } })
    expect(screen.getByPlaceholderText('Name')).toHaveValue('Deluge')
    expect(screen.getByPlaceholderText('Port')).toHaveValue('8112')
    expect(screen.getByText('Category / Label')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('Username')).not.toBeInTheDocument()
  })

  it.each([
    { type: 'nzbget', name: 'NZBGet', port: 6789, username: 'nzb-user', password: 'nzb-pass', category: 'books' },
    { type: 'qbittorrent', name: 'qBittorrent', port: 8080, username: 'qbit-user', password: 'qbit-pass', category: 'ebooks' },
    { type: 'transmission', name: 'Transmission', port: 9091, username: 'tr-user', password: 'tr-pass', category: '/downloads/books' },
    { type: 'deluge', name: 'Deluge', port: 8112, username: '', password: 'deluge-pass', category: 'books-audio' },
  ])('maps $name download client credentials on add', async ({ type, name, port, username, password, category }) => {
    renderSettings()
    await openClientsTab()

    fireEvent.click(screen.getByRole('button', { name: 'settings.clients.addButton' }))
    fireEvent.change(screen.getByRole('combobox'), { target: { value: type } })
    fireEvent.change(screen.getByPlaceholderText('Host'), { target: { value: `${type}.local` } })
    if (username) {
      fireEvent.change(screen.getByPlaceholderText('Username'), { target: { value: username } })
    }
    fireEvent.change(screen.getByPlaceholderText('Password'), { target: { value: password } })
    fireEvent.change(screen.getByDisplayValue('books'), { target: { value: category } })
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))

    await waitFor(() => {
      expect(api.addDownloadClient).toHaveBeenCalledWith({
        name,
        host: `${type}.local`,
        port,
        username,
        password,
        apiKey: '',
        category,
        categoryAudiobook: '',
        pathRemap: '',
        type,
        enabled: true,
        useSsl: false,
        urlBase: '',
      })
    })
  })

  it('edits a download client with credential remapping, SSL, URL Base, and category updates', async () => {
    const client = makeClient({ id: 44, name: 'Old SAB', host: 'sab-old', apiKey: 'old-api' })

    renderSettings({ clients: [client] })
    await openClientsTab()

    fireEvent.click(screen.getByRole('button', { name: 'common.edit' }))
    fireEvent.change(screen.getByPlaceholderText('Name'), { target: { value: 'qBit Books' } })
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'qbittorrent' } })
    fireEvent.change(screen.getByPlaceholderText('Host'), { target: { value: 'qbittorrent' } })
    fireEvent.change(screen.getByPlaceholderText('Port'), { target: { value: '8081' } })
    fireEvent.click(screen.getByRole('checkbox', { name: 'Use SSL' }))
    fireEvent.change(screen.getByPlaceholderText('/sabnzbd'), { target: { value: ' /qbittorrent ' } })
    fireEvent.change(screen.getByPlaceholderText('Username'), { target: { value: 'qbit-user' } })
    fireEvent.change(screen.getByPlaceholderText('Password'), { target: { value: 'qbit-pass' } })
    fireEvent.change(screen.getByDisplayValue('books'), { target: { value: 'ebooks' } })
    fireEvent.change(screen.getByLabelText('Download client path remap'), { target: { value: ' /media:/books ' } })
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))

    await waitFor(() => {
      expect(api.updateDownloadClient).toHaveBeenCalledWith(44, {
        ...client,
        name: 'qBit Books',
        type: 'qbittorrent',
        host: 'qbittorrent',
        port: 8081,
        username: 'qbit-user',
        password: 'qbit-pass',
        apiKey: '',
        category: 'ebooks',
        categoryAudiobook: '',
        pathRemap: '/media:/books',
        useSsl: true,
        urlBase: '/qbittorrent',
      })
    })
    expect(await screen.findByText('qBit Books')).toBeInTheDocument()
  })

  // #700: per-media-type categories. Verifies the audiobook category field
  // round-trips through the add form. The transmission case is excluded — its
  // "Category" field is repurposed as a download-directory override; an audiobook
  // category for it would need a separate path UI (left to a follow-up).
  it('adds a download client with a separate audiobook category', async () => {
    renderSettings()
    await openClientsTab()

    fireEvent.click(screen.getByRole('button', { name: 'settings.clients.addButton' }))
    fireEvent.change(screen.getByPlaceholderText('Host'), { target: { value: 'sabnzbd' } })
    fireEvent.change(screen.getByPlaceholderText('API Key'), { target: { value: 'k' } })
    fireEvent.change(screen.getByDisplayValue('books'), { target: { value: 'ebooks' } })
    fireEvent.change(screen.getByPlaceholderText('settings.clients.audiobookCategoryPlaceholder'), { target: { value: ' audiobooks ' } })
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))

    await waitFor(() => {
      expect(api.addDownloadClient).toHaveBeenCalledWith(expect.objectContaining({
        category: 'ebooks',
        categoryAudiobook: 'audiobooks',
      }))
    })
  })

  it('shows qBittorrent path health errors under the client', async () => {
    const client = makeClient({
      id: 46,
      name: 'qBit Books',
      type: 'qbittorrent',
      health: { status: 'error', message: 'qBittorrent category "books" saves to "/media/downloads"; expected "/books/downloads"' },
    })

    renderSettings({ clients: [client] })
    await openClientsTab()

    expect(await screen.findByText(/expected "\/books\/downloads"/)).toBeInTheDocument()
  })

  it('toggles, tests, and deletes a download client', async () => {
    const client = makeClient({ id: 45, name: 'Client Actions', enabled: true })
    vi.mocked(api.updateDownloadClient).mockResolvedValue({ ...client, enabled: false })

    renderSettings({ clients: [client] })
    await openClientsTab()

    fireEvent.click(screen.getByTitle('common.disable'))
    await waitFor(() => {
      expect(api.updateDownloadClient).toHaveBeenCalledWith(45, { ...client, enabled: false })
    })
    expect(await screen.findByTitle('common.enable')).toBeInTheDocument()

    vi.mocked(api.testDownloadClient).mockResolvedValue({ message: 'Connection verified' })
    fireEvent.click(screen.getByRole('button', { name: 'common.test' }))
    await waitFor(() => expect(api.testDownloadClient).toHaveBeenCalledWith(45))
    expect(await screen.findByText('common.connOk')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'common.delete' }))
    fireEvent.click(screen.getByRole('button', { name: 'common.yes' }))
    await waitFor(() => expect(api.deleteDownloadClient).toHaveBeenCalledWith(45))
    await waitFor(() => expect(screen.queryByText('Client Actions')).not.toBeInTheDocument())
  })

  it('shows error message when adding a download client fails', async () => {
    renderSettings()
    vi.mocked(api.addDownloadClient).mockRejectedValue(new Error('Connection refused'))
    await openClientsTab()

    fireEvent.click(screen.getByRole('button', { name: 'settings.clients.addButton' }))
    fireEvent.change(screen.getByPlaceholderText('Host'), { target: { value: 'badhost' } })
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))

    expect(await screen.findByText('Connection refused')).toBeInTheDocument()
    expect(api.addDownloadClient).toHaveBeenCalledOnce()
  })

  it('shows error message when editing a download client fails', async () => {
    const client = makeClient({ id: 55, name: 'Broken SAB' })

    renderSettings({ clients: [client] })
    vi.mocked(api.updateDownloadClient).mockRejectedValue(new Error('Server error'))
    await openClientsTab()

    fireEvent.click(screen.getByRole('button', { name: 'common.edit' }))
    fireEvent.change(screen.getByPlaceholderText('Host'), { target: { value: 'newhost' } })
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))

    expect(await screen.findByText('Server error')).toBeInTheDocument()
    expect(api.updateDownloadClient).toHaveBeenCalledOnce()
  })

  it('deletes a backup from the list', async () => {
    const backup = { name: 'bindery_20260513_120000.db', size: 1024 * 512, modTime: new Date().toISOString() }
    vi.mocked(api.listBackups).mockResolvedValue([backup])
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    try {
      renderSettings()

      expect(await screen.findByText(backup.name)).toBeInTheDocument()

      const deleteBtn = screen.getByRole('button', { name: 'common.delete' })
      fireEvent.click(deleteBtn)

      await waitFor(() => expect(api.deleteBackup).toHaveBeenCalledWith(backup.name))
      await waitFor(() => expect(screen.queryByText(backup.name)).not.toBeInTheDocument())
    } finally {
      confirmSpy.mockRestore()
    }
  })
})
