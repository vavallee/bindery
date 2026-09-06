import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { AuthorSyncSummary } from '../api/client'

// authorSyncSampleLimit mirrors the server's per-filter cap
// (authorSyncSkippedSampleLimit, internal/api/author_sync_summary.go) — that
// constant bounds each individual Skipped*Sample array so the notice's "for
// example" line stays readable, but merging six of those arrays multiplies
// it by six. This is the merged notice's own cap, applied once here.
const authorSyncSampleLimit = 5

// roundRobinSample interleaves one title from each non-empty source per
// round rather than draining sources in order, so a strict profile that
// trips several filters doesn't have the first source (language) silently
// crowd out every other filter's example — the point of the sample is
// showing which filter ate a book, not just that one did.
function roundRobinSample(sources: string[][], limit: number): string[] {
  const sample: string[] = []
  for (let round = 0; sample.length < limit; round++) {
    let addedAny = false
    for (const source of sources) {
      if (sample.length >= limit) break
      if (round < source.length) {
        sample.push(source[round])
        addedAny = true
      }
    }
    if (!addedAny) break
  }
  return sample
}

// The catalogue sync's filters used to drop books in silence: one Debug log
// line per rejected work and a single Info summary at the end, nothing in the
// API and nothing on the page. A reporter lost 65 books from one author to the
// allowed-languages filter and only found out by going looking in the logs,
// which a rootless container does not hand them (#1889).
//
// This is the same information the sync already had, put where the missing
// books are. It is informational rather than an error — the filter did what it
// was configured to do — so it reads as a note, not a failure.
export default function AuthorSyncNotice({ sync }: { sync?: AuthorSyncSummary }) {
  const { t } = useTranslation()
  if (!sync) return null

  const notAccepted = sync.skippedNotAccepted ?? 0
  const partBooks = sync.skippedPartBooks ?? 0
  const missingDate = sync.skippedMissingDate ?? 0
  const minPopularity = sync.skippedMinPopularity ?? 0
  const minPages = sync.skippedMinPages ?? 0
  const missingIsbn = sync.skippedMissingIsbn ?? 0
  const failed = sync.failed ?? 0
  // skippedExcluded is deliberately absent from this sum and from the list
  // below. The user excluded that book on purpose, so it is not something the
  // page owes them an explanation for. It is still in the payload, because
  // total has to reconcile (#2449).
  const skipped =
    sync.skippedLanguage +
    sync.skippedJunk +
    sync.skippedMediaType +
    notAccepted +
    partBooks +
    missingDate +
    minPopularity +
    minPages +
    missingIsbn
  // A sync that dropped nothing and lost nothing has nothing to explain; the
  // book list already says what happened. A failed write is worth saying even
  // when no filter fired, because it is the one outcome here that is a fault
  // rather than a setting.
  if (skipped <= 0 && failed <= 0) return null

  const languages = sync.allowedLanguages?.length
    ? sync.allowedLanguages.join(', ')
    : t('authorDetail.lastSync.anyLanguage', 'any')
  // One combined "for example" line built from whichever filters dropped
  // something, same shape as the original language-only sample — a bare
  // count doesn't say which books vanished, and Debug logs aren't reachable
  // in a rootless container. Capped and round-robined across sources
  // (authorSyncSampleLimit) rather than concatenated: six per-filter
  // samples of 5 each would otherwise print up to 30 titles on one line,
  // and a plain concatenation would let language (listed first) crowd out
  // every other filter's example.
  const sample = roundRobinSample(
    [
      (sync.skippedLanguageSample ?? []).map(b =>
        b.language
          ? `${b.title} (${b.language})`
          : t('authorDetail.lastSync.sampleUnknownLanguage', '{{title}} (no language)', { title: b.title }),
      ),
      (sync.skippedPartBooksSample ?? []).map(b => b.title),
      (sync.skippedMissingDateSample ?? []).map(b => b.title),
      (sync.skippedMinPopularitySample ?? []).map(b => b.title),
      (sync.skippedMinPagesSample ?? []).map(b => b.title),
      (sync.skippedMissingIsbnSample ?? []).map(b => b.title),
    ],
    authorSyncSampleLimit,
  )

  return (
    <div
      data-testid="author-sync-notice"
      className="mb-4 px-3 py-2 rounded border text-sm border-amber-300 dark:border-amber-900 bg-amber-50 dark:bg-amber-950/30 text-amber-900 dark:text-amber-200"
    >
      <div className="font-medium">
        {skipped > 0
          ? t('authorDetail.lastSync.heading', {
              count: skipped,
              defaultValue: 'Last refresh skipped {{count}} of this author’s {{total}} works',
              total: sync.total,
            })
          : t('authorDetail.lastSync.headingFailedOnly', {
              count: failed,
              defaultValue: 'Last refresh could not save {{count}} of this author’s {{total}} works',
              total: sync.total,
            })}
      </div>
      {/* The sentence that makes the numbers add up. Without it the notice
          reports a skip count against a total and leaves the reader to guess
          at the gap, which reads as loss even when every one of those works
          is on the shelf already (#2449). */}
      <div className="mt-0.5">
        {t('authorDetail.lastSync.accounting', {
          defaultValue: '{{added}} added, {{matched}} already in your library.',
          added: sync.added,
          // `?? 0` despite `matched` being required in AuthorSyncSummary. The
          // type describes what a current server sends; this value is decoded
          // from JSON, and `npm run dev` proxies to whatever backend is on
          // :8787, which may predate the field. Rendering "undefined already in
          // your library" would be a worse version of the exact bug this line
          // was added to fix.
          matched: sync.matched ?? 0,
        })}
      </div>
      <ul className="mt-1 list-disc list-inside space-y-0.5">
        {/* First, because it is the only entry here that is a fault rather
            than a setting the user chose. */}
        {failed > 0 && (
          <li>
            {t('authorDetail.lastSync.failed', {
              count: failed,
              defaultValue:
                '{{count}} could not be saved. This is an error rather than a filter, so the details are in Settings, then Logs.',
            })}
          </li>
        )}
        {sync.skippedLanguage > 0 && (
          <li>
            {t('authorDetail.lastSync.language', {
              count: sync.skippedLanguage,
              defaultValue: '{{count}} skipped by the language filter (allowed: {{languages}})',
              languages,
            })}
            {sync.unknownLanguageFail
              ? ' ' +
                t(
                  'authorDetail.lastSync.unknownFail',
                  'Works the metadata provider reported no language for were skipped too, because this author’s metadata profile is set to reject unknown languages.',
                )
              : ''}
          </li>
        )}
        {sync.skippedJunk > 0 && (
          <li>
            {t('authorDetail.lastSync.junk', {
              count: sync.skippedJunk,
              defaultValue: '{{count}} skipped as untitled provider records',
            })}
          </li>
        )}
        {sync.skippedMediaType > 0 && (
          <li>
            {t('authorDetail.lastSync.mediaType', {
              count: sync.skippedMediaType,
              defaultValue: '{{count}} skipped as the wrong format for your default media type',
            })}
          </li>
        )}
        {/* A refresh only adds books for an author you monitor and have set to
            take new items (#1815). Saying so here is the difference between
            "the refresh is broken" and "it did what I configured". */}
        {notAccepted > 0 && (
          <li>
            {t('authorDetail.lastSync.notAccepted', {
              count: notAccepted,
              defaultValue:
                '{{count}} not added, because this author is not taking newly discovered books — the author is unmonitored, or "Monitor newly discovered books" is set to don’t add them. Books already in your library were still refreshed.',
            })}
          </li>
        )}
        {partBooks > 0 && (
          <li>
            {t('authorDetail.lastSync.partBooks', {
              count: partBooks,
              defaultValue: '{{count}} skipped as box sets, omnibuses, or other multi-book bundles',
            })}
          </li>
        )}
        {missingDate > 0 && (
          <li>
            {t('authorDetail.lastSync.missingDate', {
              count: missingDate,
              defaultValue: '{{count}} skipped for having no release date',
            })}
          </li>
        )}
        {minPopularity > 0 && (
          <li>
            {t('authorDetail.lastSync.minPopularity', {
              count: minPopularity,
              defaultValue: '{{count}} skipped for falling below the minimum popularity floor',
            })}
          </li>
        )}
        {minPages > 0 && (
          <li>
            {t('authorDetail.lastSync.minPages', {
              count: minPages,
              defaultValue: '{{count}} skipped for falling below the minimum page count',
            })}
          </li>
        )}
        {missingIsbn > 0 && (
          <li>
            {t('authorDetail.lastSync.missingIsbn', {
              count: missingIsbn,
              defaultValue: '{{count}} skipped for having no ISBN on any edition',
            })}
          </li>
        )}
      </ul>
      {sample.length > 0 && (
        <div className="mt-1">
          {t('authorDetail.lastSync.examples', 'For example: {{titles}}', { titles: sample.join(', ') })}
        </div>
      )}
      <div className="mt-1 text-xs">
        <Link to="/settings?tab=metadata" className="underline">
          {t('authorDetail.lastSync.settingsLink', 'Metadata profile settings')}
        </Link>
        {' · '}
        {t('authorDetail.lastSync.completedAt', 'Refreshed {{when}}', {
          when: new Date(sync.completedAt).toLocaleString(),
        })}
      </div>
    </div>
  )
}
