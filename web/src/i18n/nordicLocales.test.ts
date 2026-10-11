import { afterEach, describe, expect, it, vi } from 'vitest'
import en from './locales/en.json'
import nb from './locales/nb.json'
import sv from './locales/sv.json'

// Structural checks for the Norwegian and Swedish bundles (#2985). A locale may
// be partial, since fallbackLng covers what is missing, but whatever it does
// supply has to be a real translation of a real key. English filler is the
// failure this guards hardest against: it renders fine, so nobody notices, and
// it hides the gap from the next translator.

type Tree = { [key: string]: string | Tree }

function flatten(tree: Tree, prefix = ''): Map<string, string> {
  const out = new Map<string, string>()
  for (const [key, value] of Object.entries(tree)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (typeof value === 'string') out.set(path, value)
    else for (const [k, v] of flatten(value, path)) out.set(k, v)
  }
  return out
}

const english = flatten(en as Tree)

// {{interpolation}}, {NamingToken} and `code spans` must survive translation
// untouched; a renamed placeholder renders as literal braces.
const placeholders = (text: string) => [...text.matchAll(/{{\s*(\w+)\s*}}/g)].map(m => m[1]).sort()
const namingTokens = (text: string) => [...text.replace(/{{[^}]*}}/g, '').matchAll(/{[A-Za-z][\w ]*(?::[^}]*)?}/g)].map(m => m[0]).sort()
const codeSpans = (text: string) => [...text.matchAll(/`[^`]*`/g)].map(m => m[0]).sort()

// Words a correct translation legitimately shares with English: product,
// protocol and format names, plus the handful of ordinary words Norwegian or
// Swedish spell the same way. A value identical to English passes only when
// every word in it is on this list, so a whole untranslated sentence cannot.
const SHARED_WORDS = new Set(
  `Bindery Hardcover OpenLibrary Open Library Google Books Audible Audnexus OPDS OPF EPUB MOBI AZW3 PDF M4B MP3
   Prowlarr Jackett Readarr Sonarr Radarr Lidarr SABnzbd NZBGet qBittorrent Transmission Deluge rTorrent Torznab
   Newznab Calibre Audiobookshelf Grimmory Kavita Komga OIDC OAuth SSO Discord Slack Telegram ntfy Apprise Gotify
   Pushover Home Assistant Webhook Usenet ISBN ISBN10 ISBN13 ASIN URL API JSON CSV ID UUID HTTP HTTPS DNS IP
   OK Torrent Goodreads Amazon Docker Prometheus Grafana Teams Matrix Email SMTP Kobo Kindle KOReader Bearer
   Token Client Secret Issuer Debug Info Warn Error Trace Auto Commit Global`.split(/\s+/).filter(Boolean).map(w => w.toLowerCase()),
)
const SHARED_BY_LOCALE: Record<string, Set<string>> = {
  nb: new Set('import status format filter serie serier logo type test proxy port host admin server'.split(' ')),
  sv: new Set('import status format filter serie serier logo version proxy port host admin server'.split(' ')),
}

function onlySharedWords(locale: string, text: string): boolean {
  // A literal path or URL, such as a placeholder showing the expected shape.
  if (/^(\/|https?:\/\/)\S*$/.test(text)) return true
  const words = text.replace(/{{[^}]*}}|{[^}]*}|`[^`]*`/g, ' ').match(/\p{L}[\p{L}\d'.-]*/gu) ?? []
  return words.every(w => {
    const bare = w.replace(/[.'-]+$/, '').toLowerCase()
    return SHARED_WORDS.has(bare) || SHARED_BY_LOCALE[locale].has(bare)
  })
}

const locales: [string, Tree][] = [['nb', nb as Tree], ['sv', sv as Tree]]

describe.each(locales)('%s locale', (locale, bundle) => {
  const translated = flatten(bundle)

  it('only uses keys that exist in en.json', () => {
    const unknown = [...translated.keys()].filter(key => !english.has(key))
    expect(unknown).toEqual([])
  })

  it('keeps every placeholder, naming token and code span', () => {
    const broken: string[] = []
    for (const [key, value] of translated) {
      const source = english.get(key)
      if (source === undefined) continue
      if (value.trim() === '') broken.push(`${key}: empty`)
      if (placeholders(value).join() !== placeholders(source).join()) broken.push(`${key}: {{placeholders}} ${placeholders(value)} vs ${placeholders(source)}`)
      if (namingTokens(value).join() !== namingTokens(source).join()) broken.push(`${key}: tokens ${namingTokens(value)} vs ${namingTokens(source)}`)
      if (codeSpans(value).join() !== codeSpans(source).join()) broken.push(`${key}: code spans differ`)
    }
    expect(broken).toEqual([])
  })

  it('supplies both plural forms together', () => {
    const unpaired = [...translated.keys()].filter(key => {
      const match = /^(.*)_(one|other)$/.exec(key)
      if (!match) return false
      return !translated.has(`${match[1]}_${match[2] === 'one' ? 'other' : 'one'}`)
    })
    expect(unpaired).toEqual([])
  })

  it('contains no English filler', () => {
    const filler = [...translated].filter(([key, value]) => value === english.get(key) && !onlySharedWords(locale, value)).map(([key, value]) => `${key}: ${value}`)
    expect(filler).toEqual([])
  })
})

describe('language detection', () => {
  afterEach(() => {
    localStorage.removeItem('bindery.lang')
    vi.restoreAllMocks()
    vi.resetModules()
  })

  // Loads a fresh copy of the real i18n setup, so the detector runs exactly as
  // it does on first paint: localStorage first, then the browser's languages.
  async function detect(browserLanguages: string[]) {
    vi.resetModules()
    localStorage.removeItem('bindery.lang')
    vi.spyOn(navigator, 'languages', 'get').mockReturnValue(browserLanguages)
    vi.spyOn(navigator, 'language', 'get').mockReturnValue(browserLanguages[0])
    return loaded()
  }

  // The i18next instance is a singleton that outlives resetModules, so
  // isInitialized is already true from the previous run. Wait on this run's
  // init instead: the locale bundles load asynchronously through the lazy
  // backend, so resolvedLanguage is only settled once it resolves.
  async function loaded() {
    const { default: instance, i18nReady } = await import('./index')
    await i18nReady
    return instance
  }

  it.each([
    ['nb', 'nb'], ['nb-NO', 'nb'], ['no', 'nb'], ['no-NO', 'nb'], ['nn', 'nb'], ['nn-NO', 'nb'],
    ['sv', 'sv'], ['sv-SE', 'sv'], ['sv-FI', 'sv'],
    ['de-DE', 'de'], ['da-DK', 'en'],
  ])('resolves a browser set to %s to %s', async (tag, expected) => {
    const instance = await detect([tag])
    expect(instance.resolvedLanguage).toBe(expected)
    const bundle = { nb, sv, en, de: instance.getResourceBundle('de', 'translation') }[expected] as Tree
    expect(instance.t('nav.wanted')).toBe(flatten(bundle).get('nav.wanted'))
  })

  it('still falls back to English for a key Bokmål does not supply', async () => {
    const instance = await detect(['nn-NO'])
    instance.addResource('en', 'translation', 'test.onlyInEnglish', 'English only')
    expect(instance.t('test.onlyInEnglish')).toBe('English only')
  })

  it('honours an explicit choice over the browser language', async () => {
    vi.resetModules()
    localStorage.setItem('bindery.lang', 'sv')
    vi.spyOn(navigator, 'languages', 'get').mockReturnValue(['nb-NO'])
    const instance = await loaded()
    expect(instance.resolvedLanguage).toBe('sv')
  })
})
