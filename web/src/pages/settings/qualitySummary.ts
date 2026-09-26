// Pure helpers behind the quality profile editor (#2733). A profile is two
// ordered lists, ebook formats and audiobook formats, top is best, and only
// ticked entries take part. Nothing here touches React or i18n so the rules
// can be tested on their own.

export interface EditorItem {
  quality: string
  allowed: boolean
}

export interface Partition {
  ebook: EditorItem[]
  audio: EditorItem[]
  // Tokens neither list knows. They are carried through untouched and re
  // appended on submit so an edit never drops what a third party client
  // stored; the server treats them as inert.
  other: EditorItem[]
}

// partitionItems splits the stored list into its per media type lists,
// preserving stored order inside each.
export function partitionItems(
  items: EditorItem[],
  ebookFormats: readonly string[],
  audioFormats: readonly string[],
): Partition {
  const out: Partition = { ebook: [], audio: [], other: [] }
  for (const item of items) {
    if (ebookFormats.includes(item.quality)) out.ebook.push(item)
    else if (audioFormats.includes(item.quality)) out.audio.push(item)
    else out.other.push(item)
  }
  return out
}

export type Summary =
  // Nothing listed: any format of this kind is accepted, ranked by the
  // built in order. There is nothing to name, so this variant carries no
  // formats.
  | { kind: 'noOpinion' }
  // Listed but nothing ticked: nothing of this kind will be grabbed, which
  // the line says on its own without listing every refused format.
  | { kind: 'noneAllowed' }
  // The ticked formats in order, and the unticked ones.
  | { kind: 'prefer'; first: string; rest: string[]; never: string[] }

// summarise describes one list the way the editor's summary line reads it.
export function summarise(items: EditorItem[]): Summary {
  if (items.length === 0) return { kind: 'noOpinion' }
  const ticked = items.filter(i => i.allowed).map(i => i.quality)
  if (ticked.length === 0) return { kind: 'noneAllowed' }
  return {
    kind: 'prefer',
    first: ticked[0],
    rest: ticked.slice(1),
    never: items.filter(i => !i.allowed).map(i => i.quality),
  }
}

// moveWithin swaps the entry at index with its neighbour in direction. At
// either end it returns the same array, so callers can skip a state update.
export function moveWithin<T>(list: T[], index: number, direction: -1 | 1): T[] {
  const target = index + direction
  if (target < 0 || target >= list.length) return list
  const next = [...list]
  ;[next[index], next[target]] = [next[target], next[index]]
  return next
}
