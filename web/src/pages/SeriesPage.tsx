import { useEffect, useState } from 'react'
import { Link, useLocation, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, MediaType, Series, SeriesHardcoverDiff, SeriesHardcoverDiffBook, SeriesHardcoverLink, SeriesHardcoverSearchResult, SystemStatus } from '../api/client'
import { hardcoverSeriesUrl } from '../util/metadataSource'
import { foldedIncludes } from '../util/foldForSearch'
import AddSeriesBookModal from '../components/AddSeriesBookModal'
import HardcoverSeriesLinkModal from '../components/HardcoverSeriesLinkModal'
import SeriesNameModal from '../components/SeriesNameModal'
import MergeSeriesModal from '../components/MergeSeriesModal'
import { btn, btnSize } from '../components/buttons'
import Switch from '../components/Switch'
import { useConfirmDialog } from '../components/useConfirmDialog'

// Series page filters (#2871). Each is computed from the series list the page
// already loaded; none of them marks, monitors, searches or fills anything.
const SERIES_FILTERS = ['all', 'missing', 'complete', 'unlinked', 'shortlisted'] as const
type SeriesFilter = typeof SERIES_FILTERS[number]

function parseSeriesFilter(raw: string | null): SeriesFilter {
  return SERIES_FILTERS.find(f => f === raw) ?? 'all'
}

// How many of a series' split edition parts (#3048) are still monitored, which
// is what the unmonitor action would change.
function monitoredSplitParts(series: Series): number {
  const parts = new Set(series.splitEditionPartBookIds ?? [])
  return (series.books ?? []).filter(b => parts.has(b.bookId) && b.book?.monitored).length
}

// The counts behind a series card's "N missing" badge, and the only place they
// are computed, so the Missing and Complete filters cannot disagree with the
// badge the user is looking at. With enhanced Hardcover on, the badge can count
// catalogue books that are not in the library: the exact figure from the diff
// once the card has been opened, otherwise the estimate from the linked
// series' book count. Both come from data already on the page; the filters
// never fetch a diff per series to decide.
function seriesMissingCounts(series: Series, enhancedHardcoverApi: boolean, diff?: SeriesHardcoverDiff) {
  const books = series.books ?? []
  // Excluded books are not a gap: counting them showed a "missing" pill
  // that Fill could not act on (#2324). Nor is a split edition part of a book
  // already in the series, which Fill skips (#3048).
  const splitParts = new Set(series.splitEditionPartBookIds ?? [])
  const gapCount = books.filter(b => b.book && b.book.status !== 'imported' && !b.book.excluded && !splitParts.has(b.bookId)).length
  const hardcoverMissingEstimate = enhancedHardcoverApi ? Math.max(0, (series.hardcoverLink?.hardcoverBookCount ?? 0) - books.length) : 0
  const hardcoverMissingCount = enhancedHardcoverApi ? (diff?.missingCount ?? hardcoverMissingEstimate) : 0
  const displayMissingCount = Math.max(gapCount, hardcoverMissingCount)
  return { gapCount, hardcoverMissingCount, displayMissingCount }
}

export default function SeriesPage() {
  const { t } = useTranslation()
  const { confirm, confirmDialog } = useConfirmDialog()
  const location = useLocation()
  const [seriesList, setSeriesList] = useState<Series[]>([])
  const [search, setSearch] = useState('')
  // The filter lives in the query string so reload and back keep it.
  const [searchParams, setSearchParams] = useSearchParams()
  const [loading, setLoading] = useState(true)
  const [expanded, setExpanded] = useState<number | null>(null)
  const [filling, setFilling] = useState<number | null>(null)
  const [applyingGenres, setApplyingGenres] = useState<number | null>(null)
  const [fillResult, setFillResult] = useState<Record<number, string>>({})
  // Format to target when adding missing Hardcover books, keyed by series id.
  // Defaults to ebook to preserve the previous add behaviour.
  const [fillMediaType, setFillMediaType] = useState<Record<number, MediaType>>({})
  const [linking, setLinking] = useState<number | null>(null)
  const [linkResult, setLinkResult] = useState<Record<number, string>>({})
  const [linkModalSeries, setLinkModalSeries] = useState<Series | null>(null)
  const [linkModalResults, setLinkModalResults] = useState<SeriesHardcoverSearchResult[]>([])
  const [diffs, setDiffs] = useState<Record<number, SeriesHardcoverDiff>>({})
  const [diffLoading, setDiffLoading] = useState<Record<number, boolean>>({})
  const [diffErrors, setDiffErrors] = useState<Record<number, string>>({})
  const [systemStatus, setSystemStatus] = useState<SystemStatus | null>(null)
  const [showAddSeries, setShowAddSeries] = useState(false)
  const [editingSeries, setEditingSeries] = useState<Series | null>(null)
  const [mergeTarget, setMergeTarget] = useState<Series | null>(null)
  const [bookModalSeries, setBookModalSeries] = useState<Series | null>(null)
  const enhancedHardcoverApi = systemStatus?.enhancedHardcoverApi ?? false

  // Keyed on the id alone, not the state object: opening a modal pushes an
  // entry with new state (useModal), which must not refetch the list or
  // re-expand the series the page was first opened on.
  const navSeriesId = (location.state as { seriesId?: number } | null)?.seriesId
  useEffect(() => {
    Promise.all([api.listSeries(), api.status()])
      .then(([list, status]) => {
        setSeriesList(list)
        setSystemStatus(status)
        if (navSeriesId) {
          setExpanded(navSeriesId)
        }
      })
      .catch(console.error)
      .finally(() => setLoading(false))
  }, [navSeriesId])

  useEffect(() => {
    document.title = `${t('series.title')} · Bindery`
    return () => { document.title = 'Bindery' }
  }, [t])

  const refreshSeriesList = async () => {
    const list = await api.listSeries()
    setSeriesList(list)
    return list
  }

  const handleCreateSeries = async (title: string) => {
    const series = await api.createSeries({ title })
    await refreshSeriesList()
    setExpanded(series.id)
    setShowAddSeries(false)
  }

  const handleRenameSeries = async (title: string) => {
    if (!editingSeries) return
    const updated = await api.updateSeries(editingSeries.id, { title })
    setSeriesList(prev => prev.map(series => series.id === updated.id ? { ...series, ...updated } : series))
    setEditingSeries(null)
  }

  // Genre override (#1446, #1709): prompt for a comma-separated list, then set
  // + lock it on every book in the series so metadata refresh keeps it. The
  // list is also stored on the series and applied to books added later.
  //
  // The prompt is seeded with the current override so it reads as an edit
  // rather than a blank slate, and submitting an empty box clears the override
  // instead of silently doing nothing — without that there is no way to undo a
  // "Set genre" from the UI.
  const applySeriesGenres = async (series: Series) => {
    const current = series.genreOverride ?? []
    const input = prompt(
      t('series.genre.prompt', { title: series.title }),
      current.join(', '),
    )
    if (input === null) return
    const genres = input.split(',').map(g => g.trim()).filter(Boolean)
    setApplyingGenres(series.id)
    try {
      if (genres.length === 0) {
        if (!series.genreOverrideSet) return
        await api.clearSeriesGenres(series.id)
        setSeriesList(prev => prev.map(s =>
          s.id === series.id ? { ...s, genreOverride: undefined, genreOverrideSet: false } : s))
        setLinkResult(prev => ({ ...prev, [series.id]: t('series.genre.removed') }))
        return
      }
      const { updated } = await api.applySeriesGenres(series.id, genres)
      setSeriesList(prev => prev.map(s =>
        s.id === series.id ? { ...s, genreOverride: genres, genreOverrideSet: true } : s))
      setLinkResult(prev => ({ ...prev, [series.id]: t('series.genre.applied', { count: updated }) }))
    } catch (err) {
      alert(err instanceof Error ? err.message : t('series.genre.failed'))
    } finally {
      setApplyingGenres(null)
    }
  }

  const deleteSeries = async (series: Series) => {
    if (!await confirm({
      title: t('series.deleteTitle'),
      body: t('series.deleteConfirm', { title: series.title }),
      confirmLabel: t('common.delete'),
    })) return
    await api.deleteSeries(series.id)
    setSeriesList(prev => prev.filter(item => item.id !== series.id))
    setDiffs(prev => {
      const next = { ...prev }
      delete next[series.id]
      return next
    })
    if (expanded === series.id) {
      setExpanded(null)
    }
  }

  const handleBookLinked = (updated: Series) => {
    setSeriesList(prev => prev.map(series => series.id === updated.id ? updated : series))
    setExpanded(updated.id)
    if (enhancedHardcoverApi && updated.hardcoverLink) {
      void loadHardcoverDiff(updated, true)
    }
  }

  const loadHardcoverDiff = async (series: Series, force = false) => {
    if (!enhancedHardcoverApi) return
    if (!series.hardcoverLink) return
    if (!force && (diffs[series.id] || diffLoading[series.id])) return
    setDiffLoading(prev => ({ ...prev, [series.id]: true }))
    setDiffErrors(prev => {
      const next = { ...prev }
      delete next[series.id]
      return next
    })
    try {
      const diff = await api.getSeriesHardcoverDiff(series.id)
      setDiffs(prev => ({ ...prev, [series.id]: diff }))
    } catch (err) {
      setDiffErrors(prev => ({ ...prev, [series.id]: err instanceof Error ? err.message : t('series.hardcover.diffFailed') }))
    } finally {
      setDiffLoading(prev => ({ ...prev, [series.id]: false }))
    }
  }

  const toggleExpanded = (series: Series) => {
    const opening = expanded !== series.id
    setExpanded(opening ? series.id : null)
    if (opening) {
      void loadHardcoverDiff(series)
    }
  }

  const toggleMonitor = async (series: Series) => {
    const next = !series.monitored
    await api.monitorSeries(series.id, next)
    setSeriesList(prev => prev.map(s => s.id === series.id ? { ...s, monitored: next } : s))
  }

  const fillGaps = async (series: Series, book?: SeriesHardcoverDiffBook, mediaType?: MediaType) => {
    setFilling(series.id)
    try {
      const r = book
        ? await api.fillSeries(series.id, {
            foreignBookId: book.foreignBookId,
            providerId: book.providerId,
            position: book.position,
            ...(mediaType ? { mediaType } : {}),
          })
        : await api.fillSeriesAll(series.id, mediaType)
      // Say what the fill left out and why, or a profile that filtered every
      // missing book reads as a fill that did nothing (#2208, #3048).
      const parts = [r.queued === 0 ? t('series.fill.nothing') : t('series.fill.queued', { count: r.queued })]
      if (r.skippedByProfile) parts.push(t('series.fill.skippedByProfile', { count: r.skippedByProfile }))
      if (r.skippedSplitParts) parts.push(t('series.fill.skippedSplitParts', { count: r.skippedSplitParts }))
      setFillResult(prev => ({ ...prev, [series.id]: parts.join(', ') }))
      const list = await refreshSeriesList()
      const updated = list.find(s => s.id === series.id)
      if (enhancedHardcoverApi && updated?.hardcoverLink) {
        await loadHardcoverDiff(updated, true)
      }
    } catch {
      setFillResult(prev => ({ ...prev, [series.id]: t('series.fill.failed') }))
    } finally {
      setFilling(null)
    }
  }

  // One click cleanup for the split edition parts a fill created before the
  // series diff learned to recognise them (#3048). Unmonitors, never deletes.
  const unmonitorSplitParts = async (series: Series) => {
    if (!await confirm({
      title: t('common.confirmTitle'),
      body: t('series.splitParts.confirm'),
      confirmLabel: t('series.splitParts.unmonitor', { count: monitoredSplitParts(series) }),
    })) return
    setFilling(series.id)
    try {
      const r = await api.unmonitorSeriesSplitParts(series.id)
      setFillResult(prev => ({ ...prev, [series.id]: t('series.splitParts.done', { count: r.unmonitored }) }))
      await refreshSeriesList()
    } catch {
      setFillResult(prev => ({ ...prev, [series.id]: t('series.splitParts.failed') }))
    } finally {
      setFilling(null)
    }
  }

  const openHardcoverLink = async (series: Series) => {
    if (!enhancedHardcoverApi) return
    setLinkResult(prev => {
      const next = { ...prev }
      delete next[series.id]
      return next
    })
    if (series.hardcoverLink) {
      setLinkModalResults([])
      setLinkModalSeries(series)
      return
    }

    setLinking(series.id)
    try {
      const response = await api.autoLinkSeriesHardcover(series.id)
      const modalSeries = response.link ? { ...series, hardcoverLink: response.link } : series
      setLinkModalResults(response.candidates ?? [])
      setLinkModalSeries(modalSeries)
      if (response.link) {
        const link = response.link
        setSeriesList(prev => prev.map(s => s.id === series.id ? { ...s, hardcoverLink: link } : s))
        await loadHardcoverDiff(modalSeries, true)
      } else if (response.reason) {
        const reason = response.reason
        setLinkResult(prev => ({ ...prev, [series.id]: reason }))
      }
    } catch (err) {
      setLinkResult(prev => ({ ...prev, [series.id]: err instanceof Error ? err.message : t('series.hardcover.searchFailed') }))
    } finally {
      setLinking(null)
    }
  }

  const handleHardcoverLinked = (seriesId: number, link?: SeriesHardcoverLink) => {
    setSeriesList(prev => prev.map(series => series.id === seriesId ? { ...series, hardcoverLink: link } : series))
    if (!link) {
      setDiffs(prev => {
        const next = { ...prev }
        delete next[seriesId]
        return next
      })
      return
    }
    const series = seriesList.find(item => item.id === seriesId)
    if (enhancedHardcoverApi && series) {
      void loadHardcoverDiff({ ...series, hardcoverLink: link }, true)
    }
  }

  // Unlinked only means something when Hardcover linking is on; otherwise
  // the page shows no link controls, so a stale ?filter=unlinked shows all.
  const requestedFilter = parseSeriesFilter(searchParams.get('filter'))
  const filter: SeriesFilter = requestedFilter === 'unlinked' && !enhancedHardcoverApi ? 'all' : requestedFilter
  const availableFilters = SERIES_FILTERS.filter(f => f !== 'unlinked' || enhancedHardcoverApi)

  const selectFilter = (next: SeriesFilter) => {
    const params = new URLSearchParams(searchParams)
    if (next === 'all') params.delete('filter')
    else params.set('filter', next)
    setSearchParams(params, { replace: true })
    // Opening a card can move it between Missing and Complete (its exact
    // Hardcover diff replaces the estimate), so the open card stays listed
    // until the user picks another filter; then the list starts clean.
    setExpanded(null)
  }

  const matchesFilter = (series: Series) => {
    switch (filter) {
      case 'missing':
        return seriesMissingCounts(series, enhancedHardcoverApi, diffs[series.id]).displayMissingCount > 0
      case 'complete':
        // A series with no books is not complete, it is empty.
        return (series.books?.length ?? 0) > 0 &&
          seriesMissingCounts(series, enhancedHardcoverApi, diffs[series.id]).displayMissingCount === 0
      case 'unlinked':
        return !series.hardcoverLink
      case 'shortlisted':
        return series.monitored
      default:
        return true
    }
  }

  const filteredSeries = seriesList.filter(series =>
    foldedIncludes(series.title, search) && (series.id === expanded || matchesFilter(series)))

  return (
    <div>
      {confirmDialog}
      <div className="flex items-center justify-between gap-3 flex-wrap mb-6">
        <h2 className="text-2xl font-bold">{t('series.title')}</h2>
        <div className="flex items-center gap-3">
          <span className="text-sm text-slate-600 dark:text-zinc-500">
            {filter === 'all' ? t('series.count', { count: seriesList.length }) : t('series.countFiltered', { shown: filteredSeries.length, total: seriesList.length })}
          </span>
          <button
            onClick={() => setShowAddSeries(true)}
            className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 rounded-md text-sm font-medium transition-colors"
          >
            {t('series.addSeries')}
          </button>
        </div>
      </div>

      <div className="flex flex-col sm:flex-row gap-3 mb-4">
        <input
          enterKeyHint="search"
          type="search"
          value={search}
          onChange={e => setSearch(e.target.value)}
          aria-label={t('series.searchPlaceholder')}
          placeholder={t('series.searchPlaceholder')}
          className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600 placeholder-slate-400 dark:placeholder-zinc-600"
        />
        <div role="group" aria-label={t('series.filterLabel')} className="flex gap-1 pointer-coarse:gap-y-5 flex-wrap items-center">
          {availableFilters.map(f => (
            <button
              key={f}
              type="button"
              aria-pressed={filter === f}
              onClick={() => selectFilter(f)}
              title={f === 'missing' && enhancedHardcoverApi ? t('series.filterMissingHint') : undefined}
              className={`touch-target px-3 py-1 rounded-md text-xs font-medium transition-colors ${filter === f ? 'bg-slate-300 dark:bg-zinc-700 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white'}`}
            >
              {t(`series.filter.${f}`)}
            </button>
          ))}
        </div>
      </div>

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
      ) : seriesList.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500">
          <p className="text-lg mb-2">{t('series.emptyTitle')}</p>
          <p className="text-sm">{t('series.emptyHint')}</p>
        </div>
      ) : filteredSeries.length === 0 ? (
        <div className="text-center py-16 text-slate-600 dark:text-zinc-500" role="status">
          <p>
            {filter === 'all'
              ? t('series.noMatch', { query: search })
              : search.trim()
                ? t('series.noMatchFiltered', { query: search })
                : t('series.noFilterMatch')}
          </p>
        </div>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 items-start">
          {/* items-start (#1682): CSS Grid defaults to align-items:stretch, so
              expanding one series card stretched every other card in the same
              row to match, making it hard to tell which one was actually open. */}
          {filteredSeries.map(series => {
            const books = series.books ?? []
            const bookCount = books.length
            const diff = diffs[series.id]
            const { gapCount, hardcoverMissingCount, displayMissingCount } = seriesMissingCounts(series, enhancedHardcoverApi, diff)
            const fillNeeded = gapCount > 0 || hardcoverMissingCount > 0
            const splitPartIds = new Set(series.splitEditionPartBookIds ?? [])
            const splitPartsToUnmonitor = monitoredSplitParts(series)
            const isOpen = expanded === series.id
            const sortedBooks = [...books].sort((a, b) => {
              const posA = parseFloat(a.positionInSeries) || 0
              const posB = parseFloat(b.positionInSeries) || 0
              return posA - posB
            })

            return (
              <div key={series.id} className="border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 overflow-hidden">
                <div
                  className="p-4 cursor-pointer hover:bg-slate-200/50 dark:hover:bg-zinc-800/50 transition-colors"
                  onClick={() => toggleExpanded(series)}
                >
                  {/* Below sm the name gets its own line and the badges wrap
                      under it: beside them it was cut to about nine
                      characters on a phone. */}
                  <div className="flex flex-wrap sm:flex-nowrap items-start justify-between gap-x-3 gap-y-1">
                    <div className="min-w-0">
                      <h3 className="font-semibold [overflow-wrap:anywhere] sm:truncate">{series.title}</h3>
                      {series.description && (
                        <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1 line-clamp-2">{series.description}</p>
                      )}
                    </div>
                    <div className="flex-shrink-0 flex items-center gap-2">
                      {displayMissingCount > 0 && (
                        <span className="text-xs text-amber-600 dark:text-amber-400 bg-amber-500/10 px-2 py-0.5 rounded-full">
                          {t('series.missingBadge', { count: displayMissingCount })}
                        </span>
                      )}
                      <span className="text-xs text-slate-600 dark:text-zinc-500 bg-slate-200 dark:bg-zinc-800 px-2 py-0.5 rounded-full">
                        {t('series.bookCount', { count: bookCount })}
                      </span>
                      <span className="text-slate-500 dark:text-zinc-600 text-xs">{isOpen ? '▲' : '▼'}</span>
                    </div>
                  </div>
                </div>

                {/* Actions row */}
                <div className="px-4 pb-3 flex items-center gap-3 pointer-coarse:gap-y-5 flex-wrap" onClick={e => e.stopPropagation()}>
                  {/* This flag is a shortlist marker, not a schedule. Nothing
                      reads series.monitored except this page: no job checks a
                      monitored series for new books, and Fill gaps ignores it.
                      Labelling it "Monitor series" promised recurring attention
                      the code never gave, which is what #2523 was filed about.
                      The control stays, the wording no longer overstates it. */}
                  <Switch
                    checked={series.monitored}
                    onChange={() => toggleMonitor(series)}
                    label={series.monitored ? t('series.shortlist.remove') : t('series.shortlist.add')}
                    title={t('series.shortlist.hint')}
                    className="touch-target"
                  >
                    {series.monitored ? t('series.shortlist.on') : t('series.shortlist.off')}
                  </Switch>
                  {enhancedHardcoverApi && (
                    <button
                      onClick={() => openHardcoverLink(series)}
                      disabled={linking === series.id}
                      className={`touch-target text-xs px-2.5 py-1 rounded font-medium border disabled:opacity-50 ${
                        series.hardcoverLink
                          ? 'border-sky-500/40 bg-sky-500/10 text-sky-700 dark:text-sky-300'
                          : 'border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-300'
                      }`}
                      title={series.hardcoverLink ? t('series.hardcover.linkedTo', { title: series.hardcoverLink.hardcoverTitle }) : t('series.hardcover.searchTitle')}
                    >
                      {linking === series.id
                        ? t('series.hardcover.searching')
                        : series.hardcoverLink
                          ? (series.hardcoverLink.linkedBy === 'auto' ? t('series.hardcover.autoLink') : t('series.hardcover.manualLink'))
                          : t('series.hardcover.search')}
                    </button>
                  )}
                  <button
                    onClick={() => setEditingSeries(series)}
                    className="touch-target text-xs px-2.5 py-1 rounded font-medium bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700"
                  >
                    {t('series.rename')}
                  </button>
                  <button
                    onClick={() => setMergeTarget(series)}
                    className="text-xs px-2.5 py-1 rounded font-medium bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700"
                    title={t('series.merge.buttonHint')}
                  >
                    {t('series.merge.button')}
                  </button>
                  {isOpen && (
                    <button
                      onClick={() => setBookModalSeries(series)}
                      className="touch-target text-xs px-2.5 py-1 rounded font-medium bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700"
                    >
                      {t('series.addBook')}
                    </button>
                  )}
                  {isOpen && (
                    <button
                      onClick={() => applySeriesGenres(series)}
                      disabled={applyingGenres === series.id}
                      className="touch-target text-xs px-2.5 py-1 rounded font-medium bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 disabled:opacity-50"
                      title={series.genreOverrideSet
                        ? (series.genreOverride?.length
                          ? t('series.genre.activeHint', { genres: series.genreOverride.join(', ') })
                          : t('series.genre.activeHintEmpty'))
                        : t('series.genre.setHint')}
                    >
                      {applyingGenres === series.id
                        ? '…'
                        : series.genreOverrideSet ? t('series.genre.active') : t('series.genre.set')}
                    </button>
                  )}
                  <button
                    onClick={() => deleteSeries(series)}
                    className={`touch-target ${btn.danger} ${btnSize.sm}`}
                  >
                    {t('common.delete')}
                  </button>
                  {fillNeeded && (
                    <button
                      onClick={() => fillGaps(series)}
                      disabled={filling === series.id}
                      className="touch-target ml-auto text-xs px-2.5 py-1 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded font-medium"
                    >
                      {filling === series.id ? t('series.fill.queuing') : t('series.fill.button')}
                    </button>
                  )}
                  {splitPartsToUnmonitor > 0 && (
                    <button
                      onClick={() => unmonitorSplitParts(series)}
                      disabled={filling === series.id}
                      className="touch-target text-xs px-2.5 py-1 rounded font-medium bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 disabled:opacity-50"
                      title={t('series.splitParts.unmonitorHint')}
                    >
                      {t('series.splitParts.unmonitor', { count: splitPartsToUnmonitor })}
                    </button>
                  )}
                  {fillResult[series.id] && (
                    <span className="ml-auto text-xs text-emerald-600 dark:text-emerald-400">{fillResult[series.id]}</span>
                  )}
                  {!fillResult[series.id] && linkResult[series.id] && (
                    <span className="ml-auto text-xs text-slate-600 dark:text-zinc-400">{linkResult[series.id]}</span>
                  )}
                </div>

                {isOpen && bookCount > 0 && (
                  <div className="border-t border-slate-200 dark:border-zinc-800 divide-y divide-slate-200/50 dark:divide-zinc-800/50">
                    {sortedBooks.map(entry => (
                      <Link
                        key={entry.bookId}
                        to={`/book/${entry.bookId}`}
                        className="flex items-center gap-3 px-4 py-3 bg-slate-100/80 dark:bg-zinc-900/80 hover:bg-slate-200/50 dark:hover:bg-zinc-800/50 transition-colors"
                      >
                        <span className="text-xs text-slate-600 dark:text-zinc-500 w-10 flex-shrink-0 font-mono">
                          #{entry.positionInSeries || '?'}
                        </span>
                        {entry.book?.imageUrl ? (
                          <img loading="lazy" decoding="async"
                            src={entry.book.imageUrl}
                            alt={entry.book.title}
                            className="w-8 h-10 object-cover rounded flex-shrink-0"
                          />
                        ) : (
                          <div className="w-8 h-10 bg-slate-200 dark:bg-zinc-800 rounded flex-shrink-0" />
                        )}
                        <div className="min-w-0">
                          <p className="text-sm font-medium truncate">
                            {entry.book?.title ?? t('series.bookFallback', { id: entry.bookId })}
                          </p>
                          {entry.book?.releaseDate && (
                            <p className="text-xs text-slate-600 dark:text-zinc-500">
                              {new Date(entry.book.releaseDate).getFullYear()}
                            </p>
                          )}
                        </div>
                        <span className="ml-auto flex items-center gap-1 flex-shrink-0">
                          {entry.book?.status && (
                            <span className={`text-xs px-2 py-0.5 rounded ${
                              entry.book.status === 'imported'
                                ? 'bg-emerald-500/20 text-emerald-400'
                                : entry.book.status === 'wanted'
                                ? 'bg-amber-500/20 text-amber-400'
                                : 'bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400'
                            }`}>
                              {t(`bookStatus.${entry.book.status}`, { defaultValue: entry.book.status })}
                            </span>
                          )}
                          {entry.book?.excluded && (
                            <span className="text-xs px-2 py-0.5 rounded bg-amber-500/20 text-amber-700 dark:text-amber-400">
                              {t('series.excluded')}
                            </span>
                          )}
                          {splitPartIds.has(entry.bookId) && (
                            <span
                              className="text-xs px-2 py-0.5 rounded bg-slate-300 dark:bg-zinc-700 text-slate-600 dark:text-zinc-400"
                              title={t('series.splitParts.badgeHint')}
                            >
                              {t('series.splitParts.badge')}
                            </span>
                          )}
                        </span>
                      </Link>
                    ))}
                  </div>
                )}

                {isOpen && bookCount === 0 && (
                  <div className="border-t border-slate-200 dark:border-zinc-800 px-4 py-3 text-sm text-slate-600 dark:text-zinc-500">
                    {t('series.noBooks')}
                  </div>
                )}

                {isOpen && enhancedHardcoverApi && series.hardcoverLink && (
                  <div className="border-t border-slate-200 dark:border-zinc-800 bg-slate-100/80 dark:bg-zinc-900/80">
                    <div className="px-4 py-3 flex items-center justify-between gap-3">
                      <div className="min-w-0">
                        <p className="text-sm font-medium truncate">{t('series.hardcover.heading', { title: series.hardcoverLink.hardcoverTitle })}</p>
                        <p className="text-xs text-slate-600 dark:text-zinc-500">
                          {diff ? t('series.hardcover.diffSummary', { present: diff.presentCount, missing: diff.missingCount }) : t('series.hardcover.checking')}
                        </p>
                        {/* Way back to the record this series is linked to
                            (#1708). Absent until the slug is known, because
                            hardcover.app does not route series on the numeric
                            id Bindery stores. */}
                        {hardcoverSeriesUrl(series.hardcoverLink.hardcoverSlug) && (
                          <a
                            href={hardcoverSeriesUrl(series.hardcoverLink.hardcoverSlug)!}
                            target="_blank"
                            rel="noopener noreferrer"
                            onClick={e => e.stopPropagation()}
                            className="text-xs text-sky-700 dark:text-sky-300 hover:underline mt-0.5 inline-block"
                          >
                            {t('common.viewOnSource', { source: 'Hardcover' })}
                          </a>
                        )}
                      </div>
                      {(diff?.missingCount ?? 0) > 0 && (
                        <div className="flex items-center gap-2 flex-shrink-0">
                          <select
                            aria-label={t('series.hardcover.formatLabel')}
                            value={fillMediaType[series.id] ?? 'ebook'}
                            onChange={e => setFillMediaType(prev => ({ ...prev, [series.id]: e.target.value as MediaType }))}
                            disabled={filling === series.id}
                            className="text-xs bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1 focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600 disabled:opacity-50"
                            title={t('series.hardcover.formatTitle')}
                          >
                            <option value="ebook">📖 {t('common.ebook')}</option>
                            <option value="audiobook">🎧 {t('common.audiobook')}</option>
                            <option value="both">📖🎧 {t('common.both')}</option>
                          </select>
                          <button
                            onClick={() => fillGaps(series, undefined, fillMediaType[series.id] ?? 'ebook')}
                            disabled={filling === series.id}
                            className="text-xs px-2.5 py-1 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded font-medium"
                          >
                            {filling === series.id ? t('series.fill.queuing') : t('series.hardcover.addAll')}
                          </button>
                        </div>
                      )}
                    </div>
                    {diffLoading[series.id] && (
                      <div className="px-4 pb-3 text-sm text-slate-600 dark:text-zinc-500">{t('series.hardcover.loadingBooks')}</div>
                    )}
                    {diffErrors[series.id] && (
                      <div className="px-4 pb-3 text-sm text-rose-600 dark:text-rose-400">{diffErrors[series.id]}</div>
                    )}
                    {diff && diff.missing.length > 0 && (
                      <div className="px-4 pb-4 space-y-2">
                        {diff.missing.slice(0, 8).map(book => {
                          const rowClass = 'flex items-center gap-3 p-3 rounded-md bg-slate-200/50 dark:bg-zinc-800/50'
                          const rowInner = (
                            <>
                              <span className="text-xs text-slate-600 dark:text-zinc-500 w-10 flex-shrink-0 font-mono">
                                #{book.position || '?'}
                              </span>
                              {book.imageUrl ? (
                                <img loading="lazy" decoding="async" src={book.imageUrl} alt={book.title} className="w-8 h-10 object-cover rounded flex-shrink-0" />
                              ) : (
                                <div className="w-8 h-10 bg-slate-200 dark:bg-zinc-800 rounded flex-shrink-0" />
                              )}
                              <div className="min-w-0">
                                <p className="text-sm font-medium truncate">{book.title}</p>
                                {book.authorName && <p className="text-xs text-slate-600 dark:text-zinc-500 truncate">{book.authorName}</p>}
                              </div>
                            </>
                          )
                          // When the missing catalog book maps to an existing library book,
                          // link the row to that book page instead of showing the "add" button.
                          if (book.localBookId != null) {
                            return (
                              <Link
                                key={`${book.foreignBookId}-${book.position}`}
                                to={`/book/${book.localBookId}`}
                                className={`${rowClass} hover:bg-slate-300/50 dark:hover:bg-zinc-700/50 transition-colors`}
                              >
                                {rowInner}
                              </Link>
                            )
                          }
                          return (
                            <div key={`${book.foreignBookId}-${book.position}`} className={rowClass}>
                              {rowInner}
                              <button
                                onClick={() => fillGaps(series, book, fillMediaType[series.id] ?? 'ebook')}
                                disabled={filling === series.id}
                                className="ml-auto text-xs px-2.5 py-1 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded font-medium flex-shrink-0"
                                title={t('series.hardcover.addTitle')}
                              >
                                {filling === series.id ? '…' : t('series.hardcover.add')}
                              </button>
                            </div>
                          )
                        })}
                        {diff.missing.length > 8 && (
                          <p className="text-xs text-slate-600 dark:text-zinc-500 px-1">{t('series.hardcover.moreMissing', { count: diff.missing.length - 8 })}</p>
                        )}
                      </div>
                    )}
                    {/* #2524: a split edition (e.g. "Part 1"/"Part 2") of a book
                        already present is not a missing volume, so it is never
                        counted or offered an add button — just linked to the
                        whole work that covers it. Collapsed by default since
                        this is the exception, not the thing a user came to
                        check. */}
                    {diff && diff.covered.length > 0 && (
                      <details className="px-4 pb-4">
                        <summary className="cursor-pointer select-none text-xs text-slate-600 dark:text-zinc-500 hover:text-slate-900 dark:hover:text-white">
                          {t('series.hardcover.coveredSection', { count: diff.covered.length })}
                        </summary>
                        <div className="mt-2 space-y-2">
                          {diff.covered.map(book => (
                            <Link
                              key={`${book.foreignBookId}-${book.position}`}
                              to={`/book/${book.localBookId}`}
                              className="flex items-center gap-3 p-3 rounded-md bg-slate-200/50 dark:bg-zinc-800/50 hover:bg-slate-300/50 dark:hover:bg-zinc-700/50 transition-colors"
                            >
                              <span className="text-xs text-slate-600 dark:text-zinc-500 w-10 flex-shrink-0 font-mono">
                                #{book.position || '?'}
                              </span>
                              <div className="min-w-0">
                                <p className="text-sm font-medium truncate">{book.title}</p>
                                <p className="text-xs text-slate-600 dark:text-zinc-500 truncate">
                                  {t('series.hardcover.coveredSubtitle', { title: book.localTitle })}
                                </p>
                              </div>
                            </Link>
                          ))}
                        </div>
                      </details>
                    )}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
      {showAddSeries && (
        <SeriesNameModal
          title={t('series.addSeries')}
          submitLabel={t('series.addSeries')}
          onClose={() => setShowAddSeries(false)}
          onSubmit={handleCreateSeries}
        />
      )}
      {editingSeries && (
        <SeriesNameModal
          title={t('series.renameSeries')}
          initialName={editingSeries.title}
          submitLabel={t('common.save')}
          onClose={() => setEditingSeries(null)}
          onSubmit={handleRenameSeries}
        />
      )}
      {mergeTarget && (
        <MergeSeriesModal
          target={mergeTarget}
          series={seriesList}
          onClose={() => setMergeTarget(null)}
          onMerged={() => { void refreshSeriesList() }}
        />
      )}
      {bookModalSeries && (
        <AddSeriesBookModal
          series={bookModalSeries}
          onClose={() => setBookModalSeries(null)}
          onLinked={handleBookLinked}
        />
      )}
      {enhancedHardcoverApi && linkModalSeries && (
        <HardcoverSeriesLinkModal
          series={linkModalSeries}
          initialResults={linkModalResults}
          onClose={() => setLinkModalSeries(null)}
          onLinked={handleHardcoverLinked}
        />
      )}
    </div>
  )
}
