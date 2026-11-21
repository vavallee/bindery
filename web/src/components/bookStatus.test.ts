import { describe, it, expect } from 'vitest'
import { bookStatusBadge } from './bookStatus'

// Stub t: return the key so we can assert which label path was taken without
// depending on the loaded i18n resources.
const t = ((key: string) => key) as unknown as Parameters<typeof bookStatusBadge>[2]

describe('bookStatusBadge', () => {
  it('labels a wanted+monitored book "Wanted" with the warning colour', () => {
    const b = bookStatusBadge('wanted', true, t)
    expect(b.label).toBe('bookStatus.wanted')
    expect(b.colorClass).toContain('warning')
  })

  it('labels a wanted+UNmonitored book "Not monitored" with a muted colour, not Wanted (#977)', () => {
    const b = bookStatusBadge('wanted', false, t)
    expect(b.label).toBe('bookStatus.notMonitored')
    expect(b.label).not.toBe('bookStatus.wanted')
    expect(b.colorClass).toMatch(/slate|zinc/)
    expect(b.colorClass).not.toContain('warning')
  })

  it('leaves non-wanted statuses unaffected by monitored', () => {
    for (const monitored of [true, false]) {
      const imp = bookStatusBadge('imported', monitored, t)
      expect(imp.label).toBe('bookStatus.imported')
      expect(imp.colorClass).toContain('success')

      const skip = bookStatusBadge('skipped', monitored, t)
      expect(skip.label).toBe('bookStatus.skipped')
      expect(skip.colorClass).toMatch(/slate|zinc/)
    }
  })

  it('falls back to a muted badge for an unknown status', () => {
    const b = bookStatusBadge('weird', true, t)
    expect(b.colorClass).toMatch(/slate|zinc/)
  })
})

// WCAG AA enforcement for the status badges. These pills carry small text, so
// the 4.5:1 normal-text threshold applies. Their colours are status tokens now
// (#3994): src/theme/contrast.test.ts measures every token pair, strong text on
// its tint, in both themes, and reads these classes through its scan of the
// source. Here the badges have to stay on the token pair that is measured.
describe('bookStatusBadge contrast (WCAG AA)', () => {
  it.each([
    ['wanted', 'bg-warning-marker/20', 'text-warning-strong'],
    ['imported', 'bg-success-marker/20', 'text-success-strong'],
  ] as const)('%s uses the measured %s and %s pair', (status, bg, text) => {
    const classes = bookStatusBadge(status, true, t).colorClass.split(/\s+/)
    expect(classes).toContain(bg)
    expect(classes).toContain(text)
  })
})
