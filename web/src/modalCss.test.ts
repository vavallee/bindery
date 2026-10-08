import { describe, it, expect, beforeAll } from 'vitest'
import postcss from 'postcss'
import tailwindcss from '@tailwindcss/postcss'

// #3052: in landscape on a phone, a modal's header and footer ate most of the
// height and its scrolling list shrank to a sliver (27px in Reconcile
// catalogue). Below 500px of height the whole panel scrolls instead. jsdom
// applies no media queries and does no layout, so this builds index.css
// through the PostCSS pipeline the production build uses and checks the rule.
let built = ''

beforeAll(async () => {
  // Tailwind resolves the @import relative to `from`, which postcss resolves
  // against the working directory: tests run from web/, like the build.
  const result = await postcss([tailwindcss()]).process('@import "./index.css";', { from: 'src/modalCss.test.css' })
  built = result.css
}, 60_000)

// The body of the first block whose prelude matches, braces balanced.
function block(source: string, prelude: RegExp): string {
  const m = prelude.exec(source)
  if (!m) return ''
  let depth = 0
  const start = source.indexOf('{', m.index)
  for (let i = start; i < source.length; i++) {
    if (source[i] === '{') depth += 1
    else if (source[i] === '}') {
      depth -= 1
      if (depth === 0) return source.slice(start + 1, i)
    }
  }
  return ''
}

describe('modal CSS', () => {
  it('lets a modal panel scroll as one area on a short viewport', () => {
    const short = block(built, /@media \(max-height: 500px\)\s*\{/)
    expect(short).not.toBe('')
    expect(block(short, /\.modal-max-h\s*\{/)).toMatch(/overflow-y:\s*auto/)
    expect(block(short, /\.modal-max-h > \*\s*\{/)).toMatch(/flex:\s*none/)
    expect(block(short, /\.modal-max-h \[class\*=["']max-h-["']\]\s*\{/)).toMatch(/max-height:\s*none/)
  })

  it('keeps the rule out of any cascade layer so it beats the utilities', () => {
    const utilities = block(built, /@layer utilities\s*\{/)
    expect(utilities).not.toMatch(/max-height: 500px/)
  })

  it('draws no focus ring on the panel itself', () => {
    expect(block(built, /\[data-modal-panel\]:focus\s*\{/)).toMatch(/outline:\s*none/)
  })
})
