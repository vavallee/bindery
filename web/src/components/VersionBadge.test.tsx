import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import VersionBadge from './VersionBadge'

describe('VersionBadge', () => {
  it('links the newer release when one is available', () => {
    render(<VersionBadge version="1.44.0" latestVersion="v1.45.0" />)
    const link = screen.getByRole('link', { name: 'v1.44.0 → v1.45.0' })
    expect(link).toHaveAttribute('href', 'https://github.com/vavallee/bindery/releases/tag/v1.45.0')
    // The update colour has to read on a light page (#3084).
    expect(link.className).toContain('text-amber-700')
  })

  it('keeps the plain version link when the install is up to date', () => {
    render(<VersionBadge version="1.45.0" latestVersion="1.45.0" />)
    expect(screen.queryByText(/→/)).toBeNull()
    expect(screen.getByRole('link')).toHaveAttribute('href', 'https://github.com/vavallee/bindery/releases/tag/v1.45.0')
  })
})
