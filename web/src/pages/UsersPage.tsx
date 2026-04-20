import React, { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, ManagedUser } from '../api/client'
import { useAuth } from '../auth/AuthContext'

const inputCls = 'w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600'
const btnCls = 'px-3 py-1.5 rounded text-sm font-medium transition-colors'

export default function UsersPage() {
  const { t } = useTranslation()
  const { isAdmin, status } = useAuth()
  const [users, setUsers] = useState<ManagedUser[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [newRole, setNewRole] = useState<'user' | 'admin'>('user')
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState('')
  const [, setResetError] = useState<Record<number, string>>({})

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

  async function handleDelete(u: ManagedUser) {
    if (!confirm(t('users.deleteConfirm', { username: u.username }))) return
    try {
      await api.deleteUser(u.id)
      setUsers(prev => prev.filter(x => x.id !== u.id))
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : t('users.deleteFail'))
    }
  }

  async function handleRoleToggle(u: ManagedUser) {
    const next = u.role === 'admin' ? 'user' : 'admin'
    try {
      await api.setUserRole(u.id, next)
      setUsers(prev => prev.map(x => x.id === u.id ? { ...x, role: next } : x))
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : t('users.roleFail'))
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

  if (!isAdmin) {
    return (
      <div className="text-center py-20 text-slate-500 dark:text-zinc-500">
        Admin access required.
      </div>
    )
  }

  return (
    <div className="space-y-8 max-w-2xl">
      <h1 className="text-2xl font-bold">{t('users.title')}</h1>

      {loading && <p className="text-sm text-slate-500 dark:text-zinc-500">{t('common.loading')}</p>}
      {error && <p className="text-sm text-red-500">{error}</p>}

      {!loading && (
        <div className="bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950">
                <th className="px-4 py-3 text-left font-medium text-slate-600 dark:text-zinc-400">{t('users.colUsername')}</th>
                <th className="px-4 py-3 text-left font-medium text-slate-600 dark:text-zinc-400">{t('users.colRole')}</th>
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
                    <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${
                      u.role === 'admin'
                        ? 'bg-amber-100 dark:bg-amber-900/30 text-amber-800 dark:text-amber-400'
                        : 'bg-slate-100 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400'
                    }`}>
                      {u.role}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-slate-500 dark:text-zinc-500">
                    {new Date(u.createdAt).toLocaleDateString()}
                  </td>
                  <td className="px-4 py-3 flex gap-2 justify-end">
                    <button
                      onClick={() => handleRoleToggle(u)}
                      className={`${btnCls} text-xs bg-slate-100 dark:bg-zinc-800 hover:bg-slate-200 dark:hover:bg-zinc-700 text-slate-700 dark:text-zinc-300`}
                    >
                      {u.role === 'admin' ? t('users.demote') : t('users.promote')}
                    </button>
                    <button
                      onClick={() => handleReset(u.id)}
                      className={`${btnCls} text-xs bg-slate-100 dark:bg-zinc-800 hover:bg-slate-200 dark:hover:bg-zinc-700 text-slate-700 dark:text-zinc-300`}
                    >
                      {t('users.resetPassword')}
                    </button>
                    <button
                      onClick={() => handleDelete(u)}
                      className={`${btnCls} text-xs bg-red-50 dark:bg-red-900/20 hover:bg-red-100 dark:hover:bg-red-900/40 text-red-600 dark:text-red-400`}
                    >
                      {t('common.delete')}
                    </button>
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
              onChange={e => setNewRole(e.target.value as 'user' | 'admin')}
            >
              <option value="user">{t('users.roleUser')}</option>
              <option value="admin">{t('users.roleAdmin')}</option>
            </select>
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
