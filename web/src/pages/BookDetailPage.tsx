import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, BINDERY_BASE, Book, HistoryEvent, MediaType, SearchResult, SearchDebug, Series } from '../api/client'
import SearchDebugPanel from '../components/SearchDebugPanel'
import CoverPlaceholder from '../components/CoverPlaceholder'
import MarkdownDescription from '../components/MarkdownDescription'
import MoreMenu from '../components/MoreMenu'
import Section from '../components/Section'
import { btn, btnSize } from '../components/buttons'
import MediaBadge from '../components/MediaBadge'
import { bookStatusBadge } from '../components/bookStatus'
import RebindModal from '../components/RebindModal'
import RenameFilesModal from '../components/RenameFilesModal'
import ConfirmDialog from '../components/ConfirmDialog'
import ClipboardManualFallback from '../components/ClipboardManualFallback'
import { useClipboardCopy } from '../components/useClipboardCopy'
import { safeHref } from '../util/safeHref'
import { metadataSourceLink } from '../util/metadataSource'
import FixMatchModal from '../components/FixMatchModal'
import EditBookModal from '../components/EditBookModal'

function formatSize(n: number): string {
  if (!n || n <= 0) return ''
  if (n >= 1073741824) return (n / 1073741824).toFixed(1) + ' GB'
  if (n >= 1048576) return (n / 1048576).toFixed(0) + ' MB'
  return (n / 1024).toFixed(0) + ' KB'
}

function formatDuration(seconds?: number): string {
  if (!seconds || seconds <= 0) return ''
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

// Maps ISO-639 language codes (both 639-1 two-letter and 639-2/B three-letter
// forms) to a full English name. Codes outside this short list fall back to the
// raw code — indexers and metadata providers only reliably tag a few majors.
const LANGUAGE_NAMES: Record<string, string> = {
  en: 'English', eng: 'English',
  fr: 'French', fre: 'French', fra: 'French',
  de: 'German', ger: 'German', deu: 'German',
  nl: 'Dutch', dut: 'Dutch', nld: 'Dutch',
  es: 'Spanish', spa: 'Spanish',
  it: 'Italian', ita: 'Italian',
  pt: 'Portuguese', por: 'Portuguese',
  ja: 'Japanese', jpn: 'Japanese',
  zh: 'Chinese', chi: 'Chinese', zho: 'Chinese',
  ru: 'Russian', rus: 'Russian',
  tl: 'Tagalog', tgl: 'Tagalog',
  id: 'Indonesian', ind: 'Indonesian',
}

function languageName(code?: string): string | null {
  if (!code) return null
  return LANGUAGE_NAMES[code.toLowerCase()] ?? code
}


// Small coloured dot for a history row, by event type.
const eventDotColors: Record<string, string> = {
  grabbed: 'bg-blue-500',
  bookImported: 'bg-emerald-500',
  imported: 'bg-emerald-500',
  downloadFailed: 'bg-red-500',
  importFailed: 'bg-red-500',
  deleted: 'bg-red-500',
  renamed: 'bg-purple-500',
  bookFileRenamed: 'bg-purple-500',
  ignored: 'bg-slate-400 dark:bg-zinc-600',
}

const resultRowCls = (approved?: boolean) =>
  `flex items-center justify-between p-2 border rounded text-xs ${
    approved === false
      ? 'bg-slate-50 dark:bg-zinc-950 border-slate-200 dark:border-zinc-800 opacity-60'
      : 'bg-slate-100 dark:bg-zinc-900 border-slate-200 dark:border-zinc-800'
  }`

export function SearchResultsSection({
  results,
  bookMediaType,
  grabbing,
  onGrab,
}: {
  results: SearchResult[]
  bookMediaType?: string
  grabbing: string | null
  onGrab: (r: SearchResult) => void
}) {
  const renderRow = (r: SearchResult, fmt?: 'ebook' | 'audiobook') => (
    <div key={r.guid} className={resultRowCls(r.approved)}>
      <div className="min-w-0 mr-3">
        <div className="flex items-center gap-1.5 flex-wrap mb-0.5">
          {fmt && <MediaBadge type={fmt} />}
          <span className="truncate text-slate-800 dark:text-zinc-200">{r.title}</span>
        </div>
        <span className="text-slate-500 dark:text-zinc-500 truncate block">
          {r.indexerName} · {formatSize(r.size)} · {r.grabs} grabs
          {safeHref(r.infoUrl) && (
            <>
              {' · '}
              <a
                href={safeHref(r.infoUrl)}
                target="_blank"
                rel="noopener noreferrer"
                onClick={e => e.stopPropagation()}
                className="text-sky-600 dark:text-sky-400 hover:underline"
              >
                ↗ indexer
              </a>
            </>
          )}
          {r.rejection && <span className="ml-2 text-amber-600 dark:text-amber-400">· {r.rejection}</span>}
        </span>
      </div>
      <button
        onClick={() => onGrab(r)}
        disabled={grabbing !== null}
        className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-[11px] font-medium flex-shrink-0"
      >
        {grabbing === r.guid ? 'Grabbing…' : 'Grab'}
      </button>
    </div>
  )

  if (bookMediaType === 'both') {
    const ebooks = results.filter(r => r.mediaType === 'ebook')
    const audiobooks = results.filter(r => r.mediaType === 'audiobook')
    return (
      <>
        {ebooks.length > 0 && (
          <section className="mb-4">
            <h3 className="text-sm font-semibold mb-2 text-slate-800 dark:text-zinc-200">Ebooks ({ebooks.length})</h3>
            <div className="space-y-1">{ebooks.slice(0, 20).map(r => renderRow(r, 'ebook'))}</div>
          </section>
        )}
        {audiobooks.length > 0 && (
          <section className="mb-4">
            <h3 className="text-sm font-semibold mb-2 text-slate-800 dark:text-zinc-200">Audiobooks ({audiobooks.length})</h3>
            <div className="space-y-1">{audiobooks.slice(0, 20).map(r => renderRow(r, 'audiobook'))}</div>
          </section>
        )}
      </>
    )
  }

  return (
    <section className="mb-6">
      <h3 className="text-sm font-semibold mb-2 text-slate-800 dark:text-zinc-200">Results ({results.length})</h3>
      <div className="space-y-1">{results.slice(0, 20).map(r => renderRow(r))}</div>
    </section>
  )
}

// Neutral action button used across the page. Composed from the shared
// vocabulary rather than hand-rolled — this file used to define its own
// `actionBtnCls`/`dangerBtnCls`, which is exactly the drift buttons.ts exists
// to prevent.
const actionBtnCls = `${btn.secondary} ${btnSize.md}`

export default function BookDetailPage() {
  const { t } = useTranslation()
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const bookId = Number(id)

  const [book, setBook] = useState<Book | null>(null)
  const [events, setEvents] = useState<HistoryEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [searching, setSearching] = useState(false)
  const [results, setResults] = useState<SearchResult[] | null>(null)
  const [searchDebug, setSearchDebug] = useState<SearchDebug | null>(null)
  const [hasIndexers, setHasIndexers] = useState<boolean | null>(null)
  const [grabbing, setGrabbing] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [asinDraft, setAsinDraft] = useState('')
  const [enriching, setEnriching] = useState(false)
  const [deletingFile, setDeletingFile] = useState(false)
  const [deletingBook, setDeletingBook] = useState(false)
  const [togglingExclude, setTogglingExclude] = useState(false)
  const [showRebind, setShowRebind] = useState(false)
  const [showFixMatch, setShowFixMatch] = useState(false)
  const [showRename, setShowRename] = useState(false)
  const [showEdit, setShowEdit] = useState(false)
  const [showDeleteBook, setShowDeleteBook] = useState(false)
  const pathClipboard = useClipboardCopy()
  // For dual-format books, which format the file section is acting on.
  const [activeFormat, setActiveFormat] = useState<'ebook' | 'audiobook'>('ebook')
  // Series membership for the meta row. series_books has been populated since
  // v0.7.0 but this page never surfaced it.
  const [series, setSeries] = useState<{ title: string; position: string }[]>([])

  useEffect(() => {
    if (book?.title) {
      document.title = `${book.title} · Bindery`
      return () => { document.title = 'Bindery' }
    }
  }, [book?.title])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    Promise.all([
      api.getBook(bookId).then(b => { if (!cancelled) { setBook(b); setAsinDraft(b.asin || '') } }),
      api.listHistory({ bookId }).then(({ items }) => setEvents(items)).catch(() => {}),
    ])
      .catch(err => setError(err instanceof Error ? err.message : t('bookDetail.loadFailed')))
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [bookId, t])

  // Series membership. There is no book→series endpoint, so this reuses the
  // author's series list and picks out this book's entries — deliberately not a
  // new API. It runs after the book has loaded (it needs authorId), never
  // blocks the page, and fails silently: no series data simply means no series
  // row, which is the same as a book that genuinely isn't in one.
  useEffect(() => {
    const authorId = book?.authorId
    const id = book?.id
    if (!authorId || !id) return
    let cancelled = false
    api.listAuthorSeries(authorId)
      .then((list: Series[]) => {
        if (cancelled) return
        const mine: { title: string; position: string }[] = []
        for (const s of list) {
          for (const entry of s.books ?? []) {
            if (entry.bookId === id) mine.push({ title: s.title, position: entry.positionInSeries })
          }
        }
        setSeries(mine)
      })
      .catch(() => { /* no series row */ })
    return () => { cancelled = true }
  }, [book?.authorId, book?.id])

  // While a grab is in flight, the download → import pipeline finishes
  // asynchronously on the backend. Poll the book + history so the file and
  // status appear without a manual page reload (#1161). Only polls while the
  // book is actively downloading/importing; the dependency on book?.status
  // tears the interval down the moment it settles. Mirrors QueuePage's 5s poll.
  useEffect(() => {
    const s = book?.status
    if (s !== 'downloading' && s !== 'downloaded') return
    let cancelled = false
    const interval = setInterval(() => {
      Promise.all([
        api.getBook(bookId),
        api.listHistory({ bookId }),
      ]).then(([b, h]) => {
        if (cancelled) return
        setBook(b)
        setEvents(h.items)
      }).catch(() => {})
    }, 5000)
    return () => { cancelled = true; clearInterval(interval) }
  }, [book?.status, bookId])

  const saveField = async (patch: Partial<Book>) => {
    if (!book) return
    setSaving(true)
    setError(null)
    try {
      const updated = await api.updateBook(book.id, patch)
      setBook(updated)
      if (patch.asin !== undefined) setAsinDraft(updated.asin || '')
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bookDetail.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  const runSearch = async () => {
    if (!book) return
    setSearching(true)
    setResults(null)
    setSearchDebug(null)
    setError(null)
    try {
      const [r, indexers] = await Promise.all([
        api.searchBook(book.id),
        api.listIndexers(),
      ])
      setHasIndexers(indexers.length > 0)
      setResults(r.results)
      setSearchDebug(r.debug ?? null)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bookDetail.searchFailed'))
    } finally {
      setSearching(false)
    }
  }

  const grab = async (r: SearchResult) => {
    if (!book) return
    setGrabbing(r.guid)
    setError(null)
    try {
      await api.grab({
        guid: r.guid,
        title: r.title,
        nzbUrl: r.nzbUrl,
        size: r.size,
        bookId: book.id,
        indexerId: r.indexerId,
        protocol: r.protocol,
        mediaType: book.mediaType,
      })
      // Refresh book + history
      const [b, h] = await Promise.all([
        api.getBook(book.id),
        api.listHistory({ bookId: book.id }),
      ])
      setBook(b)
      setEvents(h.items)
      setResults(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bookDetail.grabFailed'))
    } finally {
      setGrabbing(null)
    }
  }

  const deleteFile = async (format?: 'ebook' | 'audiobook') => {
    if (!book) return
    const hasEbook = !!book.ebookFilePath
    const hasAudiobook = !!book.audiobookFilePath
    const hasLegacy = !!book.filePath && !hasEbook && !hasAudiobook
    if (!hasEbook && !hasAudiobook && !hasLegacy) return

    // Count files being deleted from book_files for accurate dialog copy.
    const relevantFiles = book.bookFiles?.filter(f => !format || f.format === format) ?? []
    const fileCount = relevantFiles.length

    let label: string
    let pathSummary: string
    if (format === 'ebook' && hasEbook) {
      label = fileCount > 1 ? `${fileCount} ebook files` : 'the ebook file'
      pathSummary = relevantFiles.map(f => f.path).join('\n') || book.ebookFilePath
    } else if (format === 'audiobook' && hasAudiobook) {
      label = 'the audiobook folder'
      pathSummary = book.audiobookFilePath
    } else {
      label = book.mediaType === 'audiobook' ? 'the audiobook folder' : 'this file'
      pathSummary = book.filePath
    }
    if (!window.confirm(`Permanently delete ${label} from disk?\n\n${pathSummary}\n\nAny sibling files with the same name (different format) will also be removed.\n\nThe book record stays; it will flip back to "wanted".`)) return
    setDeletingFile(true)
    setError(null)
    try {
      const params = format ? `?format=${format}` : ''
      const updated = await api.deleteBookFile(book.id, params)
      setBook(updated)
      const h = await api.listHistory({ bookId: book.id }).then(p => p.items).catch(() => events)
      setEvents(h)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bookDetail.deleteFailed'))
    } finally {
      setDeletingFile(false)
    }
  }

  const deleteBook = async () => {
    if (!book) return
    const hasFiles = !!(book.filePath || book.ebookFilePath || book.audiobookFilePath || (book.bookFiles && book.bookFiles.length > 0))
    setDeletingBook(true)
    setError(null)
    try {
      await api.deleteBook(book.id, hasFiles)
      navigate(`/author/${book.authorId}`)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bookDetail.deleteFailed'))
      setDeletingBook(false)
    }
  }

  const enrich = async () => {
    if (!book || !book.asin) return
    setEnriching(true)
    setError(null)
    try {
      const updated = await api.enrichAudiobook(book.id)
      setBook(updated)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bookDetail.enrichFailed'))
    } finally {
      setEnriching(false)
    }
  }

  const toggleExclude = async () => {
    if (!book) return
    setTogglingExclude(true)
    try {
      const updated = await api.toggleExcluded(book.id)
      setBook(updated)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bookDetail.excludeFailed'))
    } finally {
      setTogglingExclude(false)
    }
  }

  const copyPath = async (path: string) => {
    await pathClipboard.copy(path)
  }

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
  if (!book) return <div className="text-slate-600 dark:text-zinc-500">{t('bookDetail.notFound')}</div>

  const mt: MediaType = book.mediaType || 'ebook'
  const isDual = mt === 'both'
  // The format the file section + actions + search apply to.
  const fmt: 'ebook' | 'audiobook' = isDual ? activeFormat : (mt === 'audiobook' ? 'audiobook' : 'ebook')

  // Resolve the file path for the currently active format.
  const activePath = isDual
    ? (fmt === 'ebook' ? book.ebookFilePath : book.audiobookFilePath)
    : (book.filePath || book.ebookFilePath || book.audiobookFilePath)
  const hasActiveFile = !!activePath

  const lang = languageName(book.language)
  const publishedDate = book.releaseDate
    ? new Date(book.releaseDate).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
    : null

  const searchLabel = searching
    ? t('bookDetail.searching')
    : mt === 'audiobook'
      ? t('bookDetail.searchAudiobookIndexers')
      : mt === 'both'
        ? t('bookDetail.searchBothIndexers')
        : t('bookDetail.searchEbookIndexers')

  const downloadHref = isDual
    ? `${BINDERY_BASE}/api/v1/book/${book.id}/file?format=${fmt}`
    : `${BINDERY_BASE}/api/v1/book/${book.id}/file`

  const formatButtonCls = (active: boolean) =>
    `px-3 py-1 text-xs font-medium border ${
      active
        ? 'bg-emerald-600 border-emerald-600 text-white'
        : 'bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300 border-slate-300 dark:border-zinc-700 hover:bg-slate-300 dark:hover:bg-zinc-700'
    }`

  return (
    // One width shared with AuthorDetailPage. These two pages used to disagree
    // (7xl vs 4xl), so author → book collapsed the content by 384px and
    // left-aligned it mid-navigation.
    <div className="max-w-7xl">
      <div className="mb-4 flex items-center gap-3 text-sm">
        <button
          onClick={() => navigate(-1)}
          className="text-emerald-600 dark:text-emerald-400 hover:underline"
        >
          {t('bookDetail.back')}
        </button>
      </div>

      {/* ===== Header: cover + metadata ===== */}
      <div className="flex flex-col sm:flex-row gap-6">
        <div className="w-32 flex-shrink-0">
          {book.imageUrl ? (
            <img src={book.imageUrl} alt={book.title} className="w-full rounded-lg" />
          ) : (
            <CoverPlaceholder id={book.id} title={book.title} className="aspect-[2/3] rounded-lg" />
          )}
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <h2 className="text-2xl font-semibold text-slate-900 dark:text-white">{book.title}</h2>
              {book.author?.authorName && (
                <Link
                  to={`/author/${book.authorId}`}
                  className="text-sm text-emerald-600 dark:text-emerald-400 hover:underline"
                >
                  {book.author.authorName}
                </Link>
              )}
            </div>
            {/* Edit belongs with the metadata it edits, not in the File card,
                which is about the bytes on disk. */}
            <button
              type="button"
              onClick={() => setShowEdit(true)}
              className={`${actionBtnCls} shrink-0`}
              title={t('bookDetail.edit.hint', 'Manually edit metadata; edited fields are locked against refresh')}
            >
              {t('bookDetail.edit.button', 'Edit')}
              {(book.lockedFields?.length ?? 0) > 0 && <span aria-hidden> 🔒</span>}
            </button>
          </div>

          <div className="flex flex-wrap items-center gap-2 mt-3 text-xs">
            {(() => {
              const badge = bookStatusBadge(book.status, book.monitored, t)
              return (
                <span className={`inline-flex items-center px-2 py-0.5 rounded font-medium ${badge.colorClass}`}>
                  {badge.label}
                </span>
              )
            })()}
            {book.excluded && (
              <span className="inline-flex items-center px-2 py-0.5 rounded font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400">
                {t('bookDetail.excludedBadge')}
              </span>
            )}
            {publishedDate && (
              <>
                <span aria-hidden className="text-slate-400 dark:text-zinc-600">·</span>
                <span className="text-slate-600 dark:text-zinc-400">
                  {t('bookDetail.publishedDate', { date: publishedDate })}
                </span>
              </>
            )}
            <span aria-hidden className="text-slate-400 dark:text-zinc-600">·</span>
            {lang ? (
              <span className="text-slate-600 dark:text-zinc-400">{lang}</span>
            ) : (
              <span
                className="inline-flex items-center px-2 py-0.5 rounded font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400"
                title={t('bookDetail.languageUnknownHint')}
              >
                {t('bookDetail.languageUnknown')}
              </span>
            )}
            {book.narrator && (
              <>
                <span aria-hidden className="text-slate-400 dark:text-zinc-600">·</span>
                <span className="text-slate-600 dark:text-zinc-400">
                  {t('bookDetail.narratedBy', { narrator: book.narrator })}
                </span>
              </>
            )}
            {book.durationSeconds ? (
              <>
                <span aria-hidden className="text-slate-400 dark:text-zinc-600">·</span>
                <span className="text-slate-600 dark:text-zinc-400">{formatDuration(book.durationSeconds)}</span>
              </>
            ) : null}
            {series.map(s => (
              <span key={`${s.title}-${s.position}`} className="contents">
                <span aria-hidden className="text-slate-400 dark:text-zinc-600">·</span>
                <span className="text-slate-600 dark:text-zinc-400">
                  {s.position
                    ? t('bookDetail.seriesPosition', {
                        series: s.title,
                        position: s.position,
                        defaultValue: '{{series}} #{{position}}',
                      })
                    : s.title}
                </span>
              </span>
            ))}
            {(() => {
              const src = metadataSourceLink(book.foreignBookId, 'book')
              return src ? (
                <>
                  <span aria-hidden className="text-slate-400 dark:text-zinc-600">·</span>
                  <a
                    href={src.url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-emerald-600 dark:text-emerald-400 hover:underline"
                  >
                    {t('common.viewOnSource', { source: src.label, defaultValue: 'View on {{source}} ↗' })}
                  </a>
                </>
              ) : null
            })()}
          </div>

          {/* Clamped with show more/less, matching AuthorDetailPage. max-w-prose
              keeps the line length readable now the page runs to 7xl. */}
          {book.description && (
            <MarkdownDescription
              text={book.description}
              showMoreLabel={t('bookDetail.description.showMore', 'Show more')}
              showLessLabel={t('bookDetail.description.showLess', 'Show less')}
              className="mt-3 max-w-prose"
            />
          )}

          {/* Media type scopes what the indexer search looks for, so it sits
              with the search action — not in the File card, which is about the
              file(s) actually on disk. */}
          <div className="mt-4 flex flex-wrap items-center gap-3">
            <label htmlFor="book-media-type" className="text-xs text-slate-500 dark:text-zinc-500">
              {t('bookDetail.mediaTypeLabel')}
            </label>
            <select
              id="book-media-type"
              value={mt}
              onChange={e => saveField({ mediaType: e.target.value as MediaType })}
              disabled={saving}
              className="w-fit bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1.5 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600 disabled:opacity-50"
              title={t('bookDetail.mediaTypeHint')}
            >
              <option value="ebook">📖 {t('common.ebook')}</option>
              <option value="audiobook">🎧 {t('common.audiobook')}</option>
              <option value="both">📖🎧 {t('common.both')}</option>
            </select>
            <button
              onClick={runSearch}
              disabled={searching}
              className={`${btn.primary} ${btnSize.md}`}
            >
              <span aria-hidden>🔍</span> {searchLabel}
            </button>
          </div>
        </div>
      </div>

      {error && (
        <div className="mt-6 px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
          {error}
        </div>
      )}

      {/* ===== File section ===== */}
      <Section title={t('bookDetail.fileHeading')}>
        <div>
          <div className="grid grid-cols-[92px_1fr] gap-x-4 gap-y-3 text-sm items-center">
            <span className="text-xs text-slate-500 dark:text-zinc-500">{t('bookDetail.formatLabel')}</span>
            {isDual ? (
              <div className="inline-flex rounded overflow-hidden w-fit" role="group" aria-label={t('bookDetail.formatLabel')}>
                <button
                  type="button"
                  onClick={() => setActiveFormat('ebook')}
                  aria-pressed={fmt === 'ebook'}
                  title={book.ebookFilePath ? t('bookDetail.formatOnDisk') : t('bookDetail.formatNotOnDisk')}
                  className={`${formatButtonCls(fmt === 'ebook')} rounded-l`}
                >
                  <span aria-hidden>📖</span> {t('common.ebook')}
                  {book.ebookFilePath && <span aria-hidden className="ml-1">✓</span>}
                </button>
                <button
                  type="button"
                  onClick={() => setActiveFormat('audiobook')}
                  aria-pressed={fmt === 'audiobook'}
                  title={book.audiobookFilePath ? t('bookDetail.formatOnDisk') : t('bookDetail.formatNotOnDisk')}
                  className={`${formatButtonCls(fmt === 'audiobook')} rounded-r -ml-px`}
                >
                  <span aria-hidden>🎧</span> {t('common.audiobook')}
                  {book.audiobookFilePath && <span aria-hidden className="ml-1">✓</span>}
                </button>
              </div>
            ) : (
              <span className="w-fit">
                <MediaBadge type={fmt} />
              </span>
            )}

            <span className="text-xs text-slate-500 dark:text-zinc-500">{t('bookDetail.pathLabel')}</span>
            <span className="flex items-center gap-2 min-w-0">
              {hasActiveFile ? (
                <>
                  <code
                    className="font-mono text-xs text-slate-500 dark:text-zinc-500 truncate"
                    title={activePath}
                  >
                    {activePath}
                  </code>
                  <button
                    type="button"
                    onClick={() => copyPath(activePath)}
                    className="shrink-0 text-slate-500 dark:text-zinc-400 hover:text-slate-700 dark:hover:text-zinc-200 text-xs border border-slate-300 dark:border-zinc-700 rounded px-1.5 py-0.5"
                    aria-label={t('bookDetail.copyPath')}
                  >
                    <span aria-hidden>⧉</span> {pathClipboard.status === 'copied' ? t('bookDetail.copied') : t('bookDetail.copy')}
                  </button>
                </>
              ) : (
                <span className="text-xs text-slate-500 dark:text-zinc-500">{t('bookDetail.noFile')}</span>
              )}
            </span>
            {pathClipboard.status === 'manual' && (
              <div className="col-start-2">
                <ClipboardManualFallback text={pathClipboard.manualText} />
              </div>
            )}
          </div>

          <div className="mt-4 pt-4 border-t border-slate-200 dark:border-zinc-800 flex flex-wrap items-center gap-2">
            <a
              href={downloadHref}
              className={`${actionBtnCls} ${hasActiveFile ? '' : 'opacity-40 pointer-events-none'}`}
              aria-disabled={!hasActiveFile}
            >
              {t('bookDetail.download')}
            </a>
            <button type="button" onClick={() => setShowRebind(true)} className={actionBtnCls}>
              {t('bookDetail.rebind')}
            </button>
            <button
              type="button"
              onClick={() => setShowFixMatch(true)}
              disabled={!hasActiveFile || deletingFile || deletingBook}
              className={actionBtnCls}
              title={t('bookDetail.fixMatch.hint', 'Move this file to a different book')}
            >
              {t('bookDetail.fixMatch.button', 'Fix match')}
            </button>
            {/* Exclude and Rename files are the rarely-reached ones; keeping
                them visible pushed Delete file out to the row's far edge and
                made the whole row read as equally weighted. */}
            <MoreMenu
              label={t('common.more', 'More')}
              buttonClassName={actionBtnCls}
              items={[
                {
                  label: togglingExclude
                    ? '…'
                    : book.excluded
                      ? t('bookDetail.unexclude')
                      : t('bookDetail.exclude'),
                  title: book.excluded ? t('bookDetail.unexcludeHint') : t('bookDetail.excludeHint'),
                  disabled: togglingExclude,
                  onSelect: toggleExclude,
                },
                {
                  label: t('bookDetail.renameFiles.button', 'Rename files'),
                  title: t('bookDetail.renameFiles.hint', 'Move this book’s files to match the current naming template'),
                  disabled: !(book.ebookFilePath || book.audiobookFilePath) || deletingFile || deletingBook,
                  onSelect: () => setShowRename(true),
                },
              ]}
            />
            {/* Ghost-danger, not solid red. Deleting the file is reversible by
                re-downloading; solid red stays reserved for "Delete book +
                files" in the Danger zone, which is not. */}
            <button
              type="button"
              onClick={() => deleteFile(isDual ? fmt : undefined)}
              disabled={deletingFile || deletingBook || !hasActiveFile}
              className={`ml-auto ${btn.danger} ${btnSize.md}`}
            >
              <span aria-hidden>🗑 </span>
              {deletingFile ? t('bookDetail.deletingFile') : t('bookDetail.deleteFile')}
            </button>
          </div>

          {book.excluded && (
            <p className="mt-2 text-xs text-amber-600 dark:text-amber-400 font-medium">
              {t('bookDetail.excludedFromSearches')}
            </p>
          )}
        </div>
      </Section>

      {/* ===== Audiobook ASIN / enrich (audiobook + dual-format only) ===== */}
      {(mt === 'audiobook' || mt === 'both') && (
        <Section title={t('bookDetail.audiobookHeading')}>
          <div>
            <div className="flex flex-col sm:flex-row sm:items-end gap-2">
              <div className="flex-1">
                <label htmlFor="book-asin" className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">
                  {t('bookDetail.asinLabel')}
                </label>
                <input
                  id="book-asin"
                  value={asinDraft}
                  onChange={e => setAsinDraft(e.target.value.toUpperCase())}
                  placeholder="B08GB58KD5"
                  className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600"
                />
              </div>
              <button
                type="button"
                onClick={() => saveField({ asin: asinDraft })}
                disabled={saving || asinDraft === (book.asin || '')}
                className={actionBtnCls}
              >
                {t('bookDetail.saveAsin')}
              </button>
              <button
                type="button"
                onClick={enrich}
                disabled={!book.asin || enriching}
                className="px-3 py-1.5 bg-indigo-600 hover:bg-indigo-500 text-white rounded text-sm font-medium disabled:opacity-40"
                title={book.asin ? t('bookDetail.enrichHint') : t('bookDetail.enrichHintNoAsin')}
              >
                {enriching ? t('bookDetail.enriching') : t('bookDetail.enrich')}
              </button>
            </div>
          </div>
        </Section>
      )}

      {/* ===== Search results ===== */}
      {results !== null && results.length === 0 && (
        <div className="mt-6 text-center py-6 text-sm text-slate-600 dark:text-zinc-500 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          {hasIndexers === false ? (
            <>
              {t('bookDetail.noIndexers')}{' '}
              <Link to="/settings" className="underline">{t('nav.settings')}</Link>.
            </>
          ) : (
            t('bookDetail.noResults')
          )}
        </div>
      )}

      {searchDebug && (
        <div className="mt-6">
          <SearchDebugPanel
            debug={searchDebug}
            resultCount={results?.length ?? 0}
            defaultOpen={results !== null && results.length === 0}
          />
        </div>
      )}

      {results !== null && results.length > 0 && (
        <div className="mt-6">
          <SearchResultsSection
            results={results}
            bookMediaType={book.mediaType}
            grabbing={grabbing}
            onGrab={grab}
          />
        </div>
      )}

      {/* ===== History section ===== */}
      {events.length > 0 && (
        <Section title={t('bookDetail.historyHeading')} cardClassName="divide-y divide-slate-200 dark:divide-zinc-800">
          <>
            {events.map(ev => (
              <div key={ev.id} className="flex items-center gap-3 px-4 py-2.5 text-sm">
                <span
                  aria-hidden
                  className={`w-2 h-2 rounded-full flex-shrink-0 ${eventDotColors[ev.eventType] ?? 'bg-slate-400 dark:bg-zinc-600'}`}
                />
                <span className="font-medium text-slate-700 dark:text-zinc-300 flex-shrink-0">
                  {t(`bookDetail.event.${ev.eventType}`, ev.eventType)}
                </span>
                <span className="font-mono text-xs text-slate-500 dark:text-zinc-500 truncate min-w-0">
                  {ev.sourceTitle || '—'}
                </span>
                <span className="ml-auto text-xs text-slate-500 dark:text-zinc-500 whitespace-nowrap flex-shrink-0">
                  {new Date(ev.createdAt).toLocaleString()}
                </span>
              </div>
            ))}
          </>
        </Section>
      )}

      {/* ===== Danger zone ===== */}
      <Section
        title={t('bookDetail.dangerHeading')}
        tone="danger"
        cardClassName="p-4 flex flex-col sm:flex-row sm:items-center gap-4"
      >
        <p className="text-sm text-slate-600 dark:text-zinc-400 flex-1">
          {t('bookDetail.dangerBody')}
        </p>
        {/* The only solid-red control on the page. Deleting the book and every
            file on disk is the one genuinely irreversible action here. */}
        <button
          type="button"
          onClick={() => setShowDeleteBook(true)}
          disabled={deletingBook || deletingFile}
          className={`shrink-0 ${btn.dangerSolid} ${btnSize.md}`}
        >
          {t('bookDetail.deleteBook')}
        </button>
      </Section>

      {showEdit && (
        <EditBookModal
          book={book}
          onClose={() => setShowEdit(false)}
          onSaved={updated => setBook(updated)}
        />
      )}
      {showRebind && (
        <RebindModal
          book={book}
          onClose={() => setShowRebind(false)}
          onSuccess={updated => {
            setBook(updated)
            setShowRebind(false)
          }}
        />
      )}

      {showRename && (
        <RenameFilesModal
          scope="book"
          id={book.id}
          label={book.title}
          onClose={() => setShowRename(false)}
          onApplied={() => api.getBook(bookId).then(setBook).catch(() => {})}
        />
      )}

      {showFixMatch && hasActiveFile && (
        <FixMatchModal
          sourceBookId={book.id}
          path={activePath}
          format={fmt}
          onClose={() => setShowFixMatch(false)}
          onReassigned={targetId => {
            setShowFixMatch(false)
            navigate(`/book/${targetId}`)
          }}
        />
      )}

      {showDeleteBook && (
        <ConfirmDialog
          title={t('bookDetail.deleteBook')}
          body={
            <p>
              {t('bookDetail.deleteBookBody1')}{' '}
              <span className="font-medium text-slate-800 dark:text-zinc-200">{book.title}</span>{' '}
              {t('bookDetail.deleteBookBody2')}
            </p>
          }
          acknowledgeLabel={t('bookDetail.deleteAcknowledge')}
          confirmLabel={t('bookDetail.deleteBookConfirm')}
          confirmingLabel={t('bookDetail.deletingBook')}
          confirming={deletingBook}
          onConfirm={deleteBook}
          onClose={() => setShowDeleteBook(false)}
        />
      )}
    </div>
  )
}
