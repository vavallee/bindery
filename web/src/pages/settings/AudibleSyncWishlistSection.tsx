import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, AudibleSyncAccount, AudibleSyncListSyncProgress, ImportList, ManagedUser } from '../../api/client'
import { primaryFill } from '../../components/buttons'
import { errorText } from '../../util/errorText'

const chip = 'text-xs px-2 py-1 rounded bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300'

// The Audible wishlist, read through Audible Sync, as an import list: its
// books are added as wanted audiobooks. The connection is the one in Settings,
// Audible Sync; each list only picks the account and what to do with books.
export default function AudibleSyncWishlistSection({ onNavigate }: { onNavigate?: (tab: string) => void }) {
  const { t } = useTranslation()
  const [lists, setLists] = useState<ImportList[]>([])
  const [configured, setConfigured] = useState<boolean | null>(null)
  const [accounts, setAccounts] = useState<AudibleSyncAccount[]>([])
  const [users, setUsers] = useState<ManagedUser[]>([])
  const [progress, setProgress] = useState<AudibleSyncListSyncProgress | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)

  const loadLists = useCallback(async () => {
    try {
      const all = await api.listImportLists()
      setLists(all.filter(l => l.type === 'audiblesync').sort((a, b) => a.name.localeCompare(b.name)))
    } catch (err) {
      setError(errorText(err, t('settings.import.audibleSync.loadFailed', 'Failed to load Audible Sync wishlist lists')))
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void loadLists()
    api.audibleSyncConfig()
      .then(cfg => {
        const ok = cfg.enabled && Boolean(cfg.baseUrl) && cfg.apiTokenConfigured
        setConfigured(ok)
        if (ok) api.audibleSyncAccounts().then(setAccounts).catch(() => {})
      })
      .catch(() => setConfigured(false))
    api.listUsers().then(setUsers).catch(() => setUsers([]))
    // Adopt a run already in flight, such as a scheduled one.
    api.audibleSyncListSyncStatus().then(p => { if (p.running) setProgress(p) }).catch(() => {})
  }, [loadLists])

  const running = Boolean(progress?.running)
  useEffect(() => {
    if (!running) return
    let cancelled = false
    const timer = setInterval(async () => {
      try {
        const next = await api.audibleSyncListSyncStatus()
        if (cancelled) return
        setProgress(next)
        if (!next.running) void loadLists()
      } catch {
        // Keep polling through a transient failure.
      }
    }, 2000)
    return () => { cancelled = true; clearInterval(timer) }
  }, [running, loadLists])

  const update = async (il: ImportList, patch: Partial<ImportList>) => {
    setError(null)
    try {
      const updated = await api.updateImportList(il.id, patch)
      setLists(prev => prev.map(l => l.id === updated.id ? updated : l))
    } catch (err) {
      setError(errorText(err, t('settings.import.audibleSync.updateFailed', 'Failed to update the list')))
    }
  }

  const add = async () => {
    setAdding(true)
    setError(null)
    try {
      const created = await api.addImportList({
        name: t('settings.import.audibleSync.defaultName', 'Audible wishlist'),
        type: 'audiblesync',
        account: '',
        enabled: true,
        monitorNew: true,
        autoAdd: true,
        skipOwned: true,
        removeOnImport: false,
        ownerUserId: null,
      })
      setLists(prev => [...prev, created])
    } catch (err) {
      setError(errorText(err, t('settings.import.audibleSync.addFailed', 'Failed to add the list')))
    } finally {
      setAdding(false)
    }
  }

  const remove = async (il: ImportList) => {
    setError(null)
    try {
      await api.deleteImportList(il.id)
      setLists(prev => prev.filter(l => l.id !== il.id))
    } catch (err) {
      setError(errorText(err, t('settings.import.audibleSync.deleteFailed', 'Failed to delete the list')))
    }
  }

  const sync = async (il: ImportList) => {
    setError(null)
    try {
      // 202: the sync keeps running server side; the effect above polls it.
      const started = await api.syncImportList(il.id) as unknown as AudibleSyncListSyncProgress
      setProgress(started)
      if (!started.running) void loadLists()
    } catch (err) {
      setError(errorText(err, t('settings.import.audibleSync.syncFailed', 'Sync failed')))
    }
  }

  return (
    <section>
      <h3 className="text-base font-semibold text-slate-800 dark:text-zinc-200 mb-2">{t('settings.import.audibleSync.heading', 'Audible wishlist (Audible Sync)')}</h3>
      <p className="text-xs text-fg-muted mb-3">
        {t('settings.import.audibleSync.description', 'Books on your Audible wishlist are added as wanted audiobooks and searched for. Books your Audible account already owns can be skipped, since Audible Sync downloads those itself.')}
      </p>

      {configured === false && (
        <p className="text-sm text-fg-muted mb-3">
          {t('settings.import.audibleSync.notConfigured', 'Connect Audible Sync first.')}{' '}
          <button
            onClick={() => onNavigate ? onNavigate('audiblesync') : window.location.assign('/settings?tab=audiblesync')}
            className="text-accent-text hover:underline"
          >
            {t('settings.import.audibleSync.configure', 'Open Audible Sync settings →')}
          </button>
        </p>
      )}

      <div className="space-y-2">
        {lists.map(il => (
          <div key={il.id} className="p-3 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
            <div className="flex items-start justify-between gap-3">
              <label className="flex items-start gap-3 min-w-0 cursor-pointer">
                <input
                  type="checkbox"
                  className="mt-1"
                  checked={il.enabled}
                  onChange={() => update(il, { enabled: !il.enabled })}
                  aria-label={t('settings.import.audibleSync.enableList', { name: il.name, defaultValue: 'Sync {{name}}' })}
                />
                <span className="min-w-0">
                  <span className="block text-sm font-medium">{il.name}</span>
                  <span className="block text-xs text-fg-muted mt-0.5">
                    {il.lastSyncAt
                      ? t('settings.import.hardcoverLastSync', { date: new Date(il.lastSyncAt).toLocaleString() })
                      : t('settings.import.hardcoverNeverSynced')}
                  </span>
                </span>
              </label>
              <div className="flex flex-wrap justify-end gap-2">
                <select
                  value={il.account || ''}
                  onChange={e => update(il, { account: e.target.value })}
                  aria-label={t('settings.import.audibleSync.account', 'Audible account')}
                  className={chip}
                >
                  <option value="">{t('settings.import.audibleSync.accountDefault', 'Default account')}</option>
                  {accounts.map(a => (
                    <option key={a.id} value={String(a.id)}>
                      {t('settings.audiblesync.fields.accountOption', { name: a.name, country: a.countryCode.toUpperCase(), defaultValue: '{{name}} ({{country}})' })}
                    </option>
                  ))}
                  {il.account && !accounts.some(a => String(a.id) === il.account) && (
                    <option value={il.account}>{t('settings.audiblesync.fields.accountUnknown', { id: il.account, defaultValue: 'Account {{id}}' })}</option>
                  )}
                </select>
                <label className={`flex items-center gap-1.5 cursor-pointer ${chip}`} title={t('settings.import.downloadBooksHint', 'On: books synced from this list are marked wanted and downloaded. Off: they are added to your library unmonitored, so nothing is downloaded until you monitor one.')}>
                  <input type="checkbox" checked={il.monitorNew} onChange={e => update(il, { monitorNew: e.target.checked })} />
                  {t('settings.import.downloadBooks', 'Download books from this list')}
                </label>
                <label className={`flex items-center gap-1.5 cursor-pointer ${chip}`} title={t('settings.import.audibleSync.skipOwnedHint', 'Leave books the Audible account owns alone: Audible Sync downloads them.')}>
                  <input type="checkbox" checked={il.skipOwned ?? true} onChange={e => update(il, { skipOwned: e.target.checked })} />
                  {t('settings.import.audibleSync.skipOwned', 'Skip owned books')}
                </label>
                <label className={`flex items-center gap-1.5 cursor-pointer ${chip}`} title={t('settings.import.audibleSync.removeOnImportHint', 'Once Bindery has imported a wishlist book, take it off the Audible wishlist. This changes your Audible account.')}>
                  <input type="checkbox" checked={il.removeOnImport ?? false} onChange={e => update(il, { removeOnImport: e.target.checked })} />
                  {t('settings.import.audibleSync.removeOnImport', 'Remove from wishlist once imported')}
                </label>
                <select
                  value={il.ownerUserId ?? ''}
                  onChange={e => update(il, { ownerUserId: e.target.value === '' ? null : Number(e.target.value) })}
                  aria-label={t('settings.import.ownerLabel', 'Owner')}
                  title={t('settings.import.ownerHint', 'Who sees books synced from this list. Global shows them to all users.')}
                  className={chip}
                >
                  <option value="">{t('settings.import.ownerGlobal', 'Global (all users)')}</option>
                  {users.map(u => <option key={u.id} value={u.id}>{u.username}</option>)}
                </select>
                <button
                  onClick={() => sync(il)}
                  disabled={running || !il.enabled || !configured}
                  className={`${chip} hover:bg-slate-300 dark:hover:bg-zinc-700 disabled:opacity-50`}
                >
                  {running && progress?.listId === il.id ? t('settings.import.hardcoverSyncing', 'Syncing…') : t('settings.import.hardcoverSyncNow', 'Sync now')}
                </button>
                <button onClick={() => remove(il)} className="text-xs text-red-700 dark:text-red-400 hover:underline">{t('common.delete')}</button>
              </div>
            </div>
            {progress?.listId === il.id && (
              <p className={`mt-2 text-xs ${progress.error ? 'text-red-700 dark:text-red-400' : 'text-accent-text'}`}>
                {progress.running
                  ? `${progress.message ?? t('settings.import.hardcoverSyncing', 'Syncing…')}${progress.stats.total > 0 ? ` (${progress.stats.processed}/${progress.stats.total})` : ''}`
                  : progress.error
                    ? `${t('settings.import.audibleSync.syncFailed', 'Sync failed')}: ${progress.error}`
                    : t('settings.import.audibleSync.syncDone', {
                      defaultValue: 'Sync finished: {{imported}} added, {{skipped}} already tracked, {{owned}} owned on Audible, {{unresolved}} with an unknown author, {{removed}} removed from the wishlist, {{failed}} failed',
                      imported: progress.stats.imported,
                      skipped: progress.stats.skipped,
                      owned: progress.stats.owned,
                      unresolved: progress.stats.unresolved,
                      removed: progress.stats.removed,
                      failed: progress.stats.failed,
                    })}
              </p>
            )}
          </div>
        ))}
      </div>

      <button
        onClick={add}
        disabled={adding || !configured}
        className={`mt-2 px-3 py-2 ${primaryFill} disabled:opacity-50 rounded text-xs font-medium`}
      >
        {t('settings.import.audibleSync.add', 'Add Audible wishlist')}
      </button>

      {error && <div className="mt-2 text-sm text-rose-700 dark:text-rose-400">{error}</div>}
    </section>
  )
}
