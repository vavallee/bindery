import { useEffect, useState } from 'react'
import {
  reorganizeApi,
  ReorganizeMove,
  ReorganizeResponse,
  ReorganizeScope,
} from '../api/reorganize'

interface Props {
  scope: ReorganizeScope
  id?: number
  // A human label for what is being reorganized ("Dune", "Frank Herbert",
  // "your library") shown in the modal header.
  label: string
  onClose: () => void
  // Called after an apply that moved at least one file, so the caller can
  // reload the affected book/author view.
  onApplied?: () => void
}

const statusStyles: Record<string, string> = {
  move: 'text-sky-600 dark:text-sky-400',
  moved: 'text-emerald-600 dark:text-emerald-400',
  noop: 'text-slate-400 dark:text-zinc-500',
  collision: 'text-amber-600 dark:text-amber-400',
  missing: 'text-amber-600 dark:text-amber-400',
  error: 'text-red-600 dark:text-red-400',
  failed: 'text-red-600 dark:text-red-400',
}

const statusLabel: Record<string, string> = {
  move: 'Will move',
  moved: 'Moved',
  noop: 'Already correct',
  collision: 'Destination exists',
  missing: 'Not on disk',
  error: 'Error',
  failed: 'Failed',
}

export default function RenameFilesModal({ scope, id, label, onClose, onApplied }: Props) {
  const [preview, setPreview] = useState<ReorganizeResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [applying, setApplying] = useState(false)
  const [applied, setApplied] = useState<ReorganizeResponse | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    reorganizeApi
      .preview(scope, id)
      .then(res => {
        if (!cancelled) setPreview(res)
      })
      .catch(err => {
        if (!cancelled) setError(err instanceof Error ? err.message : 'Preview failed')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [scope, id])

  const movable = (preview?.moves ?? []).filter(m => m.status === 'move')

  const apply = async () => {
    if (movable.length === 0) return
    setApplying(true)
    setError(null)
    try {
      const res = await reorganizeApi.apply(movable.map(m => m.fileId))
      setApplied(res)
      if (res.summary.moved > 0) onApplied?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Rename failed')
    } finally {
      setApplying(false)
    }
  }

  const shown = applied ?? preview
  const moves = shown?.moves ?? []

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div
        className="bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-700 rounded-lg shadow-xl p-6 w-full max-w-2xl mx-4 max-h-[85vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <h2 className="text-base font-semibold mb-1 text-slate-900 dark:text-white">Rename files</h2>
        <p className="text-xs text-slate-500 dark:text-zinc-400 mb-4">
          Move the files for{' '}
          <span className="font-medium text-slate-700 dark:text-zinc-200">{label}</span> to match the
          current naming template. Files already in the right place are left alone.
        </p>

        {loading ? (
          <p className="text-sm text-slate-500 dark:text-zinc-400">Computing changes…</p>
        ) : error && !moves.length ? (
          <p className="text-sm text-red-600 dark:text-red-400">{error}</p>
        ) : moves.length === 0 ? (
          <p className="text-sm text-slate-500 dark:text-zinc-400">No tracked files to rename.</p>
        ) : (
          <div className="overflow-y-auto flex-1 -mx-2 px-2">
            <ul className="space-y-3">
              {moves.map(m => (
                <MoveRow key={m.fileId} move={m} />
              ))}
            </ul>
          </div>
        )}

        {error && moves.length > 0 && (
          <p className="mt-3 text-sm text-red-600 dark:text-red-400">{error}</p>
        )}

        <div className="mt-4 pt-4 border-t border-slate-200 dark:border-zinc-800 flex items-center gap-2">
          <span className="text-xs text-slate-500 dark:text-zinc-400">
            {applied
              ? `${applied.summary.moved} moved, ${applied.summary.failed} failed`
              : `${movable.length} of ${moves.length} will move`}
          </span>
          <div className="ml-auto flex gap-2">
            <button
              type="button"
              onClick={onClose}
              className="px-4 py-1.5 text-sm rounded bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700"
            >
              {applied ? 'Close' : 'Cancel'}
            </button>
            {!applied && (
              <button
                type="button"
                onClick={apply}
                disabled={applying || movable.length === 0}
                className="px-4 py-1.5 text-sm rounded bg-sky-600 hover:bg-sky-500 disabled:opacity-40 font-medium text-white"
              >
                {applying ? 'Renaming…' : `Rename ${movable.length} file${movable.length === 1 ? '' : 's'}`}
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

function MoveRow({ move }: { move: ReorganizeMove }) {
  return (
    <li className="text-xs">
      <div className="flex items-center gap-2 mb-0.5">
        <span className={`font-medium ${statusStyles[move.status] ?? ''}`}>
          {statusLabel[move.status] ?? move.status}
        </span>
        <span className="text-slate-400 dark:text-zinc-600">·</span>
        <span className="text-slate-600 dark:text-zinc-300 truncate">{move.bookTitle}</span>
        <span className="uppercase text-[10px] tracking-wide text-slate-400 dark:text-zinc-600">
          {move.format}
        </span>
      </div>
      <div className="font-mono text-slate-500 dark:text-zinc-500 break-all">
        <div className="truncate" title={move.current}>
          <span className="text-slate-400 dark:text-zinc-600">from </span>
          {move.current}
        </div>
        {move.status !== 'noop' && (
          <div className="truncate" title={move.proposed}>
            <span className="text-slate-400 dark:text-zinc-600">to </span>
            {move.proposed}
          </div>
        )}
        {move.message && (
          <div className="text-amber-600 dark:text-amber-400 not-italic">{move.message}</div>
        )}
      </div>
    </li>
  )
}
