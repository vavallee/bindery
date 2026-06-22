import { describe, it, expect } from 'vitest'
import { safeHref } from './safeHref'

describe('safeHref', () => {
  it('allows http(s) URLs', () => {
    expect(safeHref('https://indexer.example/details/1')).toBe('https://indexer.example/details/1')
    expect(safeHref('http://indexer.local/x')).toBe('http://indexer.local/x')
    expect(safeHref('HTTPS://EXAMPLE/x')).toBe('HTTPS://EXAMPLE/x')
  })

  it('rejects dangerous and non-http schemes', () => {
    for (const u of [
      'javascript:alert(1)',
      'JaVaScRiPt:alert(1)',
      'data:text/html,<script>alert(1)</script>',
      'vbscript:msgbox',
      'file:///etc/passwd',
      '//evil.example/x',
      'ftp://host/x',
      'relative/path',
    ]) {
      expect(safeHref(u)).toBe('')
    }
  })

  it('handles empty/nullish input', () => {
    expect(safeHref('')).toBe('')
    expect(safeHref(undefined)).toBe('')
    expect(safeHref(null)).toBe('')
  })
})
