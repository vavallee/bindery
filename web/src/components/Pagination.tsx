import { useTranslation } from 'react-i18next'

interface Props {
  page: number
  totalPages: number
  pageSize: number
  totalItems: number
  onPageChange: (page: number) => void
  onPageSizeChange: (size: number) => void
  pageSizeOptions?: number[]
}

export default function Pagination({
  page, totalPages, pageSize, totalItems,
  onPageChange, onPageSizeChange,
  pageSizeOptions = [25, 50, 100, 250],
}: Props) {
  const { t } = useTranslation()
  if (totalItems === 0) return null

  const start = (page - 1) * pageSize + 1
  const end = Math.min(page * pageSize, totalItems)

  // Compute visible page numbers. Large ranges collapse into gaps, each of
  // which renders as a dropdown of the pages it hides (#2010) rather than a
  // dead "…" that forces you to step through one page at a time.
  type Gap = { gapFrom: number; gapTo: number }
  const pages: (number | Gap)[] = []
  if (totalPages <= 7) {
    for (let i = 1; i <= totalPages; i++) pages.push(i)
  } else {
    pages.push(1)
    const startP = Math.max(2, page - 1)
    const endP = Math.min(totalPages - 1, page + 1)
    if (page > 3) pages.push({ gapFrom: 2, gapTo: startP - 1 })
    for (let i = startP; i <= endP; i++) pages.push(i)
    if (page < totalPages - 2) pages.push({ gapFrom: endP + 1, gapTo: totalPages - 1 })
    pages.push(totalPages)
  }

  const btnBase = 'px-2.5 py-1 rounded text-xs font-medium transition-colors'
  const btn = `${btnBase} text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200 dark:hover:bg-zinc-800 disabled:opacity-30 disabled:hover:bg-transparent disabled:cursor-not-allowed`
  const btnActive = `${btnBase} bg-slate-300 dark:bg-zinc-700 text-slate-900 dark:text-white`
  const selectBase = 'bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1 text-xs text-slate-800 dark:text-zinc-200 focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600'
  // The gap select is styled to read as the "…" it replaces: no chrome until
  // you hover or focus it, at which point it is visibly a control.
  const gapSelect = `${btnBase} appearance-none cursor-pointer bg-transparent text-center text-slate-500 dark:text-zinc-600 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200 dark:hover:bg-zinc-800 focus:outline-none focus:ring-1 focus:ring-slate-400 dark:focus:ring-zinc-600`

  return (
    <div className="flex flex-col items-center gap-3 mt-6 pt-4 border-t border-slate-200 dark:border-zinc-800 sm:flex-row sm:justify-between">
      <div className="text-xs text-slate-600 dark:text-zinc-500">
        {start}–{end} of {totalItems}
      </div>
      <div className="flex flex-wrap justify-center items-center gap-1">
        <button onClick={() => onPageChange(1)} disabled={page === 1} className={btn}>«</button>
        <button onClick={() => onPageChange(page - 1)} disabled={page === 1} className={btn}>‹ {t('pagination.previous')}</button>
        {pages.map((p, i) =>
          typeof p === 'object' ? (
            <select
              key={`gap${i}`}
              aria-label={t('pagination.jumpToPage')}
              value=""
              onChange={e => {
                const target = Number(e.target.value)
                if (target) onPageChange(target)
              }}
              className={gapSelect}
            >
              <option value="">…</option>
              {Array.from({ length: p.gapTo - p.gapFrom + 1 }, (_, n) => p.gapFrom + n).map(n => (
                <option key={n} value={n}>{n}</option>
              ))}
            </select>
          ) : (
            <button key={p} onClick={() => onPageChange(p)} className={p === page ? btnActive : btn}>
              {p}
            </button>
          )
        )}
        <button onClick={() => onPageChange(page + 1)} disabled={page === totalPages} className={btn}>{t('pagination.next')} ›</button>
        <button onClick={() => onPageChange(totalPages)} disabled={page === totalPages} className={btn}>»</button>
      </div>
      <div className="flex items-center gap-2">
        <span className="text-xs text-slate-600 dark:text-zinc-500">{t('pagination.perPage')}:</span>
        <select
          aria-label={t('pagination.perPage')}
          value={pageSize}
          onChange={e => onPageSizeChange(Number(e.target.value))}
          className={selectBase}
        >
          {pageSizeOptions.map(n => (
            <option key={n} value={n}>{n}</option>
          ))}
        </select>
      </div>
    </div>
  )
}

