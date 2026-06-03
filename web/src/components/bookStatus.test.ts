import { describe, it, expect } from 'vitest'
import { bookStatusBadge } from './bookStatus'

// Stub t: return the key so we can assert which label path was taken without
// depending on the loaded i18n resources.
const t = ((key: string) => key) as unknown as Parameters<typeof bookStatusBadge>[2]

describe('bookStatusBadge', () => {
  it('labels a wanted+monitored book "Wanted" with the amber colour', () => {
    const b = bookStatusBadge('wanted', true, t)
    expect(b.label).toBe('bookStatus.wanted')
    expect(b.colorClass).toContain('amber')
  })

  it('labels a wanted+UNmonitored book "Not monitored" with a muted colour, not Wanted (#977)', () => {
    const b = bookStatusBadge('wanted', false, t)
    expect(b.label).toBe('bookStatus.notMonitored')
    expect(b.label).not.toBe('bookStatus.wanted')
    expect(b.colorClass).toMatch(/slate|zinc/)
    expect(b.colorClass).not.toContain('amber')
  })

  it('leaves non-wanted statuses unaffected by monitored', () => {
    for (const monitored of [true, false]) {
      const imp = bookStatusBadge('imported', monitored, t)
      expect(imp.label).toBe('bookStatus.imported')
      expect(imp.colorClass).toContain('emerald')

      const dl = bookStatusBadge('downloading', monitored, t)
      expect(dl.label).toBe('bookStatus.downloading')
      expect(dl.colorClass).toContain('blue')
    }
  })

  it('falls back to a muted badge for an unknown status', () => {
    const b = bookStatusBadge('weird', true, t)
    expect(b.colorClass).toMatch(/slate|zinc/)
  })
})
