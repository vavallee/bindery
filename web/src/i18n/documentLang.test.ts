import { afterEach, describe, expect, it } from 'vitest'
import i18n, { i18nReady } from './index'

// index.html ships <html lang="en">, and it stayed "en" whatever language the
// UI was switched to, so a screen reader read French text with an English
// voice.
afterEach(async () => {
  await i18n.changeLanguage('en')
})

describe('<html lang> follows the UI language', () => {
  it('is set once i18n is ready', async () => {
    // jsdom's document starts with no lang at all. The detector picks its
    // navigator language, en-US, which resolves to the bundled English.
    await i18nReady
    expect(document.documentElement.lang).toBe('en')
  })

  it('changes with the language', async () => {
    await i18nReady
    await i18n.changeLanguage('fr')
    expect(document.documentElement.lang).toBe('fr')
    await i18n.changeLanguage('de')
    expect(document.documentElement.lang).toBe('de')
  })

  it('names the language with translations, not a region the bundles lack', async () => {
    await i18nReady
    await i18n.changeLanguage('fr-CA')
    expect(document.documentElement.lang).toBe('fr')
  })

  it('stays English for a language Bindery has no translation for', async () => {
    await i18nReady
    await i18n.changeLanguage('pt-BR')
    expect(document.documentElement.lang).toBe('en')
  })
})
