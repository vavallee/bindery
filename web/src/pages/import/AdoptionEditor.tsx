import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import type { AdoptionBookRef, AdoptionItem, AdoptTarget, Book } from '../../api/client'
import { btn, btnSize } from '../../components/buttons'
import BookPicker from '../../components/import/BookPicker'
import CatalogueAdder from '../../components/import/CatalogueAdder'
import { bookStatusBadge } from '../../components/bookStatus'
import { adoptionHint, scorePercent, unitDisplayName } from './adoptionHint'
import { preselectable } from './adoptionMatch'

interface Props {
  item: AdoptionItem
  onAdopt: (target: AdoptTarget, preview: AdoptionBookRef | null) => void | Promise<void>
  onCancel: () => void
  // Opens the file list, for the row's Show files action.
  showFiles?: boolean
}

function refFromBook(b: Book): AdoptionBookRef {
  return {
    id: b.id, title: b.title, authorId: b.authorId, authorName: b.author?.authorName ?? '',
    imageUrl: b.imageUrl, status: b.status, mediaType: b.mediaType ?? '', monitored: b.monitored,
  }
}

// StatusPill shows a suggested book's status, so a book that already has its
// files or was skipped is visible as such before it is picked (#2879).
export function StatusPill({ status, monitored }: { status: string; monitored: boolean }) {
  const { t } = useTranslation()
  const badge = bookStatusBadge(status, monitored, t)
  return (
    <span title={badge.description} className={`shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium ${badge.colorClass}`}>
      {badge.label}
    </span>
  )
}

// AdoptionEditor opens in place under its row, never as a dialog: the
// suggestions as radio rows with their scores, a search of the library,
// prefilled and focused, and a collapsed metadata search that asks a provider
// only when submitted. Esc closes it and focus goes back to the row.
export default function AdoptionEditor({ item, onAdopt, onCancel, showFiles = false }: Props) {
  const { t } = useTranslation()
  const headingId = useId()
  const searchRef = useRef<HTMLInputElement>(null)
  // A folder author's look alike is listed but never chosen for the reader
  // when the files name someone else (#2942).
  const [chosen, setChosen] = useState<AdoptionBookRef | null>(preselectable(item)?.book ?? null)
  const [searched, setSearched] = useState<AdoptionBookRef | null>(null)
  const name = unitDisplayName(item)

  useEffect(() => { searchRef.current?.focus({ preventScroll: showFiles }) }, [showFiles])

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onCancel()
    }
  }
  const pickSearched = (b: Book) => {
    const ref = refFromBook(b)
    setSearched(ref)
    setChosen(ref)
  }
  const conflict = item.authorConflict
  const options: { book: AdoptionBookRef; score?: number; folderAuthorOnly?: boolean }[] = [
    ...item.candidates,
    ...(searched && !item.candidates.some(c => c.book.id === searched.id) ? [{ book: searched }] : []),
  ]
  const more = item.fileCount - item.members.length

  return (
    <div role="region" aria-labelledby={headingId} onKeyDown={onKeyDown} className="space-y-4">
      <h4 id={headingId} className="text-sm font-semibold text-slate-800 dark:text-zinc-200">
        {t('adoption.editor.heading', { name, defaultValue: 'Which book is {{name}}?' })}
      </h4>
      {conflict && (
        <p role="note" aria-label={t('adoption.editor.conflictLabel', 'Author conflict')} className="-mt-2 text-xs text-amber-800 dark:text-amber-400">
          {t('adoption.editor.conflict', {
            files: conflict.files, folder: conflict.folder,
            defaultValue: 'The files say {{files}}, but the folder says {{folder}}. A book by {{folder}} is only a look alike, so none is chosen for you.',
          })}
        </p>
      )}
      <p className="-mt-2 text-xs text-fg-muted">{adoptionHint(item, t).sentence}</p>

      {options.length > 0 && (
        <fieldset>
          <legend className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-fg-muted">
            {t('adoption.editor.suggestions', 'Suggestions')}
          </legend>
          <div className="divide-y divide-slate-200 dark:divide-zinc-800 rounded border border-slate-200 dark:border-zinc-800">
            {options.map(o => {
              const pct = o.score === undefined ? null : scorePercent(o.score)
              return (
                <label key={o.book.id} className="flex cursor-pointer items-center gap-3 px-3 py-2 hover:bg-slate-100 dark:hover:bg-zinc-800/60">
                  <input
                    type="radio"
                    name={`adopt-${item.id}`}
                    checked={chosen?.id === o.book.id}
                    onChange={() => setChosen(o.book)}
                    className="text-emerald-600 focus:ring-emerald-500"
                  />
                  <span className="min-w-0 flex-1 truncate text-sm text-slate-800 dark:text-zinc-200">
                    {o.book.title}
                    {o.book.authorName && <span className="text-xs text-fg-muted"> · {o.book.authorName}</span>}
                  </span>
                  {o.folderAuthorOnly && (
                    <span className="shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300">
                      {t('adoption.editor.folderAuthorOnly', 'Folder author only')}
                    </span>
                  )}
                  <StatusPill status={o.book.status} monitored={o.book.monitored} />
                  {pct !== null ? (
                    <span className="flex items-center gap-2" aria-label={t('adoption.editor.score', { percent: pct, defaultValue: '{{percent}}% title match' })}>
                      <span aria-hidden="true" className="hidden sm:block h-1.5 w-24 overflow-hidden rounded-full bg-slate-200 dark:bg-zinc-800">
                        <span className={`block h-full rounded-full ${pct >= 80 && !o.folderAuthorOnly ? 'bg-emerald-500' : 'bg-amber-500'}`} style={{ width: `${pct}%` }} />
                      </span>
                      <span className="w-9 text-right text-xs tabular-nums text-fg-muted">{pct}%</span>
                    </span>
                  ) : (
                    <span className="text-xs text-fg-muted">{t('adoption.editor.fromSearch', 'From search')}</span>
                  )}
                </label>
              )
            })}
          </div>
        </fieldset>
      )}
      {chosen?.status === 'imported' && (
        <p role="note" className="-mt-2 text-xs text-amber-800 dark:text-amber-400">
          {t('adoption.editor.alreadyImported', 'This book already has its files. Adopting adds these alongside them, and nothing is replaced or deleted.')}
        </p>
      )}

      <div className="grid gap-3 md:grid-cols-2">
        <div className="space-y-2">
          <p className="text-[11px] font-semibold uppercase tracking-wide text-fg-muted">{t('adoption.editor.searchLibrary', 'Search your library')}</p>
          <BookPicker ref={searchRef} initialTerm={item.parsedTitle} onPick={pickSearched} selectedId={searched?.id ?? null} />
          <CatalogueAdder
            initialQuery={[item.parsedTitle, item.parsedAuthor].filter(Boolean).join(' ')}
            hint={t('adoption.editor.metadataHint', 'Adds the book to your library unmonitored, as the format of these files, and adopts them. Nothing is downloaded.')}
            actionLabel={t('adoption.editor.addAndAdopt', 'Add and adopt')}
            busyLabel={t('adoption.editor.adding', 'Adding…')}
            onChoose={async b => {
              await onAdopt({
                foreignBookId: b.foreignBookId,
                foreignAuthorId: b.author?.foreignAuthorId ?? '',
                authorName: b.author?.authorName ?? '',
              }, null)
            }}
          />
        </div>
        <div className="space-y-3">
          {item.members.length > 1 && (
            <details className="text-xs" open={showFiles}>
              <summary className="cursor-pointer text-slate-600 dark:text-zinc-400">
                {t('adoption.editor.files', { count: item.fileCount, defaultValue: '{{count}} files' })}
              </summary>
              <ul className="mt-1 max-h-40 overflow-y-auto font-mono text-[11px] text-slate-500 dark:text-zinc-500">
                {item.members.map(m => <li key={m} className="truncate">{m}</li>)}
                {more > 0 && <li>{t('adoption.editor.moreFiles', { count: more, defaultValue: 'and {{count}} more' })}</li>}
              </ul>
            </details>
          )}
          <p className="text-[11px] text-fg-muted">
            {t('adoption.editor.inPlace', 'Files stay where they are. Picking a book already in your library leaves its owner and monitoring as they are.')}
          </p>
        </div>
      </div>

      <div className="flex items-center justify-end gap-2 border-t border-slate-200 dark:border-zinc-800 pt-3">
        <button type="button" onClick={onCancel} className={`${btn.ghost} ${btnSize.md}`}>
          {t('common.cancel', 'Cancel')}
        </button>
        <button
          type="button"
          disabled={!chosen}
          onClick={() => chosen && onAdopt({ bookId: chosen.id }, chosen)}
          className={`${btn.primary} ${btnSize.md}`}
        >
          {chosen
            ? t('adoption.editor.adoptAs', { title: chosen.title, defaultValue: 'Adopt as {{title}}' })
            : t('adoption.editor.pickFirst', 'Pick a book')}
        </button>
      </div>
    </div>
  )
}
