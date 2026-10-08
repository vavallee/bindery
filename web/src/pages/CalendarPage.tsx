import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, Book } from '../api/client'
import { bucketBooksByDay } from './calendarBuckets'

function getDaysInMonth(year: number, month: number) {
  return new Date(year, month + 1, 0).getDate()
}

function getFirstDayOfMonth(year: number, month: number) {
  return new Date(year, month, 1).getDay()
}

// Month and weekday names come from Intl in the active UI language, so the
// calendar follows the language switcher instead of always reading English.
// Dates are built in UTC and formatted in UTC so no local offset can move a
// label onto the neighbouring day or month.
function monthLabel(lang: string | undefined, year: number, month: number, opts: Intl.DateTimeFormatOptions) {
  return new Intl.DateTimeFormat(lang, { ...opts, timeZone: 'UTC' }).format(new Date(Date.UTC(year, month, 1)))
}

function dayOfMonthLabel(lang: string | undefined, year: number, month: number, day: number) {
  return new Intl.DateTimeFormat(lang, { month: 'short', day: 'numeric', timeZone: 'UTC' }).format(new Date(Date.UTC(year, month, day)))
}

// Sunday first, matching getFirstDayOfMonth. 2023-01-01 was a Sunday.
function weekdayNames(lang: string | undefined, weekday: 'short' | 'narrow') {
  const fmt = new Intl.DateTimeFormat(lang, { weekday, timeZone: 'UTC' })
  return Array.from({ length: 7 }, (_, i) => fmt.format(new Date(Date.UTC(2023, 0, 1 + i))))
}

export default function CalendarPage() {
  const { t, i18n } = useTranslation()
  const lang = i18n.resolvedLanguage ?? i18n.language
  const [books, setBooks] = useState<Book[]>([])
  const [loading, setLoading] = useState(true)
  const today = new Date()
  const [viewYear, setViewYear] = useState(today.getFullYear())
  const [viewMonth, setViewMonth] = useState(today.getMonth())
  // The day tapped in the phone grid, whose releases the agenda then lists on
  // their own. Kept with its month so paging away drops it without an effect.
  const [picked, setPicked] = useState<{ year: number; month: number; day: number } | null>(null)

  // Fetch only the visible month's releases (a scoped server query) rather than
  // pulling the whole library and filtering client-side. Re-fetches when the
  // month changes. releaseFrom is inclusive, releaseBefore exclusive (the 1st of
  // the next month), so the query is exactly [month-start, next-month-start).
  useEffect(() => {
    const pad = (n: number) => String(n).padStart(2, '0')
    const from = `${viewYear}-${pad(viewMonth + 1)}-01`
    const beforeYear = viewMonth === 11 ? viewYear + 1 : viewYear
    const beforeMonth = viewMonth === 11 ? 0 : viewMonth + 1
    const before = `${beforeYear}-${pad(beforeMonth + 1)}-01`
    setLoading(true)
    // listAllBooks pages through the server so a month with more releases than
    // the server's 500-row cap still renders completely (#1467). The query is
    // bounded to one month, so the full fetch stays cheap.
    api.listAllBooks({ releaseFrom: from, releaseBefore: before })
      .then(setBooks)
      .catch(console.error)
      .finally(() => setLoading(false))
  }, [viewYear, viewMonth])

  useEffect(() => {
    document.title = `${t('calendar.title')} · Bindery`
    return () => { document.title = 'Bindery' }
  }, [t])

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

  // Index books by day-of-month for the current view (timezone-safe; see
  // bucketBooksByDay).
  const booksByDay = bucketBooksByDay(books, viewYear, viewMonth)

  const daysInMonth = getDaysInMonth(viewYear, viewMonth)
  const firstDay = getFirstDayOfMonth(viewYear, viewMonth)
  const isCurrentMonth = viewYear === today.getFullYear() && viewMonth === today.getMonth()

  // Build calendar grid cells (some are empty padding)
  const cells: Array<number | null> = []
  for (let i = 0; i < firstDay; i++) cells.push(null)
  for (let i = 1; i <= daysInMonth; i++) cells.push(i)
  while (cells.length % 7 !== 0) cells.push(null)

  const hasReleases = Object.keys(booksByDay).length > 0
  const selectedDay = picked && picked.year === viewYear && picked.month === viewMonth && booksByDay[picked.day]
    ? picked.day
    : null
  const toggleDay = (day: number) =>
    setPicked(selectedDay === day ? null : { year: viewYear, month: viewMonth, day })
  const dayNames = weekdayNames(lang, 'short')
  const dayNamesNarrow = weekdayNames(lang, 'narrow')

  return (
    <div>
      <div className="flex flex-wrap items-center justify-between gap-3 mb-6">
        <h2 className="text-2xl font-bold min-w-0">{t('calendar.title')}</h2>
        <div className="flex flex-wrap items-center gap-2 w-full sm:w-auto justify-start sm:justify-end">
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
            aria-label={t('calendar.prevMonth')}
            className="px-3 py-1.5 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white bg-slate-200 dark:bg-zinc-800 hover:bg-slate-300 dark:hover:bg-zinc-700 rounded transition-colors"
          >
            ‹
          </button>
          <span className="text-sm font-medium min-w-28 sm:min-w-36 whitespace-nowrap text-center">
            {monthLabel(lang, viewYear, viewMonth, { month: 'long', year: 'numeric' })}
          </span>
          <button
            onClick={nextMonth}
            aria-label={t('calendar.nextMonth')}
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
              {dayNames.map((d, i) => (
                <div key={i} className="py-2 text-center text-xs font-medium text-slate-600 dark:text-zinc-500 uppercase tracking-wider">
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
              {dayNamesNarrow.map((d, i) => (
                <div key={i} className="py-2 text-center text-xs font-medium text-slate-600 dark:text-zinc-500">
                  {d}
                </div>
              ))}
            </div>
            <div className="grid grid-cols-7">
              {cells.map((day, idx) => {
                const isToday = isCurrentMonth && day === today.getDate()
                const count = day ? (booksByDay[day]?.length ?? 0) : 0
                const hasBooks = count > 0
                const isSelected = day !== null && day === selectedDay
                const cellCls = `aspect-square flex flex-col items-center justify-center border-b border-r border-slate-200 dark:border-zinc-800 text-xs ${
                  idx % 7 === 6 ? 'border-r-0' : ''
                } ${isSelected ? 'bg-emerald-500/15' : day ? 'bg-slate-100/50 dark:bg-zinc-900/50' : 'bg-slate-100/20 dark:bg-zinc-900/20'}`
                // A day with releases is a button: its dot used to be the only
                // sign of them, and there was nothing to tap to see which.
                if (day && hasBooks) {
                  return (
                    <button
                      key={idx}
                      type="button"
                      onClick={() => toggleDay(day)}
                      aria-pressed={isSelected}
                      aria-label={t('calendar.dayReleases', { date: dayOfMonthLabel(lang, viewYear, viewMonth, day), count })}
                      className={`${cellCls} touch-manipulation`}
                    >
                      <span className={`w-6 h-6 flex items-center justify-center rounded-full text-xs ${
                        isToday ? 'bg-emerald-600 text-white' : 'text-slate-600 dark:text-zinc-400'
                      }`}>
                        {day}
                      </span>
                      <span aria-hidden className="w-1.5 h-1.5 rounded-full bg-emerald-400 mt-0.5" />
                    </button>
                  )
                }
                return (
                  <div
                    key={idx}
                    className={cellCls}
                  >
                    {day && (
                      <>
                        <span className={`w-6 h-6 flex items-center justify-center rounded-full text-xs ${
                          isToday ? 'bg-emerald-600 text-white' : 'text-slate-600 dark:text-zinc-400'
                        }`}>
                          {day}
                        </span>
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
              <div className="flex items-center justify-between gap-3 px-4 py-2 bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
                <p className="text-xs text-slate-600 dark:text-zinc-400 font-medium" aria-live="polite">
                  {selectedDay !== null
                    ? t('calendar.releasingOn', { date: dayOfMonthLabel(lang, viewYear, viewMonth, selectedDay) })
                    : t('calendar.releasingIn', { month: monthLabel(lang, viewYear, viewMonth, { month: 'long' }), year: viewYear })}
                </p>
                {selectedDay !== null && (
                  <button
                    type="button"
                    onClick={() => setPicked(null)}
                    className="touch-target shrink-0 text-xs font-medium text-accent-text hover:underline"
                  >
                    {t('calendar.showWholeMonth')}
                  </button>
                )}
              </div>
              <div className="divide-y divide-slate-200 dark:divide-zinc-800" data-testid="calendar-agenda">
                {Object.entries(booksByDay)
                  .filter(([day]) => selectedDay === null || Number(day) === selectedDay)
                  .sort(([a], [b]) => Number(a) - Number(b))
                  .flatMap(([day, dayBooks]) =>
                    dayBooks.map(book => (
                      <div key={book.id} className="flex items-center gap-3 px-4 py-3">
                        <span className="text-xs text-slate-600 dark:text-zinc-500 w-12 flex-shrink-0">
                          {dayOfMonthLabel(lang, viewYear, viewMonth, Number(day))}
                        </span>
                        {book.imageUrl && (
                          <img loading="lazy" decoding="async" src={book.imageUrl} alt="" className="w-8 h-10 object-cover rounded flex-shrink-0" />
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
