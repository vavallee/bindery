import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import { acceptConfirm, cancelConfirm } from '../../test-utils'

// Covers the GeneralTab paths the topic-specific suites leave alone: the
// external-mode drop folder fields and their inline errors, the preferred
// language save error, a failed scan trigger, the last-scan summary variants,
// and the Security section for admins and non-admins.

const auth = vi.hoisted(() => ({
  isAdmin: true,
  authenticated: true,
  refresh: vi.fn(),
}))

vi.mock('../../components/ThemeToggle', () => ({ default: () => <button type="button">Theme</button> }))
vi.mock('../../components/LanguageSwitcher', () => ({ default: () => <select aria-label="Language" /> }))
vi.mock('../../settings/AuthSettings', () => ({ default: () => <div data-testid="auth-settings" /> }))
vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({
    status: { authenticated: auth.authenticated, username: 'admin', role: auth.isAdmin ? 'admin' : 'user', mode: 'enabled', setupRequired: false },
    loading: false,
    isAdmin: auth.isAdmin,
    refresh: auth.refresh,
    logout: vi.fn(),
  }),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => (typeof fallback === 'string' ? fallback : key),
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
      setSetting: vi.fn(),
      libraryScanStatus: vi.fn(),
      triggerLibraryScan: vi.fn(),
      getStorage: vi.fn(),
      authConfig: vi.fn(),
      authSetMode: vi.fn(),
      authRegenerateApiKey: vi.fn(),
      authRotateSessionSecret: vi.fn(),
      authChangePassword: vi.fn(),
    },
  }
})

import GeneralTab from './GeneralTab'
import { api } from '../../api/client'

const m = vi.mocked(api)

function seedSettings(entries: Record<string, string>) {
  m.listSettings.mockResolvedValue(Object.entries(entries).map(([key, value]) => ({ key, value })) as Awaited<ReturnType<typeof api.listSettings>>)
}

let alertSpy: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  vi.clearAllMocks()
  auth.isAdmin = true
  auth.authenticated = true
  auth.refresh.mockResolvedValue(undefined)
  seedSettings({})
  m.setSetting.mockResolvedValue(undefined)
  m.getStorage.mockRejectedValue(new Error('no storage'))
  m.libraryScanStatus.mockRejectedValue(new Error('no scan'))
  m.authConfig.mockResolvedValue({ mode: 'enabled', apiKey: 'secret-key-123', username: 'admin' })
  alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {})
})

afterEach(() => {
  alertSpy.mockRestore()
})

// The Save button that sits next to an input in the same row.
function saveNextTo(input: HTMLElement): HTMLElement {
  return within(input.parentElement!).getByRole('button')
}

describe('GeneralTab external import mode', () => {
  it('saves the drop folder and shows a rejected path inline', async () => {
    seedSettings({ 'import.mode': 'external', 'import.drop_folder': '/ingest' })
    m.setSetting.mockRejectedValueOnce(new Error('drop folder is not writable'))
    render(<GeneralTab />)

    const input = await screen.findByPlaceholderText('/cwa-book-ingest')
    await waitFor(() => expect(input).toHaveValue('/ingest'))
    fireEvent.click(saveNextTo(input))
    await waitFor(() => expect(m.setSetting).toHaveBeenCalledWith('import.drop_folder', '/ingest'))
    expect(await screen.findByText('drop folder is not writable')).toBeInTheDocument()

    // Editing clears the error.
    fireEvent.change(input, { target: { value: '/ingest2' } })
    expect(screen.queryByText('drop folder is not writable')).not.toBeInTheDocument()
  })

  it('saves the audiobook drop folder, falling back to the ebook folder as its placeholder', async () => {
    seedSettings({ 'import.mode': 'external', 'import.drop_folder': '/ingest' })
    m.setSetting.mockImplementation(async key => {
      if (key === 'import.audiobook.drop_folder') throw new Error('audiobook folder missing')
    })
    render(<GeneralTab />)

    const input = await screen.findByTestId('import-audiobook-drop-folder')
    await waitFor(() => expect(input).toHaveAttribute('placeholder', '/ingest'))
    fireEvent.change(input, { target: { value: '/abs-ingest' } })
    fireEvent.click(saveNextTo(input))
    await waitFor(() => expect(m.setSetting).toHaveBeenCalledWith('import.audiobook.drop_folder', '/abs-ingest'))
    expect(await screen.findByText('audiobook folder missing')).toBeInTheDocument()
    fireEvent.change(input, { target: { value: '/abs' } })
    expect(screen.queryByText('audiobook folder missing')).not.toBeInTheDocument()
  })

  it('persists the drop layout and placement choices', async () => {
    seedSettings({ 'import.mode': 'auto', 'import.audiobook.mode': 'external' })
    render(<GeneralTab />)

    // Only the audiobook side is external, so only its drop folder shows.
    const abInput = await screen.findByTestId('import-audiobook-drop-folder')
    expect(abInput).toHaveAttribute('placeholder', '/audiobook-ingest')
    expect(screen.queryByPlaceholderText('/cwa-book-ingest')).not.toBeInTheDocument()

    fireEvent.change(screen.getByDisplayValue('Flat (file in folder root)'), { target: { value: 'templated' } })
    await waitFor(() => expect(m.setSetting).toHaveBeenCalledWith('import.drop_layout', 'templated'))
    fireEvent.change(screen.getByDisplayValue('Copy'), { target: { value: 'hardlink' } })
    await waitFor(() => expect(m.setSetting).toHaveBeenCalledWith('import.drop_link_mode', 'hardlink'))
  })

  it('offers multi-disc flattening only in copy or hardlink audiobook mode', async () => {
    seedSettings({ 'import.audiobook.mode': 'copy' })
    render(<GeneralTab />)

    const flatten = await screen.findByRole('checkbox', { name: /Flatten multi-disc audiobooks/ })
    fireEvent.click(flatten)
    await waitFor(() => expect(m.setSetting).toHaveBeenCalledWith('import.audiobook.flatten_multi_disc', 'true'))

    fireEvent.change(screen.getByTestId('import-audiobook-mode'), { target: { value: 'move' } })
    await waitFor(() => expect(m.setSetting).toHaveBeenCalledWith('import.audiobook.mode', 'move'))
    expect(screen.queryByRole('checkbox', { name: /Flatten multi-disc audiobooks/ })).not.toBeInTheDocument()
  })
})

describe('GeneralTab preferred language', () => {
  it('shows the save error inline', async () => {
    m.setSetting.mockRejectedValueOnce(new Error('invalid language'))
    render(<GeneralTab />)
    const select = await screen.findByDisplayValue('settings.general.preferredLanguageEn')
    fireEvent.change(select, { target: { value: 'any' } })
    fireEvent.click(saveNextTo(select))
    await waitFor(() => expect(m.setSetting).toHaveBeenCalledWith('search.preferredLanguage', 'any'))
    expect(await screen.findByText('invalid language')).toBeInTheDocument()
  })
})

describe('GeneralTab library scan', () => {
  it('reports a scan that fails to start and re-enables the button', async () => {
    m.triggerLibraryScan.mockRejectedValue(new Error('scanner busy'))
    render(<GeneralTab />)
    const button = await screen.findByText('settings.general.scanLibraryButton')
    fireEvent.click(button)
    expect(await screen.findByText('Scan failed: scanner busy')).toBeInTheDocument()
    expect(screen.getByText('settings.general.scanLibraryButton')).not.toBeDisabled()
  })

  it('says a scan requested during a running one is queued (#3014)', async () => {
    m.triggerLibraryScan.mockResolvedValue({ message: 'library scan queued', queued: true })
    render(<GeneralTab />)
    fireEvent.click(await screen.findByText('settings.general.scanLibraryButton'))
    expect(await screen.findByText('settings.general.scanQueued')).toBeInTheDocument()
  })

  it('waits for the result carrying its own scan id, not the running scan it queued behind (#3014)', async () => {
    vi.useFakeTimers()
    try {
      m.triggerLibraryScan.mockResolvedValue({ message: 'library scan queued', queued: true, scanId: 'b-2' })
      // The scan already running finishes first, with a fresh ran_at.
      m.libraryScanStatus.mockResolvedValue({
        ran_at: new Date(Date.now() + 60_000).toISOString(),
        scan_id: 'b-1', running: true, queued: true,
        files_found: 1, reconciled: 0, unmatched: 0,
      })
      render(<GeneralTab />)
      await act(async () => { await vi.advanceTimersByTimeAsync(0) })
      fireEvent.click(screen.getByText('settings.general.scanLibraryButton'))
      await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
      expect(screen.getByText('settings.general.scanning')).toBeInTheDocument()

      // Past two minutes, still waiting while the server says a scan is going.
      await act(async () => { await vi.advanceTimersByTimeAsync(180_000) })
      expect(screen.getByText('settings.general.scanning')).toBeInTheDocument()
      expect(screen.queryByText('settings.general.scanCheckBack')).not.toBeInTheDocument()

      // Our scan's result lands.
      m.libraryScanStatus.mockResolvedValue({
        ran_at: new Date().toISOString(), scan_id: 'b-2', running: false, queued: false,
        files_found: 7, reconciled: 1, unmatched: 0,
      })
      await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
      expect(screen.getByText('settings.general.scanLibraryButton')).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('shows the stored scan error, scanned paths and the unmatched books link', async () => {
    m.libraryScanStatus.mockResolvedValue({
      ran_at: '2026-10-01T00:00:00Z',
      files_found: 12,
      reconciled: 10,
      unmatched: 2,
      already_tracked: 4,
      unmatched_units: 2,
      library_dir: '/books',
      audiobook_dir: '/audiobooks',
      scan_error: 'permission denied on /books/x',
      // The panel reads already_tracked, which the API type does not declare.
    } as Awaited<ReturnType<typeof api.libraryScanStatus>>)
    render(<GeneralTab />)
    expect(await screen.findByText('permission denied on /books/x')).toBeInTheDocument()
    expect(screen.getByText('settings.general.alreadyTracked')).toBeInTheDocument()
    expect(screen.getByText(/^\/books,/)).toBeInTheDocument()
    expect(screen.getByText('/audiobooks')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'settings.general.reviewUnmatched' })).toHaveAttribute('href', '/import')
    expect(screen.queryByText('settings.general.scanNoFilesWarning')).not.toBeInTheDocument()
  })

  it('warns when the last scan found no files', async () => {
    m.libraryScanStatus.mockResolvedValue({
      ran_at: '2026-10-01T00:00:00Z',
      files_found: 0,
      reconciled: 0,
      unmatched: 0,
      scanned_paths: ['/books'],
    })
    render(<GeneralTab />)
    expect(await screen.findByText('settings.general.scanNoFilesWarning')).toBeInTheDocument()
    expect(screen.queryByText('settings.general.alreadyTracked')).not.toBeInTheDocument()
  })

  it('never asks a non-admin for the scan summary', async () => {
    auth.isAdmin = false
    render(<GeneralTab />)
    await waitFor(() => expect(m.listSettings).toHaveBeenCalled())
    expect(m.libraryScanStatus).not.toHaveBeenCalled()
  })
})

describe('GeneralTab security section', () => {
  it('reveals, regenerates and hides the API key', async () => {
    m.authRegenerateApiKey.mockResolvedValue({ apiKey: 'fresh-key-456' })
    render(<GeneralTab />)

    const show = await screen.findByRole('button', { name: 'Show' })
    expect(screen.queryByText('secret-key-123')).not.toBeInTheDocument()
    fireEvent.click(show)
    expect(screen.getByText('secret-key-123')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Hide' }))
    expect(screen.queryByText('secret-key-123')).not.toBeInTheDocument()

    // Declining the confirmation leaves the key alone.
    fireEvent.click(screen.getByRole('button', { name: 'Regenerate' }))
    await cancelConfirm()
    expect(m.authRegenerateApiKey).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Regenerate' }))
    await acceptConfirm()
    // The new key is shown straight away so it can be copied.
    expect(await screen.findByText('fresh-key-456')).toBeInTheDocument()
  })

  it('alerts when regenerating fails', async () => {
    m.authRegenerateApiKey.mockRejectedValue(new Error('denied'))
    render(<GeneralTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Regenerate' }))
    await acceptConfirm()
    await waitFor(() => expect(alertSpy).toHaveBeenCalledWith('Regenerate failed: denied'))
  })

  it('rotates the session secret after the acknowledged confirmation, and alerts on failure', async () => {
    m.authRotateSessionSecret.mockResolvedValueOnce({ ok: true }).mockRejectedValueOnce(new Error('locked'))
    render(<GeneralTab />)

    fireEvent.click(await screen.findByRole('button', { name: 'Rotate session secret' }))
    await acceptConfirm()
    await waitFor(() => expect(m.authRotateSessionSecret).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(alertSpy).toHaveBeenCalledWith(expect.stringMatching(/^Session secret rotated/)))

    fireEvent.click(await screen.findByRole('button', { name: 'Rotate session secret' }))
    await acceptConfirm()
    await waitFor(() => expect(alertSpy).toHaveBeenCalledWith('Rotate failed: locked'))
  })

  it('changes the auth mode, refreshes auth and reloads the config', async () => {
    m.authSetMode.mockResolvedValue({ mode: 'disabled' })
    render(<GeneralTab />)
    const select = await screen.findByDisplayValue('Enabled')
    m.authConfig.mockResolvedValue({ mode: 'disabled', apiKey: 'k', username: 'admin' })
    fireEvent.change(select, { target: { value: 'disabled' } })

    await waitFor(() => expect(m.authSetMode).toHaveBeenCalledWith('disabled'))
    await waitFor(() => expect(auth.refresh).toHaveBeenCalled())
    await waitFor(() => expect(m.authConfig).toHaveBeenCalledTimes(2))
    expect(await screen.findByDisplayValue('Disabled (no auth)')).toBeInTheDocument()
    expect(screen.queryByTestId('auth-mode-warning')).not.toBeInTheDocument()
  })

  it('alerts when the mode change fails', async () => {
    m.authSetMode.mockRejectedValue(new Error('nope'))
    render(<GeneralTab />)
    fireEvent.change(await screen.findByDisplayValue('Enabled'), { target: { value: 'local-only' } })
    await waitFor(() => expect(alertSpy).toHaveBeenCalledWith('Mode change failed: nope'))
    expect(auth.refresh).not.toHaveBeenCalled()
  })

  it('shows a non-admin only the password form', async () => {
    auth.isAdmin = false
    render(<GeneralTab />)
    expect(await screen.findByPlaceholderText('Current password')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Regenerate' })).not.toBeInTheDocument()
    expect(screen.queryByTestId('auth-settings')).not.toBeInTheDocument()
  })

  it('validates and submits a password change', async () => {
    m.authChangePassword.mockRejectedValueOnce(new Error('current password is wrong')).mockResolvedValueOnce({ ok: true })
    render(<GeneralTab />)

    const current = await screen.findByPlaceholderText('Current password')
    const next = screen.getByPlaceholderText('New password')
    const confirmPw = screen.getByPlaceholderText('Confirm new password')
    const submit = screen.getByRole('button', { name: 'Change password' })
    expect(submit).toBeDisabled()

    fireEvent.change(current, { target: { value: 'old-password' } })
    fireEvent.change(next, { target: { value: 'short' } })
    fireEvent.change(confirmPw, { target: { value: 'different' } })
    fireEvent.click(submit)
    expect(await screen.findByText('New passwords do not match')).toBeInTheDocument()

    fireEvent.change(confirmPw, { target: { value: 'short' } })
    fireEvent.click(submit)
    expect(await screen.findByText('Password must be at least 8 characters')).toBeInTheDocument()
    expect(m.authChangePassword).not.toHaveBeenCalled()

    fireEvent.change(next, { target: { value: 'long-enough-pw' } })
    fireEvent.change(confirmPw, { target: { value: 'long-enough-pw' } })
    fireEvent.click(submit)
    expect(await screen.findByText('current password is wrong')).toBeInTheDocument()
    expect(m.authChangePassword).toHaveBeenCalledWith('old-password', 'long-enough-pw')

    fireEvent.click(submit)
    expect(await screen.findByText('Password updated')).toBeInTheDocument()
    expect(current).toHaveValue('')
  })

  it('renders nothing for the section until the auth config loads', async () => {
    m.authConfig.mockRejectedValue(new Error('forbidden'))
    render(<GeneralTab />)
    await waitFor(() => expect(m.authConfig).toHaveBeenCalled())
    expect(screen.queryByPlaceholderText('Current password')).not.toBeInTheDocument()
  })
})
