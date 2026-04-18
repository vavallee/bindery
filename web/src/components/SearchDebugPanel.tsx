import { useState } from 'react'
import { SearchDebug } from '../api/client'

interface Props {
  debug: SearchDebug
  resultCount: number
  defaultOpen?: boolean
}

// SearchDebugPanel renders the audit trail attached to a search response so
// users can see exactly why a search returned zero (or unexpected) results.
// Collapsed by default unless the caller opens it (typically when results=0).
export default function SearchDebugPanel({ debug, resultCount, defaultOpen }: Props) {
  const [open, setOpen] = useState(!!defaultOpen)
  const [copied, setCopied] = useState(false)

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(JSON.stringify(debug, null, 2))
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard API unavailable (insecure context, permission denied) —
      // fall back to selecting the <pre> so users can copy manually.
    }
  }

  const anyIndexerErr = debug.indexers.some(i => i.error)
  const rawCount = debug.pipeline.rawCount
  const droppedTotal = rawCount - resultCount

  return (
    <section className="mb-6 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
      <button
        type="button"
        onClick={() => setOpen(o => !o)}
        className="w-full flex items-center justify-between px-4 py-2 text-sm font-medium text-slate-700 dark:text-zinc-300 hover:bg-slate-200 dark:hover:bg-zinc-800 rounded-t-lg"
        aria-expanded={open}
      >
        <span className="flex items-center gap-2">
          <span className="text-slate-500 dark:text-zinc-500">{open ? '▾' : '▸'}</span>
          Search details
          <span className="text-xs text-slate-500 dark:text-zinc-500 font-normal">
            {debug.indexers.length} indexer{debug.indexers.length === 1 ? '' : 's'}
            {' · '}
            {rawCount} raw → {resultCount} shown
            {anyIndexerErr && <span className="ml-2 text-red-600 dark:text-red-400">· indexer error</span>}
          </span>
        </span>
        <span className="text-xs text-slate-500 dark:text-zinc-500 font-normal">
          {debug.durationMs} ms
        </span>
      </button>

      {open && (
        <div className="px-4 pb-4 space-y-4 text-xs">
          <div className="flex justify-end">
            <button
              type="button"
              onClick={copy}
              className="px-2 py-1 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded font-medium"
            >
              {copied ? 'Copied!' : 'Copy debug info (JSON)'}
            </button>
          </div>

          <div>
            <h4 className="font-semibold text-slate-700 dark:text-zinc-300 mb-1">Query</h4>
            <div className="grid grid-cols-2 gap-x-4 gap-y-0.5 text-slate-600 dark:text-zinc-400">
              {debug.query.title && <><span>title</span><span className="font-mono">{debug.query.title}</span></>}
              {debug.query.author && <><span>author</span><span className="font-mono">{debug.query.author}</span></>}
              {debug.query.mediaType && <><span>mediaType</span><span className="font-mono">{debug.query.mediaType}</span></>}
              {debug.query.year ? <><span>year</span><span className="font-mono">{debug.query.year}</span></> : null}
              {debug.query.asin && <><span>ASIN</span><span className="font-mono">{debug.query.asin}</span></>}
              {debug.query.isbn && <><span>ISBN</span><span className="font-mono">{debug.query.isbn}</span></>}
              {debug.query.allowedLanguages && debug.query.allowedLanguages.length > 0 && (
                <><span>allowedLanguages</span><span className="font-mono">{debug.query.allowedLanguages.join(', ')}</span></>
              )}
              {debug.query.freeText && <><span>freeText</span><span className="font-mono">{debug.query.freeText}</span></>}
            </div>
          </div>

          <div>
            <h4 className="font-semibold text-slate-700 dark:text-zinc-300 mb-1">
              Indexers ({debug.indexers.length})
            </h4>
            <div className="border border-slate-200 dark:border-zinc-800 rounded overflow-hidden divide-y divide-slate-200 dark:divide-zinc-800">
              {debug.indexers.map(ix => (
                <div key={`${ix.indexerId}-${ix.indexerName}`} className="px-2 py-1.5 bg-white dark:bg-zinc-950 flex items-start gap-2">
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-slate-800 dark:text-zinc-200">{ix.indexerName}</span>
                      {ix.skipped && (
                        <span className="px-1.5 py-0.5 rounded bg-slate-200 dark:bg-zinc-800 text-slate-600 dark:text-zinc-400">
                          skipped: {ix.skipReason}
                        </span>
                      )}
                      {ix.error && (
                        <span className="px-1.5 py-0.5 rounded bg-red-100 dark:bg-red-950/40 text-red-700 dark:text-red-400">
                          error
                        </span>
                      )}
                    </div>
                    {ix.error && (
                      <div className="text-red-700 dark:text-red-400 break-words font-mono mt-0.5">{ix.error}</div>
                    )}
                    {ix.categories && ix.categories.length > 0 && (
                      <div className="text-slate-500 dark:text-zinc-500 font-mono">
                        categories: {ix.categories.join(', ')}
                      </div>
                    )}
                  </div>
                  <div className="flex-shrink-0 text-right text-slate-600 dark:text-zinc-400">
                    <div>{ix.resultCount} result{ix.resultCount === 1 ? '' : 's'}</div>
                    <div className="text-slate-500 dark:text-zinc-500">{ix.durationMs} ms</div>
                  </div>
                </div>
              ))}
            </div>
          </div>

          <div>
            <h4 className="font-semibold text-slate-700 dark:text-zinc-300 mb-1">Pipeline</h4>
            <div className="font-mono text-slate-600 dark:text-zinc-400 space-y-0.5">
              <div>raw from indexers: {debug.pipeline.rawCount}</div>
              <div>after dedupe: {debug.pipeline.afterDedupe}{' '}
                {debug.pipeline.rawCount !== debug.pipeline.afterDedupe && (
                  <span className="text-amber-600 dark:text-amber-400">(−{debug.pipeline.rawCount - debug.pipeline.afterDedupe})</span>
                )}
              </div>
              <div>after usenet-junk filter: {debug.pipeline.afterUsenetJunk}{' '}
                {debug.pipeline.afterDedupe !== debug.pipeline.afterUsenetJunk && (
                  <span className="text-amber-600 dark:text-amber-400">(−{debug.pipeline.afterDedupe - debug.pipeline.afterUsenetJunk})</span>
                )}
              </div>
              <div>after relevance filter: {debug.pipeline.afterRelevance}{' '}
                {debug.pipeline.afterUsenetJunk !== debug.pipeline.afterRelevance && (
                  <span className="text-amber-600 dark:text-amber-400">(−{debug.pipeline.afterUsenetJunk - debug.pipeline.afterRelevance})</span>
                )}
              </div>
              {droppedTotal > 0 && (
                <div className="pt-1 text-slate-500 dark:text-zinc-500">
                  {droppedTotal} release{droppedTotal === 1 ? '' : 's'} filtered before display
                </div>
              )}
            </div>
          </div>

          {debug.filters.length > 0 && (
            <div>
              <h4 className="font-semibold text-slate-700 dark:text-zinc-300 mb-1">
                Filter decisions ({debug.filters.length})
              </h4>
              <div className="border border-slate-200 dark:border-zinc-800 rounded overflow-hidden divide-y divide-slate-200 dark:divide-zinc-800 max-h-64 overflow-y-auto">
                {debug.filters.map((f, i) => (
                  <div key={i} className="px-2 py-1 bg-white dark:bg-zinc-950">
                    <div className="flex items-center gap-2">
                      <span className="px-1.5 py-0.5 rounded bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300 font-mono">
                        {f.stage}
                      </span>
                      <span className="text-slate-500 dark:text-zinc-500">{f.reason}</span>
                    </div>
                    <div className="font-mono text-slate-600 dark:text-zinc-400 break-all mt-0.5">
                      {f.title}
                      {f.indexerName && <span className="text-slate-500 dark:text-zinc-500"> · {f.indexerName}</span>}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </section>
  )
}
