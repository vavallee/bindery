import { describe, it, expect } from 'vitest'

// Guards for the phone fixes from the real browser audit (#3052). jsdom does
// no layout and applies no media queries, so what a test can check is the
// class that decides the layout on a phone. Sources are read through Vite's
// raw glob, the same way pageHeaderWrap.test.ts reads them.
const sources = import.meta.glob(['./**/*.tsx', '!./**/*.test.tsx'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

function src(file: string): string {
  const s = sources[`./${file}`]
  if (s === undefined) throw new Error(`no source for ${file}`)
  return s
}

// The class attribute of the last (or first) element whose content is exactly `content`
// (the phone card, where a page renders a table and cards from one list).
function classOf(file: string, content: string, which: 'first' | 'last' = 'last'): string {
  const s = src(file)
  const at = which === 'last' ? s.lastIndexOf(`>${content}<`) : s.indexOf(`>${content}<`)
  if (at < 0) throw new Error(`${content} not found in ${file}`)
  const open = s.lastIndexOf('<', at)
  const m = /className=(?:"([^"]*)"|\{`([^`]*)`\})/.exec(s.slice(open, at))
  return m ? (m[1] ?? m[2]) : ''
}

describe('long unbroken names wrap instead of widening the page', () => {
  // overflow-wrap: break-word does not lower an element's min-content width,
  // so a grid or flex item still grew to fit one long word. anywhere does.
  it.each<[string, string, 'first' | 'last']>([
    ['pages/AuthorDetailPage.tsx', '{author.authorName}', 'last'],
    // The page heading; the last one is a confirm dialog's inline mention.
    ['pages/BookDetailPage.tsx', '{book.title}', 'first'],
    ['pages/requests/RequesterLibraryPage.tsx', '{b.title}', 'last'],
    ['pages/requests/RequesterLibraryPage.tsx', '{b.authorName}', 'last'],
    ['pages/requests/MyRequestsPage.tsx', '{r.title}', 'last'],
    ['pages/requests/RequestsPage.tsx', '{r.title}', 'last'],
    ['pages/settings/BlocklistTab.tsx', '{entry.title}', 'last'],
    ['pages/SeriesPage.tsx', '{series.title}', 'last'],
    ['pages/QueuePage.tsx', '{item.title}', 'first'],
    ['pages/QueuePage.tsx', '{item.title}', 'last'],
  ])('%s %s (%s)', (file, content, which) => {
    expect(classOf(file, content, which)).toContain('[overflow-wrap:anywhere]')
  })

  it('keeps a series name on its own line beside its badges below sm', () => {
    expect(classOf('pages/SeriesPage.tsx', '{series.title}')).toMatch(/(^|\s)sm:truncate(\s|$)/)
    expect(src('pages/SeriesPage.tsx')).toContain('flex flex-wrap sm:flex-nowrap items-start justify-between')
  })
})

describe('settings rows with a text field and a button fit a 320px screen', () => {
  // A flex-1 input without min-w-0 keeps its intrinsic width (about 20
  // characters), which pushed the Save and Test buttons past the card edge.
  it.each(['pages/settings/GeneralTab.tsx', 'pages/settings/ApiKeysTab.tsx', 'pages/settings/CalibreTab.tsx'])('%s', file => {
    const s = src(file)
    expect(s).not.toMatch(/<(input|select)\b[^>]*className="flex-1 (?![^"]*min-w-0)/)
    expect(s).toContain('className="grow basis-48 min-w-0 ')
  })
})

describe('small controls get a 44px hit area on touch screens', () => {
  it.each([
    ['components/ViewToggle.tsx', '▦'],
    ['components/ViewToggle.tsx', '☰'],
    ['pages/settings/IndexersTab.tsx', "{t('common.edit')}"],
    ['pages/settings/IndexersTab.tsx', "{t('common.yes')}"],
    ['pages/settings/IndexersTab.tsx', "{t('common.no')}"],
  ])('%s %s', (file, content) => {
    // ViewToggle's buttons share one class builder, so check the builder.
    const cls = file === 'components/ViewToggle.tsx' ? src(file) : classOf(file, content)
    expect(cls).toContain('touch-target')
  })

  it.each([
    ['pages/BooksPage.tsx', 'const statusBtnCls'],
    ['pages/import/AdoptionFacets.tsx', 'const pill'],
  ])('%s status filter chips', (file, decl) => {
    const s = src(file)
    expect(s.slice(s.indexOf(decl), s.indexOf(decl) + 120)).toContain('touch-target')
  })

  it('the Blocklist and Delete buttons on the History cards', () => {
    const s = src('pages/HistoryPage.tsx')
    expect(s).toMatch(/className="touch-target text-xs text-amber-400[^"]*"\s*>\s*\{t\('history\.blocklist'\)\}/)
    expect(s).toMatch(/className=\{`touch-target text-xs py-1 \$\{dangerLink\}`\}\s*>\s*\{t\('history\.delete'\)\}/)
  })

  it('the series filter chips', () => {
    expect(src('pages/SeriesPage.tsx')).toMatch(/className=\{`touch-target px-3 py-1 rounded-md text-xs font-medium/)
  })

  // A chip is 24px tall, so its 44px hit area overhangs 10px above and below.
  // With the 4px gap of a wrapped row, the second row's chips took taps aimed
  // at the bottom of the first row's. On touch the rows sit 20px apart.
  it.each([
    ['pages/BooksPage.tsx', '<div className="flex gap-1 pointer-coarse:gap-y-5 flex-wrap">'],
    ['pages/SeriesPage.tsx', `aria-label={t('series.filterLabel')} className="flex gap-1 pointer-coarse:gap-y-5 flex-wrap items-center"`],
    ['pages/import/AdoptionFacets.tsx', `aria-label={t('adoption.stateLabel', 'Show')} className="flex gap-1 pointer-coarse:gap-y-5 flex-wrap items-center"`],
  ])('%s wrapped chip rows leave room for the hit areas', (file, row) => {
    expect(src(file)).toContain(row)
  })

  it('the Library tab strip, with padding because the strip scrolls', () => {
    expect(src('components/NavTabs.tsx')).toContain('pointer-coarse:py-3')
  })

  it('the Sign out button in the phone menu', () => {
    expect(classOf('App.tsx', "\n                  {t('login.signOut')}\n                ")).toContain('touch-target')
  })

  it.each(['pages/BooksPage.tsx', 'pages/AuthorDetailPage.tsx'])('%s book checkboxes, through a label round the box', file => {
    const s = src(file)
    // Grid cards: the label is the tap area, larger on touch.
    expect(s).toContain('className="absolute top-0 left-0 z-10 flex p-2 pointer-coarse:p-3.5 cursor-pointer"')
    // Table rows: the label fills the cell.
    expect(s).toContain('<label className="flex items-center -mx-3 -my-2 px-3 py-2 cursor-pointer pointer-coarse:min-h-11">')
  })
})

describe('touch screens get what a tooltip carried', () => {
  it('book detail says why Enrich is disabled', () => {
    const s = src('pages/BookDetailPage.tsx')
    expect(s).toContain('aria-describedby={book.asin ? undefined : \'book-enrich-no-asin\'}')
    expect(s).toMatch(/id="book-enrich-no-asin" className="hidden pointer-coarse:block[^"]*">\s*\{t\('bookDetail\.enrichHintNoAsin'\)\}/)
  })

  it('Wanted wraps the full book title below sm', () => {
    const s = src('pages/WantedPage.tsx')
    expect(s).toContain('className="block [overflow-wrap:anywhere] sm:truncate text-sm font-medium')
  })
})

describe('failure reasons stay readable on their tinted background', () => {
  // red-400 text on a 20% red tint over white is about 2.5:1 in the light
  // theme. The dark theme keeps red-400, where it reads well.
  it.each(['pages/HistoryPage.tsx', 'pages/settings/BlocklistTab.tsx'])('%s', file => {
    const s = src(file)
    expect(s).not.toMatch(/bg-red-500\/20 text-red-400/)
    expect(s).not.toMatch(/'text-red-400'/)
  })
})
