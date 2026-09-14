import { useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { isNewerRelease, isReleaseVersion, releaseHref } from '../util/version'

// The account menu in the desktop header: who is signed in, the running
// version (admins only, with the update notice), and Sign out, behind one
// icon. These three used to sit in the header row as text, and with the
// library search added in v1.36.0 the row no longer fit its 1216px content
// box: the logo, the nav and the search box touched, and the right side ran
// past the page. Folding them here gives the row back about 150px.
//
// Dismissal matches MoreMenu: Escape and an outside press close it, Escape
// returns focus to the trigger, and the arrow keys move between items.
export default function AccountMenu({
  username,
  version,
  latestVersion,
  onSignOut,
  className = '',
}: {
  /** Shown as "Signed in as …"; omitted when auth is disabled. */
  username?: string
  /** Running version; only passed for admins, as the header badge was. */
  version?: string
  latestVersion?: string
  /** Absent when there is no session to end (auth disabled). */
  onSignOut?: () => void
  className?: string
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const itemRefs = useRef<Array<HTMLElement | null>>([])
  const [active, setActive] = useState(-1)
  const menuId = useId()

  const updateAvailable = !!version && latestVersion !== undefined && isNewerRelease(version, latestVersion)
  const latest = latestVersion?.replace(/^v/, '') ?? ''
  const versionLabel = !version ? '' : updateAvailable
    ? `v${version} → v${latest}`
    : isReleaseVersion(version) ? `v${version}` : version
  const itemCount = (version ? 1 : 0) + (onSignOut ? 1 : 0)

  const close = (returnFocus: boolean) => {
    setOpen(false)
    setActive(-1)
    if (returnFocus) triggerRef.current?.focus()
  }

  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) close(false)
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        close(true)
      }
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  useEffect(() => {
    if (open && active >= 0) itemRefs.current[active]?.focus()
  }, [open, active])

  const openMenu = (at: 'first' | 'last') => {
    setOpen(true)
    setActive(itemCount === 0 ? -1 : at === 'first' ? 0 : itemCount - 1)
  }

  const onTriggerKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      openMenu('first')
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      openMenu('last')
    }
  }

  const onMenuKeyDown = (e: React.KeyboardEvent) => {
    if (itemCount === 0) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActive(i => (i + 1) % itemCount)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive(i => (i - 1 + itemCount) % itemCount)
    } else if (e.key === 'Home') {
      e.preventDefault()
      setActive(0)
    } else if (e.key === 'End') {
      e.preventDefault()
      setActive(itemCount - 1)
    } else if (e.key === 'Tab') {
      close(false)
    }
  }

  const triggerLabel = username ? `${t('login.signedInAs')} ${username}` : t('nav.account')
  const itemClass = 'block w-full px-3 py-1.5 text-left text-sm whitespace-nowrap focus-visible:outline-none focus:bg-slate-200 dark:focus:bg-zinc-800 hover:bg-slate-200 dark:hover:bg-zinc-800'
  let index = 0

  return (
    <div ref={rootRef} className={`relative ${className}`}>
      <button
        ref={triggerRef}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={triggerLabel}
        title={updateAvailable ? `${triggerLabel}. ${t('nav.updateAvailable')}` : triggerLabel}
        onClick={() => (open ? close(false) : openMenu('first'))}
        onKeyDown={onTriggerKeyDown}
        className={`relative block p-2 rounded-md transition-colors ${open ? 'bg-slate-200 dark:bg-zinc-800 text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'}`}
      >
        <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24" strokeWidth={2} aria-hidden="true">
          <path strokeLinecap="round" strokeLinejoin="round" d="M17.982 18.725A7.488 7.488 0 0 0 12 15.75a7.488 7.488 0 0 0-5.982 2.975m11.963 0a9 9 0 1 0-11.963 0m11.963 0A8.966 8.966 0 0 1 12 21a8.966 8.966 0 0 1-5.982-2.275M15 9.75a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z" />
        </svg>
        {updateAvailable && (
          <span data-testid="account-update-dot" className="absolute top-1.5 right-1.5 w-2 h-2 rounded-full bg-amber-500" aria-hidden="true" />
        )}
      </button>

      {open && (
        <div
          id={menuId}
          role="menu"
          aria-label={triggerLabel}
          onKeyDown={onMenuKeyDown}
          className="absolute right-0 z-50 mt-1 min-w-48 rounded-md border border-slate-300 dark:border-zinc-700 bg-slate-50 dark:bg-zinc-900 py-1 shadow-lg"
        >
          {username && (
            <div role="none" className="px-3 py-2 text-xs text-fg-muted border-b border-slate-200 dark:border-zinc-800 mb-1">
              {t('login.signedInAs')} <span className="font-medium text-slate-900 dark:text-zinc-100">{username}</span>
            </div>
          )}
          {version && (() => {
            const i = index++
            return (
              <a
                ref={el => { itemRefs.current[i] = el }}
                role="menuitem"
                tabIndex={i === active ? 0 : -1}
                href={releaseHref(updateAvailable ? latest : version)}
                target="_blank"
                rel="noopener noreferrer"
                onClick={() => close(false)}
                title={updateAvailable ? t('nav.updateAvailable') : undefined}
                className={`${itemClass} ${updateAvailable ? 'font-medium text-amber-600 dark:text-amber-400' : 'text-slate-700 dark:text-zinc-300'}`}
              >
                {versionLabel}
              </a>
            )
          })()}
          {onSignOut && (() => {
            const i = index++
            return (
              <button
                ref={el => { itemRefs.current[i] = el }}
                type="button"
                role="menuitem"
                tabIndex={i === active ? 0 : -1}
                onClick={() => { close(false); onSignOut() }}
                className={`${itemClass} text-slate-700 dark:text-zinc-300`}
              >
                {t('login.signOut')}
              </button>
            )
          })()}
        </div>
      )}
    </div>
  )
}
