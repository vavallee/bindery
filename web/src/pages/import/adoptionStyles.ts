// Shared row geometry for the adoption table, so a book row, a group row and
// an editor row line up and every row has the same height at desktop width.

export const cellCls = 'block md:table-cell px-3 py-1 md:py-2.5 align-middle min-w-0 md:max-w-0'

// The action cell has a fixed width: one primary button and the More menu.
export const actionCellCls = 'block md:table-cell px-3 pb-3 pt-1 md:py-2.5 align-middle md:w-52 md:whitespace-nowrap'

export function rowCls(expanded: boolean, inGroup: boolean): string {
  const surface = expanded
    ? 'bg-emerald-500/5'
    : inGroup
      ? 'bg-white dark:bg-zinc-950/40 hover:bg-slate-100 dark:hover:bg-zinc-900'
      : 'bg-slate-100/50 dark:bg-zinc-900/50 hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'
  return `block md:table-row mb-3 md:mb-0 rounded-lg md:rounded-none border md:border-0 border-slate-200 dark:border-zinc-800 focus:outline-hidden focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-emerald-500 ${inGroup ? 'ml-4 md:ml-0' : ''} ${surface}`
}
