import { useEffect, useRef, type KeyboardEvent } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../../auth/AuthContext'
import AdoptionView from './AdoptionView'
import FolderImportView from './FolderImportView'

type View = 'library' | 'folder'

const VIEWS: View[] = ['library', 'folder']

// ImportPage is where files already on disk come into Bindery, two ways:
//   - "In your library" (default): the books a library scan found but could
//     not match, adopted in place (AdoptionView);
//   - "From a folder" (?view=folder): pick a folder, match and import it,
//     moving files into the library (FolderImportView).
// Both used to live apart (a read only table in Settings, a folder scan in
// Settings > Import and this page); this is now the one place.
export default function ImportPage() {
  const { t } = useTranslation()
  const { isAdmin } = useAuth()
  const [params, setParams] = useSearchParams()
  const view: View = params.get('view') === 'folder' ? 'folder' : 'library'
  const tabRefs = useRef<Record<View, HTMLButtonElement | null>>({ library: null, folder: null })

  useEffect(() => {
    document.title = `${t('importPage.title', 'Import')} · Bindery`
    return () => { document.title = 'Bindery' }
  }, [t])

  const select = (next: View) => {
    const p = new URLSearchParams(params)
    if (next === 'folder') p.set('view', 'folder')
    else p.delete('view')
    setParams(p, { replace: true })
  }

  // Tabs follow the WAI-ARIA pattern: arrows move between them.
  const onTabKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return
    e.preventDefault()
    const next = VIEWS[(VIEWS.indexOf(view) + (e.key === 'ArrowRight' ? 1 : VIEWS.length - 1)) % VIEWS.length]
    select(next)
    tabRefs.current[next]?.focus()
  }

  const tabCls = (active: boolean) =>
    `px-3 py-1.5 rounded-md text-sm font-medium transition-colors focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-emerald-500 ${
      active
        ? 'bg-white dark:bg-zinc-800 text-slate-900 dark:text-white shadow-sm'
        : 'text-fg-muted hover:text-slate-900 dark:hover:text-white'
    }`

  return (
    <div>
      <div className="flex flex-wrap items-end justify-between gap-3 mb-5">
        <div>
          <h2 className="text-2xl font-bold">{t('importPage.title', 'Import')}</h2>
          <p className="mt-1 text-sm text-fg-muted">
            {t('importPage.subtitle', 'Bring books you already have into Bindery.')}
          </p>
        </div>
        <div role="tablist" aria-label={t('importPage.viewsLabel', 'Import views')} className="inline-flex gap-1 p-1 rounded-lg bg-slate-200/70 dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800">
          {VIEWS.map(v => (
            <button
              key={v}
              ref={el => { tabRefs.current[v] = el }}
              id={`import-tab-${v}`}
              type="button"
              role="tab"
              aria-selected={view === v}
              aria-controls={`import-panel-${v}`}
              tabIndex={view === v ? 0 : -1}
              onClick={() => select(v)}
              onKeyDown={onTabKey}
              className={tabCls(view === v)}
            >
              {v === 'library' ? t('importPage.viewLibrary', 'In your library') : t('importPage.viewFolder', 'From a folder')}
            </button>
          ))}
        </div>
      </div>

      {/* One line under the tab strip saying what each view is for. Users
          arriving with an existing library could not tell which of the two
          to use, or thought the library scan would create books (#1617,
          #2164, Discord "Importing a library of folders and files"). */}
      <p className="mb-4 text-xs text-fg-muted">
        {view === 'folder'
          ? t('importPage.viewFolderHint', 'Files anywhere else on disk. Each one is matched to a book in your catalogue and imported into your library.')
          : t('importPage.viewLibraryHint', 'Files already inside your library folders that the last scan could not place. Nothing moves; you say which book each one is.')}
      </p>

      <div role="tabpanel" id={`import-panel-${view}`} aria-labelledby={`import-tab-${view}`}>
        {view === 'folder' ? (
          <FolderImportView />
        ) : isAdmin ? (
          <AdoptionView />
        ) : (
          <p className="py-12 text-center text-sm text-fg-muted">
            {t('adoption.adminOnly', 'Only an admin can decide what the files in the library are.')}
          </p>
        )}
      </div>
    </div>
  )
}
