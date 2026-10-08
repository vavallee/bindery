import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import UsersPage from './UsersPage'
import { ApiError } from '../api/core'
import type { ManagedUser } from '../api/client'
import { acceptConfirm, cancelConfirm } from '../test-utils'

// Keys come back verbatim, with {{vars}} appended, so assertions don't depend on
// English copy and still prove which user a label is about.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) => {
      const vars = opts && typeof opts === 'object' ? (opts as Record<string, unknown>) : {}
      const suffix = Object.values(vars).filter(v => typeof v === 'string' || typeof v === 'number').join(',')
      return suffix ? `${key}:${suffix}` : key
    },
  }),
}))

const auth = vi.hoisted(() => ({ isAdmin: true }))
vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({ isAdmin: auth.isAdmin, status: { username: 'admin' } }),
}))

vi.mock('../api/client', () => ({
  api: {
    listUsers: vi.fn(),
    setUserRole: vi.fn(),
    setUserAutoApprove: vi.fn(),
    createUser: vi.fn(),
    deleteUser: vi.fn(),
    resetUserPassword: vi.fn(),
  },
}))

import { api } from '../api/client'

function user(id: number, username: string, role: ManagedUser['role'] = 'user'): ManagedUser {
  return { id, username, role, createdAt: '2026-01-01T00:00:00Z', autoApproveRequests: false }
}

const owned = {
  authors: 2,
  books: 5,
  qualityProfiles: 0,
  metadataProfiles: 0,
  downloads: 0,
  rootFolders: 0,
  importLists: 0,
  blocklist: 0,
}

function rowFor(username: string): HTMLElement {
  return screen.getByText(username).closest('tr') as HTMLElement
}

describe('UsersPage coverage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    auth.isAdmin = true
    vi.mocked(api.listUsers).mockResolvedValue([user(1, 'admin', 'admin'), user(2, 'reader'), user(3, 'guest', 'requester')])
    vi.mocked(api.setUserRole).mockResolvedValue({ ok: true })
    vi.mocked(api.setUserAutoApprove).mockResolvedValue({ ok: true })
    vi.mocked(api.deleteUser).mockResolvedValue(undefined as never)
    vi.mocked(api.resetUserPassword).mockResolvedValue(undefined as never)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('blocks non-admins and never asks for the user list', () => {
    auth.isAdmin = false
    render(<UsersPage />)
    expect(screen.getByText('Admin access required.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
    expect(api.listUsers).not.toHaveBeenCalled()
  })

  it('shows a loading state, then the table, and marks the signed-in admin', async () => {
    let resolve: (u: ManagedUser[]) => void = () => {}
    vi.mocked(api.listUsers).mockReturnValue(new Promise(r => { resolve = r }))
    render(<UsersPage />)
    expect(screen.getByText('common.loading')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
    resolve([user(1, 'admin', 'admin'), user(2, 'reader')])
    expect(await screen.findByRole('table')).toBeInTheDocument()
    expect(within(rowFor('admin')).getByText('(users.you)')).toBeInTheDocument()
    expect(within(rowFor('reader')).queryByText('(users.you)')).toBeNull()
    expect(document.title).toBe('Users · Bindery')
  })

  it('shows the load error', async () => {
    vi.mocked(api.listUsers).mockRejectedValue(new Error('db locked'))
    render(<UsersPage />)
    expect(await screen.findByText('db locked')).toBeInTheDocument()
  })

  it('creates a user with the chosen role, appends it and clears the form', async () => {
    vi.mocked(api.createUser).mockResolvedValue(user(4, 'newbie', 'requester'))
    render(<UsersPage />)
    await screen.findByText('reader')

    const submit = screen.getByRole('button', { name: 'users.createButton' })
    expect(submit).toBeDisabled()
    const form = submit.closest('form') as HTMLFormElement
    const username = form.querySelector('input:not([type=password])') as HTMLInputElement
    const password = form.querySelector('input[type=password]') as HTMLInputElement
    const role = within(form).getByRole('combobox') as HTMLSelectElement
    fireEvent.change(username, { target: { value: 'newbie' } })
    fireEvent.change(password, { target: { value: 'hunter2hunter2' } })
    fireEvent.change(role, { target: { value: 'requester' } })
    expect(submit).toBeEnabled()
    fireEvent.click(submit)

    await waitFor(() => expect(api.createUser).toHaveBeenCalledWith('newbie', 'hunter2hunter2', 'requester'))
    expect(await screen.findByText('newbie')).toBeInTheDocument()
    expect(username.value).toBe('')
    expect(password.value).toBe('')
    expect(role.value).toBe('user')
  })

  it('shows a create error and keeps the form filled', async () => {
    vi.mocked(api.createUser).mockRejectedValue(new Error('username taken'))
    render(<UsersPage />)
    await screen.findByText('reader')
    const submit = screen.getByRole('button', { name: 'users.createButton' })
    const form = submit.closest('form') as HTMLFormElement
    const username = form.querySelector('input:not([type=password])') as HTMLInputElement
    fireEvent.change(username, { target: { value: 'reader' } })
    fireEvent.change(form.querySelector('input[type=password]') as HTMLInputElement, { target: { value: 'longenough' } })
    fireEvent.click(submit)
    expect(await screen.findByText('username taken')).toBeInTheDocument()
    expect(username.value).toBe('reader')
  })

  it('does not call the server when the role is unchanged', async () => {
    render(<UsersPage />)
    const select = await screen.findByRole('combobox', { name: 'users.roleFor:reader' })
    fireEvent.change(select, { target: { value: 'user' } })
    expect(api.setUserRole).not.toHaveBeenCalled()
  })

  it('reports an auto-approve failure and leaves the box unticked', async () => {
    vi.mocked(api.setUserAutoApprove).mockRejectedValue(new Error('nope'))
    render(<UsersPage />)
    const box = await screen.findByRole('checkbox', { name: 'users.autoApproveFor:guest' })
    fireEvent.click(box)
    expect(await screen.findByText('nope')).toBeInTheDocument()
    expect(box).not.toBeChecked()
  })

  it('deletes a user who owns nothing after confirmation', async () => {
    render(<UsersPage />)
    await screen.findByText('reader')
    fireEvent.click(within(rowFor('reader')).getByRole('button', { name: 'common.delete' }))
    await acceptConfirm()
    await waitFor(() => expect(api.deleteUser).toHaveBeenCalledWith(2))
    await waitFor(() => expect(screen.queryByText('reader')).toBeNull())
  })

  it('keeps the user when the delete confirmation is cancelled', async () => {
    render(<UsersPage />)
    await screen.findByText('reader')
    fireEvent.click(within(rowFor('reader')).getByRole('button', { name: 'common.delete' }))
    await cancelConfirm()
    expect(api.deleteUser).not.toHaveBeenCalled()
    expect(screen.getByText('reader')).toBeInTheDocument()
  })

  it('shows a plain delete failure', async () => {
    vi.mocked(api.deleteUser).mockRejectedValue(new Error('cannot delete yourself'))
    render(<UsersPage />)
    await screen.findByText('admin')
    fireEvent.click(within(rowFor('admin')).getByRole('button', { name: 'common.delete' }))
    await acceptConfirm()
    expect(await screen.findByText('cannot delete yourself')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('asks what to do with owned data on a 409, then reassigns it to the chosen user', async () => {
    vi.mocked(api.deleteUser)
      .mockRejectedValueOnce(new ApiError(409, { error: 'user owns data', counts: owned }, 'Conflict'))
      .mockResolvedValueOnce(undefined as never)
    render(<UsersPage />)
    await screen.findByText('reader')
    fireEvent.click(within(rowFor('reader')).getByRole('button', { name: 'common.delete' }))
    await acceptConfirm()

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('users.deleteTitle:reader')).toBeInTheDocument()
    const inheritor = within(dialog).getByRole('combobox', { name: 'users.deleteInheritor' })
    // The departing user can't inherit their own library.
    expect(within(inheritor).queryByRole('option', { name: 'reader' })).toBeNull()
    fireEvent.change(inheritor, { target: { value: '1' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'common.delete' }))

    await waitFor(() => expect(api.deleteUser).toHaveBeenLastCalledWith(2, { strategy: 'reassign', reassignTo: 1 }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.queryByText('reader')).toBeNull()
  })

  it('purges owned data when asked, and shows a failure of the second pass', async () => {
    vi.mocked(api.deleteUser)
      .mockRejectedValueOnce(new ApiError(409, { counts: owned }, 'Conflict'))
      .mockRejectedValueOnce(new Error('purge failed'))
    render(<UsersPage />)
    await screen.findByText('reader')
    fireEvent.click(within(rowFor('reader')).getByRole('button', { name: 'common.delete' }))
    await acceptConfirm()
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getAllByRole('radio')[1])
    fireEvent.click(within(dialog).getByRole('button', { name: 'common.delete' }))

    await waitFor(() => expect(api.deleteUser).toHaveBeenLastCalledWith(2, { strategy: 'purge' }))
    expect(await screen.findByText('purge failed')).toBeInTheDocument()
    expect(screen.getByText('reader')).toBeInTheDocument()
  })

  it('closes the owned-data dialog on cancel without deleting', async () => {
    vi.mocked(api.deleteUser).mockRejectedValueOnce(new ApiError(409, { counts: owned }, 'Conflict'))
    render(<UsersPage />)
    await screen.findByText('reader')
    fireEvent.click(within(rowFor('reader')).getByRole('button', { name: 'common.delete' }))
    await acceptConfirm()
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'common.cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(api.deleteUser).toHaveBeenCalledTimes(1)
  })

  it('resets a password from the prompt and skips the call when the prompt is dismissed', async () => {
    const prompt = vi.spyOn(window, 'prompt').mockReturnValueOnce(null).mockReturnValueOnce('newpassword1')
    render(<UsersPage />)
    await screen.findByText('reader')
    const reset = within(rowFor('reader')).getByRole('button', { name: 'users.resetPassword' })

    fireEvent.click(reset)
    expect(prompt).toHaveBeenCalledTimes(1)
    expect(api.resetUserPassword).not.toHaveBeenCalled()

    fireEvent.click(reset)
    await waitFor(() => expect(api.resetUserPassword).toHaveBeenCalledWith(2, 'newpassword1'))
  })
})
