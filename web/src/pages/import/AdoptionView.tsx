import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import AddToLibraryModal from '../../components/AddToLibraryModal'
import { btn, btnSize } from '../../components/buttons'
import Pagination from '../../components/Pagination'
import { useConfirmDialog } from '../../components/useConfirmDialog'
import AdoptionFacets from './AdoptionFacets'
import AdoptionRail from './AdoptionRail'
import AdoptionSummary from './AdoptionSummary'
import AdoptionTable from './AdoptionTable'
import { defaultAdoptionFilters, useAdoptionList } from './useAdoptionList'

// AdoptionView is the Import page's "In your library" view. It lays out the
// summary strip, the folder rail, the toolbar and the list, and chooses which
// designed state to show when there is no list: never scanned, all matched,
// nothing ignored or adopted yet, or nothing matching the filters. The data
// and every decision live in useAdoptionList.
export default function AdoptionView() {
  const { t } = useTranslation()
  const list = useAdoptionList()
  const { state, filters } = list
  const { confirm, confirmDialog } = useConfirmDialog()
  const searchRef = useRef<HTMLInputElement>(null)
  const [addAuthor, setAddAuthor] = useState<string | null>(null)
  const [nudgeScan, setNudgeScan] = useState(false)

  // "/" jumps to the search from anywhere in the view but a text field.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null
      if (e.key !== '/' || e.defaultPrevented || el?.closest('input, textarea, select, [contenteditable="true"]')) return
      e.preventDefault()
      searchRef.current?.focus()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [])

  const scan = () => {
    setNudgeScan(false)
    void list.startScan()
  }

  const ignoreFolder = async (folder: string) => {
    if (!await confirm({
      title: t('adoption.ignoreFolder.title', { folder, defaultValue: 'Ignore every book in {{folder}}?' }),
      body: t('adoption.ignoreFolder.body', 'They leave this list and later scans keep them out. You can unignore any of them from the Ignored list.'),
      confirmLabel: t('adoption.ignoreFolder.confirm', 'Ignore folder'),
    })) return
    await list.ignoreFolder(folder)
  }

  const folders = filters.state === 'pending' ? state.facets?.folders ?? [] : []
  const filtered = filters.search !== '' || filters.reason !== '' || filters.format !== '' || filters.authorFolder !== ''
  const empty = state.loaded && !state.loading && state.items.length === 0 && !state.loadError

  const emptyState = () => {
    const box = (title: string, body: string, action?: React.ReactNode) => (
      <div className="rounded-lg border border-dashed border-slate-300 dark:border-zinc-700 px-6 py-14 text-center">
        <p className="font-medium text-slate-800 dark:text-zinc-200">{title}</p>
        <p className="mx-auto mt-1 max-w-md text-sm text-fg-muted">{body}</p>
        {action && <div className="mt-4">{action}</div>}
      </div>
    )
    if (filtered) {
      return box(
        t('adoption.empty.filteredTitle', 'No books match these filters'),
        t('adoption.empty.filteredBody', 'Try another search, or clear the filters to see every book.'),
        <button type="button" onClick={() => list.setFilter({ ...defaultAdoptionFilters, state: filters.state })} className={`${btn.secondary} ${btnSize.md}`}>
          {t('adoption.empty.clear', 'Clear filters')}
        </button>,
      )
    }
    if (filters.state === 'ignored') return box(t('adoption.empty.ignoredTitle', 'Nothing is ignored'), t('adoption.empty.ignoredBody', 'Books you ignore land here, and you can bring any of them back.'))
    if (filters.state === 'adopted') return box(t('adoption.empty.adoptedTitle', 'Nothing adopted yet'), t('adoption.empty.adoptedBody', 'Books you adopt stay here for 30 days, with Undo.'))
    if (!state.scan?.ran) {
      return box(
        t('adoption.empty.neverTitle', 'Your library has not been scanned yet'),
        t('adoption.empty.neverBody', 'A scan looks through your library folders and lists every book it cannot match to your library, so you can decide what each one is. Files are never moved.'),
        <button type="button" onClick={scan} disabled={state.scan?.running} className={`${btn.primary} ${btnSize.md}`}>
          {t('adoption.empty.scanLibrary', 'Scan library')}
        </button>,
      )
    }
    return box(
      t('adoption.empty.doneTitle', 'Every book in your library is matched'),
      t('adoption.empty.doneBody', 'Nothing needs a decision. New files show up here after the next scan.'),
    )
  }

  return (
    <div>
      {confirmDialog}
      <AdoptionSummary summary={state.summary} scan={state.scan} onScan={scan} nudgeScan={nudgeScan} />

      <div className={folders.length > 0 ? 'lg:grid lg:grid-cols-[15rem_minmax(0,1fr)] lg:gap-6' : ''}>
        <AdoptionRail
          folders={folders}
          total={state.summary?.pending ?? 0}
          activeFolder={filters.authorFolder}
          onShow={folder => list.setFilter({ authorFolder: folder })}
        />
        <div className="min-w-0">
          <AdoptionFacets ref={searchRef} filters={filters} facets={state.facets} summary={state.summary} onChange={list.setFilter} />

          {state.loadError && (
            <p role="alert" className="mb-3 text-sm text-red-600 dark:text-red-400">{state.loadError}</p>
          )}
          {!state.loaded ? (
            <div className="py-10 text-center text-sm text-fg-muted">{t('common.loading', 'Loading…')}</div>
          ) : empty ? (
            emptyState()
          ) : (
            <div className={state.loading ? 'opacity-60 transition-opacity' : 'transition-opacity'} aria-busy={state.loading}>
              <AdoptionTable list={list} onSearchShortcut={() => searchRef.current?.focus()} onAddAuthor={setAddAuthor} onIgnoreFolder={folder => void ignoreFolder(folder)} />
            </div>
          )}
          <Pagination {...list.paginationProps} />
        </div>
      </div>

      {addAuthor !== null && (
        <AddToLibraryModal
          mode="author"
          initialQuery={addAuthor}
          onClose={() => setAddAuthor(null)}
          onAdded={() => { setAddAuthor(null); setNudgeScan(true) }}
        />
      )}
    </div>
  )
}
