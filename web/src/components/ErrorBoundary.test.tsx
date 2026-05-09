import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ErrorBoundary from './ErrorBoundary'

function Boom({ shouldThrow }: { shouldThrow: boolean }) {
  if (shouldThrow) throw new Error('kaboom')
  return <div data-testid="ok">ok</div>
}

describe('ErrorBoundary', () => {
  let consoleError: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    // React logs caught errors to console.error; silence to keep test output clean.
    consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  afterEach(() => {
    consoleError.mockRestore()
  })

  it('renders children when no error', () => {
    render(
      <ErrorBoundary>
        <Boom shouldThrow={false} />
      </ErrorBoundary>,
    )
    expect(screen.getByTestId('ok')).toBeInTheDocument()
  })

  it('renders fallback when child throws', () => {
    render(
      <ErrorBoundary>
        <Boom shouldThrow />
      </ErrorBoundary>,
    )
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText('Something went wrong')).toBeInTheDocument()
    expect(screen.getByText('kaboom')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reload' })).toBeInTheDocument()
  })

  it('clicking Reload calls window.location.reload', () => {
    const reload = vi.fn()
    const original = window.location
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...original, reload, assign: vi.fn() },
    })
    try {
      render(
        <ErrorBoundary>
          <Boom shouldThrow />
        </ErrorBoundary>,
      )
      fireEvent.click(screen.getByRole('button', { name: 'Reload' }))
      expect(reload).toHaveBeenCalledTimes(1)
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: original })
    }
  })

  it('toggles details when Show details is clicked', () => {
    render(
      <ErrorBoundary>
        <Boom shouldThrow />
      </ErrorBoundary>,
    )
    const btn = screen.getByRole('button', { name: 'Show details' })
    fireEvent.click(btn)
    expect(screen.getByRole('button', { name: 'Hide details' })).toBeInTheDocument()
  })
})
