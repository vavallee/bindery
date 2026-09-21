import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { BatchImportItem, BatchImportResult, Book, ScanItem } from '../../api/client'
import { btn, btnSize } from '../../components/buttons'
import { useFolderScan } from '../../components/useFolderScan'
import FolderImportRow from './FolderImportRow'
import { groupHeading, type RowState } from './folderImport'

// FolderImportView is the Import page's "From a folder" view, the manual
// import wizard (#1236). It scans a folder with
// the recursive bulk-import scan (#1434), groups the discovered units by match
// confidence, and lets the user resolve each one before importing:
//   - confident: a preselected catalogue match, deselectable and overridable;
//   - ambiguous: a candidate picker over the returned catalogue candidates;
//   - none: a search box to bind the unit to an EXISTING catalogue book, plus
//     a metadata search that creates the book (and its author) when the library
//     has no row to bind to (#1719).
// Resolved units are imported (per-unit or in bulk) through the batch endpoint,
// which reports per-item success/failure.

const MATCH_ORDER: ScanItem['match'][] = ['confident', 'ambiguous', 'none']

export default function FolderImportView() {
  const { t } = useTranslation()
  const [rows, setRows] = useState<Record<string, RowState>>({})
  const [results, setResults] = useState<Record<string, BatchImportResult>>({})
  const [importingPaths, setImportingPaths] = useState<Set<string>>(() => new Set())
  const [importError, setImportError] = useState('')

  const folderScan = useFolderScan(scanned => {
    setResults({})
    const init: Record<string, RowState> = {}
    for (const it of scanned) {
      const chosen = it.match === 'confident' && it.book ? it.book : null
      init[it.path] = { chosen, format: it.detectedFormat || '', selected: Boolean(chosen) && !it.alreadyImported }
    }
    setRows(init)
  })
  const { path, setPath, scanning, items, truncated, showImported, error: scanError, toggleShowImported } = folderScan

  // A scan attempt (fresh or re-triggered by the toggle) supersedes whatever
  // the last import attempt reported.
  const handleScan = () => { setImportError(''); folderScan.scan() }
  const handleToggleShowImported = (checked: boolean) => { setImportError(''); toggleShowImported(checked) }

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
    // be selected. Already-imported units are deliberately left out of "select
    // all" too: they start unchecked so a bulk action can't silently re-import
    // them, and a bulk toggle re-including them here would defeat that (#2480).
    const resolvable = items
      .filter(it => !it.alreadyImported)
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
      <div className="flex items-center gap-3 mb-4">
        <p className="text-sm text-slate-600 dark:text-zinc-400">
          {t('manualImport.description', 'Scan a folder of files already on disk, match each book to your library, and import them. A file with no match can be added from a metadata search.')}
        </p>
        <label className="ml-auto flex items-center gap-1.5 text-xs text-slate-600 dark:text-zinc-400 cursor-pointer select-none">
          <input
            type="checkbox"
            checked={showImported}
            onChange={e => handleToggleShowImported(e.target.checked)}
            disabled={scanning}
            className="rounded border-slate-400 dark:border-zinc-600 text-emerald-600 focus:ring-emerald-500 focus:ring-offset-0"
          />
          {t('manualImport.showImported', 'Show already imported')}
        </label>
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

      {truncated && (
        <p className="mb-3 text-xs text-amber-700 dark:text-amber-400">
          {t('manualImport.truncated', 'Showing the first 1000 units; narrow the folder to see the rest.')}
        </p>
      )}

      {items && items.length === 0 && (
        <p className="text-sm text-slate-500 dark:text-zinc-500">
          {t('manualImport.empty', 'No book files or folders found here.')}
        </p>
      )}

      {items && items.length > 0 && (
        <>
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
                    <FolderImportRow
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

