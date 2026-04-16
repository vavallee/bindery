import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Book } from '../api/client'

function getDaysInMonth(year: number, month: number) {
  return new Date(year, month + 1, 0).getDate()
}

function getFirstDayOfMonth(year: number, month: number) {
  return new Date(year, month, 1).getDay()
}

const MONTH_NAMES = [
  'January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December',
]

const DAY_NAMES = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
const DAY_NAMES_SHORT = ['S', 'M', 'T', 'W', 'T', 'F', 'S']

export default function CalendarPage() {
  const { t } = useTranslation()
  const [books, setBooks] = useState<Book[]>([])
  const [loading, setLoading] = useState(true)
  const today = new Date()
  const [viewYear, setViewYear] = useState(today.getFullYear())
  const [viewMonth, setViewMonth] = useState(today.getMonth())

  useEffect(() => {
    api.listBooks().then(setBooks).catch(console.error).finally(() => setLoading(false))
  }, [])

  const prevMonth = () => {
    if (viewMonth === 0) { setViewMonth(11); setViewYear(y => y - 1) }
    else setViewMonth(m => m - 1)
  }

  const nextMonth = () => {
    if (viewMonth === 11) { setViewMonth(0); setViewYear(y => y + 1) }
    else setViewMonth(m => m + 1)
  }

  const goToToday = () => {
    setViewYear(today.getFullYear())
    setViewMonth(today.getMonth())
  }

  // Index books by day-of-month for the current view
  const booksByDay: Record<number, Book[]> = {}
  for (const book of books) {
    if (!book.releaseDate || !book.monitored) continue
    const d = new Date(book.releaseDate)
    if (d.getFullYear() === viewYear && d.getMonth() === viewMonth) {
      const day = d.getDate()
      if (!booksByDay[day]) booksByDay[day] = []
      booksByDay[day].push(book)
    }
  }

  const daysInMonth = getDaysInMonth(viewYear, viewMonth)
  const firstDay = getFirstDayOfMonth(viewYear, viewMonth)
  const isCurrentMonth = viewYear === today.getFullYear() && viewMonth === today.getMonth()

  // Build calendar grid cells (some are empty padding)
  const cells: Array<number | null> = []
  for (let i = 0; i < firstDay; i++) cells.push(null)
  for (let i = 1; i <= daysInMonth; i++) cells.push(i)
  while (cells.length % 7 !== 0) cells.push(null)

  const hasReleases = Object.keys(booksByDay).length > 0

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-bold">{t('calendar.title')}</h2>
        <div className="flex items-center gap-2">
          {!isCurrentMonth && (
            <button
              onClick={goToToday}
              className="px-3 py-1.5 text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white border border-slate-300 dark:border-zinc-700 rounded transition-colors"
            >
              {t('calendar.today')}
            </button>
          )}
          <button
            onClick={prevMonth}
            className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded transition-colors"
          >
            ‹
          </button>
          <span className="text-sm font-medium w-36 text-center">
            {MONTH_NAMES[viewMonth]} {viewYear}
          </span>
          <button
            onClick={nextMonth}
            className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded transition-colors"
          >
            ›
          </button>
        </div>
      </div>

      {loading ? (
        <div className="text-slate-600 dark:text-zinc-500">{t('common.loading')}</div>
      ) : (
        <>
          {/* Grid calendar — hidden on mobile, shown sm+ */}
          <div className="hidden sm:block border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
            {/* Day headers */}
            <div className="grid grid-cols-7 bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
              {DAY_NAMES.map(d => (
                <div key={d} className="py-2 text-center text-xs font-medium text-slate-600 dark:text-zinc-500 uppercase tracking-wider">
                  {d}
                </div>
              ))}
            </div>

            {/* Calendar grid */}
            <div className="grid grid-cols-7">
              {cells.map((day, idx) => {
                const isToday = isCurrentMonth && day === today.getDate()
                const dayBooks = day ? (booksByDay[day] ?? []) : []
                return (
                  <div
                    key={idx}
                    className={`min-h-[100px] p-2 border-b border-r border-slate-200 dark:border-zinc-800 ${
                      day ? 'bg-slate-100/50 dark:bg-zinc-900/50' : 'bg-slate-100/20 dark:bg-zinc-900/20'
                    } ${idx % 7 === 6 ? 'border-r-0' : ''}`}
                  >
                    {day && (
                      <>
                        <div className={`text-xs font-medium mb-1 w-6 h-6 flex items-center justify-center rounded-full ${
                          isToday ? 'bg-emerald-600 text-white' : 'text-slate-600 dark:text-zinc-400'
                        }`}>
                          {day}
                        </div>
                        <div className="space-y-1">
                          {dayBooks.map(book => (
                            <div
                              key={book.id}
                              title={book.title}
                              className="text-[10px] leading-tight px-1.5 py-1 bg-emerald-500/20 text-emerald-300 rounded truncate cursor-default"
                            >
                              {book.title}
                            </div>
                          ))}
                        </div>
                      </>
                    )}
                  </div>
                )
              })}
            </div>
          </div>

          {/* Compact grid for mobile — shown below sm */}
          <div className="sm:hidden border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden mb-4">
            <div className="grid grid-cols-7 bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
              {DAY_NAMES_SHORT.map((d, i) => (
                <div key={i} className="py-2 text-center text-xs font-medium text-slate-600 dark:text-zinc-500">
                  {d}
                </div>
              ))}
            </div>
            <div className="grid grid-cols-7">
              {cells.map((day, idx) => {
                const isToday = isCurrentMonth && day === today.getDate()
                const hasBooks = day ? (booksByDay[day]?.length ?? 0) > 0 : false
                return (
                  <div
                    key={idx}
                    className={`aspect-square flex flex-col items-center justify-center border-b border-r border-slate-200 dark:border-zinc-800 text-xs ${
                      idx % 7 === 6 ? 'border-r-0' : ''
                    } ${day ? 'bg-slate-100/50 dark:bg-zinc-900/50' : 'bg-slate-100/20 dark:bg-zinc-900/20'}`}
                  >
                    {day && (
                      <>
                        <span className={`w-6 h-6 flex items-center justify-center rounded-full text-xs ${
                          isToday ? 'bg-emerald-600 text-white' : 'text-slate-600 dark:text-zinc-400'
                        }`}>
                          {day}
                        </span>
                        {hasBooks && (
                          <span className="w-1.5 h-1.5 rounded-full bg-emerald-400 mt-0.5" />
                        )}
                      </>
                    )}
                  </div>
                )
              })}
            </div>
          </div>

          {/* Agenda list — always visible, primary view on mobile */}
          {hasReleases ? (
            <div className="mt-4 border border-slate-200 dark:border-zinc-800 rounded-lg overflow-hidden">
              <div className="px-4 py-2 bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
                <p className="text-xs text-slate-600 dark:text-zinc-400 font-medium">
                  {t('calendar.releasingIn', { month: MONTH_NAMES[viewMonth], year: viewYear })}
                </p>
              </div>
              <div className="divide-y divide-slate-200 dark:divide-zinc-800">
                {Object.entries(booksByDay)
                  .sort(([a], [b]) => Number(a) - Number(b))
                  .flatMap(([day, dayBooks]) =>
                    dayBooks.map(book => (
                      <div key={book.id} className="flex items-center gap-3 px-4 py-3">
                        <span className="text-xs text-slate-600 dark:text-zinc-500 w-12 flex-shrink-0">
                          {MONTH_NAMES[viewMonth].slice(0, 3)} {day}
                        </span>
                        {book.imageUrl && (
                          <img src={book.imageUrl} alt="" className="w-8 h-10 object-cover rounded flex-shrink-0" />
                        )}
                        <span className="text-sm text-slate-800 dark:text-zinc-200 min-w-0 truncate">{book.title}</span>
                        {book.author && (
                          <span className="text-xs text-slate-600 dark:text-zinc-500 flex-shrink-0 hidden sm:block">
                            {book.author.authorName}
                          </span>
                        )}
                      </div>
                    ))
                  )}
              </div>
            </div>
          ) : (
            <p className="mt-4 text-center text-sm text-slate-500 dark:text-zinc-600">
              {t('calendar.noReleases')}
            </p>
          )}
        </>
      )}
    </div>
  )
}
