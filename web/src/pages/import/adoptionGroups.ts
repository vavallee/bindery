import type { AdoptionItem } from '../../api/client'

// When several books on the page failed for the same reason, the author is
// not in the library and they share an author folder, that is one decision,
// not one per book. They are shown as a single group row with one Add author;
// the books stay reachable inside it.

export interface AdoptionGroup {
  kind: 'group'
  key: string
  folder: string
  author: string
  items: AdoptionItem[]
  files: number
}

export interface AdoptionEntry {
  kind: 'item'
  item: AdoptionItem
  // Set when the row sits inside a group.
  groupKey?: string
}

export type AdoptionListEntry = AdoptionGroup | AdoptionEntry

const MIN_GROUP = 2

// A row whose files name another author than its folder is not part of the
// folder's decision: adding the folder's author would not match it (#2942).
function groupable(item: AdoptionItem): boolean {
  return item.state === 'pending' && item.reason === 'author_not_in_library' && item.authorFolder !== '' &&
    item.candidates.length === 0 && !item.authorConflict
}

// groupEntries keeps the page order: a group takes the place of its first book.
export function groupEntries(items: AdoptionItem[]): AdoptionListEntry[] {
  const byFolder = new Map<string, AdoptionItem[]>()
  for (const item of items) {
    if (!groupable(item)) continue
    const list = byFolder.get(item.authorFolder) ?? []
    list.push(item)
    byFolder.set(item.authorFolder, list)
  }
  const out: AdoptionListEntry[] = []
  const placed = new Set<string>()
  for (const item of items) {
    const members = groupable(item) ? byFolder.get(item.authorFolder) : undefined
    if (!members || members.length < MIN_GROUP) {
      out.push({ kind: 'item', item })
      continue
    }
    if (placed.has(item.authorFolder)) continue
    placed.add(item.authorFolder)
    const authorCounts = new Map<string, number>()
    for (const m of members) if (m.parsedAuthor) authorCounts.set(m.parsedAuthor, (authorCounts.get(m.parsedAuthor) ?? 0) + 1)
    const author = [...authorCounts.entries()].sort((a, b) => b[1] - a[1])[0]?.[0] ?? item.authorFolder
    out.push({
      kind: 'group',
      key: `group:${item.authorFolder}`,
      folder: item.authorFolder,
      author,
      items: members,
      files: members.reduce((n, m) => n + m.fileCount, 0),
    })
  }
  return out
}

// visibleEntries flattens groups for rendering and keyboard order: a group
// row, then its books when it is open.
export function visibleEntries(entries: AdoptionListEntry[], open: ReadonlySet<string>): AdoptionListEntry[] {
  const out: AdoptionListEntry[] = []
  for (const e of entries) {
    out.push(e)
    if (e.kind === 'group' && open.has(e.key)) {
      for (const item of e.items) out.push({ kind: 'item', item, groupKey: e.key })
    }
  }
  return out
}
