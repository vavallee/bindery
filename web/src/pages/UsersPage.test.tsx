import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import UsersPage from './UsersPage'

// The role column is a three value select (admin, user, requester), and the
// create form offers the same three.

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) => {
      const vars = (opts && typeof opts === 'object' ? opts : {}) as Record<string, unknown>
      const strings: Record<string, string> = {
        'users.roleFor': 'Role for {{username}}',
        'users.roleAdmin': 'Admin',
        'users.roleUser': 'User',
        'users.roleRequester': 'Requester',
        'users.fieldRole': 'Role',
        'users.autoApprove': 'Auto',
        'users.autoApproveFor': 'Auto-approve requests for {{username}}',
      }
      const s = strings[key] ?? key
      return s.replace(/\{\{(\w+)\}\}/g, (_m, name: string) => String(vars[name] ?? ''))
    },
  }),
}))

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({ isAdmin: true, status: { username: 'admin' } }),
}))

vi.mock('../api/client', () => ({
  api: {
    listUsers: vi.fn(),
    setUserRole: vi.fn(),
    setUserAutoApprove: vi.fn(),
    createUser: vi.fn(),
  },
}))

import { api } from '../api/client'

describe('UsersPage roles', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listUsers).mockResolvedValue([
      { id: 1, username: 'admin', role: 'admin', createdAt: '2026-01-01T00:00:00Z', autoApproveRequests: false },
      { id: 2, username: 'reader', role: 'user', createdAt: '2026-01-01T00:00:00Z', autoApproveRequests: false },
      { id: 3, username: 'guest', role: 'requester', createdAt: '2026-01-01T00:00:00Z', autoApproveRequests: false },
    ])
    vi.mocked(api.setUserRole).mockResolvedValue({ ok: true })
    vi.mocked(api.setUserAutoApprove).mockResolvedValue({ ok: true })
  })

  it('offers all three roles and sets requester', async () => {
    render(<UsersPage />)
    const select = await screen.findByRole('combobox', { name: 'Role for reader' })
    expect(within(select).getAllByRole('option').map(o => (o as HTMLOptionElement).value)).toEqual(['admin', 'user', 'requester'])

    fireEvent.change(select, { target: { value: 'requester' } })
    await waitFor(() => expect(api.setUserRole).toHaveBeenCalledWith(2, 'requester'))
    expect(select).toHaveValue('requester')
  })

  it('keeps the shown role when the server refuses the change', async () => {
    vi.mocked(api.setUserRole).mockRejectedValue(new Error('cannot demote the last admin user'))
    render(<UsersPage />)
    const select = await screen.findByRole('combobox', { name: 'Role for admin' })
    fireEvent.change(select, { target: { value: 'requester' } })
    expect(await screen.findByText('cannot demote the last admin user')).toBeInTheDocument()
    expect(select).toHaveValue('admin')
  })

  // The auto-approve toggle is per account and only shown for requesters.
  it('toggles auto-approve for a requester', async () => {
    render(<UsersPage />)
    const box = await screen.findByRole('checkbox', { name: 'Auto-approve requests for guest' })
    expect(box).not.toBeChecked()

    fireEvent.click(box)
    await waitFor(() => expect(api.setUserAutoApprove).toHaveBeenCalledWith(3, true))
    expect(box).toBeChecked()
  })

  it('does not offer auto-approve for an admin', async () => {
    render(<UsersPage />)
    await screen.findByRole('combobox', { name: 'Role for admin' })
    expect(screen.queryByRole('checkbox', { name: 'Auto-approve requests for admin' })).not.toBeInTheDocument()
  })
})
