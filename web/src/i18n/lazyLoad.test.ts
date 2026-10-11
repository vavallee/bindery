import { afterEach, describe, expect, it, vi } from 'vitest'
import i18n, { i18nReady, lazyLocales, localeLoader } from './index'

// Only English is bundled into the main chunk. Every other locale is a lazy
// chunk that i18next fetches through the lazyLocales backend when the
// language is switched to, and changeLanguage() resolves only once it is in.

afterEach(async () => {
  vi.restoreAllMocks()
  await i18n.changeLanguage('en')
})

describe('lazy locale loading', () => {
  it('bundles English and has a lazy loader for every other locale', async () => {
    await i18nReady
    expect(i18n.hasResourceBundle('en', 'translation')).toBe(true)
    expect(localeLoader('en')).toBeUndefined()
    for (const code of ['fr', 'de', 'es', 'nl', 'tl', 'id', 'ko', 'nb', 'sv']) {
      expect(localeLoader(code), code).toBeTypeOf('function')
    }
  })

  it('needs no fetch for English', async () => {
    await i18nReady
    const read = vi.spyOn(lazyLocales, 'read')
    await i18n.changeLanguage('en')
    expect(read.mock.calls.map(call => call[0])).not.toContain('en')
    expect(i18n.t('common.cancel')).toBe('Cancel')
  })

  it('loads a locale bundle on switching, before the language takes effect', async () => {
    await i18nReady
    expect(i18n.hasResourceBundle('fr', 'translation')).toBe(false)
    const read = vi.spyOn(lazyLocales, 'read')

    await i18n.changeLanguage('fr')

    expect(read.mock.calls.map(call => call[0])).toContain('fr')
    expect(i18n.hasResourceBundle('fr', 'translation')).toBe(true)
    // Translated straight away, never a raw key.
    expect(i18n.t('common.cancel')).toBe('Annuler')
    // A key the partial locale lacks still falls back to English.
    expect(i18n.t('common.gridView')).toBe('Grid view')
  })
})
