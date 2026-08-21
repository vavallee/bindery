import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ManagedUser, UserOwnedRows, UserDeletePlan } from '../api/client'

const btnCls = 'px-3 py-1.5 rounded text-sm font-medium transition-colors'
const inputCls = 'w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600'

// The order rows are listed in, and the i18n key for each. Blocklist is left
// out on purpose: those entries are global and survive either choice, so
// showing them next to "keep or delete" would misrepresent what is at stake.
const COUNT_ROWS: Array<{ key: keyof UserOwnedRows; label: string }> = [
  { key: 'authors', label: 'users.countAuthors' },
  { key: 'books', label: 'users.countBooks' },
  { key: 'downloads', label: 'users.countDownloads' },
  { key: 'qualityProfiles', label: 'users.countQualityProfiles' },
  { key: 'metadataProfiles', label: 'users.countMetadataProfiles' },
  { key: 'rootFolders', label: 'users.countRootFolders' },
  { key: 'importLists', label: 'users.countImportLists' },
]

type Props = {
  user: ManagedUser
  counts: UserOwnedRows
  users: ManagedUser[]
  busy: boolean
  onCancel: () => void
  onConfirm: (plan: UserDeletePlan) => void
}

// DeleteUserDialog asks what happens to a departing user's library.
//
// There is no default answer on purpose. Making the rows global publishes one
// person's library to every account on the install, and deleting them destroys
// it; either one applied silently is a decision the admin should be making.
export default function DeleteUserDialog({ user, counts, users, busy, onCancel, onConfirm }: Props) {
  const { t } = useTranslation()
  const [mode, setMode] = useState<'reassign' | 'purge'>('reassign')
  // '' means global (owned by nobody, visible to all), matching the import-list
  // owner picker.
  const [inheritor, setInheritor] = useState<string>('')

  const others = users.filter(u => u.id !== user.id)
  const nonEmpty = COUNT_ROWS.filter(r => counts[r.key] > 0)

  function confirm() {
    if (mode === 'purge') {
      onConfirm({ strategy: 'purge' })
      return
    }
    onConfirm({
      strategy: 'reassign',
      reassignTo: inheritor === '' ? null : Number(inheritor),
    })
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="dialog" aria-modal="true">
      <div className="bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 rounded-lg p-5 w-full max-w-md space-y-4">
        <h2 className="text-base font-semibold">{t('users.deleteTitle', { username: user.username })}</h2>

        <div className="text-sm text-slate-600 dark:text-zinc-400 space-y-2">
          <p>{t('users.deleteOwns')}</p>
          <ul className="space-y-0.5">
            {nonEmpty.map(r => (
              <li key={r.key} className="flex justify-between">
                <span>{t(r.label)}</span>
                <span className="font-medium tabular-nums">{counts[r.key]}</span>
              </li>
            ))}
          </ul>
          {counts.blocklist > 0 && (
            <p className="text-xs">{t('users.deleteBlocklistNote', { count: counts.blocklist })}</p>
          )}
        </div>

        <div className="space-y-3">
          <label className="flex items-start gap-2 text-sm">
            <input
              type="radio"
              className="mt-1"
              checked={mode === 'reassign'}
              onChange={() => setMode('reassign')}
            />
            <span>
              <span className="font-medium">{t('users.deleteKeep')}</span>
              <span className="block text-xs text-slate-500 dark:text-zinc-500">{t('users.deleteKeepHint')}</span>
            </span>
          </label>

          {mode === 'reassign' && (
            <select
              className={inputCls}
              value={inheritor}
              onChange={e => setInheritor(e.target.value)}
              aria-label={t('users.deleteInheritor')}
            >
              <option value="">{t('users.deleteGlobal')}</option>
              {others.map(u => (
                <option key={u.id} value={u.id}>{u.username}</option>
              ))}
            </select>
          )}

          <label className="flex items-start gap-2 text-sm">
            <input
              type="radio"
              className="mt-1"
              checked={mode === 'purge'}
              onChange={() => setMode('purge')}
            />
            <span>
              <span className="font-medium">{t('users.deletePurge')}</span>
              <span className="block text-xs text-slate-500 dark:text-zinc-500">{t('users.deletePurgeHint')}</span>
            </span>
          </label>
        </div>

        <div className="flex justify-end gap-2 pt-1">
          <button
            onClick={onCancel}
            disabled={busy}
            className={`${btnCls} bg-slate-100 dark:bg-zinc-800 hover:bg-slate-200 dark:hover:bg-zinc-700 text-slate-700 dark:text-zinc-300 disabled:opacity-50`}
          >
            {t('common.cancel')}
          </button>
          <button
            onClick={confirm}
            disabled={busy}
            className={`${btnCls} ${
              mode === 'purge'
                ? 'bg-red-600 hover:bg-red-700 text-white'
                : 'bg-slate-800 dark:bg-zinc-100 text-white dark:text-zinc-900 hover:bg-slate-700 dark:hover:bg-zinc-200'
            } disabled:opacity-50`}
          >
            {busy ? t('common.saving') : t('common.delete')}
          </button>
        </div>
      </div>
    </div>
  )
}
