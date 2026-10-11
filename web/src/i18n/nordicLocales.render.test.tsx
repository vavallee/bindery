import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import i18n from './index'
import en from './locales/en.json'
import nb from './locales/nb.json'
import sv from './locales/sv.json'
import { api } from '../api/client'
import CalendarPage from '../pages/CalendarPage'
import WantedPage from '../pages/WantedPage'
import SeriesPage from '../pages/SeriesPage'
import LanguageSwitcher from '../components/LanguageSwitcher'

// Renders real pages through the real i18n instance in Norwegian and Swedish
// (#2985). A missing or misspelled key renders as the key path itself, which
// is the thing this catches; the per-string checks live in nordicLocales.test.ts.

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      status: vi.fn(),
      listAllBooks: vi.fn(),
      listWanted: vi.fn(),
      listSeries: vi.fn(),
    },
  }
})

type Tree = { [key: string]: string | Tree }

function keyPaths(tree: Tree, prefix = ''): string[] {
  return Object.entries(tree).flatMap(([key, value]) => {
    const path = prefix ? `${prefix}.${key}` : key
    return typeof value === 'string' ? [path] : keyPaths(value, path)
  })
}

const allKeys = keyPaths(en as Tree)

// Everything a reader can see or a screen reader can announce.
function visibleText(): string {
  const attributes = [...document.body.querySelectorAll('[placeholder],[aria-label],[title],[alt]')].flatMap(el =>
    ['placeholder', 'aria-label', 'title', 'alt'].map(name => el.getAttribute(name) ?? ''),
  )
  return [document.body.textContent ?? '', ...attributes].join('\n')
}

function expectNoRawKeys() {
  const text = visibleText()
  expect(allKeys.filter(key => text.includes(key))).toEqual([])
}

// Each page with one string that only appears once it has loaded.
const pages = [
  { name: 'Calendar', element: <CalendarPage />, probe: (b: typeof nb) => b.calendar.noReleases },
  { name: 'Wanted', element: <WantedPage />, probe: (b: typeof nb) => b.wanted.empty },
  { name: 'Series', element: <SeriesPage />, probe: (b: typeof nb) => b.series.searchPlaceholder },
]

const locales: [string, typeof nb][] = [['nb', nb], ['sv', sv as typeof nb]]

describe.each(locales)('pages rendered in %s', (locale, bundle) => {
  beforeEach(async () => {
    vi.mocked(api.status).mockResolvedValue({ version: 'dev', commit: 'unknown', buildDate: '', enhancedHardcoverApi: true, hardcoverTokenConfigured: true })
    vi.mocked(api.listAllBooks).mockResolvedValue([])
    vi.mocked(api.listWanted).mockResolvedValue([])
    vi.mocked(api.listSeries).mockResolvedValue([])
    await act(async () => { await i18n.changeLanguage(locale) })
  })

  it.each(pages)('renders the $name page translated, with no raw keys', async ({ element, probe }) => {
    const translated = probe(bundle)
    const english = probe(en as unknown as typeof nb)
    expect(translated).toBeTruthy()
    expect(translated).not.toBe(english)
    render(<MemoryRouter>{element}</MemoryRouter>)
    await waitFor(() => expect(visibleText()).toContain(translated))
    expect(visibleText()).not.toContain(english)
    expectNoRawKeys()
  })

  it('lists both languages by their own names in the picker', () => {
    render(<LanguageSwitcher />)
    expect(screen.getByRole('option', { name: 'Norsk bokmål' })).toHaveValue('nb')
    expect(screen.getByRole('option', { name: 'Svenska' })).toHaveValue('sv')
    expectNoRawKeys()
  })
})

afterAll(async () => {
  await i18n.changeLanguage('en')
  localStorage.removeItem('bindery.lang')
})
