import { useTranslation } from 'react-i18next'
import type { MediaType } from '../api/client'

// A book's media type has three values, but this badge used to be a binary
// audiobook check — so 'both' rendered as "📖 Ebook", quietly hiding half of
// what the book holds. It is now also used per file row on the book detail
// page, where each row is labelled by its own book_files.format.
const STYLES: Record<MediaType, { labelKey: string; label: string; icon: string; cls: string }> = {
  ebook: {
    labelKey: 'mediaType.ebook',
    label: 'Ebook',
    icon: '📖',
    cls: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300',
  },
  audiobook: {
    labelKey: 'mediaType.audiobook',
    label: 'Audiobook',
    icon: '🎧',
    cls: 'bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300',
  },
  both: {
    labelKey: 'mediaBadge.both',
    label: 'Ebook + Audiobook',
    icon: '📖🎧',
    cls: 'bg-violet-100 text-violet-800 dark:bg-violet-950 dark:text-violet-300',
  },
}

export default function MediaBadge({ type }: { type?: MediaType }) {
  const { t } = useTranslation()
  const { labelKey, label, icon, cls } = STYLES[type ?? 'ebook'] ?? STYLES.ebook
  return (
    <span className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium ${cls}`}>
      <span aria-hidden>{icon}</span>
      {t(labelKey, label)}
    </span>
  )
}
