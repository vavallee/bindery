import { describe, it, expect } from 'vitest'

// #3237: forced colours (Windows High Contrast) drops box-shadow focus rings.
// `outline-none` sets outline-style: none, so a control that pairs it with a
// ring has no focus indicator there at all. `outline-hidden` keeps a
// transparent outline that forced colours paints instead.
const sources = import.meta.glob(['./**/*.{ts,tsx}', '!./**/*.test.{ts,tsx}'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

// The three popup menu items, whose outline-none #3269 replaces with an
// inset ring. Drop this once it has merged.
const pendingMenuItem = /focus-visible:outline-none focus(?:-visible)?:bg-/

describe('focus styles in forced colours', () => {
  it('uses outline-hidden rather than outline-none', () => {
    const hits: string[] = []
    for (const [file, s] of Object.entries(sources)) {
      s.split('\n').forEach((line, i) => {
        if (!/\boutline-none\b/.test(line)) return
        if (pendingMenuItem.test(line)) return
        hits.push(`${file}:${i + 1}`)
      })
    }
    expect(hits).toEqual([])
  })
})
