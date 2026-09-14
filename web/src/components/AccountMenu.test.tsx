import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import AccountMenu from './AccountMenu'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => ({
      'login.signedInAs': 'Signed in as',
      'login.signOut': 'Sign out',
      'nav.account': 'Account',
      'nav.updateAvailable': 'Update available',
    } as Record<string, string>)[key] ?? key,
  }),
}))

describe('AccountMenu', () => {
  const onSignOut = vi.fn()
  beforeEach(() => vi.clearAllMocks())

  it('names the trigger after the signed in user and keeps the menu closed', () => {
    render(<AccountMenu username="akadmin" version="1.36.0" onSignOut={onSignOut} />)
    expect(screen.getByRole('button', { name: 'Signed in as akadmin' })).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('shows who is signed in, the version and Sign out, and signs out', () => {
    render(<AccountMenu username="akadmin" version="1.36.0" onSignOut={onSignOut} />)
    fireEvent.click(screen.getByRole('button', { name: 'Signed in as akadmin' }))
    const menu = screen.getByRole('menu')
    expect(menu).toHaveTextContent('Signed in as akadmin')
    expect(screen.getByRole('menuitem', { name: 'v1.36.0' })).toHaveAttribute('href', expect.stringContaining('1.36.0'))
    fireEvent.click(screen.getByRole('menuitem', { name: 'Sign out' }))
    expect(onSignOut).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('marks the trigger and links the newer release when an update is available', () => {
    render(<AccountMenu username="akadmin" version="1.35.0" latestVersion="v1.36.0" onSignOut={onSignOut} />)
    expect(screen.getByTestId('account-update-dot')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Signed in as akadmin' }))
    const item = screen.getByRole('menuitem', { name: 'v1.35.0 → v1.36.0' })
    expect(item).toHaveAttribute('href', expect.stringContaining('1.36.0'))
  })

  it('shows no update dot and no version item for a non admin', () => {
    render(<AccountMenu username="reader" onSignOut={onSignOut} />)
    expect(screen.queryByTestId('account-update-dot')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Signed in as reader' }))
    expect(screen.getAllByRole('menuitem')).toHaveLength(1)
  })

  it('offers only the version when there is no session to end', () => {
    render(<AccountMenu version="1.36.0" />)
    fireEvent.click(screen.getByRole('button', { name: 'Account' }))
    expect(screen.getByRole('menuitem', { name: 'v1.36.0' })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: 'Sign out' })).not.toBeInTheDocument()
  })

  it('closes on Escape and returns focus to the trigger', () => {
    render(<AccountMenu username="akadmin" version="1.36.0" onSignOut={onSignOut} />)
    const trigger = screen.getByRole('button', { name: 'Signed in as akadmin' })
    fireEvent.keyDown(trigger, { key: 'ArrowDown' })
    expect(screen.getByRole('menuitem', { name: 'v1.36.0' })).toHaveFocus()
    fireEvent.keyDown(screen.getByRole('menu'), { key: 'ArrowDown' })
    expect(screen.getByRole('menuitem', { name: 'Sign out' })).toHaveFocus()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })

  it('closes on a press outside', () => {
    render(<div><AccountMenu username="akadmin" onSignOut={onSignOut} /><p>outside</p></div>)
    fireEvent.click(screen.getByRole('button', { name: 'Signed in as akadmin' }))
    fireEvent.pointerDown(screen.getByText('outside'))
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })
})
