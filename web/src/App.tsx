import { BrowserRouter, Routes, Route, NavLink, Link, Navigate } from 'react-router-dom'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from './api/client'
import { AuthProvider, useAuth } from './auth/AuthContext'
import AuthGuard from './auth/AuthGuard'
import LoginPage from './pages/LoginPage'
import SetupPage from './pages/SetupPage'
import AuthorsPage from './pages/AuthorsPage'
import AuthorDetailPage from './pages/AuthorDetailPage'
import BooksPage from './pages/BooksPage'
import BookDetailPage from './pages/BookDetailPage'
import WantedPage from './pages/WantedPage'
import QueuePage from './pages/QueuePage'
import SettingsPage from './pages/SettingsPage'
import HistoryPage from './pages/HistoryPage'
import SeriesPage from './pages/SeriesPage'
import CalendarPage from './pages/CalendarPage'
import DiscoverPage from './pages/DiscoverPage'
import SearchPage from './pages/SearchPage'
import Logo from './components/Logo'
import { useTheme } from './theme'

const NAV_KEYS = [
  { to: '/', key: 'authors', end: true },
  { to: '/books', key: 'books' },
  { to: '/wanted', key: 'wanted' },
  { to: '/queue', key: 'queue' },
  { to: '/history', key: 'history' },
  { to: '/series', key: 'series' },
  { to: '/calendar', key: 'calendar' },
  { to: '/discover', key: 'discover' },
  { to: '/search', key: 'search' },
]

function Shell() {
  useTheme() // ensures dark class is applied on every mount, not only when Settings is visited
  const { t } = useTranslation()
  const [version, setVersion] = useState('')
  const [menuOpen, setMenuOpen] = useState(false)
  const { status, logout } = useAuth()

  useEffect(() => {
    api.status().then(s => setVersion(s.version)).catch(() => {})
  }, [])

  const linkClass = ({ isActive }: { isActive: boolean }) =>
    `px-3 py-2 rounded-md text-sm font-medium transition-colors ${
      isActive ? 'bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'
    }`

  const mobileLinkClass = ({ isActive }: { isActive: boolean }) =>
    `block px-4 py-3 text-sm font-medium transition-colors border-b border-slate-200/50 dark:border-zinc-800/50 ${
      isActive ? 'bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'
    }`

  return (
    <div className="min-h-screen bg-slate-50 dark:bg-zinc-950 text-slate-900 dark:text-zinc-100">
      <header className="border-b border-slate-200 dark:border-zinc-800 sticky top-0 z-40 bg-slate-50 dark:bg-zinc-950">
        <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
          <div className="flex items-center justify-between h-16">
            <Link to="/" className="flex items-center gap-2 flex-shrink-0 group" onClick={() => setMenuOpen(false)}>
              <Logo className="w-14 h-14 rounded-full transition-transform group-hover:scale-105" />
              <h1 className="text-lg font-bold tracking-tight">Bindery</h1>
            </Link>

            <nav className="hidden lg:flex gap-1">
              {NAV_KEYS.map(item => (
                <NavLink key={item.to} to={item.to} end={item.end} className={linkClass}>
                  {t(`nav.${item.key}`)}
                </NavLink>
              ))}
            </nav>

            <div className="flex items-center gap-3 flex-shrink-0">
              <NavLink
                to="/settings"
                className={({ isActive }) =>
                  `hidden lg:block p-2 rounded-md transition-colors ${
                    isActive ? 'bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'
                  }`
                }
                title={t('nav.settings')}
              >
                <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24" strokeWidth={2}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.325.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 0 1 1.37.49l1.296 2.247a1.125 1.125 0 0 1-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a7.723 7.723 0 0 1 0 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.43l-1.298 2.248a1.125 1.125 0 0 1-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.47 6.47 0 0 1-.22.128c-.331.183-.581.495-.644.869l-.213 1.281c-.09.543-.56.941-1.11.941h-2.594c-.55 0-1.019-.398-1.11-.94l-.213-1.281c-.062-.374-.312-.686-.644-.87a6.52 6.52 0 0 1-.22-.127c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 0 1-1.369-.49l-1.297-2.247a1.125 1.125 0 0 1 .26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 0 1 0-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 0 1-.26-1.43l1.297-2.247a1.125 1.125 0 0 1 1.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.086.22-.128.332-.183.582-.495.644-.869l.214-1.28Z" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z" />
                </svg>
              </NavLink>
              {version && (
                /^\d+\.\d+/.test(version) ? (
                  <a
                    href={`https://github.com/vavallee/bindery/releases/tag/v${version}`}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="hidden lg:block text-xs text-slate-500 dark:text-zinc-600 hover:underline whitespace-nowrap"
                  >
                    {`v${version}`}
                  </a>
                ) : (
                  <span className="hidden lg:block text-xs text-slate-500 dark:text-zinc-600 whitespace-nowrap">{version}</span>
                )
              )}
              {status?.authenticated && status.mode !== 'disabled' && (
                <button
                  onClick={logout}
                  className="hidden lg:block text-xs text-slate-500 dark:text-zinc-500 hover:text-slate-900 dark:hover:text-white transition-colors"
                  title={status.username ? `${t('login.signedInAs')} ${status.username}` : t('login.signOut')}
                >
                  {t('login.signOut')}
                </button>
              )}
              <button
                onClick={() => setMenuOpen(open => !open)}
                className="lg:hidden p-2 rounded-md text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200 dark:hover:bg-zinc-800 transition-colors"
                aria-label="Toggle menu"
              >
                {menuOpen ? (
                  <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
                  </svg>
                ) : (
                  <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 6h16M4 12h16M4 18h16" />
                  </svg>
                )}
              </button>
            </div>
          </div>
        </div>

        {menuOpen && (
          <div className="lg:hidden border-t border-slate-200 dark:border-zinc-800">
            <nav>
              {NAV_KEYS.map(item => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={item.end}
                  className={mobileLinkClass}
                  onClick={() => setMenuOpen(false)}
                >
                  {t(`nav.${item.key}`)}
                </NavLink>
              ))}
              <NavLink
                to="/settings"
                className={mobileLinkClass}
                onClick={() => setMenuOpen(false)}
              >
                {t('nav.settings')}
              </NavLink>
            </nav>
            <div className="flex items-center justify-between px-4 py-2 border-t border-slate-200 dark:border-zinc-800">
              {version && (
                /^\d+\.\d+/.test(version) ? (
                  <a
                    href={`https://github.com/vavallee/bindery/releases/tag/v${version}`}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-xs text-slate-500 dark:text-zinc-600 hover:underline"
                  >
                    {`v${version}`}
                  </a>
                ) : (
                  <span className="text-xs text-slate-500 dark:text-zinc-600">{version}</span>
                )
              )}
              {status?.authenticated && status.mode !== 'disabled' && (
                <button
                  onClick={logout}
                  className="text-xs text-slate-500 dark:text-zinc-500 hover:text-slate-900 dark:hover:text-white transition-colors"
                >
                  {t('login.signOut')}
                </button>
              )}
            </div>
          </div>
        )}
      </header>

      <main className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-6">
        <Routes>
          <Route path="/" element={<AuthorsPage />} />
          <Route path="/author/:id" element={<AuthorDetailPage />} />
          <Route path="/books" element={<BooksPage />} />
          <Route path="/book/:id" element={<BookDetailPage />} />
          <Route path="/wanted" element={<WantedPage />} />
          <Route path="/queue" element={<QueuePage />} />
          <Route path="/history" element={<HistoryPage />} />
          <Route path="/series" element={<SeriesPage />} />
          <Route path="/calendar" element={<CalendarPage />} />
          <Route path="/discover" element={<DiscoverPage />} />
          <Route path="/search" element={<SearchPage />} />
          <Route path="/blocklist" element={<Navigate to="/settings" replace />} />
          <Route path="/settings" element={<SettingsPage />} />
        </Routes>
      </main>

      <footer className="border-t border-slate-200 dark:border-zinc-800 mt-8">
        <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-4 flex items-center justify-center gap-2">
          <a
            href="https://github.com/vavallee/bindery"
            target="_blank"
            rel="noopener noreferrer"
            className="flex items-center gap-2 text-slate-500 dark:text-zinc-600 hover:text-slate-700 dark:hover:text-zinc-300 transition-colors text-xs"
          >
            <svg className="w-4 h-4" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
              <path d="M12 2C6.477 2 2 6.484 2 12.017c0 4.425 2.865 8.18 6.839 9.504.5.092.682-.217.682-.483 0-.237-.008-.868-.013-1.703-2.782.605-3.369-1.343-3.369-1.343-.454-1.158-1.11-1.466-1.11-1.466-.908-.62.069-.608.069-.608 1.003.07 1.531 1.032 1.531 1.032.892 1.53 2.341 1.088 2.91.832.092-.647.35-1.088.636-1.338-2.22-.253-4.555-1.113-4.555-4.951 0-1.093.39-1.988 1.029-2.688-.103-.253-.446-1.272.098-2.65 0 0 .84-.27 2.75 1.026A9.564 9.564 0 0112 6.844c.85.004 1.705.115 2.504.337 1.909-1.296 2.747-1.027 2.747-1.027.546 1.379.202 2.398.1 2.651.64.7 1.028 1.595 1.028 2.688 0 3.848-2.339 4.695-4.566 4.943.359.309.678.92.678 1.855 0 1.338-.012 2.419-.012 2.747 0 .268.18.58.688.482A10.019 10.019 0 0022 12.017C22 6.484 17.522 2 12 2z" />
            </svg>
            vavallee/bindery
          </a>
        </div>
      </footer>
    </div>
  )
}

function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route path="/setup" element={<SetupPage />} />
          <Route
            path="/*"
            element={
              <AuthGuard>
                <Shell />
              </AuthGuard>
            }
          />
        </Routes>
      </AuthProvider>
    </BrowserRouter>
  )
}

export default App
