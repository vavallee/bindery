// Test helper for the Settings search drift check (#3059): finds every <h1> to
// <h6> in a component's source and says what its text is. Kept apart from the
// test so it can be tested itself, and so it is not a runtime import.

export interface FoundHeading {
  file: string
  kind: 'key' | 'literal' | 'dynamic'
  value: string
}

// The end of an opening tag: the first '>' that is not inside a {...}
// expression or a quoted attribute, so `onClick={() => go()}` does not end it.
function openingTagEnd(src: string, from: number): number {
  let depth = 0
  let quote = ''
  for (let i = from; i < src.length; i++) {
    const c = src[i]
    if (quote) {
      if (c === quote) quote = ''
    } else if (c === '"' || c === "'" || (c === '`' && depth > 0)) {
      quote = c
    } else if (c === '{') depth++
    else if (c === '}') depth--
    else if (c === '>' && depth === 0) return i
  }
  return -1
}

export function scanHeadings(sources: Record<string, string>): FoundHeading[] {
  const out: FoundHeading[] = []
  for (const [path, src] of Object.entries(sources)) {
    const file = path.replace(/^\.\//, '')
    for (const open of src.matchAll(/<h([1-6])\b/g)) {
      const level = open[1]
      const end = openingTagEnd(src, (open.index as number) + open[0].length)
      if (end < 0) continue
      const close = src.indexOf(`</h${level}>`, end)
      if (close < 0) continue
      const inner = src.slice(end + 1, close).replace(/\s+/g, ' ').trim()
      if (inner === '') continue
      const key = /^\{t\(\s*(['"`])([^'"`$]+)\1\s*[,)]/.exec(inner)
      if (key && inner.endsWith('}')) out.push({ file, kind: 'key', value: key[2] })
      else if (/[{}]/.test(inner)) out.push({ file, kind: 'dynamic', value: inner })
      else out.push({ file, kind: 'literal', value: inner })
    }
  }
  return out
}
