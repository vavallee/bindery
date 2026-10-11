// Book.language is meant to hold an ISO 639-2/B code ("eng"), but not every
// path that writes it normalises, so the same language can arrive as "en",
// "en-US", "eng" or "English". These helpers fold the common spellings
// together for display and filtering. The list is deliberately short:
// indexers and metadata providers only reliably tag a few majors, and any
// other value passes through lowercased.
//
// The full tables live in internal/models/language.go, which
// NormalizeLanguageCode reads. A language added here should fold the same way
// there, including the withdrawn two letter codes in iso639LegacyTwoLetter
// ("iw", "in", "ji").
const ALIASES = new Map(Object.entries({
  en: 'eng',
  fr: 'fre', fra: 'fre',
  de: 'ger', deu: 'ger',
  nl: 'dut', nld: 'dut',
  es: 'spa',
  it: 'ita',
  pt: 'por',
  ja: 'jpn',
  zh: 'chi', zho: 'chi',
  ru: 'rus',
  tl: 'tgl',
  id: 'ind', in: 'ind',
  he: 'heb', iw: 'heb',
  yi: 'yid', ji: 'yid',
}))

// Maps rather than plain objects: the value comes from stored metadata, and a
// language of "constructor" must not resolve to something on Object.prototype.
const NAMES = new Map(Object.entries({
  eng: 'English',
  fre: 'French',
  ger: 'German',
  dut: 'Dutch',
  spa: 'Spanish',
  ita: 'Italian',
  por: 'Portuguese',
  jpn: 'Japanese',
  chi: 'Chinese',
  rus: 'Russian',
  tgl: 'Tagalog',
  ind: 'Indonesian',
  heb: 'Hebrew',
  yid: 'Yiddish',
}))

const BY_NAME = new Map(
  Array.from(NAMES, ([code, name]) => [name.toLowerCase(), code] as const),
)

// canonicalLanguage returns the ISO 639-2/B code for any spelling it knows,
// dropping a region or script subtag ("en-US", "pt_BR") first. Empty in,
// empty out.
export function canonicalLanguage(code?: string | null): string {
  const raw = (code ?? '').trim().toLowerCase()
  if (!raw) return ''
  const named = BY_NAME.get(raw)
  if (named) return named
  const base = raw.split(/[-_]/)[0] || raw
  return ALIASES.get(base) ?? base
}

// languageName returns the English name of a language code, or the canonical
// code itself when the language is outside the short list above.
export function languageName(code?: string | null): string | null {
  const canonical = canonicalLanguage(code)
  if (!canonical) return null
  return NAMES.get(canonical) ?? canonical
}
