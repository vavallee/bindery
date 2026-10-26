import { useTranslation } from 'react-i18next'

// Placeholder shapes while a list loads. Pages used to show a bare "Loading…"
// line, which on a slow first load (History was seen sitting on it) gives no
// sense of what is coming or that anything is happening. These keep the
// layout the list will have, so nothing jumps when it arrives.
//
// One live region announces the wait; the shapes themselves are hidden from
// assistive tech, which would otherwise read a page of empty boxes.

const BAR = 'rounded bg-slate-200 dark:bg-zinc-800 motion-safe:animate-pulse'

export function SkeletonRows({ rows = 6, label }: { rows?: number; label?: string }) {
  const { t } = useTranslation()
  return (
    <div role="status" aria-live="polite" className="border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
      <span className="sr-only">{label ?? t('common.loading')}</span>
      <div aria-hidden="true" className="divide-y divide-slate-200 dark:divide-zinc-800">
        {Array.from({ length: rows }, (_, i) => (
          <div key={i} className="flex items-center gap-3 px-3 py-3 bg-slate-100 dark:bg-zinc-900">
            <div className={`${BAR} w-4 h-4`} />
            <div className={`${BAR} w-6 h-9`} />
            <div className={`${BAR} h-3 flex-1 max-w-sm`} />
            <div className={`${BAR} h-3 w-20 hidden sm:block`} />
            <div className={`${BAR} h-3 w-14 hidden md:block`} />
          </div>
        ))}
      </div>
    </div>
  )
}

export function SkeletonCards({ cards = 12, label }: { cards?: number; label?: string }) {
  const { t } = useTranslation()
  return (
    <div role="status" aria-live="polite">
      <span className="sr-only">{label ?? t('common.loading')}</span>
      <div aria-hidden="true" className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-4">
        {Array.from({ length: cards }, (_, i) => (
          <div key={i} className="border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden bg-slate-100 dark:bg-zinc-900">
            <div className={`${BAR} rounded-none aspect-[2/3]`} />
            <div className="p-2 space-y-1.5">
              <div className={`${BAR} h-3 w-4/5`} />
              <div className={`${BAR} h-2.5 w-1/2`} />
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
