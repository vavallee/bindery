import { describe, it, expect, beforeEach, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import WhatsNewToast from './WhatsNewToast'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: string | Record<string, unknown>, opts?: Record<string, unknown>) => {
      const vars = (typeof fallback === 'object' ? fallback : opts) ?? {}
      const labels: Record<string, string> = {
        'whatsNew.updated': `Updated to v${vars.version}.`,
        'whatsNew.link': "See what's new",
        'common.dismiss': 'Dismiss',
      }
      return labels[key] ?? (typeof fallback === 'string' ? fallback : key)
    },
  }),
}))

const KEY = 'bindery.lastSeenVersion'

describe('WhatsNewToast', () => {
  beforeEach(() => localStorage.clear())

  it('stays silent on a first-ever load and records the version', () => {
    render(<WhatsNewToast version="1.30.0" />)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(localStorage.getItem(KEY)).toBe('1.30.0')
  })

  it('announces an upgrade and links to the new release', () => {
    localStorage.setItem(KEY, '1.29.1')
    render(<WhatsNewToast version="1.30.0" />)

    expect(screen.getByRole('status')).toBeInTheDocument()
    expect(screen.getByText('Updated to v1.30.0.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: "See what's new" }))
      .toHaveAttribute('href', 'https://github.com/vavallee/bindery/releases/tag/v1.30.0')
    expect(localStorage.getItem(KEY)).toBe('1.30.0')
  })

  it('does not repeat on the next load of the same version', () => {
    localStorage.setItem(KEY, '1.29.1')
    const { unmount } = render(<WhatsNewToast version="1.30.0" />)
    expect(screen.getByRole('status')).toBeInTheDocument()
    unmount()

    render(<WhatsNewToast version="1.30.0" />)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('ignores dev/sha builds and empty versions', () => {
    localStorage.setItem(KEY, '1.29.1')
    render(<WhatsNewToast version="sha-9ecd99e" />)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    // The stored version must not be clobbered by a non-release build,
    // or the next real upgrade would compare against a sha.
    expect(localStorage.getItem(KEY)).toBe('1.29.1')

    render(<WhatsNewToast version="" />)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('can be dismissed', () => {
    localStorage.setItem(KEY, '1.29.1')
    render(<WhatsNewToast version="1.30.0" />)
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})
