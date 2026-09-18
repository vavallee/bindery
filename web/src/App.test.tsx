import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, within } from '@testing-library/react'
import App from './App'
import { api } from './api/client'

const { authState, logoutMock } = vi.hoisted(() => ({
  authState: {
    value: {
      status: { authenticated: false, mode: 'disabled', setupRequired: false },
      logout: vi.fn(),
      isAdmin: false,
    } as {
      status: { authenticated: boolean; mode: string; setupRequired: boolean; role?: string; username?: string }
      logout: () => void
      isAdmin: boolean
      isRequester?: boolean
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
vi.mock('./pages/requests/RequesterLibraryPage', () => ({ default: () => <div data-testid="page-requester-library" /> }))
vi.mock('./pages/requests/RequestSearchPage', () => ({ default: () => <div data-testid="page-request-search" /> }))
vi.mock('./pages/requests/MyRequestsPage', () => ({ default: () => <div data-testid="page-my-requests" /> }))
vi.mock('./pages/requests/RequestsPage', () => ({ default: () => <div data-testid="page-requests" /> }))

vi.mock('./auth/AuthGuard', () => ({ default: ({ children }: { children: React.ReactNode }) => <>{children}</> }))
vi.mock('./auth/AuthContext', () => ({
  AuthProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  useAuth: () => ({ isRequester: false, ...authState.value }),
  useIsRequester: () => authState.value.isRequester ?? false,
}))

vi.mock('./api/client', () => ({
  api: {
    status: vi.fn().mockResolvedValue({ version: '0.15.0', commit: 'abc', buildDate: '' }),
    // SetupBanner (mounted in the admin shell) probes these on mount.
    listIndexers: vi.fn().mockResolvedValue([]),
    listDownloadClients: vi.fn().mockResolvedValue([]),
    // The Import nav badge reads this once for an admin.
    unmatchedSummary: vi.fn().mockResolvedValue({ pending: 0, pendingFiles: 0, ignored: 0, adopted: 0, scan: {} }),
    pendingRequestCount: vi.fn().mockResolvedValue({ count: 3 }),
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
        'nav.requesterLibrary': 'Library', 'nav.request': 'Request', 'nav.myRequests': 'My requests',
        'nav.requests': 'Requests', 'nav.users': 'Users',
        'nav.library': 'Library', 'nav.activity': 'Activity',
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
  // The bar used to list every page, ten links with two count badges beside a
  // search box and four icons. Authors, Books and Series are now Library and
  // Wanted, Queue and History are Activity, each with a tab strip on its pages.
  it('renders the five grouped nav links in the desktop nav bar', () => {
    renderShell()
    const desktopNav = document.querySelector('nav.hidden.xl\\:flex')
    expect(desktopNav).not.toBeNull()
    const links = Array.from(desktopNav!.querySelectorAll('a'))
    expect(links.map(l => l.textContent)).toEqual(['Library', 'Activity', 'Import', 'Calendar', 'Discover'])
    expect(links.map(l => l.getAttribute('href'))).toEqual(['/', '/wanted', '/import', '/calendar', '/discover'])
  })

  it('marks Library active on the pages it groups, not only on its own href', () => {
    // The active class is the bare bg-slate-200; an inactive link still
    // carries hover:bg-slate-200/50, so match the class list, not a substring.
    const isLit = (label: string) => {
      const desktopNav = document.querySelector('nav.hidden.xl\\:flex')!
      const link = Array.from(desktopNav.querySelectorAll('a')).find(a => a.textContent === label)!
      return link.className.split(' ').includes('bg-slate-200')
    }

    window.history.pushState(null, '', '/books')
    const books = renderShell()
    expect(isLit('Library')).toBe(true)
    expect(isLit('Activity')).toBe(false)
    books.unmount()

    window.history.pushState(null, '', '/series')
    const series = renderShell()
    expect(isLit('Library')).toBe(true)
    series.unmount()

    window.history.pushState(null, '', '/queue')
    renderShell()
    expect(isLit('Activity')).toBe(true)
    expect(isLit('Library')).toBe(false)
  })

  it('desktop nav shows only from xl, where the whole row fits', () => {
    renderShell()
    const nav = document.querySelector('nav.hidden.xl\\:flex')
    expect(nav).not.toBeNull()
    expect(nav!.className).toContain('hidden')
    expect(nav!.className).toContain('xl:flex')
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

  it('hamburger button hides from xl, when the nav is in the row', () => {
    renderShell()
    const btn = screen.getByRole('button', { name: /toggle menu/i })
    expect(btn.className).toContain('xl:hidden')
  })

  it('mobile menu is hidden by default', () => {
    renderShell()
    expect(document.querySelector('div.xl\\:hidden > nav')).toBeNull()
  })

  it('opens mobile menu when hamburger is clicked', () => {
    renderShell()
    fireEvent.click(screen.getByRole('button', { name: /toggle menu/i }))
    const mobileNav = document.querySelector('div.xl\\:hidden > nav')
    expect(mobileNav).not.toBeNull()
  })

  it('mobile menu lists every page, group members under their group', () => {
    renderShell()
    fireEvent.click(screen.getByRole('button', { name: /toggle menu/i }))
    const mobileNav = document.querySelector('div.xl\\:hidden > nav')!
    const links = Array.from(mobileNav.querySelectorAll('a')).map(l => l.textContent)
    expect(links).toEqual([
      'Library', 'Authors', 'Books', 'Series',
      'Activity', 'Wanted', 'Queue', 'History',
      'Import', 'Calendar', 'Discover',
      'Search', 'Settings',
    ])
  })

  it('indents the members of a group in the mobile menu', () => {
    renderShell()
    fireEvent.click(screen.getByRole('button', { name: /toggle menu/i }))
    const mobileNav = document.querySelector('div.xl\\:hidden > nav')!
    const link = (label: string) => Array.from(mobileNav.querySelectorAll('a')).find(a => a.textContent === label)!
    expect(link('Books').className).toContain('pl-8')
    expect(link('Library').className).not.toContain('pl-8')
  })

  it('closes mobile menu when a nav link is clicked', () => {
    renderShell()
    fireEvent.click(screen.getByRole('button', { name: /toggle menu/i }))
    const mobileNav = document.querySelector('div.xl\\:hidden > nav')!
    expect(mobileNav).not.toBeNull()

    fireEvent.click(mobileNav.querySelector('a')!)
    expect(document.querySelector('div.xl\\:hidden > nav')).toBeNull()
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

describe('Shell: Import nav badge', () => {
  // Admins see how many library books still need a decision on the Import
  // entry. The count comes from an admin only route, so nobody else asks.
  it('shows the unmatched count for an admin', async () => {
    vi.mocked(api.unmatchedSummary).mockResolvedValue({ pending: 38, pendingFiles: 412, ignored: 0, adopted: 0, scan: {} as never })
    authState.value = { status: { authenticated: true, setupRequired: false, mode: 'enabled' }, logout: logoutMock, isAdmin: true }
    renderShell()
    const desktopNav = document.querySelector('nav.hidden.xl\\:flex') as HTMLElement
    const importLink = () => Array.from(desktopNav.querySelectorAll('a')).find(a => a.getAttribute('href') === '/import')
    await vi.waitFor(() => expect(importLink()?.textContent).toBe('Import38'))
  })

  it('never asks for the count as a non admin', () => {
    renderShell()
    expect(api.unmatchedSummary).not.toHaveBeenCalled()
  })
})

describe('Shell, requester role', () => {
  beforeEach(() => {
    authState.value = {
      status: { authenticated: true, mode: 'enabled', setupRequired: false, role: 'requester', username: 'reader' },
      logout: logoutMock,
      isAdmin: false,
      isRequester: true,
    }
  })

  it('shows only Library, Request and My requests', () => {
    renderShell()
    const desktopNav = document.querySelector('nav.hidden.xl\\:flex')!
    const labels = Array.from(desktopNav.querySelectorAll('a')).map(l => l.textContent)
    expect(labels).toEqual(['Library', 'Request', 'My requests'])
    expect(screen.queryByTitle('Settings')).not.toBeInTheDocument()
    expect(screen.queryByTitle('Search')).not.toBeInTheDocument()
  })

  it('renders the read only library at the root instead of Authors', async () => {
    renderShell()
    expect(await screen.findByTestId('page-requester-library')).toBeInTheDocument()
    expect(screen.queryByTestId('page-authors')).not.toBeInTheDocument()
  })

  it('sends any admin library path back to the requester library', async () => {
    window.history.pushState(null, '', '/queue')
    renderShell()
    expect(await screen.findByTestId('page-requester-library')).toBeInTheDocument()
    expect(window.location.pathname).toBe('/')
  })

  it('does not call the system status route it cannot read', () => {
    renderShell()
    expect(api.status).not.toHaveBeenCalled()
  })

  it('omits the settings and search links from the mobile menu', () => {
    renderShell()
    fireEvent.click(screen.getByRole('button', { name: /toggle menu/i }))
    const mobileNav = document.querySelector('div.xl\\:hidden > nav')!
    const links = Array.from(mobileNav.querySelectorAll('a')).map(l => l.textContent)
    expect(links).toEqual(['Library', 'Request', 'My requests'])
  })
})

describe('Shell, admin requests entry', () => {
  const asAdmin = () => {
    authState.value = {
      status: { authenticated: true, mode: 'enabled', setupRequired: false, role: 'admin' },
      logout: logoutMock,
      isAdmin: true,
    }
  }

  // Requests is a page inside Activity now, so its pending count rides the
  // Activity entry in the top bar. It is the only signal an admin gets that
  // somebody is waiting, so losing it in the regrouping would be a regression.
  it('carries the pending count on the Activity entry for admins', async () => {
    asAdmin()
    renderShell()
    const desktopNav = document.querySelector('nav.hidden.xl\\:flex')!
    const activity = Array.from(desktopNav.querySelectorAll('a')).find(l => l.getAttribute('href') === '/wanted')
    expect(activity).toBeDefined()
    expect(await within(activity as HTMLElement).findByText('3')).toBeInTheDocument()
  })

  it('shows Requests as an Activity tab with its count, for admins only', async () => {
    asAdmin()
    window.history.pushState(null, '', '/wanted')
    renderShell()
    const tabs = screen.getByRole('navigation', { name: 'Activity' })
    const requests = Array.from(tabs.querySelectorAll('a')).find(a => a.getAttribute('href') === '/requests')
    expect(requests).toBeDefined()
    expect(await within(requests as HTMLElement).findByText('3')).toBeInTheDocument()
  })

  it('does not show Requests to a plain user', () => {
    authState.value = {
      status: { authenticated: true, mode: 'enabled', setupRequired: false, role: 'user' },
      logout: logoutMock,
      isAdmin: false,
    }
    window.history.pushState(null, '', '/wanted')
    renderShell()
    const hrefs = Array.from(document.querySelectorAll('a')).map(l => l.getAttribute('href'))
    expect(hrefs).not.toContain('/requests')
    expect(api.pendingRequestCount).not.toHaveBeenCalled()
  })
})

describe('Shell, group tab strips', () => {
  it('shows the Library tabs above every Library page and marks the current one', async () => {
    window.history.pushState(null, '', '/books')
    renderShell()

    const tabs = screen.getByRole('navigation', { name: 'Library' })
    const links = Array.from(tabs.querySelectorAll('a'))
    expect(links.map(l => l.textContent)).toEqual(['Authors', 'Books', 'Series'])
    expect(links.map(l => l.getAttribute('href'))).toEqual(['/', '/books', '/series'])
    expect(links.find(l => l.textContent === 'Books')).toHaveAttribute('aria-current', 'page')
    expect(links.find(l => l.textContent === 'Authors')).not.toHaveAttribute('aria-current')
    expect(await screen.findByTestId('page-books')).toBeInTheDocument()
  })

  // Four Activity tabs measure 347px, wider than a 320px phone. Before this the
  // page itself scrolled sideways and the last tab was only reachable that way.
  it('scrolls the strip inside itself rather than widening the page', () => {
    window.history.pushState(null, '', '/wanted')
    renderShell()

    const tabs = screen.getByRole('navigation', { name: 'Activity' })
    const classes = tabs.className.split(' ')
    expect(classes).toContain('overflow-x-auto')
    expect(classes).toContain('max-w-full')
    // Each tab must keep its width inside the scroller instead of being
    // squeezed, which is what would make the row fit without scrolling.
    for (const tab of Array.from(tabs.querySelectorAll('a'))) {
      expect(tab.className.split(' ')).toContain('shrink-0')
      expect(tab.className.split(' ')).toContain('whitespace-nowrap')
    }
  })

  it('brings the active tab into view when the strip overflows', () => {
    const scrollIntoView = vi.fn()
    Element.prototype.scrollIntoView = scrollIntoView
    // jsdom reports every element as 0 by 0, so describe an overflowing strip.
    const scrollWidth = vi.spyOn(Element.prototype, 'scrollWidth', 'get').mockReturnValue(347)
    const clientWidth = vi.spyOn(Element.prototype, 'clientWidth', 'get').mockReturnValue(320)
    try {
      window.history.pushState(null, '', '/history')
      renderShell()
      expect(scrollIntoView).toHaveBeenCalledWith({ block: 'nearest', inline: 'nearest' })
    } finally {
      scrollWidth.mockRestore()
      clientWidth.mockRestore()
    }
  })

  it('leaves the scroll position alone when the strip fits', () => {
    const scrollIntoView = vi.fn()
    Element.prototype.scrollIntoView = scrollIntoView
    const scrollWidth = vi.spyOn(Element.prototype, 'scrollWidth', 'get').mockReturnValue(347)
    const clientWidth = vi.spyOn(Element.prototype, 'clientWidth', 'get').mockReturnValue(900)
    try {
      window.history.pushState(null, '', '/history')
      renderShell()
      expect(scrollIntoView).not.toHaveBeenCalled()
    } finally {
      scrollWidth.mockRestore()
      clientWidth.mockRestore()
    }
  })

  it('shows the Activity tabs on an Activity page', () => {
    window.history.pushState(null, '', '/history')
    renderShell()

    const tabs = screen.getByRole('navigation', { name: 'Activity' })
    const links = Array.from(tabs.querySelectorAll('a'))
    expect(links.map(l => l.textContent)).toEqual(['Wanted', 'Queue', 'History'])
    expect(links.find(l => l.textContent === 'History')).toHaveAttribute('aria-current', 'page')
    expect(screen.queryByRole('navigation', { name: 'Library' })).not.toBeInTheDocument()
  })

  it('shows no tab strip on a page that belongs to no group', () => {
    window.history.pushState(null, '', '/calendar')
    renderShell()

    expect(screen.queryByRole('navigation', { name: 'Library' })).not.toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: 'Activity' })).not.toBeInTheDocument()
  })

  it('gives a requester no tab strip', () => {
    authState.value = {
      status: { authenticated: true, mode: 'enabled', setupRequired: false, role: 'requester', username: 'reader' },
      logout: logoutMock,
      isAdmin: false,
      isRequester: true,
    }
    renderShell()

    expect(screen.queryByRole('navigation', { name: 'Library' })).not.toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: 'Activity' })).not.toBeInTheDocument()
  })
})
