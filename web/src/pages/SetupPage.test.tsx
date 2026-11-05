import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import SetupPage from './SetupPage'
import { apiUrl, server } from '../test/msw'
import { renderWithRouter } from '../test-utils'

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({ status: null, refresh: vi.fn() }),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}))

describe('SetupPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('shows a friendly message when setup is not offered', async () => {
    server.use(
      http.post(apiUrl('/auth/setup'), () =>
        HttpResponse.json({ error: 'raw server text' }, { status: 403 }),
      ),
    )
    renderWithRouter(<SetupPage />, { initialEntries: ['/setup'] })

    const inputs = document.querySelectorAll('input')
    fireEvent.change(inputs[0], { target: { value: 'admin' } })
    fireEvent.change(inputs[1], { target: { value: 'long-enough-password' } })
    fireEvent.change(inputs[2], { target: { value: 'long-enough-password' } })
    const form = inputs[0].closest('form')
    if (!form) throw new Error('form missing')
    fireEvent.submit(form)

    await waitFor(() => expect(screen.getByText('setup.errorLocalDisabled')).toBeInTheDocument())
    expect(screen.queryByText('raw server text')).not.toBeInTheDocument()
  })
})
