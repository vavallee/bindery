import { useTranslation } from 'react-i18next'
import type { AdoptionScanStatus, AdoptionSummary as Summary } from '../../api/client'
import { btn, btnSize } from '../../components/buttons'
import { relativeTime } from './adoptionHint'

interface Props {
  summary: Summary | null
  scan: AdoptionScanStatus | null
  onScan: () => void
  // Set after an author is added: the next useful step is a scan.
  nudgeScan: boolean
}

// The strip above the list. One number matters, the books waiting for a
// decision; files, the last scan, ignored and adopted are quieter stats beside
// it. Scan now is the one control.
export default function AdoptionSummary({ summary, scan, onScan, nudgeScan }: Props) {
  const { t, i18n } = useTranslation()
  const running = Boolean(scan?.running)
  const pending = summary?.pending ?? 0
  const recent = scan?.ranAt ? Date.now() - Date.parse(scan.ranAt) < 60_000 : false
  const ranAt = !scan?.ran ? '' : recent ? t('adoption.summary.justNow', 'just now') : relativeTime(scan.ranAt, i18n.language)

  const stat = (key: string, value: React.ReactNode, label: string, labelFirst = false) => (
    <li key={key} className={`flex items-baseline gap-1.5 whitespace-nowrap ${labelFirst ? 'flex-row-reverse' : ''}`}>
      <span className="text-sm tabular-nums text-slate-700 dark:text-zinc-300">{value}</span>
      <span className="text-xs text-fg-muted">{label}</span>
    </li>
  )

  return (
    <section
      aria-label={t('adoption.summary.label', 'Library scan summary')}
      className="mb-5 rounded-lg border border-slate-200 dark:border-zinc-800 bg-slate-100 dark:bg-zinc-900 px-4 py-3"
    >
      <div className="flex flex-wrap items-center gap-x-8 gap-y-3">
        <p className="flex items-baseline gap-2" aria-live="polite">
          {running ? (
            <span className="flex items-center gap-2 text-sm font-medium text-slate-900 dark:text-white">
              <span aria-hidden="true" className="relative flex h-2 w-2">
                <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
                <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-500" />
              </span>
              {t('adoption.summary.scanning', 'Scanning your library…')}
            </span>
          ) : (
            <>
              <span className="text-2xl font-semibold tabular-nums text-slate-900 dark:text-white">{pending}</span>
              <span className="text-sm text-slate-700 dark:text-zinc-300">
                {t('adoption.summary.needDecision', { count: pending, defaultValue: 'books need a decision' })}
              </span>
            </>
          )}
        </p>
        <ul className="flex flex-1 flex-wrap items-baseline gap-x-5 gap-y-1">
          {summary && summary.pendingFiles > 0 && stat('files', summary.pendingFiles, t('adoption.summary.files', { count: summary.pendingFiles, defaultValue: 'files' }))}
          {stat('scan', ranAt || t('adoption.summary.never', 'never'), t('adoption.summary.lastScanLabel', 'last scan'), true)}
          {summary && summary.adopted > 0 && stat('adopted', summary.adopted, t('adoption.summary.adoptedLabel', 'adopted'))}
          {summary && summary.ignored > 0 && stat('ignored', summary.ignored, t('adoption.summary.ignoredLabel', 'ignored'))}
        </ul>
        <div className="flex items-center gap-3">
          {nudgeScan && !running && (
            <p className="text-xs text-accent-text">{t('adoption.summary.nudge', 'Author added. Scan now to match their files.')}</p>
          )}
          <button type="button" onClick={onScan} disabled={running} className={`${nudgeScan ? btn.primary : btn.secondary} ${btnSize.md}`}>
            {running ? t('adoption.summary.scanningButton', 'Scanning…') : t('adoption.summary.scanNow', 'Scan now')}
          </button>
        </div>
      </div>
      {scan?.error && (
        <p role="alert" className="mt-2 text-xs text-amber-700 dark:text-amber-400">
          {t('adoption.summary.scanError', { error: scan.error, defaultValue: 'The last scan did not finish: {{error}}' })}
        </p>
      )}
      {!scan?.error && scan?.ran && scan.noFilesFound && (
        <p className="mt-2 text-xs text-amber-700 dark:text-amber-400">
          {t('adoption.summary.noFiles', 'The last scan found no book files. Check that the library folder is mounted, then scan again. Your decisions were kept.')}
        </p>
      )}
      {scan?.truncated && (
        <p className="mt-2 text-xs text-amber-700 dark:text-amber-400">
          {t('adoption.summary.truncated', 'This library is larger than one scan lists. Adopt or ignore some books and scan again to see the rest.')}
        </p>
      )}
    </section>
  )
}
