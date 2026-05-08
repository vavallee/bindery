import { useState } from 'react'
import { api, Book } from '../api/client'

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

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    const trimmed = foreignId.trim()
    if (!trimmed) {
      setError('Foreign ID is required')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const updated = await api.rebindBook(book.id, provider, trimmed)
      onSuccess(updated)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Re-bind failed')
    } finally {
      setSubmitting(false)
    }
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
      </div>
    </div>
  )
}
