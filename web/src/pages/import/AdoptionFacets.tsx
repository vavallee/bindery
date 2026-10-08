import { forwardRef } from 'react'
import { useTranslation } from 'react-i18next'
import type { AdoptionFacets as Facets, AdoptionState, AdoptionSummary } from '../../api/client'
import FilterPopover, { FilterGroup } from '../../components/FilterPopover'
import type { AdoptionFilters } from './useAdoptionList'

interface Props {
  filters: AdoptionFilters
  facets: Facets | null
  summary: AdoptionSummary | null
  onChange: (patch: Partial<AdoptionFilters>) => void
}

const STATES: AdoptionState[] = ['pending', 'ignored', 'adopted']

// The toolbar over the list: search, which decisions to look at, and the
// reason and format facets behind one disclosure. An applied filter is always
// visible as a removable chip, the same rule BooksPage follows.
const AdoptionFacets = forwardRef<HTMLInputElement, Props>(function AdoptionFacets({ filters, facets, summary, onChange }, searchRef) {
  const { t } = useTranslation()

  const stateLabel = (s: AdoptionState) => {
    const count = s === 'pending' ? summary?.pending : s === 'ignored' ? summary?.ignored : summary?.adopted
    const label = t(`adoption.state.${s}`, s === 'pending' ? 'Needs a decision' : s === 'ignored' ? 'Ignored' : 'Adopted')
    return count ? `${label} (${count})` : label
  }
  const reasonLabel = (code: string) => t(`adoption.reason.${code}`, code)
  const formatLabel = (f: string) => (f === 'audiobook' ? t('common.audiobook', 'Audiobook') : t('common.ebook', 'Ebook'))
  const pill = (active: boolean) =>
    `touch-target px-3 py-1 rounded-md text-xs font-medium transition-colors ${active ? 'bg-slate-300 dark:bg-zinc-700 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white'}`
  const chip = 'inline-flex items-center gap-1 px-2 py-1 rounded-full bg-slate-200 dark:bg-zinc-800 text-xs text-slate-700 dark:text-zinc-300 hover:bg-slate-300 dark:hover:bg-zinc-700'

  return (
    <div className="mb-3 space-y-2">
      <div className="flex flex-col sm:flex-row gap-3">
        <input
          enterKeyHint="search"
          ref={searchRef}
          type="search"
          value={filters.search}
          onChange={e => onChange({ search: e.target.value })}
          placeholder={t('adoption.searchPlaceholder', 'Search titles, authors and folders')}
          aria-label={t('adoption.searchLabel', 'Search books to decide')}
          aria-keyshortcuts="/"
          className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-slate-400 dark:focus:border-zinc-600 placeholder-slate-400 dark:placeholder-zinc-600"
        />
        <div role="radiogroup" aria-label={t('adoption.stateLabel', 'Show')} className="flex gap-1 pointer-coarse:gap-y-5 flex-wrap items-center">
          {STATES.map(s => (
            <button
              key={s}
              type="button"
              role="radio"
              aria-checked={filters.state === s}
              onClick={() => onChange({ state: s })}
              className={pill(filters.state === s)}
            >
              {stateLabel(s)}
            </button>
          ))}
        </div>
      </div>
      <div className="flex items-center gap-2 flex-wrap">
        <FilterPopover label={t('common.filters', 'Filters')} activeCount={[filters.reason, filters.format].filter(Boolean).length}>
          <FilterGroup
            label={t('adoption.filterReason', 'Why it did not match')}
            value={filters.reason}
            onChange={reason => onChange({ reason })}
            options={[
              { value: '', label: t('common.all', 'All') },
              ...(facets?.reasons ?? []).map(r => ({ value: r.value, label: `${reasonLabel(r.value)} (${r.count})` })),
            ]}
          />
          <FilterGroup
            label={t('adoption.filterFormat', 'Format')}
            value={filters.format}
            onChange={format => onChange({ format })}
            options={[
              { value: '', label: t('common.all', 'All') },
              ...(facets?.formats ?? []).map(f => ({ value: f.value, label: `${formatLabel(f.value)} (${f.count})` })),
            ]}
          />
        </FilterPopover>
        {filters.authorFolder && (
          <button type="button" onClick={() => onChange({ authorFolder: '' })} className={chip}>
            {t('adoption.chipFolder', { folder: filters.authorFolder, defaultValue: 'Folder: {{folder}}' })}
            <span aria-hidden="true">✕</span>
            <span className="sr-only">{t('common.clearFilter', 'Clear filter')}</span>
          </button>
        )}
        {filters.reason && (
          <button type="button" onClick={() => onChange({ reason: '' })} className={chip}>
            {reasonLabel(filters.reason)}
            <span aria-hidden="true">✕</span>
            <span className="sr-only">{t('common.clearFilter', 'Clear filter')}</span>
          </button>
        )}
        {filters.format && (
          <button type="button" onClick={() => onChange({ format: '' })} className={chip}>
            {formatLabel(filters.format)}
            <span aria-hidden="true">✕</span>
            <span className="sr-only">{t('common.clearFilter', 'Clear filter')}</span>
          </button>
        )}
      </div>
    </div>
  )
})

export default AdoptionFacets
