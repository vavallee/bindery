import { useTranslation } from 'react-i18next'
import type { AdoptionFolderFacet } from '../../api/client'

interface Props {
  folders: AdoptionFolderFacet[]
  total: number
  activeFolder: string
  onShow: (folder: string) => void
}

// The folder rail is navigation: each author folder with the most undecided
// books is one item that filters the list, its count right aligned. An amber
// dot marks a folder whose author is not in the library, which the list then
// shows as one group with Add author. Actions live in the list, not here.
//
// A column beside the list on wide screens, a row of scrolling chips on narrow
// ones.
export default function AdoptionRail({ folders, total, activeFolder, onShow }: Props) {
  const { t } = useTranslation()
  if (folders.length === 0) return null

  const item = (key: string, label: string, count: number, dot: 'amber' | 'slate' | null, active: boolean, onClick: () => void, title?: string) => (
    <li key={key} className="flex-shrink-0 lg:flex-shrink">
      <button
        type="button"
        onClick={onClick}
        aria-current={active ? 'true' : undefined}
        title={title}
        className={`flex w-full items-center gap-2 whitespace-nowrap rounded-md px-2.5 py-1.5 text-left text-sm transition-colors ${
          active
            ? 'bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white font-medium'
            : 'text-fg-muted hover:bg-slate-200/60 dark:hover:bg-zinc-800/60 hover:text-slate-900 dark:hover:text-white'
        }`}
      >
        {dot && <span aria-hidden="true" className={`h-1.5 w-1.5 shrink-0 rounded-full ${dot === 'amber' ? 'bg-amber-500' : 'bg-slate-400 dark:bg-zinc-600'}`} />}
        <span className="min-w-0 flex-1 truncate">{label}</span>
        <span className="tabular-nums text-xs text-fg-muted">{count}</span>
      </button>
    </li>
  )

  return (
    <nav aria-label={t('adoption.rail.label', 'Folders with the most books to decide')} className="mb-4 lg:mb-0">
      <h3 className="hidden lg:block mb-2 px-2.5 text-[11px] font-semibold uppercase tracking-wide text-fg-muted">
        {t('adoption.rail.heading', 'Folders')}
      </h3>
      <ul className="flex gap-1 overflow-x-auto pb-1 lg:flex-col lg:overflow-visible lg:pb-0">
        {item('__all', t('adoption.rail.all', 'All folders'), total, null, activeFolder === '', () => onShow(''))}
        {folders.map(f => {
          const missing = f.notInLibrary > 0 && f.notInLibrary === f.units
          return item(
            f.folder, f.folder, f.units, missing ? 'amber' : 'slate', f.folder === activeFolder,
            () => onShow(f.folder === activeFolder ? '' : f.folder),
            missing ? t('adoption.rail.notInLibrary', { author: f.author || f.folder, defaultValue: '{{author}} is not in your library' }) : undefined,
          )
        })}
      </ul>
    </nav>
  )
}
