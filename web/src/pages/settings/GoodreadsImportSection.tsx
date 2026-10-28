import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, GoodreadsCommitResult, GoodreadsPreview, GoodreadsResolvedRow } from '../../api/client'
import { primaryFill } from '../../components/buttons'

// The three Goodreads "Exclusive Shelf" values. to-read is the default and
// the only one ticked on first render.
const SHELVES = ['to-read', 'currently-reading', 'read'] as const
type Shelf = (typeof SHELVES)[number]

const SHELF_LABEL_KEY: Record<Shelf, string> = {
  'to-read': 'settings.import.goodreadsShelfToRead',
  'currently-reading': 'settings.import.goodreadsShelfCurrentlyReading',
  read: 'settings.import.goodreadsShelfRead',
}

// csvCell quotes a value for CSV output when it contains a comma, quote, or
// newline — matching RFC 4180 so the re-uploadable file round-trips cleanly.
function csvCell(value: string): string {
  if (/[",\n]/.test(value)) {
    return `"${value.replace(/"/g, '""')}"`
  }
  return value
}

// buildUnresolvedCSV produces a Goodreads-shaped CSV of just the rows that
// could not be matched, so the user can fix an ISBN or title and re-import.
function buildUnresolvedCSV(rows: GoodreadsResolvedRow[]): string {
  const header = ['Title', 'Author', 'ISBN', 'ISBN13', 'Exclusive Shelf', 'Reason']
  const lines = [header.join(',')]
  for (const r of rows) {
    lines.push(
      [
        csvCell(r.row.title),
        csvCell(r.row.author),
        csvCell(r.row.isbn ?? ''),
        csvCell(r.row.isbn13 ?? ''),
        csvCell(r.row.exclusiveShelf),
        csvCell(r.reason ?? ''),
      ].join(','),
    )
  }
  return lines.join('\n')
}

export default function GoodreadsImportSection() {
  const { t } = useTranslation()
  const [shelves, setShelves] = useState<Set<Shelf>>(new Set(['to-read']))
  const [uploading, setUploading] = useState(false)
  const [committing, setCommitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [preview, setPreview] = useState<GoodreadsPreview | null>(null)
  const [commitResult, setCommitResult] = useState<GoodreadsCommitResult | null>(null)

  const unresolvedRows = useMemo(
    () => (preview ? preview.rows.filter(r => r.outcome === 'unresolved') : []),
    [preview],
  )

  const toggleShelf = (shelf: Shelf) => {
    setShelves(prev => {
      const next = new Set(prev)
      if (next.has(shelf)) {
        next.delete(shelf)
      } else {
        next.add(shelf)
      }
      // Never allow an empty filter — fall back to to-read.
      if (next.size === 0) {
        next.add('to-read')
      }
      return next
    })
  }

  const upload = async (file: File) => {
    setUploading(true)
    setError(null)
    setPreview(null)
    setCommitResult(null)
    try {
      const fd = new FormData()
      fd.append('file', file)
      fd.append('shelves', Array.from(shelves).join(','))
      const result = await api.goodreadsPreview(fd)
      setPreview(result)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('settings.import.goodreadsUploadFailed'))
    } finally {
      setUploading(false)
    }
  }

  const commit = async () => {
    if (!preview) return
    setCommitting(true)
    setError(null)
    try {
      const result = await api.goodreadsCommit(preview.token)
      setCommitResult(result)
      setPreview(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('common.actionFailed'))
    } finally {
      setCommitting(false)
    }
  }

  const downloadUnresolved = () => {
    if (unresolvedRows.length === 0) return
    const blob = new Blob([buildUnresolvedCSV(unresolvedRows)], { type: 'text/csv' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'goodreads-unmatched.csv'
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <section>
      <h3 className="text-base font-semibold mb-2 text-slate-800 dark:text-zinc-200">
        {t('settings.import.goodreadsHeading')}
      </h3>
      <p className="text-xs text-fg-muted mb-3">
        {t('settings.import.goodreadsDescription')}
      </p>

      <div className="mb-3">
        <span className="block text-xs text-fg-muted mb-1">
          {t('settings.import.goodreadsShelfLabel')}
        </span>
        <div className="flex flex-col gap-1">
          {SHELVES.map(shelf => (
            <label key={shelf} className="inline-flex items-center gap-2 text-sm text-slate-700 dark:text-zinc-300">
              <input
                type="checkbox"
                className="rounded border-slate-300 dark:border-zinc-700"
                checked={shelves.has(shelf)}
                disabled={uploading || committing}
                onChange={() => toggleShelf(shelf)}
              />
              {t(SHELF_LABEL_KEY[shelf])}
            </label>
          ))}
        </div>
        <p className="text-xs text-fg-muted mt-1">
          {t('settings.import.goodreadsShelfHint')}
        </p>
      </div>

      <label className={`inline-flex items-center gap-2 px-3 py-2 ${primaryFill} rounded text-sm font-medium cursor-pointer`}>
        {uploading ? t('settings.import.goodreadsResolving') : t('settings.import.goodreadsUpload')}
        <input
          type="file"
          accept=".csv,text/csv"
          className="hidden"
          disabled={uploading || committing}
          onChange={e => {
            const f = e.target.files?.[0]
            if (f) upload(f)
            e.currentTarget.value = ''
          }}
        />
      </label>

      {error && <p className="mt-2 text-xs text-rose-700 dark:text-rose-400">{error}</p>}

      {preview && (
        <div className="mt-4 p-3 border border-slate-200 dark:border-zinc-800 rounded bg-slate-100 dark:bg-zinc-900 space-y-3">
          <div className="text-sm font-medium text-slate-800 dark:text-zinc-200">
            {t('settings.import.goodreadsPreviewHeading')}
          </div>
          <div className="text-xs text-fg-muted">
            {t('settings.import.goodreadsPreviewSummary', {
              total: preview.totalRows,
              resolved: preview.resolved,
              skippedExisting: preview.skippedExisting,
              skippedShelf: preview.skippedShelf,
              unresolved: preview.unresolved,
            })}
          </div>

          {unresolvedRows.length > 0 && (
            <details className="text-xs">
              <summary className="cursor-pointer text-amber-700 dark:text-amber-400">
                {t('settings.import.goodreadsShowUnresolved', { count: unresolvedRows.length })}
              </summary>
              <ul className="mt-2 space-y-0.5 font-mono">
                {unresolvedRows.map(r => (
                  <li key={r.row.rowNumber}>
                    <span className="text-slate-800 dark:text-zinc-200">{r.row.title}</span>
                    {' — '}
                    <span className="text-fg-muted">{r.reason}</span>
                  </li>
                ))}
              </ul>
              <button
                onClick={downloadUnresolved}
                className="mt-2 px-2 py-1 rounded bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300 hover:bg-slate-300 dark:hover:bg-zinc-700"
              >
                {t('settings.import.goodreadsDownloadFailed')}
              </button>
              <p className="mt-1 text-fg-muted">
                {t('settings.import.goodreadsDownloadHint')}
              </p>
            </details>
          )}

          {preview.resolved === 0 ? (
            <p className="text-xs text-fg-muted">
              {t('settings.import.goodreadsNoResolved')}
            </p>
          ) : (
            <div className="flex gap-2">
              <button
                onClick={commit}
                disabled={committing}
                className={`px-3 py-2 ${primaryFill} rounded text-xs font-medium disabled:opacity-50`}
              >
                {committing
                  ? t('settings.import.goodreadsCommitting')
                  : t('settings.import.goodreadsCommit', { count: preview.resolved })}
              </button>
              <button
                onClick={() => setPreview(null)}
                disabled={committing}
                className="px-3 py-2 bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 rounded text-xs font-medium disabled:opacity-50"
              >
                {t('settings.import.goodreadsCancel')}
              </button>
            </div>
          )}
        </div>
      )}

      {commitResult && (
        <div className="mt-4 p-3 border border-emerald-300 dark:border-emerald-900 rounded bg-emerald-50 dark:bg-emerald-950/30 space-y-1">
          <div className="text-sm text-emerald-700 dark:text-emerald-300">
            {t('settings.import.goodreadsCommitResult', {
              added: commitResult.added,
              skipped: commitResult.skipped,
              failed: commitResult.failed,
            })}
          </div>
          {commitResult.failures && Object.keys(commitResult.failures).length > 0 && (
            <ul className="text-xs space-y-0.5 font-mono">
              {Object.entries(commitResult.failures).map(([title, reason]) => (
                <li key={title}>
                  <span className="text-slate-800 dark:text-zinc-200">{title}</span>
                  {': '}
                  <span className="text-fg-muted">{reason}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </section>
  )
}
