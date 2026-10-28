import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import type { BookCalibreState } from '../api/client'

const tones: Record<string, string> = {
  pending: 'bg-sky-500/20 text-sky-800 dark:text-sky-400',
  delivered: 'bg-emerald-500/20 text-emerald-800 dark:text-emerald-400',
  failed: 'bg-red-500/20 text-red-800 dark:text-red-400',
}

// CalibreDeliveryChip shows where a book stands with Calibre (#2832):
// waiting, in Calibre, or failed. It renders nothing when the integration is
// off, the book was never queued, or the state cannot be read. Admins get the
// failure detail as a tooltip; the server sends it to no one else.
export default function CalibreDeliveryChip({ bookId }: { bookId: number }) {
  const { t } = useTranslation()
  const [state, setState] = useState<BookCalibreState | null>(null)

  useEffect(() => {
    let cancelled = false
    api.bookCalibreState(bookId)
      .then(s => { if (!cancelled) setState(s) })
      .catch(() => { if (!cancelled) setState(null) })
    return () => { cancelled = true }
  }, [bookId])

  if (!state || !(state.state in tones)) return null
  const detail = [state.lastErrorCode, state.lastError].filter(Boolean).join(': ')
  return (
    <span
      data-testid="calibre-delivery-chip"
      className={`inline-flex items-center px-2 py-0.5 rounded font-medium ${tones[state.state]}`}
      title={detail || undefined}
    >
      {t(`bookDetail.calibreChip.${state.state}`)}
    </span>
  )
}
