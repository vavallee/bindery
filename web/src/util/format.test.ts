import { describe, it, expect } from 'vitest'
import { formatBytes, formatBytesZero } from './format'

describe('formatBytes', () => {
  it('returns nothing for an unknown or absent size', () => {
    expect(formatBytes(0)).toBe('')
    expect(formatBytes(-1)).toBe('')
    expect(formatBytes(NaN)).toBe('')
    expect(formatBytes(Infinity)).toBe('')
  })

  it('uses whole numbers for B and KB', () => {
    expect(formatBytes(1)).toBe('1 B')
    expect(formatBytes(1023)).toBe('1023 B')
    expect(formatBytes(1024)).toBe('1 KB')
    expect(formatBytes(1536)).toBe('2 KB')
  })

  it('uses one decimal from MB up', () => {
    expect(formatBytes(1048576)).toBe('1.0 MB')
    expect(formatBytes(838860800)).toBe('800.0 MB')
    // Exactly 1 GiB is a GB. The old Queue/Wanted copies compared with `>` and
    // rendered it as "1024.0 MB".
    expect(formatBytes(1073741824)).toBe('1.0 GB')
    expect(formatBytes(1610612736)).toBe('1.5 GB')
    expect(formatBytes(1099511627776)).toBe('1.0 TB')
  })

  it('clamps the unit table instead of running off the end', () => {
    // RootFoldersTab had no clamp, so a petabyte rendered as "1.1 undefined".
    expect(formatBytes(1125899906842624)).toBe('1.0 PB')
    expect(formatBytes(1125899906842624 * 2048)).toMatch(/ PB$/)
    expect(formatBytes(1125899906842624 * 2048)).not.toContain('undefined')
  })
})

describe('formatBytesZero', () => {
  it('shows a real zero for the callers where zero means something', () => {
    expect(formatBytesZero(0)).toBe('0 B')
    expect(formatBytesZero(-1)).toBe('0 B')
  })

  it('otherwise matches formatBytes', () => {
    expect(formatBytesZero(1610612736)).toBe(formatBytes(1610612736))
    expect(formatBytesZero(1024)).toBe('1 KB')
  })
})
