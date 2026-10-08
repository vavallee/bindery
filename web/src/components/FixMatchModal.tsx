import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Book, ReassignPreview } from '../api/client'
import Alert from './Alert'
import { useModal } from './useModal'

type Props = {
  sourceBookId: number
  path: string
  format: string
  onClose: () => void
  onReassigned: (targetBookId: number) => void
}

// FixMatchModal lets the user move a mis-matched file to the correct existing
// book (#1238). It searches the library and POSTs the chosen target to the
// reassign endpoint.
//
// Picking a book is deliberately NOT the commit (#2055). It opens a
// confirmation step that asks what should happen on disk, and only the confirm
// button calls reassign. The default is to correct the match only: the file
// stays where it is under its current name and only the book it belongs to
// changes, which is what the *arr apps' fix match does and what a library
// imported in place expects. Moving it is an explicit choice. That runs the
// full import, so the file is moved into the target's folder and renamed from
// the naming template; the endpoint returns 202 and does the work in the
// background, so there is nothing to undo against afterwards. Choosing it
// fetches a read-only destination preview and names the path the file will
// end up at before the user confirms.
export default function FixMatchModal({ sourceBookId, path, format, onClose, onReassigned }: Props) {
  const { t } = useTranslation()
  const [term, setTerm] = useState('')
  const [results, setResults] = useState<Book[]>([])
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  // The pending target: set by picking a candidate, cleared by going back.
  // While it is set the modal shows the confirmation step instead of the list.
  const [target, setTarget] = useState<Book | null>(null)
  const [preview, setPreview] = useState<ReassignPreview | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)
  const [previewError, setPreviewError] = useState('')
  // false (the default) corrects the match and leaves the file alone; true
  // moves and renames it into the target's folder.
  const [relocate, setRelocate] = useState(false)

  useEffect(() => {
    const q = term.trim()
    if (q.length < 2) {
      setResults([])
      return
    }
    let cancelled = false
    setLoading(true)
    const handle = setTimeout(async () => {
      try {
        const { items } = await api.listBooks({ search: q, limit: 20 })
        if (!cancelled) setResults(items.filter(b => b.id !== sourceBookId))
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : 'search failed')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }, 300)
    return () => {
      cancelled = true
      clearTimeout(handle)
    }
  }, [term, sourceBookId])

  // Fetch the destination for the pending target once the user chooses to move
  // the file. A failure here does not block the reassign: it means we cannot
  // name the path, not that nothing will move, so the warning still stands and
  // the copy says the destination is unknown.
  useEffect(() => {
    if (!target || !relocate) return
    let cancelled = false
    setPreview(null)
    setPreviewError('')
    setPreviewLoading(true)
    api
      .reassignFilePreview({ path, targetBookId: target.id, format })
      .then(p => {
        if (!cancelled) setPreview(p)
      })
      .catch(e => {
        if (!cancelled) setPreviewError(e instanceof Error ? e.message : 'preview failed')
      })
      .finally(() => {
        if (!cancelled) setPreviewLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [target, path, format, relocate])

  const chooseTarget = (b: Book) => {
    setError('')
    setTarget(b)
  }

  const back = () => {
    setTarget(null)
    setPreview(null)
    setPreviewError('')
    setRelocate(false)
  }

  const confirmReassign = async () => {
    if (!target) return
    setSubmitting(true)
    setError('')
    try {
      await api.reassignFile({ path, targetBookId: target.id, format, relocate })
      onReassigned(target.id)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'reassign failed')
      setSubmitting(false)
    }
  }

  // A noop means the file is already sitting at the templated path for the
  // target, so the association changes and nothing on disk does. Warning about
  // a move that will not happen would be its own kind of lie.
  const movesOnDisk = relocate && preview?.status !== 'noop'

  const { titleId, panelProps } = useModal({ onClose, canClose: !submitting })

  return (
    <div
      className="modal-overlay fixed inset-0 z-50 flex items-start sm:items-center justify-center bg-black/50 p-4 pt-14 sm:pt-4"
      onClick={onClose}
      role="presentation"
    >
      <div
        className="w-full max-w-lg rounded-lg bg-white dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 shadow-xl modal-max-h flex flex-col"
        onClick={e => e.stopPropagation()}
        {...panelProps}
      >
        <div className="shrink-0 p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 id={titleId} className="text-base font-semibold">
            {t('bookDetail.fixMatch.title', 'Reassign file to another book')}
          </h3>
          <p className="mt-1 text-xs text-slate-500 dark:text-zinc-500 font-mono break-all sm:truncate" title={path}>
            {path}
          </p>
        </div>

        {!target && (
          <div className="flex-1 min-h-0 overflow-y-auto p-4">
            {/* Said up front, before a book is even picked, so nobody discovers
                it from the list of candidates alone. */}
            <p className="mb-3 text-xs text-slate-600 dark:text-zinc-400">
              {t(
                'bookDetail.fixMatch.moveNotice',
                'By default this only corrects which book the file belongs to and leaves the file where it is. You can also have Bindery move and rename it into the chosen book’s folder.',
              )}
            </p>
            <input
              enterKeyHint="search"
              autoFocus
              type="text"
              value={term}
              onChange={e => setTerm(e.target.value)}
              placeholder={t('bookDetail.fixMatch.searchPlaceholder', 'Search your library for the correct book…')}
              className="w-full px-3 py-2 rounded border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-950 text-sm"
            />
            {error && <p className="mt-2 text-xs text-red-600 dark:text-red-400">{error}</p>}
            <div className="mt-3 max-h-72 overflow-y-auto divide-y divide-slate-100 dark:divide-zinc-800">
              {loading && <p className="py-3 text-xs text-slate-500 dark:text-zinc-500">{t('common.loading')}</p>}
              {!loading && term.trim().length >= 2 && results.length === 0 && (
                <p className="py-3 text-xs text-slate-500 dark:text-zinc-500">
                  {t('bookDetail.fixMatch.noResults', 'No matching books in your library')}
                </p>
              )}
              {results.map(b => (
                <button
                  key={b.id}
                  type="button"
                  onClick={() => chooseTarget(b)}
                  className="block w-full text-left py-2 px-1 rounded hover:bg-slate-100 dark:hover:bg-zinc-800"
                >
                  <span className="text-sm font-medium text-slate-900 dark:text-white">{b.title}</span>
                  {b.author?.authorName && (
                    <span className="text-xs text-slate-500 dark:text-zinc-500"> · {b.author.authorName}</span>
                  )}
                </button>
              ))}
            </div>
          </div>
        )}

        {target && (
          <div className="flex-1 min-h-0 overflow-y-auto p-4">
            <p className="text-sm text-slate-700 dark:text-zinc-300">
              {t('bookDetail.fixMatch.reassigningTo', 'Reassigning to')}{' '}
              <span className="font-medium text-slate-900 dark:text-white">{target.title}</span>
              {target.author?.authorName && (
                <span className="text-slate-500 dark:text-zinc-500"> · {target.author.authorName}</span>
              )}
            </p>

            <fieldset className="mt-3 space-y-2">
              <legend className="text-xs font-medium text-slate-700 dark:text-zinc-300">
                {t('bookDetail.fixMatch.modeLegend', 'What should happen to the file?')}
              </legend>
              <label className="flex items-start gap-2 text-xs">
                <input
                  type="radio"
                  name="fix-match-mode"
                  className="mt-0.5"
                  checked={!relocate}
                  onChange={() => setRelocate(false)}
                  disabled={submitting}
                />
                <span>
                  <span className="font-medium text-slate-900 dark:text-white">
                    {t('bookDetail.fixMatch.modeLinkOnly', 'Correct the match only')}
                  </span>
                  <span className="block text-slate-500 dark:text-zinc-500">
                    {t(
                      'bookDetail.fixMatch.modeLinkOnlyHint',
                      'The file stays where it is, under its current name. Only the book it belongs to changes.',
                    )}
                  </span>
                </span>
              </label>
              <label className="flex items-start gap-2 text-xs">
                <input
                  type="radio"
                  name="fix-match-mode"
                  className="mt-0.5"
                  checked={relocate}
                  onChange={() => setRelocate(true)}
                  disabled={submitting}
                />
                <span>
                  <span className="font-medium text-slate-900 dark:text-white">
                    {t('bookDetail.fixMatch.modeRelocate', 'Also move and rename the file')}
                  </span>
                  <span className="block text-slate-500 dark:text-zinc-500">
                    {t(
                      'bookDetail.fixMatch.modeRelocateHint',
                      'Bindery moves the file into the chosen book’s folder and renames it from your naming template.',
                    )}
                  </span>
                </span>
              </label>
            </fieldset>

            {!relocate && (
              <dl className="mt-3 space-y-2 text-xs">
                <div>
                  <dt className="text-slate-500 dark:text-zinc-500">
                    {t('bookDetail.fixMatch.staysAtLabel', 'The file stays at')}
                  </dt>
                  <dd className="font-mono break-all text-slate-700 dark:text-zinc-300">{path}</dd>
                </div>
              </dl>
            )}

            {movesOnDisk && (
              <Alert
                tier="warning"
                className="mt-3"
                title={t('bookDetail.fixMatch.confirmHeading', 'This moves and renames the file on disk')}
              >
                <p className="text-xs">
                  {t(
                    'bookDetail.fixMatch.confirmBody',
                    'Bindery runs the full import against the chosen book, so the file is moved into that book’s folder and renamed from your naming template. Whatever folder layout you had for this file is replaced.',
                  )}
                </p>
                <p className="mt-2 text-xs">
                  {t(
                    'bookDetail.fixMatch.noUndo',
                    'The move starts in the background as soon as you confirm, and Bindery cannot undo it for you.',
                  )}
                </p>
              </Alert>
            )}

            {relocate && !movesOnDisk && (
              <p className="mt-3 text-xs text-slate-600 dark:text-zinc-400">
                {t(
                  'bookDetail.fixMatch.noopNotice',
                  'This file is already where your naming template puts it for that book, so only the metadata link changes.',
                )}
              </p>
            )}

            {relocate && (
            <dl className="mt-3 space-y-2 text-xs">
              <div>
                <dt className="text-slate-500 dark:text-zinc-500">
                  {t('bookDetail.fixMatch.currentPathLabel', 'Current path')}
                </dt>
                <dd className="font-mono break-all text-slate-700 dark:text-zinc-300">{path}</dd>
              </div>
              <div>
                <dt className="text-slate-500 dark:text-zinc-500">
                  {t('bookDetail.fixMatch.destinationLabel', 'It will be moved to')}
                </dt>
                <dd className="font-mono break-all text-slate-900 dark:text-white">
                  {previewLoading && (
                    <span className="font-sans text-slate-500 dark:text-zinc-500">
                      {t('bookDetail.fixMatch.destinationLoading', 'Working out the destination…')}
                    </span>
                  )}
                  {!previewLoading && preview?.destination && preview.destination}
                  {!previewLoading && !preview?.destination && (
                    <span className="font-sans text-amber-700 dark:text-amber-400">
                      {t(
                        'bookDetail.fixMatch.destinationUnknown',
                        'Bindery could not work out the destination. The file will still be moved and renamed.',
                      )}
                    </span>
                  )}
                </dd>
              </div>
            </dl>
            )}

            {relocate && previewError && <p className="mt-2 text-xs text-red-600 dark:text-red-400">{previewError}</p>}
            {relocate && preview?.message && (
              <p className="mt-2 text-xs text-amber-700 dark:text-amber-400">{preview.message}</p>
            )}
            {error && <p className="mt-2 text-xs text-red-600 dark:text-red-400">{error}</p>}
          </div>
        )}

        <div className="shrink-0 p-4 border-t border-slate-200 dark:border-zinc-800 flex flex-wrap justify-end gap-2">
          {target && (
            <button
              type="button"
              onClick={back}
              disabled={submitting}
              className="px-3 py-1.5 text-xs rounded bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 disabled:opacity-50"
            >
              {t('bookDetail.fixMatch.backButton', 'Choose a different book')}
            </button>
          )}
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1.5 text-xs rounded bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700"
          >
            {t('common.cancel')}
          </button>
          {target && (
            <button
              type="button"
              onClick={confirmReassign}
              disabled={submitting || (relocate && previewLoading)}
              className="px-3 py-1.5 text-xs rounded bg-amber-600 text-white hover:bg-amber-700 disabled:opacity-50"
            >
              {movesOnDisk
                ? t('bookDetail.fixMatch.confirmButton', 'Move and reassign')
                : t('bookDetail.fixMatch.confirmButtonNoop', 'Reassign')}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
