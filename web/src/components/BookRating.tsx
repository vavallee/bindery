import { useTranslation } from 'react-i18next'
import type { Book } from '../api/client'

// Community rating as "★ 4.21", followed by the ratings count when withCount
// is set. Renders nothing for a book no provider has rated. Both numbers are
// formatted for the UI language, so German reads "4,21" and "1.234".
export default function BookRating({ book, withCount = false, className }: {
  book: Pick<Book, 'averageRating' | 'ratingsCount'>
  withCount?: boolean
  className?: string
}) {
  const { t, i18n } = useTranslation()
  if (!book.averageRating || book.averageRating <= 0) return null
  const lang = i18n.resolvedLanguage ?? i18n.language
  const average = book.averageRating.toLocaleString(lang, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  const count = book.ratingsCount
    ? t('books.ratingsCount', { count: book.ratingsCount, formatted: book.ratingsCount.toLocaleString(lang) })
    : ''
  return (
    <span className={className} title={withCount ? undefined : count || undefined}>
      ★ {average}{withCount && count ? ` (${count})` : ''}
    </span>
  )
}
