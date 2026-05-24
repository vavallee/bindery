import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, ABSConfig, ABSImportProgress, ABSImportRun, ABSLibrary, ABSMetadataConflict, ABSReviewItem, ABSRollbackAction, ABSRollbackResult, ABSTestResult, Author, Book } from '../../api/client'
import ABSConflictPanel from '../../components/ABSAuthorConflictsPanel'
import { inputCls } from './formStyles'
import PathRemapField from './PathRemapField'

const absReviewResultLimit = 10

function uniqueTrimmed(values: string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const value of values) {
    const trimmed = value.trim()
    if (!trimmed || seen.has(trimmed)) continue
    seen.add(trimmed)
    out.push(trimmed)
  }
  return out
}

function absConfigLibraryIds(config: ABSConfig): string[] {
  return uniqueTrimmed(config.libraryIds?.length ? config.libraryIds : config.libraryId ? [config.libraryId] : [])
}

function sameStrings(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  return a.every((value, idx) => value === b[idx])
}

export default function ABSTab() {
  return (
    <div className="space-y-8">
      <AudiobookshelfSection />
    </div>
  )
}

function AudiobookshelfSection() {
  const { t } = useTranslation()
  const [config, setConfig] = useState<ABSConfig | null>(null)
  const [draft, setDraft] = useState({ baseUrl: '', apiKey: '', label: 'Audiobookshelf', enabled: false, libraryIds: [] as string[], pathRemap: '' })
  const [libraries, setLibraries] = useState<ABSLibrary[]>([])
  const [showAuthorConflicts, setShowAuthorConflicts] = useState(true)
  const [showReviewItems, setShowReviewItems] = useState(true)
  const [showBookConflicts, setShowBookConflicts] = useState(true)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [listing, setListing] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [libraryError, setLibraryError] = useState<string | null>(null)
  const [testError, setTestError] = useState<string | null>(null)
  const [testResult, setTestResult] = useState<ABSTestResult | null>(null)
  const [importProgress, setImportProgress] = useState<ABSImportProgress | null>(null)
  const [importDryRun, setImportDryRun] = useState(true)
  const [importError, setImportError] = useState<string | null>(null)
  const [runs, setRuns] = useState<ABSImportRun[]>([])
  const [rollbackResult, setRollbackResult] = useState<ABSRollbackResult | null>(null)
  const [rollbackError, setRollbackError] = useState<string | null>(null)
  const [rollbackPreviewingRunId, setRollbackPreviewingRunId] = useState<number | null>(null)
  const [rollbackApplyingRunId, setRollbackApplyingRunId] = useState<number | null>(null)
  const [conflicts, setConflicts] = useState<ABSMetadataConflict[]>([])
  const [reviewItems, setReviewItems] = useState<ABSReviewItem[]>([])
  const [reviewError, setReviewError] = useState<string | null>(null)
  const [reviewActionId, setReviewActionId] = useState<number | null>(null)
  const [dismissingRunId, setDismissingRunId] = useState<number | null>(null)
  const [authorQueries, setAuthorQueries] = useState<Record<number, string>>({})
  const [authorResults, setAuthorResults] = useState<Record<number, Author[]>>({})
  const [authorSearchId, setAuthorSearchId] = useState<number | null>(null)
  const [bookQueries, setBookQueries] = useState<Record<number, string>>({})
  const [bookResults, setBookResults] = useState<Record<number, Book[]>>({})
  const [bookSearchId, setBookSearchId] = useState<number | null>(null)
  const [conflictError, setConflictError] = useState<string | null>(null)
  const [resolvingConflictId, setResolvingConflictId] = useState<number | null>(null)
  const [relinkingAuthorId, setRelinkingAuthorId] = useState<number | null>(null)

  const applyConfig = (next: ABSConfig) => {
    setConfig(next)
    setDraft(prev => ({
      ...prev,
      baseUrl: next.baseUrl ?? '',
      apiKey: '',
      label: next.label || 'Audiobookshelf',
      enabled: next.enabled,
      libraryIds: absConfigLibraryIds(next),
      pathRemap: next.pathRemap ?? '',
    }))
  }

  const refreshConflicts = async () => {
    try {
      setConflictError(null)
      const page = await api.absConflicts()
      setConflicts(page.items)
    } catch (err: unknown) {
      setConflictError(err instanceof Error ? err.message : 'Failed to load enrichment conflicts')
    }
  }

  const refreshReviewItems = async () => {
    try {
      setReviewError(null)
      const page = await api.absReviewItems()
      setReviewItems(page.items)
    } catch (err: unknown) {
      setReviewError(err instanceof Error ? err.message : 'Failed to load review items')
    }
  }

  const refreshRuns = async () => {
    try {
      setRuns(await api.absImportRuns())
    } catch {
      setRuns([])
    }
  }

  const loadLibraries = async (payload?: { baseUrl?: string; apiKey?: string }) => {
    setListing(true)
    setLibraryError(null)
    try {
      const next = await api.absLibraries(payload)
      setLibraries(next)
    } catch (err: unknown) {
      setLibraryError(err instanceof Error ? err.message : 'Failed to load libraries')
    } finally {
      setListing(false)
    }
  }

  useEffect(() => {
    api.absConfig()
      .then(next => {
        applyConfig(next)
        if (next.baseUrl && next.apiKeyConfigured) {
          api.absLibraries()
            .then(setLibraries)
            .catch(() => {})
        }
      })
      .catch(err => setSaveError(err instanceof Error ? err.message : 'Failed to load Audiobookshelf config'))
      .finally(() => setLoading(false))
    api.absImportStatus().then(setImportProgress).catch(() => {})
    refreshRuns().catch(() => {})
    refreshReviewItems().catch(() => {})
    refreshConflicts().catch(() => {})
  }, [])

  useEffect(() => {
    if (!importProgress?.running) return
    const id = setInterval(() => {
      api.absImportStatus().then(setImportProgress).catch(() => {})
    }, 1200)
    return () => clearInterval(id)
  }, [importProgress?.running])

  useEffect(() => {
    if (importProgress?.running) return
    refreshRuns().catch(() => {})
    refreshReviewItems().catch(() => {})
    refreshConflicts().catch(() => {})
  }, [importProgress?.running, importProgress?.finishedAt])

  const probePayload = () => ({
    baseUrl: draft.baseUrl.trim(),
    ...(draft.apiKey.trim() ? { apiKey: draft.apiKey.trim() } : {}),
  })

  const save = async () => {
    setSaving(true)
    setSaveError(null)
    try {
      const libraryIds = uniqueTrimmed(draft.libraryIds)
      const next = await api.absSetConfig({
        baseUrl: draft.baseUrl.trim(),
        apiKey: draft.apiKey.trim() || undefined,
        label: draft.label.trim() || 'Audiobookshelf',
        enabled: draft.enabled,
        libraryId: libraryIds[0] ?? '',
        libraryIds,
        pathRemap: draft.pathRemap.trim(),
      })
      applyConfig(next)
      if (draft.baseUrl.trim() && (draft.apiKey.trim() || next.apiKeyConfigured)) {
        loadLibraries(probePayload()).catch(() => {})
      }
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : 'Failed to save Audiobookshelf settings')
    } finally {
      setSaving(false)
    }
  }

  const testConnection = async () => {
    setTesting(true)
    setTestError(null)
    setTestResult(null)
    try {
      const next = await api.absTest(probePayload())
      setTestResult(next)
      if (next.defaultLibraryId) {
        setDraft(prev => prev.libraryIds.length ? prev : { ...prev, libraryIds: [next.defaultLibraryId] })
      }
      if (draft.baseUrl.trim() && (draft.apiKey.trim() || config?.apiKeyConfigured)) {
        loadLibraries(probePayload()).catch(() => {})
      }
    } catch (err: unknown) {
      setTestError(err instanceof Error ? err.message : 'Connection test failed')
    } finally {
      setTesting(false)
    }
  }

  const startImport = async (dryRun: boolean) => {
    setImportError(null)
    setRollbackError(null)
    setRollbackResult(null)
    if (hasUnsavedABSConfig) {
      setImportError('Save Audiobookshelf settings before starting an import')
      return
    }
    try {
      const next = await api.absImportStart({
        dryRun,
      })
      setImportProgress(next)
    } catch (err: unknown) {
      setImportError(err instanceof Error ? err.message : 'Import failed to start')
    }
  }

  const previewRollback = async (runId: number) => {
    setRollbackPreviewingRunId(runId)
    setRollbackError(null)
    try {
      setRollbackResult(await api.absImportRollbackPreview(runId))
    } catch (err: unknown) {
      setRollbackError(err instanceof Error ? err.message : 'Failed to preview rollback')
    } finally {
      setRollbackPreviewingRunId(null)
    }
  }

  const applyRollback = async (runId: number) => {
    setRollbackApplyingRunId(runId)
    setRollbackError(null)
    try {
      const result = await api.absImportRollback(runId)
      setRollbackResult(result)
      setRuns(prev => prev.map(run => run.id === runId ? { ...run, status: result.status } : run))
      await refreshRuns()
    } catch (err: unknown) {
      setRollbackError(err instanceof Error ? err.message : 'Failed to roll back import run')
    } finally {
      setRollbackApplyingRunId(null)
    }
  }

  const resolveConflict = async (id: number, source: 'abs' | 'upstream') => {
    setResolvingConflictId(id)
    setConflictError(null)
    try {
      const updated = await api.resolveAbsConflict(id, source)
      setConflicts(prev => prev.map(conflict => conflict.id === id ? updated : conflict))
    } catch (err: unknown) {
      setConflictError(err instanceof Error ? err.message : 'Failed to resolve conflict')
    } finally {
      setResolvingConflictId(null)
    }
  }

  const relinkConflictAuthor = async (localId: number) => {
    setRelinkingAuthorId(localId)
    setConflictError(null)
    try {
      await api.relinkAuthorUpstream(localId)
      await refreshConflicts()
    } catch (err: unknown) {
      setConflictError(err instanceof Error ? err.message : 'Failed to relink author')
    } finally {
      setRelinkingAuthorId(null)
    }
  }

  const refreshConflictsQuietly = () => {
    refreshConflicts().catch(() => {})
  }

  const reviewReasonLabel = (reason: ABSReviewItem['reviewReason']) => {
    switch (reason) {
      case 'unmatched_author':
        return 'No confident author match'
      case 'ambiguous_author':
        return 'Multiple author matches'
      case 'unmatched_book':
        return 'No confident book match'
      case 'ambiguous_book':
        return 'Multiple book matches'
      default:
        return reason
    }
  }

  const approveReviewItem = async (id: number) => {
    setReviewActionId(id)
    setReviewError(null)
    try {
      await api.approveAbsReviewItem(id)
      await refreshReviewItems()
      api.absImportStatus().then(setImportProgress).catch(() => {})
      refreshRuns().catch(() => {})
    } catch (err: unknown) {
      setReviewError(err instanceof Error ? err.message : 'Failed to import review item')
    } finally {
      setReviewActionId(null)
    }
  }

  const dismissReviewItem = async (id: number) => {
    setReviewActionId(id)
    setReviewError(null)
    try {
      await api.dismissAbsReviewItem(id)
      await refreshReviewItems()
    } catch (err: unknown) {
      setReviewError(err instanceof Error ? err.message : 'Failed to dismiss review item')
    } finally {
      setReviewActionId(null)
    }
  }

  const dismissReviewRun = async (runId: number, count: number) => {
    if (!confirm(t('settings.abs.review.dismissRunConfirm', { count, runId, defaultValue: 'Dismiss {{count}} review items from run #{{runId}}? This cannot be undone.' }))) return
    setDismissingRunId(runId)
    setReviewError(null)
    try {
      await api.dismissAbsReviewRun(runId)
      await refreshReviewItems()
    } catch (err: unknown) {
      setReviewError(err instanceof Error ? err.message : 'Failed to dismiss review run')
    } finally {
      setDismissingRunId(null)
    }
  }

  const searchReviewAuthors = async (item: ABSReviewItem) => {
    const query = (authorQueries[item.id] ?? item.primaryAuthor).trim()
    if (!query) return
    setAuthorSearchId(item.id)
    setReviewError(null)
    try {
      const results = await api.searchAuthors(query)
      setAuthorResults(prev => ({ ...prev, [item.id]: results }))
    } catch (err: unknown) {
      setReviewError(err instanceof Error ? err.message : 'Author search failed')
    } finally {
      setAuthorSearchId(null)
    }
  }

  const resolveReviewAuthor = async (item: ABSReviewItem, author: Author) => {
    setReviewActionId(item.id)
    setReviewError(null)
    try {
      await api.resolveAbsReviewAuthor(item.id, {
        foreignAuthorId: author.foreignAuthorId,
        authorName: author.authorName,
        applyTo: 'same_author',
      })
      await refreshReviewItems()
      setAuthorResults(prev => ({ ...prev, [item.id]: [] }))
    } catch (err: unknown) {
      setReviewError(err instanceof Error ? err.message : 'Failed to resolve author')
    } finally {
      setReviewActionId(null)
    }
  }

  const searchReviewBooks = async (item: ABSReviewItem) => {
    const query = (bookQueries[item.id] ?? item.editedTitle ?? item.title).trim()
    if (!query) return
    setBookSearchId(item.id)
    setReviewError(null)
    try {
      const results = await api.searchBooks(query)
      setBookResults(prev => ({ ...prev, [item.id]: results }))
    } catch (err: unknown) {
      setReviewError(err instanceof Error ? err.message : 'Book search failed')
    } finally {
      setBookSearchId(null)
    }
  }

  const groupedReviewItems = useMemo(() => {
    type Group = { runId: number | null; items: ABSReviewItem[] }
    const order: (number | null)[] = []
    const map = new Map<number | null, ABSReviewItem[]>()
    for (const item of reviewItems) {
      const key = typeof item.latestRunId === 'number' && item.latestRunId > 0 ? item.latestRunId : null
      const bucket = map.get(key)
      if (bucket) {
        bucket.push(item)
      } else {
        map.set(key, [item])
        order.push(key)
      }
    }
    // Most recent run first (numerically highest id), unknown last.
    const numeric = order.filter((k): k is number => k !== null).sort((a, b) => b - a)
    const hasUnknown = order.includes(null)
    const sortedKeys: (number | null)[] = hasUnknown ? [...numeric, null] : numeric
    return sortedKeys.map<Group>(runId => ({ runId, items: map.get(runId) ?? [] }))
  }, [reviewItems])

  const resolveReviewBook = async (item: ABSReviewItem, book: Book) => {
    setReviewActionId(item.id)
    setReviewError(null)
    const editedTitle = (bookQueries[item.id] ?? item.editedTitle ?? item.title).trim()
    const authorForeignId = book.author?.foreignAuthorId?.trim() ?? ''
    const authorName = book.author?.authorName?.trim() ?? ''
    try {
      if (!item.resolvedAuthorForeignId?.trim() && authorForeignId && authorName) {
        await api.resolveAbsReviewAuthor(item.id, {
          foreignAuthorId: authorForeignId,
          authorName,
          applyTo: 'same_author',
        })
      }
      const updated = await api.resolveAbsReviewBook(item.id, {
        foreignBookId: book.foreignBookId,
        title: book.title,
        editedTitle,
      })
      setReviewItems(prev => prev.map(current => current.id === item.id ? updated : current))
      setBookResults(prev => ({ ...prev, [item.id]: [] }))
    } catch (err: unknown) {
      setReviewError(err instanceof Error ? err.message : 'Failed to resolve book')
    } finally {
      setReviewActionId(null)
    }
  }

  if (loading) {
    return <section className="text-sm text-slate-500 dark:text-zinc-500">Loading Audiobookshelf…</section>
  }

  const hasImportCredentials = Boolean(draft.apiKey.trim() || config?.apiKeyConfigured)
  const effectiveLibraryIds = draft.libraryIds.length > 0 ? draft.libraryIds : testResult?.defaultLibraryId ? [testResult.defaultLibraryId] : []
  const savedBaseUrl = config?.baseUrl ?? ''
  const savedLabel = config?.label || 'Audiobookshelf'
  const savedLibraryIds = config ? absConfigLibraryIds(config) : []
  const savedPathRemap = config?.pathRemap ?? ''
  const hasUnsavedABSConfig = Boolean(config) && (
    draft.baseUrl.trim() !== savedBaseUrl.trim() ||
    (draft.label.trim() || 'Audiobookshelf') !== savedLabel ||
    draft.enabled !== config?.enabled ||
    !sameStrings(uniqueTrimmed(draft.libraryIds), savedLibraryIds) ||
    draft.pathRemap.trim() !== savedPathRemap.trim() ||
    Boolean(draft.apiKey.trim())
  )
  const canStartImport = !importProgress?.running && !hasUnsavedABSConfig && Boolean(config?.enabled) && Boolean(config?.apiKeyConfigured) && Boolean(savedBaseUrl.trim()) && savedLibraryIds.length > 0
  const libraryRows = [
    ...libraries,
    ...draft.libraryIds
      .filter(id => !libraries.some(lib => lib.id === id))
      .map(id => ({ id, name: id, mediaType: 'book', icon: '', provider: '', folders: [] } as ABSLibrary)),
  ]
  const libraryNameById = new Map(libraryRows.map(lib => [lib.id, lib.name || lib.id]))
  const absRunStatusLabel = (status: string) => {
    switch (status) {
      case 'rolled_back':
        return 'rolled back'
      case 'completed':
      case 'failed':
      case 'running':
        return status
      default:
        return status.replace(/_/g, ' ')
    }
  }
  const absRunLibraryLabel = (run: ABSImportRun) => {
    const libraryId = (run.libraryId || run.source.libraryId || '').trim()
    if (!libraryId) return 'Unknown library'
    const name = libraryNameById.get(libraryId)?.trim()
    if (name && name !== libraryId) return `${name} (${libraryId})`
    return libraryId
  }
  const rollbackActionName = (action: ABSRollbackAction) => action.displayName?.trim() || action.externalId

  const rollbackActionLabel = (action: ABSRollbackAction) => {
    const name = rollbackActionName(action)
    switch (action.action) {
      case 'delete_author':
        return `Delete author ${name}`
      case 'delete_book':
        return `Delete book ${name}`
      case 'delete_edition':
        return `Delete edition ${name}`
      case 'delete_series':
        return `Delete series ${name}`
      case 'restore_book':
        return `Restore book metadata ${name}`
      case 'unlink_series':
        return `Remove series link ${name}`
      case 'unlink_provenance':
        return `Remove ABS link ${name}`
      case 'skip':
        return `Retain ${action.entityType} ${name}`
      default:
        return `${action.action.replace(/_/g, ' ')} ${name}`
    }
  }
  const rollbackActionDetail = (action: ABSRollbackAction) =>
    action.reason || `${action.entityType}${action.localId ? ` #${action.localId}` : ''}`
  const rollbackPreviewChanges = rollbackResult?.actions.filter(action => action.action !== 'skip') ?? []
  const rollbackPreviewRetained = rollbackResult?.actions.filter(action => action.action === 'skip') ?? []

  return (
    <section>
      <div className="flex items-center justify-between mb-2 gap-4">
        <div>
          <h3 className="text-base font-semibold text-slate-800 dark:text-zinc-200">Audiobookshelf</h3>
          <p className="text-xs text-slate-600 dark:text-zinc-500 mt-1">
            Configure one ABS source, test API-key auth, pick book libraries, then import ABS metadata into the Bindery catalog.
          </p>
          {draft.enabled && (
            <p className="text-[11px] text-sky-700 dark:text-sky-300 mt-1 max-w-xl">
              For better initial matching, it helps to review the selected ABS libraries first and match audiobooks to Audible sources so ASINs are already present before import.
            </p>
          )}
        </div>
        <button
          onClick={() => setDraft(prev => ({ ...prev, enabled: !prev.enabled }))}
          className={`relative w-11 h-6 rounded-full transition-colors flex-shrink-0 ${draft.enabled ? 'bg-emerald-600' : 'bg-slate-300 dark:bg-zinc-700'}`}
          title={draft.enabled ? 'Disable Audiobookshelf source' : 'Enable Audiobookshelf source'}
          aria-label={draft.enabled ? 'Disable Audiobookshelf source' : 'Enable Audiobookshelf source'}
        >
          <span className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full transition-transform ${draft.enabled ? 'translate-x-5' : ''}`} />
        </button>
      </div>

      <div className="mb-4 p-3 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-50 dark:bg-zinc-950">
        <div className="flex items-center justify-between gap-3">
          <div>
            <h4 className="text-sm font-medium text-slate-800 dark:text-zinc-200">Libraries</h4>
            <p className="text-[11px] text-slate-600 dark:text-zinc-500 mt-0.5">
              Select each ABS book library to import from this source.
            </p>
          </div>
          <span className="text-[11px] text-slate-500 dark:text-zinc-500 whitespace-nowrap">
            {draft.libraryIds.length} selected
          </span>
        </div>
        {libraryRows.length > 0 ? (
          <div className="mt-3 grid gap-2 sm:grid-cols-2">
            {libraryRows.map(lib => {
              const checked = draft.libraryIds.includes(lib.id)
              return (
                <label
                  key={lib.id}
                  className={`flex items-center gap-3 rounded border px-3 py-2 text-sm cursor-pointer transition-colors ${
                    checked
                      ? 'border-emerald-500/70 bg-emerald-500/10 text-slate-900 dark:text-zinc-100'
                      : 'border-slate-200 dark:border-zinc-800 bg-slate-100 dark:bg-zinc-900 text-slate-700 dark:text-zinc-300'
                  }`}
                >
                  <input
                    type="checkbox"
                    checked={checked}
                    onChange={e => {
                      const selected = e.target.checked
                      setDraft(prev => {
                        const next = selected
                          ? [...prev.libraryIds, lib.id]
                          : prev.libraryIds.filter(id => id !== lib.id)
                        return { ...prev, libraryIds: uniqueTrimmed(next) }
                      })
                    }}
                  />
                  <span className="min-w-0">
                    <span className="block truncate font-medium">{lib.name || lib.id}</span>
                    <span className="block truncate text-[11px] text-slate-500 dark:text-zinc-500">
                      {lib.provider ? `${lib.provider} · ${lib.id}` : lib.id}
                    </span>
                  </span>
                </label>
              )
            })}
          </div>
        ) : (
          <p className="mt-3 text-xs text-slate-500 dark:text-zinc-500">
            No libraries selected. Use Test connection or List libraries to discover accessible book libraries.
          </p>
        )}
        {libraryError && <div className="mt-2 text-sm text-red-600 dark:text-red-400">{libraryError}</div>}
      </div>

      <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-4">
        <div className="grid gap-4 md:grid-cols-2">
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Source label</label>
            <input
              value={draft.label}
              onChange={e => setDraft(prev => ({ ...prev, label: e.target.value }))}
              placeholder="Audiobookshelf"
              className={inputCls}
            />
          </div>
          <div>
            <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Base URL</label>
            <input
              value={draft.baseUrl}
              onChange={e => setDraft(prev => ({ ...prev, baseUrl: e.target.value }))}
              placeholder="https://abs.example.com"
              className={inputCls}
            />
          </div>
        </div>

        <div>
          <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">API key</label>
          <input
            value={draft.apiKey}
            onChange={e => setDraft(prev => ({ ...prev, apiKey: e.target.value }))}
            type="password"
            placeholder={config?.apiKeyConfigured ? 'Saved key is hidden. Enter a new key to rotate it.' : 'Paste an ABS API key'}
            className={inputCls}
          />
          <p className="text-[11px] text-slate-500 dark:text-zinc-500 mt-1">
            {config?.apiKeyConfigured
              ? 'A key is already stored and stays write-only. Leaving this blank keeps the saved key. Use a key for a user that can access the selected book libraries; ABS admin permissions are not required.'
              : 'Use a key for a user that can access the selected book libraries. ABS admin permissions are not required, and the saved key never comes back to the browser after you store it.'}
          </p>
        </div>

        {testResult && (
          <div className="px-3 py-2 rounded border border-emerald-300 dark:border-emerald-800 bg-emerald-50 dark:bg-emerald-950/30 text-sm text-emerald-800 dark:text-emerald-300">
            Connected as <span className="font-medium">{testResult.username}</span>
            {testResult.serverVersion ? ` on ABS ${testResult.serverVersion}` : ''}
            {testResult.defaultLibraryId ? ` · default library ${testResult.defaultLibraryId}` : ''}
          </div>
        )}
        {saveError && <div className="text-sm text-red-600 dark:text-red-400">{saveError}</div>}
        {testError && <div className="text-sm text-red-600 dark:text-red-400">{testError}</div>}
        {libraryError && <div className="text-sm text-red-600 dark:text-red-400">{libraryError}</div>}

        <PathRemapField
          id="abs-path-remap"
          label="ABS path remap"
          value={draft.pathRemap}
          onChange={value => setDraft(prev => ({ ...prev, pathRemap: value }))}
          placeholder="/audiobookshelf:/books/audiobookshelf,/abs:/books"
          help="Optional and separate from download-client remaps. Use when ABS reports shared-storage paths that Bindery sees under a different mount."
        />

        <div className="flex flex-wrap gap-2">
          <button
            onClick={save}
            disabled={saving}
            className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium disabled:opacity-50"
          >
            {saving ? 'Saving…' : 'Save source'}
          </button>
          <button
            onClick={testConnection}
            disabled={testing}
            className="px-3 py-2 bg-slate-700 hover:bg-slate-600 rounded text-sm font-medium disabled:opacity-50"
          >
            {testing ? 'Testing…' : 'Test connection'}
          </button>
          <button
            onClick={() => loadLibraries(probePayload())}
            disabled={listing}
            className="px-3 py-2 bg-slate-700 hover:bg-slate-600 rounded text-sm font-medium disabled:opacity-50"
          >
            {listing ? 'Loading…' : 'List libraries'}
          </button>
        </div>

        <div className="pt-3 border-t border-slate-200 dark:border-zinc-800 space-y-3">
          <div className="flex items-center justify-between gap-4">
            <div>
              <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200">Catalog import</label>
              <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">
                Import authors, books, series, and editions from the selected ABS libraries. Shared filesystem paths are reconciled into Bindery ownership automatically; when ABS and Bindery use different mount prefixes, add a path translation above. Non-visible paths stay metadata-only and are counted for manual follow-up.
              </p>
            </div>
            <div className="flex flex-col items-end gap-2 flex-shrink-0">
              <label className="flex items-center gap-2 text-xs text-slate-600 dark:text-zinc-400">
                <input
                  type="checkbox"
                  checked={importDryRun}
                  onChange={e => setImportDryRun(e.target.checked)}
                />
                Start with dry-run
              </label>
              <div className="flex gap-2">
                <button
                  onClick={() => startImport(true)}
                  disabled={!canStartImport}
                  title={hasUnsavedABSConfig ? 'Save Audiobookshelf settings before starting an import' : undefined}
                  className="px-3 py-2 bg-slate-700 hover:bg-slate-600 rounded text-sm font-medium disabled:opacity-50"
                >
                  Preview changes
                </button>
                <button
                  onClick={() => startImport(importDryRun)}
                  disabled={!canStartImport}
                  title={hasUnsavedABSConfig ? 'Save Audiobookshelf settings before starting an import' : undefined}
                  className="px-4 py-2 bg-sky-600 hover:bg-sky-500 rounded text-sm font-medium disabled:opacity-50"
                >
                  {importProgress?.running ? 'Running…' : importDryRun ? 'Run selected mode' : 'Import libraries'}
                </button>
              </div>
            </div>
          </div>

          {hasUnsavedABSConfig && (
            <p className="text-[11px] text-amber-700 dark:text-amber-300">
              Save Audiobookshelf settings before starting an import so the run uses the stored source configuration.
            </p>
          )}
          {!hasUnsavedABSConfig && effectiveLibraryIds.length === 0 && hasImportCredentials && (
            <p className="text-[11px] text-amber-700 dark:text-amber-300">
              Choose at least one library or use "Test connection" / "List libraries" to load accessible book libraries before starting an import.
            </p>
          )}

          {importError && <div className="text-sm text-red-600 dark:text-red-400">{importError}</div>}

          {importProgress && (importProgress.running || importProgress.stats || importProgress.error) && (
            <div className="rounded border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-2 space-y-2">
              {importProgress.running && (
                <div className="flex justify-between text-xs text-slate-600 dark:text-zinc-400 gap-3">
                  <span>{importProgress.message || 'Working…'}</span>
                  <span>{importProgress.processed} processed</span>
                </div>
              )}
              {importProgress.running && importProgress.resumedFromCheckpoint && importProgress.checkpoint && (
                <p className="text-[11px] text-amber-700 dark:text-amber-300">
                  Resumed from page {importProgress.checkpoint.page}{importProgress.checkpoint.lastItemId ? ` after ${importProgress.checkpoint.lastItemId}` : ''}.
                </p>
              )}
              {!importProgress.running && importProgress.error && (
                <p className="text-xs text-red-600 dark:text-red-400">{importProgress.dryRun ? 'Dry-run failed' : 'Import failed'}: {importProgress.error}</p>
              )}
              {!importProgress.running && importProgress.stats && (
                <p className="text-xs text-slate-700 dark:text-zinc-300">
                  {importProgress.dryRun ? 'Dry-run complete' : 'Import complete'} —{' '}
                  <span className="font-medium">{importProgress.stats.librariesScanned}</span> libraries scanned,{' '}
                  <span className="font-medium">{importProgress.stats.booksCreated}</span> books created,{' '}
                  <span className="font-medium">{importProgress.stats.booksLinked}</span> linked to existing rows,{' '}
                  <span className="font-medium">{importProgress.stats.booksUpdated}</span> updated,{' '}
                  <span className="font-medium">{importProgress.stats.authorsCreated}</span> authors created,{' '}
                  <span className="font-medium">{importProgress.stats.seriesCreated}</span> series created,{' '}
                  <span className="font-medium">{importProgress.stats.editionsAdded}</span> editions added,{' '}
                  <span className="font-medium">{importProgress.stats.ownedMarked}</span> formats marked owned,{' '}
                  <span className="font-medium">{importProgress.stats.reviewQueued}</span> queued for review,{' '}
                  <span className="font-medium">{importProgress.stats.pendingManual}</span> pending manual follow-up,{' '}
                  <span className="font-medium">{importProgress.stats.metadataRelinked}</span> metadata relinked,{' '}
                  <span className="font-medium">{importProgress.stats.metadataConflicts}</span> conflicts queued for review.
                </p>
              )}
              {importProgress.results && importProgress.results.length > 0 && (
                <div className="max-h-64 space-y-3 overflow-y-auto pr-1">
                  <div>
                    <p className="text-[11px] font-semibold uppercase tracking-wide text-slate-700 dark:text-zinc-300">
                      Books
                    </p>
                    <div className="mt-1 space-y-1">
                      {[...importProgress.results].reverse().map(result => (
                        <div key={`${result.itemId}-${result.bookId ?? 0}`} className="flex justify-between gap-3 text-[11px] text-slate-600 dark:text-zinc-400">
                          <span className="min-w-0 flex-1 overflow-hidden">
                            <span className="block truncate">{result.title || result.itemId}</span>
                            {result.message && (
                              <span className="block truncate text-[10px] text-slate-500 dark:text-zinc-500">{result.message}</span>
                            )}
                          </span>
                          <span className="flex-shrink-0 uppercase tracking-wide">{result.outcome}</span>
                        </div>
                      ))}
                    </div>
                  </div>

                  {importProgress.stats && (
                    <>
                      <div>
                        <p className="text-[11px] font-semibold uppercase tracking-wide text-slate-700 dark:text-zinc-300">
                          Authors
                        </p>
                        <p className="mt-1 text-[11px] text-slate-600 dark:text-zinc-400">
                          {importProgress.stats.authorsCreated} created, {importProgress.stats.authorsLinked} linked.
                        </p>
                      </div>

                      <div>
                        <p className="text-[11px] font-semibold uppercase tracking-wide text-slate-700 dark:text-zinc-300">
                          Series
                        </p>
                        <p className="mt-1 text-[11px] text-slate-600 dark:text-zinc-400">
                          {importProgress.stats.seriesCreated} created, {importProgress.stats.seriesLinked} linked.
                        </p>
                      </div>

                      <div>
                        <p className="text-[11px] font-semibold uppercase tracking-wide text-slate-700 dark:text-zinc-300">
                          Editions
                        </p>
                        <p className="mt-1 text-[11px] text-slate-600 dark:text-zinc-400">
                          {importProgress.stats.editionsAdded} added, {importProgress.stats.ownedMarked} formats marked owned.
                        </p>
                      </div>

                      <div>
                        <p className="text-[11px] font-semibold uppercase tracking-wide text-slate-700 dark:text-zinc-300">
                          Follow-up
                        </p>
                        <p className="mt-1 text-[11px] text-slate-600 dark:text-zinc-400">
                          {importProgress.stats.reviewQueued} queued for review, {importProgress.stats.pendingManual} pending manual, {importProgress.stats.metadataConflicts} conflicts queued.
                        </p>
                      </div>
                    </>
                  )}
                </div>
              )}
            </div>
          )}

          <div className="rounded border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-3 space-y-3">
            <div className="flex items-center justify-between gap-3">
              <div>
                <p className="text-sm font-medium text-slate-800 dark:text-zinc-200">Recent runs</p>
                <p className="text-xs text-slate-600 dark:text-zinc-500">Use a dry-run before a live import, then preview rollback against the exact batch if you need to unwind it safely.</p>
              </div>
              <button
                onClick={() => refreshRuns().catch(() => {})}
                className="px-3 py-2 bg-slate-700 hover:bg-slate-600 rounded text-sm font-medium"
              >
                Refresh runs
              </button>
            </div>

            {runs.length === 0 && (
              <p className="text-sm text-slate-500 dark:text-zinc-500">No ABS import runs recorded yet.</p>
            )}

            {runs.slice(0, 5).map(run => {
              const rolledBack = run.status === 'rolled_back'
              return (
              <div key={run.id} className={`rounded border px-3 py-3 space-y-2 ${rolledBack ? 'border-slate-200 dark:border-zinc-800 bg-slate-100/70 dark:bg-zinc-900/50 opacity-75' : 'border-slate-200 dark:border-zinc-800 bg-white/70 dark:bg-zinc-950/40'}`}>
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <p className="text-sm font-medium text-slate-800 dark:text-zinc-200 truncate">
                      Run #{run.id} · {run.dryRun ? 'Dry-run' : 'Live import'} · {absRunStatusLabel(run.status)}
                    </p>
                    <p className="text-[11px] text-slate-500 dark:text-zinc-500">
                      {new Date(run.startedAt).toLocaleString()} · {run.source.label || run.sourceLabel} · {absRunLibraryLabel(run)}
                    </p>
                  </div>
                  {!run.dryRun && (
                    <div className="flex gap-2">
                      <button
                        onClick={() => previewRollback(run.id)}
                        disabled={rolledBack || rollbackPreviewingRunId === run.id || rollbackApplyingRunId === run.id}
                        className="px-3 py-1.5 bg-slate-700 hover:bg-slate-600 rounded text-xs font-medium disabled:opacity-50"
                      >
                        {rollbackPreviewingRunId === run.id ? 'Previewing…' : 'Preview rollback'}
                      </button>
                      <button
                        onClick={() => applyRollback(run.id)}
                        disabled={rolledBack || rollbackApplyingRunId === run.id || rollbackPreviewingRunId === run.id}
                        className={`px-3 py-1.5 rounded text-xs font-medium disabled:opacity-50 ${rolledBack ? 'bg-slate-500 cursor-not-allowed' : 'bg-amber-600 hover:bg-amber-500'}`}
                      >
                        {rolledBack ? 'Rolled back' : rollbackApplyingRunId === run.id ? 'Rolling back…' : 'Apply rollback'}
                      </button>
                    </div>
                  )}
                </div>
                <p className="text-xs text-slate-700 dark:text-zinc-300">
                  {run.summary.error ? run.summary.error : (
                    <>
                      {run.summary.stats.itemsSeen} items seen, {run.summary.stats.itemsDetailFetched} detail fetches, {run.summary.stats.booksCreated} books created, {run.summary.stats.booksLinked} linked, {run.summary.stats.booksUpdated} updated, {run.summary.stats.ownedMarked} formats marked owned, {run.summary.stats.reviewQueued} queued for review, {run.summary.stats.pendingManual} pending manual.
                    </>
                  )}
                </p>
                {run.checkpoint && (
                  <p className="text-[11px] text-amber-700 dark:text-amber-300">
                    Last checkpoint: page {run.checkpoint.page}{run.checkpoint.lastItemId ? ` after ${run.checkpoint.lastItemId}` : ''}.
                  </p>
                )}
                {rollbackResult?.runId === run.id && (
                  <div className="rounded border border-amber-300 dark:border-amber-900 bg-amber-50 dark:bg-amber-950/20 px-3 py-3 space-y-3">
                    <div>
                      <p className="text-sm font-medium text-slate-800 dark:text-zinc-200">
                        {rollbackResult.preview ? 'Rollback preview' : 'Rollback result'}
                      </p>
                      <p className="text-xs text-slate-700 dark:text-zinc-300">
                        {rollbackResult.stats.actionsPlanned} actions planned, {rollbackResult.stats.entitiesDeleted} entities deleted, {rollbackResult.stats.provenanceUnlinked} ABS links removed, {rollbackResult.stats.skipped} retained, {rollbackResult.stats.failed} failures.
                      </p>
                    </div>

                    <div className="grid gap-3 md:grid-cols-2">
                      <div>
                        <p className="text-[11px] font-semibold uppercase tracking-wide text-amber-800 dark:text-amber-300">
                          {rollbackResult.preview ? 'Would delete or change' : 'Deleted or changed'}
                        </p>
                        {rollbackPreviewChanges.length === 0 ? (
                          <p className="text-[11px] text-slate-600 dark:text-zinc-400 mt-1">No entities would be deleted or changed.</p>
                        ) : (
                          <div className="mt-1 max-h-64 space-y-1 overflow-y-auto pr-1">
                            {rollbackPreviewChanges.map(action => (
                              <div key={`change-${action.entityType}-${action.externalId}-${action.localId}-${action.action}`} className="text-[11px] text-slate-700 dark:text-zinc-300">
                                <span className="font-medium">{rollbackActionLabel(action)}</span>
                                <span className="block text-slate-500 dark:text-zinc-500 truncate">{rollbackActionDetail(action)}</span>
                              </div>
                            ))}
                          </div>
                        )}
                      </div>

                      <div>
                        <p className="text-[11px] font-semibold uppercase tracking-wide text-slate-700 dark:text-zinc-300">
                          {rollbackResult.preview ? 'Would be retained' : 'Retained'}
                        </p>
                        {rollbackPreviewRetained.length === 0 ? (
                          <p className="text-[11px] text-slate-600 dark:text-zinc-400 mt-1">No retained entities reported.</p>
                        ) : (
                          <div className="mt-1 max-h-64 space-y-1 overflow-y-auto pr-1">
                            {rollbackPreviewRetained.map(action => (
                              <div key={`retain-${action.entityType}-${action.externalId}-${action.localId}-${action.action}`} className="text-[11px] text-slate-700 dark:text-zinc-300">
                                <span className="font-medium">{rollbackActionLabel(action)}</span>
                                <span className="block text-slate-500 dark:text-zinc-500 truncate">{rollbackActionDetail(action)}</span>
                              </div>
                            ))}
                          </div>
                        )}
                      </div>
                    </div>
                  </div>
                )}
              </div>
              )
            })}

            {rollbackError && <div className="text-sm text-red-600 dark:text-red-400">{rollbackError}</div>}
          </div>
        </div>

        <ABSConflictPanel
          title="Author conflicts"
          description="Review placeholder ABS authors here first. If Bindery can confidently relink one to upstream metadata, you can trigger that before working through book review items."
          entityType="author"
          conflicts={conflicts}
          show={showAuthorConflicts}
          emptyMessage="No author conflicts recorded yet."
          resolvedHeading="Resolved author choices"
          conflictError={conflictError}
          resolvingConflictId={resolvingConflictId}
          onToggle={() => setShowAuthorConflicts(prev => !prev)}
          onRefresh={refreshConflictsQuietly}
          onResolveConflict={resolveConflict}
          relinkAction={{
            loadingId: relinkingAuthorId,
            onRelink: relinkConflictAuthor,
          }}
        />

        <div className="pt-3 border-t border-slate-200 dark:border-zinc-800 space-y-3">
          <div className="flex items-center justify-between gap-4">
            <button
              type="button"
              onClick={() => setShowReviewItems(prev => !prev)}
              aria-expanded={showReviewItems}
              className="min-w-0 flex-1 text-left"
            >
              <div className="flex items-start gap-2">
                <span className="text-sm text-slate-500 dark:text-zinc-500 mt-0.5" aria-hidden="true">
                  {showReviewItems ? '▾' : '▸'}
                </span>
                <div>
                  <label className="block text-sm font-medium text-slate-800 dark:text-zinc-200 cursor-pointer">No-match books</label>
                  <p className="text-xs text-slate-600 dark:text-zinc-500 mt-0.5">
                    Low-confidence ABS items stay here until you explicitly import or dismiss them, which keeps bad metadata out of the main catalog.
                  </p>
                </div>
              </div>
            </button>
            <button
              onClick={() => refreshReviewItems().catch(() => {})}
              className="px-3 py-2 bg-slate-700 hover:bg-slate-600 rounded text-sm font-medium flex-shrink-0"
            >
              Refresh
            </button>
          </div>

          {showReviewItems && (
            <>
              {reviewItems.length === 0 && (
                <p className="text-sm text-slate-500 dark:text-zinc-500">No queued ABS review items.</p>
              )}

              {groupedReviewItems.map(group => (
                <div key={group.runId ?? 'unknown'} className="space-y-2">
                  <div className="flex flex-wrap items-center justify-between gap-2 px-1">
                    <p className="text-xs font-semibold uppercase tracking-wide text-slate-600 dark:text-zinc-400">
                      {group.runId !== null
                        ? t('settings.abs.review.runHeading', { runId: group.runId, count: group.items.length, defaultValue: 'Run #{{runId}} · {{count}} items' })
                        : t('settings.abs.review.unknownRunHeading', { count: group.items.length, defaultValue: 'Older items · {{count}}' })}
                    </p>
                    {group.runId !== null && group.items.length > 0 && (
                      <button
                        type="button"
                        onClick={() => dismissReviewRun(group.runId as number, group.items.length)}
                        disabled={dismissingRunId === group.runId || reviewActionId !== null}
                        className="px-2 py-1 bg-amber-600 hover:bg-amber-500 rounded text-[11px] font-medium disabled:opacity-50"
                      >
                        {dismissingRunId === group.runId
                          ? t('settings.abs.review.dismissingRun', { defaultValue: 'Dismissing…' })
                          : t('settings.abs.review.dismissRun', { defaultValue: 'Dismiss all from this run' })}
                      </button>
                    )}
                  </div>
                  {group.items.map(item => (
                <div key={item.id} className="rounded border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-3 py-3 space-y-2">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <p className="text-sm font-medium text-slate-800 dark:text-zinc-200 truncate">{item.title || item.itemId}</p>
                      <p className="text-xs text-slate-600 dark:text-zinc-400 truncate">{item.primaryAuthor || 'Unknown author'}</p>
                      {item.resolvedAuthorName && (
                        <p className="text-[11px] text-emerald-700 dark:text-emerald-300 truncate">Author: {item.resolvedAuthorName}</p>
                      )}
                      {item.resolvedBookTitle && (
                        <p className="text-[11px] text-emerald-700 dark:text-emerald-300 truncate">Book: {item.resolvedBookTitle}</p>
                      )}
                    </div>
                    <div className="flex flex-wrap items-center justify-end gap-2 flex-shrink-0">
                      {item.asin && (
                        <span className="text-[11px] px-2 py-1 rounded bg-sky-100 dark:bg-sky-950/40 text-sky-700 dark:text-sky-300">
                          ASIN {item.asin}
                        </span>
                      )}
                      {item.fileMappingFound && (
                        <span title={item.fileMappingMessage || undefined} className="text-[11px] px-2 py-1 rounded bg-emerald-100 dark:bg-emerald-950/40 text-emerald-700 dark:text-emerald-300">
                          File Mapping Found
                        </span>
                      )}
                      <span className="text-[11px] px-2 py-1 rounded bg-amber-100 dark:bg-amber-950/40 text-amber-700 dark:text-amber-300">
                        {reviewReasonLabel(item.reviewReason)}
                      </span>
                    </div>
                  </div>
                  <div className="grid gap-2 md:grid-cols-2">
                    <div className="space-y-2">
                      <div className="flex gap-2">
                        <input
                          value={authorQueries[item.id] ?? item.primaryAuthor}
                          onChange={e => setAuthorQueries(prev => ({ ...prev, [item.id]: e.target.value }))}
                          onKeyDown={e => e.key === 'Enter' && searchReviewAuthors(item)}
                          className={inputCls}
                        />
                        <button
                          onClick={() => searchReviewAuthors(item)}
                          disabled={authorSearchId === item.id}
                          className="px-3 py-2 bg-slate-700 hover:bg-slate-600 rounded text-xs font-medium disabled:opacity-50"
                        >
                          {authorSearchId === item.id ? 'Searching...' : 'Author'}
                        </button>
                      </div>
                      {(authorResults[item.id] ?? []).length > 0 && (
                        <div className="max-h-48 space-y-2 overflow-y-auto pr-1">
                          {(authorResults[item.id] ?? []).slice(0, absReviewResultLimit).map(author => (
                            <button
                              key={author.foreignAuthorId}
                              type="button"
                              onClick={() => resolveReviewAuthor(item, author)}
                              disabled={reviewActionId === item.id}
                              className="w-full text-left rounded border border-slate-200 dark:border-zinc-800 bg-white/70 dark:bg-zinc-950/40 px-2 py-1.5 text-xs hover:border-emerald-400 disabled:opacity-50"
                            >
                              <span className="block font-medium text-slate-800 dark:text-zinc-200 truncate">{author.authorName}</span>
                              {author.disambiguation && <span className="block text-[11px] text-slate-500 dark:text-zinc-500 truncate">{author.disambiguation}</span>}
                            </button>
                          ))}
                        </div>
                      )}
                    </div>
                    <div className="space-y-2">
                      <div className="flex gap-2">
                        <input
                          value={bookQueries[item.id] ?? item.editedTitle ?? item.title}
                          onChange={e => setBookQueries(prev => ({ ...prev, [item.id]: e.target.value }))}
                          onKeyDown={e => e.key === 'Enter' && searchReviewBooks(item)}
                          className={inputCls}
                        />
                        <button
                          onClick={() => searchReviewBooks(item)}
                          disabled={bookSearchId === item.id}
                          className="px-3 py-2 bg-slate-700 hover:bg-slate-600 rounded text-xs font-medium disabled:opacity-50"
                        >
                          {bookSearchId === item.id ? 'Searching...' : 'Book'}
                        </button>
                      </div>
                      {(bookResults[item.id] ?? []).length > 0 && (
                        <div className="max-h-48 space-y-2 overflow-y-auto pr-1">
                          {(bookResults[item.id] ?? []).slice(0, absReviewResultLimit).map(book => (
                            <button
                              key={book.foreignBookId}
                              type="button"
                              onClick={() => resolveReviewBook(item, book)}
                              disabled={reviewActionId === item.id}
                              className="w-full text-left rounded border border-slate-200 dark:border-zinc-800 bg-white/70 dark:bg-zinc-950/40 px-2 py-1.5 text-xs hover:border-emerald-400 disabled:opacity-50"
                            >
                              <span className="block font-medium text-slate-800 dark:text-zinc-200 truncate">{book.title}</span>
                              {book.author?.authorName && <span className="block text-[11px] text-slate-500 dark:text-zinc-500 truncate">{book.author.authorName}</span>}
                            </button>
                          ))}
                        </div>
                      )}
                    </div>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <button
                      onClick={() => approveReviewItem(item.id)}
                      disabled={reviewActionId === item.id}
                      className="px-3 py-1.5 bg-sky-600 hover:bg-sky-500 rounded text-xs font-medium disabled:opacity-50"
                    >
                      {reviewActionId === item.id ? 'Importing…' : 'Import'}
                    </button>
                    <button
                      onClick={() => dismissReviewItem(item.id)}
                      disabled={reviewActionId === item.id}
                      className="px-3 py-1.5 bg-slate-700 hover:bg-slate-600 rounded text-xs font-medium disabled:opacity-50"
                    >
                      Dismiss
                    </button>
                  </div>
                </div>
                  ))}
                </div>
              ))}
            </>
          )}

          {reviewError && <div className="text-sm text-red-600 dark:text-red-400">{reviewError}</div>}
        </div>

        <ABSConflictPanel
          title="Book conflicts"
          description="When ABS and upstream book metadata disagree, Bindery keeps the upstream value temporarily and lets you confirm the winning source here."
          entityType="book"
          conflicts={conflicts}
          show={showBookConflicts}
          emptyMessage="No book conflicts recorded yet."
          resolvedHeading="Resolved book choices"
          resolvingConflictId={resolvingConflictId}
          onToggle={() => setShowBookConflicts(prev => !prev)}
          onRefresh={refreshConflictsQuietly}
          onResolveConflict={resolveConflict}
        />
      </div>
    </section>
  )
}
