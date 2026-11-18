import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, AudibleSyncList, AudibleSyncListSyncProgress, ImportList, ManagedUser } from '../../api/client'
import { errorText } from '../../util/errorText'

const chip = 'text-xs px-2 py-1 rounded bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300'

type Source = 'wishlist' | 'unavailable'

// A saved Audible Sync list reads its source from url; empty is the wishlist.
const sourceOf = (il: ImportList): Source => (il.url === 'unavailable' ? 'unavailable' : 'wishlist')
const keyOf = (account: string | number, source: Source) => `${account}:${source}`

interface Row {
  key: string
  accountId: string
  accountName: string
  countryCode: string
  needsLogin: boolean
  source: Source
  booksCount: number
  remote?: AudibleSyncList
  local?: ImportList
}

// The lists Audible Sync offers, picked like Hardcover lists: every Audible
// account has its wishlist and its unavailable books (library titles Audible
// no longer lets it download, typically withdrawn from Plus). Ticking one
// saves it as an import list with its own options; unticking pauses it.
export default function AudibleSyncListsSection({ onNavigate }: { onNavigate?: (tab: string) => void }) {
  const { t } = useTranslation()
  const [lists, setLists] = useState<ImportList[]>([])
  const [remote, setRemote] = useState<AudibleSyncList[]>([])
  const [configured, setConfigured] = useState<boolean | null>(null)
  const [loading, setLoading] = useState(true)
  const [users, setUsers] = useState<ManagedUser[]>([])
  const [progress, setProgress] = useState<AudibleSyncListSyncProgress | null>(null)
  const [busyKey, setBusyKey] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const loadLocal = useCallback(async () => {
    try {
      const all = await api.listImportLists()
      setLists(all.filter(l => l.type === 'audiblesync'))
    } catch (err) {
      setError(errorText(err, t('settings.import.audibleSync.loadFailed', 'Failed to load saved Audible Sync lists')))
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    await loadLocal()
    try {
      const cfg = await api.audibleSyncConfig()
      const ok = cfg.enabled && Boolean(cfg.baseUrl) && cfg.apiTokenConfigured
      setConfigured(ok)
      if (ok) setRemote(await api.audibleSyncLists())
    } catch (err) {
      setRemote([])
      setError(errorText(err, t('settings.import.audibleSync.listsFailed', 'Failed to load the lists from Audible Sync')))
    } finally {
      setLoading(false)
    }
  }, [loadLocal]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void load()
    api.listUsers().then(setUsers).catch(() => setUsers([]))
    // Adopt a run already in flight, such as a scheduled one.
    api.audibleSyncListSyncStatus().then(p => { if (p.running) setProgress(p) }).catch(() => {})
  }, [load])

  const running = Boolean(progress?.running)
  useEffect(() => {
    if (!running) return
    let cancelled = false
    const timer = setInterval(async () => {
      try {
        const next = await api.audibleSyncListSyncStatus()
        if (cancelled) return
        setProgress(next)
        if (!next.running) void loadLocal()
      } catch {
        // Keep polling through a transient failure.
      }
    }, 2000)
    return () => { cancelled = true; clearInterval(timer) }
  }, [running, loadLocal])

  const replace = (updated: ImportList) => setLists(prev => prev.map(l => l.id === updated.id ? updated : l))

  const update = async (il: ImportList, patch: Partial<ImportList>) => {
    setError(null)
    try {
      replace(await api.updateImportList(il.id, patch))
    } catch (err) {
      setError(errorText(err, t('settings.import.audibleSync.updateFailed', 'Failed to update the list')))
    }
  }

  const listName = (accountName: string, source: Source) => source === 'wishlist'
    ? t('settings.import.audibleSync.wishlistName', { account: accountName, defaultValue: '{{account}}: wishlist' })
    : t('settings.import.audibleSync.unavailableName', { account: accountName, defaultValue: '{{account}}: unavailable books' })

  // Ticking a list the server offers saves it the first time and pauses or
  // resumes it after that, the same as a Hardcover list.
  const toggle = async (row: Row) => {
    setBusyKey(row.key)
    setError(null)
    try {
      if (row.local) {
        replace(await api.updateImportList(row.local.id, { enabled: !row.local.enabled }))
        return
      }
      const created = await api.addImportList({
        name: listName(row.accountName, row.source),
        type: 'audiblesync',
        url: row.source,
        account: row.accountId,
        enabled: true,
        monitorNew: true,
        autoAdd: true,
        skipOwned: row.source === 'wishlist',
        removeOnImport: false,
        ownerUserId: null,
      })
      setLists(prev => [...prev, created])
    } catch (err) {
      setError(errorText(err, t('settings.import.audibleSync.addFailed', 'Failed to add the list')))
    } finally {
      setBusyKey(null)
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
      if (!started.running) void loadLocal()
    } catch (err) {
      setError(errorText(err, t('settings.import.audibleSync.syncFailed', 'Sync failed')))
    }
  }

  // One row per list the server offers, joined to its saved list by
  // (account, source); saved lists the server no longer offers follow, so
  // they can still be paused or deleted.
  const byKey = new Map(lists.map(il => [keyOf(il.account, sourceOf(il)), il]))
  const rows: Row[] = remote.map(r => {
    const key = keyOf(r.accountId, r.source)
    return {
      key, accountId: String(r.accountId), accountName: r.accountName, countryCode: r.countryCode,
      needsLogin: r.needsLogin, source: r.source, booksCount: r.booksCount, remote: r, local: byKey.get(key),
    }
  })
  const offered = new Set(rows.map(r => r.key))
  for (const il of lists) {
    const key = keyOf(il.account, sourceOf(il))
    if (!offered.has(key)) {
      rows.push({ key, accountId: il.account, accountName: il.name, countryCode: '', needsLogin: false, source: sourceOf(il), booksCount: -1, local: il })
    }
  }

  return (
    <section>
      <div className="flex justify-between items-center mb-2">
        <h3 className="text-base font-semibold text-slate-800 dark:text-zinc-200">{t('settings.import.audibleSync.heading', 'Audible (Audible Sync)')}</h3>
        {configured && (
          <button onClick={() => load()} disabled={loading} className="px-3 py-1.5 bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-xs font-medium">
            {loading ? t('settings.import.hardcoverListLoading') : t('common.refresh', 'Refresh')}
          </button>
        )}
      </div>
      <p className="text-xs text-fg-muted mb-3">
        {t('settings.import.audibleSync.description', 'Pick the Audible lists to watch. Their books are added as wanted audiobooks and searched for. Unavailable books are titles in your library Audible no longer lets you download, usually ones withdrawn from Plus; if Audible brings one back, Audible Sync downloads it and Bindery stops searching for it.')}
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
      {configured && !loading && rows.length === 0 && (
        <p className="text-sm text-fg-muted">{t('settings.import.audibleSync.empty', 'Audible Sync has no Audible accounts yet.')}</p>
      )}

      <div className="space-y-2">
        {rows.map(row => {
          const il = row.local
          const name = il?.name ?? listName(row.accountName, row.source)
          return (
            <div key={row.key} className="p-3 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
              <label className="flex items-start gap-3 min-w-0 cursor-pointer">
                <input
                  type="checkbox"
                  className="mt-1"
                  checked={Boolean(il?.enabled)}
                  disabled={busyKey === row.key || (!row.remote && !il)}
                  onChange={() => toggle(row)}
                  aria-label={t('settings.import.audibleSync.watch', { name, defaultValue: 'Watch {{name}}' })}
                />
                <span className="min-w-0">
                  <span className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium">{name}</span>
                    {row.countryCode && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-fg-muted rounded">{row.countryCode.toUpperCase()}</span>}
                    {row.needsLogin && <span className="text-[10px] px-1.5 py-0.5 bg-amber-100 dark:bg-amber-950 text-amber-700 dark:text-amber-400 rounded">{t('settings.import.audibleSync.needsLogin', 'Needs signing in again in Audible Sync')}</span>}
                    {!row.remote && il && <span className="text-[10px] px-1.5 py-0.5 bg-amber-100 dark:bg-amber-950 text-amber-700 dark:text-amber-400 rounded">{t('settings.import.hardcoverSavedOnly', 'Saved only')}</span>}
                  </span>
                  <span className="block text-xs text-fg-muted mt-0.5">
                    {il?.lastSyncAt
                      ? t('settings.import.hardcoverLastSync', { date: new Date(il.lastSyncAt).toLocaleString() })
                      : il
                        ? t('settings.import.hardcoverNeverSynced')
                        : t('settings.import.hardcoverNotSelected', 'Not selected')}
                    {row.booksCount >= 0 && t('settings.import.hardcoverListBooks', { count: row.booksCount })}
                  </span>
                </span>
              </label>
              {/* Options sit on their own line under the name, indented to
                  line up with it, so a saved list's name never wraps. */}
              {il && (
                <div className="mt-2 ml-7 flex flex-wrap items-center gap-2">
                  <label className={`flex items-center gap-1.5 cursor-pointer ${chip}`} title={t('settings.import.downloadBooksHint', 'On: books synced from this list are marked wanted and downloaded. Off: they are added to your library unmonitored, so nothing is downloaded until you monitor one.')}>
                    <input type="checkbox" checked={il.monitorNew} onChange={e => update(il, { monitorNew: e.target.checked })} />
                    {t('settings.import.downloadBooks', 'Download books from this list')}
                  </label>
                  {row.source === 'wishlist' && (
                    <>
                      <label className={`flex items-center gap-1.5 cursor-pointer ${chip}`} title={t('settings.import.audibleSync.skipOwnedHint', 'Leave books the Audible account owns alone: Audible Sync downloads them.')}>
                        <input type="checkbox" checked={il.skipOwned ?? true} onChange={e => update(il, { skipOwned: e.target.checked })} />
                        {t('settings.import.audibleSync.skipOwned', 'Skip owned books')}
                      </label>
                      <label className={`flex items-center gap-1.5 cursor-pointer ${chip}`} title={t('settings.import.audibleSync.removeOnImportHint', 'Once Bindery has imported a wishlist book, take it off the Audible wishlist. This changes your Audible account.')}>
                        <input type="checkbox" checked={il.removeOnImport ?? false} onChange={e => update(il, { removeOnImport: e.target.checked })} />
                        {t('settings.import.audibleSync.removeOnImport', 'Remove from wishlist once imported')}
                      </label>
                    </>
                  )}
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
              )}
              {il && progress?.listId === il.id && (
                <p className={`mt-2 ml-7 text-xs ${progress.error ? 'text-red-700 dark:text-red-400' : 'text-accent-text'}`}>
                  {progress.running
                    ? `${progress.message ?? t('settings.import.hardcoverSyncing', 'Syncing…')}${progress.stats.total > 0 ? ` (${progress.stats.processed}/${progress.stats.total})` : ''}`
                    : progress.error
                      ? `${t('settings.import.audibleSync.syncFailed', 'Sync failed')}: ${progress.error}`
                      : t('settings.import.audibleSync.syncDone', {
                        defaultValue: 'Sync finished: {{imported}} added, {{skipped}} already tracked, {{owned}} owned on Audible, {{unresolved}} with an unknown author, {{removed}} removed from the wishlist, {{released}} downloadable again, {{failed}} failed',
                        imported: progress.stats.imported,
                        skipped: progress.stats.skipped,
                        owned: progress.stats.owned,
                        unresolved: progress.stats.unresolved,
                        removed: progress.stats.removed,
                        released: progress.stats.released,
                        failed: progress.stats.failed,
                      })}
                </p>
              )}
            </div>
          )
        })}
      </div>

      {error && <div className="mt-2 text-sm text-rose-700 dark:text-rose-400">{error}</div>}
    </section>
  )
}
