import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import LanguageDetector from 'i18next-browser-languagedetector'

import en from './locales/en.json'
import fr from './locales/fr.json'
import de from './locales/de.json'
import nl from './locales/nl.json'
import tl from './locales/tl.json'

// Reads from localStorage key 'bindery.lang' first, then falls back to the
// browser's navigator.language. This mirrors the theme bootstrap so the first
// paint is already in the right language — no flash of English.
i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: { translation: en },
      fr: { translation: fr },
      de: { translation: de },
      nl: { translation: nl },
      tl: { translation: tl },
    },
    fallbackLng: 'en',
    detection: {
      order: ['localStorage', 'navigator'],
      lookupLocalStorage: 'bindery.lang',
      caches: ['localStorage'],
    },
    interpolation: {
      escapeValue: false, // React already escapes output
    },
  })

export default i18n
