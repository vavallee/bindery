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
        'users.autoApproveDefault': 'Use default',
        'users.autoApproveAlways': 'Always',
        'users.autoApproveNever': 'Never',
        'users.autoApproveEffectiveOn': 'Approves on its own',
        'users.autoApproveEffectiveOff': 'Waits for an admin',
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
      { id: 3, username: 'guest', role: 'requester', createdAt: '2026-01-01T00:00:00Z', autoApproveRequests: null, autoApproveEffective: false },
    ])
    vi.mocked(api.setUserRole).mockResolvedValue({ ok: true })
    vi.mocked(api.setUserAutoApprove).mockResolvedValue({ ok: true })
  })

  // #3240: the add-user form's labels name their fields.
  it('names the add-user fields by their labels', async () => {
    render(<UsersPage />)
    await screen.findByRole('combobox', { name: 'Role for reader' })
    expect(screen.getByLabelText('users.fieldUsername').tagName).toBe('INPUT')
    expect(screen.getByLabelText('users.fieldPassword').tagName).toBe('INPUT')
    expect(screen.getByLabelText('Role').tagName).toBe('SELECT')
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
  it('offers Use default, Always and Never for a requester', async () => {
    render(<UsersPage />)
    const select = await screen.findByRole('combobox', { name: 'Auto-approve requests for guest' })
    expect(within(select).getAllByRole('option').map(o => (o as HTMLOptionElement).value)).toEqual(['default', 'always', 'never'])
    // An account with no setting of its own shows Use default and its effective result.
    expect(select).toHaveValue('default')
    expect(screen.getByTestId('auto-approve-effective-3')).toHaveTextContent('Waits for an admin')

    fireEvent.change(select, { target: { value: 'always' } })
    await waitFor(() => expect(api.setUserAutoApprove).toHaveBeenCalledWith(3, true))
    expect(select).toHaveValue('always')

    fireEvent.change(select, { target: { value: 'never' } })
    await waitFor(() => expect(api.setUserAutoApprove).toHaveBeenCalledWith(3, false))
    expect(select).toHaveValue('never')

    // Back to the default sends null, not false.
    vi.mocked(api.setUserAutoApprove).mockResolvedValue({ ok: true, autoApproveRequests: null, autoApproveEffective: true })
    fireEvent.change(select, { target: { value: 'default' } })
    await waitFor(() => expect(api.setUserAutoApprove).toHaveBeenLastCalledWith(3, null))
    expect(select).toHaveValue('default')
    await waitFor(() => expect(screen.getByTestId('auto-approve-effective-3')).toHaveTextContent('Approves on its own'))
  })

  it('does not offer auto-approve for an admin', async () => {
    render(<UsersPage />)
    await screen.findByRole('combobox', { name: 'Role for admin' })
    expect(screen.queryByRole('combobox', { name: 'Auto-approve requests for admin' })).not.toBeInTheDocument()
  })
})

// The Auto-approve note belongs to that column, which only means something
// for a requester; with none listed it read as the page's description.
describe('UsersPage auto-approve note', () => {
  it('shows the note only when a requester is listed', async () => {
    vi.mocked(api.listUsers).mockResolvedValue([
      { id: 1, username: 'admin', role: 'admin', createdAt: '2026-01-01T00:00:00Z', autoApproveRequests: false },
    ])
    const { unmount } = render(<UsersPage />)
    await screen.findAllByText('admin')
    expect(screen.queryByText('users.autoApproveHint')).toBeNull()
    unmount()
    vi.mocked(api.listUsers).mockResolvedValue([
      { id: 1, username: 'admin', role: 'admin', createdAt: '2026-01-01T00:00:00Z', autoApproveRequests: false },
      { id: 3, username: 'guest', role: 'requester', createdAt: '2026-01-01T00:00:00Z', autoApproveRequests: false },
    ])
    render(<UsersPage />)
    expect(await screen.findByText('users.autoApproveHint')).toBeInTheDocument()
  })
})
