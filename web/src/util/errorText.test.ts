import { describe, it, expect } from 'vitest'
import { errorText } from './errorText'

describe('errorText', () => {
  it("uses an Error's own message", () => {
    expect(errorText(new Error('disk full'), 'Save failed')).toBe('disk full')
  })

  it('falls back for anything that is not an Error', () => {
    expect(errorText('nope', 'Save failed')).toBe('Save failed')
    expect(errorText(undefined, 'Save failed')).toBe('Save failed')
  })
})
