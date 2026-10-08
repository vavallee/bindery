import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import UsersPage from './UsersPage'
import { mockMatchMedia } from '../test-utils'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({ isAdmin: true, status: { username: 'admin' } }),
}))

vi.mock('../api/client', () => ({
  api: { listUsers: vi.fn() },
}))

import { api } from '../api/client'

// On a phone the five column table is wider than the screen. Its wrapper
// clipped the overflow, which cut off Reset password and Delete: the only
// way to reach either was to rotate the phone.
describe('UsersPage on a phone', () => {
  beforeEach(() => {
    vi.mocked(api.listUsers).mockResolvedValue([
      { id: 2, username: 'reader', role: 'user', createdAt: '2026-01-01T00:00:00Z', autoApproveRequests: false },
    ])
  })

  it('scrolls the table sideways instead of clipping it', async () => {
    render(<UsersPage />)
    const table = await screen.findByRole('table')
    const wrapper = table.parentElement!
    expect(wrapper.className).toContain('overflow-x-auto')
    expect(wrapper.className).not.toContain('overflow-hidden')
  })

  it('keeps the action cell a table cell', async () => {
    render(<UsersPage />)
    const reset = await screen.findByRole('button', { name: 'users.resetPassword' })
    const cell = reset.closest('td')!
    // A flex td drops out of the table layout, so its border and padding no
    // longer line up with the rest of the row.
    expect(cell.className.split(/\s+/)).not.toContain('flex')
    expect(cell).toContainElement(screen.getByRole('button', { name: 'common.delete' }))
  })
})

// Below sm the table scrolled sideways with no cue that Reset password and
// Delete were off to the right. Each user is a card there instead.
describe('UsersPage below sm', () => {
  let restore: () => void = () => {}
  beforeEach(() => {
    restore = mockMatchMedia(q => q === '(width < 40rem)')
    vi.mocked(api.listUsers).mockResolvedValue([
      { id: 1, username: 'admin', role: 'admin', createdAt: '2026-01-01T00:00:00Z', autoApproveRequests: false },
      { id: 2, username: 'kid', role: 'requester', createdAt: '2026-01-01T00:00:00Z', autoApproveRequests: true },
    ])
  })
  afterEach(() => restore())

  it('renders a card per user with every control in it, and no table', async () => {
    render(<UsersPage />)
    const cards = await screen.findByTestId('users-cards')
    expect(screen.queryByRole('table')).toBeNull()
    const items = within(cards).getAllByRole('listitem')
    expect(items).toHaveLength(2)
    const kid = items[1]
    expect(within(kid).getByText('kid')).toBeInTheDocument()
    expect(within(kid).getByRole('combobox', { name: 'users.roleFor' })).toHaveValue('requester')
    expect(within(kid).getByRole('checkbox', { name: 'users.autoApproveFor' })).toBeChecked()
    expect(within(kid).getByRole('button', { name: 'users.resetPassword' })).toBeInTheDocument()
    expect(within(kid).getByRole('button', { name: 'common.delete' })).toBeInTheDocument()
    // The signed in admin is marked as you.
    expect(within(items[0]).getByText('(users.you)')).toBeInTheDocument()
  })
})
