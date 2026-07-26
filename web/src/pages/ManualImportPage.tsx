import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import type { BatchImportItem, BatchImportResult, Book, ScanItem } from '../api/client'
import { btn, btnSize } from '../components/buttons'

// ManualImportPage is the manual-import wizard (#1236). It scans a folder with
// the recursive bulk-import scan (#1434), groups the discovered units by match
// confidence, and lets the user resolve each one before importing:
//   - confident: a preselected catalogue match, deselectable and overridable;
//   - ambiguous: a candidate picker over the returned catalogue candidates;
//   - none: a search box to bind the unit to an EXISTING catalogue book.
// Resolved units are imported (per-unit or in bulk) through the batch endpoint,
// which reports per-item success/failure.
//
// OUT OF SCOPE (follow-up): creating a brand-new book/author from file metadata.
// `none` units resolve to an existing catalogue book only.

const MATCH_ORDER: ScanItem['match'][] = ['confident', 'ambiguous', 'none']

// RowState is the per-unit resolution the user is building, keyed by unit path.
interface RowState {
  // chosen is the catalogue book the unit will import against (a confident
  // match, a picked candidate, or a searched existing book). null until resolved.
  chosen: Book | null
  // format override: '' = auto-detect, or 'ebook' / 'audiobook'. Defaults to the
  // scan's detected format.
  format: string
  // selected marks the unit for the next bulk import. Only meaningful when chosen.
  selected: boolean
}

function bookLabel(b: Book): string {
  return b.author?.authorName ? `${b.title} · ${b.author.authorName}` : b.title
}

function groupHeading(match: ScanItem['match']): string {
  switch (match) {
    case 'confident': return 'Matched'
    case 'ambiguous': return 'Needs a choice'
    default: return 'Unmatched'
  }
}

export default function ManualImportPage() {
  const { t } = useTranslation()
  const [path, setPath] = useState('')
  const [scanning, setScanning] = useState(false)
  const [items, setItems] = useState<ScanItem[] | null>(null)
  const [truncated, setTruncated] = useState(false)
  const [rows, setRows] = useState<Record<string, RowState>>({})
  const [results, setResults] = useState<Record<string, BatchImportResult>>({})
  const [importingPaths, setImportingPaths] = useState<Set<string>>(() => new Set())
  const [scanError, setScanError] = useState('')
  const [importError, setImportError] = useState('')

  useEffect(() => {
    document.title = 'Manual Import · Bindery'
    return () => { document.title = 'Bindery' }
  }, [])

  const handleScan = async () => {
    const p = path.trim()
    if (!p) return
    setScanning(true)
    setScanError('')
    setImportError('')
    setItems(null)
    setResults({})
    try {
      const r = await api.scanFolder(p)
      setItems(r.items)
      setTruncated(r.truncated)
      const init: Record<string, RowState> = {}
      for (const it of r.items) {
        const chosen = it.match === 'confident' && it.book ? it.book : null
        init[it.path] = { chosen, format: it.detectedFormat || '', selected: Boolean(chosen) }
      }
      setRows(init)
    } catch (e) {
      setScanError(e instanceof Error ? e.message : 'Scan failed')
    } finally {
      setScanning(false)
    }
  }

  const patchRow = (unitPath: string, patch: Partial<RowState>) =>
    setRows(prev => ({ ...prev, [unitPath]: { ...prev[unitPath], ...patch } }))

  const pickBook = (unitPath: string, book: Book) =>
    patchRow(unitPath, { chosen: book, selected: true })

  const toggleSelected = (unitPath: string) =>
    setRows(prev => {
      const r = prev[unitPath]
      if (!r || !r.chosen) return prev
      return { ...prev, [unitPath]: { ...r, selected: !r.selected } }
    })

  // A path is submittable when it has a chosen book, is selected, and hasn't
  // already been accepted by a prior import. A `none` unit the user never
  // resolved has chosen=null, so it is never submitted.
  const submittablePaths = useMemo(() => {
    if (!items) return []
    return items
      .map(it => it.path)
      .filter(p => {
        const r = rows[p]
        return Boolean(r?.chosen) && r.selected && !results[p]?.accepted
      })
  }, [items, rows, results])

  const submit = async (paths: string[]) => {
    const batch: BatchImportItem[] = []
    for (const p of paths) {
      const r = rows[p]
      if (!r?.chosen || !r.selected) continue
      if (results[p]?.accepted) continue
      batch.push({ path: p, bookId: r.chosen.id, format: r.format || undefined })
    }
    if (batch.length === 0) return
    setImportError('')
    setImportingPaths(prev => { const n = new Set(prev); batch.forEach(b => n.add(b.path)); return n })
    try {
      const res = await api.batchImport(batch)
      setResults(prev => {
        const n = { ...prev }
        for (const r of res.results) n[r.path] = r
        return n
      })
    } catch (e) {
      setImportError(e instanceof Error ? e.message : 'Import failed')
    } finally {
      setImportingPaths(prev => { const n = new Set(prev); batch.forEach(b => n.delete(b.path)); return n })
    }
  }

  const allSelected = submittablePaths.length > 0 &&
    submittablePaths.every(p => rows[p]?.selected)

  const toggleSelectAll = () => {
    if (!items) return
    // Resolved, not-yet-imported units only — a `none` unit with no book can't
    // be selected.
    const resolvable = items
      .map(it => it.path)
      .filter(p => rows[p]?.chosen && !results[p]?.accepted)
    const turnOn = !resolvable.every(p => rows[p]?.selected)
    setRows(prev => {
      const n = { ...prev }
      for (const p of resolvable) n[p] = { ...n[p], selected: turnOn }
      return n
    })
  }

  const groups = useMemo(() => {
    const g: Record<ScanItem['match'], ScanItem[]> = { confident: [], ambiguous: [], none: [] }
    for (const it of items ?? []) g[it.match].push(it)
    return g
  }, [items])

  return (
    <div>
      <div className="mb-6">
        <h2 className="text-xl font-semibold text-slate-900 dark:text-white">
          {t('manualImport.title', 'Manual Import')}
        </h2>
        <p className="mt-1 text-sm text-slate-600 dark:text-zinc-400">
          {t('manualImport.description', 'Scan a folder of files already on disk, match each book to your library, and import them. Matching is against books already in your library.')}
        </p>
      </div>

      <div className="flex flex-col sm:flex-row gap-2 mb-6">
        <input
          type="text"
          value={path}
          onChange={e => setPath(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') handleScan() }}
          placeholder={t('manualImport.pathPlaceholder', '/downloads/books')}
          aria-label={t('manualImport.pathLabel', 'Folder to scan')}
          className="flex-1 px-3 py-2 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-950 text-sm"
        />
        <button
          onClick={handleScan}
          disabled={scanning || !path.trim()}
          className={`${btn.primary} ${btnSize.md}`}
        >
          {scanning ? t('manualImport.scanning', 'Scanning…') : t('manualImport.scan', 'Scan folder')}
        </button>
      </div>

      {scanError && (
        <p className="mb-4 text-sm text-red-600 dark:text-red-400">{scanError}</p>
      )}

      {items && items.length === 0 && (
        <p className="text-sm text-slate-500 dark:text-zinc-500">
          {t('manualImport.empty', 'No book files or folders found here.')}
        </p>
      )}

      {items && items.length > 0 && (
        <>
          {truncated && (
            <p className="mb-3 text-xs text-amber-700 dark:text-amber-400">
              {t('manualImport.truncated', 'Showing the first 1000 units; narrow the folder to see the rest.')}
            </p>
          )}

          {/* Select-all / bulk import bar */}
          <div className="sticky top-16 z-10 mb-4 flex flex-wrap items-center gap-3 rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-100 dark:bg-zinc-900 px-3 py-2">
            <label className="inline-flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={allSelected}
                onChange={toggleSelectAll}
                aria-label={t('manualImport.selectAll', 'Select all matched')}
              />
              {t('manualImport.selectAll', 'Select all matched')}
            </label>
            <button
              onClick={() => submit(submittablePaths)}
              disabled={submittablePaths.length === 0 || importingPaths.size > 0}
              className={`${btn.primary} ${btnSize.md} ml-auto`}
            >
              {t('manualImport.importSelected', { count: submittablePaths.length, defaultValue: `Import ${submittablePaths.length} selected` })}
            </button>
          </div>

          {importError && (
            <p className="mb-4 text-sm text-red-600 dark:text-red-400">{importError}</p>
          )}

          {MATCH_ORDER.map(match => {
            const group = groups[match]
            if (group.length === 0) return null
            return (
              <section key={match} className="mb-6">
                <h3 className="mb-2 text-sm font-semibold text-slate-700 dark:text-zinc-300">
                  {t(`manualImport.group.${match}`, groupHeading(match))} ({group.length})
                </h3>
                <div className="space-y-3">
                  {group.map(it => (
                    <ImportRow
                      key={it.path}
                      item={it}
                      row={rows[it.path]}
                      result={results[it.path]}
                      importing={importingPaths.has(it.path)}
                      onPick={b => pickBook(it.path, b)}
                      onToggle={() => toggleSelected(it.path)}
                      onFormat={f => patchRow(it.path, { format: f })}
                      onImport={() => submit([it.path])}
                    />
                  ))}
                </div>
              </section>
            )
          })}
        </>
      )}
    </div>
  )
}

interface ImportRowProps {
  item: ScanItem
  row: RowState | undefined
  result: BatchImportResult | undefined
  importing: boolean
  onPick: (b: Book) => void
  onToggle: () => void
  onFormat: (f: string) => void
  onImport: () => void
}

function ImportRow({ item, row, result, importing, onPick, onToggle, onFormat, onImport }: ImportRowProps) {
  const { t } = useTranslation()
  const [overriding, setOverriding] = useState(false)
  const chosen = row?.chosen ?? null
  const accepted = result?.accepted

  const badgeCls: Record<ScanItem['match'], string> = {
    confident: 'bg-emerald-100 dark:bg-emerald-950 text-emerald-700 dark:text-emerald-400',
    ambiguous: 'bg-amber-100 dark:bg-amber-950 text-amber-700 dark:text-amber-400',
    none: 'bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400',
  }

  return (
    <div className="rounded-lg border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-3">
      <div className="flex items-start gap-3">
        <input
          type="checkbox"
          className="mt-1"
          checked={Boolean(row?.selected)}
          disabled={!chosen || Boolean(accepted)}
          onChange={onToggle}
          aria-label={t('manualImport.selectUnit', { name: item.name, defaultValue: `Select ${item.name}` })}
        />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-sm truncate">{item.name}</span>
            <span className={`text-[10px] px-1.5 py-0.5 rounded ${badgeCls[item.match]}`}>
              {t(`manualImport.group.${item.match}`, groupHeading(item.match))}
            </span>
            <span className="text-[10px] px-1.5 py-0.5 rounded bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400">
              {item.detectedFormat}
            </span>
          </div>
          {/* Full source path so the user can tell which file each row refers to;
              the basename alone is ambiguous across folders (#1435). */}
          <div className="text-xs font-mono text-slate-500 dark:text-zinc-600 truncate" title={item.path}>{item.path}</div>
          <div className="text-xs text-slate-500 dark:text-zinc-600 truncate">
            {t('manualImport.parsed', { title: item.parsedTitle || '?', author: item.parsedAuthor || '?', defaultValue: `parsed: ${item.parsedTitle || '?'} / ${item.parsedAuthor || '?'}` })}
          </div>

          {/* Resolution area */}
          <div className="mt-2 space-y-2">
            {chosen && !overriding && (
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <span className="text-emerald-700 dark:text-emerald-400">→ {bookLabel(chosen)}</span>
                {!accepted && (
                  <button
                    type="button"
                    onClick={() => setOverriding(true)}
                    className="text-slate-500 dark:text-zinc-400 hover:underline"
                  >
                    {t('manualImport.change', 'Change')}
                  </button>
                )}
              </div>
            )}

            {/* Ambiguous candidate picker */}
            {item.match === 'ambiguous' && !chosen && item.candidates && item.candidates.length > 0 && (
              <fieldset className="space-y-1">
                <legend className="text-xs text-slate-500 dark:text-zinc-500">{t('manualImport.pickCandidate', 'Pick the correct book')}</legend>
                {item.candidates.map(c => (
                  <label key={c.id} className="flex items-center gap-2 text-xs cursor-pointer">
                    <input
                      type="radio"
                      name={`cand-${item.path}`}
                      onChange={() => onPick(c)}
                    />
                    <span>{bookLabel(c)}</span>
                  </label>
                ))}
              </fieldset>
            )}

            {/* None / override: search the existing catalogue */}
            {((item.match === 'none' && !chosen) || overriding) && (
              <BookPicker
                onPick={b => { onPick(b); setOverriding(false) }}
                onCancel={overriding ? () => setOverriding(false) : undefined}
              />
            )}
          </div>
        </div>

        <div className="flex flex-col items-end gap-2 flex-shrink-0">
          <select
            value={row?.format ?? ''}
            onChange={e => onFormat(e.target.value)}
            disabled={Boolean(accepted)}
            aria-label={t('manualImport.formatLabel', 'Format')}
            className="text-xs px-2 py-1 rounded border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-950"
          >
            <option value="">{t('manualImport.formatAuto', 'Auto')}</option>
            <option value="ebook">{t('manualImport.formatEbook', 'Ebook')}</option>
            <option value="audiobook">{t('manualImport.formatAudiobook', 'Audiobook')}</option>
          </select>
          <button
            type="button"
            onClick={onImport}
            disabled={!chosen || importing || Boolean(accepted)}
            className={`${btn.secondary} ${btnSize.sm}`}
          >
            {importing ? t('manualImport.importing', 'Importing…') : t('manualImport.import', 'Import')}
          </button>
        </div>
      </div>

      {result && (
        <p className={`mt-2 text-xs ${accepted ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'}`}>
          {accepted
            ? t('manualImport.queued', 'Queued — importing in the background; watch the Queue for progress.')
            : t('manualImport.failed', { error: result.error || 'failed', defaultValue: `Failed: ${result.error || 'failed'}` })}
        </p>
      )}
    </div>
  )
}

// BookPicker is a debounced search over the EXISTING catalogue (reuses the same
// /book?search= endpoint FixMatchModal uses). It resolves a `none` unit — or an
// override on a matched one — to a book already in the library. Creating a new
// book from metadata is a deferred follow-up and deliberately not offered here.
function BookPicker({ onPick, onCancel }: { onPick: (b: Book) => void; onCancel?: () => void }) {
  const { t } = useTranslation()
  const [term, setTerm] = useState('')
  const [results, setResults] = useState<Book[]>([])
  const [loading, setLoading] = useState(false)

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
        if (!cancelled) setResults(items)
      } catch {
        if (!cancelled) setResults([])
      } finally {
        if (!cancelled) setLoading(false)
      }
    }, 300)
    return () => { cancelled = true; clearTimeout(handle) }
  }, [term])

  return (
    <div className="rounded border border-slate-200 dark:border-zinc-800 p-2">
      <div className="flex gap-2">
        <input
          type="text"
          value={term}
          onChange={e => setTerm(e.target.value)}
          placeholder={t('manualImport.searchPlaceholder', 'Search your library for the book…')}
          aria-label={t('manualImport.searchLabel', 'Search your library')}
          className="flex-1 px-2 py-1 rounded border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-950 text-xs"
        />
        {onCancel && (
          <button
            type="button"
            onClick={onCancel}
            className="text-xs text-slate-500 dark:text-zinc-400 hover:underline"
          >
            {t('common.cancel', 'Cancel')}
          </button>
        )}
      </div>
      <div className="mt-2 max-h-48 overflow-y-auto divide-y divide-slate-100 dark:divide-zinc-800">
        {loading && <p className="py-2 text-xs text-slate-500 dark:text-zinc-500">{t('common.loading', 'Loading…')}</p>}
        {!loading && term.trim().length >= 2 && results.length === 0 && (
          <p className="py-2 text-xs text-slate-500 dark:text-zinc-500">
            {t('manualImport.noResults', 'No matching books in your library. Add the book first, then rescan.')}
          </p>
        )}
        {results.map(b => (
          <button
            key={b.id}
            type="button"
            onClick={() => onPick(b)}
            className="block w-full text-left py-1.5 px-1 rounded hover:bg-slate-100 dark:hover:bg-zinc-800"
          >
            <span className="text-xs font-medium text-slate-900 dark:text-white">{b.title}</span>
            {b.author?.authorName && (
              <span className="text-[11px] text-slate-500 dark:text-zinc-500"> · {b.author.authorName}</span>
            )}
          </button>
        ))}
      </div>
    </div>
  )
}
