import { beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router-dom'
import { screen } from '@testing-library/react'
import AuthGuard from './AuthGuard'
import { makeAuthStatus, renderWithRouter } from '../test-utils'

const { authState } = vi.hoisted(() => ({
  authState: {
    value: {
      status: null as unknown,
      loading: false,
    },
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
})
