import { Fragment, useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import type { AdoptionItem, AdoptionSort } from '../../api/client'
import SortHeader from '../../components/SortHeader'
import AdoptionEditor from './AdoptionEditor'
import AdoptionGroupRow from './AdoptionGroupRow'
import AdoptionRow from './AdoptionRow'
import { groupEntries, visibleEntries, type AdoptionListEntry } from './adoptionGroups'
import { matchStrength, preselectable } from './adoptionMatch'
import type { AdoptionList } from './useAdoptionList'

interface Props {
  list: AdoptionList
  onSearchShortcut: () => void
  onAddAuthor: (name: string) => void
  onIgnoreFolder: (folder: string) => void
}

// Natural direction per column: scores, file counts and sizes read biggest
// first, names read A to Z.
const NATURAL_DESC: Record<AdoptionSort, boolean> = { score: true, files: true, size: true, seen: true, title: false, folder: false }

const entryKey = (e: AdoptionListEntry) => (e.kind === 'group' ? e.key : `item:${e.item.id}`)

// AdoptionTable is the list: a real table on wide screens, stacked cards on
// narrow ones. Books sharing a missing author are one group row. One row holds
// the tab stop (roving tabindex); arrows move it, Enter opens a book's editor
// in place or a group's books, Esc closes the editor and returns focus to its
// row, i ignores, u undoes, and / jumps to the search box.
export default function AdoptionTable({ list, onSearchShortcut, onAddAuthor, onIgnoreFolder }: Props) {
  const { t } = useTranslation()
  const { state, filters } = list
  const [focusIndex, setFocusIndex] = useState(0)
  const [openGroups, setOpenGroups] = useState<Set<string>>(() => new Set())
  const [filesOpen, setFilesOpen] = useState(false)
  const rowRefs = useRef<(HTMLTableRowElement | null)[]>([])
  const restoreFocusTo = useRef<string | null>(null)
  const entries = useMemo(() => visibleEntries(groupEntries(state.items), openGroups), [state.items, openGroups])
  const direction: 'asc' | 'desc' = filters.dir || (NATURAL_DESC[filters.sort] ? 'desc' : 'asc')

  useEffect(() => { setFocusIndex(i => Math.min(i, Math.max(0, entries.length - 1))) }, [entries.length])

  // After the editor closes, focus goes back to the row it belonged to.
  useEffect(() => {
    if (state.expandedId === null && restoreFocusTo.current !== null) {
      const index = entries.findIndex(e => entryKey(e) === restoreFocusTo.current)
      rowRefs.current[index]?.focus()
      restoreFocusTo.current = null
    }
  }, [state.expandedId, entries])

  const sortBy = (key: AdoptionSort) => {
    if (key === filters.sort) list.setFilter({ dir: direction === 'asc' ? 'desc' : 'asc' })
    else list.setFilter({ sort: key, dir: '' })
  }
  const toggleGroup = (key: string) => setOpenGroups(prev => {
    const next = new Set(prev)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    return next
  })
  const openEditor = (item: AdoptionItem, showFiles: boolean) => {
    setFilesOpen(showFiles)
    list.expand(item.id)
  }
  const closeEditor = (item: AdoptionItem) => {
    restoreFocusTo.current = `item:${item.id}`
    list.collapse()
  }
  const confirm = (item: AdoptionItem) => {
    const top = preselectable(item)
    if (top && matchStrength(item, top) === 'strong') void list.adopt(item, { bookId: top.book.id }, top.book)
  }

  const onRowKey = (e: KeyboardEvent<HTMLTableRowElement>, index: number, entry: AdoptionListEntry) => {
    if (e.target !== e.currentTarget) return
    const move = (to: number) => {
      e.preventDefault()
      const next = Math.max(0, Math.min(entries.length - 1, to))
      setFocusIndex(next)
      rowRefs.current[next]?.focus()
    }
    if (e.key === 'ArrowDown') return move(index + 1)
    if (e.key === 'ArrowUp') return move(index - 1)
    if (e.key === 'Home') return move(0)
    if (e.key === 'End') return move(entries.length - 1)
    if (e.key === '/') { e.preventDefault(); onSearchShortcut(); return }
    if (entry.kind === 'group') {
      if (e.key === 'Enter') { e.preventDefault(); toggleGroup(entry.key) }
      return
    }
    const { item } = entry
    const outcome = state.outcomes[item.id]
    if (e.key === 'Enter') {
      e.preventDefault()
      if (outcome || item.state !== 'pending') return
      if (state.expandedId === item.id) closeEditor(item)
      else openEditor(item, false)
    } else if (e.key === 'Escape' && state.expandedId === item.id) {
      closeEditor(item)
    } else if (e.key === 'i' && !outcome && item.state === 'pending') {
      e.preventDefault()
      void list.ignore(item)
    } else if (e.key === 'u' && (outcome?.kind === 'adopted' || outcome?.kind === 'ignored' || (!outcome && item.state !== 'pending'))) {
      e.preventDefault()
      void list.undo(item)
    }
  }

  const captionKey = `adoption.caption.${filters.state}`
  const captionDefault = filters.state === 'ignored' ? 'Ignored books in your library' : filters.state === 'adopted' ? 'Books you adopted from your library' : 'Books in your library that need a decision'

  return (
    <div className="md:border md:border-slate-200 md:dark:border-zinc-800 md:rounded-lg">
      <table className="block md:table w-full text-sm">
        <caption className="sr-only">
          {t(captionKey, captionDefault)}. {t('adoption.caption.keys', 'Arrow keys move between books, Enter opens one, i ignores, u undoes.')}
        </caption>
        <thead className="hidden md:table-header-group">
          <tr className="bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
            <SortHeader label={t('adoption.col.book', 'Book')} sortKey="title" activeKey={filters.sort} direction={direction} onSort={sortBy} className="rounded-tl-lg" />
            <SortHeader label={t('adoption.col.match', 'Best match')} sortKey="score" activeKey={filters.sort} direction={direction} onSort={sortBy} />
            <th scope="col" className="rounded-tr-lg px-3 py-2"><span className="sr-only">{t('adoption.col.actions', 'Actions')}</span></th>
          </tr>
        </thead>
        <tbody className="block md:table-row-group md:divide-y md:divide-slate-200 md:dark:divide-zinc-800">
          {entries.map((entry, index) => {
            const common = {
              ref: (el: HTMLTableRowElement | null) => { rowRefs.current[index] = el },
              focusable: index === focusIndex,
              onFocusRow: () => setFocusIndex(index),
              onKeyDown: (e: KeyboardEvent<HTMLTableRowElement>) => onRowKey(e, index, entry),
            }
            if (entry.kind === 'group') {
              return (
                <AdoptionGroupRow key={entry.key} {...common} group={entry} open={openGroups.has(entry.key)}
                  onToggle={() => toggleGroup(entry.key)} onAddAuthor={onAddAuthor} onIgnoreFolder={onIgnoreFolder} />
              )
            }
            const { item } = entry
            const expanded = state.expandedId === item.id
            const editorId = `adoption-editor-${item.id}`
            return (
              <Fragment key={entryKey(entry) + (entry.groupKey ?? '')}>
                <AdoptionRow {...common} item={item} outcome={state.outcomes[item.id]} error={state.errors[item.id]} note={state.notes[item.id]}
                  expanded={expanded} inGroup={Boolean(entry.groupKey)} editorId={editorId}
                  onOpen={showFiles => (expanded && !showFiles ? closeEditor(item) : openEditor(item, showFiles))}
                  onConfirm={() => confirm(item)} onIgnore={() => void list.ignore(item)} onUndo={() => void list.undo(item)}
                  onAddAuthor={onAddAuthor} />
                {expanded && (
                  <tr className="block md:table-row -mt-3 mb-3 md:m-0 rounded-b-lg md:rounded-none border md:border-0 border-t-0 border-slate-200 dark:border-zinc-800 bg-emerald-500/5">
                    <td id={editorId} colSpan={3} className="block md:table-cell px-3 pb-4 pt-1 md:px-6">
                      <AdoptionEditor item={item} showFiles={filesOpen}
                        onAdopt={(target, preview) => list.adopt(item, target, preview)} onCancel={() => closeEditor(item)} />
                    </td>
                  </tr>
                )}
              </Fragment>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
