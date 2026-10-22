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
import { matchStrength, preselectable } from './adoptionMatch'
import { unitDisplayName } from './adoptionHint'

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

  // Rows on this page whose suggestion already earns a one click Confirm:
  // title within STRONG_MATCH_SCORE and the same author. With two hundred
  // books waiting, confirming them one by one was the slow part of the page.
  const strongItems = filters.state === 'pending'
    ? state.items.filter(i => {
        if (i.state !== 'pending' || state.outcomes[i.id]) return false
        const top = preselectable(i)
        return !!top && matchStrength(i, top) === 'strong'
      })
    : []
  const [confirmingStrong, setConfirmingStrong] = useState(false)
  const [strongResult, setStrongResult] = useState<string | null>(null)
  // The rows as they stand now, for the batch below: while the dialog is open
  // or the batch runs, a row can be decided by hand or reloaded away, and the
  // batch must not adopt it from the list it captured at the click.
  const latest = useRef(state)
  useEffect(() => { latest.current = state })
  const confirmStrong = async () => {
    // Each adopt moves files, so the dialog names every file and the book it
    // goes to, not only a count.
    const batch = strongItems.flatMap(item => {
      const top = preselectable(item)
      return top ? [{ item, book: top.book }] : []
    })
    if (!await confirm({
      title: t('adoption.confirmStrong.title', { count: batch.length, defaultValue: 'Confirm {{count}} strong matches?' }),
      body: (
        <>
          <p>{t('adoption.confirmStrong.body', 'Each book is adopted as its suggestion, the same as pressing Confirm on every row. Every one can be undone from its row or the Adopted list.')}</p>
          {/* tabIndex: a scroll box with no focusable content cannot be
              scrolled from the keyboard otherwise. */}
          <ul tabIndex={0} className="mt-3 max-h-64 overflow-y-auto space-y-1 text-xs rounded focus-visible:outline-2 focus-visible:outline-emerald-500" aria-label={t('adoption.confirmStrong.listLabel', 'Files and the books they go to')}>
            {batch.map(({ item, book }) => (
              <li key={item.id} className="min-w-0 [overflow-wrap:anywhere]">
                <span className="font-mono text-fg-muted">{item.relPath}</span>
                <span aria-hidden="true"> → </span>
                <span className="sr-only">{t('adoption.confirmStrong.goesTo', 'goes to')} </span>
                <span className="font-medium">{book.title}</span>
                <span className="text-fg-muted"> · {book.authorName}</span>
              </li>
            ))}
          </ul>
        </>
      ),
      confirmLabel: t('adoption.confirmStrong.confirm', 'Confirm all'),
    })) return
    setConfirmingStrong(true)
    setStrongResult(null)
    let adopted = 0
    let skipped = 0
    try {
      // One at a time: each adopt moves files on the server, and a burst of
      // them all at once is the "search storm" shape on the import side.
      for (const { item, book } of batch) {
        // The row as it stands now must still be the strong match that was
        // confirmed: same book, and still strong. A reload can settle that
        // book (imported or skipped), raise an author conflict or show the
        // file sits in the other format's folder, and adopting then would
        // add a second copy or move files to a book that was skipped.
        const now = latest.current.items.find(i => i.id === item.id)
        const top = now ? preselectable(now) : undefined
        if (!now || now.state !== 'pending' || latest.current.outcomes[item.id] || top?.book.id !== book.id || matchStrength(now, top) !== 'strong') {
          skipped++
          continue
        }
        // Stop at the first failure: the error is on that row, and the
        // remaining files stay where they are until someone has looked.
        if (!await list.adopt(now, { bookId: book.id }, book)) {
          setStrongResult(t('adoption.confirmStrong.stopped', {
            done: adopted, count: batch.length, name: unitDisplayName(item),
            defaultValue: 'Adopted {{done}} of {{count}}, then stopped: {{name}} could not be adopted. Its row says why.',
          }) + (skipped > 0
            ? ' ' + t('adoption.confirmStrong.stoppedSkipped', { count: skipped, defaultValue: '{{count}} changed since you confirmed and were left alone.' })
            : ''))
          return
        }
        adopted++
      }
      setStrongResult(skipped > 0
        ? t('adoption.confirmStrong.doneSkipped', { count: adopted, skipped, defaultValue: 'Adopted {{count}}. {{skipped}} changed since you confirmed and were left alone.' })
        : t('adoption.confirmStrong.done', { count: adopted, defaultValue: 'Adopted {{count}}.' }))
    } finally {
      setConfirmingStrong(false)
    }
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

          {strongResult && (
            <p role="status" data-testid="strong-result" className="mb-3 text-sm text-slate-700 dark:text-zinc-300">{strongResult}</p>
          )}
          {strongItems.length > 1 && (
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2 rounded-md border border-emerald-300/60 dark:border-emerald-900/60 bg-emerald-50 dark:bg-emerald-950/30 px-3 py-2 text-sm">
              <span className="text-emerald-900 dark:text-emerald-200">
                {t('adoption.confirmStrong.summary', { count: strongItems.length, defaultValue: '{{count}} books on this page have a strong match.' })}
              </span>
              <button type="button" onClick={() => void confirmStrong()} disabled={confirmingStrong} className={`${btn.primary} ${btnSize.sm}`}>
                {t('adoption.confirmStrong.button', { count: strongItems.length, defaultValue: 'Confirm {{count}} strong matches' })}
              </button>
            </div>
          )}

          {state.loadError && (
            <p role="alert" className="mb-3 text-sm text-red-700 dark:text-red-400">{state.loadError}</p>
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
