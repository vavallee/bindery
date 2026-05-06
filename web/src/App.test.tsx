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
  const { Navigate } = await vi.importActual<typeof import('react-router-dom')>('react-router-dom')
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
  },
}))

vi.mock('./theme', () => ({ useTheme: () => {} }))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => {
      const m: Record<string, string> = {
        'nav.authors': 'Authors', 'nav.books': 'Books', 'nav.wanted': 'Wanted',
        'nav.queue': 'Queue', 'nav.history': 'History', 'nav.series': 'Series',
        'nav.calendar': 'Calendar', 'nav.discover': 'Discover', 'nav.settings': 'Settings',
        'nav.search': 'Search',
        'login.signOut': 'Sign out', 'login.signedInAs': 'Signed in as',
      }
      return m[key] ?? key
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
  it('redirects authenticated users away from the login route', () => {
    authState.value = {
      status: { authenticated: true, setupRequired: false, mode: 'enabled' },
      logout: logoutMock,
      isAdmin: true,
    }
    window.history.pushState(null, '', '/login')

    renderShell()

    expect(screen.queryByTestId('page-login')).not.toBeInTheDocument()
    expect(screen.getByTestId('page-authors')).toBeInTheDocument()
    expect(window.location.pathname).toBe('/')
  })
})

describe('Shell — desktop navigation', () => {
  it('renders all 8 nav links in the desktop nav bar', () => {
    renderShell()
    const desktopNav = document.querySelector('nav.hidden.lg\\:flex')
    expect(desktopNav).not.toBeNull()
    const links = desktopNav!.querySelectorAll('a')
    expect(links.length).toBe(8)
    const labels = Array.from(links).map(l => l.textContent)
    expect(labels).toContain('Authors')
    expect(labels).toContain('Books')
    expect(labels).toContain('Wanted')
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
    expect(links).toContain('Discover')
    expect(links).toContain('Search')
    expect(links).toContain('Settings')
    expect(links.length).toBe(10) // 8 main + Search + Settings
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
