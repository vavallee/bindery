import { describe, it, expect, beforeAll } from 'vitest'
import postcss from 'postcss'
import tailwindcss from '@tailwindcss/postcss'

// Builds index.css through the PostCSS pipeline the production build uses
// (postcss.config.js) and checks the rules that only matter on a phone or in
// the dark theme. jsdom applies no media queries and does no layout, so the
// built stylesheet is the place to check them.
let built = ''

beforeAll(async () => {
  // Tailwind resolves the @import from disk relative to `from`, which postcss
  // resolves against the working directory: tests run from web/, the same
  // root the production build uses. (Importing the file with ?raw gives an
  // empty string under vitest, and the web tsconfig has no node types.)
  const result = await postcss([tailwindcss()]).process('@import "./index.css";', { from: 'src/indexCss.test.css' })
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

describe('index.css', () => {
  it('gives native controls the dark color scheme in the dark theme', () => {
    expect(block(built, /html\.dark\s*\{(?=[^}]*color-scheme)/)).toMatch(/color-scheme:\s*dark/)
    expect(block(built, /html:not\(\.dark\)\s*\{/)).toMatch(/color-scheme:\s*light/)
  })

  it('reserves room for the select arrow on touch screens only', () => {
    const touch = block(built, /@media \(hover: none\) and \(pointer: coarse\)\s*\{/)
    expect(block(touch, /select:not\(\[multiple\]\):not\(\[size\]\)\s*\{/)).toMatch(/padding-right:\s*2\.5rem/)
  })

  it('caps a select at the width of its container', () => {
    const bases = [...built.matchAll(/@layer base\s*\{/g)].map(m => block(built.slice(m.index), /@layer base\s*\{/))
    expect(bases.join('\n')).toMatch(/(^|[\s,}])select\s*\{[^}]*max-width:\s*100%/)
  })

  it('defines a 44px touch-target hit area under a coarse pointer', () => {
    const components = block(built, /@layer components\s*\{/)
    const coarse = block(components, /@media \(pointer: coarse\)\s*\{/)
    expect(block(coarse, /\.touch-target::before\s*\{/)).toMatch(/height:\s*max\(100%, 44px\)/)
    expect(block(coarse, /\.touch-target::before\s*\{/)).toMatch(/width:\s*max\(100%, 44px\)/)
    expect(block(coarse, /\.touch-target\s*\{/)).toMatch(/position:\s*relative/)
  })
})
