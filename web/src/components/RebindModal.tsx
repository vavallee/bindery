import { useState } from 'react'
import { api, ApiError, Book } from '../api/client'

interface AuthorMismatch {
  currentAuthor: string
  upstreamAuthor: string
}

interface Props {
  book: Book
  onClose: () => void
  onSuccess: (updated: Book) => void
}

export default function RebindModal({ book, onClose, onSuccess }: Props) {
  const [provider, setProvider] = useState<'openlibrary' | 'hardcover'>('openlibrary')
  const [foreignId, setForeignId] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [mismatch, setMismatch] = useState<AuthorMismatch | null>(null)

  const submit = async (force: boolean) => {
    const trimmed = foreignId.trim()
    if (!trimmed) {
      setError('Foreign ID is required')
      return
    }
    setSubmitting(true)
    setError(null)
    setMismatch(null)
    try {
      const updated = await api.rebindBook(book.id, provider, trimmed, force)
      onSuccess(updated)
    } catch (err: unknown) {
      if (err instanceof ApiError && err.status === 409) {
        const body = err.body as { force_required?: boolean; current_author?: string; upstream_author?: string; error?: string }
        if (body.force_required) {
          setMismatch({
            currentAuthor: body.current_author ?? '',
            upstreamAuthor: body.upstream_author ?? '',
          })
          setSubmitting(false)
          return
        }
        setError(body.error ?? 'Conflict')
      } else {
        setError(err instanceof Error ? err.message : 'Re-bind failed')
      }
    } finally {
      setSubmitting(false)
    }
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    submit(false)
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div
        className="bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-700 rounded-lg shadow-xl p-6 w-full max-w-md mx-4"
        onClick={e => e.stopPropagation()}
      >
        <h2 className="text-base font-semibold mb-1 text-slate-900 dark:text-white">Re-bind metadata</h2>
        <p className="text-xs text-slate-500 dark:text-zinc-400 mb-4">
          Override the upstream record for <span className="font-medium text-slate-700 dark:text-zinc-200">{book.title}</span>.
          Enter the provider and the exact foreign ID of the correct record.
        </p>

        {mismatch ? (
          <div className="space-y-4">
            <div className="rounded bg-amber-50 dark:bg-amber-950/40 border border-amber-300 dark:border-amber-700 p-3 text-xs text-amber-800 dark:text-amber-300">
              <p className="font-semibold mb-1">Author mismatch</p>
              <p>
                The upstream record belongs to <span className="font-medium">{mismatch.upstreamAuthor}</span>, but this
                book is currently attributed to <span className="font-medium">{mismatch.currentAuthor}</span>.
              </p>
              <p className="mt-1">Are you sure you want to re-bind anyway?</p>
            </div>
            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => setMismatch(null)}
                disabled={submitting}
                className="px-4 py-1.5 text-sm rounded bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 disabled:opacity-50"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={() => submit(true)}
                disabled={submitting}
                className="px-4 py-1.5 text-sm rounded bg-amber-600 hover:bg-amber-500 disabled:opacity-50 font-medium text-white"
              >
                {submitting ? 'Re-binding…' : 'Re-bind anyway'}
              </button>
            </div>
          </div>
        ) : (
          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label className="block text-xs font-medium text-slate-700 dark:text-zinc-300 mb-1">
                Provider
              </label>
              <select
                value={provider}
                onChange={e => setProvider(e.target.value as 'openlibrary' | 'hardcover')}
                disabled={submitting}
                className="w-full bg-slate-100 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-1.5 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-500 disabled:opacity-50"
              >
                <option value="openlibrary">OpenLibrary</option>
                <option value="hardcover">Hardcover</option>
              </select>
            </div>

            <div>
              <label className="block text-xs font-medium text-slate-700 dark:text-zinc-300 mb-1">
                Foreign ID
              </label>
              <input
                type="text"
                value={foreignId}
                onChange={e => setForeignId(e.target.value)}
                placeholder={provider === 'openlibrary' ? 'e.g. OL12345W' : 'e.g. hc:12345'}
                disabled={submitting}
                className="w-full bg-slate-100 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-1.5 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-500 disabled:opacity-50"
                autoFocus
              />
              <p className="mt-1 text-xs text-slate-500 dark:text-zinc-500">
                {provider === 'openlibrary'
                  ? 'Find the Work ID on openlibrary.org (e.g. OL12345W from the URL).'
                  : 'Use the Hardcover book slug or numeric ID prefixed with "hc:".'}
              </p>
            </div>

            {error && (
              <p className="text-xs text-red-600 dark:text-red-400">{error}</p>
            )}

            <div className="flex justify-end gap-2 pt-1">
              <button
                type="button"
                onClick={onClose}
                disabled={submitting}
                className="px-4 py-1.5 text-sm rounded bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 disabled:opacity-50"
              >
                Cancel
              </button>
              <button
                type="submit"
                disabled={submitting || !foreignId.trim()}
                className="px-4 py-1.5 text-sm rounded bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 font-medium"
              >
                {submitting ? 'Re-binding…' : 'Re-bind'}
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  )
}
