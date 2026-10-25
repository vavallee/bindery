import { describe, it, expect } from 'vitest'

// Interface text uses the ellipsis character, never three full stops. Most
// strings already did, in every language; the ones that didn't were English
// copied from the source, and the translations had quietly corrected them
// (#3411). Inline defaults are covered too: inlineDefaults.test.ts holds them
// equal to en.json.

const locales = import.meta.glob('./locales/*.json', { eager: true, import: 'default' }) as Record<string, Tree>

type Tree = { [key: string]: string | Tree }

function* strings(tree: Tree, prefix = ''): Generator<[string, string]> {
  for (const [key, value] of Object.entries(tree)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (typeof value === 'string') yield [path, value]
    else yield* strings(value, path)
  }
}

describe('locale strings', () => {
  it('reads every locale', () => {
    expect(Object.keys(locales).length).toBeGreaterThan(5)
  })

  it('use "…" rather than "..."', () => {
    const found: string[] = []
    for (const [file, tree] of Object.entries(locales)) {
      for (const [key, value] of strings(tree)) {
        if (value.includes('...')) found.push(`${file.replace('./locales/', '')} ${key}: ${value}`)
      }
    }
    expect(found).toEqual([])
  })
})
