import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import type { LibraryRequest, RequestKind } from '../api/client'
import CoverImage from './CoverImage'
import { primaryFill } from './buttons'

// The confirm step a requester sees in place of the Add Author and Add Book
// steps. A requester chooses only the format; the admin who approves chooses
// everything else, so none of the admin options are shown or sent.
interface Props {
  kind: RequestKind
  foreignId: string
  title: string
  subtitle?: string
  imageUrl?: string
  // The formats a request can still ask for, when the library already holds
  // the book with the other one (#3898). Absent means any format.
  requestableFormats?: string[]
  onBack: () => void
  onClose: () => void
  onRequested: (request: LibraryRequest) => void
}

export default function RequestConfirm({ kind, foreignId, title, subtitle, imageUrl, requestableFormats, onBack, onClose, onRequested }: Props) {
  const { t } = useTranslation()
  // With one format left to ask for, that format is the only choice: "any
  // format" could resolve to the one the library has and be refused, and
  // "both" would ask for nothing more.
  const onlyFormat = requestableFormats?.length === 1 ? requestableFormats[0] : ''
  const [mediaType, setMediaType] = useState(onlyFormat)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)

  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  const send = async () => {
    setSending(true)
    setError(null)
    try {
      const created = await api.createRequest(kind, foreignId, mediaType)
      onRequested(created)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('requests.confirm.failed'))
    } finally {
      setSending(false)
    }
  }

  return (
    <>
      <div className="p-4 flex-1 overflow-y-auto">
        <div className="flex items-start gap-4 rounded-md border border-slate-300 dark:border-zinc-700 bg-slate-200/50 dark:bg-zinc-800/50 p-4">
          <CoverImage src={imageUrl} alt="" className="w-20 aspect-[2/3] object-cover rounded-md flex-shrink-0" />
          <div className="min-w-0 flex-1">
            <h4 ref={headingRef} tabIndex={-1} className="rounded-sm font-semibold leading-snug break-words focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-accent-ring">{title}</h4>
            {subtitle && <p className="mt-1 text-sm text-fg-muted">{subtitle}</p>}
            <p className="mt-3 text-xs text-fg-muted">
              {kind === 'author' ? t('requests.confirm.authorHint') : t('requests.confirm.bookHint')}
            </p>
          </div>
        </div>

        <label className="mt-4 flex items-center gap-2 text-sm select-none">
          <span className="font-medium">{t('requests.confirm.format')}</span>
          <select
            aria-label={t('requests.confirm.formatLabel')}
            value={mediaType}
            onChange={e => setMediaType(e.target.value)}
            className="text-xs bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded px-2 py-1"
          >
            <option value="" disabled={onlyFormat !== ''}>{t('requests.confirm.anyFormat')}</option>
            <option value="ebook" disabled={onlyFormat !== '' && onlyFormat !== 'ebook'}>{t('common.ebook')}</option>
            <option value="audiobook" disabled={onlyFormat !== '' && onlyFormat !== 'audiobook'}>{t('common.audiobook')}</option>
            <option value="both" disabled={onlyFormat !== ''}>{t('common.both')}</option>
          </select>
        </label>

        {error && (
          <div role="alert" className="mt-3 px-3 py-2 bg-danger-subtle border border-danger-border rounded text-sm text-danger-strong">
            {error}
          </div>
        )}
      </div>

      <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end gap-2">
        <button type="button" onClick={onBack} disabled={sending} className="mr-auto px-4 py-2 text-sm text-fg-muted hover:text-slate-900 dark:hover:text-white disabled:opacity-50">{t('addToLibrary.backToResults')}</button>
        <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-fg-muted hover:text-slate-900 dark:hover:text-white">{t('common.cancel')}</button>
        <button type="button" onClick={send} disabled={sending} className={`px-4 py-2 ${primaryFill} disabled:opacity-50 disabled:cursor-not-allowed rounded-md text-sm font-medium`}>
          {sending ? t('requests.confirm.sending') : kind === 'author' ? t('requests.confirm.requestAuthor') : t('requests.confirm.requestBook')}
        </button>
      </div>
    </>
  )
}
