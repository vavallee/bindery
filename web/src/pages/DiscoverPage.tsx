import { useEffect, useState, useCallback, useMemo } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, Recommendation } from '../api/client'
import RecommendationRow from '../components/RecommendationRow'

const ROW_ORDER: Array<{ type: string; labelKey: string }> = [
  { type: 'series', labelKey: 'discover.rows.series' },
  { type: 'author_new', labelKey: 'discover.rows.author_new' },
  { type: 'genre_similar', labelKey: 'discover.rows.genre_similar' },
  { type: 'serendipity', labelKey: 'discover.rows.serendipity' },
  { type: 'genre_popular', labelKey: 'discover.rows.genre_popular' },
  { type: 'list_cross', labelKey: 'discover.rows.list_cross' },
]

export default function DiscoverPage() {
  const { t } = useTranslation()
  const [recs, setRecs] = useState<Recommendation[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [enabled, setEnabled] = useState<boolean | null>(null)
  const [toast, setToast] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const data = await api.listRecommendations({ limit: 100 })
      setRecs(data)
    } catch {
      setRecs([])
    } finally {
      setLoading(false)
    }
  }, [])

  const checkEnabled = useCallback(async () => {
    try {
      const settings = await api.listSettings()
      const setting = settings.find(s => s.key === 'recommendations.enabled')
      setEnabled(setting?.value === 'true')
    } catch {
      setEnabled(true) // assume enabled if settings check fails
    }
  }, [])

  useEffect(() => {
    load()
    checkEnabled()
  }, [load, checkEnabled])

  useEffect(() => {
    document.title = 'Discover · Bindery'
    return () => { document.title = 'Bindery' }
  }, [])

  // Auto-dismiss the toast 2.5s after it appears. Lives in an effect with
  // cleanup so the timer is cleared if the page unmounts (or the toast is
  // replaced) before it fires.
  useEffect(() => {
    if (!toast) return
    const t = setTimeout(() => setToast(null), 2500)
    return () => clearTimeout(t)
  }, [toast])

  const showToast = (msg: string) => {
    setToast(msg)
  }

  const grouped = useMemo(() => {
    const map: Record<string, Recommendation[]> = {}
    for (const rec of recs) {
      if (!map[rec.recType]) map[rec.recType] = []
      map[rec.recType].push(rec)
    }
    return map
  }, [recs])

  const handleDismiss = async (id: number) => {
    setRecs(prev => prev.filter(r => r.id !== id))
    try {
      await api.dismissRecommendation(id)
    } catch {
      // optimistic — already removed from UI
    }
  }

  const handleAdd = async (id: number) => {
    setRecs(prev => prev.filter(r => r.id !== id))
    showToast(t('discover.addedToWanted'))
    try {
      await api.addRecommendation(id)
    } catch {
      // optimistic — already removed from UI
    }
  }

  const handleExcludeAuthor = async (authorName: string) => {
    setRecs(prev => prev.filter(r => r.authorName !== authorName))
    try {
      await api.addAuthorExclusion(authorName)
    } catch {
      // optimistic — already removed from UI
    }
  }

  const handleRefresh = async () => {
    setRefreshing(true)
    try {
      await api.refreshRecommendations()
      // Wait for background regeneration then re-fetch
      setTimeout(() => {
        load().finally(() => setRefreshing(false))
      }, 2000)
    } catch {
      setRefreshing(false)
    }
  }

  // Detect cold start: have series/author_new but no genre_similar/genre_popular
  const hasSeries = (grouped['series']?.length ?? 0) > 0
  const hasAuthorNew = (grouped['author_new']?.length ?? 0) > 0
  const hasGenreSimilar = (grouped['genre_similar']?.length ?? 0) > 0
  const hasGenrePopular = (grouped['genre_popular']?.length ?? 0) > 0
  const isColdStart = (hasSeries || hasAuthorNew) && !hasGenreSimilar && !hasGenrePopular

  const hasAnyRecs = recs.length > 0

  return (
    <div>
      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-2xl font-bold">{t('discover.title')}</h2>
          <p className="text-sm text-slate-500 dark:text-zinc-500">{t('discover.subtitle')}</p>
        </div>
        <button
          onClick={handleRefresh}
          disabled={refreshing}
          className="px-4 py-2 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded-lg text-sm font-medium disabled:opacity-50 transition-colors"
        >
          {refreshing ? t('discover.refreshing') : t('discover.refresh')}
        </button>
      </div>

      {/* Toast */}
      {toast && (
        <div className="fixed bottom-6 right-6 z-50 px-4 py-2.5 bg-emerald-600 text-white rounded-lg shadow-lg text-sm font-medium animate-fade-in">
          {toast}
        </div>
      )}

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
      ) : !hasAnyRecs && enabled === false ? (
        /* Disabled state */
        <div className="text-center py-20">
          <svg className="w-16 h-16 mx-auto mb-4 text-slate-300 dark:text-zinc-700" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5} d="M9.663 17h4.673M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.343-5.657l-.707-.707m2.828 9.9a5.002 5.002 0 017.072 0" />
          </svg>
          <p className="text-slate-600 dark:text-zinc-400 mb-4">{t('discover.empty.disabled')}</p>
          <Link
            to="/settings"
            className="inline-block px-4 py-2 bg-emerald-600 hover:bg-emerald-500 text-white rounded-lg text-sm font-medium transition-colors"
          >
            {t('discover.empty.goToSettings')}
          </Link>
        </div>
      ) : !hasAnyRecs ? (
        /* Empty state — not enough data */
        <div className="text-center py-20">
          <svg className="w-16 h-16 mx-auto mb-4 text-slate-300 dark:text-zinc-700" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5} d="M12 6.253v13m0-13C10.832 5.477 9.246 5 7.5 5S4.168 5.477 3 6.253v13C4.168 18.477 5.754 18 7.5 18s3.332.477 4.5 1.253m0-13C13.168 5.477 14.754 5 16.5 5c1.747 0 3.332.477 4.5 1.253v13C19.832 18.477 18.247 18 16.5 18c-1.746 0-3.332.477-4.5 1.253" />
          </svg>
          <p className="text-slate-600 dark:text-zinc-400">{t('discover.empty.noRecs')}</p>
        </div>
      ) : (
        <>
          {/* Cold start note */}
          {isColdStart && (
            <div className="mb-6 px-4 py-3 bg-slate-100 dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 rounded-lg">
              <p className="text-sm text-slate-500 dark:text-zinc-500">{t('discover.empty.coldStart')}</p>
            </div>
          )}

          {/* Recommendation rows */}
          {ROW_ORDER.map(row => (
            <RecommendationRow
              key={row.type}
              title={t(row.labelKey)}
              recommendations={grouped[row.type] ?? []}
              onDismiss={handleDismiss}
              onAdd={handleAdd}
              onExcludeAuthor={handleExcludeAuthor}
            />
          ))}
        </>
      )}
    </div>
  )
}
