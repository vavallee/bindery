import { BrowserRouter, Routes, Route, NavLink, Link, Navigate, useLocation, useParams } from 'react-router'
import { Fragment, lazy, Suspense, useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from './api/client'
import { AuthProvider, useAuth } from './auth/AuthContext'
import AuthGuard from './auth/AuthGuard'
import PublicOnlyRoute from './auth/PublicOnlyRoute'
import AccountMenu from './components/AccountMenu'
import ErrorBoundary from './components/ErrorBoundary'
import LibrarySearch from './components/LibrarySearch'
import Logo from './components/Logo'
import NavTabs from './components/NavTabs'
import { activeGroup, isEntryActive, matchesPath, navGroupsFor, type NavItem } from './components/navGroups'
import SetupBanner from './components/SetupBanner'
import VersionBadge from './components/VersionBadge'
import WhatsNewToast from './components/WhatsNewToast'
import { useUnmatchedCount } from './components/useUnmatchedCount'
import { REQUESTS_CHANGED_EVENT } from './pages/requests/requestLabels'
import { useTheme } from './theme'

// Route-scoped error boundary: a render crash in one page shows an inline error
// inside the content area (the nav/header stay usable) instead of bubbling to
// the root boundary, which would blank the whole app. Keyed on the path so
// navigating to another page clears the error without a reload.
function RoutedErrorBoundary({ children }: { children: ReactNode }) {
  const location = useLocation()
  return (
    <ErrorBoundary inline resetKey={location.pathname}>
      {children}
    </ErrorBoundary>
  )
}

const LoginPage = lazy(() => import('./pages/LoginPage'))
const SetupPage = lazy(() => import('./pages/SetupPage'))
const AuthorsPage = lazy(() => import('./pages/AuthorsPage'))
const AuthorDetailPage = lazy(() => import('./pages/AuthorDetailPage'))
const BooksPage = lazy(() => import('./pages/BooksPage'))
const BookDetailPage = lazy(() => import('./pages/BookDetailPage'))
const WantedPage = lazy(() => import('./pages/WantedPage'))
const QueuePage = lazy(() => import('./pages/QueuePage'))
const ImportPage = lazy(() => import('./pages/import/ImportPage'))
const SettingsPage = lazy(() => import('./pages/SettingsPage'))
const UsersPage = lazy(() => import('./pages/UsersPage'))
const HistoryPage = lazy(() => import('./pages/HistoryPage'))
const SeriesPage = lazy(() => import('./pages/SeriesPage'))
const CalendarPage = lazy(() => import('./pages/CalendarPage'))
const DiscoverPage = lazy(() => import('./pages/DiscoverPage'))
const SearchPage = lazy(() => import('./pages/SearchPage'))
const RequesterLibraryPage = lazy(() => import('./pages/requests/RequesterLibraryPage'))
const RequestSearchPage = lazy(() => import('./pages/requests/RequestSearchPage'))
const MyRequestsPage = lazy(() => import('./pages/requests/MyRequestsPage'))
const RequestsPage = lazy(() => import('./pages/requests/RequestsPage'))

// The admin nav badge: pending requests, read once on load and again after
// each approve or decline. No polling.
function usePendingRequestCount(enabled: boolean): number {
  const [count, setCount] = useState(0)
  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    const load = () => {
      api.pendingRequestCount()
        .then(r => { if (!cancelled) setCount(r.count) })
        .catch(() => { /* the badge is a hint; leave it as it was */ })
    }
    load()
    window.addEventListener(REQUESTS_CHANGED_EVENT, load)
    return () => {
      cancelled = true
      window.removeEventListener(REQUESTS_CHANGED_EVENT, load)
    }
  }, [enabled])
  return enabled ? count : 0
}

function PageLoadingFallback() {
  const { t } = useTranslation()

  return (
    <div className="py-10 text-center text-sm text-slate-600 dark:text-zinc-500">
      {t('common.loading')}
    </div>
  )
}

// Settings addresses its tabs with ?tab=, but /settings/indexers is the shape
// people type and paste. It matched no route, and with the catch-all below it
// would now render "not found" for a URL that plainly means something. Redirect
// it to the canonical query form instead. SettingsPage validates the value
// against ALL_TABS, so an unknown tab lands on General rather than breaking.
function SettingsTabRedirect() {
  const { tab } = useParams<{ tab: string }>()
  return <Navigate to={`/settings?tab=${encodeURIComponent(tab ?? '')}`} replace />
}

// Catch-all. Without it an unrouted path rendered the header and nav around an
// empty <main>, which reads as a broken app rather than a wrong address. That is
// the failure /authors used to have (see the comment on its alias below).
function NotFoundPage() {
  const { t } = useTranslation()
  const location = useLocation()

  return (
    <div className="py-16 text-center">
      <p className="text-lg font-semibold text-slate-800 dark:text-zinc-200">
        {t('notFound.title', 'Page not found')}
      </p>
      <p className="mt-2 text-sm text-fg-muted">
        {t('notFound.body', 'There is nothing at {{path}}.', { path: location.pathname })}
      </p>
      <Link
        to="/"
        className="inline-block mt-6 px-4 py-2 rounded bg-emerald-600 hover:bg-emerald-500 text-white text-sm font-medium"
      >
        {t('notFound.home', 'Go to Authors')}
      </Link>
    </div>
  )
}

function Shell() {
  useTheme() // ensures dark class is applied on every mount, not only when Settings is visited
  const { t } = useTranslation()
  const [version, setVersion] = useState('')
  const [latestVersion, setLatestVersion] = useState<string | undefined>(undefined)
  const [menuOpen, setMenuOpen] = useState(false)
  const { status, logout, isAdmin, isRequester } = useAuth()
  const signedIn = !!status?.authenticated && status.mode !== 'disabled'
  // Books the library scan could not match, on the Import nav entry (admins
  // only; the count comes from an admin only route).
  const unmatched = useUnmatchedCount(isAdmin)
  const navEntries = navGroupsFor(isAdmin, isRequester)
  const pendingRequests = usePendingRequestCount(isAdmin)
  const { pathname } = useLocation()
  // The group whose tab strip belongs above the current page, if any.
  const tabGroup = activeGroup(pathname, navEntries)

  useEffect(() => {
    // /system/status is closed to requesters, and only admins see the version.
    if (isRequester) return
    api.status().then(s => {
      setVersion(s.version)
      setLatestVersion(s.latestVersion)
    }).catch(() => {})
  }, [isRequester])

  // Both count badges survive the regrouping: Import is still a top level link
  // and keeps its own, and the admin pending count rides the Activity entry as
  // well as the Requests tab inside it.
  const navLabel = (item: NavItem) => (
    <>
      {t(`nav.${item.key}`)}
      {item.key === 'import' && unmatched > 0 && (
        <span
          className="ml-1.5 inline-flex min-w-[1.25rem] items-center justify-center rounded-full bg-amber-100 px-1.5 text-[11px] font-semibold text-amber-800 dark:bg-amber-950 dark:text-amber-300"
          aria-label={t('nav.importUnmatched', { count: unmatched, defaultValue: '{{count}} books need a decision' })}
        >
          {unmatched > 999 ? '999+' : unmatched}
        </span>
      )}
      {(item.key === 'requests' || item.key === 'activity') && pendingRequests > 0 && (
        <span className="ml-1.5 px-1.5 py-0.5 rounded-full bg-amber-500 text-white text-[10px] font-semibold" aria-label={t('nav.requestsPending', { count: pendingRequests })}>
          {pendingRequests}
        </span>
      )}
    </>
  )

  const linkClass = ({ isActive }: { isActive: boolean }) =>
    `px-2.5 py-2 rounded-md text-sm font-medium whitespace-nowrap transition-colors ${
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
          <div className="flex items-center justify-between gap-4 h-16">
            <Link to="/" className="flex items-center gap-2 flex-shrink-0 group" onClick={() => setMenuOpen(false)}>
              <Logo className="w-14 h-14 rounded-full transition-transform group-hover:scale-105" />
              <h1 className="text-lg font-bold tracking-tight">Bindery</h1>
            </Link>

            {/* A group link goes to its first member and stays lit on every
                member, so the bar shows where you are without listing ten
                pages. Plain Link plus a computed class: NavLink only knows
                about its own path. */}
            <nav className="hidden xl:flex gap-1">
              {navEntries.map(item => (
                <Link key={item.to} to={item.to} className={linkClass({ isActive: isEntryActive(pathname, item) })}>
                  {navLabel(item)}
                </Link>
              ))}
            </nav>

            <div className="flex items-center gap-2 flex-shrink-0">
              {!isRequester && <LibrarySearch className="hidden lg:block w-40" />}
              {!isRequester && <NavLink
                to="/search"
                className={({ isActive }) =>
                  `hidden lg:block p-2 rounded-md transition-colors ${
                    isActive ? 'bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'
                  }`
                }
                title={t('nav.search')}
              >
                <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24" strokeWidth={2}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-5.197-5.197m0 0A7.5 7.5 0 1 0 5.196 5.196a7.5 7.5 0 0 0 10.607 10.607Z" />
                </svg>
              </NavLink>}
              {isAdmin && (
                <NavLink
                  to="/users"
                  className={({ isActive }) =>
                    `hidden lg:block p-2 rounded-md transition-colors ${
                      isActive ? 'bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'
                    }`
                  }
                  title={t('nav.users')}
                >
                  <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24" strokeWidth={2}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M15 19.128a9.38 9.38 0 0 0 2.625.372 9.337 9.337 0 0 0 4.121-.952 4.125 4.125 0 0 0-7.533-2.493M15 19.128v-.003c0-1.113-.285-2.16-.786-3.07M15 19.128v.106A12.318 12.318 0 0 1 8.624 21c-2.331 0-4.512-.645-6.374-1.766l-.001-.109a6.375 6.375 0 0 1 11.964-3.07M12 6.375a3.375 3.375 0 1 1-6.75 0 3.375 3.375 0 0 1 6.75 0Zm8.25 2.25a2.625 2.625 0 1 1-5.25 0 2.625 2.625 0 0 1 5.25 0Z" />
                  </svg>
                </NavLink>
              )}
              {!isRequester && <NavLink
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
              </NavLink>}
              {(signedIn || (isAdmin && version)) && (
                <AccountMenu
                  className="hidden lg:block"
                  username={signedIn ? status?.username : undefined}
                  version={isAdmin && version ? version : undefined}
                  latestVersion={latestVersion}
                  onSignOut={signedIn ? logout : undefined}
                />
              )}
              <button
                onClick={() => setMenuOpen(open => !open)}
                className="xl:hidden p-2 rounded-md text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200 dark:hover:bg-zinc-800 transition-colors"
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
          <div className="xl:hidden border-t border-slate-200 dark:border-zinc-800">
            {/* From lg up the search, the icons and the account menu stay in
                the header row, so the menu only carries the nav links. */}
            {!isRequester && <div className="lg:hidden px-4 py-3 border-b border-slate-200/50 dark:border-zinc-800/50">
              <LibrarySearch className="w-full" onNavigate={() => setMenuOpen(false)} />
            </div>}
            {/* The menu keeps every page reachable: a group heads its own
                block and its members are listed indented beneath it. */}
            <nav>
              {navEntries.map(item => (
                <Fragment key={item.to}>
                  <Link
                    to={item.to}
                    className={mobileLinkClass({ isActive: isEntryActive(pathname, item) })}
                    onClick={() => setMenuOpen(false)}
                  >
                    {navLabel(item)}
                  </Link>
                  {item.children?.map(child => (
                    <Link
                      key={child.to}
                      to={child.to}
                      className={`pl-8 ${mobileLinkClass({ isActive: matchesPath(pathname, child) })}`}
                      onClick={() => setMenuOpen(false)}
                    >
                      {navLabel(child)}
                    </Link>
                  ))}
                </Fragment>
              ))}
              {!isRequester && <NavLink
                to="/search"
                className={args => `lg:hidden ${mobileLinkClass(args)}`}
                onClick={() => setMenuOpen(false)}
              >
                {t('nav.search')}
              </NavLink>}
              {isAdmin && (
                <NavLink
                  to="/users"
                  className={args => `lg:hidden ${mobileLinkClass(args)}`}
                  onClick={() => setMenuOpen(false)}
                >
                  {t('nav.users')}
                </NavLink>
              )}
              {!isRequester && <NavLink
                to="/settings"
                className={args => `lg:hidden ${mobileLinkClass(args)}`}
                onClick={() => setMenuOpen(false)}
              >
                {t('nav.settings')}
              </NavLink>}
            </nav>
            <div className="lg:hidden flex items-center justify-between gap-3 px-4 py-2 border-t border-slate-200 dark:border-zinc-800">
              {signedIn && status?.username && (
                <span className="text-xs text-fg-muted truncate">{t('login.signedInAs')} {status.username}</span>
              )}
              {isAdmin && version && (
                <VersionBadge version={version} latestVersion={latestVersion} />
              )}
              {signedIn && (
                <button
                  onClick={logout}
                  className="text-xs text-fg-muted hover:text-slate-900 dark:hover:text-white transition-colors"
                >
                  {t('login.signOut')}
                </button>
              )}
            </div>
          </div>
        )}
      </header>

      {isAdmin && <SetupBanner />}
      <WhatsNewToast version={version} />

      <main className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-6">
        {tabGroup && <NavTabs group={tabGroup} renderLabel={navLabel} />}
        <Suspense fallback={<PageLoadingFallback />}>
          <RoutedErrorBoundary>
          {isRequester ? (
          <Routes>
            <Route path="/" element={<RequesterLibraryPage />} />
            <Route path="/request" element={<RequestSearchPage />} />
            <Route path="/my-requests" element={<MyRequestsPage />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
          ) : (
          <Routes>
            <Route path="/" element={<AuthorsPage />} />
            {/* Authors has always been served from "/" because it was the first
                page that existed; every nav entry added since (/books, /import,
                /settings, ...) got a real path. "/authors" therefore matched
                nothing, and with no catch-all route it rendered the chrome
                around an empty <main> — a blank page rather than a 404. Alias it
                to the canonical "/" the same way /blocklist aliases into
                settings, so a guessed or bookmarked URL lands somewhere. */}
            <Route path="/authors" element={<Navigate to="/" replace />} />
            <Route path="/author/:id" element={<AuthorDetailPage />} />
            <Route path="/books" element={<BooksPage />} />
            <Route path="/book/:id" element={<BookDetailPage />} />
            <Route path="/wanted" element={<WantedPage />} />
            <Route path="/queue" element={<QueuePage />} />
            <Route path="/import" element={<ImportPage />} />
            <Route path="/history" element={<HistoryPage />} />
            <Route path="/series" element={<SeriesPage />} />
            <Route path="/calendar" element={<CalendarPage />} />
            <Route path="/discover" element={<DiscoverPage />} />
            <Route path="/search" element={<SearchPage />} />
            <Route path="/blocklist" element={<Navigate to="/settings?tab=blocklist" replace />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="/settings/:tab" element={<SettingsTabRedirect />} />
            {isAdmin && <Route path="/users" element={<UsersPage />} />}
            {isAdmin && <Route path="/requests" element={<RequestsPage />} />}
            <Route path="*" element={<NotFoundPage />} />
          </Routes>
          )}
          </RoutedErrorBoundary>
        </Suspense>
      </main>

      <footer className="border-t border-slate-200 dark:border-zinc-800 mt-8">
        <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-4 flex items-center justify-center gap-2">
          {isAdmin && (
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
          )}
        </div>
      </footer>
    </div>
  )
}

// Read the path prefix injected by the backend at serve time. Empty string
// means the app is mounted at the root (default / existing behaviour).
const binderyBase: string =
  (window as unknown as { __BINDERY_BASE__?: string }).__BINDERY_BASE__ ?? ''

function App() {
  return (
    <BrowserRouter basename={binderyBase}>
      <AuthProvider>
        <Suspense fallback={<PageLoadingFallback />}>
          <Routes>
            <Route
              path="/login"
              element={
                <PublicOnlyRoute mode="login">
                  <LoginPage />
                </PublicOnlyRoute>
              }
            />
            <Route
              path="/setup"
              element={
                <PublicOnlyRoute mode="setup">
                  <SetupPage />
                </PublicOnlyRoute>
              }
            />
            <Route
              path="/*"
              element={
                <AuthGuard>
                  <Shell />
                </AuthGuard>
              }
            />
          </Routes>
        </Suspense>
      </AuthProvider>
    </BrowserRouter>
  )
}

export default App
