import type { TFunction } from 'i18next'
import { api, ApiError, type Download, type GrabRequest, type SearchResult } from '../api/client'
import type { ConfirmOptions } from '../components/useConfirmDialog'

/**
 * True when a grab was refused only because Bindery already imported the same
 * release, which the server lets the user override with force (#2289). Read
 * from the structured `forceAvailable` flag, never from the message text.
 */
export function isForceableGrabRefusal(err: unknown): boolean {
  return err instanceof ApiError && err.status === 409 && err.body?.forceAvailable === true
}

/**
 * Grabs a release, and when the server refuses it as already imported, asks
 * the user whether to grab it anyway and retries with force if they agree.
 * Resolves to the download, or null when the user declined. Any other error
 * is thrown unchanged.
 */
export async function grabWithForceConfirm(
  req: GrabRequest,
  confirm: (options: ConfirmOptions) => Promise<boolean>,
  t: TFunction,
): Promise<Download | null> {
  try {
    return await api.grab(req)
  } catch (err) {
    if (!isForceableGrabRefusal(err)) throw err
    const ok = await confirm({
      title: t('search.forceTitle'),
      body: t('search.forceBody'),
      confirmLabel: t('search.forceConfirm'),
    })
    if (!ok) return null
    return api.grab({ ...req, force: true })
  }
}

/**
 * Asks before grabbing a multi-book pack (#3247). A pack can be grabbed, but
 * the importer will not place it automatically, so the user is told before the
 * download starts. Resolves true for a release that is not a pack, without
 * asking. Every page that grabs from an interactive search calls this, guarded
 * by `r.pack &&` so an ordinary grab starts in the same tick as the click.
 */
export async function confirmPackGrab(
  r: Pick<SearchResult, 'pack'>,
  confirm: (options: ConfirmOptions) => Promise<boolean>,
  t: TFunction,
): Promise<boolean> {
  if (!r.pack) return true
  return confirm({
    title: t('search.pack.confirmTitle'),
    body: t('search.pack.confirmBody', { marker: r.pack }),
    confirmLabel: t('search.pack.confirm'),
  })
}

/**
 * The sentence to show for a failed grab. Uses the server's own message when it
 * sent one. A proxy answer (an empty 503, an HTML error page) has no message,
 * and the status text of an HTTP/2 response is empty, so without this the error
 * state was set to "" and nothing rendered. A network failure surfaces from
 * fetch as a TypeError and gets its own plain sentence.
 */
export function grabErrorText(err: unknown, t: TFunction): string {
  if (err instanceof ApiError) {
    return err.message.trim() || t('search.grabFailedStatus', { status: err.status })
  }
  if (err instanceof TypeError) return t('search.grabFailedNetwork')
  if (err instanceof Error && err.message.trim()) return err.message
  return t('search.grabFailed', 'Grab failed')
}
