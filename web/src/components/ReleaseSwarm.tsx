import { useTranslation } from 'react-i18next'

// ReleaseSwarm appends a torrent release's seeders and peers to its details
// line (#3819). Grabs is a lifetime download count, so a release with many
// grabs can still be dead today; seeders say whether it can download now.
// Each count renders only when the indexer reported it, so usenet rows and
// indexers that leave the attributes out read exactly as before, while a
// reported 0 still shows.
export default function ReleaseSwarm({ seeders, peers }: { seeders?: number; peers?: number }) {
  const { t } = useTranslation()
  return (
    <>
      {seeders != null && (
        <>
          {' · '}
          <span className={seeders === 0 ? 'text-red-700 dark:text-red-400' : undefined}>
            {t('search.seeders', { count: seeders })}
          </span>
        </>
      )}
      {peers != null && <>{' · '}{t('search.peers', { count: peers })}</>}
    </>
  )
}
