import { ReactNode, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, SystemStatus } from '../../api/client'

// Where the in-app "check Settings → About" guidance (update message,
// bug_report.yml pre-flight) lands. Surfaces the build identity already exposed
// by /system/status — the same source the top-right version link reads — so a
// user filing a bug can copy an exact version/commit instead of guessing from
// the header sha.

// isRelease mirrors App.tsx's header heuristic: a tagged build reports a
// semver-ish version (e.g. "1.22.1"); an untagged build reports a sha
// ("sha-9ecd99e"). Tagged builds get a "vX.Y.Z" label and a tag-specific
// release link; everything else falls back to the releases index.
function isRelease(version: string): boolean {
  return /^\d+\.\d+/.test(version)
}

function ReleaseLink({ version }: { version: string }) {
  const href = isRelease(version)
    ? `https://github.com/vavallee/bindery/releases/tag/v${version}`
    : 'https://github.com/vavallee/bindery/releases'
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className="text-sm text-blue-600 dark:text-blue-400 hover:underline"
    >
      {isRelease(version) ? `v${version}` : version}
    </a>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 py-2 border-t border-slate-200 dark:border-zinc-800 first:border-t-0 first:pt-0">
      <span className="text-sm font-medium text-slate-800 dark:text-zinc-200">{label}</span>
      <span className="text-sm text-slate-600 dark:text-zinc-400 text-right break-all">{children}</span>
    </div>
  )
}

export default function AboutTab() {
  const { t } = useTranslation()
  const [status, setStatus] = useState<SystemStatus | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    api.status().then(setStatus).catch(() => setFailed(true))
  }, [])

  return (
    <div className="space-y-8">
      <section>
        <h3 className="text-base font-semibold mb-3 text-slate-800 dark:text-zinc-200">
          {t('settings.about.title', 'About')}
        </h3>
        <p className="text-xs text-slate-600 dark:text-zinc-500 mb-3">
          {t('settings.about.hint', 'Build details for this Bindery instance. Include the version and commit when filing a bug report.')}
        </p>
        <div className="p-4 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900">
          {failed ? (
            <p className="text-sm text-slate-600 dark:text-zinc-400">
              {t('settings.about.loadError', 'Could not load build details.')}
            </p>
          ) : !status ? (
            <p className="text-sm text-slate-600 dark:text-zinc-400">
              {t('common.loading', 'Loading…')}
            </p>
          ) : (
            <>
              <Row label={t('settings.about.version', 'Version')}>
                <ReleaseLink version={status.version} />
              </Row>
              <Row label={t('settings.about.commit', 'Commit')}>
                <code className="font-mono text-xs">{status.commit}</code>
              </Row>
              {status.buildDate && (
                <Row label={t('settings.about.buildDate', 'Build date')}>
                  <span className="font-mono text-xs">{status.buildDate}</span>
                </Row>
              )}
            </>
          )}
        </div>
      </section>
    </div>
  )
}
