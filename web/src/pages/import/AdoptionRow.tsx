import { forwardRef, type KeyboardEvent } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { AdoptionItem } from '../../api/client'
import { btn, btnSize } from '../../components/buttons'
import MediaBadge from '../../components/MediaBadge'
import MoreMenu, { type MoreMenuItem } from '../../components/MoreMenu'
import { formatBytes } from '../../util/format'
import { adoptionHint, unitDisplayName } from './adoptionHint'
import { StatusPill } from './AdoptionEditor'
import { alreadySettled, inOtherFormatRoot, matchStrength, preselectable, shortHint } from './adoptionMatch'
import type { Outcome } from './adoptionReducer'
import { rowCls, cellCls, actionCellCls } from './adoptionStyles'

interface Props {
  item: AdoptionItem
  outcome: Outcome | undefined
  error: string | undefined
  note: 'keptBook' | undefined
  expanded: boolean
  focusable: boolean
  inGroup: boolean
  editorId: string
  onFocusRow: () => void
  onKeyDown: (e: KeyboardEvent<HTMLTableRowElement>) => void
  onOpen: (showFiles: boolean) => void
  onConfirm: () => void
  onIgnore: () => void
  onUndo: () => void
  onAddAuthor: (name: string) => void
}

// AdoptionRow is one book: what it is, one line on why it is here, and a fixed
// action cell holding the single most likely action plus a More menu. Only a
// strong suggestion (adoptionMatch) earns a one click Confirm; a possible one
// is a quiet link that opens the editor with it preselected. A decision turns
// the middle cell into a quiet line and the action into Undo, until the next
// fetch.
const AdoptionRow = forwardRef<HTMLTableRowElement, Props>(function AdoptionRow(
  { item, outcome, error, note, expanded, focusable, inGroup, editorId, onFocusRow, onKeyDown, onOpen, onConfirm, onIgnore, onUndo, onAddAuthor }, ref,
) {
  const { t } = useTranslation()
  const name = unitDisplayName(item)
  // With an author conflict a folder author's look alike is not offered here;
  // the row says which authors disagree instead (#2942).
  const top = preselectable(item)
  const strength = matchStrength(item, top)
  const full = adoptionHint(item, t)
  const tooltip = [full.sentence, full.tooltip].filter(Boolean).join('\n')
  const size = formatBytes(item.sizeBytes)
  const pending = item.state === 'pending' && !outcome
  const authorFirst = pending && !inGroup && !top && item.reason === 'author_not_in_library' && item.parsedAuthor !== ''
  // A file too small to be a book cannot be adopted (the server refuses it),
  // so its one action is Ignore (#2944).
  const tooSmall = pending && item.reason === 'too_small'
  // Which folder a row sits in matters when it is the other format's: an
  // ebook under the audiobooks root is not an ordinary ebook row (#2944).
  const rootLabel = inOtherFormatRoot(item)
    ? item.rootFormat === 'audiobook'
      ? t('adoption.row.inAudiobookRoot', 'In your audiobooks folder')
      : t('adoption.row.inEbookRoot', 'In your ebooks folder')
    : ''

  const outcomeLine = (() => {
    switch (outcome?.kind) {
      case undefined: return null
      case 'adopting': return t('adoption.outcome.adopting', { title: outcome.preview?.title ?? name, defaultValue: 'Adopting as {{title}}…' })
      case 'adopted': {
        const book = outcome.item.book
        const base = t('adoption.outcome.adopted', { title: book?.title ?? name, author: book?.authorName ?? '', defaultValue: 'Adopted as {{title}} by {{author}}.' })
        if (outcome.item.bookCreated) return `${base} ${t('adoption.outcome.created', 'Added to your library, unmonitored.')}`
        // The server says when the book already had a file of this format.
        return outcome.item.message ? `${base} ${t('adoption.outcome.addedAlongside', 'Added alongside the file it already had.')}` : base
      }
      case 'ignoring':
      case 'ignored': return t('adoption.outcome.ignored', 'Ignored. Later scans keep it out of this list.')
      case 'undoing': return t('adoption.outcome.undoing', 'Undoing…')
      case 'restored': return t('adoption.outcome.restored', 'Back in Needs a decision.')
    }
  })()
  const canUndo = outcome?.kind === 'adopted' || outcome?.kind === 'ignored'

  const menu: MoreMenuItem[] = []
  if (pending) {
    if (strength === 'strong' || authorFirst) menu.push({ label: t('adoption.choose', 'Choose book'), onSelect: () => onOpen(false) })
    if (item.fileCount > 1) menu.push({ label: t('adoption.showFiles', 'Show files'), onSelect: () => onOpen(true) })
    if (!tooSmall) menu.push({ label: t('adoption.ignore', 'Ignore'), onSelect: onIgnore })
  }

  let primary: React.ReactNode
  if (canUndo) {
    primary = <button type="button" onClick={onUndo} aria-keyshortcuts="u" className={`${btn.secondary} ${btnSize.sm}`}>{t('adoption.undo', 'Undo')}</button>
  } else if (outcome) {
    primary = null
  } else if (item.state === 'ignored' || item.state === 'adopted') {
    primary = (
      <button type="button" onClick={onUndo} aria-keyshortcuts="u" className={`${btn.secondary} ${btnSize.sm}`}>
        {item.state === 'ignored' ? t('adoption.unignore', 'Unignore') : t('adoption.undo', 'Undo')}
      </button>
    )
  } else if (tooSmall) {
    primary = <button type="button" onClick={onIgnore} aria-keyshortcuts="i" className={`${btn.secondary} ${btnSize.sm}`}>{t('adoption.ignore', 'Ignore')}</button>
  } else if (strength === 'strong') {
    primary = <button type="button" onClick={onConfirm} className={`${btn.primary} ${btnSize.sm}`}>{t('adoption.confirm', 'Confirm')}</button>
  } else if (authorFirst) {
    primary = <button type="button" onClick={() => onAddAuthor(item.parsedAuthor)} className={`${btn.primary} ${btnSize.sm}`}>{t('adoption.addAuthor', 'Add author')}</button>
  } else {
    primary = (
      <button type="button" onClick={() => onOpen(false)} aria-expanded={expanded} aria-controls={editorId} className={`${btn.secondary} ${btnSize.sm}`}>
        {t('adoption.choose', 'Choose book')}
      </button>
    )
  }

  return (
    <tr ref={ref} tabIndex={focusable ? 0 : -1} aria-label={name} onKeyDown={onKeyDown}
      onFocus={e => { if (e.target === e.currentTarget) onFocusRow() }}
      className={rowCls(expanded, inGroup)}
    >
      <td className={`${cellCls} md:w-[42%] ${inGroup ? 'md:pl-9' : ''}`}>
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate font-medium text-slate-800 dark:text-zinc-200">{name}</span>
          <MediaBadge type={item.format} />
          {rootLabel && (
            <span className="shrink-0 rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-800 dark:bg-amber-950 dark:text-amber-300" title={item.rootPath}>
              {rootLabel}
            </span>
          )}
        </div>
        <p className="mt-0.5 truncate text-xs text-fg-muted" title={`${item.rootPath}/${item.relPath}`}>
          <span className="tabular-nums">
            {t('adoption.row.files', { count: item.fileCount, defaultValue: '{{count}} files' })}{size ? ` · ${size}` : ''}
          </span>
          <span className="font-mono"> · {item.relPath}</span>
        </p>
      </td>
      <td className={`${cellCls} md:w-[36%]`}>
        {outcomeLine ? (
          <p role="status" className="truncate text-xs text-emerald-700 dark:text-emerald-400" title={outcomeLine}>{outcomeLine}</p>
        ) : item.state === 'adopted' && item.book ? (
          <p className="truncate text-xs text-fg-muted">
            {t('adoption.row.adoptedAs', 'Adopted as')}{' '}
            <Link to={`/book/${item.book.id}`} className="font-medium text-slate-800 dark:text-zinc-200 hover:text-emerald-600">{item.book.title}</Link>
          </p>
        ) : top ? (
          <p className="flex min-w-0 items-center gap-2 text-xs" title={tooltip}>
            <span className={`shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium ${strength === 'strong' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300' : 'bg-slate-200 text-slate-700 dark:bg-zinc-800 dark:text-zinc-300'}`}>
              {strength === 'strong' ? t('adoption.match.strong', 'Strong match') : t('adoption.match.possible', 'Possible match')}
            </span>
            {strength === 'strong' ? (
              <span className="truncate text-slate-700 dark:text-zinc-300">{top.book.title}</span>
            ) : (
              <button type="button" onClick={() => onOpen(false)} className="truncate text-slate-700 dark:text-zinc-300 underline decoration-dotted underline-offset-2 hover:text-emerald-700 dark:hover:text-emerald-400">
                {top.book.title}
              </button>
            )}
            {alreadySettled(top.book.status) && <StatusPill status={top.book.status} monitored={top.book.monitored} />}
          </p>
        ) : inGroup ? (
          // The group row above already says why; repeating it per book is noise.
          <span className="sr-only">{shortHint(item, t)}</span>
        ) : (
          <p className="truncate text-xs text-fg-muted" title={tooltip}>{shortHint(item, t)}</p>
        )}
        {note === 'keptBook' && (
          <p role="status" className="mt-0.5 truncate text-xs text-fg-muted" title={t('adoption.outcome.keptBook', 'Files removed. The book stayed because it is now in use.')}>
            {t('adoption.outcome.keptBook', 'Files removed. The book stayed because it is now in use.')}
          </p>
        )}
        {error && <p role="alert" className="mt-0.5 truncate text-xs text-red-600 dark:text-red-400" title={error}>{error}</p>}
      </td>
      <td className={actionCellCls}>
        <div className="flex items-center gap-1.5 md:justify-end">
          {primary}
          {menu.length > 0 && (
            <MoreMenu items={menu} label={t('adoption.more', 'More')} ariaLabel={t('adoption.moreFor', { name, defaultValue: 'More actions for {{name}}' })}
              buttonClassName={`${btn.ghost} ${btnSize.sm}`} />
          )}
        </div>
      </td>
    </tr>
  )
})

export default AdoptionRow
