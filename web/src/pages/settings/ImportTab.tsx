import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, BatchImportItem, BatchImportResponse, Book, HardcoverList, ImportList, ManualImportLookup, ScanItem } from '../../api/client'
import { inputCls } from './formStyles'
import GoodreadsImportSection from './GoodreadsImportSection'

interface MigrateResult {
  requested?: number
  added?: number
  skipped?: number
  errors?: number
  addedNames?: string[]
  failures?: Record<string, string>
}

interface ReadarrResult {
  authors?: MigrateResult
  indexers?: MigrateResult
  downloadClients?: MigrateResult
  blocklist?: MigrateResult
}

// onNavigate threads SettingsPage's soft (no-reload) tab switch into the
// "Configure … in General settings →" link so it doesn't full-page-reload.
export interface ImportTabProps {
  onNavigate?: (tab: string) => void
}

export default function ImportTab({ onNavigate }: ImportTabProps = {}) {
  const { t } = useTranslation()
  const [csvResult, setCsvResult] = useState<MigrateResult | null>(null)
  const [readarrResult, setReadarrResult] = useState<ReadarrResult | null>(null)
  const [uploading, setUploading] = useState<'csv' | 'readarr' | null>(null)
  const [err, setErr] = useState<string | null>(null)

  const upload = async (endpoint: 'csv' | 'readarr', file: File) => {
    setUploading(endpoint)
    setErr(null)
    setCsvResult(null)
    setReadarrResult(null)
    try {
      const fd = new FormData()
      fd.append('file', file)
      const data = await api.uploadMigrate<MigrateResult | ReadarrResult>(endpoint, fd)
      if (endpoint === 'csv') setCsvResult(data as MigrateResult)
      else setReadarrResult(data as ReadarrResult)
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Upload failed')
    } finally {
      setUploading(null)
    }
  }

  const renderResult = (r: MigrateResult | undefined, label: string) => {
    if (!r) return null
    return (
      <div className="p-3 border border-slate-200 dark:border-zinc-800 rounded bg-slate-100 dark:bg-zinc-900 space-y-1">
        <div className="text-sm font-medium">{label}</div>
        <div className="text-xs text-slate-600 dark:text-zinc-500">
          {r.requested ?? 0} requested · {r.added ?? 0} added · {r.skipped ?? 0} skipped (already exist) · {r.errors ?? 0} failed
        </div>
        {r.failures && Object.keys(r.failures).length > 0 && (
          <details className="text-xs">
            <summary className="cursor-pointer text-red-600 dark:text-red-400">Show {Object.keys(r.failures).length} failures</summary>
            <ul className="mt-2 space-y-0.5 font-mono">
              {Object.entries(r.failures).map(([name, reason]) => (
                <li key={name}><span className="text-slate-800 dark:text-zinc-200">{name}</span>: <span className="text-slate-500 dark:text-zinc-500">{reason}</span></li>
              ))}
            </ul>
          </details>
        )}
      </div>
    )
  }

  return (
    <div className="space-y-8 max-w-2xl">
      <section>
        <h3 className="text-base font-semibold mb-2 text-slate-800 dark:text-zinc-200">{t('settings.import.csvHeading')}</h3>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">
          {t('settings.import.csvDescription')}
        </p>
        <label className="inline-flex items-center gap-2 px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium cursor-pointer">
          {uploading === 'csv' ? t('settings.import.importingCsv') : t('settings.import.uploadCsv')}
          <input
            type="file"
            accept=".csv,.txt,text/csv,text/plain"
            className="hidden"
            disabled={uploading !== null}
            onChange={e => { const f = e.target.files?.[0]; if (f) upload('csv', f); e.currentTarget.value = '' }}
          />
        </label>
        {csvResult && <div className="mt-4">{renderResult(csvResult, 'Authors')}</div>}
      </section>

      <section>
        <h3 className="text-base font-semibold mb-2 text-slate-800 dark:text-zinc-200">{t('settings.import.readarrHeading')}</h3>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">
          {t('settings.import.readarrDescription')}
        </p>
        <label className="inline-flex items-center gap-2 px-3 py-2 bg-emerald-600 hover:bg-emerald-500 rounded text-sm font-medium cursor-pointer">
          {uploading === 'readarr' ? t('settings.import.importingReadarr') : t('settings.import.uploadReadarr')}
          <input
            type="file"
            accept=".db,.sqlite,application/x-sqlite3,application/octet-stream"
            className="hidden"
            disabled={uploading !== null}
            onChange={e => { const f = e.target.files?.[0]; if (f) upload('readarr', f); e.currentTarget.value = '' }}
          />
        </label>
        {readarrResult && (
          <div className="mt-4 space-y-2">
            {renderResult(readarrResult.authors, 'Authors')}
            {renderResult(readarrResult.indexers, 'Indexers')}
            {renderResult(readarrResult.downloadClients, 'Download clients')}
            {renderResult(readarrResult.blocklist, 'Blocklist')}
          </div>
        )}
      </section>
      <ManualImportSection />
      <FolderScanSection />
      <GoodreadsImportSection />
      <HardcoverListsSection onNavigate={onNavigate} />

      {err && (
        <div className="px-3 py-2 bg-red-100 dark:bg-red-950/30 border border-red-300 dark:border-red-900 rounded text-sm text-red-800 dark:text-red-300">
          {err}
        </div>
      )}
    </div>
  )
}

function ManualImportSection() {
  const { t } = useTranslation()
  const [path, setPath] = useState('')
  const [looking, setLooking] = useState(false)
  const [result, setResult] = useState<ManualImportLookup | null>(null)
  const [selectedBook, setSelectedBook] = useState<Book | null>(null)
  const [format, setFormat] = useState('')
  const [importing, setImporting] = useState(false)
  const [success, setSuccess] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const reset = () => {
    setResult(null)
    setSelectedBook(null)
    setFormat('')
    setSuccess(false)
    setErr(null)
  }

  const handleLookup = async () => {
    if (!path.trim()) return
    setLooking(true)
    reset()
    try {
      const r = await api.lookupManualImport(path.trim())
      setResult(r)
      setFormat(r.detectedFormat)
      if (r.match === 'confident' && r.book) setSelectedBook(r.book)
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Lookup failed')
    } finally {
      setLooking(false)
    }
  }

  const handleImport = async () => {
    if (!selectedBook) return
    setImporting(true)
    setErr(null)
    try {
      await api.manualImport({ path: path.trim(), bookId: selectedBook.id, format: format || undefined })
      setSuccess(true)
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Import failed')
    } finally {
      setImporting(false)
    }
  }

  return (
    <section>
      <h3 className="text-base font-semibold mb-2 text-slate-800 dark:text-zinc-200">{t('settings.import.manualImportHeading')}</h3>
      <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">{t('settings.import.manualImportDescription')}</p>

      <div className="flex gap-2 mb-3">
        <input
          className={inputCls + ' flex-1'}
          placeholder={t('settings.import.manualImportPathPlaceholder')}
          value={path}
          onChange={e => { setPath(e.target.value); reset() }}
          onKeyDown={e => { if (e.key === 'Enter') handleLookup() }}
        />
        <button
          onClick={handleLookup}
          disabled={looking || !path.trim()}
          className="px-3 py-2 bg-slate-200 dark:bg-zinc-700 hover:bg-slate-300 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-sm font-medium"
        >
          {looking ? t('settings.import.manualImportLooking') : t('settings.import.manualImportLookup')}
        </button>
      </div>

      {result && !success && (
        <div className="p-3 border border-slate-200 dark:border-zinc-800 rounded bg-slate-50 dark:bg-zinc-900 space-y-3">
          {result.match === 'confident' && selectedBook && (
            <p className="text-sm text-slate-700 dark:text-zinc-300">
              {t('settings.import.manualImportConfident', { title: selectedBook.title, author: selectedBook.author?.authorName ?? '' })}
            </p>
          )}
          {result.match === 'ambiguous' && (
            <div className="space-y-1">
              <p className="text-sm text-slate-700 dark:text-zinc-300">{t('settings.import.manualImportAmbiguous')}</p>
              <select
                className={inputCls}
                value={selectedBook?.id ?? ''}
                onChange={e => {
                  const id = Number(e.target.value)
                  setSelectedBook(result.candidates?.find(b => b.id === id) ?? null)
                }}
              >
                <option value="">— select —</option>
                {result.candidates?.map(b => (
                  <option key={b.id} value={b.id}>{b.title} {b.author ? `(${b.author.authorName})` : ''}</option>
                ))}
              </select>
            </div>
          )}
          {result.match === 'none' && (
            <p className="text-sm text-amber-700 dark:text-amber-400">{t('settings.import.manualImportNone')}</p>
          )}

          {result.match !== 'none' && (
            <div className="flex items-center gap-3">
              <div>
                <label className="block text-xs text-slate-500 dark:text-zinc-500 mb-1">{t('settings.import.manualImportFormatLabel')}</label>
                <select className={inputCls} value={format} onChange={e => setFormat(e.target.value)}>
                  <option value="">{t('settings.import.manualImportFormatAuto')}</option>
                  <option value="ebook">{t('settings.import.manualImportFormatEbook')}</option>
                  <option value="audiobook">{t('settings.import.manualImportFormatAudiobook')}</option>
                </select>
              </div>
              <button
                onClick={handleImport}
                disabled={importing || !selectedBook}
                className="mt-4 px-3 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-sm font-medium"
              >
                {importing ? t('settings.import.manualImportImporting') : t('settings.import.manualImportConfirm')}
              </button>
            </div>
          )}
        </div>
      )}

      {success && (
        <p className="text-sm text-emerald-600 dark:text-emerald-400">{t('settings.import.manualImportSuccess')}</p>
      )}
      {err && (
        <p className="text-sm text-red-600 dark:text-red-400">{err}</p>
      )}
    </section>
  )
}

interface ScanRowState {
  include: boolean
  bookId: number | null
  format: string
}

// FolderScanSection scans a folder for book units and bulk-imports the selected
// matches in one shot, so a migration backlog doesn't have to be imported one
// path at a time. Matches against the existing catalogue (add the authors/books
// first); unmatched units are listed but cannot be selected. Exported for tests.
export function FolderScanSection() {
  const { t } = useTranslation()
  const [path, setPath] = useState('')
  const [scanning, setScanning] = useState(false)
  const [items, setItems] = useState<ScanItem[] | null>(null)
  const [truncated, setTruncated] = useState(false)
  const [rows, setRows] = useState<ScanRowState[]>([])
  const [importing, setImporting] = useState(false)
  const [summary, setSummary] = useState<BatchImportResponse | null>(null)
  const [err, setErr] = useState<string | null>(null)

  const handleScan = async () => {
    if (!path.trim()) return
    setScanning(true)
    setErr(null)
    setItems(null)
    setSummary(null)
    try {
      const r = await api.scanFolder(path.trim())
      setItems(r.items)
      setTruncated(r.truncated)
      setRows(r.items.map(it => ({
        include: it.match === 'confident',
        bookId: it.match === 'confident' && it.book ? it.book.id : null,
        format: it.detectedFormat || '',
      })))
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Scan failed')
    } finally {
      setScanning(false)
    }
  }

  const patchRow = (i: number, patch: Partial<ScanRowState>) =>
    setRows(prev => prev.map((r, idx) => (idx === i ? { ...r, ...patch } : r)))

  const selectedCount = rows.filter(r => r.include && r.bookId).length

  const handleImport = async () => {
    if (!items) return
    const batch: BatchImportItem[] = []
    rows.forEach((r, i) => {
      if (r.include && r.bookId) batch.push({ path: items[i].path, bookId: r.bookId, format: r.format || undefined })
    })
    if (batch.length === 0) return
    setImporting(true)
    setErr(null)
    try {
      const res = await api.batchImport(batch)
      setSummary(res)
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Import failed')
    } finally {
      setImporting(false)
    }
  }

  const matchBadge = (m: ScanItem['match']) => {
    const map: Record<ScanItem['match'], string> = {
      confident: 'bg-emerald-100 dark:bg-emerald-950 text-emerald-700 dark:text-emerald-400',
      ambiguous: 'bg-amber-100 dark:bg-amber-950 text-amber-700 dark:text-amber-400',
      none: 'bg-slate-200 dark:bg-zinc-800 text-slate-500 dark:text-zinc-500',
    }
    return <span className={`text-[10px] px-1.5 py-0.5 rounded ${map[m]}`}>{t(`settings.import.bulkMatch_${m}`, m)}</span>
  }

  return (
    <section>
      <h3 className="text-base font-semibold mb-2 text-slate-800 dark:text-zinc-200">{t('settings.import.bulkHeading', 'Bulk folder import')}</h3>
      <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">
        {t('settings.import.bulkDescription', 'Scan a folder (e.g. your download directory) and import every book it can match to your library in one go. Matching is against books already in your library, so add the authors first. Unmatched items are listed but skipped.')}
      </p>

      <div className="flex gap-2 mb-3">
        <input
          className={inputCls + ' flex-1'}
          placeholder={t('settings.import.bulkPathPlaceholder', '/downloads/books')}
          value={path}
          onChange={e => { setPath(e.target.value); setItems(null); setSummary(null) }}
          onKeyDown={e => { if (e.key === 'Enter') handleScan() }}
        />
        <button
          onClick={handleScan}
          disabled={scanning || !path.trim()}
          className="px-3 py-2 bg-slate-200 dark:bg-zinc-700 hover:bg-slate-300 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-sm font-medium"
        >
          {scanning ? t('settings.import.bulkScanning', 'Scanning…') : t('settings.import.bulkScan', 'Scan folder')}
        </button>
      </div>

      {items && items.length === 0 && (
        <p className="text-sm text-slate-500 dark:text-zinc-600">{t('settings.import.bulkEmpty', 'No book files or folders found here.')}</p>
      )}

      {items && items.length > 0 && !summary && (
        <div className="space-y-2">
          {truncated && (
            <p className="text-xs text-amber-700 dark:text-amber-400">{t('settings.import.bulkTruncated', 'Showing the first 1000 items; narrow the folder to see the rest.')}</p>
          )}
          <div className="border border-slate-200 dark:border-zinc-800 rounded divide-y divide-slate-200 dark:divide-zinc-800 max-h-96 overflow-auto">
            {items.map((it, i) => {
              const row = rows[i]
              const disabled = it.match === 'none'
              return (
                <div key={it.path} className="p-2 flex items-start gap-2 text-sm">
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={row.include && !disabled}
                    disabled={disabled}
                    onChange={e => patchRow(i, { include: e.target.checked })}
                    aria-label={t('settings.import.bulkSelect', { name: it.name, defaultValue: `Import ${it.name}` })}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium truncate">{it.name}</span>
                      {matchBadge(it.match)}
                      <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{it.detectedFormat}</span>
                    </div>
                    {/* Full source path so the user can tell which file a match
                        (or the ambiguous picker below) refers to — the basename
                        alone is ambiguous across folders (#1435). */}
                    <div className="text-xs font-mono text-slate-500 dark:text-zinc-600 truncate" title={it.path}>{it.path}</div>
                    <div className="text-xs text-slate-500 dark:text-zinc-600 truncate">
                      {t('settings.import.bulkParsed', { title: it.parsedTitle || '?', author: it.parsedAuthor || '?', defaultValue: `parsed: ${it.parsedTitle || '?'} / ${it.parsedAuthor || '?'}` })}
                    </div>
                    {it.match === 'confident' && it.book && (
                      <div className="text-xs text-emerald-700 dark:text-emerald-400 mt-0.5">→ {it.book.title}{it.book.author ? ` (${it.book.author.authorName})` : ''}</div>
                    )}
                    {it.match === 'ambiguous' && (
                      <select
                        className={inputCls + ' mt-1 text-xs'}
                        value={row.bookId ?? ''}
                        onChange={e => {
                          const id = Number(e.target.value)
                          patchRow(i, { bookId: id || null, include: Boolean(id) })
                        }}
                      >
                        <option value="">{t('settings.import.bulkPick', '— pick a book —')}</option>
                        {it.candidates?.map(b => (
                          <option key={b.id} value={b.id}>{b.title}{b.author ? ` (${b.author.authorName})` : ''}</option>
                        ))}
                      </select>
                    )}
                    {it.match === 'none' && (
                      <div className="text-xs text-slate-400 dark:text-zinc-600 mt-0.5">{t('settings.import.bulkNoMatch', 'No catalogue match; add the book first.')}</div>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
          <button
            onClick={handleImport}
            disabled={importing || selectedCount === 0}
            className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-sm font-medium"
          >
            {importing
              ? t('settings.import.bulkImporting', 'Importing…')
              : t('settings.import.bulkImport', { count: selectedCount, defaultValue: `Import ${selectedCount} selected` })}
          </button>
        </div>
      )}

      {summary && (
        <div className="p-3 border border-slate-200 dark:border-zinc-800 rounded bg-slate-100 dark:bg-zinc-900 space-y-1">
          <div className="text-sm font-medium">{t('settings.import.bulkSummary', { accepted: summary.accepted, failed: summary.failed, defaultValue: `${summary.accepted} queued, ${summary.failed} failed` })}</div>
          {summary.failed > 0 && (
            <details className="text-xs">
              <summary className="cursor-pointer text-red-600 dark:text-red-400">{t('settings.import.bulkShowFailures', { count: summary.failed, defaultValue: `Show ${summary.failed} failures` })}</summary>
              <ul className="mt-2 space-y-0.5 font-mono">
                {summary.results.filter(r => !r.accepted).map(r => (
                  <li key={r.path}><span className="text-slate-800 dark:text-zinc-200">{r.path}</span>: <span className="text-slate-500 dark:text-zinc-500">{r.error}</span></li>
                ))}
              </ul>
            </details>
          )}
          <p className="text-xs text-slate-500 dark:text-zinc-600">{t('settings.import.bulkSummaryNote', 'Imports run in the background; watch the Queue for progress.')}</p>
        </div>
      )}

      {err && <p className="text-sm text-red-600 dark:text-red-400 mt-2">{err}</p>}
    </section>
  )
}

function sortImportLists(items: ImportList[]) {
  return [...items].sort((a, b) => a.name.localeCompare(b.name))
}

interface HardcoverImportListRow {
  slug: string
  name: string
  booksCount: number
  remote?: HardcoverList
  local?: ImportList
  stale: boolean
}

function HardcoverListsSection({ onNavigate }: { onNavigate?: (tab: string) => void }) {
  const { t } = useTranslation()
  const [lists, setLists] = useState<ImportList[]>([])
  const [hcLists, setHcLists] = useState<HardcoverList[]>([])
  const [loadingLists, setLoadingLists] = useState(true)
  const [pickerToken, setPickerToken] = useState('')
  const [activePickerToken, setActivePickerToken] = useState('')
  const [syncingId, setSyncingId] = useState<number | null>(null)
  const [actionSlug, setActionSlug] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [overrideOpen, setOverrideOpen] = useState<Record<number, boolean>>({})
  const [overrideDraft, setOverrideDraft] = useState<Record<number, string>>({})

  const loadLists = useCallback(async (token: string) => {
    setLoadingLists(true)
    setError(null)
    try {
      const all = await api.listImportLists()
      setLists(sortImportLists(all.filter(l => l.type === 'hardcover')))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load saved Hardcover lists')
    }
    try {
      setHcLists(await api.hardcoverLists(token || undefined))
    } catch (err) {
      setHcLists([])
      setError(err instanceof Error ? err.message : 'Failed to load Hardcover lists')
    } finally {
      setLoadingLists(false)
    }
  }, [])

  useEffect(() => {
    void loadLists('')
  }, [loadLists])

  const updateLocalList = (updated: ImportList) => {
    setLists(prev => sortImportLists(prev.map(l => l.id === updated.id ? updated : l)))
  }

  const handleDelete = async (id: number) => {
    await api.deleteImportList(id)
    setLists(prev => prev.filter(l => l.id !== id))
  }

  const handleToggle = async (il: ImportList) => {
    const updated = await api.updateImportList(il.id, { enabled: !il.enabled })
    updateLocalList(updated)
  }

  const handleMediaTypeChange = async (il: ImportList, mediaType: string) => {
    setError(null)
    try {
      const updated = await api.updateImportList(il.id, { mediaType })
      updateLocalList(updated)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update media type')
    }
  }

  const handleSelectList = async (list: HardcoverList, existing?: ImportList) => {
    setActionSlug(list.slug)
    setError(null)
    try {
      if (existing) {
        const updated = await api.updateImportList(existing.id, { enabled: !existing.enabled })
        updateLocalList(updated)
        return
      }

      const tokenOverride = activePickerToken.trim()
      const created = await api.addImportList({
        name: list.name,
        type: 'hardcover',
        url: list.slug,
        apiKey: tokenOverride,
        enabled: Boolean(tokenOverride),
        monitorNew: true,
        autoAdd: true,
      })
      setLists(prev => sortImportLists([...prev, created]))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update Hardcover import list')
    } finally {
      setActionSlug(null)
    }
  }

  const handleLoadOverrideLists = async () => {
    const token = pickerToken.trim()
    if (!token) return
    setActivePickerToken(token)
    await loadLists(token)
  }

  const handleUseSavedToken = async () => {
    setPickerToken('')
    setActivePickerToken('')
    await loadLists('')
  }

  const handleSync = async (id: number) => {
    setSyncingId(id)
    setError(null)
    try {
      await api.syncImportList(id)
      const all = await api.listImportLists()
      setLists(sortImportLists(all.filter(l => l.type === 'hardcover')))
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Sync failed')
    } finally {
      setSyncingId(null)
    }
  }

  const handleSaveOverride = async (il: ImportList) => {
    const token = (overrideDraft[il.id] ?? '').trim()
    if (!token) return
    setActionSlug(il.url)
    setError(null)
    try {
      const updated = await api.updateImportList(il.id, { apiKey: token })
      updateLocalList(updated)
      setOverrideDraft(prev => ({ ...prev, [il.id]: '' }))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save token override')
    } finally {
      setActionSlug(null)
    }
  }

  const handleClearOverride = async (il: ImportList) => {
    setActionSlug(il.url)
    setError(null)
    try {
      const updated = await api.updateImportList(il.id, { clearApiKey: true })
      updateLocalList(updated)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to clear token override')
    } finally {
      setActionSlug(null)
    }
  }

  const localsBySlug = new Map<string, ImportList[]>()
  for (const il of lists) {
    localsBySlug.set(il.url, [...(localsBySlug.get(il.url) ?? []), il])
  }
  const remoteSlugs = new Set(hcLists.map(l => l.slug))
  const rows: HardcoverImportListRow[] = []
  for (const list of hcLists) {
    const locals = localsBySlug.get(list.slug)
    if (locals?.length) {
      for (const il of locals) {
        rows.push({ slug: il.url, name: il.name, booksCount: list.booksCount, remote: list, local: il, stale: false })
      }
      continue
    }
    rows.push({ slug: list.slug, name: list.name, booksCount: list.booksCount, remote: list, stale: false })
  }
  for (const il of lists) {
    if (!remoteSlugs.has(il.url)) {
      rows.push({ slug: il.url, name: il.name, booksCount: 0, local: il, stale: true })
    }
  }

  return (
    <section>
      <div className="flex justify-between items-center mb-2">
        <h3 className="text-base font-semibold text-slate-800 dark:text-zinc-200">{t('settings.import.hardcoverHeading')}</h3>
        <button onClick={() => loadLists(activePickerToken)} disabled={loadingLists} className="px-3 py-1.5 bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-xs font-medium">
          {loadingLists ? t('settings.import.hardcoverListLoading') : t('common.refresh', 'Refresh')}
        </button>
      </div>
      <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">
        {t('settings.import.hardcoverDescription')}
      </p>

      <div className="mb-3 flex flex-col sm:flex-row gap-2">
        <input
          className={inputCls}
          type="password"
          placeholder={t('settings.import.hardcoverTokenPlaceholder')}
          value={pickerToken}
          onChange={e => setPickerToken(e.target.value)}
        />
        <button
          onClick={handleLoadOverrideLists}
          disabled={!pickerToken.trim() || loadingLists}
          className="px-3 py-2 bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-xs font-medium"
        >
          {t('settings.import.hardcoverLoadOverride', 'Load token lists')}
        </button>
        {activePickerToken && (
          <button
            onClick={handleUseSavedToken}
            disabled={loadingLists}
            className="px-3 py-2 bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-xs font-medium"
          >
            {t('settings.import.hardcoverUseSavedToken', 'Use saved token')}
          </button>
        )}
      </div>

      {rows.length === 0 && !loadingLists && (
        <p className="text-sm text-slate-500 dark:text-zinc-600">{t('settings.import.hardcoverEmpty')}</p>
      )}

      <div className="space-y-2">
        {rows.map(row => {
          const il = row.local
          const rowKey = il ? `local-${il.id}` : `remote-${row.slug}`
          const active = Boolean(il?.enabled)
          const busy = actionSlug === row.slug
          return (
            <div key={rowKey} className="p-3 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
              <div className="flex items-start justify-between gap-3">
                <label className="flex items-start gap-3 min-w-0 cursor-pointer">
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={active}
                    disabled={busy}
                    onChange={() => row.remote ? handleSelectList(row.remote, il) : il && handleToggle(il)}
                    aria-label={t('settings.import.hardcoverImportList', { name: row.name, defaultValue: `Import ${row.name}` })}
                  />
                  <span className="min-w-0">
                    <span className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium">{row.name}</span>
                      <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{row.slug}</span>
                      {row.stale && <span className="text-[10px] px-1.5 py-0.5 bg-amber-100 dark:bg-amber-950 text-amber-700 dark:text-amber-400 rounded">{t('settings.import.hardcoverSavedOnly', 'Saved only')}</span>}
                      {il && <span className="text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400 rounded">{il.apiKeyConfigured ? t('settings.import.hardcoverOverrideConfigured', 'Override token') : t('settings.import.hardcoverGlobalToken', 'Global token')}</span>}
                    </span>
                    <span className="block text-xs text-slate-500 dark:text-zinc-600 mt-0.5">
                      {il?.lastSyncAt
                        ? t('settings.import.hardcoverLastSync', { date: new Date(il.lastSyncAt).toLocaleString() })
                        : il
                          ? t('settings.import.hardcoverNeverSynced')
                          : t('settings.import.hardcoverNotSelected', 'Not selected')}
                      {row.remote && ` · ${row.booksCount} books`}
                    </span>
                  </span>
                </label>
                {il && (
                  <div className="flex flex-wrap justify-end gap-2">
                    <select
                      value={il.mediaType || ''}
                      onChange={e => handleMediaTypeChange(il, e.target.value)}
                      aria-label={t('settings.import.hardcoverMediaType', 'Media type')}
                      title={t('settings.import.hardcoverMediaTypeHint', 'Format synced books are created as. Auto uses what Hardcover reports.')}
                      className="text-xs px-2 py-1 rounded bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300"
                    >
                      <option value="">{t('settings.import.mediaTypeAuto', 'Auto')}</option>
                      <option value="ebook">{t('settings.import.mediaTypeEbook', 'Ebook')}</option>
                      <option value="audiobook">{t('settings.import.mediaTypeAudiobook', 'Audiobook')}</option>
                      <option value="both">{t('settings.import.mediaTypeBoth', 'Both')}</option>
                    </select>
                    <button
                      onClick={() => handleSync(il.id)}
                      disabled={syncingId === il.id || !il.enabled}
                      className="text-xs px-2 py-1 rounded bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300 hover:bg-slate-300 dark:hover:bg-zinc-700 disabled:opacity-50"
                    >
                      {syncingId === il.id ? 'Syncing...' : 'Sync now'}
                    </button>
                    <button
                      onClick={() => setOverrideOpen(prev => ({ ...prev, [il.id]: !prev[il.id] }))}
                      className="text-xs px-2 py-1 rounded bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300 hover:bg-slate-300 dark:hover:bg-zinc-700"
                    >
                      {t('settings.import.hardcoverTokenOverride', 'Token override')}
                    </button>
                    <button onClick={() => handleDelete(il.id)} className="text-xs text-red-600 dark:text-red-400 hover:underline">{t('common.delete')}</button>
                  </div>
                )}
              </div>
              {il && overrideOpen[il.id] && (
                <div className="mt-3 flex flex-col sm:flex-row gap-2">
                  <input
                    className={inputCls}
                    type="password"
                    placeholder={il.apiKeyConfigured ? t('settings.import.hardcoverTokenOverridePlaceholderConfigured', 'Override token is hidden. Enter a new token to replace it.') : t('settings.import.hardcoverTokenOverridePlaceholder', 'Paste a per-list token override')}
                    value={overrideDraft[il.id] ?? ''}
                    onChange={e => setOverrideDraft(prev => ({ ...prev, [il.id]: e.target.value }))}
                  />
                  <button
                    onClick={() => handleSaveOverride(il)}
                    disabled={busy || !(overrideDraft[il.id] ?? '').trim()}
                    className="px-3 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-xs font-medium"
                  >
                    {t('common.save')}
                  </button>
                  {il.apiKeyConfigured && (
                    <button
                      onClick={() => handleClearOverride(il)}
                      disabled={busy}
                      className="px-3 py-2 bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 disabled:opacity-50 rounded text-xs font-medium"
                    >
                      {t('settings.import.hardcoverClearOverride', 'Clear override')}
                    </button>
                  )}
                </div>
              )}
            </div>
          )
        })}
      </div>

      {error && (
        <div className="mt-2 text-sm text-rose-600 dark:text-rose-400">
          {error}
          {(error.toLowerCase().includes('token') || error.toLowerCase().includes('not configured')) && (
            <span className="block mt-1 text-xs">
              <button
                onClick={() => onNavigate ? onNavigate('api-keys') : window.location.assign('/settings?tab=api-keys')}
                className="text-emerald-600 dark:text-emerald-400 hover:underline"
              >
                {t('settings.import.configureHardcoverToken', 'Configure the Hardcover API token in API Keys settings →')}
              </button>
            </span>
          )}
        </div>
      )}
    </section>
  )
}
