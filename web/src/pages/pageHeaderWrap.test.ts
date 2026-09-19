import { describe, it, expect } from 'vitest'

// Guard against the phone bug where the Authors page scrolled sideways: the
// header row was `flex items-center justify-between` with no `flex-wrap`, so
// the title and a six button toolbar had to share one line and the row grew
// wider than the viewport.
//
// jsdom does no layout, so a rendering test cannot measure an overflow. What it
// can check is the structural rule the working pages already follow: a page
// header row that spreads a title and a toolbar with `justify-between` must be
// allowed to wrap. Books, Series, History and Import were already written that
// way; Authors, Calendar and Discover were not, and this test is what stops the
// next one from slipping back.
//
// Sources are read through Vite's raw glob, the same way
// src/i18n/inlineDefaults.test.ts reads them, so this needs no node:fs types.
const sources = import.meta.glob('./**/*.tsx', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

// A page heading: the size the pages use for their own title.
const HEADING = /<h[12][^>]*className="[^"]*text-(?:2xl|xl) font-bold/

// An opening div whose class list is a flex row using justify-between.
const FLEX_ROW = /<div className="([^"]*\bflex\b[^"]*\bjustify-between\b[^"]*)"/

interface Header {
  file: string
  line: number
  classes: string
}

function pageHeaders(): Header[] {
  const found: Header[] = []
  for (const [file, source] of Object.entries(sources)) {
    if (file.endsWith('.test.tsx')) continue
    const lines = source.split('\n')
    for (let i = 0; i < lines.length; i++) {
      const row = FLEX_ROW.exec(lines[i])
      if (!row) continue
      // The heading sits on the row itself or just inside a wrapper that holds
      // the title and a subtitle, as on Discover and Import.
      const near = lines.slice(i + 1, i + 4).join('\n')
      if (!HEADING.test(near)) continue
      found.push({ file, line: i + 1, classes: row[1] })
    }
  }
  return found
}

describe('page header rows', () => {
  const headers = pageHeaders()

  it('finds the page headers it is meant to be checking', () => {
    // Without this the regexes could stop matching and the test below would
    // pass over an empty list.
    expect(headers.length).toBeGreaterThanOrEqual(6)
    expect(headers.map(h => h.file)).toContain('./AuthorsPage.tsx')
  })

  it('lets every title-and-toolbar row wrap', () => {
    const rigid = headers
      .filter(h => !/\bflex-wrap\b/.test(h.classes))
      .map(h => `${h.file}:${h.line} ${h.classes}`)
    expect(rigid).toEqual([])
  })
})
