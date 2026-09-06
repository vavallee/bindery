import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import Alert from './Alert'

// t(key, 'fallback') is the shape Alert uses for its own three strings.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (_key: string, fallback?: string) => fallback ?? _key,
  }),
}))

describe('Alert', () => {
  it('renders the neutral info tier without amber or red', () => {
    render(<Alert tier="info" title="Nothing is wrong" testId="a" />)
    const el = screen.getByTestId('a')
    expect(el).toHaveTextContent('Nothing is wrong')
    expect(el.className).toContain('bg-slate-100')
    expect(el.className).toContain('dark:bg-zinc-900')
    expect(el.className).not.toContain('amber')
    expect(el.className).not.toContain('red')
  })

  it('renders the warning tier in amber, with a dark variant', () => {
    render(<Alert tier="warning" title="Half configured" testId="a" />)
    const el = screen.getByTestId('a')
    expect(el.className).toContain('bg-amber-50')
    expect(el.className).toContain('dark:bg-amber-950/40')
    expect(el.className).toContain('dark:text-amber-200')
  })

  it('renders the error tier in red and announces it assertively', () => {
    render(<Alert tier="error" title="Save failed" testId="a" />)
    const el = screen.getByTestId('a')
    expect(el.className).toContain('bg-red-50')
    expect(el.className).toContain('dark:bg-red-950/40')
    expect(el).toHaveAttribute('role', 'alert')
  })

  it('uses a polite live region for info and warning', () => {
    const { rerender } = render(<Alert tier="info" title="A" testId="a" />)
    expect(screen.getByTestId('a')).toHaveAttribute('role', 'status')
    rerender(<Alert tier="warning" title="A" testId="a" />)
    expect(screen.getByTestId('a')).toHaveAttribute('role', 'status')
  })

  it('lets a caller override the role', () => {
    render(<Alert tier="info" role="note" title="A" testId="a" />)
    expect(screen.getByTestId('a')).toHaveAttribute('role', 'note')
  })

  // Detail on demand is a disclosure, not a deletion: the long form has to be
  // reachable from the collapsed alert, and it has to actually be collapsed
  // first or the alert is no shorter than what it replaced.
  it('hides details until the toggle is pressed, then shows them', () => {
    render(<Alert tier="info" title="Skipped 65 works" details={<p>because of the language filter</p>} testId="a" />)
    expect(screen.queryByText('because of the language filter')).not.toBeInTheDocument()

    const toggle = screen.getByRole('button', { name: 'Show details' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(toggle)

    expect(screen.getByText('because of the language filter')).toBeInTheDocument()
    const open = screen.getByRole('button', { name: 'Hide details' })
    expect(open).toHaveAttribute('aria-expanded', 'true')
    const controls = open.getAttribute('aria-controls')!
    expect(document.getElementById(controls)).toHaveTextContent('because of the language filter')
  })

  it('collapses details again on a second press', () => {
    render(<Alert tier="info" title="A" details={<p>detail</p>} />)
    fireEvent.click(screen.getByRole('button', { name: 'Show details' }))
    fireEvent.click(screen.getByRole('button', { name: 'Hide details' }))
    expect(screen.queryByText('detail')).not.toBeInTheDocument()
  })

  it('takes a custom toggle label', () => {
    render(<Alert tier="info" title="A" details={<p>d</p>} detailsLabel="Which ones?" />)
    expect(screen.getByRole('button', { name: 'Which ones?' })).toBeInTheDocument()
  })

  it('renders no toggle when there are no details', () => {
    render(<Alert tier="info" title="A" testId="a" />)
    expect(screen.queryByRole('button', { name: 'Show details' })).not.toBeInTheDocument()
  })

  it('has no dismiss button unless asked for one', () => {
    render(<Alert tier="warning" title="A" testId="a" />)
    expect(screen.queryByRole('button', { name: 'Dismiss' })).not.toBeInTheDocument()
  })

  it('removes itself and reports the dismissal', () => {
    const onDismiss = vi.fn()
    render(<Alert tier="info" title="A" onDismiss={onDismiss} testId="a" />)
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(onDismiss).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('a')).not.toBeInTheDocument()
  })

  it('dismisses without a callback when only dismissible is set', () => {
    render(<Alert tier="info" title="A" dismissible testId="a" />)
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByTestId('a')).not.toBeInTheDocument()
  })

  it('renders children and actions alongside the title', () => {
    render(
      <Alert tier="warning" title="Head" actions={<button>Fix it</button>} testId="a">
        <p>body</p>
      </Alert>,
    )
    expect(screen.getByText('Head')).toBeInTheDocument()
    expect(screen.getByText('body')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Fix it' })).toBeInTheDocument()
  })
})
