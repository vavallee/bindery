import { useTranslation } from 'react-i18next'
import { isNewerRelease, isReleaseVersion, releaseHref } from '../util/version'

// The header version indicator, rendered in the desktop nav and the mobile
// menu footer. Two states:
//   - up to date / unknown: the muted version link (previous behavior)
//   - update available: an amber "v1.28.0 → v1.30.0" badge linking to the
//     newest release. `latestVersion` comes from /system/status, which
//     relays the telemetry ping server's answer; it is empty when telemetry
//     is disabled, so opted-out installs simply keep the muted link.
export default function VersionBadge({
  version,
  latestVersion,
  className,
}: {
  version: string
  latestVersion?: string
  className?: string
}) {
  const { t } = useTranslation()
  const updateAvailable = latestVersion !== undefined && isNewerRelease(version, latestVersion)

  if (updateAvailable) {
    return (
      <a
        href={releaseHref(latestVersion.replace(/^v/, ''))}
        target="_blank"
        rel="noopener noreferrer"
        title={t('nav.updateAvailable', 'Update available')}
        className={`text-xs font-medium text-amber-700 dark:text-amber-400 hover:underline whitespace-nowrap ${className ?? ''}`}
      >
        {`v${version} → v${latestVersion.replace(/^v/, '')}`}
      </a>
    )
  }

  return (
    <a
      href={releaseHref(version)}
      target="_blank"
      rel="noopener noreferrer"
      className={`text-xs text-fg-muted hover:underline whitespace-nowrap ${className ?? ''}`}
    >
      {isReleaseVersion(version) ? `v${version}` : version}
    </a>
  )
}
