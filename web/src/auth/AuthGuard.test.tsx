import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { act, fireEvent, screen } from '@testing-library/react'
import AuthGuard from './AuthGuard'
import { makeAuthStatus, renderWithRouter } from '../test-utils'

const { authState } = vi.hoisted(() => ({
  authState: {
    value: {
      status: null as unknown,
      loading: false,
    } as { status: unknown; loading: boolean; statusError?: boolean; refresh?: () => Promise<void> },
  },
}))

vi.mock('./AuthContext', () => ({
  useAuth: () => authState.value,
}))

function renderGuard(initialEntries = ['/']) {
  return renderWithRouter(
    <Routes>
      <Route
        path="/"
        element={
          <AuthGuard>
            <div>Protected content</div>
          </AuthGuard>
        }
      />
      <Route path="/setup" element={<div>Setup page</div>} />
      <Route path="/login" element={<div>Login page</div>} />
    </Routes>,
    { initialEntries },
  )
}

describe('AuthGuard', () => {
  beforeEach(() => {
    authState.value = {
      status: null,
      loading: false,
    }
  })

  it('renders a loading placeholder while auth status is loading', () => {
    authState.value = {
      status: null,
      loading: true,
    }

    renderGuard()

    expect(screen.getByText(/loading/i)).toBeInTheDocument()
  })

  it('redirects to setup when setup is required', () => {
    authState.value = {
      status: makeAuthStatus({ setupRequired: true }),
      loading: false,
    }

    renderGuard()

    expect(screen.getByText('Setup page')).toBeInTheDocument()
  })

  it('redirects to login when the user is not authenticated', () => {
    authState.value = {
      status: makeAuthStatus({ authenticated: false, setupRequired: false }),
      loading: false,
    }

    renderGuard()

    expect(screen.getByText('Login page')).toBeInTheDocument()
  })

  it('renders children when the user is authenticated', () => {
    authState.value = {
      status: makeAuthStatus({ authenticated: true, username: 'admin', role: 'admin' }),
      loading: false,
    }

    renderGuard()

    expect(screen.getByText('Protected content')).toBeInTheDocument()
  })

  it('offers a retry instead of the login page when the status check failed', async () => {
    let resolve: () => void = () => {}
    const refresh = vi.fn(() => new Promise<void>((r) => { resolve = r }))
    authState.value = {
      status: null,
      loading: false,
      statusError: true,
      refresh,
    }

    renderGuard()

    expect(screen.queryByText('Login page')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(refresh).toHaveBeenCalledTimes(1)
    const busy = screen.getByRole('button', { name: 'Retrying…' })
    expect(busy).toBeDisabled()
    await act(async () => { resolve() })
    expect(screen.getByRole('button', { name: 'Retry' })).toBeEnabled()
  })

  describe('automatic retry', () => {
    beforeEach(() => { vi.useFakeTimers() })
    afterEach(() => { vi.useRealTimers() })

    it('retries with a growing delay while the status check keeps failing', async () => {
      const refresh = vi.fn(() => Promise.resolve())
      authState.value = { status: null, loading: false, statusError: true, refresh }

      renderGuard()

      await act(async () => { await vi.advanceTimersByTimeAsync(1999) })
      expect(refresh).toHaveBeenCalledTimes(0)
      await act(async () => { await vi.advanceTimersByTimeAsync(1) })
      expect(refresh).toHaveBeenCalledTimes(1)
      // Second attempt waits 4s, then 8s, 16s, and stays at 30s.
      await act(async () => { await vi.advanceTimersByTimeAsync(3999) })
      expect(refresh).toHaveBeenCalledTimes(1)
      await act(async () => { await vi.advanceTimersByTimeAsync(1) })
      expect(refresh).toHaveBeenCalledTimes(2)
      await act(async () => { await vi.advanceTimersByTimeAsync(8000) })
      expect(refresh).toHaveBeenCalledTimes(3)
      await act(async () => { await vi.advanceTimersByTimeAsync(16000) })
      expect(refresh).toHaveBeenCalledTimes(4)
      await act(async () => { await vi.advanceTimersByTimeAsync(29999) })
      expect(refresh).toHaveBeenCalledTimes(4)
      await act(async () => { await vi.advanceTimersByTimeAsync(1) })
      expect(refresh).toHaveBeenCalledTimes(5)
      await act(async () => { await vi.advanceTimersByTimeAsync(30000) })
      expect(refresh).toHaveBeenCalledTimes(6)
    })

    it('stops retrying once a status loads', async () => {
      const refresh = vi.fn(() => Promise.resolve())
      authState.value = { status: null, loading: false, statusError: true, refresh }

      const view = renderGuard()
      await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
      expect(refresh).toHaveBeenCalledTimes(1)

      authState.value = {
        status: makeAuthStatus({ authenticated: true, username: 'admin', role: 'admin' }),
        loading: false,
        statusError: false,
        refresh,
      }
      view.rerender(
        <Routes>
          <Route path="/" element={<AuthGuard><div>Protected content</div></AuthGuard>} />
        </Routes>,
      )
      expect(screen.getByText('Protected content')).toBeInTheDocument()
      await act(async () => { await vi.advanceTimersByTimeAsync(120000) })
      expect(refresh).toHaveBeenCalledTimes(1)
    })
  })
})
