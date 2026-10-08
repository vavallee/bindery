import i18n, { type BackendModule, type ResourceKey } from 'i18next'
import { initReactI18next } from 'react-i18next'
import LanguageDetector from 'i18next-browser-languagedetector'

import en from './locales/en.json'

// English is the fallback for every partial locale, so it ships in the main
// bundle. The other locales are split into chunks of their own and fetched
// only when that language is actually in use: together they were about a
// third of the main bundle, and a visitor needs at most one of them.
const localeLoaders = import.meta.glob<ResourceKey>(['./locales/*.json', '!./locales/en.json'], { import: 'default' })

/** Returns the lazy loader for a language code, or undefined when there is no bundle for it. */
export function localeLoader(language: string): (() => Promise<ResourceKey>) | undefined {
  return localeLoaders[`./locales/${language}.json`]
}

// A minimal i18next backend over the chunks above. i18next asks it for every
// code in the resolve chain (for example fr-CA, then fr); a code with no
// bundle answers at once with an empty one, which keeps an English visitor's
// startup synchronous because nothing is ever fetched for them.
export const lazyLocales: BackendModule = {
  type: 'backend',
  init() {},
  read(language, _namespace, callback) {
    const load = localeLoader(language)
    if (!load) {
      callback(null, {})
      return
    }
    load().then(
      data => callback(null, data),
      (err: unknown) => callback(err instanceof Error ? err : new Error(String(err)), false),
    )
  },
}

// Region tags need no mapping: i18next tries `de-DE`, then `de`, so `nb-NO`,
// `sv-SE` and `sv-FI` already land on `nb` and `sv`. Norwegian is the one case
// where the browser's base tag is not the bundle's: `no` is the macrolanguage
// tag some browsers send, and `nn` is Nynorsk, which has no bundle of its own.
// Nynorsk readers read Bokmål routinely (it is most of what Norwegian software
// ships in), so both fall back to `nb` before English. The detected tag itself
// is kept, so dates still format in the reader's own variant.
export const FALLBACK_LNG = {
  no: ['nb', 'en'],
  nn: ['nb', 'en'],
  default: ['en'],
}

// Reads from localStorage key 'bindery.lang' first, then falls back to the
// browser's navigator.language. This mirrors the theme bootstrap so the first
// paint is already in the right language, with no flash of English or of raw
// keys: main.tsx waits for i18nReady before rendering, and changeLanguage()
// resolves only after the new bundle has loaded.
// <html lang> names the language the page is actually in, so screen readers
// pick the right voice and the browser hyphenates and translates correctly.
// index.html ships lang="en"; this keeps it in step with the UI language from
// the first render and on every change. The resolved language is the one with
// translations in use: a browser language with no bundle shows English and
// says so, and fr-CA reads as fr.
export function syncDocumentLang(): void {
  if (typeof document === 'undefined') return
  document.documentElement.lang = i18n.resolvedLanguage || i18n.language || 'en'
}

i18n.on('languageChanged', syncDocumentLang)

export const i18nReady = i18n
  .use(lazyLocales)
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: { translation: en },
    },
    // English is bundled above; the backend supplies everything else.
    partialBundledLanguages: true,
    // `no` and `nn` fall back to `nb` first, see FALLBACK_LNG.
    fallbackLng: FALLBACK_LNG,
    detection: {
      order: ['localStorage', 'navigator'],
      lookupLocalStorage: 'bindery.lang',
      caches: ['localStorage'],
    },
    interpolation: {
      escapeValue: false, // React already escapes output
    },
  })
  .finally(syncDocumentLang)

export default i18n
