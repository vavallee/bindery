import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import GeneralTab from '../../test/allGeneralSections'
import { api } from '../../api/client'

vi.mock('../../components/ThemeToggle', () => ({ default: () => <button type="button">Theme</button> }))
vi.mock('../../components/LanguageSwitcher', () => ({ default: () => <select aria-label="Language" /> }))
vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({
    status: { authenticated: true, username: 'admin', role: 'admin', mode: 'enabled', setupRequired: false },
    loading: false,
    isAdmin: true,
    refresh: vi.fn(),
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
      libraryScanStatus: vi.fn(),
      getStorage: vi.fn(),
      authConfig: vi.fn(),
      setSetting: vi.fn(),
    },
  }
})

beforeEach(() => {
  vi.mocked(api.listSettings).mockResolvedValue([
    { key: 'naming.bookTemplate', value: '{Author}/{Title}.{ext}', updatedAt: '' },
    { key: 'naming_template_audiobook', value: '{Author}/{Title}', updatedAt: '' },
    { key: 'naming.audiobook_file_template', value: '{Title}.{ext}', updatedAt: '' },
  ] as unknown as Awaited<ReturnType<typeof api.listSettings>>)
  vi.mocked(api.libraryScanStatus).mockRejectedValue(new Error('no scan'))
  vi.mocked(api.getStorage).mockRejectedValue(new Error('no storage'))
  vi.mocked(api.authConfig).mockRejectedValue(new Error('no auth cfg'))
  vi.mocked(api.setSetting).mockReset()
})

// #1668 — the File Naming fields saved silently. These assert the confirmation
// is wired end to end through GeneralTab's saveSetting, which had to start
// rethrowing: it used to swallow the error, so a failed save would still have
// shown "Saved ✓".
describe('GeneralTab file-naming save confirmation', () => {
  it.each([
    ['naming-save-book', 'naming.bookTemplate'],
    ['naming-save-audiobook', 'naming_template_audiobook'],
    ['naming-save-audiobook-file', 'naming.audiobook_file_template'],
  ])('confirms the save on %s', async (testId, key) => {
    vi.mocked(api.setSetting).mockResolvedValue(undefined as never)
    render(<GeneralTab />)

    const btn = await screen.findByTestId(testId)
    fireEvent.click(btn)

    await waitFor(() => expect(api.setSetting).toHaveBeenCalledWith(key, expect.any(String)))
    await waitFor(() => expect(screen.getByTestId(testId)).toHaveTextContent('Saved ✓'))
  })

  it('reports a failed template save as an error, not as saved', async () => {
    vi.mocked(api.setSetting).mockRejectedValue(new Error('disk full'))
    render(<GeneralTab />)

    fireEvent.click(await screen.findByTestId('naming-save-book'))

    await waitFor(() => expect(screen.getByTestId('naming-save-book')).toHaveTextContent('Error'))
    expect(screen.getByTestId('naming-save-book')).not.toHaveTextContent('Saved ✓')
  })
})
