import { useTranslation } from 'react-i18next'
import { dangerLink } from '../../components/buttons'
import { EditorItem, moveWithin, summarise } from './qualitySummary'

// FormatList is one of the two ordered lists in the quality profile editor
// (#2733): numbered from 1 at the top, top is best. Each row can be ticked,
// moved one step up or down inside this list only, or removed; formats the
// list does not hold yet are offered as "+ Add" chips and appended unticked.
// The summary line under the list says what the state means in plain words.
export default function FormatList({
  kind,
  items,
  available,
  onChange,
}: {
  kind: 'ebook' | 'audio'
  items: EditorItem[]
  available: readonly string[]
  onChange: (items: EditorItem[]) => void
}) {
  const { t } = useTranslation()
  const heading = kind === 'ebook' ? t('settings.quality.ebookList') : t('settings.quality.audiobookList')

  const toggle = (quality: string) =>
    onChange(items.map(i => i.quality === quality ? { ...i, allowed: !i.allowed } : i))
  const move = (index: number, direction: -1 | 1) => {
    const next = moveWithin(items, index, direction)
    if (next !== items) onChange(next)
  }
  const remove = (quality: string) => onChange(items.filter(i => i.quality !== quality))
  const add = (quality: string) => onChange([...items, { quality, allowed: false }])

  const present = new Set(items.map(i => i.quality))
  const missing = available.filter(f => !present.has(f))

  return (
    <div>
      <div className="flex items-baseline justify-between mb-1">
        <p className="text-xs font-medium text-slate-700 dark:text-zinc-300">{heading}</p>
        {items.length > 1 && (
          <p className="text-[10px] text-slate-500 dark:text-zinc-600">{t('settings.quality.bestFirst')}</p>
        )}
      </div>
      {items.length > 0 && (
        <ol aria-label={heading} className="space-y-1.5">
          {items.map((item, i) => (
            <li
              key={item.quality}
              className="flex items-center gap-2 px-3 py-1.5 rounded border border-slate-200 dark:border-zinc-800 bg-slate-100 dark:bg-zinc-900"
            >
              <span className="text-[10px] w-4 text-right text-slate-500 dark:text-zinc-500">{i + 1}.</span>
              <label className="flex items-center gap-2 cursor-pointer text-xs flex-1 min-w-0">
                <input
                  type="checkbox"
                  checked={item.allowed}
                  onChange={() => toggle(item.quality)}
                  className="rounded border-slate-300 dark:border-zinc-700 text-emerald-600 focus:ring-emerald-500"
                />
                <span className={item.allowed ? 'text-slate-800 dark:text-zinc-200' : 'text-slate-500 dark:text-zinc-500'}>
                  {item.quality}
                </span>
              </label>
              {kind === 'audio' && (
                <span className="text-[10px] px-1.5 py-0.5 rounded bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300">
                  {t('common.audiobook')}
                </span>
              )}
              <button
                type="button"
                onClick={() => move(i, -1)}
                disabled={i === 0}
                aria-label={t('settings.quality.moveUp')}
                className="text-xs px-1.5 py-0.5 text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white disabled:opacity-30"
              >
                {'↑'}
              </button>
              <button
                type="button"
                onClick={() => move(i, 1)}
                disabled={i === items.length - 1}
                aria-label={t('settings.quality.moveDown')}
                className="text-xs px-1.5 py-0.5 text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white disabled:opacity-30"
              >
                {'↓'}
              </button>
              <button
                type="button"
                onClick={() => remove(item.quality)}
                aria-label={t('common.remove')}
                className={`text-xs px-1.5 py-0.5 ${dangerLink}`}
              >
                {'×'}
              </button>
            </li>
          ))}
        </ol>
      )}
      <SummaryLine kind={kind} items={items} />
      {missing.length > 0 && (
        <div className="flex flex-wrap gap-1.5 mt-2">
          <span className="text-[11px] text-slate-500 dark:text-zinc-500 self-center">
            {t('settings.quality.formAddFormat')}
          </span>
          {missing.map(f => (
            <button
              type="button"
              key={f}
              onClick={() => add(f)}
              className="text-[11px] px-2 py-0.5 rounded border border-slate-300 dark:border-zinc-700 bg-slate-200 dark:bg-zinc-800 text-slate-700 dark:text-zinc-300 hover:border-slate-400 dark:hover:border-zinc-600"
            >
              + {f}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

function SummaryLine({ kind, items }: { kind: 'ebook' | 'audio'; items: EditorItem[] }) {
  const { t } = useTranslation()
  const summary = summarise(items)
  const cls = 'text-[11px] text-slate-600 dark:text-zinc-400 mt-1.5'
  switch (summary.kind) {
    case 'noOpinion':
      return (
        <p className={cls}>
          {kind === 'ebook' ? t('settings.quality.summaryNoOpinionEbook') : t('settings.quality.summaryNoOpinionAudiobook')}
        </p>
      )
    case 'noneAllowed':
      return (
        <p className={cls}>
          {kind === 'ebook' ? t('settings.quality.summaryNoneAllowedEbook') : t('settings.quality.summaryNoneAllowedAudiobook')}
        </p>
      )
    case 'prefer':
      return (
        <p className={cls}>
          <span>
            {summary.rest.length === 0
              ? t('settings.quality.summaryPrefer', { first: summary.first })
              : t('settings.quality.summaryPreferThen', { first: summary.first, rest: summary.rest.join(', ') })}
          </span>
          {summary.never.length > 0 && (
            <>
              {' '}
              <span>{t('settings.quality.summaryNever', { formats: summary.never.join(', ') })}</span>
            </>
          )}
        </p>
      )
  }
}
