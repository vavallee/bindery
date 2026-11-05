import { useTranslation } from 'react-i18next'

// PackLabel marks a release that looks like several books (#3247), with the
// words it matched in the tooltip. Rendered by every interactive search
// result list, so a pack reads the same wherever it can be grabbed.
export default function PackLabel({ marker }: { marker?: string }) {
  const { t } = useTranslation()
  if (!marker) return null
  return (
    <span
      className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300"
      title={t('search.pack.matched', { marker })}
    >
      {t('search.pack.label')}
    </span>
  )
}
