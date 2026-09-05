import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import ApiKeysTab from './ApiKeysTab'
import { api } from '../../api/client'

// The Google Books key is write-only (#2351), so it never comes back from
// listSettings and the field always renders empty. Saving from that empty
// field would overwrite the stored key with "", which the user cannot see
// happen: the running process keeps serving the old key until it restarts.
// The save button must therefore stay disabled until something is typed.

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
    api: { ...actual.api, listSettings: vi.fn(), status: vi.fn(), setSetting: vi.fn() },
  }
})

beforeEach(() => {
  // Exactly what the server sends after #2351: the key is filtered out.
  vi.mocked(api.listSettings).mockResolvedValue([])
  vi.mocked(api.status).mockRejectedValue(new Error('no status'))
  vi.mocked(api.setSetting).mockReset()
  vi.mocked(api.setSetting).mockResolvedValue(undefined as never)
})

describe('Google Books API key field', () => {
  it('cannot be saved while empty, so a blank Save does not wipe the stored key', async () => {
    render(<ApiKeysTab />)
    const save = await screen.findByTestId('save-googlebooks-key')

    expect(save).toBeDisabled()

    fireEvent.click(save)
    await waitFor(() => expect(api.setSetting).not.toHaveBeenCalled())
  })

  it('can be saved once a key is typed', async () => {
    render(<ApiKeysTab />)
    const save = await screen.findByTestId('save-googlebooks-key')
    const input = screen.getByPlaceholderText(/Saved key is hidden/i)

    fireEvent.change(input, { target: { value: 'AIzaNEWKEY' } })
    expect(save).not.toBeDisabled()

    fireEvent.click(save)
    await waitFor(() =>
      expect(api.setSetting).toHaveBeenCalledWith('googlebooks.apiKey', 'AIzaNEWKEY'),
    )
  })
})
