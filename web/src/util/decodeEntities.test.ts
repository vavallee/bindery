import { describe, it, expect } from 'vitest'
import { decodeEntities } from './decodeEntities'

describe('decodeEntities', () => {
  it.each([
    ['D&amp;D - Dragonlance', 'D&D - Dragonlance'],
    ['Tom &quot;Jones&quot;', 'Tom "Jones"'],
    ['Caf&#233; &#x41;', 'Café A'],
    ['no entities here', 'no entities here'],
    ['&unknown; stays', '&unknown; stays'],
    ['&#0; stays', '&#0; stays'],
  ])('%s', (input, expected) => {
    expect(decodeEntities(input)).toBe(expected)
  })
})
