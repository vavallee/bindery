import { Link, useLocation } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useEffect, useRef, type ReactNode } from 'react'
import { matchesPath, type NavEntry, type NavItem } from './navGroups'

// The tab strip a grouped page carries above its content. Styled like the
// segmented tabs on the Import page so it reads as the same control, but the
// tabs are real links: the URL changes, the back button works, and each page
// stays bookmarkable.
//
// The strip is 347px wide with four tabs, so on a 320px phone it used to widen
// the page and the last tab could only be reached by scrolling the whole page
// sideways. It scrolls inside itself now, the same way the wide tables and the
// adoption rail do, and the active tab is brought into view on mount so a deep
// link to the rightmost page does not open on a strip that looks truncated.
export default function NavTabs({ group, renderLabel }: { group: NavEntry; renderLabel: (item: NavItem) => ReactNode }) {
  const { t } = useTranslation()
  const { pathname } = useLocation()
  const stripRef = useRef<HTMLElement | null>(null)
  const activeRef = useRef<HTMLAnchorElement | null>(null)
  const tabs = group.children ?? []

  useEffect(() => {
    const strip = stripRef.current
    const active = activeRef.current
    if (!strip || !active) return
    // Only when the strip actually overflows: scrollIntoView on a strip that
    // fits is a no-op at best and a page jump at worst.
    if (strip.scrollWidth <= strip.clientWidth) return
    if (typeof active.scrollIntoView !== 'function') return
    active.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }, [pathname])

  if (tabs.length === 0) return null

  return (
    <nav
      ref={stripRef}
      aria-label={t(`nav.${group.key}`)}
      className="mb-5 inline-flex max-w-full gap-1 overflow-x-auto p-1 rounded-lg bg-slate-200/70 dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800"
    >
      {tabs.map(tab => {
        const active = matchesPath(pathname, tab)
        return (
          <Link
            key={tab.to}
            to={tab.to}
            ref={active ? activeRef : undefined}
            aria-current={active ? 'page' : undefined}
            className={`shrink-0 whitespace-nowrap px-3 py-1.5 rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-500 ${
              active
                ? 'bg-white dark:bg-zinc-800 text-slate-900 dark:text-white shadow-sm'
                : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white'
            }`}
          >
            {renderLabel(tab)}
          </Link>
        )
      })}
    </nav>
  )
}
