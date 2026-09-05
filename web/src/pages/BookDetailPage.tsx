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
import { metadataSourceLink, providerDisplayName, providerFromBookForeignId } from '../util/metadataSource'
import FixMatchModal from '../components/FixMatchModal'
import EditBookModal from '../components/EditBookModal'
import MetadataLinksMenu from '../components/MetadataLinksMenu'

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

/** Final path segment, for labelling row-level controls. */
function baseName(path: string): string {
  const parts = path.replace(/[/\\]+$/, '').split(/[/\\]/)
  return parts[parts.length - 1] || path
}

/** One on-disk file, labelled by its own format rather than the book's. */
type FileRow = {
  format: 'ebook' | 'audiobook'
  path: string
  sizeBytes?: number
  /** Backed by a book_files row, so `?path=` deregister will resolve it. */
  tracked: boolean
  /**
   * The bare legacy file_path row: no book_files entry, no per-format legacy
   * column, so the format above is only the media-type proxy. The server
   * resolves format-scoped requests against this row by stat-ing the path's
   * shape, which can contradict the proxy (a book declared 'audiobook' whose
   * file_path is an epub), so this row's Download and Delete go format-less —
   * safe, because this row only exists when it is the book's ONLY file.
   */
  legacyUntyped?: boolean
}

// fileRows lists every file registered against the book, each labelled by its
// OWN book_files.format.
//
// This page used to render the file section through book.mediaType, which
// records acquisition intent, not inventory. A book declaring 'audiobook'
// while also holding an epub showed an Audiobook badge next to the epub's
// path, gave the audiobook row no surface at all, and — because a
// non-dual-format book sent no ?format= — offered a Delete button that
// removed BOTH formats while displaying one path.
//
// The fallback covers rows that predate the book_files migration (028) and
// were never re-imported. The legacy single file_path carries no format, so
// its shape has to be inferred; the server stats it (a directory is an
// audiobook bundle, see legacyPathForFormat in internal/api/files.go), which
// the browser cannot do, so the book's media type is the best proxy here.
// Those rows are not `tracked`: deregister resolves paths against book_files
// and 404s for anything absent.
function fileRows(book: Book): FileRow[] {
  if (book.bookFiles && book.bookFiles.length > 0) {
    return book.bookFiles.map(f => ({
      format: f.format,
      path: f.path,
      sizeBytes: f.sizeBytes,
      tracked: true,
    }))
  }

  const rows: FileRow[] = []
  if (book.ebookFilePath) rows.push({ format: 'ebook', path: book.ebookFilePath, tracked: false })
  if (book.audiobookFilePath) rows.push({ format: 'audiobook', path: book.audiobookFilePath, tracked: false })
  if (rows.length === 0 && book.filePath) {
    rows.push({
      format: book.mediaType === 'audiobook' ? 'audiobook' : 'ebook',
      path: book.filePath,
      tracked: false,
      legacyUntyped: true,
    })
  }
  return rows
}

// groupRowsByFormat buckets the rows into the format groups the delete and
// download endpoints actually operate on. Both take ?format=, which covers
// EVERY row of that format (plus, for delete, the same-stem sibling sweep),
// so the group — not the row — is the honest unit for those two actions.
// Ebook first, matching the server's format-less resolution order.
//
// A format the book wants but holds no file for is included as an EMPTY group
// (only while some other file exists — a book with nothing on disk keeps the
// plain "no file" line). The old format switcher marked missing formats with
// "Not downloaded"; dropping the switcher without this lost the only per-format
// answer to "which one am I still waiting on".
function groupRowsByFormat(
  rows: FileRow[],
  mediaType: MediaType,
): { format: 'ebook' | 'audiobook'; rows: FileRow[] }[] {
  const formats: ('ebook' | 'audiobook')[] = ['ebook', 'audiobook']
  const wanted = (f: string) => mediaType === f || mediaType === 'both'
  return formats
    .map(format => ({ format, rows: rows.filter(r => r.format === format) }))
    .filter(g => g.rows.length > 0 || (rows.length > 0 && wanted(g.format)))
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
  const [deregistering, setDeregistering] = useState(false)
  const [deletingBook, setDeletingBook] = useState(false)
  const [togglingExclude, setTogglingExclude] = useState(false)
  const [showRebind, setShowRebind] = useState(false)
  const [showRename, setShowRename] = useState(false)
  const [showEdit, setShowEdit] = useState(false)
  const [showDeleteBook, setShowDeleteBook] = useState(false)
  // The file the Fix match modal will move, chosen per row: a mislabelled
  // book's "active" file was the wrong one to offer.
  const [fixMatchRow, setFixMatchRow] = useState<FileRow | null>(null)
  // The pending disk deletion. `format` undefined means every file on the
  // book; `paths` is exactly what the dialog shows, so the confirmation and
  // the request can never disagree.
  const [deleteTarget, setDeleteTarget] = useState<
    { format?: 'ebook' | 'audiobook'; paths: string[] } | null
  >(null)
  // The pending DB-only deregistration (#1692).
  const [deregisterTarget, setDeregisterTarget] = useState<FileRow | null>(null)
  const pathClipboard = useClipboardCopy()
  // The row whose Copy was clicked — the hook's status is shared across rows.
  const [copiedPath, setCopiedPath] = useState<string | null>(null)
  // Separate from pathClipboard so copying an id does not flip a file row's
  // button to "Copied" (#1707).
  const idClipboard = useClipboardCopy()
  const [copiedId, setCopiedId] = useState<string | null>(null)
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

  // deleteFile runs the confirmed deletion described by `deleteTarget`.
  //
  // The query params are built from the target so the request can never be
  // wider than what the dialog listed. A format-less DELETE enumerates every
  // book_files row for the book, which is only correct for the explicit
  // "delete all files" action.
  const deleteFile = async () => {
    if (!book || !deleteTarget) return
    setDeletingFile(true)
    setError(null)
    try {
      const params = deleteTarget.format ? `?format=${deleteTarget.format}` : ''
      const updated = await api.deleteBookFile(book.id, params)
      setBook(updated)
      setDeleteTarget(null)
      const h = await api.listHistory({ bookId: book.id }).then(p => p.items).catch(() => events)
      setEvents(h)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bookDetail.deleteFailed'))
    } finally {
      setDeletingFile(false)
    }
  }

  // deregisterFile drops one book_files row and touches nothing on disk
  // (#1692) — the tool for a stale path left behind when a file moved.
  const deregisterFile = async () => {
    if (!book || !deregisterTarget) return
    setDeregistering(true)
    setError(null)
    try {
      const updated = await api.deleteBookFile(book.id, `?path=${encodeURIComponent(deregisterTarget.path)}`)
      setBook(updated)
      setDeregisterTarget(null)
      const h = await api.listHistory({ bookId: book.id }).then(p => p.items).catch(() => events)
      setEvents(h)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bookDetail.deleteFailed'))
    } finally {
      setDeregistering(false)
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

  // Remember WHICH path was copied: the hook's status is shared, so without
  // this every row's copy button flipped to "Copied" together.
  const copyPath = async (path: string) => {
    setCopiedPath(path)
    await pathClipboard.copy(path)
  }

  const copyId = async (value: string) => {
    setCopiedId(value)
    await idClipboard.copy(value)
  }

  if (loading) return <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
  if (!book) return <div className="text-slate-600 dark:text-zinc-500">{t('bookDetail.notFound')}</div>

  const mt: MediaType = book.mediaType || 'ebook'

  // The provider identity of this book (#1707), primary row first.
  //
  // books.metadata_provider names the record the page is showing; the identity
  // map from #1705 adds every other provider that has been resolved to the same
  // book. metadata_provider can be empty on rows created before the column
  // existed, so the prefix of the foreign id is the fallback, matching
  // models.BookProviderFromForeignID.
  const identityRows = (() => {
    const primaryId = (book.foreignBookId || '').trim()
    const rows: { provider: string; foreignId: string; primary: boolean; link: ReturnType<typeof metadataSourceLink> }[] = []
    const seen = new Set<string>()
    if (primaryId) {
      rows.push({
        provider: (book.metadataProvider || '').trim() || providerFromBookForeignId(primaryId),
        foreignId: primaryId,
        primary: true,
        link: metadataSourceLink(primaryId, 'book'),
      })
      seen.add(primaryId)
    }
    for (const identifier of book.identifiers ?? []) {
      const id = (identifier.foreignBookId || '').trim()
      if (!id || seen.has(id)) continue
      seen.add(id)
      rows.push({
        provider: (identifier.provider || '').trim() || providerFromBookForeignId(id),
        foreignId: id,
        primary: false,
        link: metadataSourceLink(id, 'book'),
      })
    }
    return rows
  })()
  const sourceLinks = identityRows.flatMap(row => row.link ? [row.link] : [])

  // Display truth is the file inventory, never the declared media type.
  const rows = fileRows(book)
  const groups = groupRowsByFormat(rows, mt)
  const hasAnyFile = rows.length > 0

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

  // Format-scoped whenever the rows carry a real format: the format-less
  // endpoint falls back to the legacy file_path, which on a mislabelled book
  // points at the other format. The one exception is the bare legacy
  // file_path row (see FileRow.legacyUntyped): its displayed format is only a
  // proxy, the server resolves ?format= against the path's on-disk shape, and
  // the two can disagree — so that row's group goes format-less, which is
  // exact because the row only exists when it is the book's only file.
  const groupIsUntyped = (g: { rows: FileRow[] }) => g.rows.some(r => r.legacyUntyped)
  const downloadHref = (group: { format: 'ebook' | 'audiobook'; rows: FileRow[] }) =>
    groupIsUntyped(group)
      ? `${BINDERY_BASE}/api/v1/book/${book.id}/file`
      : `${BINDERY_BASE}/api/v1/book/${book.id}/file?format=${group.format}`

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
            {sourceLinks.length > 0 && (
              <>
                <span aria-hidden className="text-slate-400 dark:text-zinc-600">·</span>
                <MetadataLinksMenu links={sourceLinks} />
              </>
            )}
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
            {/* The declared media type is what search and monitoring hunt for.
                It is labelled as intent because the files below may disagree
                with it, and when they do the files are the truth. */}
            <span className="text-xs text-slate-500 dark:text-zinc-500">{t('bookDetail.wantedFormatLabel')}</span>
            <span className="w-fit">
              <MediaBadge type={mt} />
            </span>
          </div>

          {hasAnyFile ? (
            <div className="mt-4 space-y-4">
              {groups.map(group => (
                <div
                  key={group.format}
                  className="border border-slate-200 dark:border-zinc-800 rounded"
                  data-testid={`file-group-${group.format}`}
                >
                  {group.rows.length === 0 ? (
                    // A format the book wants but has no file for. The old
                    // format switcher carried this ("Not downloaded"); the
                    // list has to say it too or the missing half of a
                    // dual-format book simply vanishes.
                    <div className="px-3 py-2 flex flex-wrap items-center gap-2">
                      <MediaBadge type={group.format} />
                      <span className="text-xs text-slate-500 dark:text-zinc-500">
                        {t('bookDetail.formatNotOnDisk')}
                      </span>
                    </div>
                  ) : (
                    <>
                      <div className="px-3 py-2 flex flex-wrap items-center gap-2 border-b border-slate-200 dark:border-zinc-800">
                        <MediaBadge type={group.format} />
                        <span className="text-xs text-slate-500 dark:text-zinc-500">
                          {t('bookDetail.fileCount', { count: group.rows.length })}
                        </span>
                        <a
                          href={downloadHref(group)}
                          className={`ml-auto ${actionBtnCls}`}
                          title={t('bookDetail.downloadFormatHint')}
                        >
                          {t('bookDetail.download')}
                        </a>
                        {/* Ghost-danger, not solid red. Deleting the file is
                            reversible by re-downloading; solid red stays
                            reserved for "Delete book + files" in the Danger
                            zone, which is not. ?format=-scoped so the sibling
                            format survives — except the bare legacy row,
                            whose proxy format the server may contradict; that
                            one goes format-less, exact because it is the only
                            file (see FileRow.legacyUntyped). */}
                        <button
                          type="button"
                          onClick={() => setDeleteTarget({
                            format: groupIsUntyped(group) ? undefined : group.format,
                            paths: group.rows.map(r => r.path),
                          })}
                          disabled={deletingFile || deregistering || deletingBook}
                          className={`${btn.danger} ${btnSize.md}`}
                        >
                          <span aria-hidden>🗑 </span>
                          {group.rows.length > 1 ? t('bookDetail.deleteFiles') : t('bookDetail.deleteFile')}
                        </button>
                      </div>

                      <ul>
                        {group.rows.map(row => (
                          <li
                            key={row.path}
                            className="px-3 py-2 flex items-center gap-2 min-w-0 border-t first:border-t-0 border-slate-100 dark:border-zinc-900"
                          >
                            <code
                              className="font-mono text-xs text-slate-500 dark:text-zinc-500 truncate"
                              title={row.path}
                            >
                              {row.path}
                            </code>
                            {!!row.sizeBytes && (
                              <span className="shrink-0 text-xs text-slate-400 dark:text-zinc-600">
                                {formatSize(row.sizeBytes)}
                              </span>
                            )}
                            <button
                              type="button"
                              onClick={() => copyPath(row.path)}
                              className="ml-auto shrink-0 text-slate-500 dark:text-zinc-400 hover:text-slate-700 dark:hover:text-zinc-200 text-xs border border-slate-300 dark:border-zinc-700 rounded px-1.5 py-0.5"
                              aria-label={t('bookDetail.copyPath')}
                            >
                              <span aria-hidden>⧉</span>{' '}
                              {pathClipboard.status === 'copied' && copiedPath === row.path
                                ? t('bookDetail.copied')
                                : t('bookDetail.copy')}
                            </button>
                            <MoreMenu
                              label={t('common.more', 'More')}
                              // Distinct name per row: N menus all announced
                              // as "More" are indistinguishable to a screen
                              // reader.
                              ariaLabel={t('bookDetail.rowMore', { name: baseName(row.path) })}
                              buttonClassName="shrink-0 text-xs border border-slate-300 dark:border-zinc-700 rounded px-1.5 py-0.5 text-slate-500 dark:text-zinc-400"
                              items={[
                                {
                                  label: t('bookDetail.fixMatch.button', 'Fix match'),
                                  title: t('bookDetail.fixMatch.hint', 'Move this file to a different book'),
                                  disabled: deletingFile || deregistering || deletingBook,
                                  onSelect: () => setFixMatchRow(row),
                                },
                                {
                                  label: t('bookDetail.deregister.button'),
                                  title: row.tracked
                                    ? t('bookDetail.deregister.hint')
                                    : t('bookDetail.deregister.untracked'),
                                  disabled: !row.tracked || deletingFile || deregistering || deletingBook,
                                  onSelect: () => setDeregisterTarget(row),
                                },
                              ]}
                            />
                          </li>
                        ))}
                      </ul>
                    </>
                  )}
                </div>
              ))}
            </div>
          ) : (
            <p className="mt-4 text-xs text-slate-500 dark:text-zinc-500">{t('bookDetail.noFile')}</p>
          )}

          {pathClipboard.status === 'manual' && (
            <div className="mt-2">
              <ClipboardManualFallback text={pathClipboard.manualText} />
            </div>
          )}

          <div
            className="mt-4 pt-4 border-t border-slate-200 dark:border-zinc-800 flex flex-wrap items-center gap-2"
            data-testid="file-section-actions"
          >
            <button type="button" onClick={() => setShowRebind(true)} className={actionBtnCls}>
              {t('bookDetail.rebind')}
            </button>
            {/* With exactly one file there is nothing to disambiguate, so keep
                Fix match at the surface it used to live on instead of one
                level down in the row menu. Multi-file books pick per row. */}
            {rows.length === 1 && (
              <button
                type="button"
                onClick={() => setFixMatchRow(rows[0])}
                disabled={deletingFile || deregistering || deletingBook}
                className={actionBtnCls}
                title={t('bookDetail.fixMatch.hint', 'Move this file to a different book')}
              >
                {t('bookDetail.fixMatch.button', 'Fix match')}
              </button>
            )}
            {/* Exclude and Rename files are the rarely-reached ones; keeping
                them visible pushed the destructive action out to the row's far
                edge and made the whole row read as equally weighted. */}
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
                  disabled: !hasAnyFile || deletingFile || deregistering || deletingBook,
                  onSelect: () => setShowRename(true),
                },
                {
                  // The only caller allowed to send a format-less DELETE.
                  // Kept out of the per-group controls so the wide delete is
                  // always a deliberate, separately-labelled choice.
                  label: t('bookDetail.deleteAllFiles.button'),
                  title: t('bookDetail.deleteAllFiles.hint'),
                  disabled: !hasAnyFile || deletingFile || deregistering || deletingBook,
                  onSelect: () => setDeleteTarget({ paths: rows.map(r => r.path) }),
                },
              ]}
            />
          </div>

          {book.excluded && (
            <p className="mt-2 text-xs text-amber-600 dark:text-amber-400 font-medium">
              {t('bookDetail.excludedFromSearches')}
            </p>
          )}
        </div>
      </Section>

      {/* ===== Metadata source (#1707) =====
          With OpenLibrary, Hardcover, Google Books and DNB all in play, the
          page never said which record it was showing, so there was no way to
          tell whether a book needed rebinding. The identifier is the point:
          it is selectable and copyable, and the link out only appears for
          providers whose public URL can be built from the stored id. */}
      {identityRows.length > 0 && (
        <Section title={t('bookDetail.metadataSource.heading', 'Metadata source')}>
          <ul className="space-y-2" data-testid="metadata-source-list">
            {identityRows.map(row => (
              <li key={`${row.provider}-${row.foreignId}`} className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-medium min-w-32">
                  {providerDisplayName(row.provider) || t('bookDetail.metadataSource.unknownProvider', 'Unknown provider')}
                </span>
                {row.primary && (
                  <span className="text-xs px-1.5 py-0.5 rounded-full border border-emerald-500/40 text-emerald-700 dark:text-emerald-300">
                    {t('bookDetail.metadataSource.current', 'Current')}
                  </span>
                )}
                <code className="font-mono text-xs text-slate-600 dark:text-zinc-400 select-all break-all">
                  {row.foreignId}
                </code>
                <button
                  type="button"
                  onClick={() => copyId(row.foreignId)}
                  className="shrink-0 text-slate-500 dark:text-zinc-400 hover:text-slate-700 dark:hover:text-zinc-200 text-xs border border-slate-300 dark:border-zinc-700 rounded px-1.5 py-0.5"
                  aria-label={t('bookDetail.metadataSource.copyId', { id: row.foreignId, defaultValue: 'Copy {{id}}' })}
                >
                  <span aria-hidden>⧉</span>{' '}
                  {idClipboard.status === 'copied' && copiedId === row.foreignId
                    ? t('bookDetail.copied')
                    : t('bookDetail.copy')}
                </button>
                {row.link && (
                  <a
                    href={row.link.url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-xs text-emerald-600 dark:text-emerald-400 hover:underline"
                  >
                    {t('common.viewOnSource', { source: row.link.label, defaultValue: 'View on {{source}} ↗' })}
                  </a>
                )}
              </li>
            ))}
          </ul>
          {idClipboard.status === 'manual' && (
            <div className="mt-2">
              <ClipboardManualFallback text={idClipboard.manualText} />
            </div>
          )}
          <p className="mt-3 text-xs text-slate-500 dark:text-zinc-500">
            {t('bookDetail.metadataSource.hint', 'Re-bind this book if the record above is the wrong one.')}
          </p>
        </Section>
      )}

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

      {fixMatchRow && (
        <FixMatchModal
          sourceBookId={book.id}
          path={fixMatchRow.path}
          format={fixMatchRow.format}
          onClose={() => setFixMatchRow(null)}
          onReassigned={targetId => {
            setFixMatchRow(null)
            navigate(`/book/${targetId}`)
          }}
        />
      )}

      {/* The dialog lists every path the request will remove. The old
          window.confirm named a single path derived from the book's media
          type while a format-less DELETE removed every file on the book. */}
      {deleteTarget && (
        <ConfirmDialog
          title={deleteTarget.format
            ? t('bookDetail.deleteFilesTitle', { format: t(`common.${deleteTarget.format}`) })
            : t('bookDetail.deleteAllFiles.button')}
          body={
            <div className="space-y-2">
              <p>{t('bookDetail.deleteFilesBody', { count: deleteTarget.paths.length })}</p>
              <ul className="space-y-1">
                {deleteTarget.paths.map(p => (
                  <li key={p} className="font-mono text-xs break-all text-slate-700 dark:text-zinc-300">
                    {p}
                  </li>
                ))}
              </ul>
              <p>{t('bookDetail.deleteFilesSiblingNote')}</p>
              <p>{t('bookDetail.deleteFilesStatusNote')}</p>
            </div>
          }
          // File deletes are recoverable by re-downloading, so the book
          // delete's "cannot be undone" acknowledgement would overstate.
          acknowledgeLabel={t('bookDetail.deleteFilesAcknowledge')}
          confirmLabel={t('bookDetail.deleteFilesConfirm')}
          confirmingLabel={t('bookDetail.deletingFile')}
          confirming={deletingFile}
          onConfirm={deleteFile}
          onClose={() => setDeleteTarget(null)}
        />
      )}

      {deregisterTarget && (
        <ConfirmDialog
          title={t('bookDetail.deregister.button')}
          body={
            <div className="space-y-2">
              <p>{t('bookDetail.deregister.body')}</p>
              <p className="font-mono text-xs break-all text-slate-700 dark:text-zinc-300">
                {deregisterTarget.path}
              </p>
            </div>
          }
          acknowledgeLabel={t('bookDetail.deregister.acknowledge')}
          confirmLabel={t('bookDetail.deregister.confirm')}
          confirming={deregistering}
          onConfirm={deregisterFile}
          onClose={() => setDeregisterTarget(null)}
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
