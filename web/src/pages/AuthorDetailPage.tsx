import { useEffect, useMemo, useRef, useState, type MouseEvent } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { safeHref } from '../util/safeHref'
import { useConfirmDialog } from '../components/useConfirmDialog'
import { api, ApiError, Author, AuthorAlias, Book, BookBulkAction, MediaType, Series } from '../api/client'
import { usePolling } from '../components/usePolling'
import ViewToggle from '../components/ViewToggle'
import { bookStatusBadge } from '../components/bookStatus'
import MergeAuthorsModal from '../components/MergeAuthorsModal'
import EditAuthorModal from '../components/EditAuthorModal'
import AuthorMetadataLinkModal from '../components/AuthorMetadataLinkModal'
import RenameFilesModal from '../components/RenameFilesModal'
import BulkActionBar, { type BulkAction } from '../components/BulkActionBar'
import { useView } from '../components/useView'
import MarkdownDescription from '../components/MarkdownDescription'
import { authorProviderKey, canLinkAuthorMetadata } from '../util/authorMetadata'
import { metadataRetrievedDate, metadataSourceLink } from '../util/metadataSource'
import { isAutoGrabRefusal } from '../util/autoGrabRefusal'
import { authorSaveError } from '../util/authorSaveError'
import { canonicalLanguage, languageName } from '../util/language'
import { btn, btnSize } from '../components/buttons'
import Switch from '../components/Switch'
import CoverPlaceholder from '../components/CoverPlaceholder'
import Checkbox from '../components/Checkbox'
import BookRating from '../components/BookRating'
import MoreMenu from '../components/MoreMenu'
import Section from '../components/Section'
import AuthorSyncNotice from '../components/AuthorSyncNotice'
import CatalogueReconciliationModal from '../components/CatalogueReconciliationModal'
import DuplicateCandidatesModal from '../components/DuplicateCandidatesModal'
import CoverImage from '../components/CoverImage'
import { errorText } from '../util/errorText'
import FilterBar from '../components/FilterBar'
import { FilterGroup } from '../components/FilterPopover'
import BookStatusLegend from '../components/BookStatusLegend'
import { BELOW_SM, useMediaQuery } from '../components/useMediaQuery'

type MediaFilter = '' | 'ebook' | 'audiobook'
// 'excluded' folds in what used to be a separate "Show excluded" checkbox. It
// is a status like any other from the user's point of view, and as a checkbox
// sitting outside the chip groups it read as belonging to whichever group it
// happened to wrap next to.
// A book still wanted (some requested format is missing) that already has a
// file for another format. It belongs in the library as well as in Wanted.
const isPartiallyOwned = (b: Book): boolean =>
  b.status === 'wanted' && !!(b.ebookFilePath || b.audiobookFilePath)

type StatusFilter = '' | 'wanted' | 'imported' | 'skipped' | 'excluded'
type PublishedFilter = '' | 'released' | 'upcoming'
type MonitoredFilter = '' | 'monitored' | 'unmonitored'
// '' keeps the order the API returned. The title and date keys match
// BooksPage's SortMode; the added-* pair sorts on the row's createdAt.
type SortMode =
  | ''
  | 'title-az' | 'title-za'
  | 'date-new' | 'date-old'
  | 'added-new' | 'added-old'

const SORT_MODES: readonly SortMode[] = [
  '', 'title-az', 'title-za', 'date-new', 'date-old', 'added-new', 'added-old',
]

const STATUS_FILTERS: readonly StatusFilter[] = [
  '', 'wanted', 'imported', 'skipped', 'excluded',
] as const

// English fallbacks for the status options, used as t()'s default value so a
// locale missing these keys still renders words rather than key names.
const STATUS_FALLBACK: Record<Exclude<StatusFilter, ''>, string> = {
  wanted: 'Wanted',
  imported: 'In library',
  skipped: 'Skipped',
  excluded: 'Excluded',
}

const TODAY = new Date().toISOString().slice(0, 10)

function fmtPublishedYear(d?: string): string {
  if (!d) return '—'
  return d.slice(0, 4)
}


function mediaLabel(t: TFunction, mediaType?: Book['mediaType']): string {
  if (mediaType === 'audiobook') return `🎧 ${t('common.audiobook', 'Audiobook')}`
  if (mediaType === 'both') return `📖🎧 ${t('common.both', 'Both')}`
  return `📖 ${t('common.ebook', 'Ebook')}`
}

// Previous/Next navigation (#2548), entirely client-side: AuthorsPage already
// has its current page loaded and ordered, so it hands that over as router
// `state` instead of this page re-fetching it. Not shared as an exported
// type — AuthorsPage builds the same shape independently, same as the
// seriesId router state between AuthorsPage and SeriesPage.
//
// Trade-off: only reaches as far as the loaded list page, and doesn't
// survive a refresh or a direct link (router state is gone either way) —
// Previous/Next just don't render then.
interface AuthorNavState {
  ids: number[]
  index: number
}

// A manual Refresh answers 202 and runs the catalogue sync in the background.
// The page polls the author until the server says the sync has finished, then
// shows the result, instead of re-reading straight away and showing the page
// as it was before the click (#2601).
const REFRESH_POLL_INTERVAL_MS = 2000
const REFRESH_POLL_TIMEOUT_MS = 60000
// A sync already running when the page loads (an add's, typically) is polled
// with no time limit, backing off from 2 s to 10 s: a large catalogue can
// spend minutes in the provider fetch (#3058).
const SYNC_POLL_START_MS = 2000
const SYNC_POLL_MAX_MS = 10000
// Consecutive failed polls or reloads before the page stops watching a sync.
const SYNC_POLL_MAX_FAILURES = 3

export default function AuthorDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const location = useLocation()
  const { t } = useTranslation()
  const { confirm, confirmDialog } = useConfirmDialog()
  const authorId = Number(id)
  const narrow = useMediaQuery(BELOW_SM)

  const [author, setAuthor] = useState<Author | null>(null)
  const [books, setBooks] = useState<Book[]>([])
  const [allAuthors, setAllAuthors] = useState<Author[]>([])
  const [loading, setLoading] = useState(true)
  // Bumping reloadKey reruns the load effect with the filters in force at
  // that moment. A manual Refresh reloads this way once its sync ends, so a
  // filter picked while it waited is honoured (#2601). quietReload keeps the
  // page on screen for that reload instead of flashing the loading state.
  const [reloadKey, setReloadKey] = useState(0)
  const quietReload = useRef(false)
  // Which author a manual Refresh is running for, so moving to another author
  // with Previous/Next does not carry the spinner along (#2601).
  const [refreshingAuthorId, setRefreshingAuthorId] = useState<number | null>(null)
  const refreshing = refreshingAuthorId === authorId
  // One session per author shown. A refresh poll stops writing once the page
  // has moved to another author or unmounted.
  const pageSession = useRef({ active: true })
  useEffect(() => {
    const session = { active: true }
    pageSession.current = session
    return () => {
      session.active = false
    }
  }, [authorId])
  const [searchingWanted, setSearchingWanted] = useState(false)
  const [showMerge, setShowMerge] = useState(false)
  const [showEdit, setShowEdit] = useState(false)
  const [showRename, setShowRename] = useState(false)
  const [showMetadataLink, setShowMetadataLink] = useState(false)
  const [showCatalogueReconciliation, setShowCatalogueReconciliation] = useState(false)
  const [showDuplicateCandidates, setShowDuplicateCandidates] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Sets the photo from an image the user picks, like a book cover (#3017,
  // #3287). The response is the bare author, so only its photo fields are
  // taken: the page keeps its books, stats and series as they are.
  const [uploadingPhoto, setUploadingPhoto] = useState<'upload' | 'remove' | null>(null)
  const changePhoto = async (action: 'upload' | 'remove', file?: File) => {
    if (!author || (action === 'upload' && !file)) return
    setUploadingPhoto(action)
    setError(null)
    try {
      const updated = action === 'upload' ? await api.uploadAuthorPhoto(author.id, file as File) : await api.clearAuthorPhoto(author.id)
      setAuthor(a => (a ? { ...a, imageUrl: updated.imageUrl, imageSource: updated.imageSource } : a))
    } catch (e) {
      setError(authorSaveError(e, t, action === 'upload'
        ? t('authorDetail.photo.uploadFailed', 'Could not upload the photo')
        : t('authorDetail.photo.removeFailed', 'Could not remove the photo')))
    } finally {
      setUploadingPhoto(null)
    }
  }
  // The sync poll stopped trying (#3058): a 404/403, or a few failures in a row.
  const [syncGaveUp, setSyncGaveUp] = useState(false)
  const syncFailures = useRef(0)
  const noteSyncFailure = (err: unknown) => {
    syncFailures.current++
    const status = err instanceof ApiError ? err.status : 0
    if (status === 404 || status === 403 || syncFailures.current >= SYNC_POLL_MAX_FAILURES) setSyncGaveUp(true)
  }

  // Bulk multi-select state (#791). Selection is keyed by book.id and
  // intentionally scoped to the filtered view: hidden books can't be
  // accidentally swept up by select-all.
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [bulkBusy, setBulkBusy] = useState(false)

  const [view, setView] = useView('author-detail', 'grid')

  // Opt-in "group by series" view (#1125). Off by default — the flat list
  // stays the default. Persisted per-page in localStorage alongside the
  // grid/table preference. When on, books are grouped under the series they
  // belong to (position-ordered) with a "Standalone" group for the rest.
  const [groupBySeries, setGroupBySeries] = useState<boolean>(() => {
    try {
      return localStorage.getItem('bindery.group.author-detail.series') === 'true'
    } catch { return false }
  })
  const [authorSeries, setAuthorSeries] = useState<Series[]>([])
  // Tracks whose series `authorSeries` currently holds. The page stays
  // mounted across Previous/Next (only authorId changes), so a plain
  // "already loaded" check on authorSeries.length kept the previous
  // author's series and grouped the new author's books against them.
  // Which author and reload the series were loaded for: a manual Refresh
  // reloads through reloadKey, and its sync can change the series a book is
  // in, so the grouping is fetched again then too.
  const loadedSeries = useRef<{ authorId: number; reloadKey: number } | null>(null)

  useEffect(() => {
    try { localStorage.setItem('bindery.group.author-detail.series', String(groupBySeries)) } catch { /* ignore */ }
  }, [groupBySeries])

  // Lazily load the author's series the first time grouping is switched on, so
  // the default flat view never pays for the extra round trip. Failures fall
  // back to an empty set — every book then lands in the Standalone group.
  useEffect(() => {
    const loaded = loadedSeries.current
    if (!groupBySeries || (loaded?.authorId === authorId && loaded.reloadKey === reloadKey)) return
    // Drop the previous author's series before fetching, so neither the
    // in flight window nor a failed fetch groups these books against them.
    // The same author's stay up while a refresh reload fetches, so the
    // grouping does not flash every book into Standalone.
    if (loaded?.authorId !== authorId) setAuthorSeries([])
    let cancelled = false
    api.listAuthorSeries(authorId)
      .then(s => { if (!cancelled) { setAuthorSeries(s); loadedSeries.current = { authorId, reloadKey } } })
      .catch(() => { /* leave empty: books fall into Standalone */ })
    return () => { cancelled = true }
  }, [groupBySeries, authorId, reloadKey])

  // Filter / sort state — persisted to localStorage under page-scoped keys
  const [typeFilter, setTypeFilter] = useState<MediaFilter>(() => {
    try {
      const v = localStorage.getItem('bindery.filter.author-detail.type')
      if (v === 'ebook' || v === 'audiobook') return v
    } catch { /* ignore */ }
    return ''
  })

  const [statusFilter, setStatusFilter] = useState<StatusFilter>(() => {
    try {
      const v = localStorage.getItem('bindery.filter.author-detail.status')
      if (v && v !== '' && (STATUS_FILTERS as readonly string[]).includes(v)) return v as StatusFilter
    } catch { /* ignore */ }
    return ''
  })

  // Excluded books are omitted by the API unless asked for, so the status
  // filter has to reach the request, not just the client-side filter.
  const showExcluded = statusFilter === 'excluded'

  const [publishedFilter, setPublishedFilter] = useState<PublishedFilter>(() => {
    try {
      const v = localStorage.getItem('bindery.filter.author-detail.published')
      if (v === 'released' || v === 'upcoming') return v
    } catch { /* ignore */ }
    return ''
  })

  const [monitoredFilter, setMonitoredFilter] = useState<MonitoredFilter>(() => {
    try {
      const v = localStorage.getItem('bindery.filter.author-detail.monitored')
      if (v === 'monitored' || v === 'unmonitored') return v
    } catch { /* ignore */ }
    return ''
  })

  // Not persisted: languages differ per author, so a remembered value would
  // silently empty the list on the next author that lacks it.
  const [languageFilter, setLanguageFilter] = useState('')

  const [sort, setSort] = useState<SortMode>(() => {
    try {
      const v = localStorage.getItem('bindery.sort.author-detail')
      if (v && (SORT_MODES as readonly string[]).includes(v)) return v as SortMode
      // The Published-header toggle this replaced stored asc/desc under its own key.
      const legacy = localStorage.getItem('bindery.sort.author-detail.date')
      if (legacy === 'asc') return 'date-old'
      if (legacy === 'desc') return 'date-new'
    } catch { /* ignore */ }
    return ''
  })

  useEffect(() => {
    try { localStorage.setItem('bindery.filter.author-detail.type', typeFilter) } catch { /* ignore */ }
  }, [typeFilter])

  useEffect(() => {
    try { localStorage.setItem('bindery.filter.author-detail.status', statusFilter) } catch { /* ignore */ }
  }, [statusFilter])

  useEffect(() => {
    try { localStorage.setItem('bindery.filter.author-detail.published', publishedFilter) } catch { /* ignore */ }
  }, [publishedFilter])

  useEffect(() => {
    try { localStorage.setItem('bindery.filter.author-detail.monitored', monitoredFilter) } catch { /* ignore */ }
  }, [monitoredFilter])

  useEffect(() => {
    try {
      localStorage.setItem('bindery.sort.author-detail', sort)
      localStorage.removeItem('bindery.sort.author-detail.date')
    } catch { /* ignore */ }
  }, [sort])

  useEffect(() => {
    if (author?.authorName) {
      document.title = `${author.authorName} · Bindery`
      return () => { document.title = 'Bindery' }
    }
  }, [author?.authorName])

  useEffect(() => {
    let cancelled = false
    const quiet = quietReload.current
    quietReload.current = false
    if (!quiet) setLoading(true)
    // The page stays mounted across Previous/Next, so a stale error from the
    // previous author would otherwise still be showing under the new one.
    setError(null)
    // listAllBooks pages through the server until the author's complete
    // catalogue is loaded — a plain listBooks call silently capped the list at
    // the server default of 100, corrupting counts/filters/select-all (#1467).
    Promise.all([
      api.getAuthor(authorId),
      api.listAllBooks({ authorId, includeExcluded: showExcluded }),
    ])
      .then(([a, bs]) => {
        if (cancelled) return
        setAuthor(a)
        setBooks(bs)
        syncFailures.current = 0
        setSyncGaveUp(false)
      })
      .catch(err => {
        setError(errorText(err, t('authorDetail.loadFailed', 'Failed to load')))
        if (!cancelled) noteSyncFailure(err)
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [authorId, showExcluded, reloadKey]) // eslint-disable-line react-hooks/exhaustive-deps

  // An author whose catalogue sync is running when the page loads, straight
  // after an add or after a Refresh poll gave up, is polled until the sync
  // ends and then reloaded, so the page does not settle on "no books" while
  // the catalogue is still being fetched (#3058). A manual Refresh runs its
  // own poll, so this one stands down meanwhile. The poll keeps running until
  // a reload has actually succeeded (a failed reload leaves the author marked
  // as syncing, so the next poll reloads again), and gives up on a 404/403 or
  // after a few failures in a row, so a permanent error ends in the error
  // state rather than a spinner that never clears.
  const syncing = !!author?.syncInProgress && author.id === authorId && !refreshing && !syncGaveUp
  const [pollDelay, setPollDelay] = useState(SYNC_POLL_START_MS)
  const backOff = () => setPollDelay(d => Math.min(d * 1.5, SYNC_POLL_MAX_MS))
  usePolling(() => {
    const session = pageSession.current
    api.getAuthor(authorId)
      .then(latest => {
        if (!session.active) return
        if (!latest.syncInProgress) {
          quietReload.current = true
          setReloadKey(k => k + 1)
          return
        }
        syncFailures.current = 0
        backOff()
      })
      .catch(err => {
        if (!session.active) return
        noteSyncFailure(err)
        backOff()
      })
  }, pollDelay, syncing)

  // Validated against authorId: stale state (browser back/forward) or no
  // state at all (opened from elsewhere) must read as "no nav info", not
  // point at the wrong neighbour.
  const navState = (() => {
    const s = location.state as AuthorNavState | null
    if (s && Array.isArray(s.ids) && typeof s.index === 'number' && s.ids[s.index] === authorId) {
      return s
    }
    return null
  })()
  const prevId = navState && navState.index > 0 ? navState.ids[navState.index - 1] : null
  const nextId = navState && navState.index < navState.ids.length - 1 ? navState.ids[navState.index + 1] : null

  useEffect(() => {
    const params = new URLSearchParams(location.search)
    if (params.get('linkMetadata') !== '1') {
      return
    }
    setShowMetadataLink(true)
    params.delete('linkMetadata')
    const search = params.toString()
    navigate(
      {
        pathname: location.pathname,
        search: search ? `?${search}` : '',
        hash: location.hash,
      },
      { replace: true },
    )
  }, [location.hash, location.pathname, location.search, navigate])

  const handleRefresh = async () => {
    if (!author) return
    const session = pageSession.current
    const refreshedId = author.id
    setRefreshingAuthorId(refreshedId)
    // Reports whether this click started a sync. 409 means a sync for the
    // author is already running.
    const startRefresh = async () => {
      try {
        await api.refreshAuthor(refreshedId)
        return true
      } catch (e) {
        if (e instanceof ApiError && e.status === 409) return false
        throw e
      }
    }
    // Polls until the author's sync ends: 'done', 'timeout' after a minute,
    // or 'gone' once the page has moved to another author.
    const waitForSync = async (): Promise<'done' | 'timeout' | 'gone'> => {
      const deadline = Date.now() + REFRESH_POLL_TIMEOUT_MS
      while (Date.now() < deadline) {
        await new Promise(resolve => setTimeout(resolve, REFRESH_POLL_INTERVAL_MS))
        if (!session.active) return 'gone'
        const latest = await api.getAuthor(refreshedId)
        if (!session.active) return 'gone'
        if (!latest.syncInProgress) return 'done'
      }
      return 'timeout'
    }
    try {
      const started = await startRefresh()
      let waited = await waitForSync()
      if (waited === 'gone') return
      // A 409 means the running sync was not this click's: a scheduled, bulk,
      // Refresh all or add sync, and none of those read past the metadata
      // cache. Once it ends, ask once more so the page shows current data. A
      // second 409 means yet another sync started meanwhile; show what is
      // there rather than chase it.
      if (!started && waited === 'done' && await startRefresh()) {
        waited = await waitForSync()
        if (waited === 'gone') return
      }
      if (!session.active) return
      // Reload through the load effect, which reads the filters in force now
      // rather than the ones this click saw.
      quietReload.current = true
      setReloadKey(k => k + 1)
    } catch (e) {
      if (session.active) setError(errorText(e, t('authorDetail.refreshFailed', 'Refresh failed')))
    } finally {
      setRefreshingAuthorId(current => (current === refreshedId ? null : current))
    }
  }

  const handleToggleMonitored = async () => {
    if (!author) return
    try {
      await api.updateAuthor(author.id, { monitored: !author.monitored })
      setAuthor({ ...author, monitored: !author.monitored })
    } catch (e) {
      setError(authorSaveError(e, t, t('authorDetail.updateFailed', 'Update failed')))
    }
  }

  const handleSearchWanted = async () => {
    if (!author) return
    const searchableWantedCount = books.filter(b => b.status === 'wanted' && b.monitored && !b.excluded).length
    if (searchableWantedCount === 0) return
    setSearchingWanted(true)
    setError(null)
    try {
      const res = await api.searchAuthorWanted(author.id)
      // A refusal because automatic grabbing is off is not a failure of this
      // author's search, it is a setting the user has to change, so it gets
      // its own message naming the setting instead of a raw server string
      // (#2669).
      if (isAutoGrabRefusal(res)) {
        setError(t('search.autoGrabDisabled'))
        return
      }
      const item = res.results[String(author.id)]
      if (item && !item.ok) {
        throw new Error(item.error || t('authorDetail.searchFailed', 'Search failed'))
      }
    } catch (e) {
      setError(errorText(e, t('authorDetail.searchFailed', 'Search failed')))
    } finally {
      setSearchingWanted(false)
    }
  }

  const handleDelete = async () => {
    if (!author) return
    const withFiles = books.filter(b => b.filePath)
    if (!await confirm({
      title: t('authorDetail.deleteTitle'),
      body: withFiles.length > 0
        ? t('authorDetail.deleteWithFilesConfirm', { author: author.authorName, count: books.length, withFiles: withFiles.length })
        : t('authorDetail.deleteConfirm', { author: author.authorName, count: books.length }),
      confirmLabel: t('common.delete'),
      // Deleting files off disk is irreversible, so that variant is gated.
      acknowledgeLabel: withFiles.length > 0 ? t('authorDetail.deleteFilesAcknowledge') : undefined,
    })) return
    try {
      await api.deleteAuthor(author.id, withFiles.length > 0)
      navigate('/')
    } catch (e) {
      setError(errorText(e, t('authorDetail.deleteFailed', 'Delete failed')))
    }
  }

  const handleDeleteAlias = async (alias: AuthorAlias) => {
    if (!author) return
    if (!await confirm({
      title: t('common.confirmTitle'),
      body: t('authorDetail.aliases.removeConfirm', { name: alias.name, author: author.authorName }),
      confirmLabel: t('common.remove'),
    })) return
    setError(null)
    try {
      await api.deleteAuthorAlias(author.id, alias.id)
      setAuthor(current => current ? {
        ...current,
        aliases: (current.aliases ?? []).filter(a => a.id !== alias.id),
      } : current)
    } catch (e) {
      setError(t('authorDetail.aliases.removeFailed', {
        error: e instanceof Error ? e.message : String(e),
      }))
    }
  }

  const toggleSelect = (bookId: number) => {
    setSelected(prev => {
      const next = new Set(prev)
      if (next.has(bookId)) next.delete(bookId)
      else next.add(bookId)
      return next
    })
  }

  const clearSelection = () => setSelected(new Set())

  // reloadBooks refetches the author's books without clobbering loading state —
  // used after a bulk action to reflect changes (e.g. exclude hides rows
  // unless showExcluded is on; delete removes them outright).
  const reloadBooks = async () => {
    try {
      const items = await api.listAllBooks({ authorId, includeExcluded: showExcluded })
      setBooks(items)
    } catch (e) {
      setError(errorText(e, t('authorDetail.reloadFailed', 'Reload failed')))
    }
  }

  // runBulk routes a multi-select action through the existing /book/bulk
  // endpoint. The handler returns 200 with per-id outcomes even when some
  // rows fail (stale IDs, missing books) — surface the first error inline
  // so the user knows partial success happened without burying it.
  const runBulk = async (action: BookBulkAction, actionLabel: string, confirmMsg?: string, mediaType?: MediaType) => {
    if (selected.size === 0) return
    if (confirmMsg && !await confirm({ title: t('common.confirmTitle'), body: confirmMsg })) return
    setBulkBusy(true)
    setError(null)
    try {
      const ids = Array.from(selected)
      const res = await api.bulkActionBooks(ids, action, mediaType)
      // Same refusal as the "Search wanted" button: say what did not happen
      // and keep the selection so the user can retry after flipping the
      // setting (#2669).
      if (isAutoGrabRefusal(res)) {
        setError(t('search.autoGrabDisabled'))
        return
      }
      let okCount = 0
      let firstError = ''
      const succeeded = new Set<number>()
      for (const id of ids) {
        const r = res.results[String(id)]
        if (r?.ok) {
          okCount++
          succeeded.add(id)
        } else if (!firstError) {
          firstError = r?.error || t('authorDetail.unknownError', 'unknown error')
        }
      }
      if (okCount < ids.length) {
        setError(t('authorDetail.bulk.partial', {
          action: actionLabel,
          ok: okCount,
          total: ids.length,
          error: firstError,
        }))
      }
      // The selection survives the action so a second one can run on the
      // same books; Clear is the way out (#2457). Books that leave the view
      // (excluded, deleted, filtered out by the change) are dropped by the
      // visibleIds effect once the reload lands. Deleted ids are dropped here
      // as well, so a failed reload cannot leave a gone book selected for the
      // next action. Ids that failed to delete stay selected for a retry.
      if (action === 'delete' && succeeded.size > 0) {
        setSelected(prev => new Set([...prev].filter(id => !succeeded.has(id))))
      }
      await reloadBooks()
    } catch (e) {
      setError(t('authorDetail.bulk.failed', {
        action: actionLabel,
        error: e instanceof Error ? e.message : String(e),
      }))
    } finally {
      setBulkBusy(false)
    }
  }

  // The Language select only appears when this author's books span more than
  // one language; a stale pick for a language no longer present is ignored.
  // Codes are folded first, so "en" and "eng" rows are one English option.
  const languages = useMemo(
    () => Array.from(new Set(books.map(b => canonicalLanguage(b.language)).filter(Boolean)))
      .sort((a, b) => (languageName(a) ?? a).localeCompare(languageName(b) ?? b)),
    [books],
  )
  const activeLanguage = languages.length > 1 && languages.includes(languageFilter) ? languageFilter : ''

  const filteredBooks = useMemo(() => {
    let list = books
    // A 'both' book carries the selected format too, so it matches either
    // type chip — an exact mediaType comparison made dual-format books vanish
    // from both Type: Ebook and Type: Audiobook (#1406).
    if (typeFilter) {
      list = list.filter(b => {
        const mt = b.mediaType || 'ebook'
        return mt === typeFilter || mt === 'both'
      })
    }
    if (statusFilter) {
      // For a 'both' book under an active type filter, judge the selected
      // format rather than the aggregate status: the aggregate stays 'wanted'
      // until BOTH formats are on disk, which hid a book whose ebook was
      // already imported from Type: Ebook + Status: Imported (#1406). The
      // per-format file path is the format-scoped truth for 'imported'; when
      // the selected format has no file yet, the aggregate still supplies
      // 'wanted' and the in-flight states (which have no per-format field).
      const statusOf = (b: Book): string => {
        // With no type chip, any tracked file puts a wanted book in the library
        // view as well (#3132). Only the Imported filter remaps it: the book
        // stays in Wanted because a requested format is still missing.
        if (!typeFilter && statusFilter === 'imported' && isPartiallyOwned(b)) return 'imported'
        if (!typeFilter || (b.mediaType || 'ebook') !== 'both') return b.status
        const path = typeFilter === 'ebook' ? b.ebookFilePath : b.audiobookFilePath
        return path ? 'imported' : b.status
      }
      // "Wanted" means genuinely wanted: monitored AND status=wanted. An
      // unmonitored book can carry a stale `wanted` status (#1173) but is not
      // actually wanted, so it must be excluded — mirroring the backend's
      // BookListFilter ("wanted" additionally requires monitored=1) and the
      // monitored-aware status badge.
      list = statusFilter === 'wanted'
        ? list.filter(b => statusOf(b) === 'wanted' && b.monitored)
        // 'excluded' is a flag on the book rather than a value of `status`, so
        // it can't go through statusOf. The request already asked for excluded
        // books; this narrows the view to just them.
        : statusFilter === 'excluded'
          ? list.filter(b => b.excluded)
          : list.filter(b => statusOf(b) === statusFilter)
    }
    if (publishedFilter === 'released') {
      list = list.filter(b => !b.releaseDate || b.releaseDate.slice(0, 10) <= TODAY)
    } else if (publishedFilter === 'upcoming') {
      list = list.filter(b => !!b.releaseDate && b.releaseDate.slice(0, 10) > TODAY)
    }
    if (monitoredFilter) {
      list = list.filter(b => b.monitored === (monitoredFilter === 'monitored'))
    }
    if (activeLanguage) {
      list = list.filter(b => canonicalLanguage(b.language) === activeLanguage)
    }
    if (sort === 'title-az' || sort === 'title-za') {
      const dir = sort === 'title-az' ? 1 : -1
      list = [...list].sort((a, b) =>
        dir * (a.sortTitle || a.title).localeCompare(b.sortTitle || b.title, undefined, { numeric: true, sensitivity: 'base' }))
    } else if (sort) {
      const stamp = (b: Book) => {
        const raw = sort === 'added-new' || sort === 'added-old' ? b.createdAt : b.releaseDate
        const ms = raw ? new Date(raw).getTime() : NaN
        return Number.isNaN(ms) ? null : ms
      }
      const dir = sort === 'date-old' || sort === 'added-old' ? 1 : -1
      // Undated books go last in either direction rather than sorting as 1970.
      list = [...list].sort((a, b) => {
        const sa = stamp(a)
        const sb = stamp(b)
        if (sa === null || sb === null) return sa === sb ? 0 : sa === null ? 1 : -1
        return dir * (sa - sb)
      })
    }
    return list
  }, [books, typeFilter, statusFilter, publishedFilter, monitoredFilter, activeLanguage, sort])

  // Build the grouped-by-series sections from the author's series membership
  // joined against the filtered book set (#1125). Each section lists its books
  // ordered by position-in-series; any filtered book not in a series collects
  // in a trailing "Standalone" group. A book in multiple series appears under
  // each — matching how the series page presents membership.
  const seriesGroups = useMemo(() => {
    const byId = new Map(filteredBooks.map(b => [b.id, b]))
    const grouped: { key: string; seriesId?: number; title: string; books: Book[] }[] = []
    const placed = new Set<number>()
    for (const series of authorSeries) {
      const entries = [...(series.books ?? [])].sort((a, b) => {
        const posA = parseFloat(a.positionInSeries) || 0
        const posB = parseFloat(b.positionInSeries) || 0
        return posA - posB
      })
      const books: Book[] = []
      for (const entry of entries) {
        const book = byId.get(entry.bookId)
        if (!book) continue
        books.push(book)
        placed.add(book.id)
      }
      if (books.length > 0) grouped.push({ key: `series-${series.id}`, seriesId: series.id, title: series.title, books })
    }
    const standalone = filteredBooks.filter(b => !placed.has(b.id))
    if (standalone.length > 0) grouped.push({ key: 'standalone', title: t('authorDetail.standalone', 'Standalone'), books: standalone })
    return grouped
  }, [authorSeries, filteredBooks, t])

  // Drop any selected IDs that are no longer in the filtered view so the
  // bulk bar count never lies about what's about to be acted on.
  const visibleIds = useMemo(() => new Set(filteredBooks.map(b => b.id)), [filteredBooks])
  useEffect(() => {
    setSelected(prev => {
      let changed = false
      const next = new Set<number>()
      for (const id of prev) {
        if (visibleIds.has(id)) next.add(id)
        else changed = true
      }
      return changed ? next : prev
    })
  }, [visibleIds])

  const allVisibleSelected = filteredBooks.length > 0 && filteredBooks.every(b => selected.has(b.id))
  const someVisibleSelected = filteredBooks.some(b => selected.has(b.id)) && !allVisibleSelected

  const selectAllVisible = () => setSelected(new Set(filteredBooks.map(b => b.id)))

  if (loading) return <div className="text-fg-muted">{t('common.loading', 'Loading…')}</div>
  if (!author) return <div className="text-fg-muted">{t('authorDetail.notFound', 'Author not found')}</div>

  const searchableWantedCount = books.filter(b => b.status === 'wanted' && b.monitored && !b.excluded).length
  // Relinking is valid for every author — the backend accepts any record and the
  // candidate search deliberately does not merge same-person records — so the
  // action is always offered. The record's fullness only picks the wording below.
  const showMetadataLinkAction = true
  const metadataLinkLabel = canLinkAuthorMetadata(author)
    ? t('authorMetadataLink.actionLink', 'Link metadata')
    : t('authorMetadataLink.actionFindBetter', 'Find better metadata')
  const counts = {
    total: books.length,
    imported: books.filter(b => b.status === 'imported' || isPartiallyOwned(b)).length,
    wanted: searchableWantedCount,
    audiobook: books.filter(b => b.mediaType === 'audiobook').length,
  }

  // Column-header sorting, mirroring BooksPage: first click ascending, a second
  // click on the same column flips to descending.
  const toggleSort = (asc: SortMode, desc: SortMode) =>
    setSort(prev => prev === asc ? desc : asc)

  const sortArrow = (asc: SortMode, desc: SortMode) =>
    sort === asc ? ' ▲' : sort === desc ? ' ▼' : ''

  // This author's filtered books, in order — handed to BookDetailPage as
  // router state (#2548) for Previous/Next; see BookNavState there. #2548
  // asks for the order the user sees, so when Group by series is on the
  // chain follows seriesGroups (each series section top-to-bottom, then
  // Standalone) instead of filteredBooks' load order. A book in more than
  // one series appears in more than one group's `books`; Set dedupes to its
  // first occurrence while preserving insertion order, so it counts once.
  const bookIds = groupBySeries
    ? Array.from(new Set(seriesGroups.flatMap(group => group.books.map(b => b.id))))
    : filteredBooks.map(b => b.id)
  // Looked up once per render rather than bookIds.indexOf(book.id) inside
  // bookNavState — that would be an O(n) scan per row, O(n²) across a page.
  const bookIndexById = new Map(bookIds.map((id, i) => [id, i]))
  // hopDepth: 1 — this is the first hop into a book detail page from a list,
  // not a further Previous/Next chain hop; see BookNavState in
  // BookDetailPage.tsx for how Back uses it to skip the whole chain.
  const bookNavState = (book: Book) => ({ ids: bookIds, index: bookIndexById.get(book.id) ?? -1, hopDepth: 1 })

  // While a selection exists, a plain click anywhere on a card or row adds or
  // removes that book instead of opening it, so a click that misses the small
  // checkbox no longer navigates away and loses the selection (#2456). With
  // nothing selected the click opens the book as before. Modified clicks
  // (ctrl/cmd/shift/alt) pass through so open in a new tab keeps working;
  // middle click never reaches onClick. Returns true when it handled the click.
  const selectingClick = (e: MouseEvent, book: Book): boolean => {
    if (selected.size === 0) return false
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return false
    e.preventDefault()
    toggleSelect(book.id)
    return true
  }

  // Render helpers shared by the flat and grouped-by-series (#1125) layouts so
  // each series section and the standalone group reuse the exact same table
  // rows / grid cards instead of duplicating the markup.
  const renderTableRows = (list: Book[]) =>
    list.map(book => (
      <tr
        key={book.id}
        className={`${selected.has(book.id) ? 'bg-emerald-500/10 dark:bg-emerald-500/10' : 'bg-slate-100/50 dark:bg-zinc-900/50'} hover:bg-slate-200/50 dark:hover:bg-zinc-800/50 cursor-pointer`}
        // Client-side, matching the <Link> in this same row. The row used to do
        // a full page reload while the link inside it routed client-side, so
        // one row had two different navigation behaviours.
        // The bare row is not a link, so it has no new-tab gesture to keep:
        // while selecting, every click on it toggles (#2456).
        onClick={() => {
          if (selected.size > 0) toggleSelect(book.id)
          else navigate(`/book/${book.id}`, { state: bookNavState(book) })
        }}
      >
        <td className="px-3 py-2 w-10 align-middle" onClick={e => e.stopPropagation()}>
          {/* The label fills the cell, so the whole cell toggles the box: the
              16px box alone is hard to tap. */}
          <label className="flex items-center -mx-3 -my-2 px-3 py-2 cursor-pointer pointer-coarse:min-h-11">
            <Checkbox
              checked={selected.has(book.id)}
              onChange={() => toggleSelect(book.id)}
              label={`Select ${book.title}`}
            />
          </label>
        </td>
        <td className="px-3 py-2 align-middle">
          <Link
            to={`/book/${book.id}`}
            state={bookNavState(book)}
            className="flex items-center gap-2 min-w-0"
            onClick={e => {
              e.stopPropagation()
              selectingClick(e, book)
            }}
          >
            <CoverImage loading="lazy" decoding="async" src={book.imageUrl} alt="" className="w-6 h-9 object-cover rounded flex-shrink-0"
              fallback={
                <CoverPlaceholder
                  id={book.id}
                  title={book.title}
                  size="xs"
                  className="w-6 h-9 rounded flex-shrink-0"
                />
              }
            />
            <span className="min-w-0 flex-1">
              <span className="block text-slate-800 dark:text-zinc-200 truncate">{book.title}</span>
              <span className="mt-1 flex flex-wrap items-center gap-1 sm:hidden">
                {(() => {
                  const badge = bookStatusBadge(book.status, book.monitored, t)
                  return (
                    <span className={`inline-block px-1.5 py-0.5 rounded text-[10px] font-medium ${badge.colorClass}`}>
                      {badge.label}
                    </span>
                  )
                })()}
                <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-medium bg-slate-200 dark:bg-zinc-800 text-fg-muted">
                  {mediaLabel(t, book.mediaType)}
                </span>
                <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-medium bg-slate-200 dark:bg-zinc-800 text-fg-muted">
                  {fmtPublishedYear(book.releaseDate)}
                </span>
                {book.excluded && (
                  <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400">
                    {t('books.excluded', 'Excluded')}
                  </span>
                )}
              </span>
            </span>
          </Link>
        </td>
        <td className="hidden sm:table-cell px-3 py-2 text-fg-muted whitespace-nowrap align-middle">{fmtPublishedYear(book.releaseDate)}</td>
        <td className="hidden md:table-cell px-3 py-2 text-fg-muted whitespace-nowrap align-middle">
          {book.averageRating && book.averageRating > 0 ? <BookRating book={book} /> : '—'}
        </td>
        <td className="hidden sm:table-cell px-3 py-2 text-xs whitespace-nowrap align-middle">
          {mediaLabel(t, book.mediaType)}
        </td>
        <td className="hidden sm:table-cell px-3 py-2 whitespace-nowrap align-middle">
          {(() => {
            const badge = bookStatusBadge(book.status, book.monitored, t)
            return (
              <span className={`inline-block px-2 py-0.5 rounded text-[10px] font-medium ${badge.colorClass}`}>
                {badge.label}
              </span>
            )
          })()}
          {book.excluded && (
            <span className="inline-block ml-1 px-2 py-0.5 rounded text-[10px] font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400">
              {t('books.excluded', 'Excluded')}
            </span>
          )}
        </td>
      </tr>
    ))

  const renderTable = (list: Book[], withHeaderControls: boolean) => (
    <div className="border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
      <div className="overflow-x-auto">
        <table className="w-full table-fixed text-sm">
          <thead>
            <tr className="bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
              <th className="w-10 px-3 py-2">
                {withHeaderControls && (
                  <Checkbox
                    checked={allVisibleSelected}
                    indeterminate={someVisibleSelected}
                    onChange={select => (select ? selectAllVisible() : clearSelection())}
                    label={t('common.selectAllPage')}
                  />
                )}
              </th>
              <th
                className="w-full sm:w-[46%] text-left px-3 py-2 text-xs font-medium text-fg-muted uppercase cursor-pointer select-none hover:text-slate-900 dark:hover:text-white"
                onClick={() => toggleSort('title-az', 'title-za')}
                title={t('authorDetail.sortByTitle', 'Sort by title')}
              >
                {t('books.colTitle', 'Title')}{sortArrow('title-az', 'title-za')}
              </th>
              <th
                className="hidden sm:table-cell sm:w-28 text-left px-3 py-2 text-xs font-medium text-fg-muted uppercase cursor-pointer select-none hover:text-slate-900 dark:hover:text-white whitespace-nowrap"
                onClick={() => toggleSort('date-old', 'date-new')}
                title={t('authorDetail.sortByDate', 'Sort by publication date')}
              >
                {t('authorDetail.colPublished', 'Published')}{sortArrow('date-old', 'date-new')}
              </th>
              <th className="hidden md:table-cell md:w-24 text-left px-3 py-2 text-xs font-medium text-fg-muted uppercase">{t('books.colRating')}</th>
              <th className="hidden sm:table-cell sm:w-36 text-left px-3 py-2 text-xs font-medium text-fg-muted uppercase">{t('books.colType', 'Type')}</th>
              <th className="hidden sm:table-cell sm:w-36 text-left px-3 py-2 text-xs font-medium text-fg-muted uppercase">{t('books.colStatus', 'Status')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-200 dark:divide-zinc-800">
            {renderTableRows(list)}
          </tbody>
        </table>
      </div>
    </div>
  )

  const renderGrid = (list: Book[]) => (
    <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 gap-4">
      {list.map(book => (
        <div
          key={book.id}
          data-focus-card
          className={`relative border rounded-lg bg-slate-100 dark:bg-zinc-900 overflow-hidden group transition-colors ${selected.has(book.id) ? 'border-emerald-500' : 'border-slate-200 dark:border-zinc-800 hover:border-emerald-500'}`}
        >
          {/* The label is the tap area: 32px round the box, 44px on a touch
              screen, where the box sits further in to make room. */}
          <label
            className="absolute top-0 left-0 z-10 flex p-2 pointer-coarse:p-3.5 cursor-pointer"
            onClick={e => e.stopPropagation()}
          >
            <Checkbox
              checked={selected.has(book.id)}
              onChange={() => toggleSelect(book.id)}
              className={selected.has(book.id) ? '' : 'bg-white/80 dark:bg-zinc-900/80'}
              label={`Select ${book.title}`}
            />
          </label>
          <Link
            to={`/book/${book.id}`}
            state={bookNavState(book)}
            className="block focus-visible:outline-hidden"
            onClick={e => { selectingClick(e, book) }}
          >
            <div className="aspect-[2/3] bg-slate-200 dark:bg-zinc-800 relative">
              <CoverImage loading="lazy" decoding="async" src={book.imageUrl} alt={book.title} className="w-full h-full object-cover"
                fallback={
                  <CoverPlaceholder id={book.id} title={book.title} size="sm" show="initials" className="w-full h-full" />
                }
              />
            </div>
            {/* Fixed height. The badge row and the year are both variable, so
                cards in the same row used to end at different heights and the
                grid went ragged. */}
            <div className="p-2 h-[72px] flex flex-col">
              <h4 className="text-xs font-medium truncate" title={book.title}>{book.title}</h4>
              {/* nowrap, not wrap: at a fixed height a wrapped second badge row
                  would be clipped through the middle of a badge. Overflowing
                  sideways instead cuts a badge at the card edge, which at least
                  reads as "there is more here". Three badges plus a long
                  translated status label is the case that overflows. */}
              <div className="flex items-center gap-1 mt-1 flex-nowrap overflow-hidden">
                {(() => {
                  const badge = bookStatusBadge(book.status, book.monitored, t)
                  return (
                    <span className={`shrink-0 px-1.5 py-0.5 rounded text-[10px] font-medium ${badge.colorClass}`}>
                      {badge.label}
                    </span>
                  )
                })()}
                {book.mediaType === 'audiobook' && (
                  <span className="shrink-0 px-1.5 py-0.5 rounded text-[10px] font-medium bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300">🎧 {t('authorDetail.audioBadge', 'Audio')}</span>
                )}
                {book.excluded && (
                  <span className="shrink-0 px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-500/20 text-amber-700 dark:text-amber-400">{t('books.excluded', 'Excluded')}</span>
                )}
              </div>
              {/* No `book.releaseDate &&` guard: fmtPublishedYear already
                  returns an em dash for a missing date, so the conditional only
                  made the row appear and disappear between cards. */}
              <p className="flex items-center justify-between gap-2 text-[10px] text-fg-muted mt-auto">
                <span>{fmtPublishedYear(book.releaseDate)}</span>
                <BookRating book={book} />
              </p>
            </div>
          </Link>
        </div>
      ))}
    </div>
  )

  const renderBooks = (list: Book[], withHeaderControls = true) =>
    view === 'table' ? renderTable(list, withHeaderControls) : renderGrid(list)

  // The Excluded view lists only excluded books, so there the one exclusion
  // action is the way back (#3454). It leads that view's bar so the cap on
  // inline actions does not push it behind More.
  const exclusionAction: BulkAction = statusFilter === 'excluded'
    ? {
        label: t('authorDetail.bulk.unexclude', 'Un-exclude'),
        onClick: () => runBulk('unexclude', t('authorDetail.bulk.unexclude', 'Un-exclude')),
      }
    : {
        label: t('authorDetail.bulk.exclude'),
        variant: 'caution',
        onClick: () => runBulk('exclude', t('authorDetail.bulk.exclude'), t('authorDetail.bulk.excludeConfirm', { count: selected.size })),
      }

  return (
    // One width shared with BookDetailPage — see the note there.
    <div className={`max-w-7xl ${selected.size > 0 ? 'pb-20' : ''}`}>
      {confirmDialog}
      <div className="mb-4 flex items-center justify-between gap-3 text-sm">
        {/* Only inside a Previous/Next chain does Back go to the Authors
            list rather than browser history: a few hops through Next/Previous
            land one page short of where the list actually was. Arriving any
            other way (Wanted, the Books list, a book page, a direct link)
            must fall back to navigate(-1) or Back would strand those flows
            on the Authors list instead of where they came from. */}
        {navState ? (
          <Link to="/" className="text-fg-muted hover:text-slate-900 dark:hover:text-white">← {t('authorDetail.back', 'Back')}</Link>
        ) : (
          <button
            type="button"
            onClick={() => navigate(-1)}
            className="text-fg-muted hover:text-slate-900 dark:hover:text-white"
          >
            ← {t('authorDetail.back', 'Back')}
          </button>
        )}
        {navState && (prevId !== null || nextId !== null) && (
          <div className="flex items-center gap-2">
            {prevId !== null && (
              <Link to={`/author/${prevId}`} state={{ ids: navState.ids, index: navState.index - 1 }} aria-label={t('authorDetail.nav.previousAriaLabel', 'Previous author')} className={`${btn.ghost} ${btnSize.sm}`}>
                {t('authorDetail.nav.previous', '‹ Previous')}
              </Link>
            )}
            {nextId !== null && (
              <Link to={`/author/${nextId}`} state={{ ids: navState.ids, index: navState.index + 1 }} aria-label={t('authorDetail.nav.nextAriaLabel', 'Next author')} className={`${btn.ghost} ${btnSize.sm}`}>
                {t('authorDetail.nav.next', 'Next ›')}
              </Link>
            )}
          </div>
        )}
      </div>

      <div className="flex flex-col sm:flex-row gap-6 mb-8">
        <div className="w-32 flex-shrink-0">
          <CoverImage src={author.imageUrl} alt={author.authorName} className="w-full rounded-full shadow-lg aspect-square object-cover"
            fallback={
              <div className="aspect-square rounded-full bg-slate-200 dark:bg-zinc-800 flex items-center justify-center text-2xl font-bold text-fg-muted">
                {author.authorName.charAt(0).toUpperCase()}
              </div>
            }
          />
          <label className={`touch-target mt-2 block text-center text-xs text-accent-text hover:underline cursor-pointer has-[:focus-visible]:underline ${uploadingPhoto ? 'opacity-50 pointer-events-none' : ''}`}>
            {uploadingPhoto === 'upload'
              ? t('authorDetail.photo.uploading', 'Uploading…')
              : author.imageUrl ? t('authorDetail.photo.change', 'Change photo') : t('authorDetail.photo.upload', 'Upload photo')}
            <input
              type="file"
              accept="image/jpeg,image/png,image/webp,image/gif"
              className="sr-only"
              disabled={uploadingPhoto !== null}
              onChange={e => { void changePhoto('upload', e.target.files?.[0]); e.target.value = '' }}
            />
          </label>
          {/* Only an uploaded photo can be removed: the provider's or an
              enricher's would come straight back on the refresh that
              removing runs (#3287). */}
          {author.imageSource?.provider === 'user' && (
            <button
              type="button"
              className="touch-target block w-full text-center text-xs text-slate-600 dark:text-zinc-400 hover:underline disabled:opacity-50"
              disabled={uploadingPhoto !== null}
              onClick={() => { void changePhoto('remove') }}
            >
              {uploadingPhoto === 'remove' ? t('authorDetail.photo.removing', 'Removing…') : t('authorDetail.photo.remove', 'Remove photo')}
            </button>
          )}
        </div>
        <div className="min-w-0 flex-1">
          <h2 className="text-2xl font-bold mb-1 [overflow-wrap:anywhere]">{author.authorName}</h2>
          {author.disambiguation && (
            <p className="text-xs text-fg-muted">{author.disambiguation}</p>
          )}
          <div className="flex flex-wrap gap-x-4 gap-y-1 mt-2 text-xs text-fg-muted">
            {author.averageRating > 0 && (
              <span>★ {author.averageRating.toFixed(2)} ({t('authorDetail.ratings', { n: author.ratingsCount.toLocaleString(), defaultValue: '{{n}} ratings' })})</span>
            )}
            {(() => {
              const src = metadataSourceLink(author.foreignAuthorId, 'author')
              return src ? (
                <a
                  href={src.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-accent-text hover:underline"
                >
                  {t('common.viewOnSource', { source: src.label, defaultValue: 'View on {{source}} ↗' })}
                </a>
              ) : null
            })()}
            {/* BnF's Licence Ouverte asks for the source and the date
                wherever its data is shown (#3144). */}
            {(() => {
              const date = authorProviderKey(author) === 'bnf' ? metadataRetrievedDate(author) : null
              return date ? (
                <span data-testid="bnf-source-credit">
                  {t('common.bnfSourceCredit', { date, defaultValue: 'Data from the BnF, Licence Ouverte / Open Licence 2.0, retrieved {{date}}' })}
                </span>
              ) : null
            })()}
            {/* A photo or bio an author enricher filled in carries the credit
                its licence asks for: a Commons photo names its photographer
                and licence, Wikipedia text its source and licence (#3287). */}
            {author.imageSource && author.imageSource.provider !== 'user' && (
              <a
                data-testid="author-photo-credit"
                href={safeHref(author.imageSource.url) || undefined}
                target="_blank"
                rel="noopener noreferrer"
                className="hover:underline"
              >
                {author.imageSource.credit
                  ? t('authorDetail.credit.photo', { credit: author.imageSource.credit, license: author.imageSource.license ?? '', source: profileSourceLabel(author.imageSource.provider), defaultValue: 'Photo: {{credit}}, {{license}}, via {{source}}' })
                  : t('authorDetail.credit.photoNoCredit', { license: author.imageSource.license ?? '', source: profileSourceLabel(author.imageSource.provider), defaultValue: 'Photo: {{license}}, via {{source}}' })}
              </a>
            )}
            {author.descriptionSource && author.descriptionSource.provider !== 'user' && (
              <a
                data-testid="author-bio-credit"
                href={safeHref(author.descriptionSource.url) || undefined}
                target="_blank"
                rel="noopener noreferrer"
                className="hover:underline"
              >
                {author.descriptionSource.license
                  ? t('authorDetail.credit.bio', { license: author.descriptionSource.license, source: profileSourceLabel(author.descriptionSource.provider), defaultValue: 'Bio from {{source}}, {{license}}' })
                  : t('authorDetail.credit.bioNoLicense', { source: profileSourceLabel(author.descriptionSource.provider), defaultValue: 'Bio from {{source}}' })}
              </a>
            )}
          </div>

          {/* Four fixed cells, replacing a run-on sentence. The audiobook count
              used to vanish at zero, so the row changed shape between authors
              and the numbers never sat in the same place twice. */}
          <dl
            data-testid="author-stats"
            className="grid grid-cols-4 gap-px mt-3 rounded-lg overflow-hidden border border-slate-200 dark:border-zinc-800 bg-slate-200 dark:bg-zinc-800 max-w-lg"
          >
            {[
              { key: 'books', label: t('authorDetail.stats.books', 'Books'), value: counts.total },
              { key: 'inLibrary', label: t('authorDetail.stats.inLibrary', 'In library'), value: counts.imported },
              { key: 'wanted', label: t('authorDetail.stats.wanted', 'Wanted'), value: counts.wanted },
              { key: 'audiobooks', label: t('authorDetail.stats.audiobooks', 'Audiobooks'), value: counts.audiobook },
            ].map(cell => (
              <div key={cell.key} className="bg-slate-100 dark:bg-zinc-900 px-3 py-2">
                <dt className="text-[11px] text-fg-muted">{cell.label}</dt>
                <dd className="text-lg font-semibold tabular-nums text-slate-900 dark:text-white">
                  {cell.value}
                </dd>
              </div>
            ))}
          </dl>
          {author.description && (
            <MarkdownDescription
              text={author.description}
              showMoreLabel={t('authorDetail.description.showMore', 'Show more')}
              showLessLabel={t('authorDetail.description.showLess', 'Show less')}
              className="mt-3 max-w-prose"
            />
          )}
          <div className="flex flex-wrap gap-2 mt-4">
            <Switch
              checked={author.monitored}
              onChange={handleToggleMonitored}
              label={author.monitored ? t('authors.stopMonitoring', 'Stop monitoring') : t('authors.startMonitoring', 'Monitor')}
              className="px-1"
            >
              {author.monitored ? t('authors.monitored') : t('authors.unmonitored')}
            </Switch>
            {/* One primary action, carrying its own count, so it doesn't need a
                tooltip to say how much work it will do. */}
            <button
              onClick={handleSearchWanted}
              disabled={searchingWanted || searchableWantedCount === 0}
              className={`${btn.primary} ${btnSize.sm}`}
              title={searchableWantedCount === 0 ? t('authorDetail.actions.searchWantedNone', 'No wanted books to search') : undefined}
            >
              {searchingWanted
                ? t('authorDetail.actions.searching', 'Searching…')
                : t('authorDetail.actions.searchWanted', { count: searchableWantedCount, defaultValue: 'Search {{count}} wanted' })}
            </button>
            {/* On a phone the row keeps the switch, the primary action and
                More; Refresh and Edit fold into More rather than wrapping the
                row onto a second line. */}
            {!narrow && <button
              onClick={handleRefresh}
              disabled={refreshing}
              className={`${btn.secondary} ${btnSize.sm}`}
              title={t('authorDetail.actions.refreshHint', "Fetch this author's catalogue from the metadata provider again. Books you do not have are added according to Monitor new items. Nothing is downloaded until the next wanted search.")}
            >
              {refreshing ? t('authorDetail.actions.refreshing', 'Refreshing…') : t('authorDetail.actions.refresh', 'Refresh')}
            </button>}
            {!narrow && <button
              onClick={() => setShowEdit(true)}
              className={`${btn.secondary} ${btnSize.sm}`}
              title={t('authorDetail.actions.editHint', 'Edit quality, metadata, and root folder')}
            >
              {t('authorDetail.actions.edit', 'Edit')}
            </button>}
            {/* The rest were eight controls on one row, which wrapped and left
                Delete orphaned on a line of its own — the most destructive
                action given the most prominence by accident. */}
            <MoreMenu
              label={t('common.more', 'More')}
              buttonClassName={`${btn.secondary} ${btnSize.sm}`}
              items={[
                ...(narrow ? [
                  { label: refreshing ? t('authorDetail.actions.refreshing', 'Refreshing…') : t('authorDetail.actions.refresh', 'Refresh'), onSelect: handleRefresh, disabled: refreshing },
                  { label: t('authorDetail.actions.edit', 'Edit'), onSelect: () => setShowEdit(true) },
                ] : []),
                {
                  label: t('authorDetail.actions.renameFiles', 'Rename files'),
                  title: t('authorDetail.actions.renameFilesHint', 'Move this author’s files to match the current naming template'),
                  onSelect: () => setShowRename(true),
                },
                {
                  label: t('authorDetail.actions.merge', 'Merge…'),
                  title: t('authorDetail.actions.mergeHint', 'Merge another author into this one'),
                  onSelect: () => {
                    if (allAuthors.length === 0) api.listAllAuthors().then(setAllAuthors).catch(console.error)
                    setShowMerge(true)
                  },
                },
                ...(showMetadataLinkAction
                  ? [{ label: metadataLinkLabel, onSelect: () => setShowMetadataLink(true) }]
                  : []),
                {
                  label: t('authorDetail.actions.reconcileCatalogue', 'Reconcile catalogue…'),
                  title: t('authorDetail.actions.reconcileCatalogueHint', 'Preview stale metadata-only Wanted rows before removing them'),
                  onSelect: () => setShowCatalogueReconciliation(true),
                },
                {
                  label: t('authorDetail.actions.reviewDuplicates', 'Review duplicates…'),
                  title: t('authorDetail.actions.reviewDuplicatesHint', 'Find titles that look like the same book; nothing changes until you exclude a row'),
                  onSelect: () => setShowDuplicateCandidates(true),
                },
                {
                  label: t('authorDetail.actions.delete', 'Delete'),
                  danger: true,
                  onSelect: handleDelete,
                },
              ]}
            />
          </div>
          {author.aliases && author.aliases.length > 0 && (
            <div className="mt-4 text-xs">
              <div className="text-fg-muted mb-1">{t('authorDetail.aliasesHeading', 'Also known as')}</div>
              <div className="flex flex-wrap gap-1.5">
                {author.aliases.map(a => (
                  <span
                    key={a.id}
                    className="group/alias inline-flex items-center gap-1 px-2 py-0.5 rounded bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300"
                    title={a.sourceOlId ? t('authorDetail.aliasSource', { source: a.sourceOlId, defaultValue: 'From {{source}}' }) : undefined}
                  >
                    <span>{a.name}</span>
                    <button
                      type="button"
                      onClick={() => handleDeleteAlias(a)}
                      className="ml-0.5 leading-none text-slate-500 hover:text-red-600 focus:text-red-600 focus-visible:outline-hidden focus-visible:ring-1 focus-visible:ring-red-500 dark:text-zinc-500 dark:hover:text-red-400 dark:focus:text-red-400 opacity-100 transition-opacity [@media(hover:hover)]:opacity-0 [@media(hover:hover)]:group-hover/alias:opacity-100 [@media(hover:hover)]:focus:opacity-100 [@media(hover:hover)]:focus-visible:opacity-100"
                      aria-label={t('authorDetail.aliases.removeLabel', { name: a.name })}
                      title={t('authorDetail.aliases.removeLabel', { name: a.name })}
                    >
                      ×
                    </button>
                  </span>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>

      {showEdit && (
        <EditAuthorModal
          author={author}
          onClose={() => setShowEdit(false)}
          onSaved={updated => setAuthor(updated)}
        />
      )}

      {showRename && (
        <RenameFilesModal
          scope="author"
          id={author.id}
          label={author.authorName}
          onClose={() => setShowRename(false)}
          onApplied={() => api.listAllBooks({ authorId, includeExcluded: showExcluded }).then(setBooks).catch(() => {})}
        />
      )}

      {showMetadataLink && (
        <AuthorMetadataLinkModal
          author={author}
          onClose={() => setShowMetadataLink(false)}
          onLinked={updated => {
            setAuthor(updated)
            Promise.all([api.getAuthor(authorId), api.listAllBooks({ authorId, includeExcluded: showExcluded })])
              .then(([a, bs]) => { setAuthor(a); setBooks(bs) })
              .catch(console.error)
          }}
        />
      )}

      {showCatalogueReconciliation && (
        <CatalogueReconciliationModal
          authorId={author.id}
          authorName={author.authorName}
          onClose={() => setShowCatalogueReconciliation(false)}
          onApplied={reloadBooks}
        />
      )}

      {showDuplicateCandidates && (
        <DuplicateCandidatesModal
          authorId={author.id}
          authorName={author.authorName}
          onClose={() => setShowDuplicateCandidates(false)}
          onChanged={reloadBooks}
        />
      )}

      {showMerge && allAuthors.length > 0 && (
        <MergeAuthorsModal
          authors={allAuthors}
          initialTargetId={author.id}
          onClose={() => setShowMerge(false)}
          onMerged={() => {
            // Reload current author (aliases may have grown) + its books.
            Promise.all([api.getAuthor(authorId), api.listAllBooks({ authorId })])
              .then(([a, bs]) => { setAuthor(a); setBooks(bs) })
              .catch(console.error)
          }}
        />
      )}

      {error && (
        <div className="mb-4 px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
          {error}
        </div>
      )}

      {/* Books the last sync dropped, right above the list they are missing
          from (#1889). Renders nothing when nothing was filtered out. */}
      <AuthorSyncNotice sync={author.lastSync} />

      <Section
        bare
        title={
          <>
            {t('authorDetail.booksHeading', 'Books')}
            {filteredBooks.length !== books.length && (
              <span className="ml-2 text-sm font-normal text-fg-muted">
                {t('authorDetail.filteredCount', {
                  shown: filteredBooks.length,
                  total: books.length,
                  defaultValue: '{{shown}} of {{total}}',
                })}
              </span>
            )}
          </>
        }
        actions={
          <>
            <Switch
              checked={groupBySeries}
              onChange={() => setGroupBySeries(v => !v)}
              label={groupBySeries ? t('authorDetail.showFlatList', 'Show flat list') : t('authorDetail.groupBySeries', 'Group by series')}
            >
              {t('authorDetail.groupBySeries', 'Group by series')}
            </Switch>
            <ViewToggle view={view} onChange={setView} />
          </>
        }
      >
        {/* The shared filter bar. This row used to be six native selects
            (Type, Status, Published, Monitored, Language, Sort), the densest
            control row in the app and a third filter pattern beside Books and
            Wanted. Status is the inline segment, as on Books; the secondary
            facets sit behind Filters, each applied one shown as a chip. The
            Excluded status is still a status: it widens the fetch to excluded
            rows (#103). */}
        {books.length > 0 && (
          <FilterBar
            status={{
              value: statusFilter,
              onChange: setStatusFilter,
              label: t('authorDetail.filters.status', 'Status'),
              options: STATUS_FILTERS.map(s => ({
                value: s,
                label: s === '' ? t('common.all', 'All') : t(`authorDetail.filters.status_${s}`, STATUS_FALLBACK[s]),
              })),
              help: <BookStatusLegend />,
            }}
            sort={{
              value: sort,
              onChange: setSort,
              label: t('authorDetail.filters.sort', 'Sort'),
              options: [
                { value: '' as const, label: t('authorDetail.filters.sortDefault', 'Default') },
                { value: 'title-az' as const, label: t('authorDetail.filters.sortTitleAZ', 'Title A–Z') },
                { value: 'title-za' as const, label: t('authorDetail.filters.sortTitleZA', 'Title Z–A') },
                { value: 'date-new' as const, label: t('authorDetail.filters.sortNewest', 'Published, newest') },
                { value: 'date-old' as const, label: t('authorDetail.filters.sortOldest', 'Published, oldest') },
                { value: 'added-new' as const, label: t('authorDetail.filters.sortAddedNew', 'Added, newest') },
                { value: 'added-old' as const, label: t('authorDetail.filters.sortAddedOld', 'Added, oldest') },
              ],
            }}
            filters={{
              activeCount: [typeFilter, publishedFilter, monitoredFilter, activeLanguage].filter(Boolean).length,
              content: (
                <>
                  <FilterGroup
                    label={t('authorDetail.filters.type', 'Type')}
                    value={typeFilter}
                    onChange={setTypeFilter}
                    options={[
                      { value: '' as const, label: t('authorDetail.filters.allTypes', 'All types') },
                      { value: 'ebook' as const, label: `📖 ${t('common.ebook')}`, ariaLabel: t('common.ebook') },
                      { value: 'audiobook' as const, label: `🎧 ${t('common.audiobook')}`, ariaLabel: t('common.audiobook') },
                    ]}
                  />
                  <FilterGroup
                    label={t('authorDetail.filters.published', 'Published')}
                    value={publishedFilter}
                    onChange={setPublishedFilter}
                    options={[
                      { value: '' as const, label: t('authorDetail.filters.allPublished', 'Any date') },
                      { value: 'released' as const, label: t('authorDetail.filters.released', 'Released') },
                      { value: 'upcoming' as const, label: t('authorDetail.filters.upcoming', 'Upcoming') },
                    ]}
                  />
                  <FilterGroup
                    label={t('authorDetail.filters.monitored', 'Monitored')}
                    value={monitoredFilter}
                    onChange={setMonitoredFilter}
                    options={[
                      { value: '' as const, label: t('authorDetail.filters.allMonitored', 'Any') },
                      { value: 'monitored' as const, label: t('authorDetail.filters.monitoredOnly', 'Monitored') },
                      { value: 'unmonitored' as const, label: t('authorDetail.filters.unmonitoredOnly', 'Unmonitored') },
                    ]}
                  />
                  {languages.length > 1 && (
                    <FilterGroup
                      label={t('authorDetail.filters.language', 'Language')}
                      value={activeLanguage}
                      onChange={setLanguageFilter}
                      options={[
                        { value: '', label: t('authorDetail.filters.allLanguages', 'All languages') },
                        ...languages.map(l => ({ value: l, label: languageName(l) ?? l })),
                      ]}
                    />
                  )}
                </>
              ),
            }}
            chips={[
              ...(typeFilter ? [{ key: 'type', label: typeFilter === 'ebook' ? t('common.ebook') : t('common.audiobook'), onClear: () => setTypeFilter('') }] : []),
              ...(publishedFilter ? [{
                key: 'published',
                label: publishedFilter === 'released' ? t('authorDetail.filters.released', 'Released') : t('authorDetail.filters.upcoming', 'Upcoming'),
                onClear: () => setPublishedFilter(''),
              }] : []),
              ...(monitoredFilter ? [{
                key: 'monitored',
                label: monitoredFilter === 'monitored' ? t('authorDetail.filters.monitoredOnly', 'Monitored') : t('authorDetail.filters.unmonitoredOnly', 'Unmonitored'),
                onClear: () => setMonitoredFilter(''),
              }] : []),
              ...(activeLanguage ? [{ key: 'language', label: languageName(activeLanguage) ?? activeLanguage, onClear: () => setLanguageFilter('') }] : []),
            ]}
          >
            {/* Select/deselect every currently displayed book (#1172). Operates on
                filteredBooks, so it respects the active filters and composes
                with the per-book checkboxes used by the bulk action bar. */}
            {filteredBooks.length > 0 && (
              <button
                onClick={() => allVisibleSelected ? clearSelection() : selectAllVisible()}
                className={`${btn.ghost} ${btnSize.sm} ml-auto`}
                aria-pressed={allVisibleSelected}
              >
                {allVisibleSelected ? t('authorDetail.bulk.deselectAll') : t('authorDetail.bulk.selectAll')}
              </button>
            )}
          </FilterBar>
        )}

        {syncing && (
          <p role="status" className="mb-3 flex items-center gap-2 text-sm text-fg-muted">
            <span aria-hidden="true" className="inline-block h-3.5 w-3.5 shrink-0 animate-spin rounded-full border-2 border-current border-t-transparent" />
            {books.length === 0
              ? t('authorDetail.syncing', 'Fetching this author’s catalogue. Books appear here when it finishes, which can take a few minutes for a large catalogue.')
              : t('authorDetail.syncingMore', 'Fetching this author’s catalogue. Any new books appear here when it finishes.')}
          </p>
        )}
        {books.length === 0 ? (
          !syncing && <p className="text-sm text-fg-muted">{t('authorDetail.noBooks', 'No books tracked for this author yet.')}</p>
        ) : filteredBooks.length === 0 ? (
          <p className="text-sm text-fg-muted">{t('authorDetail.noFilterMatch', 'No books match the current filters.')}</p>
        ) : groupBySeries ? (
          <div className="space-y-6">
            {seriesGroups.map(group => (
              <div key={group.key}>
                <h4 className="text-sm font-semibold text-slate-700 dark:text-zinc-300 mb-2 flex items-center gap-2">
                  {/* The Series page opens the card named in router state, as
                      the Authors page and the header search do (#3457). */}
                  {group.seriesId !== undefined ? (
                    <Link
                      to="/series"
                      state={{ seriesId: group.seriesId }}
                      title={t('authorDetail.openSeries', 'Open this series on the Series page')}
                      className="hover:underline hover:text-emerald-700 dark:hover:text-emerald-400"
                    >
                      {group.title}
                    </Link>
                  ) : (
                    <span>{group.title}</span>
                  )}
                  <span className="text-xs font-normal text-fg-muted bg-slate-200 dark:bg-zinc-800 px-2 py-0.5 rounded-full">
                    {group.books.length} {group.books.length === 1 ? 'book' : 'books'}
                  </span>
                </h4>
                {renderBooks(group.books, false)}
              </div>
            ))}
          </div>
        ) : (
          renderBooks(filteredBooks)
        )}
      </Section>

      <BulkActionBar
        count={selected.size}
        onClear={clearSelection}
        busy={bulkBusy}
        actions={[
          ...(statusFilter === 'excluded' ? [exclusionAction] : []),
          { label: t('authorDetail.bulk.monitor'), onClick: () => runBulk('monitor', t('authorDetail.bulk.monitor')) },
          { label: t('authorDetail.bulk.unmonitor'), onClick: () => runBulk('unmonitor', t('authorDetail.bulk.unmonitor')) },
          // Search the selected books rather than all of this author's
          // wanted ones: on a catalogue padded with editions, the header's
          // "Search N wanted" is the wrong tool (#2267).
          { label: t('common.search'), onClick: () => runBulk('search', t('common.search')) },
          // Media-type corrections are author-scoped work in practice — a sync
          // mistags several titles under one author — so they belong here as
          // well as on the global Books view (#2066).
          {
            label: t('bulkActionBar.setFormat', 'Set format'),
            choices: [
              { label: t('common.ebook'), onClick: () => runBulk('set_media_type', t('books.setEbook'), undefined, 'ebook') },
              { label: t('common.audiobook'), onClick: () => runBulk('set_media_type', t('books.setAudiobook'), undefined, 'audiobook') },
              { label: t('common.both'), onClick: () => runBulk('set_media_type', t('books.setBoth'), undefined, 'both') },
            ],
          },
          ...(statusFilter === 'excluded' ? [] : [exclusionAction]),
          {
            label: t('authorDetail.bulk.delete'),
            variant: 'danger',
            onClick: () => runBulk('delete', t('authorDetail.bulk.delete'), t('authorDetail.bulk.deleteConfirm', { count: selected.size })),
          },
        ]}
      />
    </div>
  )
}

// profileSourceLabel names the source an author enricher credits (#3287).
function profileSourceLabel(provider: string): string {
  switch (provider) {
    case 'wikimedia-commons':
      return 'Wikimedia Commons'
    case 'wikipedia':
      return 'Wikipedia'
    default:
      return provider
  }
}
