import { useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Book, BookConflictBody } from '../api/client'
import { primaryFill } from './buttons'

// The "add by hand" step of AddToLibraryModal (#3840): a book no metadata
// provider knows, created exactly as typed. Renders the dialog body and footer
// as a fragment so it slots into the modal's flex column, like AddBookConfirm.
interface Props {
  // Pre-fill the title or the author, so a search that found nothing becomes
  // the add.
  initialTitle?: string
  initialAuthor?: string
  onBack: () => void
  onClose: () => void
  onAdded: (book: Book) => void
}

function basePath(): string {
  return (window as unknown as { __BINDERY_BASE__?: string }).__BINDERY_BASE__ ?? ''
}

// The 409 carries the library row, as POST /author/book's does (#1227).
function conflictBody(err: unknown): BookConflictBody | null {
  if (err && typeof err === 'object' && 'body' in err) {
    const body = (err as { body?: unknown }).body
    if (body && typeof body === 'object' && 'existingBookId' in body) return body as BookConflictBody
  }
  return null
}

const inputClass = 'w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm'
const labelClass = 'block font-medium text-sm mb-1'

export default function AddManualBook({ initialTitle, initialAuthor, onBack, onClose, onAdded }: Props) {
  const { t } = useTranslation()
  const [title, setTitle] = useState(initialTitle ?? '')
  const [authorName, setAuthorName] = useState(initialAuthor ?? '')
  const [seriesTitle, setSeriesTitle] = useState('')
  const [seriesPosition, setSeriesPosition] = useState('')
  // '' = the default.media_type setting.
  const [mediaType, setMediaType] = useState('')
  // Off by default: a book nobody has metadata for is most often one the
  // user already has the file for.
  const [searchOnAdd, setSearchOnAdd] = useState(false)
  const [adding, setAdding] = useState(false)
  const [addError, setAddError] = useState<string | null>(null)
  const [addConflict, setAddConflict] = useState<BookConflictBody | null>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const authorHintId = useId()

  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  const canAdd = title.trim() !== '' && authorName.trim() !== '' && !adding

  const add = async () => {
    if (!canAdd) return
    setAdding(true)
    setAddError(null)
    setAddConflict(null)
    try {
      const created = await api.addManualBook({
        title: title.trim(),
        authorName: authorName.trim(),
        ...(seriesTitle.trim() ? { seriesTitle: seriesTitle.trim() } : {}),
        ...(seriesTitle.trim() && seriesPosition.trim() ? { seriesPosition: seriesPosition.trim() } : {}),
        ...(mediaType ? { mediaType } : {}),
        searchOnAdd,
      })
      onAdded(created)
    } catch (err: unknown) {
      const conflict = conflictBody(err)
      setAddConflict(conflict)
      if (conflict) {
        setAddError(t('addToLibrary.manual.alreadyInLibrary'))
      } else {
        setAddError(err instanceof Error ? err.message : t('addToLibrary.book.addFailed'))
      }
    } finally {
      setAdding(false)
    }
  }

  return (
    <>
      <form
        id="add-manual-book-form"
        className="p-4 flex-1 overflow-y-auto space-y-3"
        onSubmit={e => { e.preventDefault(); void add() }}
      >
        <div>
          <h4 ref={headingRef} tabIndex={-1} className="rounded-sm font-semibold focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-emerald-500">{t('addToLibrary.manual.heading')}</h4>
          <p className="mt-1 text-xs text-fg-muted">{t('addToLibrary.manual.hint')}</p>
        </div>

        <label className="block">
          <span className={labelClass}>{t('addToLibrary.manual.titleLabel')}</span>
          <input type="text" required value={title} onChange={e => setTitle(e.target.value)} className={inputClass} />
        </label>

        <div>
          <label className="block">
            <span className={labelClass}>{t('addToLibrary.manual.authorLabel')}</span>
            <input type="text" required value={authorName} onChange={e => setAuthorName(e.target.value)} aria-describedby={authorHintId} className={inputClass} />
          </label>
          <p id={authorHintId} className="text-xs text-fg-muted mt-1">{t('addToLibrary.manual.authorHint')}</p>
        </div>

        <div className="grid grid-cols-[minmax(0,1fr)_7rem] gap-2">
          <label className="block">
            <span className={labelClass}>{t('addToLibrary.manual.seriesLabel')}</span>
            <input type="text" value={seriesTitle} onChange={e => setSeriesTitle(e.target.value)} className={inputClass} />
          </label>
          <label className="block">
            <span className={labelClass}>{t('addToLibrary.manual.positionLabel')}</span>
            <input type="text" inputMode="decimal" value={seriesPosition} onChange={e => setSeriesPosition(e.target.value)} disabled={!seriesTitle.trim()} className={`${inputClass} disabled:opacity-50`} />
          </label>
        </div>

        <label className="flex items-center gap-2 text-sm select-none">
          <span className="font-medium">{t('addToLibrary.book.format')}</span>
          <select
            aria-label={t('addToLibrary.book.formatLabel')}
            value={mediaType}
            onChange={e => setMediaType(e.target.value)}
            className="text-xs bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1"
          >
            <option value="">{t('addToLibrary.book.defaultFormat')}</option>
            <option value="ebook">{t('common.ebook')}</option>
            <option value="audiobook">{t('common.audiobook')}</option>
            <option value="both">{t('common.both')}</option>
          </select>
        </label>

        <label className="flex items-start gap-2 text-sm cursor-pointer select-none">
          <input
            type="checkbox"
            checked={searchOnAdd}
            onChange={e => setSearchOnAdd(e.target.checked)}
            className="mt-0.5 flex-shrink-0"
          />
          <span>
            <span className="font-medium">{t('addToLibrary.book.autoSearchLabel')}</span>
            <span className="block text-xs text-fg-muted mt-0.5">{t('addToLibrary.book.autoSearchHint')}</span>
          </span>
        </label>

        {addError && (
          <div role="alert" className="px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
            <div>{addError}</div>
            {addConflict?.existingBookId && (
              <div className="mt-2 text-xs font-medium">
                <a href={`${basePath()}/book/${addConflict.existingBookId}`} className="underline">{t('addToLibrary.book.openExisting')}</a>
              </div>
            )}
          </div>
        )}
      </form>

      <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
        <button type="button" onClick={onBack} disabled={adding} className="mr-auto px-4 py-2 text-sm text-fg-muted hover:text-slate-900 dark:hover:text-white disabled:opacity-50">{t('addToLibrary.backToResults')}</button>
        <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-fg-muted hover:text-slate-900 dark:hover:text-white">{t('common.cancel')}</button>
        <button type="submit" form="add-manual-book-form" disabled={!canAdd} className={`px-4 py-2 ${primaryFill} disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium`}>{adding ? t('addToLibrary.adding') : t('addToLibrary.book.confirmAdd')}</button>
      </div>
    </>
  )
}
