// 'az' | 'za' sort by name and back the existing toolbar buttons; the rest are
// the column-header sorts (#1349), whitelisted server-side by authorSortOrder.
export type SortMode =
  | 'az' | 'za'
  | 'name-az' | 'name-za'
  | 'recent'
  | 'books-asc' | 'books-desc'
  | 'rating-asc' | 'rating-desc'
  | 'monitored-asc' | 'monitored-desc'

export const SORT_MODES: readonly SortMode[] = [
  'az', 'za', 'name-az', 'name-za', 'recent', 'books-asc', 'books-desc',
  'rating-asc', 'rating-desc', 'monitored-asc', 'monitored-desc',
]
