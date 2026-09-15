import { describe, expect, it } from 'vitest'
import { createInstance } from 'i18next'
import i18n from './index'
import en from './locales/en.json'

const locales = Object.entries(i18n.options.resources!)
const english = new Map<string, string>(Object.entries(en.bookRebind))
const placeholders = (text: string) => [...text.matchAll(/{{\s*(\w+)\s*}}/g)].map(match => match[1]).sort()

describe('book rebind translations', () => {
  it.each(locales)('validates supplied translations in %s without requiring completeness', (language, bundle) => {
    const resource = bundle.translation as { bookRebind?: Record<string, string> }
    for (const [key, message] of Object.entries(resource.bookRebind ?? {})) {
      const source = english.get(key)
      expect(source, `${language}.${key}`).toBeDefined()
      expect(message.trim(), `${language}.${key}`).not.toBe('')
      expect(placeholders(message), `${language}.${key}`).toEqual(placeholders(source!))
    }
  })

  it('falls back to English when a locale supplies only some modal strings', async () => {
    const instance = createInstance()
    await instance.init({
      lng: 'fr', fallbackLng: 'en',
      resources: {
        en: { translation: en },
        fr: { translation: { bookRebind: { title: 'Réassocier les métadonnées' } } },
      },
    })
    expect(instance.t('bookRebind.title')).toBe('Réassocier les métadonnées')
    expect(instance.t('bookRebind.conflict')).toBe(en.bookRebind.conflict)
    expect(instance.t('bookRebind.description', { title: 'Example' })).toContain('Example')
  })
})
