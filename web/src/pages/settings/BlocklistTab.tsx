import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, BlocklistEntry } from '../../api/client'
import Pagination from '../../components/Pagination'
import { usePagination } from '../../components/usePagination'
import { dangerLink } from '../../components/buttons'

function formatBlocklistDate(s: string) {
  return new Date(s).toLocaleString(undefined, {
    year: 'numeric', month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
  })
}

export default function BlocklistTab() {
  const { t } = useTranslation()
  const [entries, setEntries] = useState<BlocklistEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [deleting, setDeleting] = useState(false)

  const load = () => {
    setLoading(true)
    api.listBlocklist().then(setEntries).catch(console.error).finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const handleDelete = async (id: number) => {
    await api.deleteBlocklistEntry(id).catch(console.error)
    setEntries(prev => prev.filter(e => e.id !== id))
    setSelected(prev => { const s = new Set(prev); s.delete(id); return s })
  }

  const handleBulkDelete = async () => {
    if (selected.size === 0) return
    if (!confirm(t('blocklist.deleteConfirm', { count: selected.size }))) return
    setDeleting(true)
    try {
      await api.bulkDeleteBlocklist(Array.from(selected))
      setEntries(prev => prev.filter(e => !selected.has(e.id)))
      setSelected(new Set())
    } catch (err) {
      console.error(err)
    } finally {
      setDeleting(false)
    }
  }

  const toggleSelect = (id: number) => {
    setSelected(prev => {
      const s = new Set(prev)
      if (s.has(id)) s.delete(id)
      else s.add(id)
      return s
    })
  }

  const toggleAll = () => {
    if (selected.size === entries.length) {
      setSelected(new Set())
    } else {
      setSelected(new Set(entries.map(e => e.id)))
    }
  }

  const allSelected = entries.length > 0 && selected.size === entries.length

  const { pageItems, paginationProps } = usePagination(entries, 50, 'blocklist')

  return (
    <div>
      <div className="flex flex-wrap items-center justify-between gap-3 mb-6">
        <h3 className="text-lg font-semibold">{t('blocklist.title')}</h3>
        <div className="flex items-center gap-3">
          {selected.size > 0 && (
            <button
              onClick={handleBulkDelete}
              disabled={deleting}
              className="px-3 py-1.5 bg-red-600 hover:bg-red-500 rounded text-xs font-medium transition-colors disabled:opacity-50"
            >
              {deleting ? t('blocklist.deleting') : t('blocklist.deleteSelected', { count: selected.size })}
            </button>
          )}
          <span className="text-sm text-slate-600 dark:text-zinc-500">{t('blocklist.entries', { count: entries.length })}</span>
        </div>
      </div>

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
      ) : entries.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p className="text-lg mb-2">{t('blocklist.empty')}</p>
          <p className="text-sm">{t('blocklist.emptyHint')}</p>
        </div>
      ) : (
        <>
          {/* Desktop table */}
          <div className="hidden sm:block border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
                    <th className="px-4 py-3 w-10">
                      <input
                        type="checkbox"
                        checked={allSelected}
                        onChange={toggleAll}
                        className="accent-emerald-500"
                      />
                    </th>
                    <th className="text-left px-4 py-3 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase tracking-wider">{t('blocklist.colTitle')}</th>
                    <th className="text-left px-4 py-3 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase tracking-wider">{t('blocklist.colReason')}</th>
                    <th className="text-left px-4 py-3 text-xs font-medium text-slate-600 dark:text-zinc-400 uppercase tracking-wider">{t('blocklist.colDate')}</th>
                    <th className="px-4 py-3" />
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-200 dark:divide-zinc-800">
                  {pageItems.map(entry => (
                    <tr key={entry.id} className={`transition-colors hover:bg-slate-200/50 dark:hover:bg-zinc-800/50 ${selected.has(entry.id) ? 'bg-slate-200/30 dark:bg-zinc-800/30' : 'bg-slate-100/50 dark:bg-zinc-900/50'}`}>
                      <td className="px-4 py-3">
                        <input
                          type="checkbox"
                          checked={selected.has(entry.id)}
                          onChange={() => toggleSelect(entry.id)}
                          className="accent-emerald-500"
                        />
                      </td>
                      <td className="px-4 py-3 max-w-xs">
                        <p className="text-slate-800 dark:text-zinc-200 truncate" title={entry.title}>{entry.title}</p>
                        {entry.guid && (
                          <p className="text-[10px] text-slate-500 dark:text-zinc-600 mt-0.5 font-mono truncate">{entry.guid}</p>
                        )}
                      </td>
                      <td className="px-4 py-3">
                        <span className="text-xs px-2 py-0.5 rounded bg-red-500/20 text-red-400">
                          {entry.reason || 'Unknown'}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-slate-600 dark:text-zinc-400 whitespace-nowrap text-xs">
                        {formatBlocklistDate(entry.createdAt)}
                      </td>
                      <td className="px-4 py-3 text-right">
                        <button
                          onClick={() => handleDelete(entry.id)}
                          className={`text-xs ${dangerLink}`}
                        >
                          {t('common.delete')}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>

          {/* Mobile card list */}
          <div className="sm:hidden space-y-2">
            <div className="flex items-center gap-2 mb-2">
              <input
                type="checkbox"
                checked={allSelected}
                onChange={toggleAll}
                className="accent-emerald-500"
              />
              <span className="text-xs text-slate-600 dark:text-zinc-500">{t('blocklist.selectAll')}</span>
            </div>
            {pageItems.map(entry => (
              <div
                key={entry.id}
                className={`border border-slate-200 dark:border-zinc-800 rounded-lg p-3 transition-colors ${selected.has(entry.id) ? 'bg-slate-200/30 dark:bg-zinc-800/30' : 'bg-slate-100/50 dark:bg-zinc-900/50'}`}
              >
                <div className="flex items-start gap-3">
                  <input
                    type="checkbox"
                    checked={selected.has(entry.id)}
                    onChange={() => toggleSelect(entry.id)}
                    className="accent-emerald-500 mt-0.5 flex-shrink-0"
                  />
                  <div className="min-w-0 flex-1">
                    <p className="text-sm text-slate-800 dark:text-zinc-200 break-words">{entry.title}</p>
                    {entry.guid && (
                      <p className="text-[10px] text-slate-500 dark:text-zinc-600 mt-0.5 font-mono truncate">{entry.guid}</p>
                    )}
                    <div className="flex flex-wrap items-center gap-2 mt-2">
                      <span className="text-xs px-2 py-0.5 rounded bg-red-500/20 text-red-400">
                        {entry.reason || 'Unknown'}
                      </span>
                      <span className="text-[10px] text-slate-600 dark:text-zinc-500">{formatBlocklistDate(entry.createdAt)}</span>
                    </div>
                  </div>
                  <button
                    onClick={() => handleDelete(entry.id)}
                    className={`text-xs flex-shrink-0 py-1 px-2 ${dangerLink}`}
                  >
                    {t('common.delete')}
                  </button>
                </div>
              </div>
            ))}
          </div>
        </>
      )}
      <Pagination {...paginationProps} />
    </div>
  )
}
