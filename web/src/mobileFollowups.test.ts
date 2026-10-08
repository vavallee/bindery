import { describe, it, expect } from 'vitest'

// Guards for the second real browser phone audit (#3052). jsdom does no
// layout and applies no media queries, so what a test can check is the class
// that decides the layout on a phone, read from the source the same way
// mobilePolish.test.ts does.
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

// The class attribute of the element whose content is exactly `content`.
function classOf(file: string, content: string): string {
  const s = src(file)
  const at = s.lastIndexOf(`>${content}<`)
  if (at < 0) throw new Error(`${content} not found in ${file}`)
  const open = s.lastIndexOf('<', at)
  const m = /className=(?:"([^"]*)"|\{`([^`]*)`\})/.exec(s.slice(open, at))
  return m ? (m[1] ?? m[2]) : ''
}

// The first class attribute after `marker`, an attribute that comes before
// className on the same element.
function classNear(file: string, marker: string): string {
  const s = src(file)
  const at = s.indexOf(marker)
  if (at < 0) throw new Error(`${marker} not found in ${file}`)
  const m = /className=(?:"([^"]*)"|\{`([^`]*)`\})/.exec(s.slice(at, at + 400))
  return m ? (m[1] ?? m[2]) : ''
}

const words = (cls: string) => cls.split(/\s+/)

describe('Logs date range fits a 320px screen', () => {
  // A datetime input's intrinsic width at the 16px touch font ran 6px past a
  // Galaxy S9+ screen.
  it.each(["aria-label={t('settings.logs.from')}", "aria-label={t('settings.logs.to')}"])('%s', marker => {
    expect(words(classNear('pages/settings/LogsTab.tsx', marker))).toEqual(expect.arrayContaining(['min-w-0', 'flex-1', 'sm:flex-none']))
  })

  it('gives each date field a row of its own below sm', () => {
    const s = src('pages/settings/LogsTab.tsx')
    expect(s.match(/<div className="flex items-center gap-1 w-full sm:w-auto min-w-0">/g)).toHaveLength(2)
  })
})

describe('Queue Match picker fits a 320px screen', () => {
  // As flex items with min-width auto, the actions row and the picker took
  // the search input's intrinsic width as their minimum, which made the page
  // 526px wide and clipped Search and Remove.
  it('the row actions may shrink', () => {
    expect(words(classNear('pages/QueuePage.tsx', 'data-testid="queue-row-actions"'))).toContain('min-w-0')
  })

  it('the open picker may shrink', () => {
    expect(src('pages/QueuePage.tsx')).toContain('<div className="w-full min-w-0 sm:w-64 p-2')
  })
})

describe('remaining small controls get a 44px hit area on touch screens', () => {
  it.each([
    ['pages/QueuePage.tsx', "\n                        {t('queue.retryAllFailed', 'Retry all failed')}\n                      "],
    ['pages/QueuePage.tsx', "\n                      {t('queue.clearAllFailed', 'Clear all failed')}\n                    "],
    ['pages/settings/IndexersTab.tsx', "\n          {t('settings.indexers.addButton')}\n        "],
    ['pages/BooksPage.tsx', "\n                      {t('books.download')}\n                    "],
    ['pages/SeriesPage.tsx', "\n                    {t('series.rename')}\n                  "],
    ['pages/SeriesPage.tsx', "\n                    {t('common.delete')}\n                  "],
    ['pages/SeriesPage.tsx', "\n                      {filling === series.id ? t('series.fill.queuing') : t('series.fill.button')}\n                    "],
  ])('%s %s', (file, content) => {
    expect(words(classOf(file, content))).toContain('touch-target')
  })

  it('the Shortlisted switch on Series', () => {
    const s = src('pages/SeriesPage.tsx')
    const at = s.indexOf("title={t('series.shortlist.hint')}")
    expect(s.slice(at, at + 120)).toContain('className="touch-target"')
  })

  it('the queue time toggle, with room above and below for its hit area', () => {
    expect(classNear('pages/QueuePage.tsx', 'detail={ts.absolute}')).toContain('touch-target')
    expect(src('pages/QueuePage.tsx')).toContain('gap-x-3 gap-y-1 mt-1 text-xs pointer-coarse:my-3.5 pointer-coarse:gap-y-5')
  })

  it('the Sort and Filters triggers', () => {
    expect(src('components/FilterPopover.tsx')).toContain('className={`touch-target ${btn.secondary} ${btnSize.sm}`}')
  })

  it('the bulk action bar buttons', () => {
    const s = src('components/BulkActionBar.tsx')
    expect(s).toContain('`touch-target px-3 py-1.5 rounded text-xs')
    expect(s).toContain("const clearClass = 'touch-target ")
  })

  it('the Series action row leaves room between wrapped rows', () => {
    expect(src('pages/SeriesPage.tsx')).toContain('<div className="px-4 pb-3 flex items-center gap-3 pointer-coarse:gap-y-5 flex-wrap"')
  })
})
