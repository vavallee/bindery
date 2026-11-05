import { useTranslation } from 'react-i18next'

// ReleaseNarration shows what a release title states about the recording
// (#1358): its edition type and narrators. Renders nothing when the title says
// neither, so unknown stays invisible rather than guessed.
export default function ReleaseNarration({
  narrators,
  editionType,
}: {
  narrators?: string[]
  editionType?: string
}) {
  const { t } = useTranslation()
  const names = narrators ?? []
  if (!editionType && names.length === 0) return null
  return (
    <>
      {editionType && (
        <span className="px-1.5 py-0.5 rounded text-[10px] font-medium bg-slate-200 text-slate-700 dark:bg-zinc-700 dark:text-zinc-200">
          {t(`editionType.${editionType}`, { defaultValue: editionType })}
        </span>
      )}
      {names.length > 0 && (
        <span className="text-fg-muted text-[11px]">
          {t('bookDetail.narratedBy', { narrator: names.join(', ') })}
        </span>
      )}
    </>
  )
}
