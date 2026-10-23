import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import DatabaseCheckSection from './DatabaseCheckSection'
import GeneralTab from './GeneralTab'
import { api, type DBIntegrityReport, type DBCleanupPreview, type DBCleanupResult } from '../../api/client'

const auth = vi.hoisted(() => ({ isAdmin: true }))

vi.mock('../../components/ThemeToggle', () => ({ default: () => <button type="button">Theme</button> }))
vi.mock('../../components/LanguageSwitcher', () => ({ default: () => <select aria-label="Language" /> }))
vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({
    status: { authenticated: true, username: 'u', role: auth.isAdmin ? 'admin' : 'user', mode: 'enabled', setupRequired: false },
    loading: false,
    isAdmin: auth.isAdmin,
    refresh: vi.fn(),
    logout: vi.fn(),
  }),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) =>
      opts && typeof opts === 'object' ? `${key} ${JSON.stringify(opts)}` : key,
    i18n: { changeLanguage: vi.fn() },
  }),
}))
vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listSettings: vi.fn(),
      libraryScanStatus: vi.fn(),
      getStorage: vi.fn(),
      authConfig: vi.fn(),
      checkDBIntegrity: vi.fn(),
      previewDBCleanup: vi.fn(),
      cleanupDB: vi.fn(),
    },
  }
})

beforeEach(() => {
  auth.isAdmin = true
  vi.mocked(api.checkDBIntegrity).mockReset()
  vi.mocked(api.previewDBCleanup).mockReset()
  vi.mocked(api.cleanupDB).mockReset()
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.libraryScanStatus).mockRejectedValue(new Error('no scan'))
  vi.mocked(api.authConfig).mockRejectedValue(new Error('no auth cfg'))
  vi.mocked(api.getStorage).mockRejectedValue(new Error('no storage'))
})

const drift: DBIntegrityReport = {
  total: 5,
  checkedAt: '2026-10-18T12:00:00Z',
  relationships: [
    { childTable: 'book_files', parentTable: 'books', onDelete: 'CASCADE', violations: 4 },
    { childTable: 'downloads', parentTable: 'books', onDelete: 'SET NULL', violations: 1 },
  ],
}

describe('DatabaseCheckSection', () => {
  it('does not scan until the button is pressed', () => {
    render(<DatabaseCheckSection />)
    expect(api.checkDBIntegrity).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'settings.general.dbCheckButton' })).toBeTruthy()
  })

  it('shows a loading state while the scan runs, then the table', async () => {
    let resolve!: (r: DBIntegrityReport) => void
    vi.mocked(api.checkDBIntegrity).mockReturnValue(new Promise(r => { resolve = r }))
    render(<DatabaseCheckSection />)
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCheckButton' }))

    const busy = await screen.findByRole('button', { name: 'settings.general.dbCheckRunning' })
    expect((busy as HTMLButtonElement).disabled).toBe(true)

    resolve(drift)
    await waitFor(() => expect(screen.getByText('book_files -> books')).toBeTruthy())
    expect(screen.getByText('downloads -> books')).toBeTruthy()
    expect(screen.getByText('4')).toBeTruthy()
    expect(screen.getByText('settings.general.dbCheckOnDeleteCascade')).toBeTruthy()
    expect(screen.getByText('settings.general.dbCheckOnDeleteSetNull')).toBeTruthy()
    expect(screen.queryByText('settings.general.dbCheckClean')).toBeNull()
    expect((screen.getByRole('button', { name: 'settings.general.dbCheckButton' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('tells apart two foreign keys from one child table to the same parent', async () => {
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.checkDBIntegrity).mockResolvedValue({
      total: 3,
      checkedAt: '2026-10-18T12:00:00Z',
      relationships: [
        { childTable: 'requests', parentTable: 'users', onDelete: 'CASCADE', violations: 2 },
        { childTable: 'requests', parentTable: 'users', onDelete: 'SET NULL', violations: 1 },
      ],
    })
    render(<DatabaseCheckSection />)
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCheckButton' }))
    await waitFor(() => expect(screen.getByRole('table')).toBeTruthy())
    const rows = screen.getAllByRole('row').slice(1)
    expect(rows).toHaveLength(2)
    expect(rows[0].textContent).toContain('(CASCADE)')
    expect(rows[1].textContent).toContain('(SET NULL)')
    // No duplicate React key warning.
    expect(errSpy.mock.calls.some(c => String(c[0]).includes('same key'))).toBe(false)
    errSpy.mockRestore()
  })

  it('shows the clean state when nothing drifted', async () => {
    vi.mocked(api.checkDBIntegrity).mockResolvedValue({ total: 0, relationships: [], checkedAt: '2026-10-18T12:00:00Z' })
    render(<DatabaseCheckSection />)
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCheckButton' }))
    await waitFor(() => expect(screen.getByText('settings.general.dbCheckClean')).toBeTruthy())
    expect(screen.queryByRole('table')).toBeNull()
  })

  it('shows the server message on failure and clears any earlier result', async () => {
    vi.mocked(api.checkDBIntegrity).mockResolvedValueOnce(drift)
    render(<DatabaseCheckSection />)
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCheckButton' }))
    await waitFor(() => expect(screen.getByRole('table')).toBeTruthy())

    vi.mocked(api.checkDBIntegrity).mockRejectedValueOnce(new Error('a database check is already running'))
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCheckButton' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('a database check is already running'))
    expect(screen.queryByRole('table')).toBeNull()
  })

  it('points at db-repair and offers no cleanup control before a check found drift', () => {
    render(<DatabaseCheckSection />)
    expect(screen.getByText(/dbCheckRepairNote/)).toBeTruthy()
    const link = screen.getByRole('link', { name: 'settings.general.dbCheckDocsLink' }) as HTMLAnchorElement
    expect(link.href).toContain('docs/DEPLOYMENT.md#database-foreign-key-integrity')
    expect(screen.getAllByRole('button')).toHaveLength(1)
    expect(screen.queryByRole('button', { name: 'settings.general.dbCleanupButton' })).toBeNull()
  })

  it('offers no cleanup when the check is clean', async () => {
    vi.mocked(api.checkDBIntegrity).mockResolvedValue({ total: 0, relationships: [], checkedAt: '2026-10-18T12:00:00Z' })
    render(<DatabaseCheckSection />)
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCheckButton' }))
    await waitFor(() => expect(screen.getByText('settings.general.dbCheckClean')).toBeTruthy())
    expect(screen.queryByRole('button', { name: 'settings.general.dbCleanupButton' })).toBeNull()
  })
})

const preview: DBCleanupPreview = {
  total: 7,
  fixable: 6,
  unfixable: 1,
  checkedAt: '2026-10-19T12:00:00Z',
  previewToken: 'tok.abc.def',
  expiresAt: '2026-10-19T12:15:00Z',
  relationships: [
    { childTable: 'authors', parentTable: 'users', onDelete: 'NO ACTION', violations: 1, action: 'keep', fixable: 0 },
    { childTable: 'book_files', parentTable: 'books', onDelete: 'CASCADE', violations: 4, action: 'delete', fixable: 4 },
    { childTable: 'downloads', parentTable: 'books', onDelete: 'SET NULL', violations: 2, action: 'clear', fixable: 2 },
  ],
}

const done: DBCleanupResult = {
  deleted: 4,
  cleared: 2,
  skipped: 1,
  skipNotes: ['authors -> users: ON DELETE NO ACTION, no safe automatic repair'],
  remaining: [{ childTable: 'authors', parentTable: 'users', onDelete: 'NO ACTION', violations: 1, action: 'keep', fixable: 0 }],
  remainingTotal: 1,
  verified: true,
  backupPath: '/config/backups/bindery_20261019_120000_pre-fk-cleanup.db',
  backupName: 'bindery_20261019_120000_pre-fk-cleanup.db',
  durationMs: 1200,
  completedAt: '2026-10-19T12:00:05Z',
}

async function checkThenPreview() {
  vi.mocked(api.checkDBIntegrity).mockResolvedValue(drift)
  render(<DatabaseCheckSection />)
  fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCheckButton' }))
  fireEvent.click(await screen.findByRole('button', { name: 'settings.general.dbCleanupButton' }))
  await screen.findByTestId('db-cleanup-preview')
}

describe('DatabaseCheckSection cleanup', () => {
  it('previews per relationship, and changes nothing until the confirm click', async () => {
    vi.mocked(api.previewDBCleanup).mockResolvedValue(preview)
    await checkThenPreview()
    const box = screen.getByTestId('db-cleanup-preview')
    expect(box.textContent).toContain('book_files -> books')
    expect(box.textContent).toContain('settings.general.dbCleanupActionDelete')
    expect(box.textContent).toContain('settings.general.dbCleanupActionClear')
    expect(box.textContent).toContain('settings.general.dbCleanupActionKeep')
    expect(box.textContent).toContain('"fixable":6')
    expect(box.textContent).toContain('"unfixable":1')
    expect(screen.getByText('settings.general.dbCleanupBackupNote')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'settings.general.dbCleanupConfirm' })).toBeTruthy()
    expect(api.cleanupDB).not.toHaveBeenCalled()
  })

  it('sends the previewed token on confirm, then shows counts, the backup path and what is left', async () => {
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.previewDBCleanup).mockResolvedValue(preview)
    let resolve!: (r: DBCleanupResult) => void
    vi.mocked(api.cleanupDB).mockReturnValue(new Promise(r => { resolve = r }))
    await checkThenPreview()

    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCleanupConfirm' }))
    const busy = await screen.findByRole('button', { name: 'settings.general.dbCleanupConfirming' })
    expect((busy as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'settings.general.dbCheckButton' }) as HTMLButtonElement).disabled).toBe(true)
    expect(api.cleanupDB).toHaveBeenCalledTimes(1)
    expect(api.cleanupDB).toHaveBeenCalledWith('tok.abc.def')

    resolve(done)
    const result = await screen.findByTestId('db-cleanup-result')
    expect(result.textContent).toContain('"deleted":4')
    expect(result.textContent).toContain('/config/backups/bindery_20261019_120000_pre-fk-cleanup.db')
    expect(result.textContent).toContain('authors -> users')
    expect(result.textContent).toContain('settings.general.dbCleanupRemaining')
    // The preview is spent: no second confirm button.
    expect(screen.queryByRole('button', { name: 'settings.general.dbCleanupConfirm' })).toBeNull()
    expect(errSpy.mock.calls.some(c => String(c[0]).includes('same key'))).toBe(false)
    errSpy.mockRestore()
  })

  it('shows two foreign keys from one child to the same parent without a duplicate key warning', async () => {
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const pair = [
      { childTable: 'requests', parentTable: 'users', onDelete: 'CASCADE', violations: 2, action: 'delete' as const, fixable: 2 },
      { childTable: 'requests', parentTable: 'users', onDelete: 'SET NULL', violations: 1, action: 'clear' as const, fixable: 1 },
    ]
    vi.mocked(api.previewDBCleanup).mockResolvedValue({ ...preview, relationships: pair })
    vi.mocked(api.cleanupDB).mockResolvedValue({ ...done, remainingTotal: 3, remaining: pair })
    await checkThenPreview()
    expect(screen.getByTestId('db-cleanup-preview').querySelectorAll('tbody tr')).toHaveLength(2)
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCleanupConfirm' }))
    const result = await screen.findByTestId('db-cleanup-result')
    expect(result.querySelectorAll('tbody tr')).toHaveLength(2)
    expect(errSpy.mock.calls.some(c => String(c[0]).includes('same key'))).toBe(false)
    errSpy.mockRestore()
  })

  it('does not overstate: shows rows that cannot be repaired and skipped relationships', async () => {
    vi.mocked(api.previewDBCleanup).mockResolvedValue({
      ...preview,
      fixable: 1,
      unfixable: 6,
      relationships: [
        { childTable: 'book_files', parentTable: 'books', onDelete: 'CASCADE', violations: 4, action: 'delete', fixable: 1 },
        { childTable: 'downloads', parentTable: 'books', onDelete: 'SET NULL', violations: 2, action: 'skipped', fixable: 0 },
      ],
    })
    await checkThenPreview()
    const box = screen.getByTestId('db-cleanup-preview')
    expect(box.textContent).toContain('settings.general.dbCleanupPartial {"count":3}')
    expect(box.textContent).toContain('settings.general.dbCleanupActionSkipped')
    // The skipped relationship has no partial note of its own (nothing is fixable in it).
    expect(box.textContent?.match(/dbCleanupPartial/g)).toHaveLength(1)
  })

  it('cancel drops the preview without cleaning anything', async () => {
    vi.mocked(api.previewDBCleanup).mockResolvedValue(preview)
    await checkThenPreview()
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCleanupCancel' }))
    expect(screen.queryByTestId('db-cleanup-preview')).toBeNull()
    expect(api.cleanupDB).not.toHaveBeenCalled()
  })

  it('offers no confirm when nothing is fixable', async () => {
    vi.mocked(api.previewDBCleanup).mockResolvedValue({
      ...preview, fixable: 0, unfixable: 1, previewToken: undefined, expiresAt: undefined,
      relationships: [preview.relationships[0]],
    })
    await checkThenPreview()
    expect(screen.getByText('settings.general.dbCleanupNothing')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'settings.general.dbCleanupConfirm' })).toBeNull()
  })

  it('shows the server message when the confirmation is refused and needs a fresh preview', async () => {
    vi.mocked(api.previewDBCleanup).mockResolvedValue(preview)
    vi.mocked(api.cleanupDB).mockRejectedValue(new Error('the database changed since the preview; preview again'))
    await checkThenPreview()
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCleanupConfirm' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('the database changed since the preview'))
    expect(screen.getByRole('alert').textContent).toContain('settings.general.dbCleanupFailed')
    // The stale preview is gone, so the spent token cannot be sent again.
    expect(screen.queryByRole('button', { name: 'settings.general.dbCleanupConfirm' })).toBeNull()
    expect(screen.queryByTestId('db-cleanup-result')).toBeNull()
    expect(api.cleanupDB).toHaveBeenCalledTimes(1)
  })

  it('shows the server message when the preview fails', async () => {
    vi.mocked(api.checkDBIntegrity).mockResolvedValue(drift)
    vi.mocked(api.previewDBCleanup).mockRejectedValue(new Error('a database check is already running'))
    render(<DatabaseCheckSection />)
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCheckButton' }))
    fireEvent.click(await screen.findByRole('button', { name: 'settings.general.dbCleanupButton' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('a database check is already running'))
    expect(screen.getByRole('alert').textContent).toContain('settings.general.dbCleanupPreviewFailed')
    expect(screen.queryByTestId('db-cleanup-preview')).toBeNull()
  })

  it('says so when the check after the cleanup could not finish', async () => {
    vi.mocked(api.previewDBCleanup).mockResolvedValue(preview)
    vi.mocked(api.cleanupDB).mockResolvedValue({ ...done, verified: false, remaining: [], remainingTotal: 0 })
    await checkThenPreview()
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCleanupConfirm' }))
    await screen.findByTestId('db-cleanup-result')
    expect(screen.getByRole('alert').textContent).toContain('settings.general.dbCleanupNotVerified')
    expect(screen.queryByText('settings.general.dbCleanupRemainingNone')).toBeNull()
  })

  it('reports a fully clean database after the cleanup', async () => {
    vi.mocked(api.previewDBCleanup).mockResolvedValue(preview)
    vi.mocked(api.cleanupDB).mockResolvedValue({ ...done, remaining: [], remainingTotal: 0, skipped: 0 })
    await checkThenPreview()
    fireEvent.click(screen.getByRole('button', { name: 'settings.general.dbCleanupConfirm' }))
    await screen.findByTestId('db-cleanup-result')
    expect(screen.getByText('settings.general.dbCleanupRemainingNone')).toBeTruthy()
  })
})

describe('GeneralTab database check', () => {
  it('shows the control to admins without scanning on load', async () => {
    render(<GeneralTab />)
    // The Appearance heading shows for every role once the tab has loaded.
    await screen.findByText('settings.general.appearance')
    expect(screen.getByRole('button', { name: 'settings.general.dbCheckButton' })).toBeTruthy()
    expect(api.checkDBIntegrity).not.toHaveBeenCalled()
  })

  it('hides the control from non admins', async () => {
    auth.isAdmin = false
    render(<GeneralTab />)
    await screen.findByText('settings.general.appearance')
    expect(screen.queryByRole('button', { name: 'settings.general.dbCheckButton' })).toBeNull()
    expect(screen.queryByText('settings.general.dbCheck')).toBeNull()
    expect(api.checkDBIntegrity).not.toHaveBeenCalled()
  })
})
