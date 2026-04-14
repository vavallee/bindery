import { useEffect, useMemo, useState } from 'react'
import { api, Author } from '../api/client'

interface Props {
  authors: Author[]
  initialTargetId?: number
  onClose: () => void
  onMerged: () => void
}

// MergeAuthorsModal collapses two author rows into one canonical row. The
// user picks a source (the duplicate that goes away) and a target (the
// canonical row that keeps its OL id and receives source's books).
//
// We block submit until both are chosen and different, and we show the
// target's book count so the user sees the "post-merge" total before
// committing.
export default function MergeAuthorsModal({ authors, initialTargetId, onClose, onMerged }: Props) {
  const [targetId, setTargetId] = useState<number | ''>(initialTargetId ?? '')
  const [sourceId, setSourceId] = useState<number | ''>('')
  const [sourceBookCount, setSourceBookCount] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Preview: fetch source's book count so the user sees how many books will
  // be reparented. Cheap — the author list already has statistics on each
  // row but that is computed from books, not always populated, so we do a
  // real /book?authorId lookup for accuracy.
  useEffect(() => {
    if (!sourceId) { setSourceBookCount(null); return }
    let cancelled = false
    api.listBooks({ authorId: Number(sourceId) })
      .then(bs => { if (!cancelled) setSourceBookCount(bs.length) })
      .catch(() => { if (!cancelled) setSourceBookCount(null) })
    return () => { cancelled = true }
  }, [sourceId])

  const sorted = useMemo(
    () => [...authors].sort((a, b) => a.authorName.localeCompare(b.authorName)),
    [authors],
  )
  const target = useMemo(() => authors.find(a => a.id === targetId), [authors, targetId])
  const source = useMemo(() => authors.find(a => a.id === sourceId), [authors, sourceId])

  const canSubmit = targetId !== '' && sourceId !== '' && targetId !== sourceId && !busy

  const submit = async () => {
    if (!canSubmit || !source || !target) return
    const msg = `Merge "${source.authorName}" into "${target.authorName}"?\n\n` +
      `• ${sourceBookCount ?? '?'} book(s) will be reparented.\n` +
      `• "${source.authorName}" will be kept as an alias of "${target.authorName}".\n` +
      `• This cannot be undone.`
    if (!confirm(msg)) return
    setBusy(true)
    setError(null)
    try {
      await api.mergeAuthors(Number(targetId), Number(sourceId))
      onMerged()
      onClose()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Merge failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div
        className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-lg shadow-2xl"
        onClick={e => e.stopPropagation()}
      >
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 className="text-lg font-semibold">Merge authors</h3>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1">
            Collapse a duplicate author into a canonical one. The source row is deleted; its
            books, name, and OpenLibrary id are preserved as aliases on the target.
          </p>
        </div>

        <div className="p-4 space-y-3">
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Source (will be removed)</label>
            <select
              value={sourceId}
              onChange={e => setSourceId(e.target.value ? Number(e.target.value) : '')}
              className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
            >
              <option value="">— Select source author —</option>
              {sorted.filter(a => a.id !== targetId).map(a => (
                <option key={a.id} value={a.id}>{a.authorName}</option>
              ))}
            </select>
          </div>

          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Target (canonical, kept)</label>
            <select
              value={targetId}
              onChange={e => setTargetId(e.target.value ? Number(e.target.value) : '')}
              className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
            >
              <option value="">— Select target author —</option>
              {sorted.filter(a => a.id !== sourceId).map(a => (
                <option key={a.id} value={a.id}>{a.authorName}</option>
              ))}
            </select>
          </div>

          {source && target && (
            <div className="text-xs text-slate-700 dark:text-zinc-400 bg-slate-200/50 dark:bg-zinc-800/50 rounded-md p-3 space-y-1">
              <div>
                <span className="font-medium">{source.authorName}</span> → <span className="font-medium">{target.authorName}</span>
              </div>
              <div>{sourceBookCount === null ? 'Counting books…' : `${sourceBookCount} book(s) will move to the target.`}</div>
              <div className="text-slate-500 dark:text-zinc-500">
                Alias preserved: <span className="font-mono">{source.authorName}</span>
                {source.foreignAuthorId ? ` (${source.foreignAuthorId})` : ''}
              </div>
            </div>
          )}

          {error && (
            <div className="text-xs text-red-700 dark:text-red-400 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded px-3 py-2">
              {error}
            </div>
          )}
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
          <button
            onClick={onClose}
            className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
          >
            Cancel
          </button>
          <button
            onClick={submit}
            disabled={!canSubmit}
            className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed rounded text-sm font-medium text-white"
          >
            {busy ? 'Merging…' : 'Merge'}
          </button>
        </div>
      </div>
    </div>
  )
}
