import { useState, useRef, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { Recommendation } from '../api/client'

interface RecommendationCardProps {
  rec: Recommendation
  onDismiss: (id: number) => void
  onAdd: (id: number) => void
  onExcludeAuthor: (authorName: string) => void
}

export default function RecommendationCard({ rec, onDismiss, onAdd, onExcludeAuthor }: RecommendationCardProps) {
  const { t } = useTranslation()
  const [menuOpen, setMenuOpen] = useState(false)
  const menuRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!menuOpen) return
    const handleClick = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [menuOpen])

  const imageUrl = rec.imageUrl
    ? `/api/v1/images?url=${encodeURIComponent(rec.imageUrl)}`
    : ''

  const stars = Math.round(rec.rating * 2) / 2

  return (
    <div className="bg-slate-100 dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden flex flex-col">
      {/* Cover — portrait aspect ratio matching the book grid */}
      <div className="aspect-[2/3] bg-slate-200 dark:bg-zinc-800 relative flex items-center justify-center flex-shrink-0">
        {imageUrl ? (
          <img src={imageUrl} alt={rec.title} className="w-full h-full object-cover" />
        ) : (
          <svg className="w-10 h-10 text-slate-400 dark:text-zinc-600" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5} d="M12 6.253v13m0-13C10.832 5.477 9.246 5 7.5 5S4.168 5.477 3 6.253v13C4.168 18.477 5.754 18 7.5 18s3.332.477 4.5 1.253m0-13C13.168 5.477 14.754 5 16.5 5c1.747 0 3.332.477 4.5 1.253v13C19.832 18.477 18.247 18 16.5 18c-1.746 0-3.332.477-4.5 1.253" />
          </svg>
        )}
        {/* Reason badge overlaid on the image */}
        <span className="absolute bottom-0 left-0 right-0 px-2 py-1 bg-gradient-to-t from-black/70 to-transparent text-[10px] text-white italic line-clamp-2 leading-tight">
          {rec.reason}
        </span>
      </div>

      <div className="p-3 flex flex-col flex-1">
        {/* Title and author */}
        <h4 className="font-medium text-sm leading-snug line-clamp-2" title={rec.title}>{rec.title}</h4>
        <p className="text-xs text-slate-500 dark:text-zinc-500 truncate mt-0.5">{rec.authorName}</p>

        {/* Star rating */}
        {rec.rating > 0 && (
          <div className="flex items-center gap-1 mt-1.5">
            <div className="flex">
              {[1, 2, 3, 4, 5].map(i => (
                <svg
                  key={i}
                  className={`w-3 h-3 ${i <= stars ? 'text-amber-400' : 'text-slate-300 dark:text-zinc-700'}`}
                  fill="currentColor"
                  viewBox="0 0 20 20"
                >
                  <path d="M9.049 2.927c.3-.921 1.603-.921 1.902 0l1.07 3.292a1 1 0 00.95.69h3.462c.969 0 1.371 1.24.588 1.81l-2.8 2.034a1 1 0 00-.364 1.118l1.07 3.292c.3.921-.755 1.688-1.54 1.118l-2.8-2.034a1 1 0 00-1.175 0l-2.8 2.034c-.784.57-1.838-.197-1.539-1.118l1.07-3.292a1 1 0 00-.364-1.118L2.98 8.72c-.783-.57-.38-1.81.588-1.81h3.461a1 1 0 00.951-.69l1.07-3.292z" />
                </svg>
              ))}
            </div>
            {rec.ratingsCount > 0 && (
              <span className="text-[10px] text-slate-400 dark:text-zinc-600">({rec.ratingsCount})</span>
            )}
          </div>
        )}

        {/* Genre tags */}
        {rec.genres.length > 0 && (
          <div className="flex flex-wrap gap-1 mt-1.5">
            {rec.genres.slice(0, 2).map(genre => (
              <span key={genre} className="px-1.5 py-0.5 bg-slate-200 dark:bg-zinc-800 rounded text-[10px] text-slate-600 dark:text-zinc-400 truncate max-w-[90px]">
                {genre}
              </span>
            ))}
          </div>
        )}

        {/* Spacer pushes actions to bottom */}
        <div className="flex-1" />

        {/* Actions */}
        <div className="flex items-center gap-1.5 mt-3">
          <button
            onClick={() => onAdd(rec.id)}
            className="flex-1 px-2 py-1.5 bg-emerald-600 hover:bg-emerald-500 text-white rounded text-xs font-medium transition-colors"
          >
            {t('discover.addToWanted')}
          </button>
          <button
            onClick={() => onDismiss(rec.id)}
            className="px-2 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs text-slate-600 dark:text-zinc-400 transition-colors"
            aria-label={t('discover.dismiss')}
            title={t('discover.dismiss')}
          >
            ✕
          </button>
          <div className="relative" ref={menuRef}>
            <button
              onClick={() => setMenuOpen(o => !o)}
              className="px-1.5 py-1.5 bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded text-xs text-slate-600 dark:text-zinc-400 transition-colors"
            >
              &middot;&middot;&middot;
            </button>
            {menuOpen && (
              <div className="absolute right-0 bottom-full mb-1 w-48 bg-white dark:bg-zinc-800 border border-slate-200 dark:border-zinc-700 rounded-lg shadow-lg z-10">
                <button
                  onClick={() => { onExcludeAuthor(rec.authorName); setMenuOpen(false) }}
                  className="w-full text-left px-3 py-2 text-xs text-slate-700 dark:text-zinc-300 hover:bg-slate-100 dark:hover:bg-zinc-700 rounded-lg transition-colors"
                >
                  {t('discover.dontSuggestAuthor', { author: rec.authorName })}
                </button>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
