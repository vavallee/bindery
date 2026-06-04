import { Link } from 'react-router-dom'
import { BookRef } from '../api/client'

// BookAuthorLink renders a compact secondary line — linked book title · linked
// author name — for rows that reference a book (queue, pending, history). It
// returns null when the row has no associated book, so callers can drop it in
// unconditionally. The router basename already prefixes the configured subpath
// (BINDERY_URL_BASE), so the to= paths stay root-relative.
export default function BookAuthorLink({ book, className }: { book?: BookRef; className?: string }) {
  if (!book) return null
  return (
    <p className={`text-xs text-slate-600 dark:text-zinc-400 truncate ${className ?? ''}`}>
      <Link
        to={`/book/${book.id}`}
        className="hover:text-emerald-600 dark:hover:text-emerald-400 transition-colors"
      >
        {book.title}
      </Link>
      {book.authorId > 0 && book.authorName && (
        <>
          {' · '}
          <Link
            to={`/author/${book.authorId}`}
            className="hover:text-emerald-600 dark:hover:text-emerald-400 transition-colors"
          >
            {book.authorName}
          </Link>
        </>
      )}
    </p>
  )
}
