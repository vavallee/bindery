import { forwardRef, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { btn, btnSize } from '../../components/buttons'
import MoreMenu from '../../components/MoreMenu'
import type { AdoptionGroup } from './adoptionGroups'
import { actionCellCls, cellCls, rowCls } from './adoptionStyles'

interface Props {
  group: AdoptionGroup
  open: boolean
  focusable: boolean
  onFocusRow: () => void
  onKeyDown: (e: KeyboardEvent<HTMLTableRowElement>) => void
  onToggle: () => void
  onAddAuthor: (name: string) => void
  onIgnoreFolder: (folder: string) => void
}

// AdoptionGroupRow stands for every book on the page under one author folder
// whose author is not in the library. It says so once, offers Add author once,
// and discloses the books, which keep their own Choose book and Ignore.
const AdoptionGroupRow = forwardRef<HTMLTableRowElement, Props>(function AdoptionGroupRow(
  { group, open, focusable, onFocusRow, onKeyDown, onToggle, onAddAuthor, onIgnoreFolder }, ref,
) {
  const { t } = useTranslation()
  const panelLabel = open ? t('adoption.group.hide', 'Hide books') : t('adoption.group.show', 'Show books')
  return (
    <tr ref={ref} tabIndex={focusable ? 0 : -1} onKeyDown={onKeyDown} aria-expanded={open}
      aria-label={t('adoption.group.label', { author: group.author, count: group.items.length, defaultValue: '{{author}}, {{count}} books' })}
      onFocus={e => { if (e.target === e.currentTarget) onFocusRow() }}
      className={rowCls(false, false)}
    >
      <td className={`${cellCls} md:w-[42%]`}>
        <button type="button" onClick={onToggle} aria-expanded={open} className="group flex min-w-0 max-w-full items-center gap-2 text-left">
          <svg aria-hidden="true" viewBox="0 0 20 20" fill="currentColor" className={`h-4 w-4 shrink-0 text-fg-muted transition-transform ${open ? 'rotate-90' : ''}`}>
            <path fillRule="evenodd" d="M7.2 14.8a.75.75 0 0 1 0-1.06L10.94 10 7.2 6.26a.75.75 0 1 1 1.06-1.06l4.27 4.27a.75.75 0 0 1 0 1.06L8.26 14.8a.75.75 0 0 1-1.06 0Z" clipRule="evenodd" />
          </svg>
          <span className="min-w-0">
            <span className="block truncate font-medium text-slate-800 dark:text-zinc-200 group-hover:text-success">{group.author}</span>
            <span className="block truncate text-xs text-fg-muted tabular-nums">
              {t('adoption.group.counts', { count: group.items.length, files: group.files, defaultValue: '{{count}} books, {{files}} files' })}
              <span className="font-mono"> · {group.folder}/</span>
            </span>
          </span>
        </button>
      </td>
      <td className={`${cellCls} md:w-[36%]`}>
        <p className="truncate text-xs text-warning"
          title={t('adoption.hint.authorMissing', { author: group.author, defaultValue: '{{author}} is not in your library yet. Add the author, then scan again.' })}>
          {t('adoption.group.sentence', 'Author not in your library yet')}
        </p>
      </td>
      <td className={actionCellCls}>
        <div className="flex items-center gap-1.5 md:justify-end">
          <button type="button" onClick={() => onAddAuthor(group.author)} className={`${btn.primary} ${btnSize.sm}`}>
            {t('adoption.addAuthor', 'Add author')}
          </button>
          <MoreMenu
            label={t('adoption.more', 'More')}
            ariaLabel={t('adoption.moreFor', { name: group.author, defaultValue: 'More actions for {{name}}' })}
            buttonClassName={`${btn.ghost} ${btnSize.sm}`}
            items={[
              { label: panelLabel, onSelect: onToggle },
              { label: t('adoption.rail.ignore', 'Ignore folder'), onSelect: () => onIgnoreFolder(group.folder) },
            ]}
          />
        </div>
      </td>
    </tr>
  )
})

export default AdoptionGroupRow
