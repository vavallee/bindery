// The navigation model. The top bar used to carry every page as its own link,
// which reached ten entries (two of them with count badges) beside the search
// box and four icons. The pages fall into two natural groups, so the bar now
// shows five entries and each group repeats its members as a tab strip on the
// pages that belong to it. Routes are untouched: a group link is a shortcut to
// its first member, and every member keeps the path it always had.

export type NavItem = { to: string; key: string; end?: boolean }

// A top level entry. `children` marks it as a group; without them it is a plain
// link to a single page.
export type NavEntry = NavItem & { children?: NavItem[] }

const LIBRARY_TABS: NavItem[] = [
  { to: '/', key: 'authors', end: true },
  { to: '/books', key: 'books' },
  { to: '/series', key: 'series' },
]

const ACTIVITY_TABS: NavItem[] = [
  { to: '/wanted', key: 'wanted' },
  { to: '/queue', key: 'queue' },
  { to: '/history', key: 'history' },
]

// A requester's whole shell: the read only library, search and request, and
// their own requests. Every other page calls routes the server refuses them,
// so their nav is a flat list with no groups.
const REQUESTER_NAV_KEYS: NavEntry[] = [
  { to: '/', key: 'requesterLibrary', end: true },
  { to: '/request', key: 'request' },
  { to: '/my-requests', key: 'myRequests' },
]

// navGroupsFor is the top level nav for a role. Admins get Requests as a fourth
// Activity tab; its pending count is rendered on the Activity entry and on the
// tab itself.
export function navGroupsFor(isAdmin: boolean, isRequester: boolean): NavEntry[] {
  if (isRequester) return REQUESTER_NAV_KEYS
  return [
    { to: '/', key: 'library', end: true, children: LIBRARY_TABS },
    {
      to: '/wanted',
      key: 'activity',
      children: isAdmin ? [...ACTIVITY_TABS, { to: '/requests', key: 'requests' }] : ACTIVITY_TABS,
    },
    { to: '/import', key: 'import' },
    { to: '/calendar', key: 'calendar' },
    { to: '/discover', key: 'discover' },
  ]
}

// matchesPath is the NavLink `end` rule applied by hand, because a group link
// has to light up for paths other than its own.
export function matchesPath(pathname: string, item: NavItem): boolean {
  if (item.end) return pathname === item.to
  return pathname === item.to || pathname.startsWith(`${item.to}/`)
}

// isEntryActive: a group is active on any of its members, a plain link only on
// its own page.
export function isEntryActive(pathname: string, entry: NavEntry): boolean {
  if (entry.children) return entry.children.some(child => matchesPath(pathname, child))
  return matchesPath(pathname, entry)
}

// activeGroup is the group whose tab strip belongs above the current page, or
// undefined on a page that is in no group (Import, Calendar, Discover, a detail
// page, Settings).
export function activeGroup(pathname: string, entries: NavEntry[]): NavEntry | undefined {
  return entries.find(entry => entry.children && isEntryActive(pathname, entry))
}
