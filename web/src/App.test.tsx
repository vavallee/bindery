import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import App from './App'

const { authState, logoutMock } = vi.hoisted(() => ({
  authState: {
    value: {
      status: { authenticated: false, mode: 'disabled', setupRequired: false },
      logout: vi.fn(),
      isAdmin: false,
    },
  },
  logoutMock: vi.fn(),
}))

// Mock all heavy page components so we only exercise Shell layout.
vi.mock('./pages/AuthorsPage', () => ({ default: () => <div data-testid="page-authors" /> }))
vi.mock('./pages/BooksPage', () => ({ default: () => <div data-testid="page-books" /> }))
vi.mock('./pages/WantedPage', () => ({ default: () => <div data-testid="page-wanted" /> }))
vi.mock('./pages/QueuePage', () => ({ default: () => <div data-testid="page-queue" /> }))
vi.mock('./pages/HistoryPage', () => ({ default: () => <div data-testid="page-history" /> }))
vi.mock('./pages/SeriesPage', () => ({ default: () => <div data-testid="page-series" /> }))
vi.mock('./pages/CalendarPage', () => ({ default: () => <div data-testid="page-calendar" /> }))
vi.mock('./pages/DiscoverPage', () => ({ default: () => <div data-testid="page-discover" /> }))
vi.mock('./pages/SettingsPage', () => ({ default: () => <div data-testid="page-settings" /> }))
vi.mock('./pages/LoginPage', async () => {
  const { Navigate } = await vi.importActual<typeof import('react-router')>('react-router')
  return {
    default: () => {
      const status = authState.value.status
      if (status?.setupRequired) return <Navigate to="/setup" replace />
      if (status?.authenticated) return <Navigate to="/" replace />
      return <div data-testid="page-login" />
    },
  }
})
vi.mock('./pages/SetupPage', () => ({ default: () => <div data-testid="page-setup" /> }))
vi.mock('./pages/AuthorDetailPage', () => ({ default: () => <div /> }))
vi.mock('./pages/BookDetailPage', () => ({ default: () => <div /> }))

vi.mock('./auth/AuthGuard', () => ({ default: ({ children }: { children: React.ReactNode }) => <>{children}</> }))
vi.mock('./auth/AuthContext', () => ({
  AuthProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  useAuth: () => authState.value,
}))

vi.mock('./api/client', () => ({
  api: {
    status: vi.fn().mockResolvedValue({ version: '0.15.0', commit: 'abc', buildDate: '' }),
    // SetupBanner (mounted in the admin shell) probes these on mount.
    listIndexers: vi.fn().mockResolvedValue([]),
    listDownloadClients: vi.fn().mockResolvedValue([]),
  },
}))

vi.mock('./theme', () => ({ useTheme: () => {} }))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    // Resolves like real i18next does: the label table wins, then the inline
    // default the caller passed, then the bare key. Honouring the default
    // matters because components written with t(key, 'Fallback') would
    // otherwise render their key here and read as missing.
    t: (key: string, fallback?: unknown, vars?: Record<string, unknown>) => {
      const m: Record<string, string> = {
        'nav.authors': 'Authors', 'nav.books': 'Books', 'nav.wanted': 'Wanted',
        'nav.import': 'Import',
        'nav.queue': 'Queue', 'nav.history': 'History', 'nav.series': 'Series',
        'nav.calendar': 'Calendar', 'nav.discover': 'Discover', 'nav.settings': 'Settings',
        'nav.search': 'Search',
        'login.signOut': 'Sign out', 'login.signedInAs': 'Signed in as',
      }
      if (m[key]) return m[key]
      if (typeof fallback !== 'string') return key
      const interpolations = (vars ?? (typeof fallback === 'string' ? undefined : fallback)) as Record<string, unknown> | undefined
      return interpolations
        ? fallback.replace(/\{\{(\w+)\}\}/g, (_m, name: string) => String(interpolations[name] ?? ''))
        : fallback
    },
  }),
}))

function renderShell() {
  return render(<App />)
}

beforeEach(() => {
  vi.clearAllMocks()
  logoutMock.mockReset()
  authState.value = {
    status: { authenticated: false, mode: 'disabled', setupRequired: false },
    logout: logoutMock,
    isAdmin: false,
  }
  window.history.pushState(null, '', '/')
})

describe('App auth routes', () => {
  it('redirects authenticated users away from the login route', async () => {
    authState.value = {
      status: { authenticated: true, setupRequired: false, mode: 'enabled' },
      logout: logoutMock,
      isAdmin: true,
    }
    window.history.pushState(null, '', '/login')

    renderShell()

    expect(screen.queryByTestId('page-login')).not.toBeInTheDocument()
    expect(await screen.findByTestId('page-authors')).toBeInTheDocument()
    expect(window.location.pathname).toBe('/')
  })
})

describe('App — /authors alias', () => {
  // Authors is served from "/" because it was the first page that existed.
  // Every nav entry added later got a real path, so "/authors" matched no route
  // — and with no catch-all it rendered the chrome around an empty <main>, a
  // blank page rather than a 404. Reported by a user who guessed the URL from
  // the pattern the other pages follow.
  it('lands on the authors page instead of rendering nothing', async () => {
    window.history.pushState(null, '', '/authors')

    renderShell()

    expect(await screen.findByTestId('page-authors')).toBeInTheDocument()
    expect(window.location.pathname).toBe('/')
  })

  // The alias is still the right answer for /authors specifically, because it
  // means something. Everything else now lands on the catch-all below.
  it('does not send an unrouted path to the authors page', () => {
    window.history.pushState(null, '', '/definitely-not-a-route')

    renderShell()

    expect(screen.queryByTestId('page-authors')).not.toBeInTheDocument()
    expect(screen.queryByTestId('page-books')).not.toBeInTheDocument()
  })
})

describe('App — catch-all route', () => {
  // Before the catch-all, an unrouted path rendered the header and nav around
  // an empty <main>. That reads as a broken app rather than a wrong address,
  // which is exactly how the /authors report arrived.
  it('says the page was not found instead of rendering an empty main', () => {
    window.history.pushState(null, '', '/definitely-not-a-route')

    renderShell()

    expect(screen.getByText('Page not found')).toBeInTheDocument()
    expect(screen.getByText(/definitely-not-a-route/)).toBeInTheDocument()
  })

  it('offers a way back rather than stranding the user', () => {
    window.history.pushState(null, '', '/nope')

    renderShell()

    expect(screen.getByRole('link', { name: 'Go to Authors' })).toHaveAttribute('href', '/')
  })
})

describe('App — /settings/:tab deep links', () => {
  // Settings addresses tabs with ?tab=, but /settings/indexers is the shape
  // people type and share. It matched no route, so before the catch-all it was
  // a blank page and after it would have been a 404 for a URL that plainly
  // means something.
  it('redirects the path form to the canonical query form', async () => {
    window.history.pushState(null, '', '/settings/indexers')

    renderShell()

    expect(await screen.findByTestId('page-settings')).toBeInTheDocument()
    expect(window.location.pathname).toBe('/settings')
    expect(window.location.search).toBe('?tab=indexers')
  })

  // SettingsPage validates the value against ALL_TABS and falls back to
  // General, so an unknown tab must still reach Settings rather than the 404.
  it('still reaches settings for an unrecognised tab', async () => {
    window.history.pushState(null, '', '/settings/not-a-tab')

    renderShell()

    expect(await screen.findByTestId('page-settings')).toBeInTheDocument()
    expect(screen.queryByText('Page not found')).not.toBeInTheDocument()
  })
})

describe('Shell — desktop navigation', () => {
  it('renders all 9 nav links in the desktop nav bar', () => {
    renderShell()
    const desktopNav = document.querySelector('nav.hidden.lg\\:flex')
    expect(desktopNav).not.toBeNull()
    const links = desktopNav!.querySelectorAll('a')
    expect(links.length).toBe(9)
    const labels = Array.from(links).map(l => l.textContent)
    expect(labels).toContain('Authors')
    expect(labels).toContain('Books')
    expect(labels).toContain('Wanted')
    expect(labels).toContain('Import')
    expect(labels).toContain('Discover')
    expect(labels).toContain('Calendar')
  })

  it('desktop nav has hidden lg:flex classes for responsive visibility', () => {
    renderShell()
    const nav = document.querySelector('nav.hidden.lg\\:flex')
    expect(nav).not.toBeNull()
    expect(nav!.className).toContain('hidden')
    expect(nav!.className).toContain('lg:flex')
  })

  it('settings gear icon is in the desktop header (hidden on mobile)', () => {
    renderShell()
    const settingsLink = document.querySelector('a[title="Settings"].hidden.lg\\:block')
    expect(settingsLink).not.toBeNull()
  })
})

describe('Shell — mobile navigation', () => {
  it('renders a hamburger toggle button for mobile', () => {
    renderShell()
    expect(screen.getByRole('button', { name: /toggle menu/i })).toBeInTheDocument()
  })

  it('hamburger button has lg:hidden class', () => {
    renderShell()
    const btn = screen.getByRole('button', { name: /toggle menu/i })
    expect(btn.className).toContain('lg:hidden')
  })

  it('mobile menu is hidden by default', () => {
    renderShell()
    expect(document.querySelector('div.lg\\:hidden > nav')).toBeNull()
  })

  it('opens mobile menu when hamburger is clicked', () => {
    renderShell()
    fireEvent.click(screen.getByRole('button', { name: /toggle menu/i }))
    const mobileNav = document.querySelector('div.lg\\:hidden > nav')
    expect(mobileNav).not.toBeNull()
  })

  it('mobile menu contains all nav links including Settings', () => {
    renderShell()
    fireEvent.click(screen.getByRole('button', { name: /toggle menu/i }))
    const mobileNav = document.querySelector('div.lg\\:hidden > nav')!
    const links = Array.from(mobileNav.querySelectorAll('a')).map(l => l.textContent)
    expect(links).toContain('Authors')
    expect(links).toContain('Import')
    expect(links).toContain('Discover')
    expect(links).toContain('Search')
    expect(links).toContain('Settings')
    expect(links.length).toBe(11) // 9 main + Search + Settings
  })

  it('closes mobile menu when a nav link is clicked', () => {
    renderShell()
    fireEvent.click(screen.getByRole('button', { name: /toggle menu/i }))
    const mobileNav = document.querySelector('div.lg\\:hidden > nav')!
    expect(mobileNav).not.toBeNull()

    fireEvent.click(mobileNav.querySelector('a')!)
    expect(document.querySelector('div.lg\\:hidden > nav')).toBeNull()
  })

  it('toggles hamburger icon between open/close SVG paths', () => {
    renderShell()
    const btn = screen.getByRole('button', { name: /toggle menu/i })

    // Before open: shows "hamburger" path (three horizontal lines)
    expect(btn.innerHTML).toContain('M4 6h16M4 12h16M4 18h16')

    fireEvent.click(btn)
    // After open: shows "X" path
    expect(btn.innerHTML).toContain('M6 18L18 6M6 6l12 12')

    fireEvent.click(btn)
    // Closed again: back to hamburger
    expect(btn.innerHTML).toContain('M4 6h16M4 12h16M4 18h16')
  })
})
