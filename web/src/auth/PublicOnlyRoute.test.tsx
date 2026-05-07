import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import PublicOnlyRoute from './PublicOnlyRoute'
import type { AuthStatus } from '../api/client'

// We swap the useAuth implementation per-test via vi.mock + a mutable holder.
// AuthContext is otherwise untouched; this keeps the test focused on the
// route-guard's decision tree without spinning up the real provider.
let mockAuth: { status: AuthStatus | null; loading: boolean } = {
  status: null,
  loading: false,
}

vi.mock('./AuthContext', () => ({
  useAuth: () => mockAuth,
}))

function renderAt(path: string, mode: 'login' | 'setup') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/" element={<div data-testid="home" />} />
        <Route path="/setup" element={<div data-testid="setup" />} />
        <Route
          path="/login"
          element={
            <PublicOnlyRoute mode={mode}>
              <div data-testid="login-page" />
            </PublicOnlyRoute>
          }
        />
      </Routes>
    </MemoryRouter>,
  )
}

describe('PublicOnlyRoute — login mode', () => {
  it('redirects authenticated users away from /login to /', () => {
    mockAuth = {
      loading: false,
      status: { authenticated: true, setupRequired: false, mode: 'enabled' },
    }
    renderAt('/login', 'login')
    expect(screen.getByTestId('home')).toBeInTheDocument()
    expect(screen.queryByTestId('login-page')).toBeNull()
  })

  it('renders the login page for unauthenticated users', () => {
    mockAuth = {
      loading: false,
      status: { authenticated: false, setupRequired: false, mode: 'enabled' },
    }
    renderAt('/login', 'login')
    expect(screen.getByTestId('login-page')).toBeInTheDocument()
  })

  it('shows the loading placeholder while auth status is in flight (no flash redirect)', () => {
    mockAuth = { loading: true, status: null }
    renderAt('/login', 'login')
    expect(screen.queryByTestId('login-page')).toBeNull()
    expect(screen.queryByTestId('home')).toBeNull()
    expect(screen.getByText(/Loading/i)).toBeInTheDocument()
  })

  it('routes to /setup when setup is still required, even on /login', () => {
    mockAuth = {
      loading: false,
      status: { authenticated: false, setupRequired: true, mode: 'enabled' },
    }
    renderAt('/login', 'login')
    expect(screen.getByTestId('setup')).toBeInTheDocument()
    expect(screen.queryByTestId('login-page')).toBeNull()
  })
})
