import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import type { ReactElement, ReactNode } from 'react'
import type { RenderOptions } from '@testing-library/react'
import type { AuthStatus } from './api/client'

interface RenderWithRouterOptions extends Omit<RenderOptions, 'wrapper'> {
  initialEntries?: string[]
}

export function renderWithRouter(
  ui: ReactElement,
  { initialEntries = ['/'], ...options }: RenderWithRouterOptions = {},
) {
  function Wrapper({ children }: { children: ReactNode }) {
    return <MemoryRouter initialEntries={initialEntries}>{children}</MemoryRouter>
  }

  return render(ui, { wrapper: Wrapper, ...options })
}

export function makeAuthStatus(overrides: Partial<AuthStatus> = {}): AuthStatus {
  return {
    authenticated: false,
    setupRequired: false,
    mode: 'enabled',
    localAuthEnabled: true,
    ...overrides,
  }
}

// The app confirms through ConfirmDialog rather than window.confirm (#2359), so
// a test that used to stub window.confirm clicks through the modal instead.
// Buttons are addressed by position, not label, because several suites mock
// react-i18next's `t` to return the key: Cancel is second to last, Confirm last.

/** The open confirmation modal, or null when none is open. */
export function confirmDialog(): HTMLElement | null {
  return screen.queryByTestId('confirm-dialog')
}

/** Tick the acknowledge box if there is one, then confirm. */
export async function acceptConfirm(): Promise<void> {
  const dialog = await screen.findByTestId('confirm-dialog')
  const acknowledge = within(dialog).queryByRole('checkbox')
  if (acknowledge) fireEvent.click(acknowledge)
  const buttons = within(dialog).getAllByRole('button')
  fireEvent.click(buttons[buttons.length - 1])
}

/** Dismiss the confirmation modal without confirming. */
export async function cancelConfirm(): Promise<void> {
  const dialog = await screen.findByTestId('confirm-dialog')
  const buttons = within(dialog).getAllByRole('button')
  fireEvent.click(buttons[buttons.length - 2])
}

/**
 * Install a window.matchMedia that answers `matches` for every query. jsdom
 * ships without matchMedia, which components read as a wide screen, so a test
 * of the phone layout installs this first. Returns a restore function.
 */
export function mockMatchMedia(matches: boolean | ((query: string) => boolean)): () => void {
  const had = Object.prototype.hasOwnProperty.call(window, 'matchMedia')
  const original = window.matchMedia
  const answer = typeof matches === 'function' ? matches : () => matches
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    writable: true,
    value: (query: string): MediaQueryList => ({
      matches: answer(query),
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }),
  })
  return () => {
    if (had) Object.defineProperty(window, 'matchMedia', { configurable: true, writable: true, value: original })
    else delete (window as { matchMedia?: unknown }).matchMedia
  }
}

// Clicks a bulk action by its label wherever BulkActionBar put it: inline,
// inside an inline choice menu ("Set format"), or behind the More menu, where
// a choice is listed as "<label>: <choice>". The bar caps its inline actions
// and never shows a destructive one inline, so a test that only clicked an
// inline button would be testing the layout rather than the action.
// `where` pins the action's place: 'menu' fails if the action is an inline
// button (Delete must stay behind More bulk actions), 'inline' fails if it is
// only in a menu. Left out, either place is accepted.
export function clickBulkAction(name: string, where?: 'inline' | 'menu'): void {
  const bar = screen.getByTestId('bulk-action-bar')
  const direct = within(bar).queryAllByRole('button').find(b => b.textContent === name && b.getAttribute('aria-haspopup') !== 'menu')
  if (direct && where === 'menu') throw new Error(`bulk action ${name} is inline, expected it in a menu`)
  if (!direct && where === 'inline') throw new Error(`bulk action ${name} is not an inline button`)
  if (direct) {
    fireEvent.click(direct)
    return
  }
  const matches = (el: HTMLElement) => el.textContent === name || (el.textContent ?? '').endsWith(`: ${name}`)
  for (const trigger of within(bar).queryAllByRole('button').filter(b => b.getAttribute('aria-haspopup') === 'menu')) {
    fireEvent.click(trigger)
    const item = screen.queryAllByRole('menuitem').find(matches)
    if (item) {
      fireEvent.click(item)
      return
    }
    fireEvent.keyDown(document, { key: 'Escape' })
  }
  throw new Error(`no bulk action named ${name}`)
}
