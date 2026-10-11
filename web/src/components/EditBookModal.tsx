import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Book, Series } from '../api/client'
import { useModal } from './useModal'
import { useIsAdmin } from '../auth/AuthContext'

interface Props {
  book: Book
  onClose: () => void
  onSaved: (book: Book) => void
  // Called after the book's series changed, so the page reloads its series.
  onSeriesSaved?: () => void
  // How many series the user took the book out of (#2554). Unlock all fields
  // forgets those too, so it is offered when there are any.
  seriesExclusions?: number
}

// The series a book is filed under for naming, picked as the server does
// (GetPrimarySeriesForBook): primary first, then one with a position, then the
// lowest id.
interface Membership { id: number; position: string }

const NEW_SERIES = 'new'

// Manual metadata editor (#1237, #1446). Only fields the user actually
// changed are sent — the backend locks every submitted field against
// metadata refresh, so sending unchanged values would spuriously lock them.
export default function EditBookModal({ book, onClose, onSaved, onSeriesSaved, seriesExclusions: exclusions = 0 }: Props) {
  const { t } = useTranslation()
  const [title, setTitle] = useState(book.title)
  const [description, setDescription] = useState(book.description || '')
  const [genres, setGenres] = useState((book.genres ?? []).join(', '))
  const [language, setLanguage] = useState(book.language || '')
  const [releaseDate, setReleaseDate] = useState(book.releaseDate ? book.releaseDate.slice(0, 10) : '')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Series (#2554): the book's primary series and its position, edited here
  // so one book can be fixed without going through the series page. null
  // until loaded; the row stays hidden if loading fails, and editing the
  // other fields still works.
  const [allSeries, setAllSeries] = useState<Series[] | null>(null)
  const [membership, setMembership] = useState<Membership | null>(null)
  // Every series the book is in, so No series can take it out of all of them.
  const [memberOf, setMemberOf] = useState<number[]>([])
  const [seriesChoice, setSeriesChoice] = useState<string>('')
  const [newSeriesName, setNewSeriesName] = useState('')
  const [position, setPosition] = useState('')
  // Series changes are admin only, so other users get no series row.
  const isAdmin = useIsAdmin()
  const seriesExclusions = isAdmin ? exclusions : 0

  useEffect(() => {
    if (!isAdmin) return
    let active = true
    // Only the author's own series are offered: filing a book under another
    // author's series is not a fix anyone means to make from here.
    const load = book.authorId ? api.listAuthorSeries(book.authorId) : Promise.resolve([] as Series[])
    load
      .then(authorSeries => {
        if (!active) return
        const mine = authorSeries.flatMap(s =>
          (s.books ?? []).filter(b => b.bookId === book.id).map(b => ({ id: s.id, position: b.positionInSeries, primary: b.primarySeries === true })))
        const current = [...mine].sort((a, b) =>
          Number(b.primary) - Number(a.primary) ||
          Number(a.position.trim() === '') - Number(b.position.trim() === '') ||
          a.id - b.id)[0] ?? null
        setAllSeries([...authorSeries].sort((a, b) => a.title.localeCompare(b.title)))
        setMembership(current ? { id: current.id, position: current.position } : null)
        setMemberOf(mine.map(m => m.id))
        setSeriesChoice(current ? String(current.id) : '')
        setPosition(current?.position ?? '')
      })
      .catch(() => { /* the series row stays hidden */ })
    return () => { active = false }
  }, [book.authorId, book.id, isAdmin])

  const seriesChanged = useMemo(() => {
    if (allSeries === null) return false
    const was = membership ? String(membership.id) : ''
    if (seriesChoice !== was) return true
    return seriesChoice !== '' && position.trim() !== (membership?.position ?? '')
  }, [allSeries, membership, seriesChoice, position])

  const locked = book.lockedFields ?? []
  const { titleId, panelProps } = useModal({ onClose, canClose: !saving })

  const canSave = !saving && title.trim() !== '' && !(seriesChoice === NEW_SERIES && !newSeriesName.trim())

  const save = async () => {
    const patch: Record<string, unknown> = {}
    if (title.trim() !== book.title) patch.title = title.trim()
    if (description !== (book.description || '')) patch.description = description
    const genreList = genres.split(',').map(g => g.trim()).filter(Boolean)
    if (genreList.join('\u0000') !== (book.genres ?? []).join('\u0000')) patch.genres = genreList
    if (language.trim() !== (book.language || '')) patch.language = language.trim()
    const origDate = book.releaseDate ? book.releaseDate.slice(0, 10) : ''
    if (releaseDate !== origDate) patch.releaseDate = releaseDate
    if (Object.keys(patch).length === 0 && !seriesChanged) {
      onClose()
      return
    }
    setSaving(true)
    setError(null)
    try {
      if (seriesChanged) {
        await saveSeries()
        onSeriesSaved?.()
      }
      if (Object.keys(patch).length > 0) {
        onSaved(await api.updateBook(book.id, patch as Partial<Book>))
      }
      onClose()
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bookDetail.edit.saveFailed', 'Save failed'))
    } finally {
      setSaving(false)
    }
  }

  // Files the book under the chosen series as its primary one, at the given
  // position, and takes it out of the series it was in before, or with No
  // series out of every series it is in. The server
  // demotes the book's other series and remembers the removal, so a refresh
  // does not put it back (#2554).
  const saveSeries = async () => {
    let target: number | null = null
    if (seriesChoice === NEW_SERIES) {
      target = (await api.createSeries({ title: newSeriesName.trim() })).id
    } else if (seriesChoice !== '') {
      target = Number(seriesChoice)
    }
    if (target !== null) {
      await api.linkBookToSeries(target, { bookId: book.id, positionInSeries: position.trim(), primarySeries: true })
    }
    const leave = target === null ? memberOf : membership && membership.id !== target ? [membership.id] : []
    for (const id of leave) {
      await api.removeBookFromSeries(id, book.id)
    }
  }

  const unlockAll = async () => {
    setSaving(true)
    setError(null)
    try {
      const updated = await api.updateBook(book.id, { lockedFields: [] })
      onSaved(updated)
      if (seriesExclusions > 0) onSeriesSaved?.()
      onClose()
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bookDetail.edit.saveFailed', 'Save failed'))
    } finally {
      setSaving(false)
    }
  }

  const lockBadge = (field: string) =>
    locked.includes(field) ? (
      <span
        className="ml-1 text-xs text-amber-600 dark:text-amber-400"
        title={t('bookDetail.edit.lockedHint', 'Manually edited — metadata refresh will not overwrite this field')}
      >
        🔒
      </span>
    ) : null

  const inputCls =
    'w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500'
  const labelCls = 'block text-sm font-medium mb-1'

  return (
    <div className="modal-overlay fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div
        {...panelProps}
        className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-lg shadow-2xl modal-max-h flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 id={titleId} className="text-lg font-semibold">{t('bookDetail.edit.title', 'Edit metadata')}</h3>
          <p className="text-xs text-slate-500 dark:text-zinc-500 mt-0.5">
            {t('bookDetail.edit.subtitle', 'Edited fields are locked so metadata refresh keeps your values.')}
          </p>
        </div>

        <div className="p-4 flex-1 overflow-y-auto space-y-3">
          <div>
            <label className={labelCls} htmlFor="edit-book-title">
              {t('bookDetail.edit.fieldTitle', 'Title')}
              {lockBadge('title')}
            </label>
            <input id="edit-book-title" type="text" value={title} onChange={e => setTitle(e.target.value)} className={inputCls} />
          </div>
          <div>
            <label className={labelCls} htmlFor="edit-book-description">
              {t('bookDetail.edit.fieldDescription', 'Description')}
              {lockBadge('description')}
            </label>
            <textarea
              id="edit-book-description"
              value={description}
              onChange={e => setDescription(e.target.value)}
              rows={4}
              className={inputCls}
            />
          </div>
          <div>
            <label className={labelCls} htmlFor="edit-book-genres">
              {t('bookDetail.edit.fieldGenres', 'Genres (comma-separated)')}
              {lockBadge('genres')}
            </label>
            <input id="edit-book-genres" type="text" value={genres} onChange={e => setGenres(e.target.value)} className={inputCls} placeholder="Fantasy, Epic" />
          </div>
          <div className="flex gap-3">
            <div className="flex-1">
              <label className={labelCls} htmlFor="edit-book-language">
                {t('bookDetail.edit.fieldLanguage', 'Language')}
                {lockBadge('language')}
              </label>
              <input id="edit-book-language" type="text" value={language} onChange={e => setLanguage(e.target.value)} className={inputCls} placeholder="en" />
            </div>
            <div className="flex-1">
              <label className={labelCls} htmlFor="edit-book-releasedate">
                {t('bookDetail.edit.fieldReleaseDate', 'Release date')}
                {lockBadge('releaseDate')}
              </label>
              <input id="edit-book-releasedate" type="date" value={releaseDate} onChange={e => setReleaseDate(e.target.value)} className={inputCls} />
            </div>
          </div>
          {allSeries !== null && (
            <div className="flex gap-3 items-end">
              <div className="flex-1 min-w-0">
                <label className={labelCls} htmlFor="edit-book-series">
                  {t('bookDetail.edit.fieldSeries', 'Series')}
                </label>
                <select id="edit-book-series" value={seriesChoice} onChange={e => setSeriesChoice(e.target.value)} className={inputCls}>
                  <option value="">{t('bookDetail.edit.seriesNone', 'No series')}</option>
                  {allSeries.map(s => <option key={s.id} value={String(s.id)}>{s.title}</option>)}
                  <option value={NEW_SERIES}>{t('bookDetail.edit.seriesNew', 'New series…')}</option>
                </select>
              </div>
              <div className="w-24">
                <label className={labelCls} htmlFor="edit-book-position">
                  {t('bookDetail.edit.fieldPosition', 'Position')}
                </label>
                <input id="edit-book-position" type="text" value={position} onChange={e => setPosition(e.target.value)}
                  disabled={seriesChoice === ''} className={inputCls} />
              </div>
            </div>
          )}
          {seriesChoice === NEW_SERIES && (
            <div>
              <label className={labelCls} htmlFor="edit-book-new-series">
                {t('bookDetail.edit.fieldNewSeries', 'New series name')}
              </label>
              <input id="edit-book-new-series" type="text" value={newSeriesName} onChange={e => setNewSeriesName(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter' && canSave) void save() }} className={inputCls} autoFocus />
            </div>
          )}
          {error && <p className="text-sm text-red-400">{error}</p>}
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex items-center gap-2">
          {(locked.length > 0 || seriesExclusions > 0) && (
            <button
              type="button"
              onClick={unlockAll}
              disabled={saving}
              className="text-xs px-3 py-2 text-amber-700 dark:text-amber-400 hover:underline disabled:opacity-50"
              title={t('bookDetail.edit.unlockAllHint', 'Let metadata refresh manage every field and the book\'s series again')}
            >
              {t('bookDetail.edit.unlockAll', 'Unlock all fields')}
            </button>
          )}
          <div className="ml-auto flex gap-2">
            <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">
              {t('common.cancel')}
            </button>
            <button
              type="button"
              onClick={save}
              disabled={!canSave}
              className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded-md text-sm font-medium"
            >
              {saving ? t('common.saving') : t('common.save')}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
