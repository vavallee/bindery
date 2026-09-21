import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { BatchImportResult, Book, ScanItem } from '../../api/client'
import { btn, btnSize } from '../../components/buttons'
import BookPicker from '../../components/import/BookPicker'
import CatalogueAdder from '../../components/import/CatalogueAdder'
import { groupHeading, type RowState } from './folderImport'

function bookLabel(b: Book): string {
  return b.author?.authorName ? `${b.title} · ${b.author.authorName}` : b.title
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

export default function FolderImportRow({ item, row, result, importing, onPick, onToggle, onFormat, onImport }: ImportRowProps) {
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
            {item.alreadyImported && (
              <span className="text-[10px] px-1.5 py-0.5 rounded bg-sky-100 dark:bg-sky-950 text-sky-700 dark:text-sky-400">
                {t('manualImport.alreadyImported', 'already imported')}
              </span>
            )}
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

            {/* None / override: search the existing catalogue, or add the book
                from provider metadata when the library has nothing to bind to. */}
            {((item.match === 'none' && !chosen) || overriding) && (
              <>
                <BookPicker
                  onPick={b => { onPick(b); setOverriding(false) }}
                  onCancel={overriding ? () => setOverriding(false) : undefined}
                />
                {/* searchOnAdd is deliberately false: the file is already on
                    disk, so an indexer search would grab a second copy. */}
                <CatalogueAdder
                  initialQuery={[item.parsedTitle, item.parsedAuthor].filter(Boolean).join(' ')}
                  onChoose={async b => {
                    const created = await api.addBook({
                      foreignBookId: b.foreignBookId,
                      // May be empty (a Google Books or DNB result carries a
                      // name but no author id); the backend resolves it by name.
                      foreignAuthorId: b.author?.foreignAuthorId ?? '',
                      authorName: b.author?.authorName ?? '',
                      searchOnAdd: false,
                    })
                    onPick(created)
                    setOverriding(false)
                  }}
                />
              </>
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
            disabled={!chosen || !row?.selected || importing || Boolean(accepted)}
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
