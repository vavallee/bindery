import React, { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useConfirmDialog } from '../components/useConfirmDialog'
import { api, ManagedUser, UserOwnedRows, UserDeletePlan } from '../api/client'
import { ApiError } from '../api/core'
import DeleteUserDialog from './DeleteUserDialog'
import { useAuth } from '../auth/AuthContext'
import type { UserRole } from '../auth/AuthContext'
import { literalInputAttrs } from '../util/inputAttrs'
import { BELOW_SM, useMediaQuery } from '../components/useMediaQuery'

const inputCls = 'w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600'
const btnCls = 'px-3 py-1.5 rounded text-sm font-medium transition-colors'

export default function UsersPage() {
  const { t } = useTranslation()
  const { confirm, confirmDialog } = useConfirmDialog()
  const { isAdmin, status } = useAuth()
  const narrow = useMediaQuery(BELOW_SM)
  const [users, setUsers] = useState<ManagedUser[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [newRole, setNewRole] = useState<UserRole>('user')
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState('')
  const [, setResetError] = useState<Record<number, string>>({})
  // Set when a delete came back 409 because the user owns library data. Holds
  // the user and the counts the backend reported, which drives the dialog.
  const [pendingDelete, setPendingDelete] = useState<{ user: ManagedUser; counts: UserOwnedRows } | null>(null)
  const [deleteBusy, setDeleteBusy] = useState(false)

  useEffect(() => {
    document.title = 'Users · Bindery'
    return () => { document.title = 'Bindery' }
  }, [])

  useEffect(() => {
    if (!isAdmin) return
    api.listUsers()
      .then(setUsers)
      .catch(e => setError(e instanceof Error ? e.message : t('users.loadFail')))
      .finally(() => setLoading(false))
  }, [isAdmin])

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault()
    setCreateError('')
    setCreating(true)
    try {
      const u = await api.createUser(newUsername, newPassword, newRole)
      setUsers(prev => [...prev, u])
      setNewUsername('')
      setNewPassword('')
      setNewRole('user')
    } catch (e: unknown) {
      setCreateError(e instanceof Error ? e.message : t('users.createFail'))
    } finally {
      setCreating(false)
    }
  }

  // First pass: ask the backend to delete. A user who owns nothing goes
  // straight away; one who owns library data comes back 409 with the counts,
  // and that opens the dialog rather than failing (#1899).
  async function handleDelete(u: ManagedUser) {
    if (!await confirm({
      title: t('common.confirmTitle'),
      body: t('users.deleteConfirm', { username: u.username }),
      confirmLabel: t('common.delete'),
    })) return
    setError('')
    try {
      await api.deleteUser(u.id)
      setUsers(prev => prev.filter(x => x.id !== u.id))
    } catch (e: unknown) {
      if (e instanceof ApiError && e.status === 409 && e.body?.counts) {
        setPendingDelete({ user: u, counts: e.body.counts as UserOwnedRows })
        return
      }
      setError(e instanceof Error ? e.message : t('users.deleteFail'))
    }
  }

  // Second pass: the admin has chosen what happens to the library.
  async function executeDelete(u: ManagedUser, plan: UserDeletePlan) {
    setDeleteBusy(true)
    setError('')
    try {
      await api.deleteUser(u.id, plan)
      setUsers(prev => prev.filter(x => x.id !== u.id))
      setPendingDelete(null)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : t('users.deleteFail'))
    } finally {
      setDeleteBusy(false)
    }
  }

  async function handleRoleChange(u: ManagedUser, next: UserRole) {
    if (next === u.role) return
    try {
      await api.setUserRole(u.id, next)
      setUsers(prev => prev.map(x => x.id === u.id ? { ...x, role: next } : x))
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : t('users.roleFail'))
    }
  }

  // Per-account request auto approval (#2718). Turning it on only changes what
  // happens to the account's next request; anything already queued stays.
  async function handleAutoApproveChange(u: ManagedUser, next: boolean) {
    try {
      await api.setUserAutoApprove(u.id, next)
      setUsers(prev => prev.map(x => x.id === u.id ? { ...x, autoApproveRequests: next } : x))
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : t('users.autoApproveFail'))
    }
  }

  async function handleReset(id: number) {
    const pw = prompt('New password (min 8 characters):')
    if (!pw) return
    try {
      await api.resetUserPassword(id, pw)
      setResetError(prev => ({ ...prev, [id]: '' }))
    } catch (e: unknown) {
      setResetError(prev => ({ ...prev, [id]: e instanceof Error ? e.message : 'Failed' }))
    }
  }

  // Shared by the table and the phone cards, so the two cannot drift apart.
  // Three roles, so a select rather than a promote/demote toggle. The server
  // refuses to demote the last admin.
  const roleSelect = (u: ManagedUser) => (
    <select
      value={u.role}
      onChange={e => void handleRoleChange(u, e.target.value as UserRole)}
      aria-label={t('users.roleFor', { username: u.username })}
      className={`px-2 py-0.5 rounded text-xs font-medium border border-slate-300 dark:border-zinc-700 ${
        u.role === 'admin'
          ? 'bg-amber-100 dark:bg-amber-900/30 text-amber-800 dark:text-amber-400'
          : 'bg-slate-100 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400'
      }`}
    >
      <option value="admin">{t('users.roleAdmin')}</option>
      <option value="user">{t('users.roleUser')}</option>
      <option value="requester">{t('users.roleRequester')}</option>
    </select>
  )

  // Only a requester queues requests, so the toggle is shown for that role.
  // The value stays on the account if the role changes and comes back with it.
  const autoApproveToggle = (u: ManagedUser) => u.role === 'requester' && (
    <label className="flex items-center gap-2 text-xs text-slate-600 dark:text-zinc-400">
      <input
        type="checkbox"
        checked={u.autoApproveRequests}
        onChange={e => void handleAutoApproveChange(u, e.target.checked)}
        aria-label={t('users.autoApproveFor', { username: u.username })}
        className="accent-emerald-500"
      />
      {t('users.autoApprove')}
    </label>
  )

  const actionButtons = (u: ManagedUser) => (
    <>
      <button
        onClick={() => handleReset(u.id)}
        className={`${btnCls} text-xs bg-slate-100 dark:bg-zinc-800 hover:bg-slate-200 dark:hover:bg-zinc-700 text-slate-700 dark:text-zinc-300 pointer-coarse:py-2.5`}
      >
        {t('users.resetPassword')}
      </button>
      <button
        onClick={() => handleDelete(u)}
        className={`${btnCls} text-xs bg-red-50 dark:bg-red-900/20 hover:bg-red-100 dark:hover:bg-red-900/40 text-red-600 dark:text-red-400 pointer-coarse:py-2.5`}
      >
        {t('common.delete')}
      </button>
    </>
  )

  if (!isAdmin) {
    return (
      <div className="text-center py-20 text-slate-500 dark:text-zinc-500">
        Admin access required.
      </div>
    )
  }

  return (
    <div className="space-y-8 max-w-2xl">
      {confirmDialog}
      <h1 className="text-2xl font-bold">{t('users.title')}</h1>

      {pendingDelete && (
        <DeleteUserDialog
          user={pendingDelete.user}
          counts={pendingDelete.counts}
          users={users}
          busy={deleteBusy}
          onCancel={() => setPendingDelete(null)}
          onConfirm={plan => executeDelete(pendingDelete.user, plan)}
        />
      )}

      {loading && <p className="text-sm text-slate-500 dark:text-zinc-500">{t('common.loading')}</p>}
      {error && <p className="text-sm text-red-500">{error}</p>}
      {!loading && <p className="text-xs text-slate-500 dark:text-zinc-500">{t('users.autoApproveHint')}</p>}

      {!loading && narrow && (
        // Below sm each user is a card, as on History. The five column table
        // scrolled sideways there with nothing to say so, which left Reset
        // password and Delete out of sight past the right edge.
        <ul className="space-y-2" data-testid="users-cards">
          {users.map(u => (
            <li key={u.id} className="p-3 bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 rounded-lg space-y-3">
              <div className="flex items-baseline justify-between gap-3">
                <span className="font-medium min-w-0 [overflow-wrap:anywhere]">
                  {u.username}
                  {u.username === status?.username && (
                    <span className="ml-2 text-xs text-slate-500 dark:text-zinc-500">({t('users.you')})</span>
                  )}
                </span>
                <span className="shrink-0 text-xs text-slate-500 dark:text-zinc-500">
                  {new Date(u.createdAt).toLocaleDateString()}
                </span>
              </div>
              <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                {roleSelect(u)}
                {autoApproveToggle(u)}
              </div>
              <div className="flex flex-wrap gap-2 justify-end">
                {actionButtons(u)}
              </div>
            </li>
          ))}
        </ul>
      )}

      {!loading && !narrow && (
        <div className="bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 rounded-lg overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950">
                <th className="px-4 py-3 text-left font-medium text-slate-600 dark:text-zinc-400">{t('users.colUsername')}</th>
                <th className="px-4 py-3 text-left font-medium text-slate-600 dark:text-zinc-400">{t('users.colRole')}</th>
                <th className="px-4 py-3 text-left font-medium text-slate-600 dark:text-zinc-400">{t('users.colAutoApprove')}</th>
                <th className="px-4 py-3 text-left font-medium text-slate-600 dark:text-zinc-400">{t('users.colCreated')}</th>
                <th className="px-4 py-3" />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-200 dark:divide-zinc-800">
              {users.map(u => (
                <tr key={u.id}>
                  <td className="px-4 py-3 font-medium">
                    {u.username}
                    {u.username === status?.username && (
                      <span className="ml-2 text-xs text-slate-500 dark:text-zinc-500">({t('users.you')})</span>
                    )}
                  </td>
                  <td className="px-4 py-3">
                    {roleSelect(u)}
                  </td>
                  <td className="px-4 py-3">
                    {autoApproveToggle(u)}
                  </td>
                  <td className="px-4 py-3 text-slate-500 dark:text-zinc-500">
                    {new Date(u.createdAt).toLocaleDateString()}
                  </td>
                  <td className="px-4 py-3">
                    <div className="flex gap-2 justify-end whitespace-nowrap">
                      {actionButtons(u)}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 rounded-lg p-5">
        <h2 className="text-base font-semibold mb-4">{t('users.addHeading')}</h2>
        <form onSubmit={handleCreate} className="space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs font-medium text-slate-600 dark:text-zinc-400 mb-1">{t('users.fieldUsername')}</label>
              <input
                {...literalInputAttrs}
                className={inputCls}
                value={newUsername}
                onChange={e => setNewUsername(e.target.value)}
                required
                autoComplete="off"
              />
            </div>
            <div>
              <label className="block text-xs font-medium text-slate-600 dark:text-zinc-400 mb-1">{t('users.fieldPassword')}</label>
              <input
                type="password"
                className={inputCls}
                value={newPassword}
                onChange={e => setNewPassword(e.target.value)}
                required
                autoComplete="new-password"
              />
            </div>
          </div>
          <div>
            <label className="block text-xs font-medium text-slate-600 dark:text-zinc-400 mb-1">{t('users.fieldRole')}</label>
            <select
              className={inputCls}
              value={newRole}
              onChange={e => setNewRole(e.target.value as UserRole)}
            >
              <option value="user">{t('users.roleUser')}</option>
              <option value="admin">{t('users.roleAdmin')}</option>
              <option value="requester">{t('users.roleRequester')}</option>
            </select>
            <p className="mt-1 text-xs text-slate-500 dark:text-zinc-500">{t('users.roleRequesterHint')}</p>
          </div>
          {createError && <p className="text-sm text-red-500">{createError}</p>}
          <button
            type="submit"
            disabled={creating || !newUsername || !newPassword}
            className={`${btnCls} bg-slate-800 dark:bg-zinc-100 text-white dark:text-zinc-900 hover:bg-slate-700 dark:hover:bg-zinc-200 disabled:opacity-50`}
          >
            {creating ? t('common.saving') : t('users.createButton')}
          </button>
        </form>
      </div>
    </div>
  )
}
