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
